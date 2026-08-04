package acceptance_test

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
)

var frontendSSEConnect = regexp.MustCompile(`sse-connect="([^"]+)"`)
var votingKeyOutput = regexp.MustCompile(`data-voting-key>([^<]+)</code>`)

func frontendSSEPath(t *testing.T, page string) string {
	t.Helper()
	match := frontendSSEConnect.FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("page has no SSE connection path: %s", page)
	}
	return strings.ReplaceAll(match[1], "&amp;", "&")
}

func readBallotInvalidation(reader *bufio.Reader) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "event: ballot\n" {
			return err
		}
	}
}

func TestVotingKeysIssueRedeemAndSurviveRestart(t *testing.T) {
	const unavailableMessage = "Voting Key unavailable. Check the Event and key."
	csrfToken := func(response frontendResponse) string {
		match := frontendCSRFInput.FindStringSubmatch(response.body)
		if len(match) != 2 {
			t.Fatalf("Voting page lacks CSRF proof: %d %q", response.status, response.body)
		}
		return match[1]
	}
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	otherEvent := validEventInput()
	otherEvent["name"] = "Other Event"
	otherEvent["command_id"] = "create-voting-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		otherEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Other Event\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 1, "role": "Producer", "command_id": "grant-voting-producer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	const password = "voting correct horse battery staple"
	for id, name := range []string{"Alice Voter", "Bob Voter"} {
		assertJSONRequest(
			t, administrator, server.address, "/admin/accounts",
			map[string]string{
				"name": name, "password": password,
				"command_id": "create-voter-" + strconv.Itoa(id+2),
			},
			http.StatusCreated,
			"{\"id\":"+strconv.Itoa(id+2)+",\"name\":\""+name+"\",\"administrator\":false}\n",
		)
	}

	path := "/backstage/events/1/voting-keys"
	page := getFrontendPage(t, administrator, server.address, path)
	commands := frontendNamedValues(page.body, "command_id")
	if page.status != http.StatusOK || len(commands["command_id"]) != 1 {
		t.Fatalf("Voting Key page = %d %q", page.status, page.body)
	}
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load Voting Event timezone: %v", err)
	}
	issued := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {csrfToken(page)},
		"command_id": {commands.Get("command_id")},
		"action":     {"issue"},
		"count":      {"2"},
		"expires_at": {time.Now().Add(24 * time.Hour).In(location).Format("2006-01-02T15:04")},
	})
	matches := votingKeyOutput.FindAllStringSubmatch(issued.body, -1)
	if issued.status != http.StatusOK || len(matches) != 2 {
		t.Fatalf("issued Voting Keys = %d %q", issued.status, issued.body)
	}
	firstKey, secondKey := matches[0][1], matches[1][1]

	database, err := sql.Open("sqlite", filepath.Join(server.dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open Voting database: %v", err)
	}
	var storedHash string
	if err = database.QueryRowContext(
		t.Context(), "SELECT token_hash FROM voting_keys WHERE id = 1",
	).Scan(&storedHash); err != nil {
		t.Fatalf("read protected Voting Key: %v", err)
	}
	if storedHash == firstKey || len(storedHash) != 64 {
		t.Fatalf("stored Voting Key = %q", storedHash)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close Voting database: %v", err)
	}
	for _, route := range []string{"/admin/audit", "/diagnostics"} {
		found := getFrontendPage(t, administrator, server.address, route)
		if strings.Contains(found.body, firstKey) || strings.Contains(found.body, secondKey) {
			t.Fatalf("%s leaked Voting Key: %q", route, found.body)
		}
	}

	signIn := func(name string) *http.Client {
		t.Helper()
		client := authenticatedClient(t)
		client.CheckRedirect = administrator.CheckRedirect
		assertJSONRequest(
			t, client, server.address, "/auth/sign-in",
			map[string]string{"name": name, "password": password},
			http.StatusNoContent, "",
		)
		return client
	}
	alice := signIn("Alice Voter")
	bob := signIn("Bob Voter")

	redeemPage := getFrontendPage(t, alice, server.address, "/voting")
	redeemValues := frontendNamedValues(redeemPage.body, "command_id")
	wrongEvent := postFrontendForm(t, alice, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(redeemPage)},
		"command_id": {redeemValues.Get("command_id")},
		"event_id":   {"2"},
		"voting_key": {firstKey},
	})
	if wrongEvent.status != http.StatusUnprocessableEntity ||
		!strings.Contains(wrongEvent.body, unavailableMessage) {
		t.Fatalf("wrong-Event Voting Key = %d %q", wrongEvent.status, wrongEvent.body)
	}
	assertAccessibleFormErrors(t, wrongEvent, map[string]string{
		"voting-event": unavailableMessage,
		"voting-key":   unavailableMessage,
	})
	if !strings.Contains(wrongEvent.body, `value="2"`) ||
		strings.Contains(wrongEvent.body, firstKey) {
		t.Fatalf("wrong-Event form values = %q", wrongEvent.body)
	}
	redeemValues = frontendNamedValues(wrongEvent.body, "command_id")
	successValues := url.Values{
		"csrf_token": {csrfToken(wrongEvent)},
		"command_id": {redeemValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {firstKey},
	}
	redeemed := postFrontendForm(t, alice, server.address, "/voting", successValues)
	if redeemed.status != http.StatusOK ||
		!strings.Contains(redeemed.body, "Voting Eligibility granted.") {
		t.Fatalf("Voting Key redemption = %d %q", redeemed.status, redeemed.body)
	}
	if replay := postFrontendForm(
		t, alice, server.address, "/voting", successValues,
	); replay.status != http.StatusOK ||
		!strings.Contains(replay.body, "Voting Eligibility granted.") {
		t.Fatalf("Voting Key redemption replay = %d %q", replay.status, replay.body)
	}

	bobPage := getFrontendPage(t, bob, server.address, "/voting")
	bobValues := frontendNamedValues(bobPage.body, "command_id")
	reused := postFrontendForm(t, bob, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(bobPage)},
		"command_id": {bobValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {firstKey},
	})
	if reused.status != wrongEvent.status ||
		!strings.Contains(reused.body, unavailableMessage) {
		t.Fatalf("transferred Voting Key = %d %q", reused.status, reused.body)
	}

	revokeForm := regexp.MustCompile(
		`(?s)name="command_id" value="([^"]+-revoke-2)".*?name="key_id" value="2"`,
	).FindStringSubmatch(issued.body)
	if len(revokeForm) != 2 {
		t.Fatalf("Voting Key revoke form missing: %q", issued.body)
	}
	revoked := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {csrfToken(issued)},
		"command_id": {revokeForm[1]},
		"action":     {"revoke"},
		"key_id":     {"2"},
	})
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke Voting Key = %d %q", revoked.status, revoked.body)
	}
	bobValues = frontendNamedValues(reused.body, "command_id")
	revokedRedemption := postFrontendForm(t, bob, server.address, "/voting", url.Values{
		"csrf_token": {csrfToken(reused)},
		"command_id": {bobValues.Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {secondKey},
	})
	if revokedRedemption.status != wrongEvent.status ||
		!strings.Contains(revokedRedemption.body, unavailableMessage) {
		t.Fatalf("revoked Voting Key = %d %q", revokedRedemption.status, revokedRedemption.body)
	}

	failed := revokedRedemption
	for attempt := range 6 {
		values := frontendNamedValues(failed.body, "command_id")
		failed = postFrontendForm(t, bob, server.address, "/voting", url.Values{
			"csrf_token": {csrfToken(failed)},
			"command_id": {values.Get("command_id")},
			"event_id":   {"1"},
			"voting_key": {"NOT-A-KEY"},
		})
		if attempt < 5 && failed.status != http.StatusUnprocessableEntity {
			t.Fatalf("malformed Voting Key attempt %d = %d %q", attempt+1, failed.status, failed.body)
		}
	}
	if failed.status != http.StatusTooManyRequests {
		t.Fatalf("Voting Key abuse response = %d %q", failed.status, failed.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	database, err = sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("reopen Voting database: %v", err)
	}
	var eligibilityCount int
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM voting_eligibilities WHERE event_id = 1 AND account_id = 2",
	).Scan(&eligibilityCount); err != nil {
		t.Fatalf("read durable Voting Eligibility: %v", err)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatalf("close restarted Voting database: %v", closeErr)
	}
	if eligibilityCount != 1 {
		t.Fatalf("durable Voting Eligibility count = %d, want 1", eligibilityCount)
	}
	server = startBeamersWithPublicListener(t, bin, dataDir)
	if page = getFrontendPage(t, alice, server.address, "/voting"); page.status != http.StatusOK {
		t.Fatalf("restarted Voting page = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestLiveCompetitionBallotUpdatesAndSurvivesRestart(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	settingsPath := "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	configuredEvent := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"publish-live-ballot-event"},
		"expected_event_revision":           {"2"},
		"event_name":                        {"BeamConf 2099"},
		"public":                            {"true"},
		"public_slug":                       {"beamconf-2099"},
		"planned_start_date":                {"2099-08-21"},
		"planned_end_date":                  {"2099-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"en-GB"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Pending"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configuredEvent.status != http.StatusSeeOther {
		t.Fatalf("publish live Ballot Event = %d %q", configuredEvent.status, configuredEvent.body)
	}
	entriesPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"
	page := getFrontendPage(t, administrator, server.address, entriesPath)
	created := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-entry"},
		"command_id": {"create-live-ballot-entry"},
		"entry_name": {"Live Ballot Entry"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("create Ballot Entry = %d %q", created.status, created.body)
	}
	page = getFrontendPage(t, administrator, server.address, entriesPath)
	configured := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"configure-live-ballot-readiness"},
		"expected_readiness_revision": {"0"},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Ballot Competition readiness = %d %q", configured.status, configured.body)
	}

	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Vera Voter", "password": "voter correct horse battery staple",
			"command_id": "create-live-voter",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Vera Voter\",\"administrator\":false}\n",
	)
	keysPath := "/backstage/events/1/voting-keys"
	keysPage := getFrontendPage(t, administrator, server.address, keysPath)
	issued := postFrontendForm(t, administrator, server.address, keysPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, keysPage)},
		"command_id": {frontendNamedValues(keysPage.body, "command_id").Get("command_id")},
		"action":     {"issue"},
		"count":      {"1"},
		"expires_at": {time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")},
	})
	keyMatch := votingKeyOutput.FindStringSubmatch(issued.body)
	if issued.status != http.StatusOK || len(keyMatch) != 2 {
		t.Fatalf("issue live Voting Key = %d %q", issued.status, issued.body)
	}
	voter := authenticatedClient(t)
	voter.CheckRedirect = administrator.CheckRedirect
	assertJSONRequest(
		t, voter, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Vera Voter", "password": "voter correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	votingPage := getFrontendPage(t, voter, server.address, "/voting")
	redeemed := postFrontendForm(t, voter, server.address, "/voting", url.Values{
		"csrf_token": {requireFrontendCSRF(t, votingPage)},
		"command_id": {frontendNamedValues(votingPage.body, "command_id").Get("command_id")},
		"event_id":   {"1"},
		"voting_key": {keyMatch[1]},
	})
	if redeemed.status != http.StatusOK ||
		!strings.Contains(redeemed.body, "Voting Eligibility granted.") {
		t.Fatalf("redeem live Voting Key = %d %q", redeemed.status, redeemed.body)
	}

	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"start-live-ballot-competition"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start live Ballot Competition = %d %q", started.status, started.body)
	}
	votingControlPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/voting"
	controlPage := getFrontendPage(t, administrator, server.address, votingControlPath)
	opened := postFrontendForm(t, administrator, server.address, votingControlPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, controlPage)},
		"command_id":        {"open-live-ballot"},
		"action":            {"open"},
		"expected_revision": {"0"},
	})
	if opened.status != http.StatusSeeOther {
		t.Fatalf("open live Voting Window = %d %q", opened.status, opened.body)
	}
	ineligibleCompetition := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/competitions/"+strconv.FormatInt(competitionID, 10),
	)
	if !strings.Contains(
		ineligibleCompetition.body,
		"Voting is unavailable to this Account.",
	) {
		t.Fatalf(
			"ineligible Competition Voting state = %d %q",
			ineligibleCompetition.status,
			ineligibleCompetition.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, entriesPath)
	claimed := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"claim-control"},
		"command_id":                {"claim-live-ballot-control"},
		"expected_control_revision": {"0"},
	})
	if claimed.status != http.StatusSeeOther {
		t.Fatalf("claim live Ballot Program control = %d %q", claimed.status, claimed.body)
	}
	programClient := connectClient(programv1connect.NewProgramControlServiceClient, administrator, server.address)
	current, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read live Ballot Program Channel: %v", err)
	}
	channel := current.Msg.GetChannel()
	for _, commandID := range []string{"take-live-ballot-upcoming", "take-live-ballot-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance live Ballot Program: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}
	competitionPath := "/events/beamconf-2099/competitions/" +
		strconv.FormatInt(competitionID, 10)
	competitionPage := getFrontendPage(t, voter, server.address, competitionPath)
	ballotPath := frontendLinkPath(t, competitionPage, "Vote")
	if want := "/voting/" + strconv.FormatInt(competitionID, 10) +
		"?event_id=1"; ballotPath != want {
		t.Fatalf("Competition Vote path = %q, want %q", ballotPath, want)
	}
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 0 of 0 presented Entries scored.",
	) {
		t.Fatalf("Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	participationPage := getFrontendPage(t, voter, server.address, "/my-participation")
	if participationPath := frontendLinkPath(
		t,
		participationPage,
		"Vote",
	); participationPath != ballotPath {
		t.Fatalf("My Participation Vote path = %q, want %q", participationPath, ballotPath)
	}
	beforePresentation := getFrontendPage(t, voter, server.address, ballotPath)
	if beforePresentation.status != http.StatusOK ||
		!strings.Contains(beforePresentation.body, "No Entries have been presented yet.") {
		t.Fatalf("pre-presentation Ballot = %d %q", beforePresentation.status, beforePresentation.body)
	}
	streamPath := frontendSSEPath(t, beforePresentation.body)
	streamContext, cancelStream := context.WithCancel(t.Context())
	defer cancelStream()
	streamRequest, err := http.NewRequestWithContext(
		streamContext,
		http.MethodGet,
		"http://"+server.address+streamPath,
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Ballot stream request: %v", err)
	}
	streamResponse, err := voter.Do(streamRequest)
	if err != nil {
		t.Fatalf("open Ballot stream: %v", err)
	}
	defer func() { _ = streamResponse.Body.Close() }()
	if streamResponse.StatusCode != http.StatusOK ||
		streamResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Ballot stream = %d %q", streamResponse.StatusCode, streamResponse.Header)
	}
	notifications := make(chan error, 1)
	go func() {
		notifications <- readBallotInvalidation(bufio.NewReader(streamResponse.Body))
	}()
	order, err := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address).PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview live Ballot Entry Order: %v", err)
	}
	taken, err := programClient.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "take-live-ballot-entry",
			ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
			ExpectedControlStateRevision: channel.GetControlStateRevision(),
			Preview:                      channel.GetPreview(),
			ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
			EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		},
	))
	if err != nil {
		t.Fatalf("present live Ballot Entry: %v", err)
	}
	select {
	case notificationErr := <-notifications:
		if notificationErr != nil {
			t.Fatalf("read Ballot invalidation: %v", notificationErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ballot received no presentation invalidation")
	}
	cancelStream()
	if closeErr := streamResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close Ballot stream: %v", closeErr)
	}

	ballot := getFrontendPage(t, voter, server.address, ballotPath)
	if ballot.status != http.StatusOK || !strings.Contains(ballot.body, "Live Ballot Entry") {
		t.Fatalf("presented Entry Ballot = %d %q", ballot.status, ballot.body)
	}
	voteEntryID := strconv.FormatInt(
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId(),
		10,
	)
	invalidVote := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"cast-invalid-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {voteEntryID},
		"expected_revision": {frontendNamedValues(ballot.body, "expected_revision").Get("expected_revision")},
		"value":             {"9"},
	})
	if invalidVote.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid live Vote = %d %q", invalidVote.status, invalidVote.body)
	}
	assertAccessibleFormErrors(t, invalidVote, map[string]string{
		"vote-" + voteEntryID: "Check the Vote and try again.",
	})
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 0 of 1 presented Entries scored.",
	) {
		t.Fatalf("presented Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	values := frontendNamedValues(ballot.body, "expected_revision")
	voted := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"cast-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {strconv.FormatInt(taken.Msg.GetChannel().GetProgramOutput().GetEntryId(), 10)},
		"expected_revision": {values.Get("expected_revision")},
		"value":             {"5"},
	})
	if voted.status != http.StatusSeeOther {
		t.Fatalf("cast live Vote = %d %q", voted.status, voted.body)
	}
	ballot = getFrontendPage(t, voter, server.address, ballotPath)
	editedVote := postFrontendForm(t, voter, server.address, ballotPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, ballot)},
		"command_id":        {"edit-live-ballot-vote"},
		"event_id":          {"1"},
		"entry_id":          {strconv.FormatInt(taken.Msg.GetChannel().GetProgramOutput().GetEntryId(), 10)},
		"expected_revision": {frontendNamedValues(ballot.body, "expected_revision").Get("expected_revision")},
		"value":             {"4"},
	})
	if editedVote.status != http.StatusSeeOther {
		t.Fatalf("edit live Vote = %d %q", editedVote.status, editedVote.body)
	}
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if !strings.Contains(
		competitionPage.body,
		"Voting is open. 1 of 1 presented Entries scored.",
	) {
		t.Fatalf("scored Competition Voting state = %d %q", competitionPage.status, competitionPage.body)
	}
	controlPage = getFrontendPage(t, administrator, server.address, votingControlPath)
	if !strings.Contains(controlPage.body, "Participating: 1.") ||
		strings.Contains(controlPage.body, "Your score") ||
		strings.Contains(controlPage.body, `name="value"`) {
		t.Fatalf("Crew Voting projection leaked or missed participation: %q", controlPage.body)
	}
	entryID := strconv.FormatInt(
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId(),
		10,
	)
	resultsPath := "/backstage/events/1/results"
	resultsPage := getFrontendPage(t, administrator, server.address, resultsPath)
	savedBeforeTally := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
			"action":                 {"save-results-draft"},
			"command_id":             {"save-pre-tally-results"},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {"0"},
			"disposition":            {"Publish"},
			"score_type":             {"None"},
			"score_visibility":       {"Public"},
			"score_precision":        {"0"},
			"score_requirement":      {"Optional"},
			"score_interpretation":   {"Informational"},
			"standing_entry_id":      {entryID},
			"standing":               {"Placed"},
			"placement":              {"1"},
			"display_order":          {"1"},
			"score":                  {""},
		},
	)
	if savedBeforeTally.status != http.StatusSeeOther {
		t.Fatalf(
			"save pre-Tally Results = %d %q",
			savedBeforeTally.status,
			savedBeforeTally.body,
		)
	}
	resultsPage = getFrontendPage(t, administrator, server.address, resultsPath)
	readyBeforeTally := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		url.Values{
			"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
			"action":                 {"mark-results-ready"},
			"command_id":             {"ready-pre-tally-results"},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {"1"},
		},
	)
	if readyBeforeTally.status != http.StatusSeeOther {
		t.Fatalf(
			"ready pre-Tally Results = %d %q",
			readyBeforeTally.status,
			readyBeforeTally.body,
		)
	}
	closeValues := url.Values{
		"csrf_token":        {requireFrontendCSRF(t, controlPage)},
		"command_id":        {"close-live-ballot"},
		"action":            {"close"},
		"expected_revision": {frontendNamedValues(controlPage.body, "expected_revision").Get("expected_revision")},
	}
	closed := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		closeValues,
	)
	if closed.status != http.StatusSeeOther {
		t.Fatalf("close live Voting Window = %d %q", closed.status, closed.body)
	}
	replayedClose := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		closeValues,
	)
	if replayedClose.status != http.StatusSeeOther {
		t.Fatalf(
			"replay close live Voting Window = %d %q",
			replayedClose.status,
			replayedClose.body,
		)
	}
	conflictingCloseValues := closeValues
	conflictingCloseValues.Set("expected_revision", "0")
	conflictingClose := postFrontendForm(
		t,
		administrator,
		server.address,
		votingControlPath,
		conflictingCloseValues,
	)
	if conflictingClose.status != http.StatusConflict {
		t.Fatalf(
			"conflicting close live Voting Window = %d %q",
			conflictingClose.status,
			conflictingClose.body,
		)
	}
	resultsPage = getFrontendPage(t, administrator, server.address, resultsPath)
	if resultsPage.status != http.StatusOK ||
		!strings.Contains(resultsPage.body, "Draft revision <code>2</code>") ||
		!strings.Contains(resultsPage.body, `data-tone="draft">Not Ready</span>`) ||
		!strings.Contains(resultsPage.body, "Voting Tally: 1") ||
		!strings.Contains(resultsPage.body, "4 total from 1 Ballots") ||
		strings.Contains(resultsPage.body, "Release standalone Results") {
		t.Fatalf("seeded Tally Results = %d %q", resultsPage.status, resultsPage.body)
	}
	overrideValues := url.Values{
		"csrf_token":             {requireFrontendCSRF(t, resultsPage)},
		"action":                 {"save-results-draft"},
		"command_id":             {"override-tally-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"2"},
		"disposition":            {"Publish"},
		"score_type":             {"None"},
		"score_visibility":       {"Public"},
		"score_precision":        {"0"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"Informational"},
		"standing_entry_id":      {entryID},
		"standing":               {"Unplaced"},
		"placement":              {""},
		"display_order":          {"1"},
		"score":                  {""},
	}
	rejectedOverride := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		overrideValues,
	)
	if rejectedOverride.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"reasonless Tally override = %d %q",
			rejectedOverride.status,
			rejectedOverride.body,
		)
	}
	overrideValues.Set("tally_override_reason", "Producer resolved the tied review.")
	savedOverride := postFrontendForm(
		t,
		administrator,
		server.address,
		resultsPath,
		overrideValues,
	)
	if savedOverride.status != http.StatusSeeOther {
		t.Fatalf(
			"save Tally override = %d %q",
			savedOverride.status,
			savedOverride.body,
		)
	}
	competitionPage = getFrontendPage(t, voter, server.address, competitionPath)
	if got := frontendLinkPath(t, competitionPage, "View Ballot"); got != ballotPath ||
		!strings.Contains(competitionPage.body, "Voting is closed.") {
		t.Fatalf(
			"closed Competition Ballot = %d path %q %q",
			competitionPage.status,
			got,
			competitionPage.body,
		)
	}
	readOnly := getFrontendPage(t, voter, server.address, ballotPath)
	if !strings.Contains(readOnly.body, "Voting is closed.") ||
		!strings.Contains(readOnly.body, "Your score: 4") {
		t.Fatalf("closed private Ballot = %d %q", readOnly.status, readOnly.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	recoveryContext, cancelRecovery := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelRecovery()
	recoveryRequest, err := http.NewRequestWithContext(
		recoveryContext, http.MethodGet, "http://"+server.address+streamPath, http.NoBody,
	)
	if err != nil {
		t.Fatalf("create restarted Ballot stream request: %v", err)
	}
	recoveryResponse, err := voter.Do(recoveryRequest)
	if err != nil {
		t.Fatalf("open restarted Ballot stream: %v", err)
	}
	if err = readBallotInvalidation(bufio.NewReader(recoveryResponse.Body)); err != nil {
		t.Fatalf("read restarted Ballot invalidation: %v", err)
	}
	if closeErr := recoveryResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close restarted Ballot stream: %v", closeErr)
	}
	restarted := getFrontendPage(t, voter, server.address, ballotPath)
	if restarted.status != http.StatusOK ||
		!strings.Contains(restarted.body, "Voting is closed.") ||
		!strings.Contains(restarted.body, "Your score: 4") {
		t.Fatalf("restarted private Ballot = %d %q", restarted.status, restarted.body)
	}
	restartedResults := getFrontendPage(t, administrator, server.address, resultsPath)
	if restartedResults.status != http.StatusOK ||
		!strings.Contains(restartedResults.body, "Draft revision <code>3</code>") ||
		!strings.Contains(restartedResults.body, "Voting Tally: 1") ||
		!strings.Contains(
			restartedResults.body,
			"Producer resolved the tied review.",
		) ||
		strings.Contains(restartedResults.body, "Release standalone Results") {
		t.Fatalf(
			"restarted Tally Results = %d %q",
			restartedResults.status,
			restartedResults.body,
		)
	}
	server.stop(t)
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open Tally database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var tallyCount, publicationCount int
	var tallyEntries, overrideReason string
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*), entries FROM voting_tallies WHERE competition_session_id = ?",
		competitionID,
	).Scan(&tallyCount, &tallyEntries); err != nil {
		t.Fatalf("read durable Voting Tally: %v", err)
	}
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM results_publications WHERE scope_session_id = ?",
		competitionID,
	).Scan(&publicationCount); err != nil {
		t.Fatalf("count isolated Results publications: %v", err)
	}
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT reason FROM audit_entries "+
			"WHERE action = 'SaveCompetitionResultsDraft' AND reason <> '' "+
			"ORDER BY id DESC LIMIT 1",
	).Scan(&overrideReason); err != nil {
		t.Fatalf("read Tally override Audit Entry: %v", err)
	}
	if tallyCount != 1 ||
		strings.Contains(tallyEntries, "account") ||
		publicationCount != 0 ||
		overrideReason != "Producer resolved the tied review." {
		t.Fatalf(
			"Tally persistence = count %d entries %q publications %d audit %q",
			tallyCount,
			tallyEntries,
			publicationCount,
			overrideReason,
		)
	}
}

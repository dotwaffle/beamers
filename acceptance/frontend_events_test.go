package acceptance_test

import (
	"fmt"
	"html"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestBrowserPublishesEventsUnderCurrentSlugs(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	second := validEventInput()
	second["name"] = "Summer Showcase"
	second["command_id"] = "create-public-event-2"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events", second,
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Summer Showcase\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/2/grants",
		map[string]any{
			"account_id": 1, "role": "Producer",
			"command_id": "grant-public-event-2",
		},
		http.StatusCreated,
		"{\"event_id\":2,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	draft := validEventInput()
	draft["name"] = "Secret Draft"
	draft["command_id"] = "create-private-event-3"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events", draft,
		http.StatusCreated,
		"{\"id\":3,\"name\":\"Secret Draft\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)

	submitEvent := func(eventID int, name, slug, start, end, locale string, listed bool) frontendResponse {
		t.Helper()
		path := "/backstage/events/" + strconv.Itoa(eventID) + "/settings"
		page := getFrontendPage(t, administrator, server.address, path)
		values := frontendNamedValues(
			page.body,
			"command_id",
			"expected_event_revision",
		)
		values.Set("csrf_token", requireFrontendCSRF(t, page))
		values.Set("event_name", name)
		values.Set("public_slug", slug)
		values.Set("planned_start_date", start)
		values.Set("planned_end_date", end)
		values.Set("timezone", "Europe/Berlin")
		values.Set("event_locale", locale)
		values.Set("content_language", "en-GB")
		values.Set("event_day_boundary", "06:00")
		values.Set("entry_default_disposition", "Pending")
		values.Set("submission_eligibility", "AllAccounts")
		values.Set("voting_method", "Range1To5")
		values.Set("self_vote_policy", "Allowed")
		values.Set("target_adjustment_presets_seconds", "-300,300,600")
		if listed {
			values.Set("public", "true")
		}
		return postFrontendForm(t, administrator, server.address, path, values)
	}
	setListed := func(eventID int, name, slug, start, end, locale string, listed bool) {
		t.Helper()
		saved := submitEvent(eventID, name, slug, start, end, locale, listed)
		if saved.status != http.StatusSeeOther {
			t.Fatalf("set Event %d listing to %t = %d %q", eventID, listed, saved.status, saved.body)
		}
	}
	setListed(1, "BeamConf 2099", "beamconf-2099", "2099-08-21", "2099-08-23", "en-GB", true)
	setListed(2, "Summer Showcase", "summer-private", "2026-08-21", "2026-08-23", "de-DE", false)
	setListed(2, "Summer Showcase", "summer-showcase", "2026-08-21", "2026-08-23", "de-DE", true)

	root := getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	for _, want := range []string{
		"Featured Event",
		`href="/events/beamconf-2099"`,
		"BeamConf 2099",
		`href="/events/summer-showcase"`,
		"Summer Showcase",
	} {
		if root.status != http.StatusOK || !strings.Contains(root.body, want) {
			t.Fatalf("Public Event Listing lacks %q: %d %q", want, root.status, root.body)
		}
	}
	if strings.Contains(root.body, "Secret Draft") {
		t.Fatalf("Public Event Listing disclosed Draft Event: %q", root.body)
	}
	privateSlug := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/summer-private",
	)
	if privateSlug.status != http.StatusNotFound {
		t.Fatalf("never-public Event Slug = %d %q", privateSlug.status, privateSlug.body)
	}
	for path, name := range map[string]string{
		"/events/beamconf-2099":   "BeamConf 2099",
		"/events/summer-showcase": "Summer Showcase",
	} {
		page := getFrontendPage(t, authenticatedClient(t), server.publicAddress, path)
		if page.status != http.StatusOK || !strings.Contains(page.body, name) {
			t.Fatalf("public Event %s = %d %q", path, page.status, page.body)
		}
	}
	private := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/secret-draft",
	)
	unknown := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/unknown-event",
	)
	if private.status != http.StatusNotFound ||
		private.status != unknown.status ||
		private.body != unknown.body {
		t.Fatalf(
			"private and unknown Events differ: private=%d %q unknown=%d %q",
			private.status, private.body, unknown.status, unknown.body,
		)
	}

	setListed(2, "Summer Showcase", "summer-stage", "2026-08-21", "2026-08-23", "de-DE", true)
	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", true)
	publicClient := authenticatedClient(t)
	publicClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for _, alias := range []string{"summer-showcase", "summer-stage"} {
		for _, suffix := range []string{"", "/schedule", "/competitions", "/results"} {
			redirect := getFrontendPage(
				t,
				publicClient,
				server.publicAddress,
				"/events/"+alias+suffix,
			)
			if redirect.status != http.StatusFound ||
				redirect.header.Get("Location") != "/events/summer-final"+suffix {
				t.Fatalf(
					"Event Slug Alias %q = %d Location %q",
					alias+suffix,
					redirect.status,
					redirect.header.Get("Location"),
				)
			}
		}
	}
	collision := submitEvent(
		1,
		"BeamConf 2099",
		"summer-stage",
		"2099-08-21",
		"2099-08-23",
		"en-GB",
		true,
	)
	if collision.status != http.StatusConflict ||
		!strings.Contains(collision.body, "Event Slug is already in use.") {
		t.Fatalf("retained Event Slug collision = %d %q", collision.status, collision.body)
	}

	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", false)
	privateAlias := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/summer-stage",
	)
	unknownAlias := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/unknown-alias",
	)
	if privateAlias.status != http.StatusNotFound ||
		privateAlias.status != unknownAlias.status ||
		privateAlias.body != unknownAlias.body {
		t.Fatalf(
			"private and unknown Event Slug Aliases differ: private=%d %q unknown=%d %q",
			privateAlias.status,
			privateAlias.body,
			unknownAlias.status,
			unknownAlias.body,
		)
	}
	setListed(2, "Summer Showcase", "summer-final", "2026-08-21", "2026-08-23", "de-DE", true)

	administration := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	aliasForm := regexp.MustCompile(
		`name="action" value="prune-event-slug-alias">\s*` +
			`<input type="hidden" name="command_id" value="([^"]+)">\s*` +
			`<input type="hidden" name="alias_id" value="([0-9]+)">`,
	).FindStringSubmatch(administration.body)
	if len(aliasForm) != 3 {
		t.Fatalf("Event Slug Alias prune form missing: %q", administration.body)
	}
	pruneValues := url.Values{
		"csrf_token": {requireFrontendCSRF(t, administration)},
		"action":     {"prune-event-slug-alias"},
		"command_id": {aliasForm[1]},
		"alias_id":   {aliasForm[2]},
	}
	unconfirmed := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/administration",
		pruneValues,
	)
	if unconfirmed.status != http.StatusUnprocessableEntity ||
		!strings.Contains(unconfirmed.body, "Confirm the Event Slug Alias pruning warning.") {
		t.Fatalf("unconfirmed Event Slug Alias pruning = %d %q", unconfirmed.status, unconfirmed.body)
	}
	administration = getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	aliasForm = regexp.MustCompile(
		`name="action" value="prune-event-slug-alias">\s*` +
			`<input type="hidden" name="command_id" value="([^"]+)">\s*` +
			`<input type="hidden" name="alias_id" value="([0-9]+)">`,
	).FindStringSubmatch(administration.body)
	pruneValues = url.Values{
		"csrf_token": {requireFrontendCSRF(t, administration)},
		"action":     {"prune-event-slug-alias"},
		"command_id": {aliasForm[1]},
		"alias_id":   {aliasForm[2]},
		"confirmed":  {"true"},
	}
	pruned := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/administration",
		pruneValues,
	)
	if pruned.status != http.StatusSeeOther {
		t.Fatalf("prune Event Slug Alias = %d %q", pruned.status, pruned.body)
	}
	setListed(2, "Summer Showcase", "summer-showcase", "2026-08-21", "2026-08-23", "de-DE", true)

	root = getFrontendPage(t, administrator, server.address, "/")
	denied := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/3/settings",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, root)},
			"public":     {"true"},
		},
	)
	if denied.status != http.StatusNotFound {
		t.Fatalf("Administrator published without Event Grant: %d %q", denied.status, denied.body)
	}

	setListed(1, "BeamConf 2099", "beamconf-2099", "2099-08-21", "2099-08-23", "en-GB", false)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if strings.Contains(root.body, "BeamConf 2099") ||
		!strings.Contains(root.body, "Summer Showcase") {
		t.Fatalf("Public Event Listing followed Active Event instead of Producer state: %q", root.body)
	}
	if hidden := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099",
	); hidden.status != http.StatusNotFound {
		t.Fatalf("unlisted Active Event = %d %q", hidden.status, hidden.body)
	}
	for _, suffix := range []string{"/schedule", "/competitions", "/results"} {
		if hidden := getFrontendPage(
			t,
			authenticatedClient(t),
			server.publicAddress,
			"/events/beamconf-2099"+suffix,
		); hidden.status != http.StatusNotFound {
			t.Fatalf("unlisted Active Event%s = %d %q", suffix, hidden.status, hidden.body)
		}
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	root = getFrontendPage(t, authenticatedClient(t), server.publicAddress, "/")
	if !strings.Contains(root.body, `href="/events/summer-showcase"`) ||
		strings.Contains(root.body, "BeamConf 2099") {
		t.Fatalf("restarted Public Event Listing = %d %q", root.status, root.body)
	}
	for _, alias := range []string{"summer-stage", "summer-final"} {
		redirect := getFrontendPage(
			t,
			publicClient,
			server.publicAddress,
			"/events/"+alias,
		)
		if redirect.status != http.StatusFound ||
			redirect.header.Get("Location") != "/events/summer-showcase" {
			t.Fatalf(
				"restarted Event Slug Alias %q = %d Location %q",
				alias,
				redirect.status,
				redirect.header.Get("Location"),
			)
		}
	}
	server.stop(t)
}

func TestBrowserFollowsCanonicalPublicEventJourney(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	presentationID := prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	settings := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/settings",
	)
	values := frontendNamedValues(
		settings.body,
		"command_id",
		"expected_event_revision",
	)
	values.Set("csrf_token", requireFrontendCSRF(t, settings))
	values.Set("event_name", "BeamConf 2099")
	values.Set("public_slug", "beamconf-2099")
	values.Set("planned_start_date", "2099-08-21")
	values.Set("planned_end_date", "2099-08-23")
	values.Set("timezone", "Europe/Berlin")
	values.Set("event_locale", "en-GB")
	values.Set("content_language", "en-GB")
	values.Set("event_day_boundary", "06:00")
	values.Set("entry_default_disposition", "Pending")
	values.Set("submission_eligibility", "AllAccounts")
	values.Set("voting_method", "Range1To5")
	values.Set("self_vote_policy", "Allowed")
	values.Set("target_adjustment_presets_seconds", "-300,300,600")
	values.Set("public", "true")
	if published := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/1/settings",
		values,
	); published.status != http.StatusSeeOther {
		t.Fatalf("publish Event = %d %q", published.status, published.body)
	}

	emptyCompetitions := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/competitions",
	)
	if emptyCompetitions.status != http.StatusOK ||
		!strings.Contains(
			emptyCompetitions.body,
			"No public Competitions are available yet.",
		) {
		t.Fatalf(
			"empty canonical Competitions = %d %q",
			emptyCompetitions.status,
			emptyCompetitions.body,
		)
	}
	competitionID, _ := addCompetitionSession(t, administrator, server)

	root := getFrontendPage(t, administrator, server.address, "/")
	if !strings.Contains(root.body, `href="/events/beamconf-2099"`) {
		t.Fatalf("root has no public Event journey: %q", root.body)
	}
	assertFrontendPrimaryNavigation(t, root, true)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, "/profile"),
		true,
	)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, "/my-participation"),
		true,
	)
	hubPath := frontendLinkPath(t, root, "BeamConf 2099")
	hub := getFrontendPage(
		t,
		administrator,
		server.address,
		hubPath,
	)
	for _, want := range []string{
		"Ada Admin",
		"2099-08-21",
		"2099-08-23",
		`href="/events/beamconf-2099/schedule"`,
		`href="/events/beamconf-2099/competitions"`,
		`href="/events/beamconf-2099/results"`,
	} {
		if hub.status != http.StatusOK || !strings.Contains(hub.body, want) {
			t.Fatalf("public Event hub lacks %q: %d %q", want, hub.status, hub.body)
		}
	}
	assertFrontendPrimaryNavigation(t, hub, true)
	assertFrontendEventShell(
		t,
		hub,
		hubPath,
		"Events",
		"BeamConf 2099",
	)

	publicClient := authenticatedClient(t)
	publicClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	schedule := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		frontendLinkPath(t, hub, "Event Schedule"),
	)
	if schedule.status != http.StatusOK ||
		!strings.Contains(schedule.body, "Opening Keynote") ||
		!strings.Contains(
			schedule.body,
			`href="/events/beamconf-2099/schedule/sessions/`,
		) ||
		strings.Contains(schedule.body, "Private Soundcheck") {
		t.Fatalf("canonical Event Schedule = %d %q", schedule.status, schedule.body)
	}
	if got := schedule.header.Get("Cache-Control"); got != "public, max-age=15, must-revalidate" {
		t.Errorf("canonical Event Schedule Cache-Control = %q", got)
	}
	assertFrontendSignedOutNavigation(t, schedule)
	signedInSchedule := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/schedule",
	)
	assertFrontendPrimaryNavigation(t, signedInSchedule, true)
	assertFrontendEventShell(
		t,
		signedInSchedule,
		"/events/beamconf-2099/schedule",
		"Events",
		"BeamConf 2099",
		"Schedule",
	)
	publicListenerSchedule := getFrontendPage(
		t,
		administrator,
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	assertFrontendPrimaryNavigation(t, publicListenerSchedule, false)
	sessionPath := frontendLinkPath(t, schedule, "Opening Keynote")
	sessionPage := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		sessionPath,
	)
	for _, want := range []string{
		"Welcome to BeamConf 2099",
		"Original Speaker",
		"Main Hall",
		"Status: Scheduled",
		`lang="en-GB"`,
		`data-locale="en-GB"`,
		`href="/assets/events/1/theme.css"`,
	} {
		if sessionPage.status != http.StatusOK || !strings.Contains(sessionPage.body, want) {
			t.Fatalf("canonical Session lacks %q: %d %q", want, sessionPage.status, sessionPage.body)
		}
	}
	if strings.Contains(sessionPage.body, "Call Pat") {
		t.Fatalf("canonical Session leaked Crew Notes: %q", sessionPage.body)
	}
	assertFrontendEventShell(
		t,
		sessionPage,
		"/events/beamconf-2099/schedule",
		"Events",
		"BeamConf 2099",
		"Schedule",
		"Opening Keynote",
	)
	initialSessionETag := sessionPage.header.Get("ETag")
	publicPresentationVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Presentation",
			"target_id":   strconv.FormatInt(presentationID, 10),
			"name":        "Keynote slides",
			"command_id":  "upload-public-presentation-file",
		},
		"slides.txt",
		"text/plain",
		[]byte("slides"),
	))
	crewPresentationVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Presentation",
			"target_id":   strconv.FormatInt(presentationID, 10),
			"name":        "Speaker notes",
			"command_id":  "upload-crew-presentation-file",
			"crew_only":   "true",
		},
		"notes.txt",
		"text/plain",
		[]byte("notes"),
	))
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	if strings.Contains(sessionPage.body, "slides.txt") ||
		strings.Contains(sessionPage.body, "notes.txt") {
		t.Fatalf("unreleased Session Attachment leaked: %q", sessionPage.body)
	}
	for _, versionID := range []int{publicPresentationVersion.ID, crewPresentationVersion.ID} {
		if err := storetest.MarkAttachmentVersionFinal(
			t.Context(),
			filepath.Join(server.dataDir, "beamers.db"),
			versionID,
		); err != nil {
			t.Fatalf("finalize Presentation Attachment %d: %v", versionID, err)
		}
	}
	if configured := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnLive", "expected_revision": 0,
			"command_id": "configure-public-page-release",
		},
	); configured.status != http.StatusOK {
		t.Fatalf("configure Event Attachment release = %d %q", configured.status, configured.body)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	startedPresentation, err := sessionClient.StartSession(
		t.Context(),
		connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: presentationID,
			CommandId:                 "start-presentation-page-release",
			ExpectedLiveStateRevision: proto.Int64(0),
		}),
	)
	if err != nil {
		t.Fatalf("start Presentation for Attachment release: %v", err)
	}
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	for _, want := range []string{
		"Keynote slides (slides.txt)",
		fmt.Sprintf(`href="/public/attachments/%d"`, publicPresentationVersion.ID),
	} {
		if sessionPage.status != http.StatusOK || !strings.Contains(sessionPage.body, want) {
			t.Fatalf("released Session lacks %q: %d %q", want, sessionPage.status, sessionPage.body)
		}
	}
	if got := sessionPage.header.Get("ETag"); got == "" || got == initialSessionETag {
		t.Fatalf("released Session ETag = %q, initial %q", got, initialSessionETag)
	}
	if strings.Contains(sessionPage.body, "Speaker notes") ||
		strings.Contains(sessionPage.body, "notes.txt") {
		t.Fatalf("Crew Only Session Attachment leaked: %q", sessionPage.body)
	}
	if held := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		fmt.Sprintf(
			"/crew/events/1/attachment-versions/%d/release",
			publicPresentationVersion.ID,
		),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-presentation-page-file",
		},
	); held.status != http.StatusOK {
		t.Fatalf("hold Session Attachment = %d %q", held.status, held.body)
	}
	sessionPage = getFrontendPage(t, publicClient, server.publicAddress, sessionPath)
	if strings.Contains(sessionPage.body, "slides.txt") {
		t.Fatalf("held Session Attachment leaked: %q", sessionPage.body)
	}
	crewOnly := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/2",
	)
	unknown := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/999999",
	)
	if crewOnly.status != http.StatusNotFound ||
		unknown.status != http.StatusNotFound ||
		crewOnly.body != unknown.body {
		t.Fatalf(
			"safe Session not-found mismatch: Crew Only %d %q, unknown %d %q",
			crewOnly.status,
			crewOnly.body,
			unknown.status,
			unknown.body,
		)
	}
	if _, err = sessionClient.EndSession(
		t.Context(),
		connect.NewRequest(&sessionv1.EndSessionRequest{
			EventId: 1, SessionId: presentationID,
			CommandId: "end-presentation-page-release",
			ExpectedLiveStateRevision: new(
				startedPresentation.Msg.GetState().GetLiveStateRevision(),
			),
		}),
	); err != nil {
		t.Fatalf("end Presentation after Attachment release: %v", err)
	}

	competitions := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		frontendLinkPath(t, hub, "Competitions"),
	)
	if competitions.status != http.StatusOK ||
		!strings.Contains(competitions.body, "Demo Competition") ||
		!strings.Contains(competitions.body, "Projects presented by attendees") ||
		strings.Contains(competitions.body, "Browser Certified Result") {
		t.Fatalf("canonical Competitions index = %d %q", competitions.status, competitions.body)
	}
	assertFrontendEventShell(
		t,
		competitions,
		"/events/beamconf-2099/competitions",
		"Events",
		"BeamConf 2099",
		"Competitions",
	)
	competitionPath := frontendLinkPath(t, competitions, "Demo Competition")
	competition := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if competition.status != http.StatusOK ||
		!strings.Contains(competition.body, "Results have not been published yet.") {
		t.Fatalf("canonical Competition state = %d %q", competition.status, competition.body)
	}
	assertFrontendEventShell(
		t,
		competition,
		"/events/beamconf-2099/competitions",
		"Events",
		"BeamConf 2099",
		"Competitions",
		"Demo Competition",
	)
	assertFrontendPrimaryNavigation(
		t,
		getFrontendPage(t, administrator, server.address, competitionPath),
		true,
	)
	initialCompetitionETag := competition.header.Get("ETag")

	results := getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	if results.status != http.StatusOK ||
		!strings.Contains(results.body, "Results have not been published yet.") {
		t.Fatalf("canonical Event Results state = %d %q", results.status, results.body)
	}
	assertFrontendEventShell(
		t,
		results,
		"/events/beamconf-2099/results",
		"Events",
		"BeamConf 2099",
		"Results",
	)
	entryID := prepareReleasedBrowserResults(t, administrator, server, competitionID)
	publicVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "Public download",
			"command_id":  "upload-public-competition-file",
		},
		"public.txt",
		"text/plain",
		[]byte("public"),
	))
	crewVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "Crew secret",
			"command_id":  "upload-crew-competition-file",
			"crew_only":   "true",
		},
		"crew.txt",
		"text/plain",
		[]byte("crew"),
	))
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if strings.Contains(competition.body, "public.txt") ||
		strings.Contains(competition.body, "crew.txt") {
		t.Fatalf("unreleased Competition Attachment leaked: %q", competition.body)
	}
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	for _, version := range []attachmentVersionResponse{publicVersion, crewVersion} {
		if _, err = competitionClient.SetEntryAttachmentReadiness(
			t.Context(),
			connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
				EventId: 1, SessionId: competitionID, EntryId: entryID,
				AttachmentVersionId: int64(version.ID),
				CommandId:           fmt.Sprintf("finalize-competition-file-%d", version.ID),
				ExpectedRevision:    int64(version.ReadinessRevision),
				Final:               true,
				Primary:             version.ID == publicVersion.ID,
			}),
		); err != nil {
			t.Fatalf("finalize Competition Attachment %d: %v", version.ID, err)
		}
	}
	if _, err = sessionClient.StartSession(
		t.Context(),
		connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:                 "start-competition-page-release",
			ExpectedLiveStateRevision: proto.Int64(0),
		}),
	); err != nil {
		t.Fatalf("start Competition for Attachment release: %v", err)
	}
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	for _, want := range []string{
		fmt.Sprintf(`id="entry-%d"`, entryID),
		"Browser Certified Result",
		"Public download (public.txt)",
		fmt.Sprintf(`href="/public/attachments/%d"`, publicVersion.ID),
		`href="/events/beamconf-2099/results"`,
	} {
		if competition.status != http.StatusOK || !strings.Contains(competition.body, want) {
			t.Fatalf("published Competition lacks %q: %d %q", want, competition.status, competition.body)
		}
	}
	if got := competition.header.Get("ETag"); got == "" || got == initialCompetitionETag {
		t.Fatalf("published Competition ETag = %q, initial %q", got, initialCompetitionETag)
	}
	if strings.Contains(competition.body, "Crew secret") ||
		strings.Contains(competition.body, "crew.txt") {
		t.Fatalf("Crew Only Competition Attachment leaked: %q", competition.body)
	}
	if held := requestJSONMethod(
		t.Context(),
		http.MethodPatch,
		administrator,
		server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-competition-page-file",
		},
	); held.status != http.StatusOK {
		t.Fatalf("hold Competition Attachment = %d %q", held.status, held.body)
	}
	competition = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		competitionPath,
	)
	if strings.Contains(competition.body, "public.txt") {
		t.Fatalf("held Competition Attachment leaked: %q", competition.body)
	}
	results = getFrontendPage(
		t,
		publicClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	if results.status != http.StatusOK ||
		!strings.Contains(results.body, "Browser Certified Result") ||
		!strings.Contains(results.body, `href="/events/beamconf-2099/schedule"`) ||
		!strings.Contains(results.body, `href="/assets/events/1/theme.css"`) ||
		!strings.Contains(results.body, `data-reduced-effects="false"`) ||
		!strings.Contains(results.body, `class="skip-link" href="#main-content"`) {
		t.Fatalf("canonical published Event Results = %d %q", results.status, results.body)
	}
	if strings.Contains(results.body, `href="/schedule"`) {
		t.Fatalf("canonical published Event Results lost Event context: %q", results.body)
	}
	signedInResults := getFrontendPage(
		t,
		administrator,
		server.address,
		"/events/beamconf-2099/results",
	)
	reducedResultsClient := authenticatedClient(t)
	resultsURL, err := url.Parse("http://" + server.publicAddress)
	if err != nil {
		t.Fatalf("parse public Results URL: %v", err)
	}
	reducedResultsClient.Jar.SetCookies(resultsURL, []*http.Cookie{{
		Name: "beamers_reduced_effects", Value: "true",
	}})
	reducedResults := getFrontendPage(
		t,
		reducedResultsClient,
		server.publicAddress,
		"/events/beamconf-2099/results",
	)
	for label, response := range map[string]frontendResponse{
		"signed in":       signedInResults,
		"reduced effects": reducedResults,
	} {
		if response.status != http.StatusOK ||
			response.body != results.body ||
			response.header.Get("ETag") != results.header.Get("ETag") {
			t.Fatalf(
				"%s Results differ from anonymous: %d %q %q",
				label,
				response.status,
				response.header.Get("ETag"),
				response.body,
			)
		}
	}
	if results.header.Get("Cache-Control") != "public, max-age=15, must-revalidate" ||
		results.header.Get("ETag") == "" ||
		results.header.Get("Vary") != "" {
		t.Fatalf("public Results cache headers = %#v", results.header)
	}

	removedPaths := []string{
		"/schedule",
		"/schedule/sessions/" + strconv.FormatInt(presentationID, 10),
		"/submissions",
		"/results/events/1",
		"/results/events/1/standalone/" + strconv.FormatInt(competitionID, 10),
		"/results/events/1/event-awards",
	}
	for _, listener := range []struct {
		name, address string
		client        *http.Client
	}{
		{"Backstage", server.address, administrator},
		{"public", server.publicAddress, publicClient},
	} {
		for _, path := range removedPaths {
			response := getFrontendPage(t, listener.client, listener.address, path)
			assertFrontendRecovery(t, response, http.StatusNotFound, "Page not found")
		}
	}

	for _, path := range []string{
		"/events/unknown/schedule",
		"/events/unknown/competitions",
		"/events/unknown/results",
	} {
		if response := getFrontendPage(
			t,
			publicClient,
			server.publicAddress,
			path,
		); response.status != http.StatusNotFound {
			t.Errorf("%s = %d %q, want 404", path, response.status, response.body)
		}
	}
	server.stop(t)
}

func TestBrowserEventOverviewAndSettings(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 1,
			"role":       "Producer",
			"command_id": "grant-overview-producer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)

	backstage := getFrontendPage(t, administrator, server.address, "/backstage")
	overviewPath := frontendLinkPath(t, backstage, "Event overview")
	overview := getFrontendPage(t, administrator, server.address, overviewPath)
	for _, want := range []string{
		"Revision 2026",
		"Not listed",
		"Not active",
		"No Rundown published",
		"On Ended",
		"Event settings",
		"Plan and publish",
	} {
		if overview.status != http.StatusOK || !strings.Contains(overview.body, want) {
			t.Fatalf("Event overview lacks %q: %d %q", want, overview.status, overview.body)
		}
	}
	if strings.Contains(overview.body, ">Attachment release</a>") {
		t.Fatalf("Event overview links unavailable release controls: %q", overview.body)
	}

	settingsPath := frontendLinkPath(t, overview, "Event settings")
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	invalid := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"browser-invalid-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {""},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"fr"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, `name="content_language" value="fr"`) {
		t.Fatalf("invalid Event settings = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"event-name": "must be 1 to 200 characters without control characters",
	})

	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, invalid)},
		"command_id":                        {"browser-update-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Revision Browser"},
		"public":                            {"true"},
		"public_slug":                       {"revision-browser"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configured.status != http.StatusSeeOther ||
		configured.header.Get("Location") != settingsPath {
		t.Fatalf("Event settings update = %d Location %q %q", configured.status, configured.header.Get("Location"), configured.body)
	}
	settings = getFrontendPage(t, administrator, server.address, settingsPath)
	if !strings.Contains(settings.body, "Revision Browser") ||
		!strings.Contains(frontendBackstageNavigation(t, settings), "Revision Browser") {
		t.Fatalf("updated Event settings = %d %q", settings.status, settings.body)
	}

	stale := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"browser-stale-event-settings"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Stale Event"},
		"public":                            {"true"},
		"public_slug":                       {"stale-event"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Event changed") ||
		!strings.Contains(stale.body, `value="Stale Event"`) {
		t.Fatalf("stale Event settings = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	planning := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/planning",
	)
	if planning.status != http.StatusOK ||
		strings.Contains(planning.body, `name="event_name"`) {
		t.Fatalf("Planning retained general Event settings: %d %q", planning.status, planning.body)
	}
	for _, path := range []string{overviewPath, settingsPath} {
		if public := getFrontendPage(
			t, administrator, server.publicAddress, path,
		); public.status != http.StatusNotFound {
			t.Fatalf("public-listener %s = %d, want 404", path, public.status)
		}
	}
	server.stop(t)
}

func TestBrowserControlsEventAttachmentRelease(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	rundown, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("load release cue Ceremony: %v", err)
	}
	var ceremonyID int64
	for _, session := range rundown.Msg.GetSessions() {
		if session.GetType() == rundownv1.SessionType_SESSION_TYPE_CEREMONY {
			ceremonyID = session.GetId()
			break
		}
	}
	if ceremonyID == 0 {
		t.Fatal("release cue Ceremony not found")
	}

	const settingsPath = "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	for _, want := range []string{"Attachment release", "On Live", "Closing Session"} {
		if settings.status != http.StatusOK || !strings.Contains(settings.body, want) {
			t.Fatalf("Event release settings lack %q: %d %q", want, settings.status, settings.body)
		}
	}
	invalid := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, settings)},
		"action":                    {"configure-attachment-release"},
		"command_id":                {"browser-invalid-event-release"},
		"expected_release_revision": {"1"},
		"release_policy":            {"Later"},
		"cue_session_id":            {strconv.FormatInt(ceremonyID, 10)},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, `value="`+strconv.FormatInt(ceremonyID, 10)+`" selected`) {
		t.Fatalf("invalid Event Attachment release = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"release-policy": "Check the Attachment release settings and confirmation.",
	})

	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, settings)},
		"action":                    {"configure-attachment-release"},
		"command_id":                {"browser-configure-event-release"},
		"expected_release_revision": {"1"},
		"release_policy":            {"OnEventReleaseCue"},
		"cue_session_id":            {strconv.FormatInt(ceremonyID, 10)},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Event Attachment release = %d %q", configured.status, configured.body)
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)

	preview := getFrontendPage(t, administrator, server.address, settingsPath)
	for _, want := range []string{
		"On Event Release Cue",
		"Cue not fired",
		"Eligible Final Versions",
		">1</dd>",
		"Held Final Versions",
		">0</dd>",
		"Blocked Final Versions",
	} {
		if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
			t.Fatalf("Event release preview lacks %q: %d %q", want, preview.status, preview.body)
		}
	}
	if strings.Contains(preview.body, "Competition override") {
		t.Fatalf("Event settings expose Competition override editing: %q", preview.body)
	}
	releaseValues := frontendNamedValues(
		preview.body,
		"expected_release_revision",
		"preview_fingerprint",
	)
	staleFingerprint := releaseValues.Get("preview_fingerprint")
	held := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-versions/"+
			strconv.Itoa(fixture.publicVersion.ID)+"/release",
		map[string]any{
			"expected_revision": fixture.publicVersion.ReleaseRevision,
			"hold":              true, "command_id": "hold-browser-release-preview",
		},
	)
	if held.status != http.StatusOK {
		t.Fatalf("hold previewed Attachment = %d %q", held.status, held.body)
	}
	stale := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-stale-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {staleFingerprint},
		"confirmed":                 {"true"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Release impact changed") {
		t.Fatalf("stale Event Release Cue = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)
	if regexp.MustCompile(`<input[^>]+id="release-cue-confirmed"[^>]+checked`).MatchString(stale.body) {
		t.Fatalf("stale Event Release Cue retained confirmation: %q", stale.body)
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)

	lifted := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-versions/"+
			strconv.Itoa(fixture.publicVersion.ID)+"/release",
		map[string]any{
			"expected_revision": fixture.publicVersion.ReleaseRevision + 1,
			"hold":              false, "command_id": "lift-browser-release-preview",
		},
	)
	if lifted.status != http.StatusOK {
		t.Fatalf("lift previewed Attachment hold = %d %q", lifted.status, lifted.body)
	}
	preview = getFrontendPage(t, administrator, server.address, settingsPath)
	releaseValues = frontendNamedValues(
		preview.body,
		"expected_release_revision",
		"preview_fingerprint",
	)
	unconfirmed := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-unconfirmed-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {releaseValues.Get("preview_fingerprint")},
	})
	if unconfirmed.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Event Release Cue = %d %q", unconfirmed.status, unconfirmed.body)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"release-cue-confirmed": "Check the Attachment release settings and confirmation.",
	})
	fire := url.Values{
		"csrf_token":                {requireFrontendCSRF(t, preview)},
		"action":                    {"fire-attachment-release-cue"},
		"command_id":                {"browser-fire-release-cue"},
		"expected_release_revision": {releaseValues.Get("expected_release_revision")},
		"preview_fingerprint":       {releaseValues.Get("preview_fingerprint")},
		"confirmed":                 {"true"},
	}
	for attempt := range 2 {
		fired := postFrontendForm(t, administrator, server.address, settingsPath, fire)
		if fired.status != http.StatusSeeOther {
			t.Fatalf("fire Event Release Cue attempt %d = %d %q", attempt+1, fired.status, fired.body)
		}
	}
	assertPublicAttachmentOnListeners(
		t, server, fixture.publicVersion.ID, http.StatusOK, "public release",
	)
	assertPublicAttachmentOnListeners(
		t, server, fixture.crewVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	server.stop(t)
}

func TestBrowserPreflightsAndActivatesEvent(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	secondEvent := validEventInput()
	secondEvent["name"] = "Blocked Event"
	secondEvent["command_id"] = "create-blocked-browser-event"
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		secondEvent, http.StatusCreated,
		"{\"id\":2,\"name\":\"Blocked Event\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	const path = "/backstage/administration"
	page := getFrontendPage(t, administrator, server.address, path)
	blocked := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"preflight"},
		"event_id":   {"2"},
	})
	if blocked.status != http.StatusOK ||
		!strings.Contains(blocked.body, "published_rundown_missing") ||
		strings.Contains(blocked.body, `name="action" value="activate"`) {
		t.Fatalf("blocked browser Activation Preflight = %d %q", blocked.status, blocked.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	preflight := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"preflight"},
		"event_id":   {"1"},
	})
	if preflight.status != http.StatusOK ||
		!strings.Contains(preflight.body, "Activation Preflight") ||
		!strings.Contains(preflight.body, `name="action" value="activate"`) {
		t.Fatalf("browser Activation Preflight = %d %q", preflight.status, preflight.body)
	}
	confirmation := frontendNamedValues(
		preflight.body,
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	confirmation.Set("csrf_token", requireFrontendCSRF(t, preflight))
	confirmation.Set("action", "activate")
	invalidConfirmation := maps.Clone(confirmation)
	invalidConfirmation.Set("command_id", "")
	invalidActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		invalidConfirmation,
	)
	if invalidActivation.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidActivation.body, `role="alert"`) ||
		!strings.Contains(invalidActivation.body, `id="error-summary"`) ||
		!strings.Contains(invalidActivation.body, `name="action" value="activate"`) {
		t.Fatalf(
			"invalid browser Event activation = %d %q",
			invalidActivation.status,
			invalidActivation.body,
		)
	}
	assertAccessibleFormErrors(t, invalidActivation, map[string]string{
		"administration-activation-1-confirmation": "valid command identity",
	})
	correctedConfirmation := frontendActivationFormValues(
		t,
		invalidActivation.body,
		"csrf_token",
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	correctedConfirmation.Set("action", "activate")
	activated := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		correctedConfirmation,
	)
	if activated.status != http.StatusSeeOther {
		t.Fatalf("browser Event activation = %d %q", activated.status, activated.body)
	}
	staleActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		confirmation,
	)
	if staleActivation.status != http.StatusConflict {
		t.Fatalf(
			"stale browser Event activation = %d %q",
			staleActivation.status,
			staleActivation.body,
		)
	}
	assertAccessibleFormErrors(t, staleActivation, map[string]string{
		"administration-activation-1-confirmation": "Preflight is stale",
	})
	refreshedConfirmation := frontendActivationFormValues(
		t,
		staleActivation.body,
		"csrf_token",
		"event_id",
		"event_revision",
		"published_revision",
		"activation_generation",
		"fingerprint",
		"command_id",
	)
	refreshedConfirmation.Set("action", "activate")
	refreshedActivation := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		refreshedConfirmation,
	)
	if refreshedActivation.status != http.StatusSeeOther {
		t.Fatalf(
			"refreshed browser Event activation = %d %q",
			refreshedActivation.status,
			refreshedActivation.body,
		)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Active Event #1") ||
		!strings.Contains(page.body, "generation 3") {
		t.Fatalf("restarted Active Event = %d %q", page.status, page.body)
	}
	server.stop(t)
}

func TestBrowserPlansAndPublishesEvent(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	const path = "/backstage/events/1/planning"
	newEvent := getFrontendPage(t, administrator, server.address, "/backstage/events/new")
	if !regexp.MustCompile(`name="grant_self" value="true" checked`).MatchString(newEvent.body) {
		t.Fatalf("first Event Producer self-grant is not checked: %q", newEvent.body)
	}
	createdEvent := postFrontendForm(
		t,
		administrator,
		server.address,
		"/backstage/events/new",
		url.Values{
			"csrf_token":                        {requireFrontendCSRF(t, newEvent)},
			"command_id":                        {frontendNamedValues(newEvent.body, "command_id").Get("command_id")},
			"grant_command_id":                  {frontendNamedValues(newEvent.body, "grant_command_id").Get("grant_command_id")},
			"event_name":                        {"Revision 2026"},
			"planned_start_date":                {"2026-08-21"},
			"planned_end_date":                  {"2026-08-23"},
			"timezone":                          {"Europe/Berlin"},
			"event_locale":                      {"de-DE"},
			"content_language":                  {"en-GB"},
			"event_day_boundary":                {"06:00"},
			"entry_default_disposition":         {"Pending"},
			"target_adjustment_presets_seconds": {"-300,300,600"},
			"grant_self":                        {"true"},
		},
	)
	if createdEvent.status != http.StatusSeeOther ||
		createdEvent.header.Get("Location") != path {
		t.Fatalf("browser Event creation = %d %q", createdEvent.status, createdEvent.body)
	}
	laterEvent := getFrontendPage(t, administrator, server.address, "/backstage/events/new")
	if regexp.MustCompile(`name="grant_self" value="true" checked`).MatchString(laterEvent.body) {
		t.Fatalf("later Event Producer self-grant remains checked: %q", laterEvent.body)
	}
	administration := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/administration",
	)
	if !strings.Contains(administration.body, "CreateEventGrant") ||
		!strings.Contains(administration.body, "EventGrant #1:1") {
		t.Fatalf("first Event self-grant Audit Entry = %d %q", administration.status, administration.body)
	}
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Plan and publish",
		`name="location_name"`,
		`name="csv_data"`,
		`name="icalendar_data"`,
		"<dt>Draft revision</dt><dd>0</dd>",
		"<dt>Published revision</dt><dd>0</dd>",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("planning page lacks %q: %d %q", want, page.status, page.body)
		}
	}

	settingsPath := "/backstage/events/1/settings"
	settingsPage := getFrontendPage(t, administrator, server.address, settingsPath)
	configured := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settingsPage)},
		"command_id":                        {"browser-update-event"},
		"expected_event_revision":           {"1"},
		"event_name":                        {"Revision Browser"},
		"planned_start_date":                {"2026-08-21"},
		"planned_end_date":                  {"2026-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"de-DE"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Included"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if configured.status != http.StatusSeeOther || configured.header.Get("Location") != settingsPath {
		t.Fatalf("configure Event = %d %q", configured.status, configured.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	invalidDraft := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-invalid-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Safe Hall"},
		"lane_name":               {"Safe Stage"},
		"track_name":              {"Safe Track"},
		"session_title":           {strings.Repeat("x", 201)},
		"session_type":            {"Ceremony"},
		"audience_visibility":     {"Public"},
		"planned_start":           {"2026-08-21T10:00"},
		"planned_end":             {"2026-08-21T10:30"},
		"timing_policy":           {"FixedEnd"},
		"minimum_duration":        {"15m"},
		"start_boundary":          {"Hard"},
		"end_boundary":            {"Soft"},
	})
	if invalidDraft.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidDraft.body, `name="location_name" value="Safe Hall"`) {
		t.Fatalf("invalid Draft structure = %d %q", invalidDraft.status, invalidDraft.body)
	}
	assertAccessibleFormErrors(t, invalidDraft, map[string]string{
		"draft-session-title": "must be 1 to 200 characters without control characters",
	})

	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, invalidDraft)},
		"action":                  {"draft"},
		"command_id":              {"browser-create-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Hall A"},
		"lane_name":               {"Main Stage"},
		"track_name":              {"Demos"},
		"session_title":           {"Opening Ceremony"},
		"session_type":            {"Ceremony"},
		"audience_visibility":     {"Public"},
		"planned_start":           {"2026-08-21T10:00"},
		"planned_end":             {"2026-08-21T10:30"},
		"timing_policy":           {"FixedEnd"},
		"minimum_duration":        {"15m"},
		"start_boundary":          {"Hard"},
		"end_boundary":            {"Soft"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("create Draft structure = %d %q", created.status, created.body)
	}
	if anonymous := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); anonymous.status != http.StatusNotFound {
		t.Fatalf("public-listener planning = %d, want 404", anonymous.status)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>1</dd>") ||
		!strings.Contains(page.body, "Opening Ceremony") {
		t.Fatalf("reviewable Draft = %d %q", page.status, page.body)
	}
	if !strings.Contains(
		page.body,
		`<details><summary><span>Session #1: Opening Ceremony</span>`,
	) {
		t.Fatalf("Session editor is not collapsed behind details/summary: %q", page.body)
	}
	if reviewIndex, materializedIndex := strings.Index(page.body, "<h2>Draft review</h2>"),
		strings.Index(page.body, "<h2>Materialized Draft</h2>"); reviewIndex < 0 ||
		materializedIndex < 0 || reviewIndex > materializedIndex {
		t.Fatalf(
			"Draft review does not precede Materialized Draft: review=%d materialized=%d",
			reviewIndex, materializedIndex,
		)
	}
	for _, want := range []string{
		`<select id="draft-lane-location-id" name="lane_location_id">`,
		`<select id="draft-session-lane-id" name="session_lane_id">`,
		`<select id="draft-session-location-id" name="session_location_id">`,
		`<select id="draft-session-track-id" name="session_track_id">`,
		`<option value="1">Hall A</option>`,
		`<option value="1">Main Stage</option>`,
		`<option value="1">Demos</option>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("current-Draft-structure picker lacks %q: %q", want, page.body)
		}
	}
	invalidSessionTime := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-invalid-session-time"},
		"expected_draft_revision": {"1"},
		"session_id":              {"1"},
		"base_session": {html.UnescapeString(
			frontendNamedValues(page.body, "base_session").Get("base_session"),
		)},
		"session_title":       {"Opening Ceremony"},
		"session_type":        {"Ceremony"},
		"audience_visibility": {"Public"},
		"planned_start":       {"not-a-time"},
		"planned_end":         {"2026-08-21T10:30"},
		"timing_policy":       {"FixedEnd"},
		"minimum_duration":    {"15m"},
		"start_boundary":      {"Hard"},
		"end_boundary":        {"Soft"},
	})
	if invalidSessionTime.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid materialized Session time = %d %q",
			invalidSessionTime.status, invalidSessionTime.body)
	}
	assertAccessibleFormErrors(t, invalidSessionTime, nil)
	sessionStartFieldID := "session-1-planned-start"
	sessionStartZoneID := sessionStartFieldID + "-zone"
	wantDescribedBy := `aria-describedby="` + sessionStartZoneID + " " + sessionStartFieldID + `-error"`
	if !strings.Contains(invalidSessionTime.body, `id="`+sessionStartFieldID+`-error"`) ||
		!strings.Contains(invalidSessionTime.body, wantDescribedBy) {
		t.Fatalf(
			"invalid materialized Session time lacks merged aria-describedby %q: %q",
			wantDescribedBy, invalidSessionTime.body,
		)
	}
	if got := strings.Count(
		invalidSessionTime.body,
		`id="`+sessionStartFieldID+`"`,
	); got != 1 {
		t.Fatalf("invalid materialized Session time has %d %q inputs, want 1",
			got, sessionStartFieldID)
	}
	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-stale-rundown"},
		"expected_draft_revision": {"0"},
		"location_name":           {"Stale Hall"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, `role="alert"`) ||
		!strings.Contains(stale.body, "Draft changed") ||
		!strings.Contains(stale.body, `name="location_name" value="Stale Hall"`) {
		t.Fatalf("stale Draft response = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	preview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"publish-preview"},
		"change_id":  {"4"},
	})
	if preview.status != http.StatusOK ||
		!strings.Contains(preview.body, "Confirm publish") ||
		!strings.Contains(preview.body, "<dt>Draft revision</dt><dd>1</dd>") ||
		!strings.Contains(preview.body, "<dt>Published revision</dt><dd>0</dd>") ||
		!strings.Contains(preview.body, "Automatically included dependency") ||
		!strings.Contains(preview.body, "Affected Lanes: Lane #1") ||
		!strings.Contains(preview.body, "Affected Displays: none currently assigned") ||
		!strings.Contains(preview.body, `New Location &#34;Hall A&#34; created.`) ||
		!strings.Contains(preview.body, `New Session &#34;Opening Ceremony&#34; created.`) {
		t.Fatalf("Publish Preview = %d %q", preview.status, preview.body)
	}
	staleConfirmation := frontendNamedValues(preview.body,
		"draft_revision", "published_revision", "fingerprint", "change_id",
	)
	staleConfirmation.Set("csrf_token", requireFrontendCSRF(t, preview))
	staleConfirmation.Set("action", "publish")
	staleConfirmation.Set("command_id", "browser-stale-publish")
	staleConfirmation.Set("publish_note", "Safe publish note")
	deferred := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-deferred-track"},
		"expected_draft_revision": {"1"},
		"track_name":              {"Deferred Track"},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("create deferred Draft work = %d %q", deferred.status, deferred.body)
	}
	stalePublish := postFrontendForm(
		t, administrator, server.address, path, staleConfirmation,
	)
	if stalePublish.status != http.StatusConflict ||
		!strings.Contains(stalePublish.body, "Publish Preview is stale") ||
		!strings.Contains(stalePublish.body, "Confirm publish") ||
		!strings.Contains(stalePublish.body, "Safe publish note") {
		t.Fatalf("stale Publish = %d %q", stalePublish.status, stalePublish.body)
	}
	assertAccessibleFormErrors(t, stalePublish, nil)
	confirmation := frontendNamedValues(
		stalePublish.body,
		"draft_revision", "published_revision", "fingerprint", "change_id",
	)
	confirmation.Set("csrf_token", requireFrontendCSRF(t, stalePublish))
	confirmation.Set("action", "publish")
	confirmation.Set("command_id", "browser-publish-rundown")
	published := postFrontendForm(t, administrator, server.address, path, confirmation)
	if published.status != http.StatusSeeOther {
		t.Fatalf("Publish = %d %q", published.status, published.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Published revision</dt><dd>1</dd>") ||
		!strings.Contains(page.body, "Opening Ceremony") ||
		!strings.Contains(page.body, "Deferred Track") {
		t.Fatalf("Published planning page = %d %q", page.status, page.body)
	}
	edited := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"draft"},
		"command_id":              {"browser-edit-location"},
		"expected_draft_revision": {"3"},
		"location_id":             {"1"},
		"location_name":           {"Hall Alpha"},
		"base_location_name":      {"Hall A"},
	})
	if edited.status != http.StatusSeeOther {
		t.Fatalf("edit materialized Draft = %d %q", edited.status, edited.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>4</dd>") ||
		!strings.Contains(page.body, `value="Hall Alpha"`) {
		t.Fatalf("edited materialized Draft = %d %q", page.status, page.body)
	}
	publicSchedule := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099/schedule",
	)
	for _, private := range []string{"Opening Ceremony", "Deferred Track", "Hall Alpha"} {
		if strings.Contains(publicSchedule.body, private) {
			t.Fatalf("public Schedule disclosed %q: %q", private, publicSchedule.body)
		}
	}

	const csvMappings = "kind=record_type\nkey=external_key\ntitle=title\nstart=planned_start\nend=planned_end\nlane=lane\nlocation=location"
	const csvData = "kind,key,title,start,end,lane,location\nSession,browser-session,Imported Session,2026-08-21T11:00:00+02:00,2026-08-21T11:30:00+02:00,Main Stage,Hall Alpha\n"
	invalidCSV := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":   {requireFrontendCSRF(t, page)},
		"action":       {"csv-preview"},
		"csv_mappings": {"not-a-mapping"},
		"csv_data":     {csvData},
	})
	if invalidCSV.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidCSV.body, csvData) {
		t.Fatalf("invalid CSV preview = %d %q", invalidCSV.status, invalidCSV.body)
	}
	assertAccessibleFormErrors(t, invalidCSV, map[string]string{
		"csv-mappings": "must use one source=target mapping per line",
	})
	csvPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":   {requireFrontendCSRF(t, page)},
		"action":       {"csv-preview"},
		"csv_mappings": {csvMappings},
		"csv_data":     {csvData},
	})
	if csvPreview.status != http.StatusOK ||
		!strings.Contains(csvPreview.body, "Imported Session") ||
		!strings.Contains(csvPreview.body, "<details open><summary>CSV import") ||
		!strings.Contains(csvPreview.body, "Confirm CSV import") {
		t.Fatalf("CSV preview = %d %q", csvPreview.status, csvPreview.body)
	}
	csvConfirmation := frontendNamedValues(
		csvPreview.body,
		"expected_draft_revision",
		"fingerprint",
	)
	csvConfirmation["proposal_id"] = frontendCheckboxValues(csvPreview.body, "proposal_id")
	csvConfirmation.Set("csrf_token", requireFrontendCSRF(t, csvPreview))
	csvConfirmation.Set("action", "csv-import")
	csvConfirmation.Set("command_id", "browser-import-csv")
	csvConfirmation.Set("csv_mappings", csvMappings)
	csvConfirmation.Set("csv_data", csvData)
	invalidCSVConfirmation := csvConfirmation
	invalidCSVConfirmation.Del("proposal_id")
	invalidCSVConfirmation.Set("command_id", "browser-empty-import-csv")
	emptyCSV := postFrontendForm(
		t, administrator, server.address, path, invalidCSVConfirmation,
	)
	if emptyCSV.status != http.StatusUnprocessableEntity ||
		!strings.Contains(emptyCSV.body, csvData) {
		t.Fatalf("empty CSV selection = %d %q", emptyCSV.status, emptyCSV.body)
	}
	assertAccessibleFormErrors(t, emptyCSV, map[string]string{
		"csv-proposal-ids": "select at least one proposal",
	})
	csvConfirmation = frontendNamedValues(
		emptyCSV.body,
		"expected_draft_revision",
		"fingerprint",
	)
	csvConfirmation["proposal_id"] = frontendCheckboxOptions(emptyCSV.body, "proposal_id")
	csvConfirmation.Set("csrf_token", requireFrontendCSRF(t, emptyCSV))
	csvConfirmation.Set("action", "csv-import")
	csvConfirmation.Set("command_id", "browser-import-csv")
	csvConfirmation.Set("csv_mappings", csvMappings)
	csvConfirmation.Set("csv_data", csvData)
	if imported := postFrontendForm(
		t, administrator, server.address, path, csvConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("CSV import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>5</dd>") ||
		!strings.Contains(page.body, "Imported Session") {
		t.Fatalf("CSV Draft = %d %q", page.status, page.body)
	}

	const icalendarData = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:browser-ical\r\nDTSTART:20260821T120000Z\r\nDTEND:20260821T123000Z\r\nSUMMARY:iCalendar Session\r\nLOCATION:Main Stage\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	icalendarPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"icalendar-preview"},
		"icalendar_data": {icalendarData},
	})
	if icalendarPreview.status != http.StatusOK ||
		!strings.Contains(icalendarPreview.body, "iCalendar Session") ||
		!strings.Contains(icalendarPreview.body, "<details open><summary>iCalendar import") ||
		!strings.Contains(icalendarPreview.body, "Confirm iCalendar import") {
		t.Fatalf("iCalendar preview = %d %q", icalendarPreview.status, icalendarPreview.body)
	}
	icalendarConfirmation := frontendNamedValues(
		icalendarPreview.body,
		"expected_draft_revision",
		"fingerprint",
	)
	icalendarConfirmation["proposal_id"] = frontendCheckboxValues(
		icalendarPreview.body,
		"proposal_id",
	)
	icalendarConfirmation.Set("csrf_token", requireFrontendCSRF(t, icalendarPreview))
	icalendarConfirmation.Set("action", "icalendar-import")
	icalendarConfirmation.Set("command_id", "browser-import-icalendar")
	icalendarConfirmation.Set("icalendar_data", icalendarData)
	invalidICalendarConfirmation := icalendarConfirmation
	invalidICalendarConfirmation.Del("proposal_id")
	invalidICalendarConfirmation.Set("command_id", "browser-empty-import-icalendar")
	emptyICalendar := postFrontendForm(
		t, administrator, server.address, path, invalidICalendarConfirmation,
	)
	if emptyICalendar.status != http.StatusUnprocessableEntity ||
		!strings.Contains(emptyICalendar.body, icalendarData) {
		t.Fatalf("empty iCalendar selection = %d %q", emptyICalendar.status, emptyICalendar.body)
	}
	assertAccessibleFormErrors(t, emptyICalendar, map[string]string{
		"icalendar-proposal-ids": "select at least one proposal",
	})
	icalendarConfirmation = frontendNamedValues(
		emptyICalendar.body,
		"expected_draft_revision",
		"fingerprint",
	)
	icalendarConfirmation["proposal_id"] = frontendCheckboxOptions(
		emptyICalendar.body,
		"proposal_id",
	)
	icalendarConfirmation.Set("csrf_token", requireFrontendCSRF(t, emptyICalendar))
	icalendarConfirmation.Set("action", "icalendar-import")
	icalendarConfirmation.Set("command_id", "browser-import-icalendar")
	icalendarConfirmation.Set("icalendar_data", icalendarData)
	if imported := postFrontendForm(
		t, administrator, server.address, path, icalendarConfirmation,
	); imported.status != http.StatusSeeOther {
		t.Fatalf("iCalendar import = %d %q", imported.status, imported.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<dt>Draft revision</dt><dd>6</dd>") ||
		!strings.Contains(page.body, "iCalendar Session") {
		t.Fatalf("iCalendar Draft = %d %q", page.status, page.body)
	}
	server.stop(t)
}

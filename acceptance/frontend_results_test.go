package acceptance_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
)

func TestBrowserStagesAndReviewsCompetitionResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	createEntry := func(commandID, name string) int64 {
		t.Helper()
		created, err := competitionClient.CreateEntry(
			t.Context(),
			connect.NewRequest(&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: commandID, Name: name,
			}),
		)
		if err != nil {
			t.Fatalf("create Results Entry: %v", err)
		}
		return created.Msg.GetEntry().GetId()
	}
	firstID := createEntry("browser-results-entry-first", "First Result")
	secondID := createEntry("browser-results-entry-second", "Second Result")
	path := "/backstage/events/1/results"

	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Results and Prizegiving",
		"Demo Competition",
		"First Result",
		"Second Result",
		`name="action" value="save-results-draft"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("Results page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	invalidDraft := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"NoPublicResults"},
		"no_public_reason":       {"Retain this Crew Reason."},
		"tally_override_reason":  {"Retain this tally reason."},
		"public_explanation":     {"Retain this Results explanation."},
		"score_type":             {"Duration"},
		"score_visibility":       {"CrewOnly"},
		"score_unit":             {"seconds"},
		"score_precision":        {"-1"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"LowerWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Unplaced", "Placed"},
		"placement":     {"", "2"},
		"display_order": {"2", "1"},
		"score":         {"1m2s", "59s"},
	})
	if invalidDraft.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidDraft.body,
			">Retain this Results explanation.</textarea>",
		) ||
		!strings.Contains(invalidDraft.body, ">Retain this Crew Reason.</textarea>") ||
		!strings.Contains(invalidDraft.body, `<option value="NoPublicResults" selected>No Public Results</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="Duration" selected>Duration</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="CrewOnly" selected>Crew Only</option>`) ||
		!strings.Contains(invalidDraft.body, `name="score_unit" value="seconds"`) ||
		!strings.Contains(invalidDraft.body, `<option value="Optional" selected>Optional</option>`) ||
		!strings.Contains(invalidDraft.body, `<option value="LowerWins" selected>Lower Wins</option>`) ||
		!strings.Contains(invalidDraft.body, `name="score" value="1m2s"`) ||
		!strings.Contains(invalidDraft.body, `name="score" value="59s"`) {
		t.Fatalf("invalid Results Draft = %d %q", invalidDraft.status, invalidDraft.body)
	}
	assertAccessibleFormErrors(t, invalidDraft, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) + "-score-precision": "nonnegative integer",
	})
	invalidPlacement := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, invalidDraft)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results-placement"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"Decimal"},
		"score_visibility":       {"Public"},
		"score_unit":             {"points"},
		"score_precision":        {"1"},
		"score_requirement":      {"Required"},
		"score_interpretation":   {"HigherWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Placed", "Placed"},
		"placement":     {"not-a-number", "2"},
		"display_order": {"1", "2"},
		"score":         {"9.5", "8.0"},
	})
	if invalidPlacement.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidPlacement.body, `name="placement" value="not-a-number"`) ||
		!strings.Contains(invalidPlacement.body, `name="score" value="9.5"`) ||
		!strings.Contains(invalidPlacement.body, `name="score" value="8.0"`) {
		t.Fatalf(
			"invalid Results placement = %d %q",
			invalidPlacement.status,
			invalidPlacement.body,
		)
	}
	assertAccessibleFormErrors(t, invalidPlacement, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) + "-placement-" +
			strconv.FormatInt(firstID, 10): "positive integer",
	})
	invalidRanking := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, invalidPlacement)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-invalid-results-ranking"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"Decimal"},
		"score_visibility":       {"Public"},
		"score_unit":             {"points"},
		"score_precision":        {"1"},
		"score_requirement":      {"Required"},
		"score_interpretation":   {"HigherWins"},
		"standing_entry_id": {
			strconv.FormatInt(firstID, 10),
			strconv.FormatInt(secondID, 10),
		},
		"standing":      {"Placed", "Placed"},
		"placement":     {"2", "1"},
		"display_order": {"1", "2"},
		"score":         {"9.5", "8.0"},
	})
	if invalidRanking.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid Results ranking = %d %q",
			invalidRanking.status,
			invalidRanking.body,
		)
	}
	assertAccessibleFormErrors(t, invalidRanking, map[string]string{
		"results-" + strconv.FormatInt(competitionID, 10) +
			"-result-standings": "competition ranking",
	})

	save := func(commandID, expectedRevision string, placements []string) frontendResponse {
		t.Helper()
		return postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token":             {requireFrontendCSRF(t, page)},
			"action":                 {"save-results-draft"},
			"command_id":             {commandID},
			"competition_session_id": {strconv.FormatInt(competitionID, 10)},
			"expected_revision":      {expectedRevision},
			"disposition":            {"Publish"},
			"score_type":             {"Decimal"},
			"score_visibility":       {"Public"},
			"score_unit":             {"points"},
			"score_precision":        {"1"},
			"score_requirement":      {"Required"},
			"score_interpretation":   {"HigherWins"},
			"public_explanation":     {"Retain this Results explanation."},
			"standing_entry_id": {
				strconv.FormatInt(firstID, 10),
				strconv.FormatInt(secondID, 10),
			},
			"standing":      {"Placed", "Placed"},
			"placement":     placements,
			"display_order": {"1", "2"},
			"score":         {"9.5", "8.0"},
		})
	}
	if saved := save("browser-save-results", "0", []string{"1", "2"}); saved.status != http.StatusSeeOther || saved.header.Get("Location") != path {
		t.Fatalf("save browser Results = %d %q", saved.status, saved.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>1</code>") ||
		!strings.Contains(page.body, `data-tone="draft">Not Ready</span>`) {
		t.Fatalf("saved browser Results missing revision state: %d %q", page.status, page.body)
	}
	staleDraft := save("browser-stale-results", "0", []string{"1", "2"})
	if staleDraft.status != http.StatusConflict ||
		!strings.Contains(staleDraft.body, "Results changed") ||
		!strings.Contains(
			staleDraft.body,
			">Retain this Results explanation.</textarea>",
		) {
		t.Fatalf("stale Results Draft = %d %q", staleDraft.status, staleDraft.body)
	}
	assertAccessibleFormErrors(t, staleDraft, nil)
	page = getFrontendPage(t, administrator, server.address, path)

	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"1"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>1</code>") ||
		!strings.Contains(page.body, `data-tone="success">Ready</span>`) {
		t.Fatalf("reviewed browser Results missing Ready state: %d %q", page.status, page.body)
	}

	if tied := save("browser-tie-results", "1", []string{"1", "1"}); tied.status != http.StatusSeeOther {
		t.Fatalf("save tied browser Results = %d %q", tied.status, tied.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Draft revision <code>2</code>") ||
		!strings.Contains(page.body, `data-tone="draft">Not Ready</span>`) {
		t.Fatalf("changed browser Results did not clear Ready: %d %q", page.status, page.body)
	}

	for _, entry := range []struct {
		id   int64
		name string
	}{
		{firstID, "First Result"},
		{secondID, "Second Result"},
	} {
		if !regexp.MustCompile(
			`type="checkbox"\s+name="award_recipient_entry_ids_0"\s+value="` +
				strconv.FormatInt(entry.id, 10) + `"[^>]*>\s*` +
				regexp.QuoteMeta(entry.name) + ` \(#` + strconv.FormatInt(entry.id, 10) + `\)`,
		).MatchString(page.body) {
			t.Fatalf(
				"Competition Award recipient picker lacks a scoped %q checkbox: %d %q",
				entry.name, page.status, page.body,
			)
		}
	}
	invalidAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"save-competition-awards"},
		"command_id":                  {"browser-invalid-results-awards"},
		"competition_session_id":      {strconv.FormatInt(competitionID, 10)},
		"expected_revision":           {"2"},
		"award_key":                   {"retained-key"},
		"award_name":                  {"Retained Competition Award"},
		"award_recipient_entry_ids_0": {strconv.FormatInt(firstID, 10)},
		"award_recipient_names":       {"Retained recipient"},
		"award_promoted":              {"true"},
		"award_display_order":         {"not-a-number"},
	})
	if invalidAwards.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidAwards.body, `name="award_key" value="retained-key"`) ||
		!strings.Contains(
			invalidAwards.body,
			`name="award_name" value="Retained Competition Award"`,
		) ||
		!strings.Contains(
			invalidAwards.body,
			">Retained recipient</textarea>",
		) ||
		!strings.Contains(
			invalidAwards.body,
			`name="award_display_order" value="not-a-number"`,
		) {
		t.Fatalf("invalid Competition Awards = %d %q", invalidAwards.status, invalidAwards.body)
	}
	assertAccessibleFormErrors(t, invalidAwards, map[string]string{
		"competition-awards-" + strconv.FormatInt(competitionID, 10) +
			"-award-display-order-0": "positive integer",
	})

	awarded := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, invalidAwards)},
		"action":                      {"save-competition-awards"},
		"command_id":                  {"browser-save-results-awards"},
		"competition_session_id":      {strconv.FormatInt(competitionID, 10)},
		"expected_revision":           {"2"},
		"award_key":                   {"audience-choice"},
		"award_name":                  {"Audience Choice"},
		"award_recipient_entry_ids_0": {strconv.FormatInt(secondID, 10)},
		"award_recipient_names":       {""},
		"award_promoted":              {"true"},
		"award_display_order":         {"1"},
	})
	if awarded.status != http.StatusSeeOther {
		t.Fatalf("save browser Competition Awards = %d %q", awarded.status, awarded.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{"Draft revision <code>3</code>", "Audience Choice", "audience-choice"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Competition Award page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	staleAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"save-competition-awards"},
		"command_id":                  {"browser-stale-results-awards"},
		"competition_session_id":      {strconv.FormatInt(competitionID, 10)},
		"expected_revision":           {"2"},
		"award_key":                   {"stale-key"},
		"award_name":                  {"Stale Competition Award"},
		"award_recipient_entry_ids_0": {strconv.FormatInt(firstID, 10)},
		"award_recipient_names":       {"Stale recipient"},
		"award_promoted":              {"false"},
		"award_display_order":         {"1"},
	})
	if staleAwards.status != http.StatusConflict ||
		!strings.Contains(staleAwards.body, `name="award_key" value="stale-key"`) ||
		!strings.Contains(
			staleAwards.body,
			`name="award_name" value="Stale Competition Award"`,
		) ||
		!strings.Contains(staleAwards.body, ">Stale recipient</textarea>") {
		t.Fatalf("stale Competition Awards = %d %q", staleAwards.status, staleAwards.body)
	}
	assertAccessibleFormErrors(t, staleAwards, nil)
	page = getFrontendPage(t, administrator, server.address, path)

	ready = postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-awarded-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"3"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark awarded browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	ceremonyID := frontendNamedValues(page.body, "ceremony_session_id").
		Get("ceremony_session_id")
	if ceremonyID == "" || !strings.Contains(page.body, "Designate Prizegiving") {
		t.Fatalf("browser Prizegiving designation unavailable: %d %q", page.status, page.body)
	}
	designated := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":          {requireFrontendCSRF(t, page)},
		"action":              {"designate-prizegiving"},
		"command_id":          {"browser-designate-prizegiving"},
		"ceremony_session_id": {ceremonyID},
	})
	if designated.status != http.StatusSeeOther {
		t.Fatalf("designate browser Prizegiving = %d %q", designated.status, designated.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Plan revision <code>0</code>") ||
		!strings.Contains(page.body, `name="action" value="save-prizegiving-plan"`) {
		t.Fatalf("designated browser Prizegiving missing plan: %d %q", page.status, page.body)
	}

	template := "{{.Event.Name}} Results\n{{range .Items}}{{with .Competition}}{{.Title}}{{end}}{{end}}"
	invalidPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-invalid-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"0"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"AllAtCue"},
		"results_text_template_revision": {"0"},
		"results_text_template":          {"Retain this Prizegiving template."},
	})
	if invalidPlan.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidPlan.body, ">Retain this Prizegiving template.</textarea>") ||
		!strings.Contains(invalidPlan.body, `<option value="AllAtCue" selected>All At Cue</option>`) ||
		!regexp.MustCompile(
			`name="plan_competition_session_id" value="`+
				strconv.FormatInt(competitionID, 10)+`" checked`,
		).MatchString(invalidPlan.body) {
		t.Fatalf("invalid browser Prizegiving plan = %d %q", invalidPlan.status, invalidPlan.body)
	}
	assertAccessibleFormErrors(t, invalidPlan, map[string]string{
		"prizegiving-" + ceremonyID + "-results-text-template-revision": "positive integer",
	})

	savedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, invalidPlan)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-save-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"0"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"ProgressiveOnReveal"},
		"results_text_template_revision": {"1"},
		"results_text_template":          {template},
	})
	if savedPlan.status != http.StatusSeeOther {
		t.Fatalf("save browser Prizegiving plan = %d %q", savedPlan.status, savedPlan.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Plan revision <code>1</code>",
		"CompetitionResults",
		"CompetitionAward",
		`name="reveal_method"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Prizegiving plan lacks %q: %d %q", want, page.status, page.body)
		}
	}
	invalidEditedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-invalid-edited-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"1"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"AtCeremonyEnd"},
		"results_text_template_revision": {"2"},
		"results_text_template":          {"Retain this edited Prizegiving template."},
		"item_kind":                      {"CompetitionResults", "CompetitionAward"},
		"item_competition_session_id":    {strconv.FormatInt(competitionID, 10), strconv.FormatInt(competitionID, 10)},
		"item_award_key":                 {"", "audience-choice"},
		"sequence_display_order":         {"not-a-number", "4"},
		"reveal_method":                  {"AnimatedScoreBars", "SequentialPodium"},
		"publication_display_order":      {"2", "1"},
	})
	if invalidEditedPlan.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="sequence_display_order" value="not-a-number"`,
		) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="sequence_display_order" value="4"`,
		) ||
		!strings.Contains(invalidEditedPlan.body, `<option value="AnimatedScoreBars" selected>Animated Score Bars</option>`) ||
		!strings.Contains(invalidEditedPlan.body, `<option value="SequentialPodium" selected>Sequential Podium</option>`) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="publication_display_order" value="2"`,
		) ||
		!strings.Contains(
			invalidEditedPlan.body,
			`name="publication_display_order" value="1"`,
		) {
		t.Fatalf(
			"invalid edited Prizegiving plan = %d %q",
			invalidEditedPlan.status,
			invalidEditedPlan.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEditedPlan, map[string]string{
		"prizegiving-" + ceremonyID + "-sequence-display-order-0": "positive integer",
	})

	editedPlan := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                     {requireFrontendCSRF(t, page)},
		"action":                         {"save-prizegiving-plan"},
		"command_id":                     {"browser-edit-prizegiving-plan"},
		"ceremony_session_id":            {ceremonyID},
		"expected_revision":              {"1"},
		"plan_competition_session_id":    {strconv.FormatInt(competitionID, 10)},
		"release_policy":                 {"ProgressiveOnReveal"},
		"results_text_template_revision": {"1"},
		"results_text_template":          {template},
		"item_kind":                      {"CompetitionResults", "CompetitionAward"},
		"item_competition_session_id":    {strconv.FormatInt(competitionID, 10), strconv.FormatInt(competitionID, 10)},
		"item_award_key":                 {"", "audience-choice"},
		"sequence_display_order":         {"1", "2"},
		"reveal_method":                  {"SequentialPodium", "StaticResult"},
		"publication_display_order":      {"1", "2"},
	})
	if editedPlan.status != http.StatusSeeOther {
		t.Fatalf("edit browser Prizegiving plan = %d %q", editedPlan.status, editedPlan.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Plan revision <code>2</code>") ||
		!regexp.MustCompile(`<option value="SequentialPodium" selected>Sequential Podium</option>`).MatchString(page.body) {
		t.Fatalf("edited browser Prizegiving sequence missing: %d %q", page.status, page.body)
	}

	preflight := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":          {requireFrontendCSRF(t, page)},
		"action":              {"run-prizegiving-preflight"},
		"command_id":          {"browser-lock-prizegiving"},
		"ceremony_session_id": {ceremonyID},
		"expected_revision":   {"2"},
	})
	if preflight.status != http.StatusSeeOther {
		t.Fatalf("browser Prizegiving Preflight = %d %q", preflight.status, preflight.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		`data-tone="warning">Locked</span>`,
		"Preview locked Results",
		"Rehearse locked Results",
		"Open Prizegiving Program Control",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("locked browser Prizegiving lacks %q: %d %q", want, page.status, page.body)
		}
	}
	preview := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?ceremony_id="+ceremonyID+"&preview=Preview",
	)
	rehearsal := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?ceremony_id="+ceremonyID+"&preview=Rehearsal",
	)
	for label, response := range map[string]frontendResponse{
		"Preview": preview, "Rehearsal": rehearsal,
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, label) ||
			!strings.Contains(response.body, "PREVIEW — NOT PROGRAM OUTPUT") {
			t.Fatalf("%s browser Results = %d %q", label, response.status, response.body)
		}
	}
	publicPath := "/results/events/1/prizegiving/" + ceremonyID
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, publicPath,
	); public.status != http.StatusNotFound {
		t.Fatalf("Preview or rehearsal published Results: %d %q", public.status, public.body)
	}

	ceremonyIDValue, err := strconv.ParseInt(ceremonyID, 10, 64)
	if err != nil {
		t.Fatalf("parse browser Prizegiving ID: %v", err)
	}
	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-prizegiving"},
		"session_id":                   {ceremonyID},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start browser Prizegiving = %d %q", started.status, started.body)
	}
	programClient := programv1connect.NewProgramControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	channel, err := programClient.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: ceremonyIDValue,
		}),
	)
	if err != nil {
		t.Fatalf("load browser Prizegiving Program Channel: %v", err)
	}
	claimed, err := programClient.ChangeControl(
		t.Context(),
		connect.NewRequest(&programv1.ChangeControlRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "claim-results-browser-prizegiving",
			ExpectedControlStateRevision: channel.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("claim browser Prizegiving control: %v", err)
	}
	taken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "take-first-browser-result",
			ExpectedLiveStateRevision:    claimed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: claimed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      claimed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil {
		t.Fatalf("take first browser Result from %+v: %v", claimed.Msg.GetChannel(), err)
	}
	firstRevealed, err := programClient.ActOnResult(
		t.Context(),
		connect.NewRequest(&programv1.ActOnResultRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "reveal-first-browser-result",
			Action:                       programv1.ResultAction_RESULT_ACTION_SKIP_TO_FINAL,
			Item:                         taken.Msg.GetChannel().GetProgramOutput(),
			ExpectedProgramRevision:      taken.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("reveal first browser Result: %v", err)
	}
	partial := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/1/results.json",
	)
	if partial.status != http.StatusOK ||
		!strings.Contains(partial.body, `"status": "Partial"`) ||
		!strings.Contains(partial.body, "Demo Competition") {
		t.Fatalf("progressive partial browser Results = %d %q", partial.status, partial.body)
	}
	secondTaken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "take-second-browser-result",
			ExpectedLiveStateRevision:    firstRevealed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: firstRevealed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      firstRevealed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil {
		t.Fatalf("take second browser Result: %v", err)
	}
	if _, err = programClient.ActOnResult(
		t.Context(),
		connect.NewRequest(&programv1.ActOnResultRequest{
			EventId: 1, SessionId: ceremonyIDValue,
			CommandId:                    "reveal-second-browser-result",
			Action:                       programv1.ResultAction_RESULT_ACTION_SKIP_TO_FINAL,
			Item:                         secondTaken.Msg.GetChannel().GetProgramOutput(),
			ExpectedProgramRevision:      secondTaken.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: secondTaken.Msg.GetChannel().GetControlStateRevision(),
		}),
	); err != nil {
		t.Fatalf("reveal second browser Result: %v", err)
	}
	completeReveal := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/2/results.json",
	)
	if completeReveal.status != http.StatusOK ||
		!strings.Contains(completeReveal.body, `"status": "Partial"`) ||
		!strings.Contains(completeReveal.body, "Audience Choice") {
		t.Fatalf(
			"complete progressive reveal browser Results = %d %q",
			completeReveal.status,
			completeReveal.body,
		)
	}
	operationsPage = getFrontendPage(t, administrator, server.address, operationsPath)
	ended := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"end-session"},
		"command_id":                   {"browser-end-prizegiving"},
		"session_id":                   {ceremonyID},
		"expected_live_state_revision": {"1"},
	})
	if ended.status != http.StatusSeeOther {
		t.Fatalf("end browser Prizegiving = %d %q", ended.status, ended.body)
	}
	final := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		publicPath+"/revisions/3/results.json",
	)
	if final.status != http.StatusOK ||
		!strings.Contains(final.body, `"status": "Final"`) ||
		!strings.Contains(final.body, "Audience Choice") {
		t.Fatalf("final browser Prizegiving Results = %d %q", final.status, final.body)
	}
}

func TestBrowserPublishesAndCorrectsStandaloneResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := competitionClient.CreateEntry(
		t.Context(),
		connect.NewRequest(&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "browser-standalone-entry", Name: "Standalone Winner",
		}),
	)
	if err != nil {
		t.Fatalf("create standalone Results Entry: %v", err)
	}
	path := "/backstage/events/1/results"
	page := getFrontendPage(t, administrator, server.address, path)
	saved := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"save-results-draft"},
		"command_id":             {"browser-save-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"0"},
		"disposition":            {"Publish"},
		"score_type":             {"None"},
		"score_visibility":       {"Public"},
		"score_precision":        {"0"},
		"score_requirement":      {"Optional"},
		"score_interpretation":   {"Informational"},
		"standing_entry_id":      {strconv.FormatInt(created.Msg.GetEntry().GetId(), 10)},
		"standing":               {"Placed"},
		"placement":              {"1"},
		"display_order":          {"1"},
		"score":                  {""},
	})
	if saved.status != http.StatusSeeOther {
		t.Fatalf("save standalone browser Results = %d %q", saved.status, saved.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"mark-results-ready"},
		"command_id":             {"browser-ready-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
		"expected_revision":      {"1"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("mark standalone browser Results Ready = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Release standalone Results") {
		t.Fatalf("standalone browser release unavailable: %d %q", page.status, page.body)
	}
	released := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"release-standalone-results"},
		"command_id":             {"browser-release-standalone-results"},
		"competition_session_id": {strconv.FormatInt(competitionID, 10)},
	})
	if released.status != http.StatusSeeOther {
		t.Fatalf("release standalone browser Results = %d %q", released.status, released.body)
	}

	scopePath := "/results/events/1/standalone/" + strconv.FormatInt(competitionID, 10)
	htmlPath := "/events/beamconf-2099/results"
	htmlResults := getFrontendPage(t, authenticatedClient(t), server.publicAddress, htmlPath)
	text := getFrontendPage(t, authenticatedClient(t), server.publicAddress, scopePath+"/results.txt")
	jsonRevisionOne := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		scopePath+"/revisions/1/results.json",
	)
	for label, response := range map[string]frontendResponse{
		"HTML": htmlResults, "text": text, "JSON": jsonRevisionOne,
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, "Standalone Winner") {
			t.Fatalf("standalone %s Results = %d %q", label, response.status, response.body)
		}
	}
	if text.header.Get("ETag") != jsonRevisionOne.header.Get("ETag") {
		t.Fatalf(
			"standalone machine Results ETags = text %q JSON %q",
			text.header.Get("ETag"),
			jsonRevisionOne.header.Get("ETag"),
		)
	}

	server.stop(t)
	server = startBeamersWithPublicListener(t, server.bin, server.dataDir)
	htmlAfterRestart := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, htmlPath,
	)
	if htmlAfterRestart.status != http.StatusOK ||
		htmlAfterRestart.body != htmlResults.body ||
		htmlAfterRestart.header.Get("ETag") != htmlResults.header.Get("ETag") {
		t.Fatalf(
			"standalone Results after restart = %d %q %q",
			htmlAfterRestart.status,
			htmlAfterRestart.header.Get("ETag"),
			htmlAfterRestart.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `name="action" value="save-results-correction"`) {
		t.Fatalf("browser Results Correction unavailable: %d %q", page.status, page.body)
	}
	correctedJSON := strings.Replace(
		jsonRevisionOne.body,
		"Demo Competition",
		"Corrected Demo Competition",
		1,
	)
	malformed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-malformed-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {`{"items":`},
		"crew_reason":                  {"Retain this Correction Crew Reason."},
		"public_note":                  {"Retain this Correction public note."},
	})
	if malformed.status != http.StatusUnprocessableEntity ||
		!strings.Contains(malformed.body, `>{&#34;items&#34;:</textarea>`) ||
		!strings.Contains(
			malformed.body,
			">Retain this Correction Crew Reason.</textarea>",
		) ||
		!strings.Contains(
			malformed.body,
			">Retain this Correction public note.</textarea>",
		) {
		t.Fatalf("malformed browser Results Correction = %d %q", malformed.status, malformed.body)
	}
	assertAccessibleFormErrors(t, malformed, map[string]string{
		"results-correction-" + strconv.FormatInt(competitionID, 10) +
			"-corrected-results-json": "valid Results JSON",
	})
	page = malformed
	reasonless := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-reasonless-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {correctedJSON},
	})
	if reasonless.status != http.StatusUnprocessableEntity {
		t.Fatalf("reasonless browser Results Correction = %d %q", reasonless.status, reasonless.body)
	}
	if !strings.Contains(reasonless.body, "Corrected Demo Competition") {
		t.Fatalf("reasonless Results Correction lost safe JSON: %q", reasonless.body)
	}
	assertAccessibleFormErrors(t, reasonless, map[string]string{
		"results-correction-" + strconv.FormatInt(competitionID, 10) + "-crew-reason": "Crew Reason",
	})
	page = reasonless
	correction := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"save-results-correction"},
		"command_id":                   {"browser-save-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"0"},
		"base_publication_revision":    {"1"},
		"corrected_results_json":       {correctedJSON},
		"crew_reason":                  {"The published Competition title was incomplete."},
		"public_note":                  {"Competition title corrected."},
	})
	if correction.status != http.StatusSeeOther {
		t.Fatalf("save browser Results Correction = %d %q", correction.status, correction.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Correction revision <code>1</code>") ||
		!strings.Contains(page.body, "Draft") {
		t.Fatalf("saved browser Results Correction unavailable: %d %q", page.status, page.body)
	}
	reviewed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"review-results-correction"},
		"command_id":                   {"browser-review-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"1"},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review browser Results Correction = %d %q", reviewed.status, reviewed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	published := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"publish-results-correction"},
		"command_id":                   {"browser-publish-correction"},
		"correction_scope":             {"Standalone"},
		"correction_scope_session_id":  {strconv.FormatInt(competitionID, 10)},
		"expected_correction_revision": {"2"},
	})
	if published.status != http.StatusSeeOther {
		t.Fatalf("publish browser Results Correction = %d %q", published.status, published.body)
	}
	corrected := getFrontendPage(t, authenticatedClient(t), server.publicAddress, htmlPath)
	prior := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		scopePath+"/revisions/1/results.json",
	)
	if corrected.status != http.StatusOK ||
		!strings.Contains(corrected.body, "Corrected Demo Competition") ||
		!strings.Contains(corrected.body, "Competition title corrected.") ||
		prior.body != jsonRevisionOne.body {
		t.Fatalf(
			"corrected or prior browser Results = corrected %d %q, prior %d %q",
			corrected.status, corrected.body, prior.status, prior.body,
		)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	standaloneWinnerID := created.Msg.GetEntry().GetId()
	if !regexp.MustCompile(
		`type="checkbox"\s+name="event_award_recipient_entry_ids_0"\s+value="` +
			strconv.FormatInt(standaloneWinnerID, 10) + `"[^>]*>\s*` +
			`Standalone Winner — Demo Competition \(#` + strconv.FormatInt(standaloneWinnerID, 10) + `\)`,
	).MatchString(page.body) {
		t.Fatalf("Event Award recipients lack a scoped picker: %d %q", page.status, page.body)
	}
	invalidEventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"save-event-awards"},
		"command_id":        {"browser-invalid-event-awards"},
		"expected_revision": {"0"},
		"event_award_key":   {"retained-event-key"},
		"event_award_name":  {"Retained Event Award"},
		"event_award_recipient_entry_ids_0": {
			strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
		},
		"event_award_recipient_names": {"Retained Event recipient"},
		"event_award_path":            {"Standalone"},
		"event_award_display_order":   {"not-a-number"},
	})
	if invalidEventAwards.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_key" value="retained-event-key"`,
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_name" value="Retained Event Award"`,
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			">Retained Event recipient</textarea>",
		) ||
		!strings.Contains(
			invalidEventAwards.body,
			`name="event_award_display_order" value="not-a-number"`,
		) {
		t.Fatalf(
			"invalid browser Event Awards = %d %q",
			invalidEventAwards.status,
			invalidEventAwards.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEventAwards, map[string]string{
		"event-awards-event-award-display-order-0": "positive integer",
	})
	eventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, invalidEventAwards)},
		"action":                      {"save-event-awards"},
		"command_id":                  {"browser-save-event-awards"},
		"expected_revision":           {"0"},
		"event_award_key":             {"community"},
		"event_award_name":            {"Community Award"},
		"event_award_recipient_names": {"Community Hero"},
		"event_award_path":            {"Standalone"},
		"event_award_display_order":   {"1"},
	})
	if eventAwards.status != http.StatusSeeOther {
		t.Fatalf("save browser Event Awards = %d %q", eventAwards.status, eventAwards.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Community Award",
		"Standalone path revision <code>1</code>",
		`data-tone="draft">Not Ready</span>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("browser Event Awards lack %q: %d %q", want, page.status, page.body)
		}
	}
	staleEventAwards := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"save-event-awards"},
		"command_id":        {"browser-stale-event-awards"},
		"expected_revision": {"0"},
		"event_award_key":   {"stale-event-key"},
		"event_award_name":  {"Stale Event Award"},
		"event_award_recipient_entry_ids_0": {
			strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
		},
		"event_award_recipient_names": {"Stale Event recipient"},
		"event_award_path":            {"Standalone"},
		"event_award_display_order":   {"1"},
	})
	if staleEventAwards.status != http.StatusConflict ||
		!strings.Contains(
			staleEventAwards.body,
			`name="event_award_key" value="stale-event-key"`,
		) ||
		!strings.Contains(
			staleEventAwards.body,
			`name="event_award_name" value="Stale Event Award"`,
		) ||
		!strings.Contains(
			staleEventAwards.body,
			">Stale Event recipient</textarea>",
		) {
		t.Fatalf(
			"stale browser Event Awards = %d %q",
			staleEventAwards.status,
			staleEventAwards.body,
		)
	}
	assertAccessibleFormErrors(t, staleEventAwards, nil)
	page = getFrontendPage(t, administrator, server.address, path)
	eventAwardsReady := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":            {requireFrontendCSRF(t, page)},
		"action":                {"mark-event-awards-ready"},
		"command_id":            {"browser-ready-event-awards"},
		"expected_revision":     {"1"},
		"event_award_path_kind": {"Standalone"},
		"event_award_path_prizegiving_session_id": {"0"},
		"expected_path_revision":                  {"1"},
	})
	if eventAwardsReady.status != http.StatusSeeOther {
		t.Fatalf("mark browser Event Awards Ready = %d %q", eventAwardsReady.status, eventAwardsReady.body)
	}
	eventAwardsPreflight := getFrontendPage(
		t,
		administrator,
		server.address,
		path+"?event_awards_preflight=true",
	)
	if eventAwardsPreflight.status != http.StatusOK ||
		!strings.Contains(
			eventAwardsPreflight.body,
			"Standalone Event Awards Preflight passed without changing release state.",
		) {
		t.Fatalf(
			"standalone Event Awards Preflight = %d %q",
			eventAwardsPreflight.status,
			eventAwardsPreflight.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	eventAwardsReleased := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":             {requireFrontendCSRF(t, page)},
		"action":                 {"release-standalone-event-awards"},
		"command_id":             {"browser-release-event-awards"},
		"expected_revision":      {"1"},
		"expected_path_revision": {"1"},
	})
	if eventAwardsReleased.status != http.StatusSeeOther {
		t.Fatalf("release browser Event Awards = %d %q", eventAwardsReleased.status, eventAwardsReleased.body)
	}
	eventAwardsPath := "/results/events/1/event-awards"
	for label, response := range map[string]frontendResponse{
		"HTML": getFrontendPage(
			t, authenticatedClient(t), server.publicAddress, "/events/beamconf-2099/results",
		),
		"text": getFrontendPage(
			t, authenticatedClient(t), server.publicAddress, eventAwardsPath+"/results.txt",
		),
		"JSON": getFrontendPage(
			t,
			authenticatedClient(t),
			server.publicAddress,
			eventAwardsPath+"/revisions/1/results.json",
		),
	} {
		if response.status != http.StatusOK ||
			!strings.Contains(response.body, "Community Award") ||
			!strings.Contains(response.body, "Community Hero") {
			t.Fatalf("public Event Awards %s = %d %q", label, response.status, response.body)
		}
	}
}

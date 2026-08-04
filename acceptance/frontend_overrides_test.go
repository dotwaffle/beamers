package acceptance_test

import (
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
)

func TestBrowserControlsProgramOutputAndOverrides(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	code, _ := prepareBrowserEnrollment(t, server)
	operationsPath := "/backstage/events/1/operations"
	operations := getFrontendPage(t, administrator, server.address, operationsPath)
	enrolled := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, operations)},
		"action":     {"enroll-display"},
		"command_id": {"browser-control-enroll-display"},
		"code":       {code},
		"name":       {"Stage Screen"},
	})
	if enrolled.status != http.StatusSeeOther {
		t.Fatalf("enroll control Display = %d %q", enrolled.status, enrolled.body)
	}
	operations = getFrontendPage(t, administrator, server.address, operationsPath)
	assigned := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, operations)},
		"action":             {"assign-display"},
		"command_id":         {"browser-control-assign-display"},
		"display_id":         {"1"},
		"location_id":        {"1"},
		"view_key":           {"stage-timer"},
		"display_group_keys": {"stage"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign control Display = %d %q", assigned.status, assigned.body)
	}

	path := "/backstage/events/1/control"
	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Program Output and Overrides",
		`href="/crew/program/` + strconv.FormatInt(sessionID, 10) + `?event_id=1"`,
		"Previous, Current, Next, Preview, and committed Program Output",
		"Stage Message",
		"Technical Difficulties",
		"Urgent Notice",
		"Emergency Alert",
		"Overlay or Replace",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("control page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	buildVersion := frontendNamedValues(page.body, "build_version").Get("build_version")
	staleControl := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {"obsolete-build"},
		"action":           {"preview-stage-message"},
		"text":             {"Stale control"},
		"target_group_key": {"stage"},
		"duration_seconds": {"300"},
		"emphasis":         {"Normal"},
	})
	if staleControl.status != http.StatusConflict ||
		!strings.Contains(staleControl.body, "reload required") ||
		!strings.Contains(staleControl.body, "Stale control") ||
		staleControl.header.Get("X-Beamers-Build") != buildVersion {
		t.Fatalf("stale Backstage control = %d %q", staleControl.status, staleControl.body)
	}
	assertAccessibleFormErrors(t, staleControl, nil)
	invalidPreview := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-stage-message"},
		"text":             {"Retain this message"},
		"duration_seconds": {"300"},
		"emphasis":         {"Normal"},
	})
	for _, want := range []string{
		`role="alert"`,
		`aria-invalid="true"`,
		`aria-describedby="stage-target-group-error"`,
		"Enter a Display Group.",
		"Retain this message",
	} {
		if invalidPreview.status != http.StatusUnprocessableEntity ||
			!strings.Contains(invalidPreview.body, want) {
			t.Fatalf(
				"invalid Override preview lacks %q: %d %q",
				want,
				invalidPreview.status,
				invalidPreview.body,
			)
		}
	}
	assertAccessibleFormErrors(t, invalidPreview, map[string]string{
		"stage-target-group": "Enter a Display Group.",
	})
	invalidTarget := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-urgent-notice"},
		"text":             {"Retain the target warning"},
		"target_type":      {"Event"},
		"target_id":        {"7"},
		"target_key":       {"old-group"},
		"presentation":     {"Overlay"},
		"duration_seconds": {"300"},
	})
	for _, want := range []string{
		`aria-describedby="preview-urgent-notice-target-id-error"`,
		`aria-describedby="preview-urgent-notice-target-key-error"`,
		"Leave the numeric ID empty for this target.",
		"Leave the Display Group key empty for this target.",
		"Retain the target warning",
	} {
		if invalidTarget.status != http.StatusUnprocessableEntity ||
			!strings.Contains(invalidTarget.body, want) {
			t.Fatalf("invalid Override target lacks %q: %d %q",
				want, invalidTarget.status, invalidTarget.body)
		}
	}
	assertAccessibleFormErrors(t, invalidTarget, map[string]string{
		"preview-urgent-notice-target-id":  "Leave the numeric ID empty",
		"preview-urgent-notice-target-key": "Leave the Display Group key empty",
	})
	invalidNumber := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":       {requireFrontendCSRF(t, page)},
		"build_version":    {buildVersion},
		"action":           {"preview-urgent-notice"},
		"text":             {"Retain malformed numbers"},
		"target_type":      {"Location"},
		"target_id":        {"not-a-number"},
		"presentation":     {"Overlay"},
		"duration_seconds": {"also-not-a-number"},
	})
	if invalidNumber.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidNumber.body, `value="not-a-number"`) ||
		!strings.Contains(invalidNumber.body, `value="also-not-a-number"`) {
		t.Fatalf(
			"invalid Override numbers = %d %q",
			invalidNumber.status,
			invalidNumber.body,
		)
	}

	previewAndActivate := func(
		action string,
		label string,
		values url.Values,
		wantPresentation string,
	) url.Values {
		t.Helper()
		page = getFrontendPage(t, administrator, server.address, path)
		values.Set("csrf_token", requireFrontendCSRF(t, page))
		values.Set(
			"build_version",
			frontendNamedValues(page.body, "build_version").Get("build_version"),
		)
		values.Set("action", "preview-"+action)
		preview := postFrontendForm(t, administrator, server.address, path, values)
		for _, want := range []string{
			"Confirm " + label,
			"Resolved Displays",
			"Stage Screen",
			wantPresentation,
			`name="action" value="activate-` + action + `"`,
		} {
			if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
				t.Fatalf("%s preview lacks %q: %d %q", label, want, preview.status, preview.body)
			}
		}
		confirmation := frontendNamedValues(
			preview.body,
			"command_id",
			"build_version",
			"text",
			"target_group_key",
			"target_type",
			"target_id",
			"target_key",
			"presentation",
			"duration_seconds",
			"until_cleared",
			"emphasis",
			"preview_fingerprint",
		)
		confirmation.Set("csrf_token", requireFrontendCSRF(t, preview))
		confirmation.Set("action", "activate-"+action)
		confirmation.Set("confirmed", "true")
		activated := postFrontendForm(
			t, administrator, server.address, path, confirmation,
		)
		if activated.status != http.StatusSeeOther ||
			activated.header.Get("Location") != path {
			t.Fatalf("activate %s = %d %q", label, activated.status, activated.body)
		}
		return confirmation
	}

	stageConfirmation := previewAndActivate(
		"stage-message",
		"Stage Message",
		url.Values{
			"text":             {"Two minutes remaining"},
			"target_group_key": {"stage"},
			"duration_seconds": {"300"},
			"until_cleared":    {"true"},
			"emphasis":         {"Attention"},
		},
		"Overlay",
	)
	if retried := postFrontendForm(
		t, administrator, server.address, path, stageConfirmation,
	); retried.status != http.StatusSeeOther {
		t.Fatalf("exact Stage Message retry = %d %q", retried.status, retried.body)
	}
	staleActivation := maps.Clone(stageConfirmation)
	staleActivation.Set("build_version", "obsolete-build")
	staleActivationResponse := postFrontendForm(
		t, administrator, server.address, path, staleActivation,
	)
	if staleActivationResponse.status != http.StatusConflict ||
		!strings.Contains(staleActivationResponse.body, "Confirm Stage Message") ||
		!strings.Contains(staleActivationResponse.body, "Two minutes remaining") {
		t.Fatalf(
			"stale Stage Message activation = %d %q",
			staleActivationResponse.status,
			staleActivationResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleActivationResponse, map[string]string{
		"override-confirmed": "review and confirm this refreshed preview",
	})
	unconfirmed := maps.Clone(stageConfirmation)
	unconfirmed.Del("confirmed")
	if rejected := postFrontendForm(
		t, administrator, server.address, path, unconfirmed,
	); rejected.status != http.StatusUnprocessableEntity ||
		!strings.Contains(rejected.body, `role="alert"`) {
		t.Fatalf("unconfirmed Stage Message = %d %q", rejected.status, rejected.body)
	} else {
		assertAccessibleFormErrors(t, rejected, map[string]string{
			"override-confirmed": "Confirm Stage Message",
		})
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if count := strings.Count(page.body, "<h3>StageMessage</h3>"); count != 1 {
		t.Fatalf("exact Stage Message retry created %d active Overrides: %q", count, page.body)
	}
	clearStage := frontendNamedValues(
		page.body,
		"override_id",
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearStage.Set("csrf_token", requireFrontendCSRF(t, page))
	clearStage.Set("action", "clear")
	unconfirmedClear := postFrontendForm(
		t, administrator, server.address, path, clearStage,
	)
	if unconfirmedClear.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"unconfirmed Stage Message clear = %d %q",
			unconfirmedClear.status,
			unconfirmedClear.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedClear, map[string]string{
		"override-clear-" + clearStage.Get("override_id") + "-confirmed": "Confirm this action",
	})

	previewAndActivate(
		"technical-difficulties",
		"Technical Difficulties",
		url.Values{
			"text":             {"Please stand by"},
			"target_group_key": {"stage"},
			"duration_seconds": {"300"},
			"until_cleared":    {"true"},
		},
		"Replace",
	)
	previewAndActivate(
		"urgent-notice",
		"Urgent Notice",
		url.Values{
			"text":             {"Venue closes in ten minutes"},
			"target_type":      {"Event"},
			"target_id":        {"0"},
			"presentation":     {"Overlay"},
			"duration_seconds": {"300"},
		},
		"Overlay",
	)
	emergencyPath := "/crew/events/1/emergency-alerts/confirmation"
	invalidEmergency := getFrontendPage(
		t,
		administrator,
		server.address,
		emergencyPath+"?"+url.Values{
			"text":        {"Retain invalid Emergency Alert"},
			"target_type": {"Location"},
			"target_id":   {"not-a-number"},
		}.Encode(),
	)
	if invalidEmergency.status != http.StatusBadRequest ||
		!strings.Contains(invalidEmergency.body, "Retain invalid Emergency Alert") ||
		!strings.Contains(invalidEmergency.body, `value="not-a-number"`) {
		t.Fatalf(
			"invalid Emergency Alert preview = %d %q",
			invalidEmergency.status,
			invalidEmergency.body,
		)
	}
	assertAccessibleFormErrors(t, invalidEmergency, map[string]string{
		"emergency-target-id": "positive numeric target ID",
	})
	emergencyPreview := getFrontendPage(
		t,
		administrator,
		server.address,
		emergencyPath+"?"+url.Values{
			"text":        {"Evacuate using marked exits"},
			"target_type": {"Event"},
			"target_id":   {"0"},
		}.Encode(),
	)
	for _, want := range []string{
		"Confirm Emergency Alert",
		"Stage Screen",
		"Evacuate using marked exits",
		`name="preview_fingerprint"`,
		`href="/assets/frontend.css"`,
	} {
		if emergencyPreview.status != http.StatusOK ||
			!strings.Contains(emergencyPreview.body, want) {
			t.Fatalf(
				"Emergency Alert preview lacks %q: %d %q",
				want,
				emergencyPreview.status,
				emergencyPreview.body,
			)
		}
	}
	emergencyConfirmation := frontendNamedValues(
		emergencyPreview.body,
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	emergencyConfirmation.Set("confirmation_method", "Keyboard")
	staleEmergency := maps.Clone(emergencyConfirmation)
	staleEmergency.Set("build_version", "obsolete-build")
	staleEmergencyResponse := postFrontendForm(
		t, administrator, server.address, emergencyPath, staleEmergency,
	)
	if staleEmergencyResponse.status != http.StatusConflict ||
		staleEmergencyResponse.header.Get("X-Beamers-Build") != buildVersion ||
		!strings.Contains(staleEmergencyResponse.body, "Evacuate using marked exits") ||
		!strings.Contains(staleEmergencyResponse.body, `name="confirmation_method" value="Keyboard"`) {
		t.Fatalf(
			"stale Emergency Alert = %d %q",
			staleEmergencyResponse.status,
			staleEmergencyResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleEmergencyResponse, map[string]string{
		"emergency-alert-confirmation": "review this refreshed Emergency Alert",
	})
	emergencyConfirmation = frontendNamedValues(
		staleEmergencyResponse.body,
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	emergencyConfirmation.Set("confirmation_method", "Keyboard")
	emergency := postFrontendForm(
		t, administrator, server.address, emergencyPath, emergencyConfirmation,
	)
	if emergency.status != http.StatusSeeOther ||
		emergency.header.Get("Location") != path {
		t.Fatalf("activate Emergency Alert = %d %q", emergency.status, emergency.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Two minutes remaining",
		"Please stand by",
		"Venue closes in ten minutes",
		"Evacuate using marked exits",
		"Review and clear Emergency Alert",
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("recovered control page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	clearPath := regexp.MustCompile(
		`/crew/events/1/overrides/\d+/clear-confirmation`,
	).FindString(page.body)
	if clearPath == "" {
		t.Fatalf("Emergency Alert clear confirmation link missing: %q", page.body)
	}
	clearPreview := getFrontendPage(t, administrator, server.address, clearPath)
	if !strings.Contains(clearPreview.body, `href="/assets/frontend.css"`) {
		t.Fatalf("Emergency clear confirmation lacks Frontend styles: %q", clearPreview.body)
	}
	clearConfirmation := frontendNamedValues(
		clearPreview.body,
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearConfirmation.Set("confirmation_method", "Keyboard")
	staleClear := maps.Clone(clearConfirmation)
	staleClear.Set("build_version", "obsolete-build")
	staleClearResponse := postFrontendForm(
		t, administrator, server.address, clearPath, staleClear,
	)
	if staleClearResponse.status != http.StatusConflict ||
		staleClearResponse.header.Get("X-Beamers-Build") != buildVersion ||
		!strings.Contains(staleClearResponse.body, "Evacuate using marked exits") ||
		!strings.Contains(staleClearResponse.body, `name="confirmation_method" value="Keyboard"`) {
		t.Fatalf(
			"stale Emergency clear = %d %q",
			staleClearResponse.status,
			staleClearResponse.body,
		)
	}
	assertAccessibleFormErrors(t, staleClearResponse, map[string]string{
		"emergency-clear-confirmation": "review this refreshed Emergency Alert clear",
	})
	clearConfirmation = frontendNamedValues(
		staleClearResponse.body,
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearConfirmation.Set("confirmation_method", "Keyboard")
	cleared := postFrontendForm(
		t, administrator, server.address, clearPath, clearConfirmation,
	)
	if clearPreview.status != http.StatusOK ||
		cleared.status != http.StatusSeeOther ||
		cleared.header.Get("Location") != path {
		t.Fatalf(
			"clear Emergency Alert = preview %d, clear %d %q",
			clearPreview.status,
			cleared.status,
			cleared.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Contains(page.body, "Evacuate using marked exits") {
		t.Fatalf("cleared Emergency Alert remained active: %q", page.body)
	}
	prizegivingID := prepareBrowserPrizegiving(t, administrator, server)
	programClient := connectClient(programv1connect.NewProgramControlServiceClient, administrator, server.address)
	current, err := programClient.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: prizegivingID,
		}),
	)
	if err != nil {
		t.Fatalf("load browser Prizegiving Program Channel: %v", err)
	}
	claimed, err := programClient.ChangeControl(
		t.Context(),
		connect.NewRequest(&programv1.ChangeControlRequest{
			EventId: 1, SessionId: prizegivingID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "claim-browser-prizegiving-check",
			ExpectedControlStateRevision: current.Msg.GetChannel().GetControlStateRevision(),
		}),
	)
	if err != nil {
		t.Fatalf("claim browser Prizegiving Program Channel: %v", err)
	}
	taken, err := programClient.Take(
		t.Context(),
		connect.NewRequest(&programv1.TakeRequest{
			EventId: 1, SessionId: prizegivingID,
			CommandId:                    "take-browser-prizegiving-check",
			ExpectedLiveStateRevision:    claimed.Msg.GetChannel().GetLiveStateRevision(),
			ExpectedControlStateRevision: claimed.Msg.GetChannel().GetControlStateRevision(),
			Preview:                      claimed.Msg.GetChannel().GetPreview(),
		}),
	)
	if err != nil || taken.Msg.GetChannel().GetProgramOutput().GetResult() == nil {
		t.Fatalf("take browser Prizegiving Result: %+v, %v", taken, err)
	}
	beginBrowserReveal(t, administrator, server, prizegivingID)
	page = getFrontendPage(t, administrator, server.address, path)
	if prizegivingID <= 0 ||
		!strings.Contains(
			page.body,
			`/crew/program/`+strconv.FormatInt(prizegivingID, 10)+`?event_id=1`,
		) {
		t.Fatalf("browser Prizegiving Program Channel missing: %d %q", prizegivingID, page.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener control page = %d, want 404", public.status)
	}
	server.stop(t)
}

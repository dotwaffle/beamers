package acceptance_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestBrowserOperatesSessionDurably(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	path := "/backstage/events/1/operations"
	operator := provisionOperator(t, administrator, server)
	observer := provisionObserver(t, administrator, server)
	observerPage := getFrontendPage(t, observer, server.address, path)
	if observerPage.status != http.StatusForbidden {
		t.Fatalf("Observer Session operations = %d, want 403", observerPage.status)
	}
	operator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	page := getFrontendPage(t, operator, server.address, path)
	for _, want := range []string{
		"Sessions and Displays",
		"Opening Keynote",
		"Scheduled",
		`name="action" value="start-session"`,
		`name="expected_live_state_revision" value="0"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("operations page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	started := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-keynote"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther || started.header.Get("Location") != path {
		t.Fatalf("browser Start Session = %d %q", started.status, started.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	startedArticle := frontendSessionArticle(t, page.body, sessionID)
	if !strings.Contains(startedArticle, "Live") ||
		!strings.Contains(startedArticle, `name="action" value="end-session"`) ||
		!strings.Contains(startedArticle, `name="expected_live_state_revision" value="1"`) {
		t.Fatalf("started browser Session = %d %q", page.status, startedArticle)
	}
	stale := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-stale-start"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, `role="alert"`) ||
		!strings.Contains(stale.body, "Session changed") {
		t.Fatalf("stale browser Session command = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, operator, server.address, path)
	restartedArticle := frontendSessionArticle(t, page.body, sessionID)
	if page.status != http.StatusOK ||
		!strings.Contains(restartedArticle, "Opening Keynote") ||
		!strings.Contains(restartedArticle, "Live") {
		t.Fatalf("restarted browser Session = %d %q", page.status, restartedArticle)
	}
	ended := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"end-session"},
		"command_id":                   {"browser-end-keynote"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
	})
	if ended.status != http.StatusSeeOther {
		t.Fatalf("browser End Session = %d %q", ended.status, ended.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	endedArticle := frontendSessionArticle(t, page.body, sessionID)
	if !strings.Contains(endedArticle, `data-tone="ended">Ended</span>`) ||
		!strings.Contains(endedArticle, "revision <code>2</code>") {
		t.Fatalf("ended browser Session = %d %q", page.status, endedArticle)
	}
	server.stop(t)
}

func TestBrowserPreviewsAdjustsCancelsAndReinstatesSession(t *testing.T) {
	producer, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	producer.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	path := "/backstage/events/1/operations"

	page := getFrontendPage(t, producer, server.address, path)
	started := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"start-session"},
		"command_id":                   {"browser-start-before-adjustment"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start before browser adjustment = %d %q", started.status, started.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	invalidAdjustment := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-adjust-target"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
		"adjustment":                   {"not-a-duration"},
	})
	if invalidAdjustment.status != http.StatusBadRequest ||
		!strings.Contains(invalidAdjustment.body, `value="not-a-duration"`) {
		t.Fatalf(
			"invalid target adjustment = %d %q",
			invalidAdjustment.status,
			invalidAdjustment.body,
		)
	}
	assertAccessibleFormErrors(t, invalidAdjustment, map[string]string{
		"preview-adjust-target-" + strconv.FormatInt(sessionID, 10) + "-adjustment": "Enter a duration",
	})

	preview := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-adjust-target"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"1"},
		"adjustment":                   {"5m"},
	})
	for _, want := range []string{
		"Adjust Target Preview",
		"Hard Boundary",
		`data-timezone="Europe/Berlin"`,
		`name="action" value="adjust-target"`,
		`name="preview_fingerprint"`,
	} {
		if preview.status != http.StatusOK || !strings.Contains(preview.body, want) {
			t.Fatalf("Adjust Target Preview lacks %q: %d %q", want, preview.status, preview.body)
		}
	}
	adjustment := frontendNamedValues(
		preview.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	adjustment.Set("csrf_token", requireFrontendCSRF(t, preview))
	adjustment.Set("action", "adjust-target")
	unconfirmedAdjustment := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		adjustment,
	)
	if unconfirmedAdjustment.status != http.StatusConflict ||
		!strings.Contains(unconfirmedAdjustment.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedAdjustment.body) {
		t.Fatalf(
			"unconfirmed Adjust Target = %d %q",
			unconfirmedAdjustment.status,
			unconfirmedAdjustment.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedAdjustment, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm Adjust Target",
	})

	staleAdjustment := frontendNamedValues(
		unconfirmedAdjustment.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	staleAdjustment.Set("csrf_token", requireFrontendCSRF(t, unconfirmedAdjustment))
	staleAdjustment.Set("action", "adjust-target")
	staleAdjustment.Set("preview_fingerprint", "stale-target-preview")
	staleAdjustment.Set("confirmed", "true")
	staleAdjustment.Set("hard_boundary_confirmed", "true")
	staleTarget := postFrontendForm(t, producer, server.address, path, staleAdjustment)
	if staleTarget.status != http.StatusConflict ||
		!strings.Contains(staleTarget.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(staleTarget.body) {
		t.Fatalf("stale Adjust Target = %d %q", staleTarget.status, staleTarget.body)
	}
	assertAccessibleFormErrors(t, staleTarget, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "review and confirm",
	})

	hardBoundaryAdjustment := frontendNamedValues(
		staleTarget.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	hardBoundaryAdjustment.Set("csrf_token", requireFrontendCSRF(t, staleTarget))
	hardBoundaryAdjustment.Set("action", "adjust-target")
	hardBoundaryAdjustment.Set("confirmed", "true")
	missingHardBoundary := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		hardBoundaryAdjustment,
	)
	if missingHardBoundary.status != http.StatusConflict ||
		!strings.Contains(missingHardBoundary.body, "Adjust Target Preview") ||
		regexp.MustCompile(
			`id="adjust-target-`+strconv.FormatInt(sessionID, 10)+
				`-hard-boundary-confirmed"[^>]+checked`,
		).MatchString(missingHardBoundary.body) {
		t.Fatalf(
			"unconfirmed Hard Boundary adjustment = %d %q",
			missingHardBoundary.status,
			missingHardBoundary.body,
		)
	}
	assertAccessibleFormErrors(t, missingHardBoundary, map[string]string{
		"adjust-target-" + strconv.FormatInt(sessionID, 10) +
			"-hard-boundary-confirmed": "Confirm Hard Boundary movement",
	})

	adjustment = frontendNamedValues(
		missingHardBoundary.body,
		"session_id",
		"expected_live_state_revision",
		"adjustment",
		"preview_fingerprint",
		"command_id",
	)
	adjustment.Set("csrf_token", requireFrontendCSRF(t, missingHardBoundary))
	adjustment.Set("action", "adjust-target")
	adjustment.Set("confirmed", "true")
	adjustment.Set("hard_boundary_confirmed", "true")
	adjusted := postFrontendForm(t, producer, server.address, path, adjustment)
	if adjusted.status != http.StatusSeeOther {
		t.Fatalf("browser Adjust Target = %d %q", adjusted.status, adjusted.body)
	}

	page = getFrontendPage(t, producer, server.address, path)
	unconfirmedCancellation := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"cancel-session"},
		"command_id":                   {"browser-unconfirmed-cancellation"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"2"},
		"public_cancellation_message":  {"Retain this public message."},
		"crew_notes":                   {"Retain these Crew notes."},
	})
	if unconfirmedCancellation.status != http.StatusUnprocessableEntity ||
		!strings.Contains(
			unconfirmedCancellation.body,
			`value="Retain this public message."`,
		) ||
		!strings.Contains(
			unconfirmedCancellation.body,
			">Retain these Crew notes.</textarea>",
		) ||
		regexp.MustCompile(
			`id="cancel-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedCancellation.body) {
		t.Fatalf(
			"unconfirmed cancellation = %d %q",
			unconfirmedCancellation.status,
			unconfirmedCancellation.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedCancellation, map[string]string{
		"cancel-session-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm cancellation",
	})

	canceled := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"cancel-session"},
		"command_id":                   {"browser-cancel-after-adjustment"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"2"},
		"confirmed":                    {"true"},
		"public_cancellation_message":  {"Speaker delayed."},
		"crew_notes":                   {"Move to the next open placement."},
	})
	if canceled.status != http.StatusSeeOther {
		t.Fatalf("browser Cancel Session = %d %q", canceled.status, canceled.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	canceledArticle := frontendSessionArticle(t, page.body, sessionID)
	if !strings.Contains(canceledArticle, `data-tone="canceled">Canceled</span>`) ||
		!strings.Contains(canceledArticle, "revision <code>3</code>") ||
		!strings.Contains(canceledArticle, `name="action" value="preview-reinstate"`) {
		t.Fatalf("canceled browser Session = %d %q", page.status, canceledArticle)
	}
	if !regexp.MustCompile(`type="checkbox"\s+name="lane_ids"`).MatchString(canceledArticle) ||
		!regexp.MustCompile(`type="checkbox"\s+name="location_ids"`).MatchString(canceledArticle) ||
		!strings.Contains(canceledArticle, "Main Lane") ||
		!strings.Contains(canceledArticle, "Main Hall") {
		t.Fatalf(
			"Reinstate Lane/Location IDs lack a named checkbox picker: %d %q",
			page.status,
			canceledArticle,
		)
	}
	invalidLaneIDs := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, page)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"not-lane-ids"},
		"location_ids":                 {"1"},
	})
	if invalidLaneIDs.status != http.StatusBadRequest {
		t.Fatalf("invalid Reinstate Lane IDs = %d %q", invalidLaneIDs.status, invalidLaneIDs.body)
	}
	assertAccessibleFormErrors(t, invalidLaneIDs, map[string]string{
		"preview-reinstate-" + strconv.FormatInt(sessionID, 10) + "-lane-ids": "Select at least one Lane",
	})

	invalidLocationIDs := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, invalidLaneIDs)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"1"},
		"location_ids":                 {"not-location-ids"},
	})
	if invalidLocationIDs.status != http.StatusBadRequest {
		t.Fatalf(
			"invalid Reinstate Location IDs = %d %q",
			invalidLocationIDs.status,
			invalidLocationIDs.body,
		)
	}
	assertAccessibleFormErrors(t, invalidLocationIDs, map[string]string{
		"preview-reinstate-" + strconv.FormatInt(sessionID, 10) + "-location-ids": "Select at least one Location",
	})

	reinstatePreview := postFrontendForm(t, producer, server.address, path, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, invalidLocationIDs)},
		"action":                       {"preview-reinstate"},
		"session_id":                   {strconv.FormatInt(sessionID, 10)},
		"expected_live_state_revision": {"3"},
		"forecast_start":               {"2099-08-21T11:30"},
		"lane_ids":                     {"1"},
		"location_ids":                 {"1"},
	})
	for _, want := range []string{
		"Reinstate Session Preview",
		"Hard Boundary",
		`name="action" value="reinstate-session"`,
		`name="preview_fingerprint"`,
	} {
		if reinstatePreview.status != http.StatusOK ||
			!strings.Contains(reinstatePreview.body, want) {
			t.Fatalf(
				"Reinstate Session Preview lacks %q: %d %q",
				want,
				reinstatePreview.status,
				reinstatePreview.body,
			)
		}
	}
	reinstatement := frontendNamedValues(
		reinstatePreview.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	reinstatement.Set("csrf_token", requireFrontendCSRF(t, reinstatePreview))
	reinstatement.Set("action", "reinstate-session")
	unconfirmedReinstatement := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		reinstatement,
	)
	if unconfirmedReinstatement.status != http.StatusConflict ||
		!strings.Contains(unconfirmedReinstatement.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(unconfirmedReinstatement.body) {
		t.Fatalf(
			"unconfirmed Reinstate Session = %d %q",
			unconfirmedReinstatement.status,
			unconfirmedReinstatement.body,
		)
	}
	assertAccessibleFormErrors(t, unconfirmedReinstatement, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) + "-confirmed": "Confirm Reinstate",
	})

	staleReinstatement := frontendNamedValues(
		unconfirmedReinstatement.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	staleReinstatement.Set("csrf_token", requireFrontendCSRF(t, unconfirmedReinstatement))
	staleReinstatement.Set("action", "reinstate-session")
	staleReinstatement.Set("preview_fingerprint", "stale-reinstate-preview")
	staleReinstatement.Set("confirmed", "true")
	staleReinstatement.Set("hard_boundary_confirmed", "true")
	staleReinstate := postFrontendForm(t, producer, server.address, path, staleReinstatement)
	if staleReinstate.status != http.StatusConflict ||
		!strings.Contains(staleReinstate.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-confirmed"[^>]+checked`,
		).MatchString(staleReinstate.body) {
		t.Fatalf("stale Reinstate Session = %d %q", staleReinstate.status, staleReinstate.body)
	}
	assertAccessibleFormErrors(t, staleReinstate, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) +
			"-confirmed": "review and confirm",
	})

	hardBoundaryReinstatement := frontendNamedValues(
		staleReinstate.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	hardBoundaryReinstatement.Set("csrf_token", requireFrontendCSRF(t, staleReinstate))
	hardBoundaryReinstatement.Set("action", "reinstate-session")
	hardBoundaryReinstatement.Set("confirmed", "true")
	missingReinstateHardBoundary := postFrontendForm(
		t,
		producer,
		server.address,
		path,
		hardBoundaryReinstatement,
	)
	if missingReinstateHardBoundary.status != http.StatusConflict ||
		!strings.Contains(missingReinstateHardBoundary.body, "Reinstate Session Preview") ||
		regexp.MustCompile(
			`id="reinstate-session-`+strconv.FormatInt(sessionID, 10)+
				`-hard-boundary-confirmed"[^>]+checked`,
		).MatchString(missingReinstateHardBoundary.body) {
		t.Fatalf(
			"unconfirmed Reinstate Hard Boundary = %d %q",
			missingReinstateHardBoundary.status,
			missingReinstateHardBoundary.body,
		)
	}
	assertAccessibleFormErrors(t, missingReinstateHardBoundary, map[string]string{
		"reinstate-session-" + strconv.FormatInt(sessionID, 10) +
			"-hard-boundary-confirmed": "Confirm Hard Boundary movement",
	})

	reinstatement = frontendNamedValues(
		missingReinstateHardBoundary.body,
		"session_id",
		"expected_live_state_revision",
		"forecast_start",
		"lane_ids",
		"location_ids",
		"preview_fingerprint",
		"command_id",
	)
	reinstatement.Set("csrf_token", requireFrontendCSRF(t, missingReinstateHardBoundary))
	reinstatement.Set("action", "reinstate-session")
	reinstatement.Set("confirmed", "true")
	reinstatement.Set("hard_boundary_confirmed", "true")
	reinstated := postFrontendForm(t, producer, server.address, path, reinstatement)
	if reinstated.status != http.StatusSeeOther {
		t.Fatalf("browser Reinstate Session = %d %q", reinstated.status, reinstated.body)
	}
	page = getFrontendPage(t, producer, server.address, path)
	reinstatedArticle := frontendSessionArticle(t, page.body, sessionID)
	if !strings.Contains(reinstatedArticle, `data-tone="neutral">Scheduled</span>`) ||
		!strings.Contains(reinstatedArticle, "revision <code>4</code>") {
		t.Fatalf("reinstated browser Session = %d %q", page.status, reinstatedArticle)
	}
	server.stop(t)
}

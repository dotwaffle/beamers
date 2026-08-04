package acceptance_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func enrollAndAssignDisplay(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
	name string,
	viewKey string,
) *http.Client {
	t.Helper()
	displayClient := authenticatedClient(t)
	enrollment := get(t, displayClient, server.address, "/display")
	body, readErr := io.ReadAll(enrollment.Body)
	closeErr := enrollment.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display Enrollment page: %v", err)
	}
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(string(body))
	claimed := postForm(t, administrator, server.address, url.Values{
		"code": {code}, "name": {name}, "command_id": {"claim-snapshot-display"},
		"build_version": {crewBuild(t, administrator, server.address)},
	})
	if closeErr := claimed.Body.Close(); closeErr != nil {
		t.Errorf("close Display claim response: %v", closeErr)
	}
	if claimed.StatusCode != http.StatusCreated {
		t.Fatalf("claim Display = %d, want %d", claimed.StatusCode, http.StatusCreated)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": viewKey,
			"command_id": "assign-snapshot-display",
		},
		http.StatusOK,
		fmt.Sprintf(
			"{\"display_id\":1,\"event_id\":1,\"location_id\":1,\"view_key\":%q}\n",
			viewKey,
		),
	)
	return displayClient
}

func TestStageMessagesAndTechnicalDifficultiesOverrideCurrentDisplayView(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Stage Display", "stage-timer",
	)
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	started, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "start-override-session",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	))
	if err != nil {
		t.Fatalf("start Session beneath Overrides: %v", err)
	}
	configured := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/stage-message-configuration",
		map[string]any{
			"default_duration_seconds": 10, "expected_revision": 0,
			"command_id": "configure-stage-message-presets",
			"presets": []map[string]any{{
				"key": "wrap", "text": "Please wrap up",
				"target_group_key": "crew", "duration_seconds": 15,
				"emphasis": "Attention",
			}},
		},
	)
	if configured.status != http.StatusOK {
		t.Fatalf("configure Stage Message preset = %d: %s", configured.status, configured.body)
	}
	preview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages/preview",
		map[string]any{"preset_key": "wrap", "until_cleared": true},
	)
	if preview.status != http.StatusOK ||
		!strings.Contains(preview.body, `"text":"Please wrap up"`) ||
		!strings.Contains(preview.body, `"displays":[{"id":1,"name":"Stage Display","view_key":"stage-timer"}]`) {
		t.Fatalf("preview Stage Message targets = %d: %s", preview.status, preview.body)
	}
	operator := provisionOperator(t, administrator, server)
	denied := requestJSON(
		t.Context(), operator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "unauthorized", "target_group_key": "crew",
			"until_cleared": true, "command_id": "deny-unscoped-stage-message",
		},
	)
	if denied.status != http.StatusForbidden {
		t.Fatalf("unscoped Operator Stage Message = %d: %s", denied.status, denied.body)
	}
	preset := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"preset_key": "wrap", "until_cleared": true,
			"command_id": "send-preset-stage-message",
		},
	)
	var presetOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(preset.body), &presetOverride); err != nil ||
		preset.status != http.StatusOK || presetOverride.ID <= 0 {
		t.Fatalf("send preset Stage Message = %d: %s (%v)", preset.status, preset.body, err)
	}
	page := readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, `data-stage-message`) ||
		!strings.Contains(page, `data-emphasis="Attention"`) ||
		!strings.Contains(page, "Attention:") ||
		!strings.Contains(page, "Please wrap up") {
		t.Fatalf("preset Stage Message missing accessible emphasis: %s", page)
	}
	replacement := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "Stop now", "target_group_key": "crew",
			"emphasis": "Urgent", "until_cleared": true,
			"command_id": "replace-stage-message",
		},
	)
	var replacementOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(replacement.body), &replacementOverride); err != nil ||
		replacement.status != http.StatusOK || replacementOverride.ID <= presetOverride.ID {
		t.Fatalf("replace Stage Message = %d: %s (%v)", replacement.status, replacement.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Urgent:") || !strings.Contains(page, "Stop now") ||
		strings.Contains(page, "Please wrap up") {
		t.Fatalf("replacement Stage Message queued old content: %s", page)
	}

	reassignedPublic := requestJSON(
		t.Context(), administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "event-overview",
			"display_group_keys": []string{"crew"},
			"command_id":         "assign-override-display-public",
		},
	)
	if reassignedPublic.status != http.StatusOK {
		t.Fatalf("assign public Display to crew group = %d: %s", reassignedPublic.status, reassignedPublic.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Stop now") || strings.Contains(page, `data-stage-message`) {
		t.Fatalf("Stage Message leaked to public Display: %s", page)
	}
	activeOverrides := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if activeOverrides.status != http.StatusOK ||
		!strings.Contains(activeOverrides.body, fmt.Sprintf(`"id":%d`, replacementOverride.ID)) ||
		!strings.Contains(activeOverrides.body, `"displays":[]`) {
		t.Fatalf("active Override targets after membership change = %d: %s", activeOverrides.status, activeOverrides.body)
	}
	reassignedCrew := requestJSON(
		t.Context(), administrator, server.address, "/admin/displays/1/assign",
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "stage-timer",
			"display_group_keys": []string{"crew"},
			"command_id":         "restore-override-display-crew",
		},
	)
	if reassignedCrew.status != http.StatusOK {
		t.Fatalf("restore crew Display Assignment = %d: %s", reassignedCrew.status, reassignedCrew.body)
	}
	if page = readDisplayHTML(t, displayClient, server.address); !strings.Contains(page, "Stop now") {
		t.Fatalf("live Display Group membership did not restore current Stage Message: %s", page)
	}
	activeOverrides = requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if activeOverrides.status != http.StatusOK ||
		!strings.Contains(activeOverrides.body, `"displays":[{"id":1,"name":"Stage Display","view_key":"stage-timer"}]`) {
		t.Fatalf("active Override targets after membership restore = %d: %s", activeOverrides.status, activeOverrides.body)
	}

	technical := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/technical-difficulties",
		map[string]any{
			"target_group_key": "crew", "until_cleared": true,
			"command_id": "activate-technical-difficulties",
		},
	)
	var technicalOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err = json.Unmarshal([]byte(technical.body), &technicalOverride); err != nil ||
		technical.status != http.StatusOK || technicalOverride.ID <= 0 {
		t.Fatalf("activate Technical Difficulties = %d: %s (%v)", technical.status, technical.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, `data-override-kind="TechnicalDifficulties"`) ||
		!strings.Contains(page, "Technical Difficulties") ||
		!strings.Contains(page, "Stop now") ||
		strings.Contains(page, `<time data-stage-timer-clock`) {
		t.Fatalf("Technical Difficulties Replace with Stage Message Overlay = %s", page)
	}
	appliedOverrides := readDisplaySnapshot(t, displayClient, server.address)
	if appliedOverrides.StageMessage.ID != strconv.Itoa(replacementOverride.ID) ||
		appliedOverrides.TechnicalDifficulties.ID != strconv.Itoa(technicalOverride.ID) {
		t.Fatalf("Display Override Snapshot = %+v", appliedOverrides)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedOverrides)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_stage_message_id":%d`, replacementOverride.ID),
	)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_technical_difficulties_id":%d`, technicalOverride.ID),
	)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	sessionClient = sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Technical Difficulties") || !strings.Contains(page, "Stop now") {
		t.Fatalf("Overrides did not survive restart: %s", page)
	}
	clearedStage := requestJSON(
		t.Context(), administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear", replacementOverride.ID),
		map[string]any{
			"expected_revision": replacementOverride.Revision,
			"command_id":        "clear-stage-message",
		},
	)
	if clearedStage.status != http.StatusOK {
		t.Fatalf("clear Stage Message = %d: %s", clearedStage.status, clearedStage.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Stop now") || strings.Contains(page, "Please wrap up") ||
		!strings.Contains(page, "Technical Difficulties") {
		t.Fatalf("clearing Stage Message revealed queued content or cleared Replace: %s", page)
	}
	clearedTechnical := requestJSON(
		t.Context(), administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear", technicalOverride.ID),
		map[string]any{
			"expected_revision": technicalOverride.Revision,
			"command_id":        "clear-technical-difficulties",
		},
	)
	if clearedTechnical.status != http.StatusOK {
		t.Fatalf("clear Technical Difficulties = %d: %s", clearedTechnical.status, clearedTechnical.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Technical Difficulties") ||
		!strings.Contains(page, `<time data-stage-timer-clock`) {
		t.Fatalf("clearing Replace did not restore current Stage Timer: %s", page)
	}
	expiring := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/stage-messages",
		map[string]any{
			"text": "One second", "target_group_key": "crew",
			"duration_seconds": 1, "emphasis": "Normal",
			"command_id": "send-expiring-stage-message",
		},
	)
	if expiring.status != http.StatusOK {
		t.Fatalf("send expiring Stage Message = %d: %s", expiring.status, expiring.body)
	}
	var expiringOverride struct {
		ID int `json:"id"`
	}
	if err = json.Unmarshal([]byte(expiring.body), &expiringOverride); err != nil ||
		expiringOverride.ID <= 0 {
		t.Fatalf("decode expiring Stage Message = %s: %v", expiring.body, err)
	}
	if page = readDisplayHTML(t, displayClient, server.address); !strings.Contains(page, "One second") {
		t.Fatalf("expiring Stage Message not initially visible: %s", page)
	}
	appliedExpiring := readDisplaySnapshot(t, displayClient, server.address)
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedExpiring)
	if fixtureErr := storetest.SetDisplayOverrideExpiry(
		t.Context(), filepath.Join(server.dataDir, "beamers.db"), expiringOverride.ID, time.Unix(1, 0),
	); fixtureErr != nil {
		t.Fatalf("expire Stage Message fixture: %v", fixtureErr)
	}
	appliedExpired := readDisplaySnapshot(t, displayClient, server.address)
	if appliedExpired.StreamPosition != appliedExpiring.StreamPosition ||
		appliedExpired.StageMessage.ID != "" {
		t.Fatalf("expired same-cursor Display Snapshot = %+v", appliedExpired)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, appliedExpired)
	stale := requestDisplayAcknowledgment(
		t, displayClient, server.address, appliedExpiring, displayHealth{},
	)
	if stale.status != http.StatusBadRequest ||
		!strings.Contains(stale.body, `"code":"failed_precondition"`) {
		t.Fatalf("stale same-cursor Override acknowledgment = %d: %s", stale.status, stale.body)
	}
	if page = readDisplayHTML(t, displayClient, server.address); strings.Contains(page, "One second") {
		t.Fatalf("expired Stage Message remained visible: %s", page)
	}
	ended, err := sessionClient.EndSession(t.Context(), connect.NewRequest(
		&sessionv1.EndSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "end-session-after-overrides",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
		},
	))
	if err != nil || ended.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Fatalf("Overrides mutated Session live state = %+v, %v", ended, err)
	}
	server.stop(t)
}

func TestUrgentNoticesAndEmergencyAlertsTargetCurrentDisplays(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Priority Display", "stage-timer",
	)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	assign := func(viewKey string, groups []string, commandID string) {
		t.Helper()
		result := requestJSON(
			t.Context(), administrator, server.address, "/admin/displays/1/assign",
			map[string]any{
				"event_id": 1, "location_id": 1, "view_key": viewKey,
				"display_group_keys": groups, "command_id": commandID,
			},
		)
		if result.status != http.StatusOK {
			t.Fatalf("assign Priority Display = %d: %s", result.status, result.body)
		}
	}
	previewTarget := func(target map[string]any, wantDisplay bool) {
		t.Helper()
		result := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/urgent-notices/preview",
			map[string]any{
				"target": target, "text": "Scope preview",
				"presentation": "Overlay", "until_cleared": true,
			},
		)
		hasDisplay := strings.Contains(result.body, `"id":1,"name":"Priority Display"`)
		if result.status != http.StatusOK || hasDisplay != wantDisplay {
			t.Fatalf("preview target %v = %d: %s", target, result.status, result.body)
		}
	}

	previewTarget(map[string]any{"type": "Event"}, true)
	previewTarget(map[string]any{"type": "Crew"}, true)
	previewTarget(map[string]any{"type": "Public"}, false)
	previewTarget(map[string]any{"type": "Location", "id": 1}, true)
	previewTarget(map[string]any{"type": "Lane", "id": 1}, true)
	previewTarget(map[string]any{"type": "Display", "id": 1}, true)
	assign("stage-timer", []string{"ops"}, "assign-priority-custom-group")
	previewTarget(map[string]any{"type": "DisplayGroup", "key": "ops"}, true)
	assign("event-overview", nil, "assign-priority-public")
	previewTarget(map[string]any{"type": "Public"}, true)
	assign("competition-output", nil, "assign-priority-program")
	previewTarget(map[string]any{"type": "ProgramChannel", "id": competitionID}, true)
	assign("stage-timer", []string{"ops"}, "restore-priority-stage")

	technical := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/technical-difficulties",
		map[string]any{
			"target_group_key": "crew", "until_cleared": true,
			"command_id": "priority-technical",
		},
	)
	if technical.status != http.StatusOK {
		t.Fatalf("activate underlying Technical Difficulties = %d: %s", technical.status, technical.body)
	}
	stage := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "Stage underneath", "target_group_key": "crew",
			"until_cleared": true, "command_id": "priority-stage-message",
		},
	)
	if stage.status != http.StatusOK {
		t.Fatalf("activate underlying Stage Message = %d: %s", stage.status, stage.body)
	}
	overlay := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/urgent-notices",
		map[string]any{
			"target": map[string]any{"type": "DisplayGroup", "key": "ops"},
			"text":   "Urgent overlay", "presentation": "Overlay",
			"until_cleared": true, "command_id": "activate-urgent-overlay",
		},
	)
	var overlayOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(overlay.body), &overlayOverride); err != nil ||
		overlay.status != http.StatusOK || overlayOverride.ID <= 0 {
		t.Fatalf("activate Urgent Overlay = %d: %s (%v)", overlay.status, overlay.body, err)
	}
	page := readDisplayHTML(t, displayClient, server.address)
	for _, want := range []string{"Technical Difficulties", "Stage underneath", "Urgent overlay"} {
		if !strings.Contains(page, want) {
			t.Fatalf("Urgent Overlay did not compose above lower content %q: %s", want, page)
		}
	}
	assign("stage-timer", nil, "leave-priority-custom-group")
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Urgent overlay") {
		t.Fatalf("logical Urgent target did not re-resolve after leave: %s", page)
	}
	active := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/overrides", nil,
	)
	if active.status != http.StatusOK ||
		!strings.Contains(active.body, fmt.Sprintf(`"id":%d`, overlayOverride.ID)) ||
		!strings.Contains(active.body, `"displays":[]`) {
		t.Fatalf("active Urgent membership after leave = %d: %s", active.status, active.body)
	}

	individual := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/urgent-notices",
		map[string]any{
			"target": map[string]any{"type": "Display", "id": 1},
			"text":   "Fixed Display notice", "presentation": "Replace",
			"until_cleared": true, "command_id": "activate-individual-urgent",
		},
	)
	var individualOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(individual.body), &individualOverride); err != nil ||
		individual.status != http.StatusOK || individualOverride.ID <= overlayOverride.ID {
		t.Fatalf("activate individual Urgent Replace = %d: %s (%v)", individual.status, individual.body, err)
	}
	rejectedOverrides := []struct {
		method string
		path   string
		body   map[string]any
		action string
	}{
		{
			method: http.MethodPatch,
			path:   "/crew/events/1/stage-message-configuration",
			body: map[string]any{
				"expected_revision": -1,
				"command_id":        "reject-invalid-stage-message-configuration",
			},
			action: "ConfigureStageMessages",
		},
		{
			path: "/crew/events/1/stage-messages",
			body: map[string]any{
				"text": "invalid", "target_group_key": "crew",
				"duration_seconds": -1, "command_id": "reject-invalid-stage-message",
			},
			action: "SendStageMessage",
		},
		{
			path: "/crew/events/1/technical-difficulties",
			body: map[string]any{
				"text": "invalid", "duration_seconds": 0,
				"command_id": "reject-technical",
			},
			action: "ActivateTechnicalDifficulties",
		},
		{
			path: "/crew/events/1/urgent-notices",
			body: map[string]any{
				"target": map[string]any{"type": "Event"}, "text": "invalid",
				"duration_seconds": -1, "command_id": "reject-invalid-urgent",
			},
			action: "ActivateUrgentNotice",
		},
		{
			path: fmt.Sprintf(
				"/crew/events/1/overrides/%d/clear",
				individualOverride.ID,
			),
			body: map[string]any{
				"expected_revision": 0, "command_id": "reject-invalid-clear",
			},
			action: "ClearDisplayOverride",
		},
	}
	for _, rejected := range rejectedOverrides {
		for range 2 {
			method := rejected.method
			if method == "" {
				method = http.MethodPost
			}
			response := requestJSONMethod(
				t.Context(), method, administrator, server.address,
				rejected.path, rejected.body,
			)
			if response.status != http.StatusUnprocessableEntity {
				t.Fatalf(
					"%s invalid input = %d: %s",
					rejected.action, response.status, response.body,
				)
			}
		}
	}
	assign("event-overview", []string{"other"}, "move-fixed-priority-display")
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Fixed Display notice") ||
		strings.Contains(page, "Technical Difficulties") ||
		strings.Contains(page, "Stage underneath") {
		t.Fatalf("individual Urgent Replace did not remain fixed or suppress lower content: %s", page)
	}

	emergencyPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate using marked exits",
		},
	)
	var preview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
	}
	if err := json.Unmarshal([]byte(emergencyPreview.body), &preview); err != nil ||
		emergencyPreview.status != http.StatusOK || preview.ConfirmationFingerprint == "" {
		t.Fatalf("preview Emergency Alert = %d: %s (%v)", emergencyPreview.status, emergencyPreview.body, err)
	}
	confirmationPage := get(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation?target_type=Event&text="+
			url.QueryEscape("Evacuate using marked exits"),
	)
	confirmationBody, readErr := io.ReadAll(confirmationPage.Body)
	closeErr := confirmationPage.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		confirmationPage.StatusCode != http.StatusOK ||
		!bytes.Contains(confirmationBody, []byte(`value="Keyboard"`)) ||
		!bytes.Contains(confirmationBody, []byte("Priority Display")) {
		t.Fatalf(
			"keyboard Emergency confirmation page = %d: %s (%v)",
			confirmationPage.StatusCode, confirmationBody, err,
		)
	}
	unconfirmedEmergency := map[string]any{
		"target": map[string]any{"type": "Event"},
		"text":   "Evacuate using marked exits", "preview_fingerprint": preview.ConfirmationFingerprint,
		"command_id": "reject-unconfirmed-emergency",
	}
	for range 2 {
		unconfirmed := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/emergency-alerts", unconfirmedEmergency,
		)
		if unconfirmed.status != http.StatusUnprocessableEntity {
			t.Fatalf("unconfirmed Emergency Alert = %d: %s", unconfirmed.status, unconfirmed.body)
		}
	}
	for range 2 {
		missingFingerprint := requestJSON(
			t.Context(), administrator, server.address,
			"/crew/events/1/emergency-alerts",
			map[string]any{
				"target": map[string]any{"type": "Event"},
				"text":   "Evacuate using marked exits", "confirmed": true,
				"confirmation_method": "Keyboard",
				"command_id":          "reject-missing-emergency-fingerprint",
			},
		)
		if missingFingerprint.status != http.StatusUnprocessableEntity {
			t.Fatalf(
				"missing Emergency fingerprint = %d: %s",
				missingFingerprint.status, missingFingerprint.body,
			)
		}
	}
	rejectedEntries, _ := readAuditHistory(t, administrator, server.address)
	rejectedCounts := map[string]int{
		"ConfigureStageMessages":        1,
		"SendStageMessage":              1,
		"ActivateTechnicalDifficulties": 1,
		"ActivateUrgentNotice":          1,
		"ClearDisplayOverride":          1,
		"ActivateEmergencyAlert":        2,
	}
	for _, entry := range rejectedEntries {
		if entry.Outcome != "Rejected" {
			continue
		}
		if _, expected := rejectedCounts[entry.Action]; !expected {
			continue
		}
		if entry.Reason != "override_invalid_input" {
			t.Errorf("%s rejection reason = %q", entry.Action, entry.Reason)
		}
		rejectedCounts[entry.Action]--
	}
	for action, remaining := range rejectedCounts {
		if remaining != 0 {
			t.Errorf("%s rejected Audit Entry count remaining = %d", action, remaining)
		}
	}
	emergency := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate using marked exits", "preview_fingerprint": preview.ConfirmationFingerprint,
			"confirmed": true, "confirmation_method": "Keyboard",
			"command_id": "activate-confirmed-emergency",
		},
	)
	var emergencyOverride struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal([]byte(emergency.body), &emergencyOverride); err != nil ||
		emergency.status != http.StatusOK || emergencyOverride.ID <= individualOverride.ID {
		t.Fatalf("activate Emergency Alert = %d: %s (%v)", emergency.status, emergency.body, err)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Emergency Alert") ||
		!strings.Contains(page, "Evacuate using marked exits") ||
		strings.Contains(page, "Fixed Display notice") ||
		strings.Contains(page, "Technical Difficulties") {
		t.Fatalf("Emergency Alert did not suppress every lower priority: %s", page)
	}
	applied := readDisplaySnapshot(t, displayClient, server.address)
	if applied.EmergencyAlert.ID != strconv.Itoa(emergencyOverride.ID) {
		t.Fatalf("Emergency Alert Display Snapshot = %+v", applied)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, applied)
	assertDisplayListContains(
		t, administrator, server.address,
		fmt.Sprintf(`"applied_emergency_alert_id":%d`, emergencyOverride.ID),
	)

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	page = readDisplayHTML(t, displayClient, server.address)
	if !strings.Contains(page, "Evacuate using marked exits") {
		t.Fatalf("Emergency Alert did not survive restart: %s", page)
	}
	clearPath := fmt.Sprintf(
		"/crew/events/1/overrides/%d/clear", emergencyOverride.ID,
	)
	clearConfirmation := get(
		t, administrator, server.address,
		fmt.Sprintf(
			"/crew/events/1/overrides/%d/clear-confirmation",
			emergencyOverride.ID,
		),
	)
	clearConfirmationBody, readErr := io.ReadAll(clearConfirmation.Body)
	closeErr = clearConfirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		clearConfirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(clearConfirmationBody, []byte(`value="Keyboard"`)) ||
		!bytes.Contains(clearConfirmationBody, []byte("Clear Emergency Alert")) {
		t.Fatalf(
			"keyboard Emergency clear confirmation page = %d: %s (%v)",
			clearConfirmation.StatusCode, clearConfirmationBody, err,
		)
	}
	rejectedClear := requestJSON(
		t.Context(), administrator, server.address, clearPath,
		map[string]any{
			"expected_revision": emergencyOverride.Revision,
			"command_id":        "reject-unconfirmed-emergency-clear",
		},
	)
	if rejectedClear.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Emergency clear = %d: %s", rejectedClear.status, rejectedClear.body)
	}
	cleared := requestJSON(
		t.Context(), administrator, server.address, clearPath,
		map[string]any{
			"expected_revision": emergencyOverride.Revision,
			"confirmed":         true, "confirmation_method": "Keyboard",
			"command_id": "clear-confirmed-emergency",
		},
	)
	if cleared.status != http.StatusOK {
		t.Fatalf("confirmed Emergency clear = %d: %s", cleared.status, cleared.body)
	}
	page = readDisplayHTML(t, displayClient, server.address)
	if strings.Contains(page, "Emergency Alert") ||
		!strings.Contains(page, "Fixed Display notice") {
		t.Fatalf("Emergency clear did not restore current underlying Urgent Replace: %s", page)
	}
	server.stop(t)
}

func TestEmergencyAlertSurvivesPartialStorageFailureAndRecoversEvidence(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Degraded Display", "event-overview",
	)
	beforeFailure := readDisplaySnapshot(t, displayClient, server.address)
	if beforeFailure.EmergencyAlert.ID != "" {
		t.Fatalf("initial Display Snapshot has Emergency Alert: %+v", beforeFailure)
	}

	healthyPreviewResult := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate through the north exit",
		},
	)
	var healthyPreview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
		Nondurable              bool   `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(healthyPreviewResult.body), &healthyPreview); err != nil ||
		healthyPreviewResult.status != http.StatusOK ||
		healthyPreview.ConfirmationFingerprint == "" || healthyPreview.Nondurable {
		t.Fatalf(
			"preview durable Emergency Alert = %d: %s (%v)",
			healthyPreviewResult.status, healthyPreviewResult.body, err,
		)
	}

	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if err := storetest.FailCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("fail durable command evidence: %v", err)
	}
	ordinary := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "must roll back", "target_group_key": "crew",
			"until_cleared": true, "command_id": "reject-degraded-stage-message",
		},
	)
	if ordinary.status != http.StatusInternalServerError {
		t.Fatalf("ordinary mutation during failure = %d: %s", ordinary.status, ordinary.body)
	}
	if page := readDisplayHTML(t, displayClient, server.address); strings.Contains(page, "must roll back") {
		t.Fatalf("failed ordinary mutation reached Display: %s", page)
	}
	newSignIn := requestJSON(
		t.Context(), authenticatedClient(t), server.address, "/auth/sign-in",
		map[string]string{
			"name": "Ada Admin", "password": "correct horse battery staple",
		},
	)
	if newSignIn.status != http.StatusInternalServerError {
		t.Fatalf(
			"new sign-in before degraded Emergency detection = %d: %s",
			newSignIn.status, newSignIn.body,
		)
	}

	rejectedDurableConfirmation := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts",
		map[string]any{
			"target":              map[string]any{"type": "Event"},
			"text":                "Evacuate through the north exit",
			"preview_fingerprint": healthyPreview.ConfirmationFingerprint,
			"confirmed":           true,
			"confirmation_method": "Keyboard",
			"command_id":          "reject-durable-confirmation-after-failure",
		},
	)
	if rejectedDurableConfirmation.status != http.StatusConflict {
		t.Fatalf(
			"durable confirmation after storage failure = %d: %s",
			rejectedDurableConfirmation.status, rejectedDurableConfirmation.body,
		)
	}
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("rejected durable confirmation reached Display: %+v", snapshot)
	}

	emergencyPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/emergency-alerts/preview",
		map[string]any{
			"target": map[string]any{"type": "Event"},
			"text":   "Evacuate through the north exit",
		},
	)
	var preview struct {
		ConfirmationFingerprint string `json:"confirmation_fingerprint"`
		Nondurable              bool   `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(emergencyPreview.body), &preview); err != nil ||
		emergencyPreview.status != http.StatusOK || preview.ConfirmationFingerprint == "" ||
		!preview.Nondurable {
		t.Fatalf(
			"preview degraded Emergency Alert = %d: %s (%v)",
			emergencyPreview.status, emergencyPreview.body, err,
		)
	}
	confirmation := get(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation?target_type=Event&text="+
			url.QueryEscape("Evacuate through the north exit"),
	)
	confirmationBody, readErr := io.ReadAll(confirmation.Body)
	closeErr := confirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		confirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(confirmationBody, []byte("Severely degraded")) ||
		!bytes.Contains(confirmationBody, []byte("nondurable")) {
		t.Fatalf(
			"degraded Emergency confirmation before activation = %d: %s (%v)",
			confirmation.StatusCode, confirmationBody, err,
		)
	}
	activation := frontendNamedValues(
		string(confirmationBody),
		"target_type",
		"target_id",
		"target_key",
		"text",
		"preview_fingerprint",
		"command_id",
		"build_version",
	)
	activation.Set("confirmation_method", "Keyboard")
	activated := postFrontendForm(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation", activation,
	)
	var emergency struct {
		ID         int  `json:"id"`
		Revision   int  `json:"revision"`
		Nondurable bool `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(activated.body), &emergency); err != nil ||
		activated.status != http.StatusOK || activated.header.Get("Location") != "" ||
		emergency.ID <= 0 ||
		emergency.Revision != 1 || !emergency.Nondurable {
		t.Fatalf(
			"activate degraded Emergency Alert = %d: %s (%v)",
			activated.status, activated.body, err,
		)
	}
	replayed := postFrontendForm(
		t, administrator, server.address,
		"/crew/events/1/emergency-alerts/confirmation", activation,
	)
	if replayed.status != http.StatusOK || replayed.body != activated.body {
		t.Fatalf(
			"retry degraded Emergency Alert = %d: %s, want %d: %s",
			replayed.status, replayed.body, activated.status, activated.body,
		)
	}

	applied := readDisplaySnapshot(t, displayClient, server.address)
	if applied.EmergencyAlert.ID != strconv.Itoa(emergency.ID) ||
		!strings.Contains(readDisplayHTML(t, displayClient, server.address), "Evacuate through the north exit") {
		t.Fatalf("degraded Emergency Alert Display Snapshot = %+v", applied)
	}

	clearConfirmation := get(
		t, administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear-confirmation", emergency.ID),
	)
	clearConfirmationBody, readErr := io.ReadAll(clearConfirmation.Body)
	closeErr = clearConfirmation.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil ||
		clearConfirmation.StatusCode != http.StatusOK ||
		!bytes.Contains(clearConfirmationBody, []byte("Severely degraded")) ||
		!bytes.Contains(clearConfirmationBody, []byte("nondurable")) {
		t.Fatalf(
			"degraded Emergency clear confirmation = %d: %s (%v)",
			clearConfirmation.StatusCode, clearConfirmationBody, err,
		)
	}
	clearForm := frontendNamedValues(
		string(clearConfirmationBody),
		"expected_revision",
		"command_id",
		"build_version",
	)
	clearForm.Set("confirmation_method", "Keyboard")
	cleared := postFrontendForm(
		t, administrator, server.address,
		fmt.Sprintf("/crew/events/1/overrides/%d/clear-confirmation", emergency.ID),
		clearForm,
	)
	var clearedEmergency struct {
		Revision   int  `json:"revision"`
		Nondurable bool `json:"nondurable"`
	}
	if err := json.Unmarshal([]byte(cleared.body), &clearedEmergency); err != nil ||
		cleared.status != http.StatusOK || cleared.header.Get("Location") != "" ||
		clearedEmergency.Revision != 2 ||
		!clearedEmergency.Nondurable {
		t.Fatalf(
			"clear degraded Emergency Alert = %d: %s (%v)",
			cleared.status, cleared.body, err,
		)
	}
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("cleared degraded Emergency Alert remained in snapshot: %+v", snapshot)
	}

	readiness := requestProbe(t.Context(), server.address, "/readyz", 5*time.Second)
	assertProbeResult(t, "/readyz", readiness, http.StatusServiceUnavailable, "not ready\n")
	if err := storetest.AllowCommandEvidence(t.Context(), databasePath); err != nil {
		t.Fatalf("restore durable command evidence: %v", err)
	}
	beforeRecovery := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "wait for evidence recovery", "target_group_key": "crew",
			"until_cleared": true, "command_id": "wait-for-emergency-recovery",
		},
	)
	if beforeRecovery.status != http.StatusInternalServerError {
		t.Fatalf(
			"ordinary mutation before evidence recovery = %d: %s",
			beforeRecovery.status, beforeRecovery.body,
		)
	}
	assertProbe(t, server.address, "/readyz", "ready\n")
	afterRecovery := requestJSON(
		t.Context(), administrator, server.address,
		"/crew/events/1/stage-messages",
		map[string]any{
			"text": "recovered", "target_group_key": "crew",
			"until_cleared": true, "command_id": "resume-after-emergency-recovery",
		},
	)
	if afterRecovery.status != http.StatusOK {
		t.Fatalf(
			"ordinary mutation after evidence recovery = %d: %s",
			afterRecovery.status, afterRecovery.body,
		)
	}
	assertRecoveredEvidence := func(label string) {
		entries, _ := readAuditHistory(t, administrator, server.address)
		remaining := map[string]int{
			"ActivateEmergencyAlert/Rejected/override_revision_conflict": 1,
			"ActivateEmergencyAlert/Succeeded/":                          1,
			"ClearDisplayOverride/Succeeded/":                            1,
		}
		for _, entry := range entries {
			key := entry.Action + "/" + entry.Outcome + "/" + entry.Reason
			if _, expected := remaining[key]; expected {
				remaining[key]--
			}
		}
		for evidence, count := range remaining {
			if count != 0 {
				t.Errorf("%s Emergency evidence %q remaining = %d", label, evidence, count)
			}
		}
	}
	assertRecoveredEvidence("recovered")
	assertProbe(t, server.address, "/readyz", "ready\n")
	assertRecoveredEvidence("repeated recovery")

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamers(t, bin, dataDir)
	if snapshot := readDisplaySnapshot(t, displayClient, server.address); snapshot.EmergencyAlert.ID != "" {
		t.Fatalf("cleared Emergency Alert returned after restart: %+v", snapshot)
	}
	server.stop(t)
}

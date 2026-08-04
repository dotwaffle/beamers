package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func provisionOperator(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
) *http.Client {
	t.Helper()
	return provisionOperatorWithLanes(t, administrator, server, []int{1})
}

func openAndCloseProgramStream(
	t *testing.T,
	client *http.Client,
	address string,
	sessionID int64,
) {
	t.Helper()
	streamContext, cancelStream := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(
		streamContext,
		http.MethodGet,
		fmt.Sprintf(
			"http://%s/crew/program/%d/events?event_id=1",
			address,
			sessionID,
		),
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Program control stream request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open Program control stream: %v", err)
	}
	cancelStream()
	if err = response.Body.Close(); err != nil {
		t.Fatalf("close Program control stream: %v", err)
	}
}

func provisionObserver(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
) *http.Client {
	t.Helper()
	const password = "observer correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Oli Observer", "password": password,
			"command_id": "create-account-oli",
		},
		http.StatusCreated, "{\"id\":3,\"name\":\"Oli Observer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{
			"account_id": 3, "role": "Observer", "command_id": "grant-oli-observer",
		},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":3,\"role\":\"Observer\"}\n",
	)
	observer := authenticatedClient(t)
	assertJSONRequest(
		t, observer, server.address, "/auth/sign-in",
		map[string]string{"name": "Oli Observer", "password": password},
		http.StatusNoContent, "",
	)
	return observer
}

func provisionOperatorWithLanes(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
	laneIDs []int,
) *http.Client {
	t.Helper()
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Opal Operator", "password": "operator correct horse battery staple",
			"command_id": "create-account-opal",
		},
		http.StatusCreated, "{\"id\":2,\"name\":\"Opal Operator\",\"administrator\":false}\n",
	)
	grant := map[string]any{
		"account_id": 2, "role": "Operator", "command_id": "grant-opal-operator",
	}
	wantGrant := "{\"event_id\":1,\"account_id\":2,\"role\":\"Operator\"}\n"
	if len(laneIDs) > 0 {
		grant["lane_ids"] = laneIDs
		wantGrant = "{\"event_id\":1,\"account_id\":2,\"role\":\"Operator\",\"lane_ids\":[1]}\n"
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		grant, http.StatusCreated, wantGrant,
	)
	operator := authenticatedClient(t)
	assertJSONRequest(
		t, operator, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Opal Operator", "password": "operator correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	return operator
}

func TestAdministratorSelectsExistingAccountForEventGrant(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/admin/accounts", http.StatusOK,
		"[{\"id\":1,\"name\":\"Ada Admin\",\"administrator\":true},{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}]\n",
	)
	server.stop(t)
}

func TestAdministratorInspectsAuditHistory(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "audited account correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Avery Audited", "password": password,
			"command_id": "create-account-for-audit",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Avery Audited\",\"administrator\":false}\n",
	)
	response := get(t, administrator, server.address, "/admin/audit")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Audit history: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Audit history status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	var entries []acceptanceAuditEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode Audit history: %v", err)
	}
	if len(entries) != 1 || entries[0].ActorAccountID != 1 || entries[0].ActorName != "Ada Admin" ||
		entries[0].ServerTime.IsZero() || entries[0].Action != "CreateAccount" ||
		entries[0].TargetType != "Account" || entries[0].TargetID != "2" ||
		entries[0].Outcome != "Succeeded" || entries[0].Reason != "" || entries[0].Note != "" {
		t.Errorf("Audit history = %+v", entries)
	}
	if strings.Contains(string(body), password) {
		t.Error("Audit history contains an Account password")
	}
	server.stop(t)
}

func TestAdministratorDisablesAccountImmediately(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "retired account correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Riley Retired", "password": password,
			"command_id": "create-account-to-disable",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Riley Retired\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 2, "role": "Producer", "command_id": "grant-retiring-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":2,\"role\":\"Producer\"}\n",
	)
	retired := authenticatedClient(t)
	assertJSONRequest(
		t, retired, server.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusNoContent, "",
	)
	assertJSONRequest(
		t, retired, server.address, "/admin/accounts",
		map[string]string{
			"name": "Forbidden Account", "password": "forbidden correct horse battery staple",
			"command_id": "retired-actor-rejected-command",
		},
		http.StatusForbidden, "Administrator authority required\n",
	)
	disable := map[string]string{
		"command_id": "disable-riley", "reason": "crew_departed",
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		disable, http.StatusNoContent, "",
	)
	assertSessionRejected(t, retired, server.address)
	assertJSONRequest(
		t, authenticatedClient(t), server.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusUnauthorized, "authentication failed\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/admin/accounts", http.StatusOK,
		"[{\"id\":1,\"name\":\"Ada Admin\",\"administrator\":true}]\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		disable, http.StatusNoContent, "",
	)

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	entries, body := readAuditHistory(t, administrator, restarted.address)
	var disableCount int
	var retainedActor bool
	for _, entry := range entries {
		if entry.Action == "DisableAccount" && entry.TargetType == "Account" && entry.TargetID == "2" {
			disableCount++
			if entry.Outcome != "Succeeded" || entry.Reason != disable["reason"] {
				t.Errorf("Disable Account Audit Entry = %+v", entry)
			}
		}
		if entry.ActorAccountID == 2 && entry.ActorName == "Riley Retired" &&
			entry.Action == "CreateAccount" && entry.Outcome == "Rejected" {
			retainedActor = true
		}
	}
	if disableCount != 1 {
		t.Errorf("Disable Account Audit Entry count = %d, want 1", disableCount)
	}
	if !retainedActor {
		t.Error("Audit history lost the disabled actor identity")
	}
	if strings.Contains(string(body), password) || strings.Contains(string(body), "forbidden correct horse battery staple") {
		t.Error("Audit history contains a password")
	}
	assertJSONRequest(
		t, authenticatedClient(t), restarted.address, "/auth/sign-in",
		map[string]string{"name": "Riley Retired", "password": password},
		http.StatusUnauthorized, "authentication failed\n",
	)
	restarted.stop(t)
}

func TestAuditSeparatesDomainRejectionsFromTransportFailures(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	const password = "audit boundary correct horse battery staple"
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Taylor Transport", "password": password,
			"command_id": "create-audit-boundary-account",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Taylor Transport\",\"administrator\":false}\n",
	)
	malformed, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost,
		"http://"+server.address+"/admin/accounts/2/disable",
		strings.NewReader("{"),
	)
	if err != nil {
		t.Fatalf("create malformed Disable Account request: %v", err)
	}
	malformed.Header.Set("Content-Type", "application/json")
	malformedResponse, err := administrator.Do(malformed)
	if err != nil {
		t.Fatalf("send malformed Disable Account request: %v", err)
	}
	if closeErr := malformedResponse.Body.Close(); closeErr != nil {
		t.Errorf("close malformed Disable Account response: %v", closeErr)
	}
	if malformedResponse.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed Disable Account status = %d, want %d", malformedResponse.StatusCode, http.StatusBadRequest)
	}
	assertJSONRequest(
		t, authenticatedClient(t), server.address, "/admin/accounts/2/disable",
		map[string]string{"command_id": "unauthenticated-disable", "reason": "access_revoked"},
		http.StatusUnauthorized, "authentication required\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		map[string]string{"reason": "access_revoked"},
		http.StatusBadRequest, "invalid request\n",
	)
	entries, _ := readAuditHistory(t, administrator, server.address)
	if len(entries) != 1 {
		t.Fatalf("Audit Entry count after transport failures = %d, want 1", len(entries))
	}

	const rejectedSecret = "Bearer should-not-enter-audit"
	invalid := map[string]string{"command_id": "validation-blocked-disable", "reason": rejectedSecret}
	for range 2 {
		assertJSONRequest(
			t, administrator, server.address, "/admin/accounts/2/disable",
			invalid, http.StatusUnprocessableEntity, "valid command_id and reason are required\n",
		)
	}
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/2/disable",
		map[string]string{
			"command_id": "validation-blocked-disable", "reason": "access_revoked",
		},
		http.StatusConflict, "command_id was already used with a different payload\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts/1/disable",
		map[string]string{"command_id": "disable-last-administrator", "reason": "access_revoked"},
		http.StatusConflict, "last Administrator cannot be disabled\n",
	)
	transportActor := authenticatedClient(t)
	assertJSONRequest(
		t, transportActor, server.address, "/auth/sign-in",
		map[string]string{"name": "Taylor Transport", "password": password},
		http.StatusNoContent, "",
	)
	assertGETResponse(
		t, transportActor, server.address, "/admin/audit",
		http.StatusForbidden, "Administrator authority required\n",
	)
	assertJSONRequest(
		t, transportActor, server.address, "/admin/accounts/1/disable",
		map[string]string{"command_id": "unauthorized-disable", "reason": "access_revoked"},
		http.StatusForbidden, "Administrator authority required\n",
	)
	entries, auditBody := readAuditHistory(t, administrator, server.address)
	wantReasons := map[string]int{
		"disable_reason_required": 1,
		"command_id_conflict":     1,
		"last_administrator":      1,
		"administrator_required":  1,
	}
	for _, entry := range entries {
		if entry.Outcome == "Rejected" {
			wantReasons[entry.Reason]--
		}
	}
	for reason, remaining := range wantReasons {
		if remaining != 0 {
			t.Errorf("Rejected Audit Entries with reason %q remaining = %d", reason, remaining)
		}
	}
	if len(entries) != 5 {
		t.Errorf("Audit Entry count = %d, want 5", len(entries))
	}
	if strings.Contains(string(auditBody), rejectedSecret) {
		t.Error("Audit history contains rejected secret-like reason text")
	}
	server.stop(t)
}

type acceptanceAuditEntry struct {
	ActorAccountID int       `json:"actor_account_id"`
	ActorName      string    `json:"actor_name"`
	ServerTime     time.Time `json:"server_time"`
	Action         string    `json:"action"`
	TargetType     string    `json:"target_type"`
	TargetID       string    `json:"target_id"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason"`
	Note           string    `json:"note"`
}

func readAuditHistory(
	t *testing.T,
	client *http.Client,
	address string,
) ([]acceptanceAuditEntry, []byte) {
	t.Helper()
	response := get(t, client, address, "/admin/audit")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Audit history: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Audit history status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	var entries []acceptanceAuditEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode Audit history: %v", err)
	}
	return entries, body
}

func TestRejectedEventGrantRetryReturnsOriginalOutcome(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	grant := map[string]any{
		"account_id": 2, "role": "Producer", "command_id": "grant-missing-event",
	}
	for range 2 {
		assertJSONRequest(
			t, administrator, server.address, "/admin/events/99/grants",
			grant, http.StatusNotFound, "Event not found\n",
		)
	}
	server.stop(t)
}

func TestProducerGrantControlsEventCrewRead(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/accounts",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
			"command_id": "create-account-pat",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Pat Producer\",\"administrator\":false}\n",
	)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 2, "role": "Producer", "command_id": "grant-pat-producer"},
		http.StatusCreated,
		"{\"event_id\":1,\"account_id\":2,\"role\":\"Producer\"}\n",
	)

	producer := authenticatedClient(t)
	assertJSONRequest(
		t, producer, server.address, "/auth/sign-in",
		map[string]string{
			"name": "Pat Producer", "password": "producer correct horse battery staple",
		},
		http.StatusNoContent, "",
	)
	assertGETResponse(
		t, producer, server.address, "/crew/events/1", http.StatusOK,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertGETResponse(
		t, administrator, server.address, "/crew/events/1",
		http.StatusForbidden, "Event access denied\n",
	)
	server.stop(t)
}

func TestAdministratorAuthorityDoesNotPermitEventCrewMutation(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	assertJSONRequest(
		t, administrator, server.address, "/admin/events",
		validEventInput(), http.StatusCreated,
		"{\"id\":1,\"name\":\"Revision 2026\",\"planned_start_date\":\"2026-08-21\",\"planned_end_date\":\"2026-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"de-DE\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	changed := validEventInput()
	changed["name"] = "Changed without an Event Grant"
	changed["command_id"] = "update-event-without-grant"
	assertJSONMethodRequest(
		t, http.MethodPut, administrator, server.address, "/crew/events/1",
		changed, http.StatusForbidden, "Event access denied\n",
	)
	server.stop(t)
}

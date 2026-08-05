package programcontrol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestProgramChannelCommandsAreDisplayGroupScoped covers the D3 resolution: a
// Program Channel command is judged by the Display Group keys of the Displays
// currently consuming the channel, not by Event-wide Operator authority.
//
// An Operator whose Display Group grant does not cover those keys is refused
// with the same durable code the Event-wide guard produced, the refusal leaves
// a Command Receipt that answers its retry and a Rejected Audit Entry, and an
// Operator whose grant does cover them is admitted where the old rule already
// admitted every Operator.
func TestProgramChannelCommandsAreDisplayGroupScoped(t *testing.T) {
	storage, administrator, eventID := openProgramAuthorizationTest(t)
	sessionID, locationID := publishProgramCompetition(t, storage, administrator, eventID)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 12, 30, 0, 0, time.UTC)
	}
	publications, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	service, err := programcontrol.New(storage, publications, now, nil, nil, nil)
	if err != nil {
		t.Fatalf("create Program control: %v", err)
	}

	// An Operator of this Event whose grant names the Display Group the
	// channel's consuming Display will carry.
	operator := administrator
	operator.Administrator = false
	operator.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	operator.EventScopes = map[int]viewer.EventScope{
		eventID: {DisplayGroupKeys: map[string]struct{}{"main": {}}},
	}

	claim := func(actor auth.Account, commandID string) error {
		_, err := service.Control(t.Context(), actor, programcontrol.ControlInput{
			EventID: eventID, SessionID: sessionID,
			Action: programcontrol.ControlClaim, CommandID: commandID,
		})
		return err
	}

	// Before any keyed Display consumes the channel, its scope resolves to no
	// Display Group keys, which only a Producer passes.
	if err := claim(operator, "claim-unrouted-channel"); !errors.Is(
		err, programcontrol.ErrOperatorRequired,
	) {
		t.Fatalf("claim of unrouted channel = %v, want %v", err, programcontrol.ErrOperatorRequired)
	}

	assignProgramDisplay(t, storage, administrator, displays.AssignInput{
		EventID: eventID, LocationID: locationID,
		ViewKey:          displayviews.CompetitionOutput,
		DisplayGroupKeys: []string{"main"},
		CommandID:        "assign-program-display",
	})

	// The same Operator is admitted once their grant covers every Display
	// Group the channel feeds.
	if err := claim(operator, "claim-covered-channel"); err != nil {
		t.Fatalf("claim by covering Operator = %v, want admitted", err)
	}

	// An Operator whose grant names a different Display Group is refused, the
	// refusal answers its own retry, and it leaves exactly one Rejected Audit
	// Entry carrying the durable code the Event-wide guard produced.
	outside := operator
	outside.EventScopes = map[int]viewer.EventScope{
		eventID: {DisplayGroupKeys: map[string]struct{}{"other": {}}},
	}
	for range 2 {
		if err := claim(outside, "claim-outside-scope"); !errors.Is(
			err, programcontrol.ErrOperatorRequired,
		) {
			t.Fatalf("claim outside scope = %v, want %v", err, programcontrol.ErrOperatorRequired)
		}
	}
	// Two refusals were committed in this test — the unrouted claim and the
	// out-of-scope claim — and the retried refusal added no third entry
	// because its retry was answered from its Command Receipt.
	entries := rejectedProgramAudits(t, storage, administrator, "ChangeProgramControlClaim")
	if len(entries) != 2 {
		t.Fatalf(
			"Rejected Audit Entries for refused claims = %d, want one per refused "+
				"command with the retry answered from its Command Receipt",
			len(entries),
		)
	}
	for _, entry := range entries {
		if entry.Reason != "program_operator_required" {
			t.Errorf(
				"Rejected Audit Entry reason = %q, want the durable code the "+
					"Event-wide guard produced",
				entry.Reason,
			)
		}
	}
}

// rejectedProgramAudits returns the Rejected Audit Entries recorded for one
// command action.
func rejectedProgramAudits(
	t *testing.T,
	storage *store.SQLite,
	reader auth.Account,
	action string,
) []store.AuditEntry {
	t.Helper()

	entries, err := storage.ListAuditEntries(reader.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Audit Entries: %v", err)
	}
	rejected := make([]store.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == action && entry.Outcome == "Rejected" {
			rejected = append(rejected, entry)
		}
	}
	return rejected
}

// assignProgramDisplay enrolls one Display and routes it to the given Location
// and View so the Program Channel there gains a consuming Display.
func assignProgramDisplay(
	t *testing.T,
	storage *store.SQLite,
	administrator auth.Account,
	input displays.AssignInput,
) {
	t.Helper()

	service, err := displays.New(storage, displays.Config{
		Now: time.Now, Random: bytes.NewReader(make([]byte, 128)),
		EnrollmentTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}
	enrollment, err := service.EnrollmentForBrowser(t.Context(), "", "")
	if err != nil {
		t.Fatalf("create Display Enrollment: %v", err)
	}
	display, err := service.ClaimEnrollment(
		t.Context(), administrator, displays.ClaimInput{
			Code: enrollment.Code, Name: "Program", CommandID: "claim-program-display",
		},
	)
	if err != nil {
		t.Fatalf("claim Display Enrollment: %v", err)
	}
	input.DisplayID = display.ID
	if _, err = service.Assign(t.Context(), administrator, input); err != nil {
		t.Fatalf("assign Display: %v", err)
	}
}

func openProgramAuthorizationTest(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	hostContext := systemactor.NewContext(t.Context(), systemactor.HostMaintenance)
	if err := store.Initialize(hostContext, dataDir); err != nil {
		t.Fatalf("initialize Program storage: %v", err)
	}
	storage, err := store.Open(hostContext, dataDir)
	if err != nil {
		t.Fatalf("open Program storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close Program storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(hostContext, bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue Program bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(hostContext, store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator",
		NormalizedName: "administrator", PasswordHash: "password-hash",
		SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Program Administrator: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Program Event", PlannedStartDate: "2026-08-21",
		PlannedEndDate: "2026-08-21", Timezone: "UTC", EventLocale: "en-US",
		EntryDefaultDisposition: "Included", CommandID: "create-program-event",
	})
	if err != nil {
		t.Fatalf("create Program Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), producer, event.ID, producer.ID, "Producer", "grant-program-producer",
	); err != nil {
		t.Fatalf("grant Program Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, producer, event.ID
}

// publishProgramCompetition publishes one Competition Session on one Lane at
// one Location and returns the Session and Location identifiers.
func publishProgramCompetition(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) (sessionID, locationID int) {
	t.Helper()
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-program-draft",
		Locations: []rundown.LocationDraftInput{{Ref: "hall", Name: "Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "lane", Name: "Lane", Location: rundown.TargetRef{Ref: "hall"},
		}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "competition", Title: "Competition", Type: rundown.SessionCompetition,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       start, PlannedEnd: start.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundaryHard,
			SubmissionDeadline: start.Add(-time.Hour),
			Lanes:              []rundown.TargetRef{{Ref: "lane"}},
			Locations:          []rundown.TargetRef{{Ref: "hall"}},
		}},
	})
	if err != nil {
		t.Fatalf("create Program Draft: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), producer, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("preview Program Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-program",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Program: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), producer, eventID)
	if err != nil || len(crew.Sessions) != 1 || len(crew.Locations) != 1 {
		t.Fatalf("load Program Session = %+v, %v", crew, err)
	}
	return crew.Sessions[0].ID, crew.Locations[0].ID
}

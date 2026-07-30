package competition

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/overrides"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
	"github.com/dotwaffle/beamers/internal/voting"
)

func TestDisplayAssignmentSelectsDisplayNotifications(t *testing.T) {
	storage, administrator, eventID := openNotificationTest(t)
	publishNotificationCompetition(t, storage, administrator, eventID)
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), administrator, eventID)
	if err != nil || len(crew.Locations) != 1 {
		t.Fatalf("load Display Assignment Location = %+v, %v", crew, err)
	}
	notifications := 0
	service, err := displays.New(storage, displays.Config{
		Now: time.Now, Random: bytes.NewReader(make([]byte, 128)),
		EnrollmentTTL:  10 * time.Minute,
		NotifyDisplays: func() { notifications++ },
	})
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}
	enrollment, err := service.EnrollmentForBrowser(t.Context(), "", "")
	if err != nil {
		t.Fatalf("create Display Enrollment: %v", err)
	}
	display, err := service.ClaimEnrollment(
		t.Context(),
		administrator,
		displays.ClaimInput{
			Code: enrollment.Code, Name: "Main", CommandID: "claim-assigned-display",
		},
	)
	if err != nil {
		t.Fatalf("claim Display Enrollment: %v", err)
	}
	notifications = 0
	input := displays.AssignInput{
		DisplayID: display.ID, EventID: eventID, LocationID: crew.Locations[0].ID,
		ViewKey: displayviews.EventOverview, CommandID: "assign-notified-display",
	}
	if _, err = service.Assign(t.Context(), administrator, input); err != nil {
		t.Fatalf("assign Display: %v", err)
	}
	if _, err = service.Assign(
		t.Context(), auth.Account{ID: administrator.ID}, input,
	); err != nil {
		t.Fatalf("replay Display Assignment: %v", err)
	}
	rejected := input
	rejected.CommandID = "reject-display-assignment"
	if _, err = service.Assign(
		t.Context(), auth.Account{ID: administrator.ID}, rejected,
	); !errors.Is(err, displays.ErrAdministratorRequired) {
		t.Fatalf("rejected Display Assignment error = %v", err)
	}
	if notifications != 2 {
		t.Fatalf("Display Assignment notifications = %d, want 2", notifications)
	}
}

func TestEntryCommandsSelectCombinedAndScheduleOnlyNotifications(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	sessionID := publishNotificationCompetition(t, storage, producer, eventID)
	var displayNotifications, scheduleNotifications int
	service, err := New(
		storage,
		func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) },
		func() { displayNotifications++ },
		func() { scheduleNotifications++ },
	)
	if err != nil {
		t.Fatalf("create Competition service: %v", err)
	}
	input := CreateEntryInput{
		EventID: eventID, SessionID: sessionID, CommandID: "create-notified-entry",
		Name: "Aurora", PublicDetails: "Public entry",
	}
	created, err := service.CreateEntry(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("create Competition Entry: %v", err)
	}
	if displayNotifications != 1 || scheduleNotifications != 1 {
		t.Fatalf(
			"successful Entry notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	if _, err = service.CreateEntry(t.Context(), producer, input); err != nil {
		t.Fatalf("replay Competition Entry creation: %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 2 {
		t.Fatalf(
			"replayed Entry notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	input.Name = "Conflict"
	if _, err = service.CreateEntry(
		t.Context(),
		producer,
		input,
	); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("conflicting Entry creation error = %v", err)
	}
	if _, err = service.CreateEntry(
		t.Context(),
		auth.Account{ID: producer.ID},
		CreateEntryInput{
			EventID: eventID, SessionID: sessionID, CommandID: "reject-entry",
			Name: "Rejected",
		},
	); !errors.Is(err, ErrProducerRequired) {
		t.Fatalf("unauthorized Entry creation error = %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 2 {
		t.Fatalf(
			"rejected Entry notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	hold := SetEntryReleaseHoldInput{
		EventID: eventID, SessionID: sessionID, EntryID: created.ID,
		ExpectedRevision: created.Revision, Hold: true,
		CrewReason: "Awaiting corrected file", CommandID: "hold-entry-release",
	}
	if _, err = service.SetEntryReleaseHold(t.Context(), producer, hold); err != nil {
		t.Fatalf("set Entry release hold: %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 3 {
		t.Fatalf(
			"release-hold notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
	if _, err = service.SetEntryReleaseHold(t.Context(), producer, hold); err != nil {
		t.Fatalf("replay Entry release hold: %v", err)
	}
	if displayNotifications != 2 || scheduleNotifications != 4 {
		t.Fatalf(
			"replayed release-hold notifications = display %d, schedule %d",
			displayNotifications,
			scheduleNotifications,
		)
	}
}

func TestVotingCommandsSelectVotingNotifications(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	sessionID := publishNotificationCompetition(t, storage, producer, eventID)
	notifications := 0
	service, err := voting.New(
		storage,
		nil,
		time.Now,
		func() { notifications++ },
	)
	if err != nil {
		t.Fatalf("create Voting service: %v", err)
	}
	input := voting.ConfigureInput{
		EventID: eventID, SessionID: sessionID,
		CommandID: "configure-notified-voting",
	}
	configured, err := service.Configure(t.Context(), producer, input)
	if err != nil {
		t.Fatalf("configure Voting: %v", err)
	}
	if notifications != 1 {
		t.Fatalf("Voting notifications = %d, want 1", notifications)
	}
	if _, err = service.Configure(t.Context(), producer, input); err != nil {
		t.Fatalf("replay Voting configuration: %v", err)
	}
	if notifications != 2 {
		t.Fatalf("replayed Voting notifications = %d, want 2", notifications)
	}
	conflicting := input
	conflicting.MethodOverride = "Range1To5"
	if _, err = service.Configure(
		t.Context(), producer, conflicting,
	); !errors.Is(err, voting.ErrVotingRevision) {
		t.Fatalf("conflicting Voting configuration error = %v", err)
	}
	rejected := input
	rejected.CommandID = "reject-stale-voting"
	rejected.ExpectedRevision = configured.Revision + 1
	if _, err = service.Configure(
		t.Context(), producer, rejected,
	); !errors.Is(err, voting.ErrVotingRevision) {
		t.Fatalf("rejected Voting configuration error = %v", err)
	}
	if notifications != 2 {
		t.Fatalf("failed Voting notifications = %d, want 2", notifications)
	}
}

func TestOverrideCommandsSelectDisplayNotifications(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	notifications := 0
	service, err := overrides.New(
		t.Context(),
		storage,
		time.Now,
		func() { notifications++ },
	)
	if err != nil {
		t.Fatalf("create Override service: %v", err)
	}
	input := overrides.ConfigureInput{
		EventID: eventID, DefaultDurationSeconds: 10,
		CommandID: "configure-notified-overrides",
	}
	if _, err = service.ConfigureStageMessages(t.Context(), producer, input); err != nil {
		t.Fatalf("configure Stage Messages: %v", err)
	}
	if notifications != 1 {
		t.Fatalf("Override notifications = %d, want 1", notifications)
	}
	if _, err = service.ConfigureStageMessages(t.Context(), producer, input); err != nil {
		t.Fatalf("replay Stage Message configuration: %v", err)
	}
	if notifications != 2 {
		t.Fatalf("replayed Override notifications = %d, want 2", notifications)
	}
	conflicting := input
	conflicting.DefaultDurationSeconds = 20
	if _, err = service.ConfigureStageMessages(
		t.Context(), producer, conflicting,
	); !errors.Is(err, overrides.ErrCommandConflict) {
		t.Fatalf("conflicting Stage Message configuration error = %v", err)
	}
	rejected := input
	rejected.CommandID = "reject-stage-message-configuration"
	if _, err = service.ConfigureStageMessages(
		t.Context(), auth.Account{ID: producer.ID}, rejected,
	); !errors.Is(err, overrides.ErrProducerRequired) {
		t.Fatalf("rejected Stage Message configuration error = %v", err)
	}
	if notifications != 2 {
		t.Fatalf("failed Override notifications = %d, want 2", notifications)
	}
}

func TestProgramEntryTakeSelectsDisplayProgramAndVotingNotifications(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	sessionID := publishNotificationCompetition(t, storage, producer, eventID)
	entries, err := New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Competition service: %v", err)
	}
	if _, err = entries.ConfigureReadiness(
		t.Context(),
		producer,
		ConfigureReadinessInput{
			EventID: eventID, SessionID: sessionID,
			CommandID: "disable-program-file-preflight",
		},
	); err != nil {
		t.Fatalf("configure Program Competition readiness: %v", err)
	}
	if _, err = entries.CreateEntry(t.Context(), producer, CreateEntryInput{
		EventID: eventID, SessionID: sessionID,
		Name: "Aurora", CommandID: "create-program-entry",
	}); err != nil {
		t.Fatalf("create Program Entry: %v", err)
	}
	activationService, err := activation.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := activationService.Preflight(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("preflight Program Event: %v", err)
	}
	if _, err = activationService.Activate(
		t.Context(),
		producer,
		activation.ActivateInput{
			EventID: eventID, CommandID: "activate-program-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate Program Event: %v", err)
	}
	publications, err := results.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	sessions, err := sessioncontrol.New(storage, publications, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Session service: %v", err)
	}
	if _, err = sessions.Start(t.Context(), producer, sessioncontrol.StartInput{
		EventID: eventID, SessionID: sessionID, CommandID: "start-program-session",
	}); err != nil {
		t.Fatalf("start Program Session: %v", err)
	}
	var displayNotifications, programNotifications, votingNotifications int
	program, err := programcontrol.New(
		storage,
		publications,
		time.Now,
		func() { displayNotifications++ },
		func() { programNotifications++ },
		func() { votingNotifications++ },
	)
	if err != nil {
		t.Fatalf("create Program service: %v", err)
	}
	claimed, err := program.Control(t.Context(), producer, programcontrol.ControlInput{
		EventID: eventID, SessionID: sessionID,
		Action: programcontrol.ControlClaim, CommandID: "claim-entry-program",
	})
	if err != nil {
		t.Fatalf("claim Entry Program: %v", err)
	}
	disconnected, err := program.Control(
		t.Context(),
		producer,
		programcontrol.ControlInput{
			EventID: eventID, SessionID: sessionID,
			Action:           programcontrol.ControlDisconnect,
			CommandID:        "disconnect-entry-program",
			ExpectedRevision: claimed.ControlRevision,
		},
	)
	if err != nil {
		t.Fatalf("disconnect Entry Program: %v", err)
	}
	_, release, err := program.OpenConnection(
		t.Context(), producer, eventID, sessionID,
	)
	if err != nil {
		t.Fatalf("open Entry Program connection: %v", err)
	}
	if programNotifications != 3 {
		t.Fatalf("connected Program notifications = %d, want 3", programNotifications)
	}
	release()
	release()
	if programNotifications != 4 {
		t.Fatalf("disconnected Program notifications = %d, want 4", programNotifications)
	}
	claimed, err = program.Current(t.Context(), producer, eventID, sessionID)
	if err != nil || claimed.ControlRevision != disconnected.ControlRevision+2 {
		t.Fatalf("Program state after connection release = %+v, %v", claimed, err)
	}
	var entryItem store.ProgramItem
	for _, item := range claimed.Channel.Items {
		if item.Kind == store.ProgramItemEntry {
			entryItem = item
			break
		}
	}
	selected, err := program.SelectPreview(
		t.Context(),
		producer,
		programcontrol.SelectPreviewInput{
			EventID: eventID, SessionID: sessionID, Item: entryItem,
			ExpectedRevision: claimed.ControlRevision,
			CommandID:        "select-program-entry",
		},
	)
	if err != nil {
		t.Fatalf("select Program Entry: %v", err)
	}
	order, err := entries.PreviewEntryOrder(t.Context(), producer, eventID, sessionID)
	if err != nil {
		t.Fatalf("preview Program Entry order: %v", err)
	}
	input := programcontrol.TakeInput{
		EventID: eventID, SessionID: sessionID, CommandID: "take-program-entry",
		ExpectedRevision:           selected.Channel.Revision,
		ExpectedControlRevision:    selected.ControlRevision,
		Item:                       selected.Preview,
		ExpectedEntryOrderRevision: order.Revision,
		EntryOrderFingerprint:      order.Fingerprint,
	}
	if _, err = program.Take(t.Context(), producer, input); err != nil {
		t.Fatalf("take Program Entry: %v", err)
	}
	if displayNotifications != 1 || programNotifications != 6 || votingNotifications != 1 {
		t.Fatalf(
			"Program Entry notifications = display %d, program %d, voting %d",
			displayNotifications, programNotifications, votingNotifications,
		)
	}
	if _, err = program.Take(t.Context(), producer, input); err != nil {
		t.Fatalf("replay Program Entry Take: %v", err)
	}
	if displayNotifications != 2 || programNotifications != 7 || votingNotifications != 2 {
		t.Fatalf(
			"replayed Program Entry notifications = display %d, program %d, voting %d",
			displayNotifications, programNotifications, votingNotifications,
		)
	}
	conflicting := input
	conflicting.ExpectedRevision++
	if _, err = program.Take(
		t.Context(), producer, conflicting,
	); !errors.Is(err, programcontrol.ErrCommandConflict) {
		t.Fatalf("conflicting Program Entry Take error = %v", err)
	}
	if displayNotifications != 2 || programNotifications != 7 || votingNotifications != 2 {
		t.Fatalf(
			"conflicting Program Entry notifications = display %d, program %d, voting %d",
			displayNotifications, programNotifications, votingNotifications,
		)
	}
}

func openNotificationTest(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize Competition storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open Competition storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close Competition storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue Competition bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Administrator",
			NormalizedName: "administrator", PasswordHash: "password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Competition Administrator: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Competition Event", PlannedStartDate: "2026-08-21",
		PlannedEndDate: "2026-08-21", Timezone: "UTC", EventLocale: "en-US",
		EntryDefaultDisposition: "Included", CommandID: "create-competition-event",
	})
	if err != nil {
		t.Fatalf("create Competition Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), producer, event.ID, producer.ID, "Producer", "grant-competition-producer",
	); err != nil {
		t.Fatalf("grant Competition Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, producer, event.ID
}

func publishNotificationCompetition(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) int {
	t.Helper()
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-competition-draft",
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
		t.Fatalf("create Competition Draft: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), producer, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("preview Competition Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-competition",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Competition: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), producer, eventID)
	if err != nil || len(crew.Sessions) != 1 {
		t.Fatalf("load Competition Session = %+v, %v", crew, err)
	}
	return crew.Sessions[0].ID
}

package results_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPrizegivingPublicCommandsPreflightAndPreview(t *testing.T) {
	storage, actor, eventID := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	}
	ceremonyID, competitionID := publishPrizegivingSessions(
		t,
		storage,
		actor,
		eventID,
		now,
	)
	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	if _, err = service.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-prizegiving",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	draft, err := service.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-results", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save Results: %v", err)
	}
	draft, err = service.MarkReady(t.Context(), actor, results.MarkReadyInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "mark-results-ready", ExpectedRevision: draft.Revision,
	})
	if err != nil {
		t.Fatalf("mark Results Ready: %v", err)
	}
	item := results.ResultItem{
		Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
		DisplayOrder: 1, RevealMethod: "UnknownMethod",
	}
	invalidInput := results.SavePrizegivingPlanInput{
		EventID: eventID, CeremonySessionID: ceremonyID,
		CommandID: "save-invalid-plan", CompetitionSessionIDs: []int{competitionID},
		Sequence: []results.ResultItem{item},
		PublicationOrder: []results.ResultItemRef{
			item.Ref(1),
		},
		Template: results.TextTemplate{
			Revision: 1, Source: "{{call .Command}}",
		},
	}
	invalidPlan, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		invalidInput,
	)
	if err != nil {
		t.Fatalf("save editable invalid plan: %v", err)
	}
	revoked := actor
	revoked.EventRoles = nil
	replayed, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		invalidInput,
	)
	if err != nil || replayed.Revision != invalidPlan.Revision {
		t.Fatalf("replay Prizegiving plan = %+v, %v", replayed, err)
	}
	conflict := invalidInput
	conflict.Template.Source = "{{.EventTitle}}\n"
	if _, err = service.SavePrizegivingPlan(
		t.Context(),
		actor,
		conflict,
	); !errors.Is(err, results.ErrCommandConflict) {
		t.Fatalf("conflicting Prizegiving command error = %v", err)
	}
	unauthorized := invalidInput
	unauthorized.CommandID = "unauthorized-plan"
	unauthorized.ExpectedRevision = invalidPlan.Revision
	if _, err = service.SavePrizegivingPlan(
		t.Context(),
		revoked,
		unauthorized,
	); !errors.Is(err, results.ErrProducerRequired) {
		t.Fatalf("unauthorized Prizegiving command error = %v", err)
	}
	blocked, err := service.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "blocked-preflight", ExpectedRevision: invalidPlan.Revision,
		},
	)
	if !errors.Is(err, results.ErrPrizegivingPreflightBlocked) {
		t.Fatalf("blocked Preflight error = %v", err)
	}
	codes := make(map[string]bool, len(blocked.Findings))
	for _, finding := range blocked.Findings {
		codes[finding.Code] = true
	}
	if !codes["invalid_reveal_method"] || !codes["unsafe_results_template"] {
		t.Fatalf("blocked Preflight findings = %+v", blocked.Findings)
	}

	validPlan, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "save-valid-plan", ExpectedRevision: invalidPlan.Revision,
			CompetitionSessionIDs: []int{competitionID},
			Template: results.TextTemplate{
				Revision: 2, Source: "{{.EventTitle}}\n",
			},
		},
	)
	if err != nil {
		t.Fatalf("save valid Prizegiving plan: %v", err)
	}
	locked, err := service.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "lock-preflight", ExpectedRevision: validPlan.Revision,
		},
	)
	if err != nil || !locked.Plan.Locked {
		t.Fatalf("lock Prizegiving = %+v, %v", locked, err)
	}
	if _, err = service.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-later-results", ExpectedRevision: draft.Revision,
		Disposition: results.NoPublicResults, NoPublicReason: "withheld",
		Score: results.ScorePolicy{Type: results.None},
	}); err != nil {
		t.Fatalf("save later Results: %v", err)
	}
	preview, err := service.PreviewPrizegiving(
		t.Context(),
		actor,
		eventID,
		ceremonyID,
		results.PrizegivingPreviewModePreview,
	)
	if err != nil {
		t.Fatalf("Preview Prizegiving: %v", err)
	}
	if preview.Watermark == "" ||
		len(preview.CompetitionResults) != 1 ||
		preview.CompetitionResults[0].ID != draft.ID ||
		preview.CompetitionResults[0].Disposition != results.Publish {
		t.Fatalf("Prizegiving Preview = %+v", preview)
	}
}

func TestPrizegivingPublicProgramControlRevealsLockedResult(t *testing.T) {
	storage, actor, eventID := openPrizegivingApplicationTest(t)
	nowValue := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	now := func() time.Time { return nowValue }
	ceremonyID, competitionID := publishPrizegivingSessions(
		t,
		storage,
		actor,
		eventID,
		now,
	)
	resultsService, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	if _, err = resultsService.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-public-program-prizegiving",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	draft, err := resultsService.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-public-program-results", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save Results: %v", err)
	}
	if _, err = resultsService.MarkReady(
		t.Context(),
		actor,
		results.MarkReadyInput{
			EventID: eventID, SessionID: competitionID,
			CommandID:        "ready-public-program-results",
			ExpectedRevision: draft.Revision,
		},
	); err != nil {
		t.Fatalf("mark Results Ready: %v", err)
	}
	item := results.ResultItem{
		Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
		DisplayOrder: 1, RevealMethod: results.RevealSequentialPodium,
	}
	plan, err := resultsService.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:             "save-public-program-plan",
			CompetitionSessionIDs: []int{competitionID},
			Sequence:              []results.ResultItem{item},
			PublicationOrder:      []results.ResultItemRef{item.Ref(1)},
			Template: results.TextTemplate{
				Revision: 1, Source: "{{.EventTitle}}\n",
			},
		},
	)
	if err != nil {
		t.Fatalf("save Prizegiving plan: %v", err)
	}
	if _, err = resultsService.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:        "lock-public-program-plan",
			ExpectedRevision: plan.Revision,
		},
	); err != nil {
		t.Fatalf("lock Prizegiving plan: %v", err)
	}
	activationService, err := activation.New(storage, now)
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := activationService.Preflight(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("preflight Event activation: %v", err)
	}
	if _, err = activationService.Activate(
		t.Context(),
		actor,
		activation.ActivateInput{
			EventID: eventID, CommandID: "activate-public-program-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate Event: %v", err)
	}
	sessionService, err := sessioncontrol.New(storage, now)
	if err != nil {
		t.Fatalf("create Session control: %v", err)
	}
	if _, err = sessionService.Start(
		t.Context(),
		actor,
		sessioncontrol.StartInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID: "start-public-program-prizegiving",
		},
	); err != nil {
		t.Fatalf("start Prizegiving: %v", err)
	}
	programService, err := programcontrol.New(storage, now)
	if err != nil {
		t.Fatalf("create Program control: %v", err)
	}
	claimed, err := programService.Control(
		t.Context(),
		actor,
		programcontrol.ControlInput{
			EventID: eventID, SessionID: ceremonyID,
			Action: programcontrol.ControlClaim, CommandID: "claim-public-program",
		},
	)
	if err != nil {
		t.Fatalf("claim Program Channel: %v", err)
	}
	taken, err := programService.Take(t.Context(), actor, programcontrol.TakeInput{
		EventID: eventID, SessionID: ceremonyID,
		CommandID:               "take-public-program-result",
		ExpectedRevision:        claimed.Channel.Revision,
		ExpectedControlRevision: claimed.ControlRevision,
		Item:                    claimed.Preview,
	})
	if err != nil {
		t.Fatalf("Take Result: %v", err)
	}
	if !taken.Committed ||
		taken.State.Channel.Output.Result == nil ||
		taken.State.Channel.Output.Result.Status != "Taken" ||
		taken.State.Channel.Output.Result.Release != "Held" {
		t.Fatalf("Taken Program Result = %+v", taken)
	}
	revealing, err := programService.ActOnResult(
		t.Context(),
		actor,
		programcontrol.ResultActionInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "reveal-public-program-result",
			Action:                  programcontrol.ResultReveal,
			Item:                    taken.State.Channel.Output,
			ExpectedProgramRevision: taken.State.Channel.Revision,
			ExpectedControlRevision: taken.State.ControlRevision,
		},
	)
	if err != nil {
		t.Fatalf("Reveal Result: %v", err)
	}
	if !revealing.Committed ||
		revealing.State.Channel.Output.Result.Status != "Revealing" ||
		revealing.State.Channel.Output.Result.Release != "Held" ||
		revealing.State.Channel.Output.Result.RevealDuration != 3*time.Second {
		t.Fatalf("Revealing Program Result = %+v", revealing)
	}
	nowValue = nowValue.Add(3 * time.Second)
	revealed, err := programService.Current(
		t.Context(),
		actor,
		eventID,
		ceremonyID,
	)
	if err != nil {
		t.Fatalf("restore elapsed Result Reveal: %v", err)
	}
	if revealed.Channel.Output.Result.Status != "Revealed" ||
		revealed.Channel.Output.Result.Release != "Ready" ||
		revealed.Channel.Output.Result.CompetitionResults.ID != draft.ID {
		t.Fatalf("restored Program Result = %+v", revealed)
	}
	replayed, err := programService.ActOnResult(
		t.Context(),
		actor,
		programcontrol.ResultActionInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "replay-public-program-result",
			Action:                  programcontrol.ResultReplayReveal,
			Item:                    revealed.Channel.Output,
			ExpectedProgramRevision: revealed.Channel.Revision,
			ExpectedControlRevision: revealed.ControlRevision,
		},
	)
	if err != nil {
		t.Fatalf("Replay Result: %v", err)
	}
	if !replayed.Committed ||
		!replayed.State.Channel.Output.Result.Replay ||
		replayed.State.Channel.Output.Result.RevealSeed !=
			revealed.Channel.Output.Result.RevealSeed {
		t.Fatalf("Replayed Program Result = %+v", replayed)
	}
}

func openPrizegivingApplicationTest(
	t *testing.T,
) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(
		t.Context(),
		bootstrapHash,
		now,
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash,
			Name:          "Producer", NormalizedName: "producer",
			PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
			Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{
		ID: created.ID, Name: created.Name, Administrator: true,
	}
	eventService, err := events.New(storage, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(
		t.Context(),
		administrator,
		events.CreateInput{
			Name: "Revision 2026", PlannedStartDate: "2026-08-21",
			PlannedEndDate: "2026-08-23", Timezone: "Europe/Berlin",
			EventLocale: "de-DE", EventDayBoundary: "06:00",
			CommandID: "create-event-for-prizegiving",
		},
	)
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(),
		administrator,
		event.ID,
		administrator.ID,
		"Producer",
		"grant-prizegiving-producer",
	); err != nil {
		t.Fatalf("grant Producer: %v", err)
	}
	administrator.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, administrator, event.ID
}

func publishPrizegivingSessions(
	t *testing.T,
	storage *store.SQLite,
	actor auth.Account,
	eventID int,
	now func() time.Time,
) (int, int) {
	t.Helper()
	commands, err := rundown.NewCommands(storage, now)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-prizegiving-sessions",
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane",
			Location: rundown.TargetRef{Ref: "main"},
		}},
		Sessions: []rundown.SessionDraftInput{
			{
				Ref: "competition", Title: "Final",
				Type:               rundown.SessionCompetition,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start, PlannedEnd: start.Add(time.Hour),
				TimingPolicy:    rundown.TimingFixedEnd,
				MinimumDuration: 30 * time.Minute,
				StartBoundary:   rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				SubmissionDeadline: start.Add(-time.Hour),
				Lanes:              []rundown.TargetRef{{Ref: "main-lane"}},
			},
			{
				Ref: "ceremony", Title: "Prizegiving",
				Type:               rundown.SessionCeremony,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start.Add(2 * time.Hour),
				PlannedEnd:         start.Add(3 * time.Hour),
				TimingPolicy:       rundown.TimingFixedEnd,
				MinimumDuration:    30 * time.Minute,
				StartBoundary:      rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				Lanes: []rundown.TargetRef{{Ref: "main-lane"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create Prizegiving sessions: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(
		t.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: eventID, ChangeIDs: changeIDs},
	)
	if err != nil {
		t.Fatalf("preview Prizegiving sessions: %v", err)
	}
	if _, err = commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-prizegiving-sessions",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision:     preview.DraftRevision,
			PublishedRevision: preview.PublishedRevision,
			ChangeIDs:         preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Prizegiving sessions: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("load Prizegiving sessions: %v", err)
	}
	var ceremonyID, competitionID int
	for _, session := range crew.Sessions {
		switch session.Title {
		case "Prizegiving":
			ceremonyID = session.ID
		case "Final":
			competitionID = session.ID
		}
	}
	if ceremonyID == 0 || competitionID == 0 {
		t.Fatalf("published Prizegiving sessions = %+v", crew.Sessions)
	}
	return ceremonyID, competitionID
}

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPrizegivingPlanAssignmentIsUniqueAndLockIsImmutable(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	firstCeremony := createPublishedResultsSession(
		t, client, event.ID, sessionpublishedversion.TypeCeremony, "Awards",
	)
	secondCeremony := createPublishedResultsSession(
		t, client, event.ID, sessionpublishedversion.TypeCeremony, "Closing",
	)
	competition := createPublishedResultsSession(
		t, client, event.ID, sessionpublishedversion.TypeCompetition, "Final",
	)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  7,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	for _, ceremony := range []int{firstCeremony.ID, secondCeremony.ID} {
		transaction, err := installation.BeginCommand(ctx)
		if err != nil {
			t.Fatalf("begin Prizegiving designation: %v", err)
		}
		if _, err = transaction.DesignatePrizegiving(ctx, DesignatePrizegivingParams{
			EventID: event.ID, CeremonySessionID: ceremony,
			CreatedByAccountID: 7, Now: now,
		}); err != nil {
			t.Fatalf("designate Prizegiving: %v", err)
		}
		if err = transaction.Commit(); err != nil {
			t.Fatalf("commit Prizegiving designation: %v", err)
		}
	}

	firstTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving plan: %v", err)
	}
	first, err := firstTransaction.SavePrizegivingPlan(ctx, SavePrizegivingPlanParams{
		EventID: event.ID, CeremonySessionID: firstCeremony.ID,
		ExpectedRevision: 0, CompetitionSessionIDs: []int{competition.ID},
		Sequence: []PrizegivingResultItem{{
			Kind: "CompetitionResults", CompetitionSessionID: competition.ID,
			DisplayOrder: 1, RevealMethod: "StaticResult",
		}},
		PublicationOrder: []PrizegivingResultItemRef{{
			Kind: "CompetitionResults", CompetitionSessionID: competition.ID,
			DisplayOrder: 1,
		}},
		Template: PrizegivingResultsTextTemplate{
			Revision: 1, Source: "{{.EventTitle}}\n",
		},
	})
	if err != nil {
		t.Fatalf("save Prizegiving plan: %v", err)
	}
	if err = firstTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving plan: %v", err)
	}
	if first.Revision != 1 ||
		len(first.CompetitionSessionIDs) != 1 ||
		first.CompetitionSessionIDs[0] != competition.ID {
		t.Fatalf("saved Prizegiving plan = %+v", first)
	}
	preflightTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving Preflight state load: %v", err)
	}
	preflightState, err := preflightTransaction.LoadPrizegivingPreflightState(
		ctx,
		event.ID,
		firstCeremony.ID,
	)
	if err != nil {
		t.Fatalf("load Prizegiving Preflight state: %v", err)
	}
	if len(preflightState.Competitions) != 1 ||
		preflightState.Competitions[0].Draft.Disposition != "Pending" {
		t.Fatalf("Prizegiving Preflight state = %+v", preflightState)
	}
	if err = preflightTransaction.Rollback(); err != nil {
		t.Fatalf("roll back Prizegiving Preflight state load: %v", err)
	}
	viewContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  8,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Observer},
		EventScopes: map[int]viewer.EventScope{
			event.ID: {
				Capabilities: map[viewer.Capability]struct{}{viewer.ViewResults: {}},
			},
		},
	})
	if visible, viewErr := installation.LoadPrizegivingPlan(
		viewContext,
		event.ID,
		firstCeremony.ID,
	); viewErr != nil || visible.Revision != 1 {
		t.Fatalf("View Results Prizegiving plan = %+v, %v", visible, viewErr)
	}
	if visible, viewErr := client.PrizegivingCompetition.Query().
		Exist(viewContext); viewErr != nil || !visible {
		t.Fatalf("View Results Prizegiving assignment = %v, %v", visible, viewErr)
	}
	if _, viewErr := installation.LoadPrizegivingPlan(
		t.Context(),
		event.ID,
		firstCeremony.ID,
	); viewErr == nil {
		t.Fatal("missing viewer read Prizegiving plan")
	}
	manageContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID:  9,
		EventRoles: map[int]viewer.Role{event.ID: viewer.Operator},
		EventScopes: map[int]viewer.EventScope{
			event.ID: {
				Capabilities: map[viewer.Capability]struct{}{viewer.ManageResults: {}},
			},
		},
	})
	deniedTransaction, err := installation.BeginCommand(manageContext)
	if err != nil {
		t.Fatalf("begin unauthorized Prizegiving plan: %v", err)
	}
	_, err = deniedTransaction.SavePrizegivingPlan(
		manageContext,
		SavePrizegivingPlanParams{
			EventID: event.ID, CeremonySessionID: firstCeremony.ID,
			ExpectedRevision: 1, CompetitionSessionIDs: []int{competition.ID},
			Sequence: first.Sequence, PublicationOrder: first.PublicationOrder,
			Template: first.Template,
		},
	)
	if err == nil {
		t.Fatal("Manage Results without Producer authority changed Prizegiving plan")
	}
	if err = deniedTransaction.Rollback(); err != nil {
		t.Fatalf("roll back unauthorized Prizegiving plan: %v", err)
	}

	secondTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin conflicting Prizegiving plan: %v", err)
	}
	_, err = secondTransaction.SavePrizegivingPlan(ctx, SavePrizegivingPlanParams{
		EventID: event.ID, CeremonySessionID: secondCeremony.ID,
		ExpectedRevision: 0, CompetitionSessionIDs: []int{competition.ID},
		Sequence: []PrizegivingResultItem{{
			Kind: "CompetitionResults", CompetitionSessionID: competition.ID,
			DisplayOrder: 1, RevealMethod: "StaticResult",
		}},
		PublicationOrder: []PrizegivingResultItemRef{{
			Kind: "CompetitionResults", CompetitionSessionID: competition.ID,
			DisplayOrder: 1,
		}},
		Template: PrizegivingResultsTextTemplate{
			Revision: 1, Source: "{{.EventTitle}}\n",
		},
	})
	if !errors.Is(err, ErrCompetitionPrizegivingAssignment) {
		t.Fatalf("duplicate Competition assignment error = %v", err)
	}
	if err = secondTransaction.Rollback(); err != nil {
		t.Fatalf("roll back conflicting Prizegiving plan: %v", err)
	}

	lockTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving lock: %v", err)
	}
	locked, err := lockTransaction.LockPrizegivingPlan(
		ctx,
		event.ID,
		firstCeremony.ID,
		1,
		PrizegivingPreflightLock{
			PlanRevision: 1,
			CompetitionSources: []PrizegivingCompetitionLock{{
				SessionID: competition.ID, DraftRevision: 4,
			}},
			Sequence: []PrizegivingLockedResultItem{{
				PrizegivingResultItem: first.Sequence[0], RevealSeed: 42,
			}},
			PublicationOrder: first.PublicationOrder,
			Template:         first.Template,
		},
		7,
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("lock Prizegiving plan: %v", err)
	}
	if err = lockTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving lock: %v", err)
	}
	if !locked.Locked || locked.Lock.Sequence[0].RevealSeed != 42 {
		t.Fatalf("locked Prizegiving plan = %+v", locked)
	}

	editTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin locked Prizegiving edit: %v", err)
	}
	_, err = editTransaction.SavePrizegivingPlan(ctx, SavePrizegivingPlanParams{
		EventID: event.ID, CeremonySessionID: firstCeremony.ID,
		ExpectedRevision: 1, Template: first.Template,
	})
	if !errors.Is(err, ErrPrizegivingLocked) {
		t.Fatalf("locked Prizegiving edit error = %v", err)
	}
	if err = editTransaction.Rollback(); err != nil {
		t.Fatalf("roll back locked Prizegiving edit: %v", err)
	}
}

func TestPrizegivingDefaultsOrderAndPreviewUsesLockedSources(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	ceremony := createPublishedResultsSession(
		t, client, event.ID, sessionpublishedversion.TypeCeremony, "Awards",
	)
	late := createPublishedResultsSessionAt(
		t, client, event.ID, sessionpublishedversion.TypeCompetition, "Late",
		time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
	)
	early := createPublishedResultsSessionAt(
		t, client, event.ID, sessionpublishedversion.TypeCompetition, "Early",
		time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
	)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: 7, EventRoles: map[int]viewer.Role{event.ID: viewer.Producer},
	})
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)

	designation, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving designation: %v", err)
	}
	if _, err = designation.DesignatePrizegiving(ctx, DesignatePrizegivingParams{
		EventID: event.ID, CeremonySessionID: ceremony.ID,
		CreatedByAccountID: 7, Now: now,
	}); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	if err = designation.Commit(); err != nil {
		t.Fatalf("commit Prizegiving designation: %v", err)
	}

	drafts := make(map[int]CompetitionResultsDraft)
	for index, sessionID := range []int{late.ID, early.ID} {
		transaction, beginErr := installation.BeginCommand(ctx)
		if beginErr != nil {
			t.Fatalf("begin Results Draft: %v", beginErr)
		}
		awards := []CompetitionAwardInput(nil)
		if sessionID == early.ID {
			awards = []CompetitionAwardInput{{
				Key: "judges-choice", Name: "Judges' Choice",
				Recipients: []AwardRecipientInput{{DisplayName: "Ari"}},
				Promoted:   true, DisplayOrder: 1,
			}}
		}
		found, saveErr := transaction.SaveCompetitionResultsDraft(
			ctx,
			SaveCompetitionResultsDraftParams{
				EventID: event.ID, SessionID: sessionID, ExpectedRevision: 0,
				Disposition: "Pending", ScoreType: "None",
				ScoreVisibility: "Public", ScoreRequirement: "Optional",
				ScoreInterpretation: "Informational",
				CreatedByAccountID:  7, Now: now.Add(time.Duration(index) * time.Minute),
				Awards: awards,
			},
		)
		if saveErr != nil {
			t.Fatalf("save Results Draft: %v", saveErr)
		}
		if commitErr := transaction.Commit(); commitErr != nil {
			t.Fatalf("commit Results Draft: %v", commitErr)
		}
		drafts[sessionID] = found
	}
	awardsTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Event Awards: %v", err)
	}
	eventAwards, err := awardsTransaction.SaveEventAwardsDraft(
		ctx,
		SaveEventAwardsDraftParams{
			EventID: event.ID, ExpectedRevision: 0,
			CreatedByAccountID: 7, Now: now,
			Awards: []EventAwardInput{{
				Key: "community", Name: "Community",
				Recipients:   []AwardRecipientInput{{DisplayName: "Volunteers"}},
				DisplayOrder: 1,
				ReleasePath: AwardReleasePath{
					Kind: "Prizegiving", PrizegivingSessionID: ceremony.ID,
				},
			}},
		},
	)
	if err != nil {
		t.Fatalf("save Event Awards: %v", err)
	}
	if err = awardsTransaction.Commit(); err != nil {
		t.Fatalf("commit Event Awards: %v", err)
	}

	planTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving plan: %v", err)
	}
	plan, err := planTransaction.SavePrizegivingPlan(ctx, SavePrizegivingPlanParams{
		EventID: event.ID, CeremonySessionID: ceremony.ID,
		CompetitionSessionIDs: []int{late.ID, early.ID},
		Sequence: []PrizegivingResultItem{
			{
				Kind: "CompetitionResults", CompetitionSessionID: early.ID,
				DisplayOrder: 1, RevealMethod: "StaticResult",
			},
			{
				Kind: "CompetitionAward", CompetitionSessionID: early.ID,
				AwardKey: "judges-choice", DisplayOrder: 2,
				RevealMethod: "StaticResult",
			},
			{
				Kind: "CompetitionResults", CompetitionSessionID: late.ID,
				DisplayOrder: 3, RevealMethod: "StaticResult",
			},
			{
				Kind: "EventAward", AwardKey: "community",
				DisplayOrder: 4, RevealMethod: "StaticResult",
			},
		},
		PublicationOrder: []PrizegivingResultItemRef{
			{
				Kind: "CompetitionResults", CompetitionSessionID: early.ID,
				DisplayOrder: 1,
			},
			{
				Kind: "CompetitionAward", CompetitionSessionID: early.ID,
				AwardKey: "judges-choice", DisplayOrder: 2,
			},
			{
				Kind: "CompetitionResults", CompetitionSessionID: late.ID,
				DisplayOrder: 3,
			},
			{Kind: "EventAward", AwardKey: "community", DisplayOrder: 4},
		},
		Template: PrizegivingResultsTextTemplate{
			Revision: 1, Source: "{{.EventTitle}}\n",
		},
	})
	if err != nil {
		t.Fatalf("save default Prizegiving plan: %v", err)
	}
	if err = planTransaction.Commit(); err != nil {
		t.Fatalf("commit default Prizegiving plan: %v", err)
	}
	if len(plan.Sequence) != 4 ||
		plan.Sequence[0].CompetitionSessionID != early.ID ||
		plan.Sequence[1].Kind != "CompetitionAward" ||
		plan.Sequence[2].CompetitionSessionID != late.ID ||
		plan.Sequence[3].Kind != "EventAward" {
		t.Fatalf("default Prizegiving order = %+v", plan.Sequence)
	}

	lockTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Prizegiving lock: %v", err)
	}
	locked, err := lockTransaction.LockPrizegivingPlan(
		ctx,
		event.ID,
		ceremony.ID,
		plan.Revision,
		PrizegivingPreflightLock{
			PlanRevision: plan.Revision,
			CompetitionSources: []PrizegivingCompetitionLock{
				{
					SessionID: early.ID, DraftID: drafts[early.ID].ID,
					DraftRevision: drafts[early.ID].Revision,
				},
				{
					SessionID: late.ID, DraftID: drafts[late.ID].ID,
					DraftRevision: drafts[late.ID].Revision,
				},
			},
			EventAwardsDraftRevision: eventAwards.Revision,
			Sequence: []PrizegivingLockedResultItem{{
				PrizegivingResultItem: plan.Sequence[0], RevealSeed: 42,
			}},
			PublicationOrder: plan.PublicationOrder,
			Template:         plan.Template,
		},
		7,
		now,
	)
	if err != nil {
		t.Fatalf("lock Prizegiving plan: %v", err)
	}
	if err = lockTransaction.Commit(); err != nil {
		t.Fatalf("commit Prizegiving lock: %v", err)
	}
	if !locked.Locked {
		t.Fatal("Prizegiving was not locked")
	}

	revisionTransaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin later Results Draft: %v", err)
	}
	if _, err = revisionTransaction.SaveCompetitionResultsDraft(
		ctx,
		SaveCompetitionResultsDraftParams{
			EventID: event.ID, SessionID: early.ID, ExpectedRevision: 1,
			Disposition: "NoPublicResults", NoPublicCrewReason: "withheld",
			ScoreType: "None", ScoreVisibility: "Public",
			ScoreRequirement: "Optional", ScoreInterpretation: "Informational",
			CreatedByAccountID: 7, Now: now.Add(time.Hour),
			Awards: []CompetitionAwardInput{{
				Key: "judges-choice", Name: "Changed",
				Recipients: []AwardRecipientInput{{DisplayName: "Changed"}},
				Promoted:   true, DisplayOrder: 1,
			}},
		},
	); err != nil {
		t.Fatalf("save later Results Draft: %v", err)
	}
	if err = revisionTransaction.Commit(); err != nil {
		t.Fatalf("commit later Results Draft: %v", err)
	}

	preview, err := installation.LoadPrizegivingPreview(
		ctx,
		event.ID,
		ceremony.ID,
	)
	if err != nil {
		t.Fatalf("load Prizegiving Preview: %v", err)
	}
	if len(preview.CompetitionResults) != 2 ||
		preview.CompetitionResults[0].Revision != 1 ||
		preview.CompetitionResults[0].Awards[0].Name != "Judges' Choice" ||
		len(preview.EventAwards) != 1 ||
		preview.EventAwards[0].Name != "Community" {
		t.Fatalf("locked Prizegiving Preview = %+v", preview)
	}
}

func createPublishedResultsSessionAt(
	t *testing.T,
	client *ent.Client,
	eventID int,
	sessionType sessionpublishedversion.Type,
	title string,
	plannedStart time.Time,
) *ent.Session {
	t.Helper()
	ctx := systemContext(t.Context())
	found := client.Session.Create().
		SetEventID(eventID).
		SetLifecycle(session.LifecycleEnded).
		SaveX(ctx)
	client.SessionPublishedVersion.Create().
		SetSessionID(found.ID).
		SetPublishedRevision(1).
		SetTitle(title).
		SetType(sessionType).
		SetAudienceVisibility(sessionpublishedversion.AudienceVisibilityPublic).
		SetPlannedStart(plannedStart).
		SetPlannedEnd(plannedStart.Add(time.Hour)).
		SetTimingPolicy(sessionpublishedversion.TimingPolicyFixedEnd).
		SetMinimumDurationSeconds(1800).
		SetStartBoundary(sessionpublishedversion.StartBoundaryHard).
		SetEndBoundary(sessionpublishedversion.EndBoundaryHard).
		SaveX(ctx)
	return found
}

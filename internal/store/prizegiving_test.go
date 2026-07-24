package store

import (
	"errors"
	"testing"
	"time"

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

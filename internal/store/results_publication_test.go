package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

func TestResultsPublicationAppendIsImmutableAndRevisionChecked(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	ceremony := createPublishedResultsSession(
		t,
		client,
		event.ID,
		"Ceremony",
		"Prizegiving",
	)
	ctx := systemContext(t.Context())
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	ref := PrizegivingResultItemRef{
		Kind: "CompetitionResults", CompetitionSessionID: 17, DisplayOrder: 1,
	}
	lock := prizegivingvalue.Lock{
		PlanRevision: 3,
		PublicationOrder: []prizegivingvalue.ItemRef{{
			Kind: "CompetitionResults", CompetitionSessionID: 17, DisplayOrder: 1,
		}},
		CompetitionSources: []prizegivingvalue.CompetitionLock{{
			SessionID: 17, DraftID: 23, DraftRevision: 2, Disposition: "Publish",
		}},
	}
	transaction := beginCommand(t, installation, ctx)
	first, err := transaction.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: 0,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationPartial,
			Items:  []PrizegivingResultItemRef{ref},
			Lock:   lock, CreatedByAccountID: 7, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("append Results Publication: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Results Publication: %v", err)
	}
	if first.Revision != 1 ||
		first.Status != ResultsPublicationPartial ||
		len(first.Items) != 1 ||
		first.Lock.CompetitionSources[0].DraftID != 23 {
		t.Fatalf("first Results Publication = %+v", first)
	}

	loaded, err := installation.LoadResultsPublication(
		ctx,
		event.ID,
		ResultsPublicationPrizegiving,
		ceremony.ID,
	)
	if err != nil || loaded.Revision != first.Revision {
		t.Fatalf("load current Results Publication = %+v, %v", loaded, err)
	}

	stale := beginCommand(t, installation, ctx)
	_, err = stale.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: 0,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationFinal,
			Items:  []PrizegivingResultItemRef{ref},
			Lock:   lock, CreatedByAccountID: 7, Now: now,
		},
	)
	if !errors.Is(err, ErrResultsPublicationRevision) {
		t.Fatalf("stale Results Publication error = %v", err)
	}
	if rollbackErr := stale.Rollback(); rollbackErr != nil {
		t.Fatalf("roll back stale Results Publication: %v", rollbackErr)
	}
}

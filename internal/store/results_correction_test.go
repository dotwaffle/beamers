package store

import (
	"errors"
	"testing"
	"time"
)

func TestResultsCorrectionLifecycleIsAppendOnlyAndReviewBound(t *testing.T) {
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
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	params := AppendResultsCorrectionParams{
		EventID: event.ID, Scope: ResultsPublicationPrizegiving,
		ScopeSessionID: ceremony.ID, ExpectedRevision: 0,
		BasePublicationRevision: 3, Status: ResultsCorrectionDraft,
		PublicationOrder: []PrizegivingResultItemRef{{
			Kind: "CompetitionResults", CompetitionSessionID: 9, DisplayOrder: 1,
		}},
		ItemsJSON: `[{"kind":"CompetitionResults"}]`,
		Template: PrizegivingResultsTextTemplate{
			Revision: 2, Source: "{{.EventTitle}}",
		},
		CrewReason: "Correct the published placement.",
		PublicNote: "Placement corrected.", CreatedByAccountID: 7, Now: now,
	}
	transaction := beginCommand(t, installation, ctx)
	draft, err := transaction.AppendResultsCorrection(ctx, params)
	if err != nil {
		t.Fatalf("append Results Correction Draft: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Results Correction Draft: %v", err)
	}
	if draft.Revision != 1 || draft.Status != ResultsCorrectionDraft {
		t.Fatalf("Results Correction Draft = %+v", draft)
	}
	params.ExpectedRevision = 1
	params.Status = ResultsCorrectionReady
	params.Now = now.Add(time.Minute)
	transaction = beginCommand(t, installation, ctx)
	ready, err := transaction.AppendResultsCorrection(ctx, params)
	if err != nil {
		t.Fatalf("append Ready Results Correction: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Ready Results Correction: %v", err)
	}
	if ready.Revision != 2 || ready.Status != ResultsCorrectionReady {
		t.Fatalf("Ready Results Correction = %+v", ready)
	}
	changed := params
	changed.ExpectedRevision = 2
	changed.Status = ResultsCorrectionPublished
	changed.PublicNote = "Changed after review."
	changed.PublishedResultsRevision = 4
	transaction = beginCommand(t, installation, ctx)
	if _, err = transaction.AppendResultsCorrection(
		ctx,
		changed,
	); !errors.Is(err, ErrResultsCorrectionTransition) {
		t.Fatalf("changed reviewed Results Correction error = %v", err)
	}
	if err = transaction.Rollback(); err != nil {
		t.Fatalf("roll back changed Results Correction: %v", err)
	}
	params.ExpectedRevision = 2
	params.Status = ResultsCorrectionPublished
	params.PublishedResultsRevision = 4
	params.Now = now.Add(2 * time.Minute)
	transaction = beginCommand(t, installation, ctx)
	published, err := transaction.AppendResultsCorrection(ctx, params)
	if err != nil {
		t.Fatalf("append Published Results Correction: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Published Results Correction: %v", err)
	}
	if published.Revision != 3 ||
		published.Status != ResultsCorrectionPublished ||
		published.PublishedResultsRevision != 4 {
		t.Fatalf("Published Results Correction = %+v", published)
	}
	loaded, err := installation.LoadResultsCorrection(
		ctx,
		event.ID,
		ResultsPublicationPrizegiving,
		ceremony.ID,
	)
	if err != nil || loaded.Revision != published.Revision {
		t.Fatalf("load Results Correction = %+v, %v", loaded, err)
	}
}

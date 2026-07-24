package store

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/competitionentry"
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
	lock := PrizegivingPreflightLock{
		PlanRevision: 3,
		PublicationOrder: []PrizegivingResultItemRef{{
			Kind: "CompetitionResults", CompetitionSessionID: 17, DisplayOrder: 1,
		}},
		CompetitionSources: []PrizegivingCompetitionLock{{
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

	enrichedLock := lock
	enrichedLock.RenderSource = []byte(`{"event_name":"frozen"}`)
	continuation := beginCommand(t, installation, ctx)
	second, err := continuation.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: 1,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationFinal,
			Items:  []PrizegivingResultItemRef{ref},
			Lock:   enrichedLock, CreatedByAccountID: 7, Now: now.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("continue pre-render Results Publication: %v", err)
	}
	if err = continuation.Commit(); err != nil {
		t.Fatalf("commit continued Results Publication: %v", err)
	}
	if second.Revision != 2 || len(second.Lock.RenderSource) == 0 {
		t.Fatalf("continued Results Publication = %+v", second)
	}

	correction := beginCommand(t, installation, ctx)
	corrected, err := correction.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: 2,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationFinal,
			Items:  []PrizegivingResultItemRef{ref}, Lock: enrichedLock,
			ResultsCorrectionRevision: 3,
			RenderedHTML:              "<p>corrected</p>", RenderedText: "corrected",
			RenderedJSON: `{"revision":3}`, CreatedByAccountID: 7,
			Now: now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("append corrected Results Publication: %v", err)
	}
	if err = correction.Commit(); err != nil {
		t.Fatalf("commit corrected Results Publication: %v", err)
	}
	if corrected.Revision != 3 || corrected.ResultsCorrectionRevision != 3 {
		t.Fatalf("corrected Results Publication = %+v", corrected)
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

func TestStandaloneResultsReleaseStateIncludesRequiredEntryResolution(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	competition := createPublishedResultsSession(
		t,
		client,
		event.ID,
		"Competition",
		"Final",
	)
	ctx := systemContext(t.Context())
	client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Unresolved").
		SetDisposition(competitionentry.DispositionIncluded).
		SetPresentationStatus(competitionentry.PresentationStatusNotPresented).
		SetResolutionRequired(true).
		SaveX(ctx)
	transaction := beginCommand(t, installation, ctx)
	state, err := transaction.LoadStandaloneResultsReleaseState(
		ctx,
		event.ID,
		competition.ID,
	)
	if err != nil {
		t.Fatalf("load standalone Results release state: %v", err)
	}
	if !state.ResolutionRequired {
		t.Fatalf("standalone Results state = %+v, want required resolution", state)
	}
	if err = transaction.Rollback(); err != nil {
		t.Fatalf("roll back standalone Results state query: %v", err)
	}
}

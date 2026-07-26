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

func TestEventResultsPublicationRejectsNonMonotonicAppend(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	ctx := systemContext(t.Context())
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	competition := PrizegivingResultItemRef{
		Kind: "CompetitionResults", CompetitionSessionID: 17, DisplayOrder: 1,
	}
	award := PrizegivingResultItemRef{
		Kind: "EventAward", AwardKey: "community", DisplayOrder: 2,
	}
	base := AppendResultsPublicationParams{
		EventID: event.ID, Scope: ResultsPublicationEvent,
		ScopeSessionID: event.ID, ExpectedRevision: 0,
		Policy: ResultsPublicationStandalonePolicy,
		Status: ResultsPublicationFinal,
		Items:  []PrizegivingResultItemRef{competition, award},
		Lock: PrizegivingPreflightLock{
			PublicationOrder: []PrizegivingResultItemRef{competition, award},
		},
		Now: now,
	}
	transaction := beginCommand(t, installation, ctx)
	if _, err := transaction.AppendResultsPublication(ctx, base); err != nil {
		t.Fatalf("append Event Results Publication: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit Event Results Publication: %v", err)
	}
	tests := []struct {
		name   string
		change func(*AppendResultsPublicationParams)
	}{
		{
			name: "retraction",
			change: func(params *AppendResultsPublicationParams) {
				params.Items = params.Items[:1]
			},
		},
		{
			name: "Final to Partial",
			change: func(params *AppendResultsPublicationParams) {
				params.Status = ResultsPublicationPartial
			},
		},
		{
			name: "reorder",
			change: func(params *AppendResultsPublicationParams) {
				params.Lock.PublicationOrder = []PrizegivingResultItemRef{
					award,
					competition,
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := base
			params.ExpectedRevision = 1
			params.Now = now.Add(time.Second)
			test.change(&params)
			rejected := beginCommand(t, installation, ctx)
			if _, err := rejected.AppendResultsPublication(
				ctx,
				params,
			); !errors.Is(err, ErrResultsPublicationTransition) {
				t.Fatalf("non-monotonic Event append error = %v", err)
			}
			if err := rejected.Rollback(); err != nil {
				t.Fatalf("roll back rejected Event append: %v", err)
			}
		})
	}
}

func TestResultsPublicationRenderSourceFreezesEventLocaleAndContentLanguage(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	event := createSchemaTestEvent(t, client)
	client.Event.UpdateOne(event).
		SetContentLanguage("fr").
		SaveX(systemContext(t.Context()))

	source, err := installation.LoadResultsPublicationRenderSource(
		t.Context(),
		event.ID,
		PrizegivingPreflightLock{},
	)
	if err != nil {
		t.Fatalf("load Results Publication render source: %v", err)
	}
	if source.EventName != event.Name ||
		source.EventLocale != "de-DE" ||
		source.ContentLanguage != "fr" {
		t.Fatalf("Results Publication Event metadata = %+v", source)
	}
}

func TestPartialResultsCorrectionPreservesLockedUnreleasedItems(t *testing.T) {
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
	released := PrizegivingResultItemRef{
		Kind: "CompetitionResults", CompetitionSessionID: 17, DisplayOrder: 1,
	}
	unreleased := PrizegivingResultItemRef{
		Kind: "EventAward", AwardKey: "community", DisplayOrder: 2,
	}
	lock := PrizegivingPreflightLock{
		PlanRevision: 3,
		PublicationOrder: []PrizegivingResultItemRef{
			released,
			unreleased,
		},
		RenderSource: []byte(`{"event_name":"frozen"}`),
	}
	firstTransaction := beginCommand(t, installation, ctx)
	first, err := firstTransaction.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: 0,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationPartial,
			Items:  []PrizegivingResultItemRef{released},
			Lock:   lock, CreatedByAccountID: 7, Now: now,
		},
	)
	if err != nil {
		t.Fatalf("append Partial Results Publication: %v", err)
	}
	if err = firstTransaction.Commit(); err != nil {
		t.Fatalf("commit Partial Results Publication: %v", err)
	}
	correctionTransaction := beginCommand(t, installation, ctx)
	corrected, err := correctionTransaction.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: first.Revision,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationPartial,
			Items:  []PrizegivingResultItemRef{released},
			Lock:   lock, ResultsCorrectionRevision: 1,
			RenderedHTML: "<p>corrected</p>", RenderedText: "corrected",
			RenderedJSON: `{"revision":2}`, CreatedByAccountID: 7,
			Now: now.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("append corrected Partial Results Publication: %v", err)
	}
	if err = correctionTransaction.Commit(); err != nil {
		t.Fatalf("commit corrected Partial Results Publication: %v", err)
	}
	if corrected.Revision != 2 ||
		len(corrected.Items) != 1 ||
		len(corrected.Lock.PublicationOrder) != 2 {
		t.Fatalf("corrected Partial Results Publication = %+v", corrected)
	}
	continuationTransaction := beginCommand(t, installation, ctx)
	continued, err := continuationTransaction.AppendResultsPublication(
		ctx,
		AppendResultsPublicationParams{
			EventID: event.ID, Scope: ResultsPublicationPrizegiving,
			ScopeSessionID: ceremony.ID, ExpectedRevision: corrected.Revision,
			Policy: ResultsPublicationProgressive,
			Status: ResultsPublicationFinal,
			Items:  []PrizegivingResultItemRef{released, unreleased},
			Lock:   lock, CreatedByAccountID: 7, Now: now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("continue corrected Partial Results Publication: %v", err)
	}
	if err = continuationTransaction.Commit(); err != nil {
		t.Fatalf("commit continued Results Publication: %v", err)
	}
	if continued.Status != ResultsPublicationFinal ||
		len(continued.Items) != 2 {
		t.Fatalf("continued Results Publication = %+v", continued)
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

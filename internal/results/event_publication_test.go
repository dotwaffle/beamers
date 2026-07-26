package results

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestEventPublicationCombinesScopesInFrozenPlannedOrder(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	competitions := []store.PrizegivingCompetitionOrderState{
		{
			SessionID: 20, PlannedStart: now.Add(time.Hour),
			Draft: store.CompetitionResultsDraft{
				SessionID: 20, Disposition: string(Publish),
			},
		},
		{
			SessionID: 10, PlannedStart: now,
			Draft: store.CompetitionResultsDraft{
				SessionID: 10, Disposition: string(Publish),
			},
		},
		{
			SessionID: 30, PlannedStart: now.Add(2 * time.Hour),
			Draft: store.CompetitionResultsDraft{
				SessionID: 30, Disposition: string(Publish),
			},
		},
	}
	later := eventPublicationTestScope(t, 1, 20, "Later")
	later.ScopeSessionID = 102
	firstRendered, firstParams, err := buildEventResultsPublication(
		store.EventResultsAggregateState{
			Competitions: competitions,
			Publications: []store.ResultsPublication{later},
		},
		store.ResultsPublication{},
		later,
		1,
		now,
	)
	if err != nil ||
		firstParams.Status != store.ResultsPublicationPartial ||
		len(firstParams.Items) != 1 ||
		firstParams.Items[0].CompetitionSessionID != 20 ||
		len(firstParams.Lock.PublicationOrder) != 3 ||
		firstParams.Lock.PublicationOrder[0].CompetitionSessionID != 10 {
		t.Fatalf("first Event Publication = %+v, %v", firstParams, err)
	}
	current := store.ResultsPublication{
		EventID: 1, Scope: store.ResultsPublicationEvent, ScopeSessionID: 1,
		Revision: 1, Policy: firstParams.Policy, Status: firstParams.Status,
		Items: firstParams.Items, Lock: firstParams.Lock, Template: firstParams.Template,
		RenderedHTML: firstRendered.HTML, RenderedText: firstRendered.Text,
		RenderedJSON: firstRendered.JSON,
	}
	earlier := eventPublicationTestScope(t, 1, 10, "Earlier")
	earlier.ScopeSessionID = 101
	earlier.Policy = store.ResultsPublicationAllAtCue
	standalone := eventPublicationTestScope(t, 1, 30, "Standalone")
	standalone.Scope = store.ResultsPublicationStandalone
	standalone.Policy = store.ResultsPublicationStandalonePolicy
	eventAward := eventPublicationTestAward(t, 1)
	secondRendered, secondParams, err := buildEventResultsPublication(
		store.EventResultsAggregateState{
			Competitions: competitions,
			EventAwards: []store.EventAward{{
				Key: "community", Name: "Community Award", DisplayOrder: 1,
			}},
			Publications: []store.ResultsPublication{
				later, earlier, standalone, eventAward,
			},
		},
		current,
		earlier,
		1,
		now.Add(time.Minute),
	)
	if err != nil ||
		secondParams.Status != store.ResultsPublicationFinal ||
		len(secondParams.Items) != 4 ||
		secondParams.Items[0].CompetitionSessionID != 10 ||
		secondParams.Items[1].CompetitionSessionID != 20 ||
		secondParams.Items[2].CompetitionSessionID != 30 ||
		secondParams.Items[3].Kind != prizegivingvalue.ItemEventAward {
		t.Fatalf("final Event Publication = %+v, %v", secondParams, err)
	}
	var model PublicResultsPublication
	if json.Unmarshal([]byte(secondRendered.JSON), &model) != nil ||
		model.Items[0].Competition.Title != "Earlier" ||
		model.Items[1].Competition.Title != "Later" ||
		model.Items[2].Competition.Title != "Standalone" ||
		model.Items[3].Award.Name != "Community Award" {
		t.Fatalf("final Event model = %+v", model)
	}
}

func eventPublicationTestAward(t *testing.T, eventID int) store.ResultsPublication {
	t.Helper()
	model := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         PublicResultsEvent{Name: "Event", Language: "en"},
		Revision:      1,
		Status:        ResultsPublicationFinal,
		PublishedAt:   time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
		Items: []PublicResultsItem{{
			Kind: ResultItemEventAward,
			Award: &PublicResultsAward{
				Key: "community", Name: "Community Award",
				Recipients: []string{"Volunteers"},
			},
		}},
	}
	rendered, err := RenderPublicResults(model, DefaultResultsTextTemplate())
	if err != nil {
		t.Fatalf("render Event Award Results: %v", err)
	}
	return store.ResultsPublication{
		EventID: eventID, Scope: store.ResultsPublicationEventAwards,
		ScopeSessionID: eventID, Revision: 1,
		Policy: store.ResultsPublicationStandalonePolicy,
		Status: store.ResultsPublicationFinal,
		Items: []store.PrizegivingResultItemRef{{
			Kind: prizegivingvalue.ItemEventAward, AwardKey: "community",
			DisplayOrder: 1,
		}},
		Template:     prizegivingTemplateInput(DefaultResultsTextTemplate()),
		RenderedHTML: rendered.HTML, RenderedText: rendered.Text,
		RenderedJSON: rendered.JSON,
	}
}

func eventPublicationTestScope(
	t *testing.T,
	eventID, sessionID int,
	title string,
) store.ResultsPublication {
	t.Helper()
	ref := store.PrizegivingResultItemRef{
		Kind:                 prizegivingvalue.ItemCompetitionResults,
		CompetitionSessionID: sessionID,
		DisplayOrder:         1,
	}
	model := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         PublicResultsEvent{Name: "Event", Language: "en"},
		Revision:      1,
		Status:        ResultsPublicationFinal,
		PublishedAt:   time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
		Items: []PublicResultsItem{{
			Kind: ResultItemCompetition,
			Competition: &PublicCompetitionResults{
				SessionID: sessionID, Title: title,
			},
		}},
	}
	rendered, err := RenderPublicResults(model, DefaultResultsTextTemplate())
	if err != nil {
		t.Fatalf("render scoped Results: %v", err)
	}
	return store.ResultsPublication{
		EventID: eventID, Scope: store.ResultsPublicationPrizegiving,
		ScopeSessionID: sessionID, Revision: 1,
		Policy: store.ResultsPublicationProgressive,
		Status: store.ResultsPublicationFinal, Items: []store.PrizegivingResultItemRef{ref},
		Template:     prizegivingTemplateInput(DefaultResultsTextTemplate()),
		RenderedHTML: rendered.HTML, RenderedText: rendered.Text,
		RenderedJSON: rendered.JSON,
	}
}

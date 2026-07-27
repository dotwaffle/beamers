package rundown

import (
	"slices"
	"testing"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestDraftReviewKeepsConflictedWorkVisible(t *testing.T) {
	preview, err := formPublishPreview(store.PublishState{
		DraftRevision: 4, PublishedRevision: 2,
		Changes: []store.PendingDraftChange{{
			ID: 7, Kind: "UpdateSession", TargetType: "Session", TargetID: 3,
			FactKey: "title", Status: "Conflicted",
			PayloadJSON: `{"before":"Published title","after":"Draft title"}`,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("review conflicted Draft: %v", err)
	}
	if len(preview.ChangeIDs) != 0 || len(preview.Changes) != 1 ||
		preview.Changes[0].Status != "Conflicted" ||
		len(preview.ValidationFailures) != 1 ||
		preview.Changes[0].CurrentValueJSON != `"Draft title"` {
		t.Fatalf("conflicted Draft review = %+v", preview)
	}
}

func TestDraftReviewRejectsCorruptEvidence(t *testing.T) {
	_, err := formPublishPreview(store.PublishState{
		Changes: []store.PendingDraftChange{{
			ID: 8, TargetType: "Session", TargetID: 3,
			Status: "Effective", PayloadJSON: `{`,
		}},
	}, nil)
	if err == nil {
		t.Fatal("review corrupt Draft evidence succeeded")
	}
}

func TestPublishPreviewNamesAffectedLanesAndDisplays(t *testing.T) {
	preview := PublishPreview{
		ChangeIDs: []int{4},
		Changes: []DraftChange{{
			ID: 4, TargetType: "Session", TargetID: 9,
		}},
	}
	err := addPublishImpact(&preview, store.CrewRundownState{
		Lanes: []store.PublishedLane{{ID: 2, Name: "Main", LocationID: 3}},
		Sessions: []store.PublishedSession{{
			ID: 9, LaneIDs: []int{2}, LocationIDs: []int{3},
		}},
	}, store.DraftRundownState{}, []store.PublishImpactDisplay{{
		ID: 5, Name: "Stage Left", LocationID: 3,
	}})
	if err != nil {
		t.Fatalf("form Publish impact: %v", err)
	}
	if len(preview.AffectedLanes) != 1 || preview.AffectedLanes[0] != "Lane #2" ||
		len(preview.AffectedDisplays) != 1 || preview.AffectedDisplays[0] != "Display #5 Stage Left" {
		t.Fatalf("Publish impact = %+v", preview)
	}
}

func TestPublishImpactIgnoresUnselectedDraftPlacement(t *testing.T) {
	preview := PublishPreview{
		ChangeIDs: []int{4},
		Changes: []DraftChange{
			{ID: 4, TargetType: "Session", TargetID: 9, FactKey: "title"},
			{
				ID: 5, TargetType: "Session", TargetID: 9, FactKey: "lanes:2",
				PreviousValueJSON: "false", CurrentValueJSON: "true",
			},
		},
	}
	err := addPublishImpact(&preview, store.CrewRundownState{
		Lanes: []store.PublishedLane{{ID: 1, LocationID: 10}},
		Sessions: []store.PublishedSession{{
			ID: 9, LaneIDs: []int{1}, LocationIDs: []int{10},
		}},
	}, store.DraftRundownState{
		Lanes: []store.PublishedLane{
			{ID: 1, LocationID: 10},
			{ID: 2, LocationID: 20},
		},
		Sessions: []store.PublishedSession{{
			ID: 9, LaneIDs: []int{2}, LocationIDs: []int{20},
		}},
	}, []store.PublishImpactDisplay{
		{ID: 1, Name: "Published route", LocationID: 10},
		{ID: 2, Name: "Draft route", LocationID: 20},
	})
	if err != nil {
		t.Fatalf("form partial Publish impact: %v", err)
	}
	if len(preview.AffectedLanes) != 1 || preview.AffectedLanes[0] != "Lane #1" ||
		len(preview.AffectedDisplays) != 1 ||
		preview.AffectedDisplays[0] != "Display #1 Published route" {
		t.Fatalf("partial Publish impact = %+v", preview)
	}
}

func TestPublishImpactIncludesPreviousAndNextSelectedPlacement(t *testing.T) {
	preview := PublishPreview{
		ChangeIDs: []int{5, 6, 7, 8},
		Changes: []DraftChange{
			{ID: 5, TargetType: "Session", TargetID: 9, FactKey: "lanes:1", CurrentValueJSON: "false"},
			{ID: 6, TargetType: "Session", TargetID: 9, FactKey: "lanes:2", CurrentValueJSON: "true"},
			{ID: 7, TargetType: "Session", TargetID: 9, FactKey: "locations:10", CurrentValueJSON: "false"},
			{ID: 8, TargetType: "Session", TargetID: 9, FactKey: "locations:20", CurrentValueJSON: "true"},
		},
	}
	err := addPublishImpact(&preview, store.CrewRundownState{
		Lanes:    []store.PublishedLane{{ID: 1, LocationID: 10}},
		Sessions: []store.PublishedSession{{ID: 9, LaneIDs: []int{1}, LocationIDs: []int{10}}},
	}, store.DraftRundownState{
		Lanes: []store.PublishedLane{{ID: 1, LocationID: 10}, {ID: 2, LocationID: 20}},
	}, []store.PublishImpactDisplay{
		{ID: 1, Name: "Old", LocationID: 10},
		{ID: 2, Name: "New", LocationID: 20},
	})
	if err != nil {
		t.Fatalf("form selected move impact: %v", err)
	}
	if !slices.Equal(preview.AffectedLanes, []string{"Lane #1", "Lane #2"}) ||
		!slices.Equal(preview.AffectedDisplays, []string{"Display #1 Old", "Display #2 New"}) {
		t.Fatalf("selected move impact = %+v", preview)
	}
}

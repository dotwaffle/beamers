package results_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestWorkspaceLoadsOrdinaryResultsState(t *testing.T) {
	storage, actor, eventID, _ := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	}
	ceremonyID, assignedID, standaloneID := publishWorkspaceSessions(
		t, storage, actor, eventID, now,
	)
	competitionService, err := competition.New(storage, now, nil, nil)
	if err != nil {
		t.Fatalf("create Competition service: %v", err)
	}
	entry, err := competitionService.CreateEntry(
		t.Context(),
		actor,
		competition.CreateEntryInput{
			EventID: eventID, SessionID: assignedID,
			CommandID: "create-workspace-entry", Name: "Finalist",
		},
	)
	if err != nil {
		t.Fatalf("create Competition Entry: %v", err)
	}
	entry, err = competitionService.ChangeDisposition(
		t.Context(),
		actor,
		competition.ChangeDispositionInput{
			EventID: eventID, SessionID: assignedID, EntryID: entry.ID,
			CommandID: "include-workspace-entry", ExpectedRevision: entry.Revision,
			Disposition: competition.DispositionIncluded,
		},
	)
	if err != nil {
		t.Fatalf("include Competition Entry: %v", err)
	}
	if _, err = competitionService.CreateEntry(
		t.Context(),
		actor,
		competition.CreateEntryInput{
			EventID: eventID, SessionID: assignedID,
			CommandID: "create-pending-workspace-entry", Name: "Pending",
		},
	); err != nil {
		t.Fatalf("create pending Competition Entry: %v", err)
	}

	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	if _, err = service.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-workspace-prizegiving",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	item := results.ResultItem{
		Kind: results.ResultItemCompetition, CompetitionSessionID: assignedID,
		DisplayOrder: 1, RevealMethod: results.RevealStatic,
	}
	plan, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:             "save-workspace-prizegiving-plan",
			CompetitionSessionIDs: []int{assignedID},
			Sequence:              []results.ResultItem{item},
			PublicationOrder:      []results.ResultItemRef{item.Ref(1)},
			ReleasePolicy:         results.ResultsProgressiveOnReveal,
			Template:              results.DefaultResultsTextTemplate(),
		},
	)
	if err != nil {
		t.Fatalf("save Prizegiving plan: %v", err)
	}
	eventAwards, err := service.SaveEventAwards(
		t.Context(),
		actor,
		results.SaveEventAwardsInput{
			EventID: eventID, CommandID: "save-workspace-event-awards",
			Awards: []results.EventAward{{
				Award: results.Award{
					Key: "community", Name: "Community Award",
					Recipients:   []results.AwardRecipient{{DisplayName: "Volunteers"}},
					DisplayOrder: 1,
				},
				ReleasePath: results.AwardReleasePath{Kind: results.StandaloneRelease},
			}},
		},
	)
	if err != nil {
		t.Fatalf("save Event Awards: %v", err)
	}
	if _, err = service.Save(
		t.Context(),
		actor,
		results.SaveInput{
			EventID: eventID, SessionID: standaloneID,
			CommandID:      "save-workspace-standalone-results",
			Disposition:    results.NoPublicResults,
			NoPublicReason: "Organizer decision",
			Score:          results.ScorePolicy{Type: results.None},
		},
	); err != nil {
		t.Fatalf("save standalone Results: %v", err)
	}
	if _, err = service.ReleaseStandaloneResults(
		t.Context(),
		actor,
		results.ReleaseStandaloneResultsInput{
			EventID: eventID, CompetitionSessionID: standaloneID,
			CommandID: "release-workspace-standalone-results",
		},
	); err != nil {
		t.Fatalf("release standalone Results: %v", err)
	}
	history, err := service.GetCorrectionHistory(
		t.Context(),
		actor,
		eventID,
		results.PublicationScopeStandalone,
		standaloneID,
	)
	if err != nil || len(history.Publications) != 1 {
		t.Fatalf("load standalone publication history = %+v, %v", history, err)
	}
	publication := history.Publications[0]
	var model results.PublicResultsPublication
	if err = json.Unmarshal([]byte(publication.JSON), &model); err != nil {
		t.Fatalf("decode standalone publication: %v", err)
	}
	model.Items[0].NoPublicResults.Explanation = "Corrected organizer decision"
	correction, err := service.SaveCorrection(
		t.Context(),
		actor,
		results.SaveCorrectionInput{
			EventID: eventID, Scope: results.PublicationScopeStandalone,
			ScopeSessionID:          standaloneID,
			CommandID:               "save-workspace-results-correction",
			BasePublicationRevision: publication.Revision,
			PublicationOrder:        publication.PublicationOrder,
			Items:                   model.Items,
			Template:                publication.Template,
			CrewReason:              "Clarify the public explanation.",
		},
	)
	if err != nil {
		t.Fatalf("save Results Correction: %v", err)
	}

	workspace, err := service.Workspace(
		t.Context(),
		actor,
		results.WorkspaceRequest{EventID: eventID},
	)
	if err != nil {
		t.Fatalf("load Results Workspace: %v", err)
	}
	if workspace.Event.ID != eventID ||
		workspace.EventAwards.Revision != eventAwards.Revision ||
		len(workspace.Competitions) != 2 ||
		len(workspace.Prizegivings) != 1 ||
		workspace.Prizegivings[0].Preview != nil ||
		workspace.EventAwardsPreflight != nil {
		t.Fatalf("Results Workspace = %+v", workspace)
	}
	assigned := workspaceCompetition(t, workspace, assignedID)
	if assigned.AssignedPrizegivingID != ceremonyID ||
		len(assigned.Entries) != 1 ||
		assigned.Entries[0].ID != entry.ID ||
		assigned.Entries[0].Standing.Standing != results.Unplaced ||
		assigned.Entries[0].Standing.DisplayOrder != 1 {
		t.Fatalf("assigned Competition = %+v", assigned)
	}
	standalone := workspaceCompetition(t, workspace, standaloneID)
	if standalone.Correction == nil ||
		standalone.Correction.Current.Revision != correction.Revision ||
		standalone.Correction.PublicationRevision != publication.Revision ||
		standalone.Correction.PublicationHistoryCount != 1 ||
		len(standalone.Correction.Publications) != 1 ||
		!strings.Contains(
			standalone.Correction.CorrectedResultsJSON,
			"Corrected organizer decision",
		) {
		t.Fatalf("standalone Competition = %+v", standalone)
	}
	prizegiving := workspace.Prizegivings[0]
	if !prizegiving.Designated ||
		prizegiving.Session.ID != ceremonyID ||
		prizegiving.Plan.Revision != plan.Revision {
		t.Fatalf("Prizegiving = %+v", prizegiving)
	}

	blockedPreview, err := service.Workspace(
		t.Context(),
		actor,
		results.WorkspaceRequest{
			EventID: eventID,
			PrizegivingPreview: &results.WorkspacePrizegivingPreviewRequest{
				CeremonySessionID: ceremonyID,
				Mode:              results.PrizegivingPreviewModePreview,
			},
		},
	)
	if err != nil ||
		len(blockedPreview.Prizegivings[0].PreviewFindings) != 1 ||
		blockedPreview.Prizegivings[0].PreviewFindings[0].Code !=
			"prizegiving_preflight_required" {
		t.Fatalf("blocked Workspace Preview = %+v, %v", blockedPreview, err)
	}
	blockedAwards, err := service.Workspace(
		t.Context(),
		actor,
		results.WorkspaceRequest{EventID: eventID, EventAwardsPreflight: true},
	)
	if err != nil ||
		blockedAwards.EventAwardsPreflight == nil ||
		len(blockedAwards.EventAwardsPreflight.Findings) != 1 ||
		blockedAwards.EventAwardsPreflight.Findings[0].Code !=
			"event_awards_not_ready" {
		t.Fatalf("blocked Event Awards Workspace = %+v, %v", blockedAwards, err)
	}

	if _, err = service.MarkEventAwardsReady(
		t.Context(),
		actor,
		results.MarkEventAwardsReadyInput{
			EventID: eventID, CommandID: "ready-workspace-event-awards",
			ExpectedRevision:     eventAwards.Revision,
			ReleasePath:          results.AwardReleasePath{Kind: results.StandaloneRelease},
			ExpectedPathRevision: 1,
		},
	); err != nil {
		t.Fatalf("mark Workspace Event Awards Ready: %v", err)
	}
	readyAwards, err := service.Workspace(
		t.Context(),
		actor,
		results.WorkspaceRequest{EventID: eventID, EventAwardsPreflight: true},
	)
	if err != nil ||
		readyAwards.EventAwardsPreflight == nil ||
		len(readyAwards.EventAwardsPreflight.Findings) != 0 {
		t.Fatalf("ready Event Awards Workspace = %+v, %v", readyAwards, err)
	}

	_, err = service.Save(
		t.Context(),
		actor,
		results.SaveInput{
			EventID: eventID, SessionID: assignedID,
			CommandID:      "save-workspace-prizegiving-results",
			Disposition:    results.NoPublicResults,
			NoPublicReason: "Organizer decision",
			Score:          results.ScorePolicy{Type: results.None},
		},
	)
	if err != nil {
		t.Fatalf("save Workspace Prizegiving Results: %v", err)
	}
	item.Kind = results.ResultItemNoPublicResults
	plan, err = service.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:             "update-workspace-prizegiving-plan",
			ExpectedRevision:      plan.Revision,
			CompetitionSessionIDs: []int{assignedID},
			Sequence:              []results.ResultItem{item},
			PublicationOrder:      []results.ResultItemRef{item.Ref(1)},
			ReleasePolicy:         results.ResultsProgressiveOnReveal,
			Template:              results.DefaultResultsTextTemplate(),
		},
	)
	if err != nil {
		t.Fatalf("update Workspace Prizegiving plan: %v", err)
	}
	if _, err = service.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:        "preflight-workspace-prizegiving",
			ExpectedRevision: plan.Revision,
		},
	); err != nil {
		t.Fatalf("preflight Workspace Prizegiving: %v", err)
	}
	previewed, err := service.Workspace(
		t.Context(),
		actor,
		results.WorkspaceRequest{
			EventID: eventID,
			PrizegivingPreview: &results.WorkspacePrizegivingPreviewRequest{
				CeremonySessionID: ceremonyID,
				Mode:              results.PrizegivingPreviewModePreview,
			},
		},
	)
	if err != nil ||
		previewed.Prizegivings[0].Preview == nil ||
		previewed.Prizegivings[0].Preview.Watermark == "" ||
		len(previewed.Prizegivings[0].PreviewFindings) != 0 {
		t.Fatalf("Workspace Preview = %+v, %v", previewed, err)
	}

	unauthorized := actor
	unauthorized.Administrator = false
	unauthorized.EventRoles = nil
	if _, err = service.Workspace(
		t.Context(),
		unauthorized,
		results.WorkspaceRequest{EventID: eventID},
	); !errors.Is(err, results.ErrViewRequired) {
		t.Fatalf("unauthorized Workspace error = %v", err)
	}
	viewerOnly := actor
	viewerOnly.Administrator = false
	viewerOnly.EventRoles = map[int]viewer.Role{eventID: viewer.Observer}
	viewerOnly.EventScopes = map[int]viewer.EventScope{
		eventID: {
			Capabilities: map[viewer.Capability]struct{}{
				viewer.ViewResults: {},
			},
		},
	}
	if _, err = service.Workspace(
		t.Context(),
		viewerOnly,
		results.WorkspaceRequest{
			EventID: eventID, EventAwardsPreflight: true,
		},
	); !errors.Is(err, results.ErrProducerRequired) {
		t.Fatalf("unauthorized Workspace Preflight error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = service.Workspace(
		canceled,
		actor,
		results.WorkspaceRequest{EventID: eventID},
	); err == nil {
		t.Fatal("Workspace succeeded after a required read was canceled")
	}
}

func workspaceCompetition(
	t *testing.T,
	workspace results.Workspace,
	sessionID int,
) results.WorkspaceCompetition {
	t.Helper()
	for _, competition := range workspace.Competitions {
		if competition.Session.ID == sessionID {
			return competition
		}
	}
	t.Fatalf("Competition %d missing from Workspace", sessionID)
	return results.WorkspaceCompetition{}
}

func publishWorkspaceSessions(
	t *testing.T,
	storage *store.SQLite,
	actor auth.Account,
	eventID int,
	now func() time.Time,
) (int, int, int) {
	t.Helper()
	commands, err := rundown.NewCommands(storage, now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-workspace-sessions",
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane",
			Location: rundown.TargetRef{Ref: "main"},
		}},
		Sessions: []rundown.SessionDraftInput{
			{
				Ref: "assigned", Title: "Assigned Final",
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
				Ref: "standalone", Title: "Standalone Final",
				Type:               rundown.SessionCompetition,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start.Add(time.Hour),
				PlannedEnd:         start.Add(2 * time.Hour),
				TimingPolicy:       rundown.TimingFixedEnd,
				MinimumDuration:    30 * time.Minute,
				StartBoundary:      rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				SubmissionDeadline: start.Add(-time.Hour),
				Lanes:              []rundown.TargetRef{{Ref: "main-lane"}},
			},
			{
				Ref: "ceremony", Title: "Prizegiving",
				Type:               rundown.SessionCeremony,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start.Add(3 * time.Hour),
				PlannedEnd:         start.Add(4 * time.Hour),
				TimingPolicy:       rundown.TimingFixedEnd,
				MinimumDuration:    30 * time.Minute,
				StartBoundary:      rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				Lanes: []rundown.TargetRef{{Ref: "main-lane"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create Workspace sessions: %v", err)
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
		t.Fatalf("preview Workspace sessions: %v", err)
	}
	if _, err = commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-workspace-sessions",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision:     preview.DraftRevision,
			PublishedRevision: preview.PublishedRevision,
			ChangeIDs:         preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Workspace sessions: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("load Workspace sessions: %v", err)
	}
	var ceremonyID, assignedID, standaloneID int
	for _, session := range crew.Sessions {
		switch session.Title {
		case "Prizegiving":
			ceremonyID = session.ID
		case "Assigned Final":
			assignedID = session.ID
		case "Standalone Final":
			standaloneID = session.ID
		}
	}
	if ceremonyID == 0 || assignedID == 0 || standaloneID == 0 {
		t.Fatalf("published Workspace sessions = %+v", crew.Sessions)
	}
	return ceremonyID, assignedID, standaloneID
}

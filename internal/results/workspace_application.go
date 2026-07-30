package results

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// Workspace is the ordinary event-scoped Results preparation and release view.
type Workspace struct {
	Event                WorkspaceEvent
	Competitions         []WorkspaceCompetition
	Prizegivings         []WorkspacePrizegiving
	EventAwards          EventAwardsDraft
	EventAwardsPreflight *StandaloneEventAwardsPreflight
}

// WorkspaceRequest selects ordinary state and optional preview or preflight work.
type WorkspaceRequest struct {
	EventID              int
	PrizegivingPreview   *WorkspacePrizegivingPreviewRequest
	EventAwardsPreflight bool
}

// WorkspacePrizegivingPreviewRequest selects one side-effect-free preview.
type WorkspacePrizegivingPreviewRequest struct {
	CeremonySessionID int
	Mode              PrizegivingPreviewMode
}

// WorkspaceEvent is the Event identity and locale needed for Results work.
type WorkspaceEvent struct {
	ID          int
	Name        string
	EventLocale string
}

// WorkspaceSession is one published Competition or Ceremony.
type WorkspaceSession struct {
	ID    int
	Title string
}

// WorkspaceCompetition combines one published Competition with Results work.
type WorkspaceCompetition struct {
	Session               WorkspaceSession
	Draft                 Draft
	Entries               []WorkspaceEntry
	AssignedPrizegivingID int
	Correction            *WorkspaceCorrection
}

// WorkspaceEntry combines one eligible Entry with its current or default Standing.
type WorkspaceEntry struct {
	ID       int
	Name     string
	Standing Standing
}

// WorkspacePrizegiving combines one Ceremony with its optional designation.
type WorkspacePrizegiving struct {
	Session         WorkspaceSession
	Designated      bool
	Plan            PrizegivingPlan
	Preview         *PrizegivingPreview
	PreviewFindings []PrizegivingPreflightFinding
}

// WorkspaceCorrection contains current correction and immutable publication state.
type WorkspaceCorrection struct {
	Scope                   PublicationScope
	ScopeSessionID          int
	Current                 Correction
	PublicationRevision     int
	CorrectedResultsJSON    string
	PublicationHistoryCount int
	Publications            []PublicationHistoryRevision
}

// Workspace returns all ordinary Results work for one authorized Event.
func (service *Service) Workspace(
	ctx context.Context,
	actor auth.Account,
	request WorkspaceRequest,
) (Workspace, error) {
	if request.EventID <= 0 {
		return Workspace{}, ErrInvalidInput
	}
	if !actor.HasCapability(request.EventID, viewer.ViewResults) {
		return Workspace{}, ErrViewRequired
	}
	event, err := service.storage.FindCrewEvent(
		actor.Context(ctx), actor.ID, request.EventID,
	)
	if err != nil {
		return Workspace{}, err
	}
	crewRundown, err := service.storage.LoadCrewRundown(
		actor.Context(ctx), request.EventID,
	)
	if err != nil {
		return Workspace{}, err
	}
	workspace := Workspace{
		Event: WorkspaceEvent{
			ID: event.ID, Name: event.Name, EventLocale: event.EventLocale,
		},
		Competitions: make([]WorkspaceCompetition, 0),
		Prizegivings: make([]WorkspacePrizegiving, 0),
	}
	assignments := make(map[int]int)
	for _, session := range crewRundown.Sessions {
		workspaceSession := WorkspaceSession{ID: session.ID, Title: session.Title}
		switch session.Type {
		case "Competition":
			found, loadErr := service.workspaceCompetition(
				ctx, actor, request.EventID, workspaceSession,
			)
			if loadErr != nil {
				return Workspace{}, loadErr
			}
			workspace.Competitions = append(workspace.Competitions, found)
		case "Ceremony":
			plan, loadErr := service.GetPrizegivingPlan(
				ctx, actor, request.EventID, session.ID,
			)
			if errors.Is(loadErr, ErrPrizegivingSession) {
				workspace.Prizegivings = append(
					workspace.Prizegivings,
					WorkspacePrizegiving{Session: workspaceSession},
				)
				continue
			}
			if loadErr != nil {
				return Workspace{}, loadErr
			}
			if plan.Template.Revision == 0 {
				plan.Template = DefaultResultsTextTemplate()
			}
			for _, competitionID := range plan.CompetitionSessionIDs {
				assignments[competitionID] = session.ID
			}
			workspace.Prizegivings = append(
				workspace.Prizegivings,
				WorkspacePrizegiving{
					Session: workspaceSession, Designated: true, Plan: plan,
				},
			)
		default:
			continue
		}
	}
	for index := range workspace.Competitions {
		workspace.Competitions[index].AssignedPrizegivingID =
			assignments[workspace.Competitions[index].Session.ID]
	}
	workspace.EventAwards, err = service.GetEventAwards(
		ctx,
		actor,
		request.EventID,
	)
	if err != nil {
		return Workspace{}, err
	}
	if request.EventAwardsPreflight {
		preflight, preflightErr := service.PreflightStandaloneEventAwards(
			ctx,
			actor,
			request.EventID,
		)
		if preflightErr != nil {
			return Workspace{}, preflightErr
		}
		workspace.EventAwardsPreflight = &preflight
	}
	if request.PrizegivingPreview != nil {
		if err := service.loadWorkspacePrizegivingPreview(
			ctx,
			actor,
			request.EventID,
			request.PrizegivingPreview,
			&workspace,
		); err != nil {
			return Workspace{}, err
		}
	}
	return workspace, nil
}

func (service *Service) loadWorkspacePrizegivingPreview(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	request *WorkspacePrizegivingPreviewRequest,
	workspace *Workspace,
) error {
	if request.CeremonySessionID <= 0 ||
		request.Mode != PrizegivingPreviewModePreview &&
			request.Mode != PrizegivingPreviewModeRehearsal {
		return ErrInvalidInput
	}
	index := -1
	for candidate := range workspace.Prizegivings {
		if workspace.Prizegivings[candidate].Session.ID == request.CeremonySessionID {
			index = candidate
			break
		}
	}
	if index < 0 {
		return ErrInvalidInput
	}
	preview, err := service.PreviewPrizegiving(
		ctx,
		actor,
		eventID,
		request.CeremonySessionID,
		request.Mode,
	)
	switch {
	case err == nil:
		workspace.Prizegivings[index].Preview = &preview
		return nil
	case errors.Is(err, ErrPrizegivingPreflightRequired):
		workspace.Prizegivings[index].PreviewFindings =
			[]PrizegivingPreflightFinding{prizegivingFinding(
				"prizegiving_preflight_required",
				err.Error(),
			)}
		return nil
	case errors.Is(err, ErrPrizegivingSession):
		workspace.Prizegivings[index].PreviewFindings =
			[]PrizegivingPreflightFinding{prizegivingFinding(
				"prizegiving_session_not_found",
				err.Error(),
			)}
		return nil
	default:
		return err
	}
}

func (service *Service) workspaceCompetition(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	session WorkspaceSession,
) (WorkspaceCompetition, error) {
	state, err := service.storage.LoadCompetition(
		actor.Context(ctx), eventID, session.ID,
	)
	if err != nil {
		return WorkspaceCompetition{}, err
	}
	draft, err := service.Get(ctx, actor, eventID, session.ID)
	if err != nil {
		return WorkspaceCompetition{}, err
	}
	standings := make(map[int]Standing, len(draft.Standings))
	for _, standing := range draft.Standings {
		standings[standing.EntryID] = standing
	}
	entries := make([]WorkspaceEntry, 0, len(state.Entries))
	for _, entry := range state.Entries {
		if entry.Disposition != "Included" ||
			entry.ResultDisposition != "Eligible" {
			continue
		}
		standing, ok := standings[entry.ID]
		if !ok {
			standing = Standing{
				EntryID: entry.ID, Standing: Unplaced,
				DisplayOrder: len(entries) + 1,
			}
		}
		entries = append(entries, WorkspaceEntry{
			ID: entry.ID, Name: entry.Name, Standing: standing,
		})
	}
	correction, err := service.workspaceCorrection(
		ctx, actor, eventID, session.ID,
	)
	if err != nil {
		return WorkspaceCompetition{}, err
	}
	return WorkspaceCompetition{
		Session: session, Draft: draft, Entries: entries, Correction: correction,
	}, nil
}

func (service *Service) workspaceCorrection(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (*WorkspaceCorrection, error) {
	history, err := service.GetCorrectionHistory(
		ctx, actor, eventID, PublicationScopeStandalone, sessionID,
	)
	if err != nil {
		return nil, err
	}
	if len(history.Publications) == 0 {
		return nil, nil
	}
	latest := history.Publications[0]
	for _, publication := range history.Publications[1:] {
		if publication.Revision > latest.Revision {
			latest = publication
		}
	}
	current, err := service.GetCorrection(
		ctx, actor, eventID, PublicationScopeStandalone, sessionID,
	)
	if err != nil {
		return nil, err
	}
	correctedJSON := latest.JSON
	if current.Revision > 0 &&
		(current.Status == CorrectionDraft || current.Status == CorrectionReady) {
		var model PublicResultsPublication
		if unmarshalErr := json.Unmarshal([]byte(latest.JSON), &model); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		model.Items = current.Proposal.Items
		encoded, marshalErr := json.MarshalIndent(model, "", "  ")
		if marshalErr != nil {
			return nil, marshalErr
		}
		correctedJSON = string(encoded)
	}
	return &WorkspaceCorrection{
		Scope: PublicationScopeStandalone, ScopeSessionID: sessionID,
		Current: current, PublicationRevision: latest.Revision,
		CorrectedResultsJSON:    correctedJSON,
		PublicationHistoryCount: len(history.Publications),
		Publications:            history.Publications,
	}, nil
}

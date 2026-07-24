package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionresultsdraft"
	"github.com/dotwaffle/beamers/ent/competitionresultstanding"
)

var (
	// ErrCompetitionResultsRevision means a Results command used a stale revision.
	ErrCompetitionResultsRevision = errors.New("competition results revision conflict")
)

// CompetitionResultStandingInput is one Entry result in a proposed Draft.
type CompetitionResultStandingInput struct {
	EntryID            int
	Standing           string
	Placement          int
	DisplayOrder       int
	DecimalScore       *string
	DurationScoreNanos *int64
}

// CompetitionResultStanding is one immutable Entry result.
type CompetitionResultStanding struct {
	EntryID            int     `json:"entry_id"`
	Standing           string  `json:"standing"`
	Placement          int     `json:"placement,omitempty"`
	DisplayOrder       int     `json:"display_order"`
	DecimalScore       *string `json:"decimal_score,omitempty"`
	DurationScoreNanos *int64  `json:"duration_score_nanos,omitempty"`
}

// CompetitionResultsDraft is one versioned Competition Results proposal.
type CompetitionResultsDraft struct {
	ID                  int                         `json:"id"`
	EventID             int                         `json:"event_id"`
	SessionID           int                         `json:"session_id"`
	Revision            int                         `json:"revision"`
	Disposition         string                      `json:"disposition"`
	NoPublicCrewReason  string                      `json:"no_public_crew_reason,omitempty"`
	PublicExplanation   string                      `json:"public_explanation,omitempty"`
	ScoreType           string                      `json:"score_type"`
	ScoreVisibility     string                      `json:"score_visibility"`
	ScoreUnit           string                      `json:"score_unit,omitempty"`
	ScorePrecision      int                         `json:"score_precision"`
	ScoreRequirement    string                      `json:"score_requirement"`
	ScoreInterpretation string                      `json:"score_interpretation"`
	Ready               bool                        `json:"ready"`
	ReadyByAccountID    int                         `json:"ready_by_account_id,omitempty"`
	ReadyAt             time.Time                   `json:"ready_at,omitzero"`
	CreatedByAccountID  int                         `json:"created_by_account_id"`
	CreatedAt           time.Time                   `json:"created_at"`
	Standings           []CompetitionResultStanding `json:"standings"`
}

// SaveCompetitionResultsDraftParams contains one whole immutable revision.
type SaveCompetitionResultsDraftParams struct {
	EventID, SessionID  int
	ExpectedRevision    int
	Disposition         string
	NoPublicCrewReason  string
	PublicExplanation   string
	ScoreType           string
	ScoreVisibility     string
	ScoreUnit           string
	ScorePrecision      int
	ScoreRequirement    string
	ScoreInterpretation string
	CreatedByAccountID  int
	Now                 time.Time
	Standings           []CompetitionResultStandingInput
}

// MarkCompetitionResultsReadyParams confirms one exact current revision.
type MarkCompetitionResultsReadyParams struct {
	EventID, SessionID  int
	ExpectedRevision    int
	ReviewedByAccountID int
	Now                 time.Time
}

// LoadCompetitionResultsDraft returns the current Results revision.
func (installation *SQLite) LoadCompetitionResultsDraft(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionResultsDraft, error) {
	found, err := installation.client.CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(eventID),
			competitionresultsdraft.CompetitionSessionIDEQ(sessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		WithStandings(func(query *ent.CompetitionResultStandingQuery) {
			query.Order(ent.Asc(competitionresultstanding.FieldDisplayOrder))
		}).
		First(ctx)
	if ent.IsNotFound(err) {
		return CompetitionResultsDraft{
			EventID: eventID, SessionID: sessionID,
			Disposition: "Pending", ScoreType: "None",
			ScoreVisibility: "Public", ScoreRequirement: "Optional",
			ScoreInterpretation: "Informational",
		}, nil
	}
	if err != nil {
		return CompetitionResultsDraft{}, opaqueError("load Competition Results Draft", err)
	}
	return competitionResultsDraft(found), nil
}

// SaveCompetitionResultsDraft appends one whole Results revision.
func (transaction *CommandTx) SaveCompetitionResultsDraft(
	ctx context.Context,
	params SaveCompetitionResultsDraftParams,
) (CompetitionResultsDraft, error) {
	client := transaction.transaction.Client()
	internalContext := systemContext(ctx)
	if _, _, err := loadCompetitionConfiguration(
		internalContext,
		client.Session,
		client.Event,
		params.EventID,
		params.SessionID,
	); err != nil {
		return CompetitionResultsDraft{}, err
	}
	current, err := client.CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(params.EventID),
			competitionresultsdraft.CompetitionSessionIDEQ(params.SessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		First(internalContext)
	currentRevision := 0
	if err == nil {
		currentRevision = current.Revision
	} else if !ent.IsNotFound(err) {
		return CompetitionResultsDraft{}, opaqueError("load current Competition Results Draft", err)
	}
	if currentRevision != params.ExpectedRevision {
		return CompetitionResultsDraft{}, ErrCompetitionResultsRevision
	}
	scoreVisibility := params.ScoreVisibility
	if scoreVisibility == "" {
		scoreVisibility = "Public"
	}
	scoreRequirement := params.ScoreRequirement
	if scoreRequirement == "" {
		scoreRequirement = "Optional"
	}
	scoreInterpretation := params.ScoreInterpretation
	if scoreInterpretation == "" {
		scoreInterpretation = "Informational"
	}
	created, err := client.CompetitionResultsDraft.Create().
		SetEventID(params.EventID).
		SetCompetitionSessionID(params.SessionID).
		SetRevision(currentRevision + 1).
		SetDisposition(competitionresultsdraft.Disposition(params.Disposition)).
		SetNoPublicCrewReason(params.NoPublicCrewReason).
		SetPublicExplanation(params.PublicExplanation).
		SetScoreType(competitionresultsdraft.ScoreType(params.ScoreType)).
		SetScoreVisibility(competitionresultsdraft.ScoreVisibility(scoreVisibility)).
		SetScoreUnit(params.ScoreUnit).
		SetScorePrecision(params.ScorePrecision).
		SetScoreRequirement(competitionresultsdraft.ScoreRequirement(scoreRequirement)).
		SetScoreInterpretation(competitionresultsdraft.ScoreInterpretation(scoreInterpretation)).
		SetCreatedByAccountID(params.CreatedByAccountID).
		SetCreatedAt(params.Now.UTC()).
		Save(ctx)
	if err != nil {
		return CompetitionResultsDraft{}, opaqueError("create Competition Results Draft", err)
	}
	for _, standing := range params.Standings {
		if _, err = client.CompetitionResultStanding.Create().
			SetEventID(params.EventID).
			SetResultsDraftID(created.ID).
			SetCompetitionSessionID(params.SessionID).
			SetEntryID(standing.EntryID).
			SetStanding(competitionresultstanding.Standing(standing.Standing)).
			SetNillablePlacement(optionalPositiveInt(standing.Placement)).
			SetDisplayOrder(standing.DisplayOrder).
			SetNillableDecimalScore(standing.DecimalScore).
			SetNillableDurationScoreNanos(standing.DurationScoreNanos).
			Save(ctx); err != nil {
			return CompetitionResultsDraft{}, opaqueError("create Competition Result Standing", err)
		}
	}
	return loadCompetitionResultsDraftByID(internalContext, client, created.ID)
}

// MarkCompetitionResultsReady marks one exact current revision as reviewed.
func (transaction *CommandTx) MarkCompetitionResultsReady(
	ctx context.Context,
	params MarkCompetitionResultsReadyParams,
) (CompetitionResultsDraft, error) {
	client := transaction.transaction.Client()
	internalContext := systemContext(ctx)
	current, err := client.CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(params.EventID),
			competitionresultsdraft.CompetitionSessionIDEQ(params.SessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		First(internalContext)
	if ent.IsNotFound(err) || err == nil && current.Revision != params.ExpectedRevision {
		return CompetitionResultsDraft{}, ErrCompetitionResultsRevision
	}
	if err != nil {
		return CompetitionResultsDraft{}, opaqueError("load Results Draft for review", err)
	}
	updated, err := current.Update().
		SetReadyByAccountID(params.ReviewedByAccountID).
		SetReadyAt(params.Now.UTC()).
		Save(ctx)
	if err != nil {
		return CompetitionResultsDraft{}, opaqueError("mark Competition Results Ready", err)
	}
	return loadCompetitionResultsDraftByID(internalContext, client, updated.ID)
}

func loadCompetitionResultsDraftByID(
	ctx context.Context,
	client *ent.Client,
	draftID int,
) (CompetitionResultsDraft, error) {
	found, err := client.CompetitionResultsDraft.Query().
		Where(competitionresultsdraft.IDEQ(draftID)).
		WithStandings(func(query *ent.CompetitionResultStandingQuery) {
			query.Order(ent.Asc(competitionresultstanding.FieldDisplayOrder))
		}).
		Only(ctx)
	if err != nil {
		return CompetitionResultsDraft{}, opaqueError("reload Competition Results Draft", err)
	}
	return competitionResultsDraft(found), nil
}

func competitionResultsDraft(found *ent.CompetitionResultsDraft) CompetitionResultsDraft {
	result := CompetitionResultsDraft{
		ID: found.ID, EventID: found.EventID, SessionID: found.CompetitionSessionID,
		Revision: found.Revision, Disposition: string(found.Disposition),
		NoPublicCrewReason: found.NoPublicCrewReason, PublicExplanation: found.PublicExplanation,
		ScoreType: string(found.ScoreType), ScoreVisibility: string(found.ScoreVisibility),
		ScoreUnit: found.ScoreUnit, ScorePrecision: found.ScorePrecision,
		ScoreRequirement:    string(found.ScoreRequirement),
		ScoreInterpretation: string(found.ScoreInterpretation),
		CreatedByAccountID:  found.CreatedByAccountID, CreatedAt: found.CreatedAt,
		Standings: make([]CompetitionResultStanding, 0, len(found.Edges.Standings)),
	}
	if found.ReadyAt != nil && found.ReadyByAccountID != nil {
		result.Ready = true
		result.ReadyAt = *found.ReadyAt
		result.ReadyByAccountID = *found.ReadyByAccountID
	}
	for _, standing := range found.Edges.Standings {
		item := CompetitionResultStanding{
			EntryID: standing.EntryID, Standing: string(standing.Standing),
			DisplayOrder: standing.DisplayOrder, DecimalScore: standing.DecimalScore,
			DurationScoreNanos: standing.DurationScoreNanos,
		}
		if standing.Placement != nil {
			item.Placement = *standing.Placement
		}
		result.Standings = append(result.Standings, item)
	}
	return result
}

func optionalPositiveInt(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

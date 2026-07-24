package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/competitionresultsdraft"
	"github.com/dotwaffle/beamers/ent/competitionresultstanding"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/eventawardsdraft"
	"github.com/dotwaffle/beamers/ent/prizegiving"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/awardvalue"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrCompetitionResultsRevision means a Results command used a stale revision.
	ErrCompetitionResultsRevision = errors.New("competition results revision conflict")
	// ErrCompetitionResultsEntry means a Standing references an Entry outside its Competition.
	ErrCompetitionResultsEntry = errors.New("competition results entry is outside the competition")
	// ErrResultsAwardEntry means an Award references an Entry outside its scope.
	ErrResultsAwardEntry = errors.New("result award entry is outside its scope")
	// ErrEventAwardsRevision means an Event Awards command used a stale revision.
	ErrEventAwardsRevision = errors.New("event awards revision conflict")
	// ErrEventAwardPath means an Event Award references an invalid release path.
	ErrEventAwardPath = errors.New("event award release path is invalid")
	// ErrPrizegivingSession means a designation does not target an Event Ceremony.
	ErrPrizegivingSession = errors.New("prizegiving must designate an Event Ceremony")
)

// AwardRecipientInput identifies one Entry or explicit display-name recipient.
type AwardRecipientInput struct {
	EntryID     int    `json:"entry_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// CompetitionAwardInput is one Competition Award snapshot value.
type CompetitionAwardInput struct {
	Key          string                `json:"key"`
	Name         string                `json:"name"`
	Recipients   []AwardRecipientInput `json:"recipients"`
	Promoted     bool                  `json:"promoted,omitempty"`
	DisplayOrder int                   `json:"display_order"`
}

// CompetitionAward is one persisted Competition Award snapshot value.
type CompetitionAward = CompetitionAwardInput

// AwardReleasePath identifies one independently reviewed Event Award path.
type AwardReleasePath struct {
	Kind                 string `json:"kind"`
	PrizegivingSessionID int    `json:"prizegiving_session_id,omitempty"`
}

// EventAwardInput is one Event Award snapshot value.
type EventAwardInput struct {
	Key          string                `json:"key"`
	Name         string                `json:"name"`
	Recipients   []AwardRecipientInput `json:"recipients"`
	DisplayOrder int                   `json:"display_order"`
	ReleasePath  AwardReleasePath      `json:"release_path"`
}

// EventAward is one persisted Event Award snapshot value.
type EventAward = EventAwardInput

// EventAwardPathState records review of one effective path revision.
type EventAwardPathState struct {
	ReleasePath      AwardReleasePath `json:"release_path"`
	Revision         int              `json:"revision"`
	Ready            bool             `json:"ready"`
	ReadyByAccountID int              `json:"ready_by_account_id,omitempty"`
	ReadyAt          time.Time        `json:"ready_at,omitzero"`
}

// EventAwardsDraft is one versioned Event Awards proposal.
type EventAwardsDraft struct {
	ID                 int                   `json:"id"`
	EventID            int                   `json:"event_id"`
	Revision           int                   `json:"revision"`
	Awards             []EventAward          `json:"awards"`
	PathStates         []EventAwardPathState `json:"path_states"`
	CreatedByAccountID int                   `json:"created_by_account_id"`
	CreatedAt          time.Time             `json:"created_at"`
}

// Prizegiving is one Ceremony Session explicitly designated for Results release.
type Prizegiving struct {
	ID                 int       `json:"id"`
	EventID            int       `json:"event_id"`
	CeremonySessionID  int       `json:"ceremony_session_id"`
	CreatedByAccountID int       `json:"created_by_account_id"`
	CreatedAt          time.Time `json:"created_at"`
}

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
	Awards              []CompetitionAward          `json:"awards"`
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
	Awards              []CompetitionAwardInput
}

// SaveEventAwardsDraftParams contains one whole immutable Event Awards revision.
type SaveEventAwardsDraftParams struct {
	EventID            int
	ExpectedRevision   int
	CreatedByAccountID int
	Now                time.Time
	Awards             []EventAwardInput
}

// MarkEventAwardsReadyParams confirms one exact path revision.
type MarkEventAwardsReadyParams struct {
	EventID              int
	ExpectedRevision     int
	ReleasePath          AwardReleasePath
	ExpectedPathRevision int
	ReviewedByAccountID  int
	Now                  time.Time
}

// DesignatePrizegivingParams identifies one Ceremony Session.
type DesignatePrizegivingParams struct {
	EventID, CeremonySessionID int
	CreatedByAccountID         int
	Now                        time.Time
}

// MarkCompetitionResultsReadyParams confirms one exact current revision.
type MarkCompetitionResultsReadyParams struct {
	EventID, SessionID  int
	ExpectedRevision    int
	ReviewedByAccountID int
	Now                 time.Time
}

// CompetitionResultsEligibleEntry is one Included, eligible Entry and its
// current canonical presentation order.
type CompetitionResultsEligibleEntry struct {
	ID          int
	LockedOrder int
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

// LoadEventAwardsDraft returns the current Event Awards revision.
func (installation *SQLite) LoadEventAwardsDraft(
	ctx context.Context,
	eventID int,
) (EventAwardsDraft, error) {
	found, err := installation.client.EventAwardsDraft.Query().
		Where(eventawardsdraft.EventIDEQ(eventID)).
		Order(ent.Desc(eventawardsdraft.FieldRevision)).
		First(ctx)
	if ent.IsNotFound(err) {
		return EventAwardsDraft{EventID: eventID}, nil
	}
	if err != nil {
		return EventAwardsDraft{}, opaqueError("load Event Awards Draft", err)
	}
	return eventAwardsDraft(found), nil
}

// LoadCompetitionResultsDraft returns the current Results revision in a command.
func (transaction *CommandTx) LoadCompetitionResultsDraft(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionResultsDraft, error) {
	found, err := transaction.transaction.Client().CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(eventID),
			competitionresultsdraft.CompetitionSessionIDEQ(sessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		WithStandings(func(query *ent.CompetitionResultStandingQuery) {
			query.Order(ent.Asc(competitionresultstanding.FieldDisplayOrder))
		}).
		First(systemContext(ctx))
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

// LoadEventAwardsDraft returns the current Event Awards revision in a command.
func (transaction *CommandTx) LoadEventAwardsDraft(
	ctx context.Context,
	eventID int,
) (EventAwardsDraft, error) {
	found, err := transaction.transaction.Client().EventAwardsDraft.Query().
		Where(eventawardsdraft.EventIDEQ(eventID)).
		Order(ent.Desc(eventawardsdraft.FieldRevision)).
		First(systemContext(ctx))
	if ent.IsNotFound(err) {
		return EventAwardsDraft{EventID: eventID}, nil
	}
	if err != nil {
		return EventAwardsDraft{}, opaqueError("load Event Awards Draft", err)
	}
	return eventAwardsDraft(found), nil
}

// DesignatePrizegiving records one Ceremony Session as a Results release path.
func (transaction *CommandTx) DesignatePrizegiving(
	ctx context.Context,
	params DesignatePrizegivingParams,
) (Prizegiving, error) {
	client := transaction.transaction.Client()
	internalContext := systemContext(ctx)
	ceremony, err := client.Session.Query().
		Where(
			session.IDEQ(params.CeremonySessionID),
			session.EventIDEQ(params.EventID),
		).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return Prizegiving{}, ErrPrizegivingSession
	}
	if err != nil {
		return Prizegiving{}, opaqueError("load Prizegiving Ceremony", err)
	}
	published, err := ceremony.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(internalContext)
	if err != nil || published.Type != sessionpublishedversion.TypeCeremony {
		return Prizegiving{}, ErrPrizegivingSession
	}
	existing, err := client.Prizegiving.Query().
		Where(prizegiving.CeremonySessionIDEQ(params.CeremonySessionID)).
		Only(internalContext)
	if err == nil {
		return storedPrizegiving(existing), nil
	}
	if !ent.IsNotFound(err) {
		return Prizegiving{}, opaqueError("load Prizegiving designation", err)
	}
	created, err := client.Prizegiving.Create().
		SetEventID(params.EventID).
		SetCeremonySessionID(params.CeremonySessionID).
		SetCreatedByAccountID(params.CreatedByAccountID).
		SetCreatedAt(params.Now.UTC()).
		Save(ctx)
	if err != nil {
		return Prizegiving{}, opaqueError("designate Prizegiving", err)
	}
	return storedPrizegiving(created), nil
}

// SaveEventAwardsDraft appends one whole Event Awards revision.
func (transaction *CommandTx) SaveEventAwardsDraft(
	ctx context.Context,
	params SaveEventAwardsDraftParams,
) (EventAwardsDraft, error) {
	client := transaction.transaction.Client()
	internalContext := systemContext(ctx)
	if _, err := client.Event.Query().
		Where(event.IDEQ(params.EventID)).
		Only(internalContext); ent.IsNotFound(err) {
		return EventAwardsDraft{}, ErrEventNotFound
	} else if err != nil {
		return EventAwardsDraft{}, opaqueError("load Event for Awards", err)
	}
	current, err := transaction.LoadEventAwardsDraft(internalContext, params.EventID)
	if err != nil {
		return EventAwardsDraft{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return EventAwardsDraft{}, ErrEventAwardsRevision
	}
	entryIDs := eventAwardRecipientEntryIDs(params.Awards)
	if len(entryIDs) > 0 {
		count, countErr := client.CompetitionEntry.Query().
			Where(
				competitionentry.IDIn(entryIDs...),
				competitionentry.EventIDEQ(params.EventID),
			).
			Count(internalContext)
		if countErr != nil {
			return EventAwardsDraft{}, opaqueError("validate Event Award Entries", countErr)
		}
		if count != len(entryIDs) {
			return EventAwardsDraft{}, ErrResultsAwardEntry
		}
	}
	if pathErr := validateEventAwardPrizegivings(
		internalContext, client, params.EventID, params.Awards,
	); pathErr != nil {
		return EventAwardsDraft{}, pathErr
	}
	created, err := client.EventAwardsDraft.Create().
		SetEventID(params.EventID).
		SetRevision(current.Revision + 1).
		SetAwards(eventAwardValues(params.Awards)).
		SetPathStates(eventAwardPathStateValues(
			nextEventAwardPathStates(current, params.Awards),
		)).
		SetCreatedByAccountID(params.CreatedByAccountID).
		SetCreatedAt(params.Now.UTC()).
		Save(ctx)
	if err != nil {
		return EventAwardsDraft{}, opaqueError("create Event Awards Draft", err)
	}
	return eventAwardsDraft(created), nil
}

// MarkEventAwardsReady records Producer review of one exact path revision.
func (transaction *CommandTx) MarkEventAwardsReady(
	ctx context.Context,
	params MarkEventAwardsReadyParams,
) (EventAwardsDraft, error) {
	current, err := transaction.LoadEventAwardsDraft(ctx, params.EventID)
	if err != nil {
		return EventAwardsDraft{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return EventAwardsDraft{}, ErrEventAwardsRevision
	}
	found := false
	for index := range current.PathStates {
		state := &current.PathStates[index]
		if state.ReleasePath == params.ReleasePath {
			if state.Revision != params.ExpectedPathRevision {
				return EventAwardsDraft{}, ErrEventAwardsRevision
			}
			state.Ready = true
			state.ReadyByAccountID = params.ReviewedByAccountID
			state.ReadyAt = params.Now.UTC()
			found = true
			break
		}
	}
	if !found {
		return EventAwardsDraft{}, ErrEventAwardPath
	}
	updated, err := transaction.transaction.Client().EventAwardsDraft.
		UpdateOneID(current.ID).
		SetPathStates(eventAwardPathStateValues(current.PathStates)).
		Save(ctx)
	if err != nil {
		return EventAwardsDraft{}, opaqueError("mark Event Awards Ready", err)
	}
	return eventAwardsDraft(updated), nil
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
	entryIDs := make([]int, 0, len(params.Standings))
	uniqueEntryIDs := make(map[int]struct{}, len(params.Standings))
	for _, standing := range params.Standings {
		uniqueEntryIDs[standing.EntryID] = struct{}{}
	}
	for entryID := range uniqueEntryIDs {
		entryIDs = append(entryIDs, entryID)
	}
	if len(entryIDs) != len(params.Standings) {
		return CompetitionResultsDraft{}, ErrCompetitionResultsEntry
	}
	if len(entryIDs) > 0 {
		entryCount, countErr := client.CompetitionEntry.Query().
			Where(
				competitionentry.IDIn(entryIDs...),
				competitionentry.EventIDEQ(params.EventID),
				competitionentry.CompetitionSessionIDEQ(params.SessionID),
			).
			Count(internalContext)
		if countErr != nil {
			return CompetitionResultsDraft{}, opaqueError("validate Competition Result Entries", countErr)
		}
		if entryCount != len(entryIDs) {
			return CompetitionResultsDraft{}, ErrCompetitionResultsEntry
		}
	}
	awardEntryIDs := awardRecipientEntryIDs(params.Awards)
	if len(awardEntryIDs) > 0 {
		entryCount, countErr := client.CompetitionEntry.Query().
			Where(
				competitionentry.IDIn(awardEntryIDs...),
				competitionentry.EventIDEQ(params.EventID),
				competitionentry.CompetitionSessionIDEQ(params.SessionID),
			).
			Count(internalContext)
		if countErr != nil {
			return CompetitionResultsDraft{}, opaqueError("validate Competition Award Entries", countErr)
		}
		if entryCount != len(awardEntryIDs) {
			return CompetitionResultsDraft{}, ErrResultsAwardEntry
		}
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
		SetAwards(competitionAwardValues(params.Awards)).
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

// LoadCompetitionResultsReviewState returns the current Draft and the exact
// eligible Entry set from the same command transaction.
func (transaction *CommandTx) LoadCompetitionResultsReviewState(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionResultsDraft, []CompetitionResultsEligibleEntry, error) {
	internalContext := systemContext(ctx)
	client := transaction.transaction.Client()
	current, err := client.CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(eventID),
			competitionresultsdraft.CompetitionSessionIDEQ(sessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		First(internalContext)
	if ent.IsNotFound(err) {
		return CompetitionResultsDraft{}, nil, ErrCompetitionResultsRevision
	}
	if err != nil {
		return CompetitionResultsDraft{}, nil, opaqueError("load Results Draft for review", err)
	}
	draft, err := loadCompetitionResultsDraftByID(internalContext, client, current.ID)
	if err != nil {
		return CompetitionResultsDraft{}, nil, err
	}
	_, competition, err := transaction.competitionConfiguration(
		internalContext, eventID, sessionID,
	)
	if err != nil {
		return CompetitionResultsDraft{}, nil, err
	}
	included, err := transaction.includedCompetitionEntries(internalContext, sessionID)
	if err != nil {
		return CompetitionResultsDraft{}, nil, err
	}
	order, _, err := competitionEntryOrder(competition, included)
	if err != nil {
		return CompetitionResultsDraft{}, nil, err
	}
	eligibleIDs := make(map[int]struct{}, len(included))
	for _, entry := range included {
		if entry.ResultDisposition == competitionentry.ResultDispositionEligible {
			eligibleIDs[entry.ID] = struct{}{}
		}
	}
	eligible := make([]CompetitionResultsEligibleEntry, 0, len(eligibleIDs))
	for index, entryID := range order.EntryIDs {
		if _, ok := eligibleIDs[entryID]; ok {
			eligible = append(eligible, CompetitionResultsEligibleEntry{
				ID: entryID, LockedOrder: index + 1,
			})
		}
	}
	return draft, eligible, nil
}

// SupersedeCompetitionResultsDraft clones the current Draft into a new,
// non-Ready revision after a relevant Competition fact changes.
func (transaction *CommandTx) SupersedeCompetitionResultsDraft(
	ctx context.Context,
	eventID, sessionID int,
	now time.Time,
) error {
	internalContext := systemContext(ctx)
	client := transaction.transaction.Client()
	current, err := client.CompetitionResultsDraft.Query().
		Where(
			competitionresultsdraft.EventIDEQ(eventID),
			competitionresultsdraft.CompetitionSessionIDEQ(sessionID),
		).
		Order(ent.Desc(competitionresultsdraft.FieldRevision)).
		WithStandings(func(query *ent.CompetitionResultStandingQuery) {
			query.Order(ent.Asc(competitionresultstanding.FieldDisplayOrder))
		}).
		First(internalContext)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load Results Draft to supersede", err)
	}
	identity, ok := viewer.FromContext(ctx)
	if !ok || identity.AccountID <= 0 {
		return errors.New("supersede Results Draft: viewer context is missing")
	}
	found := competitionResultsDraft(current)
	standings := make([]CompetitionResultStandingInput, 0, len(found.Standings))
	for _, standing := range found.Standings {
		standings = append(standings, CompetitionResultStandingInput(standing))
	}
	awards := append([]CompetitionAwardInput(nil), found.Awards...)
	_, err = transaction.SaveCompetitionResultsDraft(
		internalContext,
		SaveCompetitionResultsDraftParams{
			EventID: eventID, SessionID: sessionID,
			ExpectedRevision:   found.Revision,
			Disposition:        found.Disposition,
			NoPublicCrewReason: found.NoPublicCrewReason,
			PublicExplanation:  found.PublicExplanation,
			ScoreType:          found.ScoreType, ScoreVisibility: found.ScoreVisibility,
			ScoreUnit: found.ScoreUnit, ScorePrecision: found.ScorePrecision,
			ScoreRequirement:    found.ScoreRequirement,
			ScoreInterpretation: found.ScoreInterpretation,
			CreatedByAccountID:  identity.AccountID, Now: now,
			Standings: standings,
			Awards:    awards,
		},
	)
	return err
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
		Awards:    competitionAwards(found.Awards),
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

func awardRecipientEntryIDs(awards []CompetitionAwardInput) []int {
	seen := make(map[int]struct{})
	for _, award := range awards {
		for _, recipient := range award.Recipients {
			if recipient.EntryID > 0 {
				seen[recipient.EntryID] = struct{}{}
			}
		}
	}
	return mapKeys(seen)
}

func eventAwardRecipientEntryIDs(awards []EventAwardInput) []int {
	seen := make(map[int]struct{})
	for _, award := range awards {
		for _, recipient := range award.Recipients {
			if recipient.EntryID > 0 {
				seen[recipient.EntryID] = struct{}{}
			}
		}
	}
	return mapKeys(seen)
}

func mapKeys(values map[int]struct{}) []int {
	keys := make([]int, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}

func competitionAwardValues(awards []CompetitionAwardInput) []awardvalue.Competition {
	values := make([]awardvalue.Competition, 0, len(awards))
	for _, award := range awards {
		values = append(values, awardvalue.Competition{
			Key: award.Key, Name: award.Name, Promoted: award.Promoted,
			DisplayOrder: award.DisplayOrder,
			Recipients:   awardRecipientValues(award.Recipients),
		})
	}
	return values
}

func competitionAwards(values []awardvalue.Competition) []CompetitionAward {
	awards := make([]CompetitionAward, 0, len(values))
	for _, value := range values {
		awards = append(awards, CompetitionAward{
			Key: value.Key, Name: value.Name, Promoted: value.Promoted,
			DisplayOrder: value.DisplayOrder,
			Recipients:   awardRecipients(value.Recipients),
		})
	}
	return awards
}

func awardRecipientValues(recipients []AwardRecipientInput) []awardvalue.Recipient {
	values := make([]awardvalue.Recipient, 0, len(recipients))
	for _, recipient := range recipients {
		values = append(values, awardvalue.Recipient{
			EntryID: recipient.EntryID, DisplayName: recipient.DisplayName,
		})
	}
	return values
}

func awardRecipients(values []awardvalue.Recipient) []AwardRecipientInput {
	recipients := make([]AwardRecipientInput, 0, len(values))
	for _, value := range values {
		recipients = append(recipients, AwardRecipientInput{
			EntryID: value.EntryID, DisplayName: value.DisplayName,
		})
	}
	return recipients
}

func validateEventAwardPrizegivings(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	awards []EventAwardInput,
) error {
	sessionIDs := make(map[int]struct{})
	for _, award := range awards {
		switch award.ReleasePath.Kind {
		case "Standalone":
			if award.ReleasePath.PrizegivingSessionID != 0 {
				return ErrEventAwardPath
			}
		case "Prizegiving":
			if award.ReleasePath.PrizegivingSessionID <= 0 {
				return ErrEventAwardPath
			}
			sessionIDs[award.ReleasePath.PrizegivingSessionID] = struct{}{}
		default:
			return ErrEventAwardPath
		}
	}
	ids := mapKeys(sessionIDs)
	if len(ids) == 0 {
		return nil
	}
	count, err := client.Prizegiving.Query().
		Where(
			prizegiving.CeremonySessionIDIn(ids...),
			prizegiving.EventIDEQ(eventID),
		).
		Count(ctx)
	if err != nil {
		return opaqueError("validate Event Award Prizegivings", err)
	}
	if count != len(ids) {
		return ErrEventAwardPath
	}
	found, err := client.Session.Query().
		Where(session.IDIn(ids...), session.EventIDEQ(eventID)).
		All(ctx)
	if err != nil {
		return opaqueError("validate Prizegiving Ceremonies", err)
	}
	for _, ceremony := range found {
		published, queryErr := ceremony.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if queryErr != nil || published.Type != sessionpublishedversion.TypeCeremony {
			return ErrEventAwardPath
		}
	}
	return nil
}

func eventAwardValues(awards []EventAwardInput) []awardvalue.Event {
	values := make([]awardvalue.Event, 0, len(awards))
	for _, award := range awards {
		values = append(values, awardvalue.Event{
			Key: award.Key, Name: award.Name, DisplayOrder: award.DisplayOrder,
			Recipients: awardRecipientValues(award.Recipients),
			ReleasePath: awardvalue.ReleasePath{
				Kind:                 award.ReleasePath.Kind,
				PrizegivingSessionID: award.ReleasePath.PrizegivingSessionID,
			},
		})
	}
	return values
}

func eventAwards(values []awardvalue.Event) []EventAward {
	awards := make([]EventAward, 0, len(values))
	for _, value := range values {
		awards = append(awards, EventAward{
			Key: value.Key, Name: value.Name, DisplayOrder: value.DisplayOrder,
			Recipients: awardRecipients(value.Recipients),
			ReleasePath: AwardReleasePath{
				Kind:                 value.ReleasePath.Kind,
				PrizegivingSessionID: value.ReleasePath.PrizegivingSessionID,
			},
		})
	}
	return awards
}

func eventAwardPathStateValues(states []EventAwardPathState) []awardvalue.PathState {
	values := make([]awardvalue.PathState, 0, len(states))
	for _, state := range states {
		var readyAt *time.Time
		if state.Ready {
			value := state.ReadyAt
			readyAt = &value
		}
		values = append(values, awardvalue.PathState{
			ReleasePath: awardvalue.ReleasePath{
				Kind:                 state.ReleasePath.Kind,
				PrizegivingSessionID: state.ReleasePath.PrizegivingSessionID,
			},
			Revision: state.Revision, ReadyByAccountID: state.ReadyByAccountID,
			ReadyAt: readyAt,
		})
	}
	return values
}

func eventAwardPathStates(values []awardvalue.PathState) []EventAwardPathState {
	states := make([]EventAwardPathState, 0, len(values))
	for _, value := range values {
		state := EventAwardPathState{
			ReleasePath: AwardReleasePath{
				Kind:                 value.ReleasePath.Kind,
				PrizegivingSessionID: value.ReleasePath.PrizegivingSessionID,
			},
			Revision: value.Revision, ReadyByAccountID: value.ReadyByAccountID,
		}
		if value.ReadyAt != nil {
			state.Ready = true
			state.ReadyAt = *value.ReadyAt
		}
		states = append(states, state)
	}
	return states
}

func nextEventAwardPathStates(
	current EventAwardsDraft,
	next []EventAwardInput,
) []EventAwardPathState {
	states := make(map[AwardReleasePath]EventAwardPathState, len(current.PathStates))
	for _, state := range current.PathStates {
		states[state.ReleasePath] = state
	}
	paths := make(map[AwardReleasePath]struct{})
	for _, award := range current.Awards {
		paths[award.ReleasePath] = struct{}{}
	}
	for _, award := range next {
		paths[award.ReleasePath] = struct{}{}
	}
	for path := range paths {
		if equalEventPathAwards(
			eventAwardsForPath(current.Awards, path),
			eventAwardsForPath(next, path),
		) {
			continue
		}
		state := states[path]
		state.ReleasePath = path
		state.Revision++
		state.Ready = false
		state.ReadyByAccountID = 0
		state.ReadyAt = time.Time{}
		states[path] = state
	}
	result := make([]EventAwardPathState, 0, len(states))
	for _, state := range states {
		result = append(result, state)
	}
	sort.Slice(result, func(first, second int) bool {
		if result[first].ReleasePath.Kind != result[second].ReleasePath.Kind {
			return result[first].ReleasePath.Kind == "Standalone"
		}
		return result[first].ReleasePath.PrizegivingSessionID <
			result[second].ReleasePath.PrizegivingSessionID
	})
	return result
}

func eventAwardsForPath(
	values []EventAwardInput,
	path AwardReleasePath,
) []EventAwardInput {
	found := make([]EventAwardInput, 0)
	for _, value := range values {
		if value.ReleasePath == path {
			found = append(found, value)
		}
	}
	sort.Slice(found, func(first, second int) bool {
		return found[first].DisplayOrder < found[second].DisplayOrder
	})
	return found
}

func equalEventPathAwards(first, second []EventAwardInput) bool {
	return slices.EqualFunc(first, second, func(left, right EventAwardInput) bool {
		return left.Key == right.Key &&
			left.Name == right.Name &&
			left.DisplayOrder == right.DisplayOrder &&
			left.ReleasePath == right.ReleasePath &&
			slices.Equal(left.Recipients, right.Recipients)
	})
}

func eventAwardsDraft(found *ent.EventAwardsDraft) EventAwardsDraft {
	return EventAwardsDraft{
		ID: found.ID, EventID: found.EventID, Revision: found.Revision,
		Awards: eventAwards(found.Awards), PathStates: eventAwardPathStates(found.PathStates),
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

func storedPrizegiving(found *ent.Prizegiving) Prizegiving {
	return Prizegiving{
		ID: found.ID, EventID: found.EventID,
		CeremonySessionID:  found.CeremonySessionID,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

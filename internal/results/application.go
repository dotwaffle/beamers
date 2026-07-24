package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrViewRequired means the actor cannot read unreleased Results Drafts.
	ErrViewRequired = errors.New("results view capability required")
	// ErrManageRequired means the actor cannot change Results Drafts.
	ErrManageRequired = errors.New("results manage capability required")
	// ErrProducerRequired means only a Producer can complete Results review.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrRevisionConflict means a Results command used a stale Draft revision.
	ErrRevisionConflict = store.ErrCompetitionResultsRevision
	// ErrEntryOutsideCompetition means a Standing crossed an ownership boundary.
	ErrEntryOutsideCompetition = store.ErrCompetitionResultsEntry
	// ErrCompetitionNotFound means no Competition matched the stable IDs.
	ErrCompetitionNotFound = store.ErrCompetitionNotFound
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrInvalidInput means a Results request is malformed or unsafe.
	ErrInvalidInput = errors.New("invalid results input")
)

// SaveInput contains one whole proposed immutable Draft revision.
type SaveInput struct {
	EventID           int         `json:"event_id"`
	SessionID         int         `json:"session_id"`
	CommandID         string      `json:"command_id"`
	ExpectedRevision  int         `json:"expected_revision"`
	Disposition       Disposition `json:"disposition"`
	NoPublicReason    string      `json:"no_public_reason,omitempty"`
	PublicExplanation string      `json:"public_explanation,omitempty"`
	Score             ScorePolicy `json:"score"`
	Standings         []Standing  `json:"standings"`
}

// MarkReadyInput identifies one exact Draft revision for Producer review.
type MarkReadyInput struct {
	EventID          int    `json:"event_id"`
	SessionID        int    `json:"session_id"`
	CommandID        string `json:"command_id"`
	ExpectedRevision int    `json:"expected_revision"`
}

// Service owns unreleased Results Draft queries and durable commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates a Results Service with explicit dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("results storage is required")
	}
	if now == nil {
		return nil, errors.New("results clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Get returns the current Results Draft to explicitly authorized Event crew.
func (service *Service) Get(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Draft, error) {
	if eventID <= 0 || sessionID <= 0 {
		return Draft{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return Draft{}, ErrViewRequired
	}
	found, err := service.storage.LoadCompetitionResultsDraft(
		actor.Context(ctx), eventID, sessionID,
	)
	if err != nil {
		return Draft{}, err
	}
	return draft(found), nil
}

// Save appends one complete proposed Draft snapshot and clears Ready by versioning.
func (service *Service) Save(
	ctx context.Context,
	actor auth.Account,
	input SaveInput,
) (Draft, error) {
	input.Score = scorePolicyDefaults(input.Score)
	if err := validateSaveInput(input); err != nil {
		return Draft{}, err
	}
	if !actor.HasCapability(input.EventID, viewer.ManageResults) {
		return Draft{}, ErrManageRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Draft{}, errors.New("encode Results Draft command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "SaveCompetitionResultsDraft",
		TargetType:     "Competition",
		TargetID:       strconv.Itoa(input.SessionID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Draft]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (Draft, error) {
			var stored store.CompetitionResultsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Draft{}, err
			}
			return draft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Draft], error) {
			stored, saveErr := transaction.SaveCompetitionResultsDraft(
				actor.Context(ctx), saveParams(input, actor.ID, identity.Now),
			)
			if saveErr != nil {
				return command.Execution[Draft]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Draft]{}, errors.New("encode Results Draft outcome")
			}
			return command.Success(draft(stored), string(outcome)), nil
		},
	})
}

// MarkReady records Producer review of one exact current Publish revision.
func (service *Service) MarkReady(
	ctx context.Context,
	actor auth.Account,
	input MarkReadyInput,
) (Draft, error) {
	if err := validateMarkReadyInput(input); err != nil {
		return Draft{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Draft{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Draft{}, errors.New("encode Results review command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "MarkCompetitionResultsReady",
		TargetType:     "Competition",
		TargetID:       strconv.Itoa(input.SessionID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Draft]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (Draft, error) {
			var stored store.CompetitionResultsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Draft{}, err
			}
			return draft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Draft], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Draft]{}, ErrProducerRequired
			}
			current, storedEntries, loadErr := transaction.LoadCompetitionResultsReviewState(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[Draft]{}, loadErr
			}
			if current.Revision != input.ExpectedRevision {
				return command.Execution[Draft]{}, ErrRevisionConflict
			}
			entries := make([]EligibleEntry, 0, len(storedEntries))
			for _, entry := range storedEntries {
				entries = append(entries, EligibleEntry{
					ID: entry.ID, LockedOrder: entry.LockedOrder,
				})
			}
			if reviewErr := Review(draft(current), entries); reviewErr != nil {
				return command.Execution[Draft]{}, reviewErr
			}
			stored, markErr := transaction.MarkCompetitionResultsReady(
				actor.Context(ctx), store.MarkCompetitionResultsReadyParams{
					EventID: input.EventID, SessionID: input.SessionID,
					ExpectedRevision:    input.ExpectedRevision,
					ReviewedByAccountID: actor.ID, Now: identity.Now,
				},
			)
			if markErr != nil {
				return command.Execution[Draft]{}, markErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Draft]{}, errors.New("encode Results review outcome")
			}
			return command.Success(draft(stored), string(outcome)), nil
		},
	})
}

func validateSaveInput(input SaveInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 ||
		len(input.Standings) > 10000 ||
		!boundedText(input.NoPublicReason, 10000) ||
		!boundedText(input.PublicExplanation, 10000) {
		return ErrInvalidInput
	}
	if err := ValidateDraft(Draft{
		Disposition: input.Disposition, NoPublicReason: input.NoPublicReason,
		PublicExplanation: input.PublicExplanation, Score: input.Score,
		Standings: input.Standings,
	}); err != nil {
		return err
	}
	return nil
}

func validateMarkReadyInput(input MarkReadyInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision <= 0 {
		return ErrInvalidInput
	}
	return nil
}

func boundedText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}

func scorePolicyDefaults(policy ScorePolicy) ScorePolicy {
	if policy.Visibility == "" {
		policy.Visibility = ScorePublic
	}
	if policy.Requirement == "" {
		policy.Requirement = ScoreOptional
	}
	if policy.Interpretation == "" {
		policy.Interpretation = Informational
	}
	return policy
}

func saveParams(
	input SaveInput,
	actorID int,
	now time.Time,
) store.SaveCompetitionResultsDraftParams {
	params := store.SaveCompetitionResultsDraftParams{
		EventID: input.EventID, SessionID: input.SessionID,
		ExpectedRevision: input.ExpectedRevision,
		Disposition:      string(input.Disposition), NoPublicCrewReason: input.NoPublicReason,
		PublicExplanation: input.PublicExplanation,
		ScoreType:         string(input.Score.Type), ScoreVisibility: string(input.Score.Visibility),
		ScoreUnit: input.Score.Unit, ScorePrecision: input.Score.Precision,
		ScoreRequirement:    string(input.Score.Requirement),
		ScoreInterpretation: string(input.Score.Interpretation),
		CreatedByAccountID:  actorID, Now: now,
		Standings: make([]store.CompetitionResultStandingInput, 0, len(input.Standings)),
	}
	for _, standing := range input.Standings {
		stored := store.CompetitionResultStandingInput{
			EntryID: standing.EntryID, Standing: string(standing.Standing),
			Placement: standing.Placement, DisplayOrder: standing.DisplayOrder,
			DecimalScore: standing.Score.Decimal,
		}
		if standing.Score.Duration != nil {
			nanos := standing.Score.Duration.Nanoseconds()
			stored.DurationScoreNanos = &nanos
		}
		params.Standings = append(params.Standings, stored)
	}
	return params
}

func draft(stored store.CompetitionResultsDraft) Draft {
	result := Draft{
		ID: stored.ID, EventID: stored.EventID, SessionID: stored.SessionID,
		Revision: stored.Revision, Disposition: Disposition(stored.Disposition),
		NoPublicReason: stored.NoPublicCrewReason, PublicExplanation: stored.PublicExplanation,
		Score: ScorePolicy{
			Type: ScoreType(stored.ScoreType), Visibility: ScoreVisibility(stored.ScoreVisibility),
			Unit: stored.ScoreUnit, Precision: stored.ScorePrecision,
			Requirement:    ScoreRequirement(stored.ScoreRequirement),
			Interpretation: ScoreInterpretation(stored.ScoreInterpretation),
		},
		Ready: stored.Ready, ReadyByAccountID: stored.ReadyByAccountID,
		ReadyAt: stored.ReadyAt, CreatedByAccountID: stored.CreatedByAccountID,
		CreatedAt: stored.CreatedAt,
		Standings: make([]Standing, 0, len(stored.Standings)),
	}
	for _, standing := range stored.Standings {
		score := ScoreValue{Decimal: standing.DecimalScore}
		if standing.DurationScoreNanos != nil {
			duration := time.Duration(*standing.DurationScoreNanos)
			score.Duration = &duration
		}
		result.Standings = append(result.Standings, Standing{
			EntryID: standing.EntryID, Standing: ResultStanding(standing.Standing),
			Placement: standing.Placement, DisplayOrder: standing.DisplayOrder,
			Score: score,
		})
	}
	return result
}

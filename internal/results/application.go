package results

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/awardvalue"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
	"github.com/dotwaffle/beamers/internal/votingvalue"
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
	// ErrAwardEntryOutsideScope means an Award recipient crossed an ownership boundary.
	ErrAwardEntryOutsideScope = store.ErrResultsAwardEntry
	// ErrEventAwardsRevision means an Event Awards command used a stale Draft revision.
	ErrEventAwardsRevision = store.ErrEventAwardsRevision
	// ErrEventAwardPath means an Event Award targets an invalid release path.
	ErrEventAwardPath = store.ErrEventAwardPath
	// ErrPrizegivingSession means a designation does not target an Event Ceremony.
	ErrPrizegivingSession = store.ErrPrizegivingSession
	// ErrCompetitionNotFound means no Competition matched the stable IDs.
	ErrCompetitionNotFound = store.ErrCompetitionNotFound
	// ErrEventNotFound means no Event matched the stable ID.
	ErrEventNotFound = store.ErrEventNotFound
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrInvalidInput means a Results request is malformed or unsafe.
	ErrInvalidInput = errors.New("invalid results input")
)

// SaveInput contains one whole proposed immutable Draft revision.
type SaveInput struct {
	EventID             int         `json:"event_id"`
	SessionID           int         `json:"session_id"`
	CommandID           string      `json:"command_id"`
	ExpectedRevision    int         `json:"expected_revision"`
	Disposition         Disposition `json:"disposition"`
	NoPublicReason      string      `json:"no_public_reason,omitempty"`
	TallyOverrideReason string      `json:"tally_override_reason,omitempty"`
	PublicExplanation   string      `json:"public_explanation,omitempty"`
	Score               ScorePolicy `json:"score"`
	Standings           []Standing  `json:"standings"`
}

// MarkReadyInput identifies one exact Draft revision for Producer review.
type MarkReadyInput struct {
	EventID          int    `json:"event_id"`
	SessionID        int    `json:"session_id"`
	CommandID        string `json:"command_id"`
	ExpectedRevision int    `json:"expected_revision"`
}

// SaveCompetitionAwardsInput replaces Awards while preserving Placement and Score.
type SaveCompetitionAwardsInput struct {
	EventID          int     `json:"event_id"`
	SessionID        int     `json:"session_id"`
	CommandID        string  `json:"command_id"`
	ExpectedRevision int     `json:"expected_revision"`
	Awards           []Award `json:"awards"`
}

// EventAwardPathState records review of one effective release-path revision.
type EventAwardPathState struct {
	ReleasePath      AwardReleasePath
	Revision         int
	Ready            bool
	ReadyByAccountID int
	ReadyAt          time.Time
}

// EventAwardsDraft is one versioned Event Awards proposal and its path reviews.
type EventAwardsDraft struct {
	ID                 int
	EventID            int
	Revision           int
	Awards             []EventAward
	PathStates         []EventAwardPathState
	CreatedByAccountID int
	CreatedAt          time.Time
}

// Prizegiving is one explicitly designated Ceremony Session.
type Prizegiving struct {
	ID                 int
	EventID            int
	CeremonySessionID  int
	CreatedByAccountID int
	CreatedAt          time.Time
}

// DesignatePrizegivingInput identifies one Ceremony Session for Results release.
type DesignatePrizegivingInput struct {
	EventID           int    `json:"event_id"`
	CeremonySessionID int    `json:"ceremony_session_id"`
	CommandID         string `json:"command_id"`
}

// SaveEventAwardsInput replaces one Event's complete Award assignment snapshot.
type SaveEventAwardsInput struct {
	EventID          int          `json:"event_id"`
	CommandID        string       `json:"command_id"`
	ExpectedRevision int          `json:"expected_revision"`
	Awards           []EventAward `json:"awards"`
}

// MarkEventAwardsReadyInput identifies one exact path revision for review.
type MarkEventAwardsReadyInput struct {
	EventID              int              `json:"event_id"`
	CommandID            string           `json:"command_id"`
	ExpectedRevision     int              `json:"expected_revision"`
	ReleasePath          AwardReleasePath `json:"release_path"`
	ExpectedPathRevision int              `json:"expected_path_revision"`
}

// Service owns unreleased Results Draft queries and durable commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// PublicArtifact is one frozen released Results representation set.
type PublicArtifact struct {
	Revision int
	HTML     string
	Text     string
	JSON     string
}

// PublicArtifact returns a released Results revision without crew authority.
func (service *Service) PublicArtifact(
	ctx context.Context,
	eventID int,
	scope PublicationScope,
	scopeSessionID int,
	revision int,
) (PublicArtifact, bool, error) {
	if eventID <= 0 || scopeSessionID <= 0 || revision < 0 ||
		(scope != PublicationScopePrizegiving &&
			scope != PublicationScopeStandalone &&
			scope != PublicationScopeEventAwards &&
			scope != PublicationScopeEvent) {
		return PublicArtifact{}, false, ErrInvalidInput
	}
	storeScope := map[PublicationScope]store.ResultsPublicationScope{
		PublicationScopePrizegiving: store.ResultsPublicationPrizegiving,
		PublicationScopeStandalone:  store.ResultsPublicationStandalone,
		PublicationScopeEventAwards: store.ResultsPublicationEventAwards,
		PublicationScopeEvent:       store.ResultsPublicationEvent,
	}[scope]
	if revision == 0 {
		found, err := service.storage.LoadResultsPublication(
			ctx,
			eventID,
			storeScope,
			scopeSessionID,
		)
		if err != nil {
			return PublicArtifact{}, false, err
		}
		if found.Revision == 0 {
			return PublicArtifact{}, false, nil
		}
		return publicArtifact(found), true, nil
	}
	found, exists, err := service.storage.LoadResultsPublicationRevision(
		ctx,
		eventID,
		storeScope,
		scopeSessionID,
		revision,
	)
	if err != nil {
		return PublicArtifact{}, false, err
	}
	if !exists {
		return PublicArtifact{}, false, nil
	}
	return publicArtifact(found), true, nil
}

func publicArtifact(found store.ResultsPublication) PublicArtifact {
	return PublicArtifact{
		Revision: found.Revision,
		HTML:     found.RenderedHTML,
		Text:     found.RenderedText,
		JSON:     found.RenderedJSON,
	}
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
	result := draft(found)
	if found.VotingTallyID > 0 {
		tally, loadErr := service.storage.LoadVotingTally(
			actor.Context(ctx), eventID, sessionID,
		)
		if loadErr != nil {
			return Draft{}, loadErr
		}
		value := votingTally(tally)
		result.VotingTally = &value
	}
	return result, nil
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
			current, loadErr := transaction.LoadCompetitionResultsDraft(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[Draft]{}, loadErr
			}
			params := saveParams(input, actor.ID, identity.Now)
			params.Awards = current.Awards
			override := false
			if current.VotingTallyID > 0 {
				tally, tallyErr := transaction.LoadVotingTally(
					actor.Context(ctx), input.EventID, input.SessionID,
				)
				if tallyErr != nil {
					return command.Execution[Draft]{}, tallyErr
				}
				_, eligible, stateErr := transaction.LoadCompetitionResultsReviewState(
					actor.Context(ctx), input.EventID, input.SessionID,
				)
				if stateErr != nil {
					return command.Execution[Draft]{}, stateErr
				}
				override = !followsVotingTally(input.Standings, tally, eligible)
				input.TallyOverrideReason = strings.TrimSpace(input.TallyOverrideReason)
				if override && input.TallyOverrideReason == "" {
					return command.Execution[Draft]{}, ErrCrewReasonRequired
				}
				params.VotingTallyID = current.VotingTallyID
				params.TallyOverrideReason = input.TallyOverrideReason
			}
			stored, saveErr := transaction.SaveCompetitionResultsDraft(
				actor.Context(ctx), params,
			)
			if saveErr != nil {
				return command.Execution[Draft]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Draft]{}, errors.New("encode Results Draft outcome")
			}
			execution := command.Success(draft(stored), string(outcome))
			if override {
				execution = execution.WithAudit(store.AuditDetails{
					Reason: input.TallyOverrideReason,
				})
			}
			return execution, nil
		},
	})
}

// SaveCompetitionAwards appends one Results revision without changing Placement or Score.
func (service *Service) SaveCompetitionAwards(
	ctx context.Context,
	actor auth.Account,
	input SaveCompetitionAwardsInput,
) (Draft, error) {
	if err := validateSaveCompetitionAwardsInput(input); err != nil {
		return Draft{}, err
	}
	if !actor.HasCapability(input.EventID, viewer.ManageResults) {
		return Draft{}, ErrManageRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Draft{}, errors.New("encode Competition Awards command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "SaveCompetitionAwards",
		TargetType:     "Competition",
		TargetID:       strconv.Itoa(input.SessionID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Draft]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Draft, error) {
			var stored store.CompetitionResultsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Draft{}, err
			}
			return draft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Draft], error) {
			current, loadErr := transaction.LoadCompetitionResultsDraft(
				actor.Context(ctx), input.EventID, input.SessionID,
			)
			if loadErr != nil {
				return command.Execution[Draft]{}, loadErr
			}
			if competitionAwardPromotionChanged(current.Awards, input.Awards) &&
				!actor.CanProduceEvent(input.EventID) {
				return command.Execution[Draft]{}, ErrProducerRequired
			}
			params := cloneCompetitionResultsParams(
				current, actor.ID, identity.Now, competitionAwardInputs(input.Awards),
			)
			params.ExpectedRevision = input.ExpectedRevision
			stored, saveErr := transaction.SaveCompetitionResultsDraft(
				actor.Context(ctx), params,
			)
			if saveErr != nil {
				return command.Execution[Draft]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Draft]{}, errors.New("encode Competition Awards outcome")
			}
			return command.Success(draft(stored), string(outcome)), nil
		},
	})
}

// DesignatePrizegiving records one Producer-selected Ceremony release path.
func (service *Service) DesignatePrizegiving(
	ctx context.Context,
	actor auth.Account,
	input DesignatePrizegivingInput,
) (Prizegiving, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Prizegiving{}, err
	}
	if input.EventID <= 0 || input.CeremonySessionID <= 0 {
		return Prizegiving{}, ErrInvalidInput
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Prizegiving{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Prizegiving{}, errors.New("encode Prizegiving designation command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "DesignatePrizegiving",
		TargetType:     "Session",
		TargetID:       strconv.Itoa(input.CeremonySessionID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Prizegiving]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Prizegiving, error) {
			var stored store.Prizegiving
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Prizegiving{}, err
			}
			return prizegiving(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Prizegiving], error) {
			stored, designateErr := transaction.DesignatePrizegiving(
				actor.Context(ctx),
				store.DesignatePrizegivingParams{
					EventID: input.EventID, CeremonySessionID: input.CeremonySessionID,
					CreatedByAccountID: actor.ID, Now: identity.Now,
				},
			)
			if designateErr != nil {
				return command.Execution[Prizegiving]{}, designateErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Prizegiving]{}, errors.New("encode Prizegiving designation outcome")
			}
			return command.Success(prizegiving(stored), string(outcome)), nil
		},
	})
}

// GetEventAwards returns the current Event Awards Draft to authorized crew.
func (service *Service) GetEventAwards(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (EventAwardsDraft, error) {
	if eventID <= 0 {
		return EventAwardsDraft{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return EventAwardsDraft{}, ErrViewRequired
	}
	found, err := service.storage.LoadEventAwardsDraft(actor.Context(ctx), eventID)
	if err != nil {
		return EventAwardsDraft{}, err
	}
	return eventAwardsDraft(found), nil
}

// SaveEventAwards appends one complete Event Awards snapshot.
func (service *Service) SaveEventAwards(
	ctx context.Context,
	actor auth.Account,
	input SaveEventAwardsInput,
) (EventAwardsDraft, error) {
	if err := validateSaveEventAwardsInput(input); err != nil {
		return EventAwardsDraft{}, err
	}
	if !actor.HasCapability(input.EventID, viewer.ManageResults) {
		return EventAwardsDraft{}, ErrManageRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EventAwardsDraft{}, errors.New("encode Event Awards command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "SaveEventAwardsDraft",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EventAwardsDraft]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (EventAwardsDraft, error) {
			var stored store.EventAwardsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return EventAwardsDraft{}, err
			}
			return eventAwardsDraft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EventAwardsDraft], error) {
			stored, saveErr := transaction.SaveEventAwardsDraft(
				actor.Context(ctx), store.SaveEventAwardsDraftParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					CreatedByAccountID: actor.ID, Now: identity.Now,
					Awards: eventAwardInputs(input.Awards),
				},
			)
			if saveErr != nil {
				return command.Execution[EventAwardsDraft]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[EventAwardsDraft]{}, errors.New("encode Event Awards outcome")
			}
			return command.Success(eventAwardsDraft(stored), string(outcome)), nil
		},
	})
}

// MarkEventAwardsReady records Producer review of one exact release path.
func (service *Service) MarkEventAwardsReady(
	ctx context.Context,
	actor auth.Account,
	input MarkEventAwardsReadyInput,
) (EventAwardsDraft, error) {
	if err := validateMarkEventAwardsReadyInput(input); err != nil {
		return EventAwardsDraft{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return EventAwardsDraft{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EventAwardsDraft{}, errors.New("encode Event Awards review command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "MarkEventAwardsReady",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EventAwardsDraft]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (EventAwardsDraft, error) {
			var stored store.EventAwardsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return EventAwardsDraft{}, err
			}
			return eventAwardsDraft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EventAwardsDraft], error) {
			stored, markErr := transaction.MarkEventAwardsReady(
				actor.Context(ctx), store.MarkEventAwardsReadyParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					ReleasePath:          awardReleasePathInput(input.ReleasePath),
					ExpectedPathRevision: input.ExpectedPathRevision,
					ReviewedByAccountID:  actor.ID, Now: identity.Now,
				},
			)
			if markErr != nil {
				return command.Execution[EventAwardsDraft]{}, markErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[EventAwardsDraft]{}, errors.New("encode Event Awards review outcome")
			}
			return command.Success(eventAwardsDraft(stored), string(outcome)), nil
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
		!boundedText(input.TallyOverrideReason, 1000) ||
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

func validateSaveCompetitionAwardsInput(input SaveCompetitionAwardsInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 {
		return ErrInvalidInput
	}
	return ValidateAwards(input.Awards)
}

func validateSaveEventAwardsInput(input SaveEventAwardsInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.ExpectedRevision < 0 || len(input.Awards) > 1000 {
		return ErrInvalidInput
	}
	return ValidateEventAwards(input.Awards)
}

func validateMarkEventAwardsReadyInput(input MarkEventAwardsReadyInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.ExpectedRevision <= 0 ||
		input.ExpectedPathRevision <= 0 || !validAwardPath(input.ReleasePath) {
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
		TallyOverrideReason: input.TallyOverrideReason,
		PublicExplanation:   input.PublicExplanation,
		ScoreType:           string(input.Score.Type), ScoreVisibility: string(input.Score.Visibility),
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
		VotingTallyID: stored.VotingTallyID, TallyOverrideReason: stored.TallyOverrideReason,
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
		Awards:    awards(stored.Awards),
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

func votingTally(stored store.VotingTally) VotingTally {
	result := VotingTally{
		ID: stored.ID, Participating: stored.Participating,
		Method: stored.Method, SelfVotePolicy: stored.SelfVotePolicy,
		CreatedAt: stored.CreatedAt,
		Entries:   make([]VotingTallyEntry, 0, len(stored.Entries)),
	}
	for _, entry := range stored.Entries {
		result.Entries = append(result.Entries, VotingTallyEntry{
			EntryID: entry.EntryID, Total: entry.Total, Count: entry.Count,
		})
	}
	return result
}

func followsVotingTally(
	standings []Standing,
	tally store.VotingTally,
	eligible []store.CompetitionResultsEligibleEntry,
) bool {
	eligibleIDs := make([]int, 0, len(eligible))
	for _, entry := range eligible {
		eligibleIDs = append(eligibleIDs, entry.ID)
	}
	expected := votingvalue.Placements(tally.Entries, eligibleIDs)
	ordered := append([]Standing(nil), standings...)
	sort.Slice(ordered, func(first, second int) bool {
		return ordered[first].DisplayOrder < ordered[second].DisplayOrder
	})
	if len(ordered) != len(expected) {
		return false
	}
	for index := range expected {
		if ordered[index].EntryID != expected[index].EntryID ||
			ordered[index].Standing != Placed ||
			ordered[index].Placement != expected[index].Placement ||
			ordered[index].DisplayOrder != expected[index].DisplayOrder {
			return false
		}
	}
	return true
}

func cloneCompetitionResultsParams(
	current store.CompetitionResultsDraft,
	actorID int,
	now time.Time,
	awards []store.CompetitionAwardInput,
) store.SaveCompetitionResultsDraftParams {
	standings := make([]store.CompetitionResultStandingInput, 0, len(current.Standings))
	for _, standing := range current.Standings {
		standings = append(standings, store.CompetitionResultStandingInput(standing))
	}
	return store.SaveCompetitionResultsDraftParams{
		EventID: current.EventID, SessionID: current.SessionID,
		ExpectedRevision: current.Revision,
		Disposition:      current.Disposition, NoPublicCrewReason: current.NoPublicCrewReason,
		PublicExplanation: current.PublicExplanation,
		ScoreType:         current.ScoreType, ScoreVisibility: current.ScoreVisibility,
		ScoreUnit: current.ScoreUnit, ScorePrecision: current.ScorePrecision,
		ScoreRequirement:    current.ScoreRequirement,
		ScoreInterpretation: current.ScoreInterpretation,
		VotingTallyID:       current.VotingTallyID,
		TallyOverrideReason: current.TallyOverrideReason,
		CreatedByAccountID:  actorID, Now: now, Standings: standings, Awards: awards,
	}
}

func competitionAwardInputs(values []Award) []store.CompetitionAwardInput {
	awards := make([]store.CompetitionAwardInput, 0, len(values))
	for _, value := range values {
		awards = append(awards, store.CompetitionAwardInput{
			Key: value.Key, Name: value.Name, Promoted: value.Promoted,
			DisplayOrder: value.DisplayOrder,
			Recipients:   awardRecipientInputs(value.Recipients),
		})
	}
	return awards
}

func awards(values []store.CompetitionAward) []Award {
	result := make([]Award, 0, len(values))
	for _, value := range values {
		result = append(result, Award{
			Key: value.Key, Name: value.Name, Promoted: value.Promoted,
			DisplayOrder: value.DisplayOrder,
			Recipients:   awardRecipients(value.Recipients),
		})
	}
	return result
}

func competitionAwardPromotionChanged(
	current []store.CompetitionAward,
	next []Award,
) bool {
	currentPromotions := make([]awardvalue.Promotion, 0, len(current))
	for _, award := range current {
		currentPromotions = append(currentPromotions, awardvalue.Promotion{
			Key: award.Key, Promoted: award.Promoted,
		})
	}
	nextPromotions := make([]awardvalue.Promotion, 0, len(next))
	for _, award := range next {
		nextPromotions = append(nextPromotions, awardvalue.Promotion{
			Key: award.Key, Promoted: award.Promoted,
		})
	}
	return awardvalue.PromotionChanged(currentPromotions, nextPromotions)
}

func eventAwardInputs(values []EventAward) []store.EventAwardInput {
	awards := make([]store.EventAwardInput, 0, len(values))
	for _, value := range values {
		awards = append(awards, store.EventAwardInput{
			Key: value.Key, Name: value.Name, DisplayOrder: value.DisplayOrder,
			Recipients:  awardRecipientInputs(value.Recipients),
			ReleasePath: awardReleasePathInput(value.ReleasePath),
		})
	}
	return awards
}

func awardRecipientInputs(values []AwardRecipient) []store.AwardRecipientInput {
	recipients := make([]store.AwardRecipientInput, 0, len(values))
	for _, value := range values {
		recipients = append(recipients, store.AwardRecipientInput{
			EntryID: value.EntryID, DisplayName: value.DisplayName,
		})
	}
	return recipients
}

func awardRecipients(values []store.AwardRecipientInput) []AwardRecipient {
	recipients := make([]AwardRecipient, 0, len(values))
	for _, value := range values {
		recipients = append(recipients, AwardRecipient{
			EntryID: value.EntryID, DisplayName: value.DisplayName,
		})
	}
	return recipients
}

func awardReleasePathInput(value AwardReleasePath) store.AwardReleasePath {
	return store.AwardReleasePath{
		Kind: string(value.Kind), PrizegivingSessionID: value.PrizegivingSessionID,
	}
}

func awardReleasePath(value store.AwardReleasePath) AwardReleasePath {
	return AwardReleasePath{
		Kind:                 AwardReleasePathKind(value.Kind),
		PrizegivingSessionID: value.PrizegivingSessionID,
	}
}

func eventAwardsDraft(stored store.EventAwardsDraft) EventAwardsDraft {
	result := EventAwardsDraft{
		ID: stored.ID, EventID: stored.EventID, Revision: stored.Revision,
		CreatedByAccountID: stored.CreatedByAccountID, CreatedAt: stored.CreatedAt,
		Awards:     make([]EventAward, 0, len(stored.Awards)),
		PathStates: make([]EventAwardPathState, 0, len(stored.PathStates)),
	}
	for _, award := range stored.Awards {
		result.Awards = append(result.Awards, EventAward{
			Award: Award{
				Key: award.Key, Name: award.Name, DisplayOrder: award.DisplayOrder,
				Recipients: awardRecipients(award.Recipients),
			},
			ReleasePath: awardReleasePath(award.ReleasePath),
		})
	}
	for _, state := range stored.PathStates {
		result.PathStates = append(result.PathStates, EventAwardPathState{
			ReleasePath: awardReleasePath(state.ReleasePath), Revision: state.Revision,
			Ready: state.Ready, ReadyByAccountID: state.ReadyByAccountID,
			ReadyAt: state.ReadyAt,
		})
	}
	return result
}

func prizegiving(stored store.Prizegiving) Prizegiving {
	return Prizegiving{
		ID: stored.ID, EventID: stored.EventID,
		CeremonySessionID:  stored.CeremonySessionID,
		CreatedByAccountID: stored.CreatedByAccountID, CreatedAt: stored.CreatedAt,
	}
}

// resultsRejections is the single source for Results rejection codes in both
// directions, so a replayed receipt restores the same failure the original
// command produced.
var resultsRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrRevisionConflict, Code: "results_revision"},
		{Err: ErrEventAwardsRevision, Code: "event_awards_revision"},
		{Err: store.ErrResultsPublicationRevision, Code: "publication_revision"},
		{Err: ErrCorrectionRevision, Code: "correction_revision"},
		{Err: ErrResultsPublicationRequired, Code: "publication_required"},
		{Err: ErrPrizegivingPreflightRequired, Code: "prizegiving_preflight_required"},
		{Err: store.ErrResultsPublicationTransition, Code: "publication_transition"},
		{Err: ErrCorrectionTransition, Code: "correction_transition"},
		{Err: ErrResultItemTransition, Code: "result_item_transition"},
		{Err: ErrResultsReleasePolicy, Code: "invalid_release_policy"},
		{Err: ErrCorrectionBase, Code: "correction_base_changed"},
		{Err: ErrResultsCorrection, Code: "invalid_correction"},
		{Err: ErrCompetitionPrizegivingAssignment, Code: "competition_prizegiving_assignment"},
		{Err: ErrCompetitionNotFound, Code: "competition_not_found"},
		{Err: ErrEventNotFound, Code: "event_not_found"},
		{Err: ErrPrizegivingSession, Code: "prizegiving_session_not_found"},
	},
}

func auditResultsRejections[T any](
	apply func(*store.CommandTx) (command.Execution[T], error),
) func(*store.CommandTx) (command.Execution[T], error) {
	return command.RejectKnown(resultsRejections, apply)
}

func decodeResultsCommandReceipt[T any](outcome string) (T, error) {
	return command.ReplayKnown[T](resultsRejections, outcome)
}

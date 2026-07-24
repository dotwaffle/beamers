// Package competition owns Competition Entry submission and disposition.
package competition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrProducerRequired means a Competition command lacked Producer authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrOperatorRequired means a live Competition command lacked Operator authority.
	ErrOperatorRequired = errors.New("operator authority required")
	// ErrCompetitionNotFound means no published Competition matched the stable IDs.
	ErrCompetitionNotFound = store.ErrCompetitionNotFound
	// ErrSubmissionClosed means the fixed Deadline has arrived.
	ErrSubmissionClosed = store.ErrCompetitionSubmissionClosed
	// ErrEntryNotFound means no retained Entry matched the stable IDs.
	ErrEntryNotFound = store.ErrCompetitionEntryNotFound
	// ErrEntryRevisionConflict means an Entry command used a stale revision.
	ErrEntryRevisionConflict = store.ErrCompetitionEntryRevision
	// ErrReadinessRevisionConflict means policy configuration used stale state.
	ErrReadinessRevisionConflict = store.ErrCompetitionReadinessRevision
	// ErrAttachmentReadinessRevisionConflict means Attachment readiness used stale state.
	ErrAttachmentReadinessRevisionConflict = store.ErrAttachmentReadinessRevision
	// ErrEntryOrderRevisionConflict means an order command used stale state.
	ErrEntryOrderRevisionConflict = store.ErrEntryOrderRevision
	// ErrEntryOrderLocked means live presentation froze the sequence.
	ErrEntryOrderLocked = store.ErrEntryOrderLocked
	// ErrEntryOrderPreviewStale means ordering inputs changed after preview.
	ErrEntryOrderPreviewStale = store.ErrEntryOrderPreviewStale
	// ErrPresentedEntryDisposition means an Entry already began presentation.
	ErrPresentedEntryDisposition = store.ErrPresentedEntryDisposition
	// ErrEntryOrderInvalid means policy, seed, or manual sequence is invalid.
	ErrEntryOrderInvalid = store.ErrEntryOrderInvalid
	// ErrLiveDispositionConfirmation means a live change lacked explicit confirmation.
	ErrLiveDispositionConfirmation = store.ErrLiveDispositionConfirmation
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrInvalidInput means a Competition request contains unsafe values.
	ErrInvalidInput = errors.New("invalid Competition input")
	// ErrEntryResolution means a final Entry resolution is invalid.
	ErrEntryResolution = store.ErrCompetitionResolution
	// ErrCrewReasonRequired means an exception omitted its durable Crew Reason.
	ErrCrewReasonRequired = store.ErrCompetitionCrewReason
)

// Disposition controls whether an Entry participates.
type Disposition string

const (
	// DispositionPending keeps an Entry crew-only and nonparticipating.
	DispositionPending Disposition = "Pending"
	// DispositionIncluded makes an Entry attendee-visible and participating.
	DispositionIncluded Disposition = "Included"
	// DispositionRejected retains an Entry without participation.
	DispositionRejected Disposition = "Rejected"
)

// Entry is one retained Competition submission.
type Entry struct {
	ID                            int
	CompetitionSessionID          int
	Name                          string
	PublicDetails                 string
	CrewNotes                     string
	Disposition                   Disposition
	Revision                      int
	ContentRevision               int
	ReviewCurrent                 bool
	PresentationStatus            string
	DeferredSequence              int
	ResolutionRequired            bool
	ResultDisposition             string
	TechnicalFailureReason        string
	ResolutionCrewReason          string
	PublicDisqualificationMessage string
	ReleaseHold                   bool
	FirstPresentedAt              time.Time
	CreatedAt                     time.Time
}

// State is current fixed Competition configuration and retained Entries.
type State struct {
	EventID                     int
	SessionID                   int
	SubmissionDeadline          time.Time
	EffectiveDefaultDisposition Disposition
	RequireEntryReview          bool
	FileDeliveryRequired        bool
	ReadinessRevision           int
	EntryOrder                  EntryOrder
	Entries                     []Entry
	ResultsReady                bool
	ReleaseReady                bool
}

// EntryOrderPolicy selects the canonical Included Entry sequence.
type EntryOrderPolicy string

const (
	// EntryOrderSubmission preserves Entry creation order.
	EntryOrderSubmission EntryOrderPolicy = "SubmissionOrder"
	// EntryOrderManual uses the crew-selected Entry sequence.
	EntryOrderManual EntryOrderPolicy = "ManualOrder"
	// EntryOrderDeterministicShuffle derives a reproducible seeded sequence.
	EntryOrderDeterministicShuffle EntryOrderPolicy = "DeterministicShuffle"
)

// EntryOrder is the current canonical or locked Included Entry sequence.
type EntryOrder struct {
	Policy   EntryOrderPolicy
	Seed     int64
	Revision int
	EntryIDs []int
	Locked   bool
}

// EntryOrderPreview binds a visible order to current durable state.
type EntryOrderPreview struct {
	EntryOrder
	Fingerprint string
}

// ConfigureEntryOrderInput changes the pre-live order policy.
type ConfigureEntryOrderInput struct {
	EventID          int              `json:"event_id"`
	SessionID        int              `json:"session_id"`
	CommandID        string           `json:"command_id"`
	ExpectedRevision int              `json:"expected_revision"`
	Policy           EntryOrderPolicy `json:"policy"`
	Seed             int64            `json:"seed"`
	ManualEntryIDs   []int            `json:"manual_entry_ids,omitempty"`
}

// PreflightFinding is one stable actionable Competition Start blocker.
type PreflightFinding struct {
	Code, Message string
	EntryID       int
}

// Preflight is the exact current Competition Start readiness result.
type Preflight struct {
	EventID, SessionID   int
	RequireEntryReview   bool
	FileDeliveryRequired bool
	Blockers             []PreflightFinding
	Attachments          []AttachmentReadiness
}

// CreateEntryInput contains one proposed Competition Entry.
type CreateEntryInput struct {
	EventID, SessionID  int
	CommandID           string
	Name, PublicDetails string
	CrewNotes           string
}

// UpdateEntryInput contains one optimistic Entry content change.
type UpdateEntryInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedRevision            int
	Name, PublicDetails         string
	CrewNotes                   string
}

// ChangeDispositionInput contains one optimistic participation change.
type ChangeDispositionInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedRevision            int
	Disposition                 Disposition
	ConfirmedLiveOverride       bool
}

// ConfigureReadinessInput changes independent Competition Start policies.
type ConfigureReadinessInput struct {
	EventID                   int    `json:"event_id"`
	SessionID                 int    `json:"session_id"`
	CommandID                 string `json:"command_id"`
	ExpectedReadinessRevision int    `json:"expected_readiness_revision"`
	RequireEntryReview        bool   `json:"require_entry_review"`
	FileDeliveryRequired      bool   `json:"file_delivery_required"`
}

// Readiness is current Competition Start policy state.
type Readiness struct {
	RequireEntryReview   bool
	FileDeliveryRequired bool
	ReadinessRevision    int
}

// ReviewEntryInput confirms one exact Entry content revision.
type ReviewEntryInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedRevision            int
}

// SetEntryAttachmentReadinessInput changes independent Final and Primary facts.
type SetEntryAttachmentReadinessInput struct {
	EventID             int    `json:"event_id"`
	SessionID           int    `json:"session_id"`
	EntryID             int    `json:"entry_id"`
	AttachmentVersionID int    `json:"attachment_version_id"`
	CommandID           string `json:"command_id"`
	ExpectedRevision    int    `json:"expected_revision"`
	Final               bool   `json:"final"`
	Primary             bool   `json:"primary"`
}

// RecordTechnicalFailureInput records cause without deciding the Entry result.
type RecordTechnicalFailureInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedRevision            int
	Reason                      string
}

// ResolveEntryInput records one final result, visibility, and hold decision.
type ResolveEntryInput struct {
	EventID, SessionID, EntryID   int
	CommandID                     string
	ExpectedRevision              int
	ResultDisposition             string
	CrewReason                    string
	PublicDisqualificationMessage string
}

// SetEntryReleaseHoldInput applies or lifts a Producer hold independently.
type SetEntryReleaseHoldInput struct {
	EventID, SessionID, EntryID int
	CommandID                   string
	ExpectedRevision            int
	Hold                        bool
	CrewReason                  string
}

// EndPreflight is the warned deferred set bound to current revisions.
type EndPreflight struct {
	DeferredEntries      []Entry
	Fingerprint          string
	RequiresConfirmation bool
}

// AttachmentReadiness is current Final and Primary state.
type AttachmentReadiness struct {
	AttachmentVersionID int
	EntryID             int
	AttachmentVersion   int
	LogicalName         string
	OriginalFilename    string
	ReadinessRevision   int
	Final, Primary      bool
}

// Service owns Competition queries and Entry commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates a Competition Service with explicit dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("competition storage is required")
	}
	if now == nil {
		return nil, errors.New("competition clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Get returns one Competition to granted Event crew.
func (service *Service) Get(ctx context.Context, actor auth.Account, eventID, sessionID int) (State, error) {
	if eventID <= 0 || sessionID <= 0 {
		return State{}, ErrInvalidInput
	}
	if !actor.Administrator && actor.EventRoles[eventID] == "" {
		return State{}, ErrProducerRequired
	}
	stored, err := service.storage.LoadCompetition(actor.Context(ctx), eventID, sessionID)
	if err != nil {
		return State{}, err
	}
	return state(stored), nil
}

// PreflightStart reports blockers from the configured readiness rules.
func (service *Service) PreflightStart(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Preflight, error) {
	if eventID <= 0 || sessionID <= 0 {
		return Preflight{}, ErrInvalidInput
	}
	if !actor.Administrator && actor.EventRoles[eventID] == "" {
		return Preflight{}, ErrProducerRequired
	}
	stored, err := service.storage.PreflightCompetitionStart(actor.Context(ctx), eventID, sessionID)
	if err != nil {
		return Preflight{}, err
	}
	result := Preflight{
		EventID: stored.EventID, SessionID: stored.SessionID,
		RequireEntryReview:   stored.RequireEntryReview,
		FileDeliveryRequired: stored.FileDeliveryRequired,
		Blockers:             make([]PreflightFinding, 0, len(stored.Blockers)),
		Attachments:          make([]AttachmentReadiness, 0, len(stored.Attachments)),
	}
	for _, blocker := range stored.Blockers {
		result.Blockers = append(result.Blockers, PreflightFinding{
			Code: string(blocker.Code), Message: blocker.Message, EntryID: blocker.EntryID,
		})
	}
	for _, attachment := range stored.Attachments {
		result.Attachments = append(result.Attachments, attachmentReadiness(attachment))
	}
	return result, nil
}

// PreflightEnd returns the exact deferred Entries requiring warned confirmation.
func (service *Service) PreflightEnd(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (EndPreflight, error) {
	if eventID <= 0 || sessionID <= 0 {
		return EndPreflight{}, ErrInvalidInput
	}
	if !actor.CanOperateEvent(eventID) {
		return EndPreflight{}, ErrOperatorRequired
	}
	stored, err := service.storage.PreflightCompetitionEnd(actor.Context(ctx), eventID, sessionID)
	if err != nil {
		return EndPreflight{}, err
	}
	result := EndPreflight{
		DeferredEntries:      make([]Entry, 0, len(stored.DeferredEntries)),
		Fingerprint:          stored.Fingerprint,
		RequiresConfirmation: stored.RequiresConfirmation,
	}
	for _, deferred := range stored.DeferredEntries {
		result.DeferredEntries = append(result.DeferredEntries, entry(deferred))
	}
	return result, nil
}

// PreviewEntryOrder returns the reproducible current Included Entry sequence.
func (service *Service) PreviewEntryOrder(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (EntryOrderPreview, error) {
	if eventID <= 0 || sessionID <= 0 {
		return EntryOrderPreview{}, ErrInvalidInput
	}
	if !actor.Administrator && actor.EventRoles[eventID] == "" {
		return EntryOrderPreview{}, ErrProducerRequired
	}
	stored, fingerprint, err := service.storage.LoadCompetitionEntryOrder(
		actor.Context(ctx), eventID, sessionID,
	)
	if err != nil {
		return EntryOrderPreview{}, err
	}
	return EntryOrderPreview{
		EntryOrder:  entryOrder(stored),
		Fingerprint: fingerprint,
	}, nil
}

// ConfigureEntryOrder changes the pre-live order policy.
func (service *Service) ConfigureEntryOrder(
	ctx context.Context,
	actor auth.Account,
	input ConfigureEntryOrderInput,
) (EntryOrder, error) {
	if input.ExpectedRevision < 0 || !validEntryOrderPolicy(input.Policy) {
		return EntryOrder{}, ErrInvalidInput
	}
	return service.executeEntryOrderCommand(
		ctx, actor, input.EventID, input.SessionID, input.CommandID,
		"ConfigureCompetitionEntryOrder", input,
		func(transaction *store.CommandTx, now time.Time) (store.EntryOrderState, error) {
			return transaction.ConfigureCompetitionEntryOrder(
				actor.Context(ctx), store.ConfigureEntryOrderParams{
					EventID: input.EventID, SessionID: input.SessionID,
					ExpectedRevision: input.ExpectedRevision,
					Policy:           store.EntryOrderPolicy(input.Policy), Seed: input.Seed,
					ManualEntryIDs: slices.Clone(input.ManualEntryIDs),
					Now:            now,
				},
			)
		},
	)
}

func (service *Service) executeEntryOrderCommand(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
	commandID, action string,
	payload any,
	apply func(*store.CommandTx, time.Time) (store.EntryOrderState, error),
) (EntryOrder, error) {
	if err := command.ValidateID(commandID); err != nil {
		return EntryOrder{}, err
	}
	if eventID <= 0 || sessionID <= 0 {
		return EntryOrder{}, ErrInvalidInput
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return EntryOrder{}, errors.New("encode Competition Entry Order command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(encodedPayload)),
		Action:      action, TargetType: "Competition", TargetID: strconv.Itoa(sessionID),
		Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EntryOrder]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (EntryOrder, error) {
			var stored store.EntryOrderState
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return EntryOrder{}, err
			}
			return entryOrder(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EntryOrder], error) {
			if !actor.CanProduceEvent(eventID) {
				return command.Execution[EntryOrder]{}, ErrProducerRequired
			}
			stored, applyErr := apply(transaction, identity.Now)
			if applyErr != nil {
				return command.Execution[EntryOrder]{}, applyErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[EntryOrder]{}, errors.New("encode Competition Entry Order outcome")
			}
			return command.Success(entryOrder(stored), string(outcome)), nil
		},
	})
}

func validEntryOrderPolicy(policy EntryOrderPolicy) bool {
	return policy == EntryOrderSubmission ||
		policy == EntryOrderManual ||
		policy == EntryOrderDeterministicShuffle
}

// ConfigureReadiness changes independent Competition Start policies.
func (service *Service) ConfigureReadiness(
	ctx context.Context,
	actor auth.Account,
	input ConfigureReadinessInput,
) (Readiness, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Readiness{}, err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedReadinessRevision < 0 {
		return Readiness{}, ErrInvalidInput
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Readiness{}, errors.New("encode Competition readiness command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "ConfigureCompetitionReadiness", TargetType: "Competition",
		TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Readiness]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Readiness, error) {
			var stored store.CompetitionReadiness
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Readiness{}, err
			}
			return readiness(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Readiness], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Readiness]{}, ErrProducerRequired
			}
			stored, applyErr := transaction.ConfigureCompetitionReadiness(
				actor.Context(ctx), store.ConfigureCompetitionReadinessParams{
					EventID: input.EventID, SessionID: input.SessionID,
					ExpectedReadinessRevision: input.ExpectedReadinessRevision,
					RequireEntryReview:        input.RequireEntryReview,
					FileDeliveryRequired:      input.FileDeliveryRequired,
				},
			)
			if applyErr != nil {
				return command.Execution[Readiness]{}, applyErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Readiness]{}, errors.New("encode Competition readiness outcome")
			}
			return command.Success(readiness(stored), string(outcome)), nil
		},
	})
}

// CreateEntry creates one retained Entry before the fixed Deadline.
func (service *Service) CreateEntry(ctx context.Context, actor auth.Account, input CreateEntryInput) (Entry, error) {
	input.Name = strings.TrimSpace(input.Name)
	if err := validateEntryCommand(input.EventID, input.SessionID, 0, 0, input.CommandID, input.Name, input.PublicDetails, input.CrewNotes); err != nil {
		return Entry{}, err
	}
	return service.execute(ctx, actor, input.EventID, input.CommandID, "CreateCompetitionEntry", "unidentified", input,
		func(transaction *store.CommandTx, now time.Time) (store.CompetitionEntry, error) {
			return transaction.CreateCompetitionEntry(actor.Context(ctx), store.CreateCompetitionEntryParams{
				EventID: input.EventID, SessionID: input.SessionID, Name: input.Name,
				PublicDetails: input.PublicDetails, CrewNotes: input.CrewNotes, Now: now,
			})
		},
	)
}

// UpdateEntry changes retained Entry content before the fixed Deadline.
func (service *Service) UpdateEntry(ctx context.Context, actor auth.Account, input UpdateEntryInput) (Entry, error) {
	input.Name = strings.TrimSpace(input.Name)
	if err := validateEntryCommand(
		input.EventID, input.SessionID, input.EntryID, input.ExpectedRevision,
		input.CommandID, input.Name, input.PublicDetails, input.CrewNotes,
	); err != nil {
		return Entry{}, err
	}
	return service.execute(ctx, actor, input.EventID, input.CommandID, "UpdateCompetitionEntry", strconv.Itoa(input.EntryID), input,
		func(transaction *store.CommandTx, now time.Time) (store.CompetitionEntry, error) {
			return transaction.UpdateCompetitionEntry(actor.Context(ctx), store.UpdateCompetitionEntryParams{
				EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
				ExpectedRevision: input.ExpectedRevision, Name: input.Name,
				PublicDetails: input.PublicDetails, CrewNotes: input.CrewNotes, Now: now,
			})
		},
	)
}

// ChangeDisposition changes retained participation before the fixed Deadline.
func (service *Service) ChangeDisposition(
	ctx context.Context,
	actor auth.Account,
	input ChangeDispositionInput,
) (Entry, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Entry{}, err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.EntryID <= 0 || input.ExpectedRevision <= 0 {
		return Entry{}, ErrInvalidInput
	}
	if input.Disposition != DispositionPending && input.Disposition != DispositionIncluded &&
		input.Disposition != DispositionRejected {
		return Entry{}, fmt.Errorf("%w: disposition must be Pending, Included, or Rejected", ErrInvalidInput)
	}
	return service.execute(ctx, actor, input.EventID, input.CommandID, "ChangeCompetitionEntryDisposition", strconv.Itoa(input.EntryID), input,
		func(transaction *store.CommandTx, now time.Time) (store.CompetitionEntry, error) {
			return transaction.ChangeCompetitionEntryDisposition(actor.Context(ctx), store.ChangeCompetitionEntryDispositionParams{
				EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
				ExpectedRevision: input.ExpectedRevision, Disposition: string(input.Disposition),
				ConfirmedLive: input.ConfirmedLiveOverride, Now: now,
			})
		},
	)
}

// ReviewEntry confirms one exact current Entry content revision.
func (service *Service) ReviewEntry(
	ctx context.Context,
	actor auth.Account,
	input ReviewEntryInput,
) (Entry, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Entry{}, err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.EntryID <= 0 ||
		input.ExpectedRevision <= 0 {
		return Entry{}, ErrInvalidInput
	}
	return service.execute(
		ctx, actor, input.EventID, input.CommandID, "ReviewCompetitionEntry",
		strconv.Itoa(input.EntryID), input,
		func(transaction *store.CommandTx, now time.Time) (store.CompetitionEntry, error) {
			return transaction.ReviewCompetitionEntry(
				actor.Context(ctx), store.ReviewCompetitionEntryParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					ExpectedRevision: input.ExpectedRevision, ReviewerAccountID: actor.ID, Now: now,
				},
			)
		},
	)
}

// RecordTechnicalFailure records a reason without deciding judging or release.
func (service *Service) RecordTechnicalFailure(
	ctx context.Context,
	actor auth.Account,
	input RecordTechnicalFailureInput,
) (Entry, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if err := validateExceptionCommand(
		input.EventID,
		input.SessionID,
		input.EntryID,
		input.ExpectedRevision,
		input.CommandID,
	); err != nil {
		return Entry{}, err
	}
	if input.Reason == "" || !boundedText(input.Reason, 10000) {
		return Entry{}, ErrCrewReasonRequired
	}
	return service.executeOperator(
		ctx,
		actor,
		input.EventID,
		input.CommandID,
		"RecordCompetitionTechnicalFailure",
		strconv.Itoa(input.EntryID),
		input,
		func(transaction *store.CommandTx, _ time.Time) (store.CompetitionEntry, error) {
			return transaction.RecordCompetitionTechnicalFailure(
				actor.Context(ctx),
				store.TechnicalFailureParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					ExpectedRevision: input.ExpectedRevision, Reason: input.Reason,
				},
			)
		},
	)
}

// ResolveEntry records a Producer's final result, visibility, and hold decision.
func (service *Service) ResolveEntry(
	ctx context.Context,
	actor auth.Account,
	input ResolveEntryInput,
) (Entry, error) {
	input.CrewReason = strings.TrimSpace(input.CrewReason)
	input.PublicDisqualificationMessage = strings.TrimSpace(input.PublicDisqualificationMessage)
	if err := validateExceptionCommand(
		input.EventID,
		input.SessionID,
		input.EntryID,
		input.ExpectedRevision,
		input.CommandID,
	); err != nil {
		return Entry{}, err
	}
	if input.CrewReason == "" ||
		!boundedText(input.CrewReason, 10000) ||
		!boundedText(input.PublicDisqualificationMessage, 10000) {
		return Entry{}, ErrCrewReasonRequired
	}
	return service.execute(
		ctx,
		actor,
		input.EventID,
		input.CommandID,
		"ResolveCompetitionEntry",
		strconv.Itoa(input.EntryID),
		input,
		func(transaction *store.CommandTx, now time.Time) (store.CompetitionEntry, error) {
			return transaction.ResolveCompetitionEntry(
				actor.Context(ctx),
				store.ResolveCompetitionEntryParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					ExpectedRevision:              input.ExpectedRevision,
					ResultDisposition:             input.ResultDisposition,
					CrewReason:                    input.CrewReason,
					PublicDisqualificationMessage: input.PublicDisqualificationMessage,
					Now:                           now,
				},
			)
		},
	)
}

// SetEntryReleaseHold changes only the reversible Attachment release gate.
func (service *Service) SetEntryReleaseHold(
	ctx context.Context,
	actor auth.Account,
	input SetEntryReleaseHoldInput,
) (Entry, error) {
	input.CrewReason = strings.TrimSpace(input.CrewReason)
	if err := validateExceptionCommand(
		input.EventID, input.SessionID, input.EntryID,
		input.ExpectedRevision, input.CommandID,
	); err != nil {
		return Entry{}, err
	}
	if input.CrewReason == "" || !boundedText(input.CrewReason, 10000) {
		return Entry{}, ErrCrewReasonRequired
	}
	return service.execute(
		ctx, actor, input.EventID, input.CommandID, "SetCompetitionEntryReleaseHold",
		strconv.Itoa(input.EntryID), input,
		func(transaction *store.CommandTx, _ time.Time) (store.CompetitionEntry, error) {
			return transaction.SetCompetitionEntryReleaseHold(
				actor.Context(ctx), store.SetCompetitionEntryReleaseHoldParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					ExpectedRevision: input.ExpectedRevision, Hold: input.Hold,
					CrewReason: input.CrewReason,
				},
			)
		},
	)
}

// SetEntryAttachmentReadiness changes Final and Primary independently.
func (service *Service) SetEntryAttachmentReadiness(
	ctx context.Context,
	actor auth.Account,
	input SetEntryAttachmentReadinessInput,
) (AttachmentReadiness, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return AttachmentReadiness{}, err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.EntryID <= 0 ||
		input.AttachmentVersionID <= 0 || input.ExpectedRevision <= 0 {
		return AttachmentReadiness{}, ErrInvalidInput
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return AttachmentReadiness{}, errors.New("encode Attachment readiness command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "SetEntryAttachmentReadiness", TargetType: "AttachmentVersion",
		TargetID: strconv.Itoa(input.AttachmentVersionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[AttachmentReadiness]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (AttachmentReadiness, error) {
			var stored store.AttachmentReadiness
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return AttachmentReadiness{}, err
			}
			return attachmentReadiness(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[AttachmentReadiness], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[AttachmentReadiness]{}, ErrProducerRequired
			}
			stored, applyErr := transaction.SetEntryAttachmentReadiness(
				actor.Context(ctx), store.SetEntryAttachmentReadinessParams{
					EventID: input.EventID, SessionID: input.SessionID, EntryID: input.EntryID,
					AttachmentVersionID: input.AttachmentVersionID,
					ExpectedRevision:    input.ExpectedRevision,
					Final:               input.Final, Primary: input.Primary,
				},
			)
			if applyErr != nil {
				return command.Execution[AttachmentReadiness]{}, applyErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[AttachmentReadiness]{}, errors.New("encode Attachment readiness outcome")
			}
			return command.Success(attachmentReadiness(stored), string(outcome)), nil
		},
	})
}

func (service *Service) execute(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	commandID, action, targetID string,
	payload any,
	apply func(*store.CommandTx, time.Time) (store.CompetitionEntry, error),
) (Entry, error) {
	return service.executeEntryCommand(
		ctx, actor, eventID, commandID, action, targetID, payload,
		actor.CanProduceEvent, ErrProducerRequired, apply,
	)
}

func (service *Service) executeOperator(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	commandID, action, targetID string,
	payload any,
	apply func(*store.CommandTx, time.Time) (store.CompetitionEntry, error),
) (Entry, error) {
	return service.executeEntryCommand(
		ctx, actor, eventID, commandID, action, targetID, payload,
		actor.CanOperateEvent, ErrOperatorRequired, apply,
	)
}

func (service *Service) executeEntryCommand(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	commandID, action, targetID string,
	payload any,
	authorized func(int) bool,
	authorizationError error,
	apply func(*store.CommandTx, time.Time) (store.CompetitionEntry, error),
) (Entry, error) {
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return Entry{}, errors.New("encode Competition Entry command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID, PayloadHash: command.PayloadHash(string(encodedPayload)),
		Action: action, TargetType: "CompetitionEntry", TargetID: targetID, Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Entry]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Entry, error) {
			var stored store.CompetitionEntry
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return Entry{}, err
			}
			return entry(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Entry], error) {
			if !authorized(eventID) {
				return command.Execution[Entry]{}, authorizationError
			}
			stored, applyErr := apply(transaction, identity.Now)
			if applyErr != nil {
				return command.Execution[Entry]{}, applyErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[Entry]{}, errors.New("encode Competition Entry outcome")
			}
			return command.Success(entry(stored), string(outcome)).
				WithTargetID(strconv.Itoa(stored.ID)), nil
		},
	})
}

func validateExceptionCommand(
	eventID, sessionID, entryID, expectedRevision int,
	commandID string,
) error {
	if err := command.ValidateID(commandID); err != nil {
		return err
	}
	if eventID <= 0 || sessionID <= 0 || entryID <= 0 || expectedRevision <= 0 {
		return ErrInvalidInput
	}
	return nil
}

func validateEntryCommand(
	eventID, sessionID, entryID, expectedRevision int,
	commandID, name, publicDetails, crewNotes string,
) error {
	if err := command.ValidateID(commandID); err != nil {
		return err
	}
	if eventID <= 0 || sessionID <= 0 || entryID < 0 || expectedRevision < 0 ||
		(entryID == 0) != (expectedRevision == 0) {
		return ErrInvalidInput
	}
	if !visibleText(name, 200) {
		return fmt.Errorf("%w: name must be 1 to 200 visible characters", ErrInvalidInput)
	}
	if !boundedText(publicDetails, 10000) || !boundedText(crewNotes, 10000) {
		return fmt.Errorf("%w: Entry details must not exceed 10000 characters", ErrInvalidInput)
	}
	return nil
}

func visibleText(value string, maximum int) bool {
	if value == "" || !boundedText(value, maximum) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func boundedText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func state(stored store.CompetitionState) State {
	result := State{
		EventID: stored.EventID, SessionID: stored.SessionID,
		SubmissionDeadline:          stored.SubmissionDeadline,
		EffectiveDefaultDisposition: Disposition(stored.EffectiveDefaultDisposition),
		RequireEntryReview:          stored.RequireEntryReview,
		FileDeliveryRequired:        stored.FileDeliveryRequired,
		ReadinessRevision:           stored.ReadinessRevision,
		EntryOrder:                  entryOrder(stored.EntryOrder),
		ResultsReady:                stored.ResultsReady,
		ReleaseReady:                stored.ReleaseReady,
		Entries:                     make([]Entry, 0, len(stored.Entries)),
	}
	for _, storedEntry := range stored.Entries {
		result.Entries = append(result.Entries, entry(storedEntry))
	}
	return result
}

func entryOrder(stored store.EntryOrderState) EntryOrder {
	return EntryOrder{
		Policy: EntryOrderPolicy(stored.Policy), Seed: stored.Seed,
		Revision: stored.Revision, EntryIDs: slices.Clone(stored.EntryIDs), Locked: stored.Locked,
	}
}

func entry(stored store.CompetitionEntry) Entry {
	return Entry{
		ID: stored.ID, CompetitionSessionID: stored.CompetitionSessionID,
		Name: stored.Name, PublicDetails: stored.PublicDetails, CrewNotes: stored.CrewNotes,
		Disposition: Disposition(stored.Disposition), Revision: stored.Revision,
		ContentRevision: stored.ContentRevision, ReviewCurrent: stored.ReviewCurrent,
		PresentationStatus:            stored.PresentationStatus,
		DeferredSequence:              stored.DeferredSequence,
		ResolutionRequired:            stored.ResolutionRequired,
		ResultDisposition:             stored.ResultDisposition,
		TechnicalFailureReason:        stored.TechnicalFailureReason,
		ResolutionCrewReason:          stored.ResolutionCrewReason,
		PublicDisqualificationMessage: stored.PublicDisqualificationMessage,
		ReleaseHold:                   stored.ReleaseHold,
		FirstPresentedAt:              stored.FirstPresentedAt,
		CreatedAt:                     stored.CreatedAt,
	}
}

func readiness(stored store.CompetitionReadiness) Readiness {
	return Readiness{
		RequireEntryReview:   stored.RequireEntryReview,
		FileDeliveryRequired: stored.FileDeliveryRequired,
		ReadinessRevision:    stored.ReadinessRevision,
	}
}

func attachmentReadiness(stored store.AttachmentReadiness) AttachmentReadiness {
	return AttachmentReadiness{
		AttachmentVersionID: stored.AttachmentVersionID,
		EntryID:             stored.EntryID,
		AttachmentVersion:   stored.AttachmentVersion,
		LogicalName:         stored.LogicalName,
		OriginalFilename:    stored.OriginalFilename,
		ReadinessRevision:   stored.ReadinessRevision,
		Final:               stored.Final,
		Primary:             stored.Primary,
	}
}

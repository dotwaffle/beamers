// Package competition owns Competition Entry submission and disposition.
package competition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// ErrCompetitionNotFound means no published Competition matched the stable IDs.
	ErrCompetitionNotFound = store.ErrCompetitionNotFound
	// ErrSubmissionClosed means the fixed Deadline has arrived.
	ErrSubmissionClosed = store.ErrCompetitionSubmissionClosed
	// ErrEntryNotFound means no retained Entry matched the stable IDs.
	ErrEntryNotFound = store.ErrCompetitionEntryNotFound
	// ErrEntryRevisionConflict means an Entry command used a stale revision.
	ErrEntryRevisionConflict = store.ErrCompetitionEntryRevision
	// ErrLiveDispositionConfirmation means a live change lacked explicit confirmation.
	ErrLiveDispositionConfirmation = store.ErrLiveDispositionConfirmation
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrInvalidInput means a Competition request contains unsafe values.
	ErrInvalidInput = errors.New("invalid Competition input")
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
	ID                   int
	CompetitionSessionID int
	Name                 string
	PublicDetails        string
	CrewNotes            string
	Disposition          Disposition
	Revision             int
	CreatedAt            time.Time
}

// State is current fixed Competition configuration and retained Entries.
type State struct {
	EventID                     int
	SessionID                   int
	SubmissionDeadline          time.Time
	EffectiveDefaultDisposition Disposition
	Entries                     []Entry
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

func (service *Service) execute(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	commandID, action, targetID string,
	payload any,
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
			if !actor.CanProduceEvent(eventID) {
				return command.Execution[Entry]{}, ErrProducerRequired
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
		Entries:                     make([]Entry, 0, len(stored.Entries)),
	}
	for _, storedEntry := range stored.Entries {
		result.Entries = append(result.Entries, entry(storedEntry))
	}
	return result
}

func entry(stored store.CompetitionEntry) Entry {
	return Entry{
		ID: stored.ID, CompetitionSessionID: stored.CompetitionSessionID,
		Name: stored.Name, PublicDetails: stored.PublicDetails, CrewNotes: stored.CrewNotes,
		Disposition: Disposition(stored.Disposition), Revision: stored.Revision, CreatedAt: stored.CreatedAt,
	}
}

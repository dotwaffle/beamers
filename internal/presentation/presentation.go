// Package presentation owns assigned Presentation submissions.
package presentation

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrInvalidInput means Presentation submission input is malformed.
	ErrInvalidInput = errors.New("invalid Presentation submission input")
	// ErrInvalidSpeaker identifies an invalid user-editable Speaker credit.
	ErrInvalidSpeaker = errors.New("invalid Presentation Speaker credit")
	// ErrInvalidPublicDetails identifies invalid user-editable Presentation details.
	ErrInvalidPublicDetails = errors.New("invalid Presentation public details")
	// ErrProducerRequired means the actor cannot manage the Event.
	ErrProducerRequired = errors.New("producer role required")
	// ErrPresentationNotFound hides unknown and non-Presentation Sessions.
	ErrPresentationNotFound = store.ErrPresentationNotFound
	// ErrSubmitterRequired hides unknown and differently owned Presentations.
	ErrSubmitterRequired = store.ErrPresentationSubmitterRequired
	// ErrRevisionConflict means the caller used stale Presentation submission state.
	ErrRevisionConflict = store.ErrPresentationSubmissionRevision
	// ErrAccessClosed means the Presentation cutoff has arrived.
	ErrAccessClosed = store.ErrPresentationSubmissionClosed
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
)

// State is Producer-visible Presentation submission state.
type State struct {
	EventID, SessionID, SubmitterAccountID, Revision int
	SessionTitle, Speaker, PublicDetails             string
	UploadDeadline                                   time.Time
}

// Submission is one Account's narrow assigned Presentation projection.
type Submission struct {
	EventID, SessionID, Revision                   int
	EventName, SessionTitle, Timezone, EventLocale string
	Speaker, PublicDetails                         string
	UploadDeadline                                 time.Time
}

// Account is one enabled Account selectable by a Producer.
type Account = store.SubmissionAccount

// AssignSubmitterInput assigns or replaces one Submitter Account.
type AssignSubmitterInput struct {
	EventID, SessionID, AccountID int
	ExpectedRevision              int
	CommandID                     string
}

// UpdateSubmissionInput changes Account-maintained public content.
type UpdateSubmissionInput struct {
	EventID, SessionID int
	ExpectedRevision   int
	Speaker            string
	PublicDetails      string
	CommandID          string
}

// Service owns Presentation submission commands and projections.
type Service struct {
	storage        *store.SQLite
	now            func() time.Time
	notifyDisplays func()
	notifySchedule func()
}

// New creates a Presentation submission Service with explicit dependencies.
func New(
	storage *store.SQLite,
	now func() time.Time,
	notifyDisplays func(),
	notifySchedule func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("presentation storage is required")
	}
	if now == nil {
		return nil, errors.New("presentation clock is required")
	}
	return &Service{
		storage: storage, now: now,
		notifyDisplays: notifyDisplays, notifySchedule: notifySchedule,
	}, nil
}

// Get returns one Presentation to its Event Producer.
func (service *Service) Get(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (State, error) {
	if eventID <= 0 || sessionID <= 0 {
		return State{}, ErrInvalidInput
	}
	if !actor.CanProduceEvent(eventID) {
		return State{}, ErrProducerRequired
	}
	stored, err := service.storage.LoadPresentationSubmission(
		actor.Context(ctx),
		eventID,
		sessionID,
	)
	if err != nil {
		return State{}, err
	}
	return state(stored), nil
}

// Submissions returns only the signed-in Account's assigned Presentations.
func (service *Service) Submissions(
	ctx context.Context,
	actor auth.Account,
) ([]Submission, error) {
	if actor.ID <= 0 {
		return nil, ErrSubmitterRequired
	}
	stored, err := service.storage.LoadAccountPresentationSubmissions(
		actor.Context(ctx),
		actor.ID,
	)
	if err != nil {
		return nil, err
	}
	result := make([]Submission, 0, len(stored))
	for _, found := range stored {
		result = append(result, submission(found))
	}
	return result, nil
}

// AssignableAccounts returns enabled Accounts to an Event Producer.
func (service *Service) AssignableAccounts(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]Account, error) {
	if eventID <= 0 {
		return nil, ErrInvalidInput
	}
	if !actor.CanProduceEvent(eventID) {
		return nil, ErrProducerRequired
	}
	return service.storage.ListSubmissionAccounts(actor.Context(ctx))
}

// AssignSubmitter assigns or replaces one enabled Account.
func (service *Service) AssignSubmitter(
	ctx context.Context,
	actor auth.Account,
	input AssignSubmitterInput,
) (State, error) {
	if err := validateCommand(
		input.EventID,
		input.SessionID,
		input.ExpectedRevision,
		input.CommandID,
	); err != nil || input.AccountID <= 0 {
		if err != nil {
			return State{}, err
		}
		return State{}, ErrInvalidInput
	}
	return service.execute(
		ctx,
		actor,
		input.SessionID,
		input.CommandID,
		"AssignPresentationSubmitter",
		input,
		actor.CanProduceEvent(input.EventID),
		ErrProducerRequired,
		authz.Event(input.EventID),
		"Submitter Account #"+strconv.Itoa(input.AccountID),
		nil,
		func(transaction *store.CommandTx, _ time.Time) (store.PresentationSubmission, error) {
			return transaction.AssignPresentationSubmitter(
				actor.Context(ctx),
				store.AssignPresentationSubmitterParams{
					EventID: input.EventID, SessionID: input.SessionID,
					AccountID: input.AccountID, ExpectedRevision: input.ExpectedRevision,
				},
			)
		},
	)
}

// UpdateSubmission changes approved public content during current access.
func (service *Service) UpdateSubmission(
	ctx context.Context,
	actor auth.Account,
	input UpdateSubmissionInput,
) (State, error) {
	input.Speaker = strings.TrimSpace(input.Speaker)
	if err := validateCommand(
		input.EventID,
		input.SessionID,
		input.ExpectedRevision,
		input.CommandID,
	); err != nil {
		return State{}, err
	}
	if input.ExpectedRevision == 0 || actor.ID <= 0 {
		return State{}, ErrInvalidInput
	}
	var fieldErrors []error
	if !validSpeaker(input.Speaker) {
		fieldErrors = append(fieldErrors, ErrInvalidSpeaker)
	}
	if !boundedText(input.PublicDetails, 10000) {
		fieldErrors = append(fieldErrors, ErrInvalidPublicDetails)
	}
	if len(fieldErrors) > 0 {
		return State{}, errors.Join(append([]error{ErrInvalidInput}, fieldErrors...)...)
	}
	return service.execute(
		ctx,
		actor,
		input.SessionID,
		input.CommandID,
		"UpdatePresentationSubmission",
		input,
		true,
		nil,
		authz.Installation(),
		"",
		service.notifyPublicDetails,
		func(transaction *store.CommandTx, now time.Time) (store.PresentationSubmission, error) {
			return transaction.UpdatePresentationSubmission(
				actor.Context(ctx),
				store.UpdatePresentationSubmissionParams{
					EventID: input.EventID, SessionID: input.SessionID, AccountID: actor.ID,
					ExpectedRevision: input.ExpectedRevision,
					Speaker:          input.Speaker, PublicDetails: input.PublicDetails, Now: now,
				},
			)
		},
	)
}

func (service *Service) execute(
	ctx context.Context,
	actor auth.Account,
	sessionID int,
	commandID, action string,
	payload any,
	authorized bool,
	authorizationError error,
	facts authz.Facts,
	auditNote string,
	notify func(),
	apply func(*store.CommandTx, time.Time) (store.PresentationSubmission, error),
) (State, error) {
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return State{}, errors.New("encode Presentation submission command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(encodedPayload)),
		Action:      action, TargetType: "Presentation", TargetID: strconv.Itoa(sessionID),
		Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[State]{
		Storage:  service.storage,
		Identity: identity,
		Notify:   notify,
		Authorization: command.Authorization{
			Facts: facts, Refusals: presentationRejections,
		},
		Replay: func(outcome string) (State, error) {
			var stored store.PresentationSubmission
			if decodeErr := decodeReceipt(outcome, &stored); decodeErr != nil {
				return State{}, decodeErr
			}
			return state(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[State], error) {
			if !authorized {
				return reject(State{}, authorizationError)
			}
			stored, applyErr := apply(transaction, identity.Now)
			if applyErr != nil {
				return reject(State{}, applyErr)
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[State]{}, errors.New("encode Presentation submission outcome")
			}
			execution := command.Success(state(stored), string(outcome))
			if auditNote != "" {
				execution = execution.WithAudit(store.AuditDetails{Note: auditNote})
			}
			return execution, nil
		},
	})
}

func (service *Service) notifyPublicDetails() {
	if service.notifyDisplays != nil {
		service.notifyDisplays()
	}
	if service.notifySchedule != nil {
		service.notifySchedule()
	}
}

func reject(
	result State,
	err error,
) (command.Execution[State], error) {
	rejection, found := rejection(err)
	if !found {
		return command.Execution[State]{}, err
	}
	return command.Reject(result, rejection, err), nil
}

// presentationRejections is the single source for Presentation rejection
// codes in both directions.
var presentationRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrPresentationNotFound, Code: "presentation_not_found"},
		{Err: ErrSubmitterRequired, Code: "submitter_required"},
		{Err: ErrRevisionConflict, Code: "stale_presentation_submission"},
		{Err: ErrAccessClosed, Code: "submission_closed"},
	},
}

func rejection(err error) (store.CommandRejection, bool) {
	return presentationRejections.Rejection(err)
}

func decodeReceipt(outcome string, target any) error {
	return presentationRejections.Restore(store.DecodeCommandReceipt(outcome, target))
}

func validateCommand(
	eventID, sessionID, expectedRevision int,
	commandID string,
) error {
	if err := command.ValidateID(commandID); err != nil {
		return err
	}
	if eventID <= 0 || sessionID <= 0 || expectedRevision < 0 {
		return ErrInvalidInput
	}
	return nil
}

func validSpeaker(value string) bool {
	if !boundedText(value, 200) {
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

func state(stored store.PresentationSubmission) State {
	return State{
		EventID: stored.EventID, SessionID: stored.SessionID,
		SubmitterAccountID: stored.SubmitterAccountID, Revision: stored.Revision,
		SessionTitle: stored.SessionTitle, Speaker: stored.Speaker,
		PublicDetails: stored.PublicDetails, UploadDeadline: stored.UploadDeadline,
	}
}

func submission(stored store.PresentationSubmission) Submission {
	return Submission{
		EventID: stored.EventID, SessionID: stored.SessionID, Revision: stored.Revision,
		EventName: stored.EventName, SessionTitle: stored.SessionTitle,
		Timezone: stored.Timezone, EventLocale: stored.EventLocale,
		Speaker: stored.Speaker, PublicDetails: stored.PublicDetails,
		UploadDeadline: stored.UploadDeadline,
	}
}

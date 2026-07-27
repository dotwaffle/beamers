package voting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrWindowUnavailable means the Competition cannot make the requested transition.
	ErrWindowUnavailable = errors.New("voting Window is unavailable")
	// ErrVotingRevision means the submitted Ballot snapshot is stale.
	ErrVotingRevision = errors.New("voting Ballot changed")
	// ErrVotingIneligible means the Account lacks Event Voting Eligibility.
	ErrVotingIneligible = errors.New("account is not voting eligible")
	// ErrVoteUnavailable hides unknown, unpresented, and self-excluded Entries.
	ErrVoteUnavailable = errors.New("competition Entry is not votable")
)

// Window is the public value-free Competition voting configuration.
type Window = store.VotingWindow

// Ballot is one Account's complete private snapshot.
type Ballot = store.Ballot

// Participation is the value-free Crew voting projection.
type Participation = store.VotingParticipation

// ConfigureInput changes pre-open Competition voting policy.
type ConfigureInput struct {
	EventID, SessionID, ExpectedRevision int
	MethodOverride, SelfVoteOverride     string
	CommandID                            string
}

// WindowInput opens or closes one exact Competition snapshot.
type WindowInput struct {
	EventID, SessionID, ExpectedRevision int
	CommandID                            string
}

// VoteInput replaces one score on the Account's private Ballot.
type VoteInput struct {
	EventID, SessionID, EntryID int
	Value, ExpectedRevision     int
	CommandID                   string
}

// Configure changes overrides only before voting has opened.
func (service *Service) Configure(
	ctx context.Context,
	actor auth.Account,
	input ConfigureInput,
) (Window, error) {
	if !actor.CanProduceEvent(input.EventID) {
		return Window{}, ErrProducerRequired
	}
	if err := validateConfigure(input); err != nil {
		return Window{}, err
	}
	identity := service.ballotIdentity(
		actor, "ConfigureCompetitionVoting", input.EventID, input.SessionID,
		input.CommandID, input.ExpectedRevision, input.MethodOverride, input.SelfVoteOverride,
	)
	return command.Execute(actor.Context(ctx), command.Plan[Window]{
		Storage: service.storage, Identity: identity, Replay: replayWindow,
		Apply: func(transaction *store.CommandTx) (command.Execution[Window], error) {
			window, err := transaction.ConfigureVoting(ctx, store.ConfigureVotingParams{
				EventID: input.EventID, SessionID: input.SessionID,
				ExpectedRevision: input.ExpectedRevision,
				MethodOverride:   input.MethodOverride, SelfVoteOverride: input.SelfVoteOverride,
			})
			if err != nil {
				return ballotFailure[Window](err), nil
			}
			return encodedBallotSuccess(window)
		},
	})
}

func validateConfigure(input ConfigureInput) error {
	if err := command.ValidateID(input.CommandID); err != nil ||
		input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 {
		return ErrInvalidInput
	}
	if input.MethodOverride != "" && input.MethodOverride != "Range1To5" {
		return ErrInvalidInput
	}
	if input.SelfVoteOverride != "" &&
		input.SelfVoteOverride != "Allowed" &&
		input.SelfVoteOverride != "Neutral" {
		return ErrInvalidInput
	}
	return nil
}

// Open freezes effective policy and starts Ballot mutation.
func (service *Service) Open(
	ctx context.Context,
	actor auth.Account,
	input WindowInput,
) (Window, error) {
	return service.changeWindow(ctx, actor, input, true)
}

// Close stops Ballot mutation for the later immutable tally.
func (service *Service) Close(
	ctx context.Context,
	actor auth.Account,
	input WindowInput,
) (Window, error) {
	return service.changeWindow(ctx, actor, input, false)
}

func (service *Service) changeWindow(
	ctx context.Context,
	actor auth.Account,
	input WindowInput,
	open bool,
) (Window, error) {
	if !actor.CanProduceEvent(input.EventID) {
		return Window{}, ErrProducerRequired
	}
	if err := command.ValidateID(input.CommandID); err != nil ||
		input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 {
		return Window{}, ErrInvalidInput
	}
	action := "CloseCompetitionVoting"
	if open {
		action = "OpenCompetitionVoting"
	}
	identity := service.ballotIdentity(
		actor, action, input.EventID, input.SessionID,
		input.CommandID, input.ExpectedRevision,
	)
	return command.Execute(actor.Context(ctx), command.Plan[Window]{
		Storage: service.storage, Identity: identity, Replay: replayWindow,
		Apply: func(transaction *store.CommandTx) (command.Execution[Window], error) {
			params := store.VotingWindowParams{
				EventID: input.EventID, SessionID: input.SessionID,
				ExpectedRevision: input.ExpectedRevision, Now: identity.Now,
			}
			var (
				window Window
				err    error
			)
			if open {
				window, err = transaction.OpenVotingWindow(ctx, params)
			} else {
				window, err = transaction.CloseVotingWindow(ctx, params)
			}
			if err != nil {
				return ballotFailure[Window](err), nil
			}
			return encodedBallotSuccess(window)
		},
	})
}

// Vote replaces one explicit score and returns a fresh private snapshot.
func (service *Service) Vote(
	ctx context.Context,
	actor auth.Account,
	input VoteInput,
) (Ballot, error) {
	if err := command.ValidateID(input.CommandID); err != nil ||
		input.EventID <= 0 || input.SessionID <= 0 || input.EntryID <= 0 ||
		input.Value < 1 || input.Value > 5 || input.ExpectedRevision < 0 ||
		actor.ID <= 0 {
		return Ballot{}, ErrInvalidInput
	}
	identity := service.ballotIdentity(
		actor, "CastCompetitionVote", input.EventID, input.SessionID,
		input.CommandID, input.ExpectedRevision, input.EntryID, input.Value,
	)
	_, err := command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var result struct{}
			return result, ballotReceiptError(outcome)
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			_, castErr := transaction.CastVote(ctx, store.CastVoteParams{
				EventID: input.EventID, SessionID: input.SessionID,
				AccountID: actor.ID, EntryID: input.EntryID, Value: input.Value,
				ExpectedRevision: input.ExpectedRevision, Now: identity.Now,
			})
			if castErr != nil {
				return ballotFailure[struct{}](castErr), nil
			}
			return command.Success(struct{}{}, "{}"), nil
		},
	})
	if err != nil {
		return Ballot{}, err
	}
	return service.Ballot(ctx, actor, input.EventID, input.SessionID)
}

// Ballot returns only the signed-in Account's scores.
func (service *Service) Ballot(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Ballot, error) {
	if actor.ID <= 0 || eventID <= 0 || sessionID <= 0 {
		return Ballot{}, ErrInvalidInput
	}
	found, err := service.storage.LoadBallot(
		actor.Context(ctx), eventID, sessionID, actor.ID,
	)
	return found, ballotError(err)
}

// OpenBallots returns open Competitions for which the Account is eligible.
func (service *Service) OpenBallots(
	ctx context.Context,
	actor auth.Account,
) ([]Window, error) {
	if actor.ID <= 0 {
		return nil, ErrInvalidInput
	}
	return service.storage.ListOpenBallots(actor.Context(ctx), actor.ID)
}

// Window returns value-free configuration to Event Crew.
func (service *Service) Window(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Window, error) {
	if _, ok := actor.EventRoles[eventID]; !ok {
		return Window{}, ErrProducerRequired
	}
	return service.storage.LoadVotingWindow(actor.Context(ctx), eventID, sessionID)
}

// Participation returns aggregate Crew counts without individual scores.
func (service *Service) Participation(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Participation, error) {
	if _, ok := actor.EventRoles[eventID]; !ok {
		return Participation{}, ErrProducerRequired
	}
	return service.storage.LoadVotingParticipation(actor.Context(ctx), eventID, sessionID)
}

func (service *Service) ballotIdentity(
	actor auth.Account,
	action string,
	eventID, sessionID int,
	commandID string,
	values ...any,
) store.CommandIdentity {
	payload := []string{strconv.Itoa(eventID), strconv.Itoa(sessionID)}
	for _, value := range values {
		payload = append(payload, fmt.Sprint(value))
	}
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(payload...),
		Action:      action, TargetType: "Competition", TargetID: strconv.Itoa(sessionID),
		Now: service.now().UTC(),
	}
}

func encodedBallotSuccess[T any](value T) (command.Execution[T], error) {
	outcome, err := json.Marshal(value)
	if err != nil {
		return command.Execution[T]{}, errors.New("encode voting command outcome")
	}
	return command.Success(value, string(outcome)), nil
}

func replayWindow(outcome string) (Window, error) {
	var window Window
	err := store.DecodeCommandReceipt(outcome, &window)
	if err != nil {
		return Window{}, ballotReceiptError(outcome)
	}
	return window, nil
}

func ballotFailure[T any](err error) command.Execution[T] {
	var zero T
	mapped := ballotError(err)
	code := "voting_window_unavailable"
	switch {
	case errors.Is(mapped, ErrVotingRevision):
		code = "voting_revision_conflict"
	case errors.Is(mapped, ErrVotingIneligible):
		code = "voting_ineligible"
	case errors.Is(mapped, ErrVoteUnavailable):
		code = "vote_unavailable"
	}
	return command.Reject(zero, store.CommandRejection{Code: code}, mapped)
}

func ballotError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrVotingRevision):
		return ErrVotingRevision
	case errors.Is(err, store.ErrVotingIneligible):
		return ErrVotingIneligible
	case errors.Is(err, store.ErrVoteUnavailable):
		return ErrVoteUnavailable
	case errors.Is(err, store.ErrVotingWindowState),
		errors.Is(err, store.ErrVotingCompetitionNotFound):
		return ErrWindowUnavailable
	default:
		return err
	}
}

func ballotReceiptError(outcome string) error {
	var result struct{}
	err := store.DecodeCommandReceipt(outcome, &result)
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	switch rejected.Rejection.Code {
	case "voting_window_unavailable":
		return ErrWindowUnavailable
	case "voting_revision_conflict":
		return ErrVotingRevision
	case "voting_ineligible":
		return ErrVotingIneligible
	case "vote_unavailable":
		return ErrVoteUnavailable
	default:
		return err
	}
}

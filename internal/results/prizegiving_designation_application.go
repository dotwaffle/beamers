package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

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

func prizegiving(stored store.Prizegiving) Prizegiving {
	return Prizegiving{
		ID: stored.ID, EventID: stored.EventID,
		CeremonySessionID:  stored.CeremonySessionID,
		CreatedByAccountID: stored.CreatedByAccountID, CreatedAt: stored.CreatedAt,
	}
}

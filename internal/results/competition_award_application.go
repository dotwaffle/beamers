package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/awardvalue"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// SaveCompetitionAwardsInput replaces Awards while preserving Placement and Score.
type SaveCompetitionAwardsInput struct {
	EventID          int     `json:"event_id"`
	SessionID        int     `json:"session_id"`
	CommandID        string  `json:"command_id"`
	ExpectedRevision int     `json:"expected_revision"`
	Awards           []Award `json:"awards"`
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

func validateSaveCompetitionAwardsInput(input SaveCompetitionAwardsInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 {
		return ErrInvalidInput
	}
	return ValidateAwards(input.Awards)
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

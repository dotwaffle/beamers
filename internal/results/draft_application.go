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
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/votingvalue"
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Draft]{
		Storage:  service.storage,
		Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: resultsRejections,
		},
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Draft]{
		Storage:  service.storage,
		Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: resultsRejections,
		},
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

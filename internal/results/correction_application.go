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
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrCorrectionRevision means correction state advanced after observation.
	ErrCorrectionRevision = store.ErrResultsCorrectionRevision
	// ErrCorrectionTransition means correction review or publication is invalid.
	ErrCorrectionTransition = store.ErrResultsCorrectionTransition
	// ErrCorrectionBase means the released revision changed or is unavailable.
	ErrCorrectionBase = errors.New("results correction base publication changed")
)

// PublicationScope identifies one correction target.
type PublicationScope string

const (
	// PublicationScopePrizegiving targets one Prizegiving publication.
	PublicationScopePrizegiving PublicationScope = "Prizegiving"
	// PublicationScopeStandalone targets one standalone Competition publication.
	PublicationScopeStandalone PublicationScope = "Standalone"
)

// CorrectionStatus describes one append-only review revision.
type CorrectionStatus string

const (
	// CorrectionDraft is editable and not reviewed.
	CorrectionDraft CorrectionStatus = "Draft"
	// CorrectionReady is the exact Producer-reviewed proposal.
	CorrectionReady CorrectionStatus = "Ready"
	// CorrectionPublished records atomic public publication.
	CorrectionPublished CorrectionStatus = "Published"
)

// Correction is one Results Correction lifecycle revision.
type Correction struct {
	EventID                  int                `json:"event_id"`
	Scope                    PublicationScope   `json:"scope"`
	ScopeSessionID           int                `json:"scope_session_id"`
	Revision                 int                `json:"revision"`
	BasePublicationRevision  int                `json:"base_publication_revision"`
	Status                   CorrectionStatus   `json:"status"`
	Proposal                 CorrectionProposal `json:"proposal"`
	PublishedResultsRevision int                `json:"published_results_revision,omitempty"`
	CreatedByAccountID       int                `json:"created_by_account_id"`
	CreatedAt                time.Time          `json:"created_at"`
}

// SaveCorrectionInput replaces one complete correction proposal.
type SaveCorrectionInput struct {
	EventID                 int                 `json:"event_id"`
	Scope                   PublicationScope    `json:"scope"`
	ScopeSessionID          int                 `json:"scope_session_id"`
	CommandID               string              `json:"command_id"`
	ExpectedRevision        int                 `json:"expected_revision"`
	BasePublicationRevision int                 `json:"base_publication_revision"`
	PublicationOrder        []ResultItemRef     `json:"publication_order"`
	Items                   []PublicResultsItem `json:"items"`
	Template                TextTemplate        `json:"template"`
	CrewReason              string              `json:"crew_reason"`
	PublicNote              string              `json:"public_note,omitempty"`
}

// ReviewCorrectionInput identifies one exact Draft for Producer review.
type ReviewCorrectionInput struct {
	EventID          int              `json:"event_id"`
	Scope            PublicationScope `json:"scope"`
	ScopeSessionID   int              `json:"scope_session_id"`
	CommandID        string           `json:"command_id"`
	ExpectedRevision int              `json:"expected_revision"`
}

// PublishCorrectionResult contains the lifecycle and new public revision.
type PublishCorrectionResult struct {
	Correction  Correction  `json:"correction"`
	Publication Publication `json:"publication"`
}

// GetCorrection returns the latest exact proposal to authorized Results viewers.
func (service *Service) GetCorrection(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	scope PublicationScope,
	scopeSessionID int,
) (Correction, error) {
	if eventID <= 0 ||
		scopeSessionID <= 0 ||
		!validCorrectionScope(scope) {
		return Correction{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return Correction{}, ErrViewRequired
	}
	found, err := service.storage.LoadResultsCorrection(
		actor.Context(ctx),
		eventID,
		store.ResultsPublicationScope(scope),
		scopeSessionID,
	)
	if err != nil {
		return Correction{}, err
	}
	return correctionFromStore(found), nil
}

// SaveCorrection appends one unreviewed correction proposal.
func (service *Service) SaveCorrection(
	ctx context.Context,
	actor auth.Account,
	input SaveCorrectionInput,
) (Correction, error) {
	if err := validateSaveCorrectionInput(input); err != nil {
		return Correction{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Correction{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Correction{}, errors.New("encode Results Correction command")
	}
	identity := correctionCommandIdentity(
		actor,
		input.EventID,
		input.ScopeSessionID,
		input.CommandID,
		"SaveResultsCorrection",
		string(payload),
		service.now().UTC(),
	)
	return command.Execute(actor.Context(ctx), command.Plan[Correction]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Correction, error) {
			var result Correction
			if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
				return Correction{}, err
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Correction], error) {
			current, loadErr := transaction.LoadResultsCorrection(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationScope(input.Scope),
				input.ScopeSessionID,
			)
			if loadErr != nil {
				return command.Execution[Correction]{}, loadErr
			}
			if current.Revision != input.ExpectedRevision {
				return command.Execution[Correction]{}, ErrCorrectionRevision
			}
			proposal := correctionProposalFromSave(input)
			if _, validateErr := validateCorrectionProposal(
				actor.Context(ctx),
				transaction,
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				input.BasePublicationRevision,
				proposal,
				identity.Now,
			); validateErr != nil {
				return command.Execution[Correction]{}, validateErr
			}
			params, paramsErr := correctionStoreParams(
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				current.Revision,
				input.BasePublicationRevision,
				store.ResultsCorrectionDraft,
				proposal,
				0,
				actor.ID,
				identity.Now,
			)
			if paramsErr != nil {
				return command.Execution[Correction]{}, paramsErr
			}
			stored, appendErr := transaction.AppendResultsCorrection(
				actor.Context(ctx),
				params,
			)
			if appendErr != nil {
				return command.Execution[Correction]{}, appendErr
			}
			return correctionExecution(correctionFromStore(stored))
		},
	})
}

// ReviewCorrection marks one exact proposal Ready.
func (service *Service) ReviewCorrection(
	ctx context.Context,
	actor auth.Account,
	input ReviewCorrectionInput,
) (Correction, error) {
	return service.advanceCorrectionReview(
		ctx,
		actor,
		input,
		store.ResultsCorrectionReady,
		"ReviewResultsCorrection",
	)
}

// PublishCorrection atomically publishes one exact Ready proposal.
func (service *Service) PublishCorrection(
	ctx context.Context,
	actor auth.Account,
	input ReviewCorrectionInput,
) (PublishCorrectionResult, error) {
	if err := validateReviewCorrectionInput(input); err != nil {
		return PublishCorrectionResult{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return PublishCorrectionResult{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return PublishCorrectionResult{}, errors.New("encode Results Correction publication")
	}
	identity := correctionCommandIdentity(
		actor,
		input.EventID,
		input.ScopeSessionID,
		input.CommandID,
		"PublishResultsCorrection",
		string(payload),
		service.now().UTC(),
	)
	return command.Execute(actor.Context(ctx), command.Plan[PublishCorrectionResult]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (PublishCorrectionResult, error) {
			var result PublishCorrectionResult
			if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
				return PublishCorrectionResult{}, err
			}
			return result, nil
		},
		Apply: func(
			transaction *store.CommandTx,
		) (command.Execution[PublishCorrectionResult], error) {
			current, loadErr := transaction.LoadResultsCorrection(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationScope(input.Scope),
				input.ScopeSessionID,
			)
			if loadErr != nil {
				return command.Execution[PublishCorrectionResult]{}, loadErr
			}
			if current.Revision != input.ExpectedRevision {
				return command.Execution[PublishCorrectionResult]{}, ErrCorrectionRevision
			}
			if current.Status != store.ResultsCorrectionReady {
				return command.Execution[PublishCorrectionResult]{}, ErrCorrectionTransition
			}
			proposal, decodeErr := correctionProposalFromStore(current)
			if decodeErr != nil {
				return command.Execution[PublishCorrectionResult]{}, decodeErr
			}
			base, validateErr := validateCorrectionProposal(
				actor.Context(ctx),
				transaction,
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				current.BasePublicationRevision,
				proposal,
				identity.Now,
			)
			if validateErr != nil {
				return command.Execution[PublishCorrectionResult]{}, validateErr
			}
			var currentModel PublicResultsPublication
			if json.Unmarshal([]byte(base.RenderedJSON), &currentModel) != nil {
				return command.Execution[PublishCorrectionResult]{}, ErrCorrectionBase
			}
			correctedModel, buildErr := BuildCorrectedResultsPublication(
				currentModel,
				publicationFromStore(base).Items,
				proposal,
				identity.Now,
			)
			if buildErr != nil {
				return command.Execution[PublishCorrectionResult]{}, buildErr
			}
			rendered, renderErr := RenderPublicResults(correctedModel, proposal.Template)
			if renderErr != nil {
				return command.Execution[PublishCorrectionResult]{}, renderErr
			}
			nextCorrectionRevision := current.Revision + 1
			lock := base.Lock
			lock.PublicationOrder = prizegivingItemRefInputs(proposal.PublicationOrder)
			lock.Template = prizegivingTemplateInput(proposal.Template)
			published, appendErr := transaction.AppendResultsPublication(
				actor.Context(ctx),
				store.AppendResultsPublicationParams{
					EventID:          input.EventID,
					Scope:            store.ResultsPublicationScope(input.Scope),
					ScopeSessionID:   input.ScopeSessionID,
					ExpectedRevision: base.Revision,
					Policy:           base.Policy, Status: base.Status,
					Items: prizegivingItemRefInputs(proposal.PublicationOrder),
					Lock:  lock, Template: prizegivingTemplateInput(proposal.Template),
					RenderedHTML: rendered.HTML, RenderedText: rendered.Text,
					RenderedJSON:              rendered.JSON,
					ResultsCorrectionRevision: nextCorrectionRevision,
					CreatedByAccountID:        actor.ID, Now: identity.Now,
				},
			)
			if appendErr != nil {
				return command.Execution[PublishCorrectionResult]{}, appendErr
			}
			params, paramsErr := correctionStoreParams(
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				current.Revision,
				current.BasePublicationRevision,
				store.ResultsCorrectionPublished,
				proposal,
				published.Revision,
				actor.ID,
				identity.Now,
			)
			if paramsErr != nil {
				return command.Execution[PublishCorrectionResult]{}, paramsErr
			}
			stored, appendErr := transaction.AppendResultsCorrection(
				actor.Context(ctx),
				params,
			)
			if appendErr != nil {
				return command.Execution[PublishCorrectionResult]{}, appendErr
			}
			result := PublishCorrectionResult{
				Correction:  correctionFromStore(stored),
				Publication: publicationFromStore(published),
			}
			outcome, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				return command.Execution[PublishCorrectionResult]{}, errors.New(
					"encode published Results Correction",
				)
			}
			return command.Success(result, string(outcome)), nil
		},
	})
}

func (service *Service) advanceCorrectionReview(
	ctx context.Context,
	actor auth.Account,
	input ReviewCorrectionInput,
	status store.ResultsCorrectionStatus,
	action string,
) (Correction, error) {
	if err := validateReviewCorrectionInput(input); err != nil {
		return Correction{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Correction{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Correction{}, errors.New("encode Results Correction review")
	}
	identity := correctionCommandIdentity(
		actor,
		input.EventID,
		input.ScopeSessionID,
		input.CommandID,
		action,
		string(payload),
		service.now().UTC(),
	)
	return command.Execute(actor.Context(ctx), command.Plan[Correction]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Correction, error) {
			var result Correction
			if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
				return Correction{}, err
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Correction], error) {
			current, loadErr := transaction.LoadResultsCorrection(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationScope(input.Scope),
				input.ScopeSessionID,
			)
			if loadErr != nil {
				return command.Execution[Correction]{}, loadErr
			}
			if current.Revision != input.ExpectedRevision {
				return command.Execution[Correction]{}, ErrCorrectionRevision
			}
			if current.Status != store.ResultsCorrectionDraft {
				return command.Execution[Correction]{}, ErrCorrectionTransition
			}
			proposal, decodeErr := correctionProposalFromStore(current)
			if decodeErr != nil {
				return command.Execution[Correction]{}, decodeErr
			}
			if _, validateErr := validateCorrectionProposal(
				actor.Context(ctx),
				transaction,
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				current.BasePublicationRevision,
				proposal,
				identity.Now,
			); validateErr != nil {
				return command.Execution[Correction]{}, validateErr
			}
			params, paramsErr := correctionStoreParams(
				input.EventID,
				input.Scope,
				input.ScopeSessionID,
				current.Revision,
				current.BasePublicationRevision,
				status,
				proposal,
				0,
				actor.ID,
				identity.Now,
			)
			if paramsErr != nil {
				return command.Execution[Correction]{}, paramsErr
			}
			stored, appendErr := transaction.AppendResultsCorrection(
				actor.Context(ctx),
				params,
			)
			if appendErr != nil {
				return command.Execution[Correction]{}, appendErr
			}
			return correctionExecution(correctionFromStore(stored))
		},
	})
}

func validateCorrectionProposal(
	ctx context.Context,
	transaction *store.CommandTx,
	eventID int,
	scope PublicationScope,
	scopeSessionID, baseRevision int,
	proposal CorrectionProposal,
	now time.Time,
) (store.ResultsPublication, error) {
	base, err := transaction.LoadResultsPublication(
		ctx,
		eventID,
		store.ResultsPublicationScope(scope),
		scopeSessionID,
	)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	if base.Revision == 0 ||
		base.Revision != baseRevision ||
		base.RenderedJSON == "" {
		return store.ResultsPublication{}, ErrCorrectionBase
	}
	var currentModel PublicResultsPublication
	if json.Unmarshal([]byte(base.RenderedJSON), &currentModel) != nil {
		return store.ResultsPublication{}, ErrCorrectionBase
	}
	corrected, err := BuildCorrectedResultsPublication(
		currentModel,
		publicationFromStore(base).Items,
		proposal,
		now,
	)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	if _, err = RenderPublicResults(corrected, proposal.Template); err != nil {
		return store.ResultsPublication{}, err
	}
	return base, nil
}

func validateSaveCorrectionInput(input SaveCorrectionInput) error {
	if command.ValidateID(input.CommandID) != nil ||
		input.EventID <= 0 ||
		input.ScopeSessionID <= 0 ||
		input.ExpectedRevision < 0 ||
		input.BasePublicationRevision <= 0 ||
		!validCorrectionScope(input.Scope) ||
		!boundedResultsTextTemplate(input.Template) {
		return ErrInvalidInput
	}
	return nil
}

func validateReviewCorrectionInput(input ReviewCorrectionInput) error {
	if command.ValidateID(input.CommandID) != nil ||
		input.EventID <= 0 ||
		input.ScopeSessionID <= 0 ||
		input.ExpectedRevision <= 0 ||
		!validCorrectionScope(input.Scope) {
		return ErrInvalidInput
	}
	return nil
}

func validCorrectionScope(scope PublicationScope) bool {
	return scope == PublicationScopePrizegiving ||
		scope == PublicationScopeStandalone
}

func correctionProposalFromSave(input SaveCorrectionInput) CorrectionProposal {
	return CorrectionProposal{
		PublicationOrder: input.PublicationOrder,
		Items:            input.Items, Template: input.Template,
		CrewReason: input.CrewReason, PublicNote: input.PublicNote,
	}
}

func correctionProposalFromStore(
	value store.ResultsCorrection,
) (CorrectionProposal, error) {
	var items []PublicResultsItem
	if json.Unmarshal([]byte(value.ItemsJSON), &items) != nil {
		return CorrectionProposal{}, ErrCorrectionTransition
	}
	return CorrectionProposal{
		PublicationOrder: prizegivingItemRefs(value.PublicationOrder),
		Items:            items, Template: prizegivingTemplate(value.Template),
		CrewReason: value.CrewReason, PublicNote: value.PublicNote,
	}, nil
}

func correctionStoreParams(
	eventID int,
	scope PublicationScope,
	scopeSessionID, expectedRevision, baseRevision int,
	status store.ResultsCorrectionStatus,
	proposal CorrectionProposal,
	publishedRevision, actorID int,
	now time.Time,
) (store.AppendResultsCorrectionParams, error) {
	itemsJSON, err := json.Marshal(proposal.Items)
	if err != nil {
		return store.AppendResultsCorrectionParams{}, ErrResultsCorrection
	}
	return store.AppendResultsCorrectionParams{
		EventID: eventID, Scope: store.ResultsPublicationScope(scope),
		ScopeSessionID: scopeSessionID, ExpectedRevision: expectedRevision,
		BasePublicationRevision: baseRevision, Status: status,
		PublicationOrder: prizegivingItemRefInputs(proposal.PublicationOrder),
		ItemsJSON:        string(itemsJSON), Template: prizegivingTemplateInput(proposal.Template),
		CrewReason: proposal.CrewReason, PublicNote: proposal.PublicNote,
		PublishedResultsRevision: publishedRevision,
		CreatedByAccountID:       actorID, Now: now,
	}, nil
}

func correctionFromStore(value store.ResultsCorrection) Correction {
	proposal, _ := correctionProposalFromStore(value)
	return Correction{
		EventID: value.EventID, Scope: PublicationScope(value.Scope),
		ScopeSessionID: value.ScopeSessionID, Revision: value.Revision,
		BasePublicationRevision: value.BasePublicationRevision,
		Status:                  CorrectionStatus(value.Status), Proposal: proposal,
		PublishedResultsRevision: value.PublishedResultsRevision,
		CreatedByAccountID:       value.CreatedByAccountID, CreatedAt: value.CreatedAt,
	}
}

func correctionExecution(
	value Correction,
) (command.Execution[Correction], error) {
	outcome, err := json.Marshal(value)
	if err != nil {
		return command.Execution[Correction]{}, errors.New(
			"encode Results Correction outcome",
		)
	}
	return command.Success(value, string(outcome)), nil
}

func correctionCommandIdentity(
	actor auth.Account,
	eventID, scopeSessionID int,
	commandID, action, payload string,
	now time.Time,
) store.CommandIdentity {
	return store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(payload), Action: action,
		TargetType: "ResultsPublication",
		TargetID:   strconv.Itoa(eventID) + "/" + strconv.Itoa(scopeSessionID),
		Now:        now,
	}
}

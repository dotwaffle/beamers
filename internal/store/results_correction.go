package store

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/resultscorrection"
)

var (
	// ErrResultsCorrectionRevision means correction state advanced after observation.
	ErrResultsCorrectionRevision = errors.New("results correction revision conflict")
	// ErrResultsCorrectionTransition means a correction lifecycle append is invalid.
	ErrResultsCorrectionTransition = errors.New("invalid Results Correction transition")
)

// ResultsCorrectionStatus describes one append-only review revision.
type ResultsCorrectionStatus string

const (
	// ResultsCorrectionDraft is editable and not reviewed.
	ResultsCorrectionDraft ResultsCorrectionStatus = "Draft"
	// ResultsCorrectionReady is the exact Producer-reviewed proposal.
	ResultsCorrectionReady ResultsCorrectionStatus = "Ready"
	// ResultsCorrectionPublished records atomic public publication.
	ResultsCorrectionPublished ResultsCorrectionStatus = "Published"
)

// ResultsCorrection is one append-only correction lifecycle revision.
type ResultsCorrection struct {
	EventID                  int
	Scope                    ResultsPublicationScope
	ScopeSessionID           int
	Revision                 int
	BasePublicationRevision  int
	Status                   ResultsCorrectionStatus
	PublicationOrder         []PrizegivingResultItemRef
	ItemsJSON                string
	Template                 PrizegivingResultsTextTemplate
	CrewReason               string
	PublicNote               string
	PublishedResultsRevision int
	CreatedByAccountID       int
	CreatedAt                time.Time
}

// AppendResultsCorrectionParams contains one complete lifecycle revision.
type AppendResultsCorrectionParams struct {
	EventID                  int
	Scope                    ResultsPublicationScope
	ScopeSessionID           int
	ExpectedRevision         int
	BasePublicationRevision  int
	Status                   ResultsCorrectionStatus
	PublicationOrder         []PrizegivingResultItemRef
	ItemsJSON                string
	Template                 PrizegivingResultsTextTemplate
	CrewReason               string
	PublicNote               string
	PublishedResultsRevision int
	CreatedByAccountID       int
	Now                      time.Time
}

// AppendResultsCorrection appends one validated correction lifecycle revision.
func (transaction *CommandTx) AppendResultsCorrection(
	ctx context.Context,
	params AppendResultsCorrectionParams,
) (ResultsCorrection, error) {
	ctx = systemContext(ctx)
	current, found, err := loadResultsCorrection(
		ctx,
		transaction.transaction.Client(),
		params.EventID,
		params.Scope,
		params.ScopeSessionID,
	)
	if err != nil {
		return ResultsCorrection{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return current, ErrResultsCorrectionRevision
	}
	if !validResultsCorrectionAppend(current, found, params) {
		return current, ErrResultsCorrectionTransition
	}
	create := transaction.transaction.ResultsCorrection.Create().
		SetEventID(params.EventID).
		SetScope(resultscorrection.Scope(params.Scope)).
		SetScopeSessionID(params.ScopeSessionID).
		SetRevision(params.ExpectedRevision + 1).
		SetBasePublicationRevision(params.BasePublicationRevision).
		SetStatus(resultscorrection.Status(params.Status)).
		SetPublicationOrder(prizegivingItemRefValues(params.PublicationOrder)).
		SetItemsJSON(params.ItemsJSON).
		SetResultsTextTemplate(prizegivingTemplateValue(params.Template)).
		SetCrewReason(params.CrewReason).
		SetPublicNote(params.PublicNote).
		SetCreatedByAccountID(params.CreatedByAccountID).
		SetCreatedAt(params.Now)
	if params.PublishedResultsRevision > 0 {
		create.SetPublishedResultsRevision(params.PublishedResultsRevision)
	}
	created, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return ResultsCorrection{}, ErrResultsCorrectionRevision
		}
		return ResultsCorrection{}, opaqueError("append Results Correction", err)
	}
	return resultsCorrection(created), nil
}

// LoadResultsCorrection returns the latest correction lifecycle revision.
func (installation *SQLite) LoadResultsCorrection(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsCorrection, error) {
	found, _, err := loadResultsCorrection(
		systemContext(ctx),
		installation.client,
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

// LoadResultsCorrection returns the latest correction inside a command transaction.
func (transaction *CommandTx) LoadResultsCorrection(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsCorrection, error) {
	found, _, err := loadResultsCorrection(
		systemContext(ctx),
		transaction.transaction.Client(),
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

func loadResultsCorrection(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsCorrection, bool, error) {
	found, err := client.ResultsCorrection.Query().
		Where(
			resultscorrection.EventIDEQ(eventID),
			resultscorrection.ScopeEQ(resultscorrection.Scope(scope)),
			resultscorrection.ScopeSessionIDEQ(scopeSessionID),
		).
		Order(ent.Desc(resultscorrection.FieldRevision)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ResultsCorrection{}, false, nil
	}
	if err != nil {
		return ResultsCorrection{}, false, opaqueError("load Results Correction", err)
	}
	return resultsCorrection(found), true, nil
}

func validResultsCorrectionAppend(
	current ResultsCorrection,
	found bool,
	params AppendResultsCorrectionParams,
) bool {
	if params.BasePublicationRevision <= 0 ||
		params.CreatedByAccountID <= 0 ||
		params.ItemsJSON == "" ||
		params.CrewReason == "" {
		return false
	}
	if !found {
		return params.Status == ResultsCorrectionDraft &&
			params.PublishedResultsRevision == 0
	}
	switch params.Status {
	case ResultsCorrectionDraft:
		return params.PublishedResultsRevision == 0 &&
			(current.Status == ResultsCorrectionDraft ||
				current.Status == ResultsCorrectionReady ||
				current.Status == ResultsCorrectionPublished)
	case ResultsCorrectionReady:
		return current.Status == ResultsCorrectionDraft &&
			params.PublishedResultsRevision == 0 &&
			sameResultsCorrectionContent(current, params)
	case ResultsCorrectionPublished:
		return current.Status == ResultsCorrectionReady &&
			params.PublishedResultsRevision == params.BasePublicationRevision+1 &&
			sameResultsCorrectionContent(current, params)
	default:
		return false
	}
}

func sameResultsCorrectionContent(
	current ResultsCorrection,
	params AppendResultsCorrectionParams,
) bool {
	return current.BasePublicationRevision == params.BasePublicationRevision &&
		reflect.DeepEqual(current.PublicationOrder, params.PublicationOrder) &&
		current.ItemsJSON == params.ItemsJSON &&
		current.Template == params.Template &&
		current.CrewReason == params.CrewReason &&
		current.PublicNote == params.PublicNote
}

func resultsCorrection(found *ent.ResultsCorrection) ResultsCorrection {
	return ResultsCorrection{
		EventID: found.EventID, Scope: ResultsPublicationScope(found.Scope),
		ScopeSessionID: found.ScopeSessionID, Revision: found.Revision,
		BasePublicationRevision:  found.BasePublicationRevision,
		Status:                   ResultsCorrectionStatus(found.Status),
		PublicationOrder:         prizegivingItemRefs(found.PublicationOrder),
		ItemsJSON:                found.ItemsJSON,
		Template:                 prizegivingTemplate(found.ResultsTextTemplate),
		CrewReason:               found.CrewReason,
		PublicNote:               found.PublicNote,
		PublishedResultsRevision: found.PublishedResultsRevision,
		CreatedByAccountID:       found.CreatedByAccountID,
		CreatedAt:                found.CreatedAt,
	}
}

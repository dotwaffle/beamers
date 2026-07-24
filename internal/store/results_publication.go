package store

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/resultspublication"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

var (
	// ErrResultsPublicationRevision means a release scope advanced after observation.
	ErrResultsPublicationRevision = errors.New("results publication revision conflict")
	// ErrResultsPublicationTransition means an append would retract or rewrite public truth.
	ErrResultsPublicationTransition = errors.New("invalid Results Publication transition")
)

// ResultsPublicationScope identifies one independently released result set.
type ResultsPublicationScope string

const (
	// ResultsPublicationPrizegiving identifies one Ceremony release scope.
	ResultsPublicationPrizegiving ResultsPublicationScope = "Prizegiving"
	// ResultsPublicationStandalone identifies one unassigned Competition scope.
	ResultsPublicationStandalone ResultsPublicationScope = "Standalone"
)

// ResultsPublicationPolicy selects the release trigger for one scope.
type ResultsPublicationPolicy = prizegivingvalue.ReleasePolicy

const (
	// ResultsPublicationAllAtCue releases a locked set atomically at its cue.
	ResultsPublicationAllAtCue = prizegivingvalue.ReleaseAllAtCue
	// ResultsPublicationProgressive releases completed Result Items.
	ResultsPublicationProgressive = prizegivingvalue.ReleaseProgressiveOnReveal
	// ResultsPublicationAtCeremonyEnd releases a resolved set at completion.
	ResultsPublicationAtCeremonyEnd = prizegivingvalue.ReleaseAtCeremonyEnd
	// ResultsPublicationStandalonePolicy releases one unassigned Competition.
	ResultsPublicationStandalonePolicy = prizegivingvalue.ReleaseStandalone
)

// ResultsPublicationStatus describes a scope's current public state.
type ResultsPublicationStatus string

const (
	// ResultsPublicationPartial contains a released subset.
	ResultsPublicationPartial ResultsPublicationStatus = "Partial"
	// ResultsPublicationFinal contains the complete scope.
	ResultsPublicationFinal ResultsPublicationStatus = "Final"
)

// ResultsPublication is one immutable release-manifest revision.
type ResultsPublication struct {
	EventID            int
	Scope              ResultsPublicationScope
	ScopeSessionID     int
	Revision           int
	Policy             ResultsPublicationPolicy
	Status             ResultsPublicationStatus
	Items              []PrizegivingResultItemRef
	Lock               PrizegivingPreflightLock
	CreatedByAccountID int
	CreatedAt          time.Time
}

// AppendResultsPublicationParams contains one complete immutable manifest.
type AppendResultsPublicationParams struct {
	EventID            int
	Scope              ResultsPublicationScope
	ScopeSessionID     int
	ExpectedRevision   int
	Policy             ResultsPublicationPolicy
	Status             ResultsPublicationStatus
	Items              []PrizegivingResultItemRef
	Lock               PrizegivingPreflightLock
	CreatedByAccountID int
	Now                time.Time
}

// AppendResultsPublication appends one monotonic manifest revision.
func (transaction *CommandTx) AppendResultsPublication(
	ctx context.Context,
	params AppendResultsPublicationParams,
) (ResultsPublication, error) {
	ctx = systemContext(ctx)
	current, found, err := loadResultsPublication(
		ctx,
		transaction.transaction.Client(),
		params.EventID,
		params.Scope,
		params.ScopeSessionID,
	)
	if err != nil {
		return ResultsPublication{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return current, ErrResultsPublicationRevision
	}
	if found && !validResultsPublicationAppend(current, params) {
		return current, ErrResultsPublicationTransition
	}
	create := transaction.transaction.ResultsPublication.Create().
		SetEventID(params.EventID).
		SetScope(resultspublication.Scope(params.Scope)).
		SetScopeSessionID(params.ScopeSessionID).
		SetRevision(params.ExpectedRevision + 1).
		SetReleasePolicy(resultspublication.ReleasePolicy(params.Policy)).
		SetStatus(resultspublication.Status(params.Status)).
		SetItems(prizegivingItemRefValues(params.Items)).
		SetPrizegivingLock(prizegivingLockValue(params.Lock)).
		SetCreatedAt(params.Now)
	if params.CreatedByAccountID > 0 {
		create.SetCreatedByAccountID(params.CreatedByAccountID)
	}
	created, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return ResultsPublication{}, ErrResultsPublicationRevision
		}
		return ResultsPublication{}, opaqueError("append Results Publication", err)
	}
	return resultsPublication(created), nil
}

// LoadResultsPublication returns the latest manifest or zero before first release.
func (installation *SQLite) LoadResultsPublication(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, error) {
	found, _, err := loadResultsPublication(
		systemContext(ctx),
		installation.client,
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

// LoadResultsPublication returns the latest manifest inside a command transaction.
func (transaction *CommandTx) LoadResultsPublication(
	ctx context.Context,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, error) {
	found, _, err := loadResultsPublication(
		systemContext(ctx),
		transaction.transaction.Client(),
		eventID,
		scope,
		scopeSessionID,
	)
	return found, err
}

func loadResultsPublication(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	scope ResultsPublicationScope,
	scopeSessionID int,
) (ResultsPublication, bool, error) {
	found, err := client.ResultsPublication.Query().
		Where(
			resultspublication.EventIDEQ(eventID),
			resultspublication.ScopeEQ(resultspublication.Scope(scope)),
			resultspublication.ScopeSessionIDEQ(scopeSessionID),
		).
		Order(ent.Desc(resultspublication.FieldRevision)).
		First(ctx)
	if ent.IsNotFound(err) {
		return ResultsPublication{}, false, nil
	}
	if err != nil {
		return ResultsPublication{}, false, opaqueError("load Results Publication", err)
	}
	return resultsPublication(found), true, nil
}

func validResultsPublicationAppend(
	current ResultsPublication,
	params AppendResultsPublicationParams,
) bool {
	if current.Status == ResultsPublicationFinal ||
		current.Policy != params.Policy ||
		!reflect.DeepEqual(current.Lock, params.Lock) {
		return false
	}
	nextItems := make(map[PrizegivingResultItemRef]struct{}, len(params.Items))
	for _, ref := range params.Items {
		nextItems[ref] = struct{}{}
	}
	for _, ref := range current.Items {
		if _, ok := nextItems[ref]; !ok {
			return false
		}
	}
	return true
}

func resultsPublication(found *ent.ResultsPublication) ResultsPublication {
	result := ResultsPublication{
		EventID:        found.EventID,
		Scope:          ResultsPublicationScope(found.Scope),
		ScopeSessionID: found.ScopeSessionID,
		Revision:       found.Revision,
		Policy:         ResultsPublicationPolicy(found.ReleasePolicy),
		Status:         ResultsPublicationStatus(found.Status),
		Items:          prizegivingItemRefs(found.Items),
		Lock:           prizegivingLock(found.PrizegivingLock),
		CreatedAt:      found.CreatedAt,
	}
	if found.CreatedByAccountID != nil {
		result.CreatedByAccountID = *found.CreatedByAccountID
	}
	return result
}

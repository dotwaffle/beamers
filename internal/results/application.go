package results

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrViewRequired means the actor cannot read unreleased Results Drafts.
	ErrViewRequired = errors.New("results view capability required")
	// ErrManageRequired means the actor cannot change Results Drafts.
	ErrManageRequired = errors.New("results manage capability required")
	// ErrProducerRequired means only a Producer can complete Results review.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrRevisionConflict means a Results command used a stale Draft revision.
	ErrRevisionConflict = store.ErrCompetitionResultsRevision
	// ErrEntryOutsideCompetition means a Standing crossed an ownership boundary.
	ErrEntryOutsideCompetition = store.ErrCompetitionResultsEntry
	// ErrAwardEntryOutsideScope means an Award recipient crossed an ownership boundary.
	ErrAwardEntryOutsideScope = store.ErrResultsAwardEntry
	// ErrEventAwardsRevision means an Event Awards command used a stale Draft revision.
	ErrEventAwardsRevision = store.ErrEventAwardsRevision
	// ErrEventAwardPath means an Event Award targets an invalid release path.
	ErrEventAwardPath = store.ErrEventAwardPath
	// ErrPrizegivingSession means a designation does not target an Event Ceremony.
	ErrPrizegivingSession = store.ErrPrizegivingSession
	// ErrCompetitionNotFound means no Competition matched the stable IDs.
	ErrCompetitionNotFound = store.ErrCompetitionNotFound
	// ErrEventNotFound means no Event matched the stable ID.
	ErrEventNotFound = store.ErrEventNotFound
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrInvalidInput means a Results request is malformed or unsafe.
	ErrInvalidInput = errors.New("invalid results input")
)

// Service owns unreleased Results Draft queries and durable commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates a Results Service with explicit dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("results storage is required")
	}
	if now == nil {
		return nil, errors.New("results clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Get returns the current Results Draft to explicitly authorized Event crew.
func (service *Service) Get(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (Draft, error) {
	if eventID <= 0 || sessionID <= 0 {
		return Draft{}, ErrInvalidInput
	}
	if !authz.Holds(actor.Identity(), eventID, authz.ViewResults) {
		return Draft{}, ErrViewRequired
	}
	found, err := service.storage.LoadCompetitionResultsDraft(
		actor.Context(ctx), eventID, sessionID,
	)
	if err != nil {
		return Draft{}, err
	}
	result := draft(found)
	if found.VotingTallyID > 0 {
		tally, loadErr := service.storage.LoadVotingTally(
			actor.Context(ctx), eventID, sessionID,
		)
		if loadErr != nil {
			return Draft{}, loadErr
		}
		value := votingTally(tally)
		result.VotingTally = &value
	}
	return result, nil
}

// resultsRejections is the single source for Results rejection codes in both
// directions, so a replayed receipt restores the same failure the original
// command produced.
var resultsRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrManageRequired, Code: "manage_results_required"},
		{Err: ErrRevisionConflict, Code: "results_revision"},
		{Err: ErrEventAwardsRevision, Code: "event_awards_revision"},
		{Err: store.ErrResultsPublicationRevision, Code: "publication_revision"},
		{Err: ErrCorrectionRevision, Code: "correction_revision"},
		{Err: ErrResultsPublicationRequired, Code: "publication_required"},
		{Err: ErrPrizegivingPreflightRequired, Code: "prizegiving_preflight_required"},
		{Err: store.ErrResultsPublicationTransition, Code: "publication_transition"},
		{Err: ErrCorrectionTransition, Code: "correction_transition"},
		{Err: ErrResultItemTransition, Code: "result_item_transition"},
		{Err: ErrResultsReleasePolicy, Code: "invalid_release_policy"},
		{Err: ErrCorrectionBase, Code: "correction_base_changed"},
		{Err: ErrResultsCorrection, Code: "invalid_correction"},
		{Err: ErrCompetitionPrizegivingAssignment, Code: "competition_prizegiving_assignment"},
		{Err: ErrCompetitionNotFound, Code: "competition_not_found"},
		{Err: ErrEventNotFound, Code: "event_not_found"},
		{Err: ErrPrizegivingSession, Code: "prizegiving_session_not_found"},
	},
}

func auditResultsRejections[T any](
	apply func(*store.CommandTx) (command.Execution[T], error),
) func(*store.CommandTx) (command.Execution[T], error) {
	return command.RejectKnown(resultsRejections, apply)
}

func decodeResultsCommandReceipt[T any](outcome string) (T, error) {
	return command.ReplayKnown[T](resultsRejections, outcome)
}

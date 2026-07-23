package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
)

var (
	// ErrEntryOrderRevision means an order command used stale state.
	ErrEntryOrderRevision = errors.New("competition Entry Order revision conflict")
	// ErrEntryOrderLocked means live presentation already froze the sequence.
	ErrEntryOrderLocked = errors.New("competition Entry Order is locked")
	// ErrEntryOrderPreviewStale means durable ordering inputs changed after preview.
	ErrEntryOrderPreviewStale = errors.New("competition Entry Order preview is stale")
	// ErrEntryOrderInvalid means a policy, seed, or manual sequence is invalid.
	ErrEntryOrderInvalid = errors.New("invalid Competition Entry Order")
	// ErrPresentedEntryDisposition means an Entry already began presentation.
	ErrPresentedEntryDisposition = errors.New("presented Entry disposition is immutable")
)

// EntryOrderPolicy selects the canonical Included Entry sequence.
type EntryOrderPolicy string

const (
	// EntryOrderSubmission preserves Entry creation order.
	EntryOrderSubmission EntryOrderPolicy = "SubmissionOrder"
	// EntryOrderManual uses the crew-selected Entry sequence.
	EntryOrderManual EntryOrderPolicy = "ManualOrder"
	// EntryOrderDeterministicShuffle derives a reproducible seeded sequence.
	EntryOrderDeterministicShuffle EntryOrderPolicy = "DeterministicShuffle"
)

// EntryOrderState is the current canonical or locked Included Entry sequence.
type EntryOrderState struct {
	Policy   EntryOrderPolicy `json:"policy"`
	Seed     int64            `json:"seed"`
	Revision int              `json:"revision"`
	EntryIDs []int            `json:"entry_ids"`
	Locked   bool             `json:"locked"`
}

// ConfigureEntryOrderParams changes the pre-live order policy.
type ConfigureEntryOrderParams struct {
	EventID, SessionID int
	ExpectedRevision   int
	Policy             EntryOrderPolicy
	Seed               int64
	ManualEntryIDs     []int
}

type entryOrderPolicyBehavior struct {
	configure func(*ent.SessionUpdateOne, ConfigureEntryOrderParams)
	valid     func(ConfigureEntryOrderParams, []*ent.CompetitionEntry) bool
	order     func(int64, []int, []*ent.CompetitionEntry) []int
}

// TakeEntrySlideParams records presentation and freezes the first exact preview.
type TakeEntrySlideParams struct {
	EventID, SessionID int
	ExpectedRevision   int
	PreviewFingerprint string
	EntryID            int
	Now                time.Time
}

// LoadCompetitionEntryOrder returns a reproducible preview and its fingerprint.
func (installation *SQLite) LoadCompetitionEntryOrder(
	ctx context.Context,
	eventID, sessionID int,
) (EntryOrderState, string, error) {
	_, found, err := loadCompetitionConfiguration(
		ctx, installation.client.Session, installation.client.Event, eventID, sessionID,
	)
	if err != nil {
		return EntryOrderState{}, "", err
	}
	entries, err := installation.client.CompetitionEntry.Query().
		Where(
			competitionentry.CompetitionSessionIDEQ(sessionID),
			competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return EntryOrderState{}, "", opaqueError("load Competition Entry Order", err)
	}
	return competitionEntryOrder(found, entries)
}

// ConfigureCompetitionEntryOrder changes and previews the pre-live sequence.
func (transaction *CommandTx) ConfigureCompetitionEntryOrder(
	ctx context.Context,
	params ConfigureEntryOrderParams,
) (EntryOrderState, error) {
	_, found, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return EntryOrderState{}, err
	}
	if found.EntryOrderRevision != params.ExpectedRevision {
		return EntryOrderState{}, ErrEntryOrderRevision
	}
	if found.Lifecycle != session.LifecycleScheduled || !found.EntryOrderLockedAt.IsZero() {
		return EntryOrderState{}, ErrEntryOrderLocked
	}
	entries, err := transaction.includedCompetitionEntries(ctx, params.SessionID)
	if err != nil {
		return EntryOrderState{}, err
	}
	behavior, exists := entryOrderBehavior(params.Policy)
	if !exists || !behavior.valid(params, entries) {
		return EntryOrderState{}, ErrEntryOrderInvalid
	}
	update := found.Update().
		SetEntryOrderPolicy(session.EntryOrderPolicy(params.Policy)).
		AddEntryOrderRevision(1)
	behavior.configure(update, params)
	updated, err := update.Save(ctx)
	if err != nil {
		return EntryOrderState{}, opaqueError("configure Competition Entry Order", err)
	}
	state, _, err := competitionEntryOrder(updated, entries)
	return state, err
}

// TakeCompetitionEntrySlide records presentation and freezes the first exact preview.
func (transaction *CommandTx) TakeCompetitionEntrySlide(
	ctx context.Context,
	params TakeEntrySlideParams,
) (EntryOrderState, error) {
	_, found, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return EntryOrderState{}, err
	}
	if found.EntryOrderRevision != params.ExpectedRevision {
		return EntryOrderState{}, ErrEntryOrderRevision
	}
	if found.Lifecycle != session.LifecycleLive {
		return EntryOrderState{}, ErrEntryOrderLocked
	}
	entries, err := transaction.includedCompetitionEntries(ctx, params.SessionID)
	if err != nil {
		return EntryOrderState{}, err
	}
	preview, fingerprint, err := competitionEntryOrder(found, entries)
	if err != nil {
		return EntryOrderState{}, err
	}
	if fingerprint != params.PreviewFingerprint {
		return EntryOrderState{}, ErrEntryOrderPreviewStale
	}
	if !slices.Contains(preview.EntryIDs, params.EntryID) {
		return EntryOrderState{}, ErrEntryOrderInvalid
	}
	updated := found
	if found.EntryOrderLockedAt.IsZero() {
		updated, err = found.Update().
			SetLockedEntryOrderIds(slices.Clone(preview.EntryIDs)).
			SetEntryOrderLockedAt(params.Now).
			AddEntryOrderRevision(1).
			Save(ctx)
		if err != nil {
			return EntryOrderState{}, opaqueError("lock Competition Entry Order", err)
		}
		run, runErr := transaction.transaction.SessionRun.Query().
			Where(
				sessionrun.SessionIDEQ(params.SessionID),
				sessionrun.ActualEndIsNil(),
			).
			Order(ent.Desc(sessionrun.FieldID)).
			First(ctx)
		if runErr != nil {
			return EntryOrderState{}, opaqueError("load active Competition Run", runErr)
		}
		if _, runErr = run.Update().
			SetLockedEntryOrderIds(slices.Clone(preview.EntryIDs)).
			Save(ctx); runErr != nil {
			return EntryOrderState{}, opaqueError("capture Run Snapshot Entry Order", runErr)
		}
	}
	presented, err := transaction.competitionEntry(
		ctx, params.EventID, params.SessionID, params.EntryID,
	)
	if err != nil {
		return EntryOrderState{}, err
	}
	if presented.FirstPresentedAt.IsZero() {
		if _, err = presented.Update().
			SetFirstPresentedAt(params.Now).
			SetPresentationStatus(competitionentry.PresentationStatusPresented).
			AddRevision(1).
			Save(ctx); err != nil {
			return EntryOrderState{}, opaqueError("mark presented Entry", err)
		}
	}
	if !presented.FirstPresentedAt.IsZero() &&
		presented.PresentationStatus != competitionentry.PresentationStatusPresented {
		if _, err = presented.Update().
			SetPresentationStatus(competitionentry.PresentationStatusPresented).
			AddRevision(1).
			Save(ctx); err != nil {
			return EntryOrderState{}, opaqueError("mark presented Entry", err)
		}
	}
	state, _, err := competitionEntryOrder(updated, entries)
	return state, err
}

func (transaction *CommandTx) includedCompetitionEntries(
	ctx context.Context,
	sessionID int,
) ([]*ent.CompetitionEntry, error) {
	entries, err := transaction.transaction.CompetitionEntry.Query().
		Where(
			competitionentry.CompetitionSessionIDEQ(sessionID),
			competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("load Included Competition Entries", err)
	}
	return entries, nil
}

func sameEntryIDSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func competitionEntryOrder(
	found *ent.Session,
	entries []*ent.CompetitionEntry,
) (EntryOrderState, string, error) {
	state := EntryOrderState{
		Policy: EntryOrderPolicy(found.EntryOrderPolicy.String()),
		Seed:   found.EntryOrderSeed, Revision: found.EntryOrderRevision,
	}
	if len(found.LockedEntryOrderIds) > 0 || !found.EntryOrderLockedAt.IsZero() {
		state.EntryIDs = slices.Clone(found.LockedEntryOrderIds)
		state.Locked = true
	} else {
		state.EntryIDs = orderedEntryIDs(state.Policy, state.Seed, found.EntryOrderManualIds, entries)
	}
	fingerprint, err := entryOrderFingerprint(found.ID, state, entries)
	if err != nil {
		return EntryOrderState{}, "", err
	}
	return state, fingerprint, nil
}

func orderedEntryIDs(
	policy EntryOrderPolicy,
	seed int64,
	manual []int,
	entries []*ent.CompetitionEntry,
) []int {
	behavior, exists := entryOrderBehavior(policy)
	if !exists {
		behavior, _ = entryOrderBehavior(EntryOrderSubmission)
	}
	return behavior.order(seed, manual, entries)
}

func entryOrderBehavior(policy EntryOrderPolicy) (entryOrderPolicyBehavior, bool) {
	submissionOrder := func(_ int64, _ []int, entries []*ent.CompetitionEntry) []int {
		result := make([]int, 0, len(entries))
		for _, entry := range entries {
			result = append(result, entry.ID)
		}
		return result
	}
	switch policy {
	case EntryOrderSubmission:
		return entryOrderPolicyBehavior{
			configure: func(update *ent.SessionUpdateOne, _ ConfigureEntryOrderParams) {
				update.ClearEntryOrderManualIds()
			},
			valid: func(params ConfigureEntryOrderParams, _ []*ent.CompetitionEntry) bool {
				return len(params.ManualEntryIDs) == 0
			},
			order: submissionOrder,
		}, true
	case EntryOrderManual:
		return entryOrderPolicyBehavior{
			configure: func(update *ent.SessionUpdateOne, params ConfigureEntryOrderParams) {
				update.SetEntryOrderManualIds(slices.Clone(params.ManualEntryIDs))
			},
			valid: func(params ConfigureEntryOrderParams, entries []*ent.CompetitionEntry) bool {
				included := make([]int, len(entries))
				for index, entry := range entries {
					included[index] = entry.ID
				}
				return sameEntryIDSet(params.ManualEntryIDs, included)
			},
			order: func(_ int64, manual []int, entries []*ent.CompetitionEntry) []int {
				submission := submissionOrder(0, nil, entries)
				included := make(map[int]struct{}, len(entries))
				for _, entryID := range submission {
					included[entryID] = struct{}{}
				}
				result := make([]int, 0, len(entries))
				seen := make(map[int]struct{}, len(entries))
				for _, entryID := range manual {
					if _, exists := included[entryID]; !exists {
						continue
					}
					if _, duplicate := seen[entryID]; duplicate {
						continue
					}
					result = append(result, entryID)
					seen[entryID] = struct{}{}
				}
				for _, entryID := range submission {
					if _, exists := seen[entryID]; !exists {
						result = append(result, entryID)
					}
				}
				return result
			},
		}, true
	case EntryOrderDeterministicShuffle:
		return entryOrderPolicyBehavior{
			configure: func(update *ent.SessionUpdateOne, params ConfigureEntryOrderParams) {
				update.SetEntryOrderSeed(params.Seed).ClearEntryOrderManualIds()
			},
			valid: func(params ConfigureEntryOrderParams, _ []*ent.CompetitionEntry) bool {
				return params.Seed > 0 && len(params.ManualEntryIDs) == 0
			},
			order: func(seed int64, _ []int, entries []*ent.CompetitionEntry) []int {
				result := submissionOrder(0, nil, entries)
				slices.SortFunc(result, func(left, right int) int {
					leftKey := entryShuffleKey(seed, left)
					rightKey := entryShuffleKey(seed, right)
					return bytes.Compare(leftKey[:], rightKey[:])
				})
				return result
			},
		}, true
	default:
		return entryOrderPolicyBehavior{}, false
	}
}

func entryShuffleKey(seed int64, entryID int) [sha256.Size]byte {
	input := strconv.FormatInt(seed, 10) + ":" + strconv.Itoa(entryID)
	return sha256.Sum256([]byte(input))
}

func entryOrderFingerprint(
	sessionID int,
	state EntryOrderState,
	entries []*ent.CompetitionEntry,
) (string, error) {
	revisions := make([]int, 0, len(entries))
	for _, entry := range entries {
		revisions = append(revisions, entry.Revision)
	}
	encoded, err := json.Marshal(struct {
		SessionID int             `json:"session_id"`
		State     EntryOrderState `json:"state"`
		Revisions []int           `json:"revisions"`
	}{SessionID: sessionID, State: state, Revisions: revisions})
	if err != nil {
		return "", errors.New("encode Competition Entry Order fingerprint")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

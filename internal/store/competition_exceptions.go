package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
)

var (
	// ErrCompetitionEntryDefer means an Entry cannot be deferred from its current position.
	ErrCompetitionEntryDefer = errors.New("competition Entry cannot be deferred")
	// ErrCompetitionResolution means an Entry cannot take the requested final resolution.
	ErrCompetitionResolution = errors.New("invalid Competition Entry resolution")
	// ErrCompetitionCrewReason means an exception command omitted its durable Crew Reason.
	ErrCompetitionCrewReason = errors.New("competition Entry Crew Reason is required")
	// ErrDeferredEntriesConfirmation means Ending requires confirmation of the current deferred set.
	ErrDeferredEntriesConfirmation = errors.New("deferred Competition Entries require warned confirmation")
	// ErrDeferredEntriesPreviewStale means deferred Entries changed after the Ending preflight.
	ErrDeferredEntriesPreviewStale = errors.New("deferred Competition Entries preview is stale")
)

// DeferCompetitionEntryParams advances past one exact unpresented canonical Entry.
type DeferCompetitionEntryParams struct {
	EventID, SessionID, EntryID int
	ExpectedEntryRevision       int
	ExpectedProgramRevision     int
	Now                         time.Time
}

// ResolveCompetitionEntryParams records one final judging and visibility decision.
type ResolveCompetitionEntryParams struct {
	EventID, SessionID, EntryID   int
	ExpectedRevision              int
	ResultDisposition             string
	CrewReason                    string
	PublicDisqualificationMessage string
	Now                           time.Time
}

// TechnicalFailureParams records a cause without deciding judging or release.
type TechnicalFailureParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Reason                      string
}

// SetCompetitionEntryReleaseHoldParams applies or lifts a Producer hold.
type SetCompetitionEntryReleaseHoldParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Hold                        bool
	CrewReason                  string
}

// CompetitionEndPreflight binds warned deferred Entries to current durable revisions.
type CompetitionEndPreflight struct {
	DeferredEntries      []CompetitionEntry
	Fingerprint          string
	RequiresConfirmation bool
}

// DeferCompetitionEntry advances the canonical cursor and appends one retry occurrence.
func (transaction *CommandTx) DeferCompetitionEntry(
	ctx context.Context,
	params DeferCompetitionEntryParams,
) (CompetitionEntry, error) {
	state, err := transaction.LoadProgramChannel(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if state.Revision != params.ExpectedProgramRevision {
		return CompetitionEntry{}, ErrProgramRevision
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedEntryRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	_, index, found := findProgramItem(state.Items, ProgramItem{
		Kind: ProgramItemEntry, EntryID: params.EntryID,
	})
	if !found || index != stateCursor(state)+1 ||
		entry.Disposition != competitionentry.DispositionIncluded ||
		entry.PresentationStatus != competitionentry.PresentationStatusScheduled ||
		!entry.FirstPresentedAt.IsZero() {
		return competitionEntry(entry), ErrCompetitionEntryDefer
	}
	latestDeferred, err := transaction.transaction.CompetitionEntry.Query().
		Where(
			competitionentry.CompetitionSessionIDEQ(params.SessionID),
			competitionentry.DeferredSequenceNotNil(),
		).
		Order(ent.Desc(competitionentry.FieldDeferredSequence)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return CompetitionEntry{}, opaqueError("load Competition defer sequence", err)
	}
	maxSequence := 0
	if latestDeferred != nil {
		maxSequence = latestDeferred.DeferredSequence
	}
	updated, err := entry.Update().
		SetPresentationStatus(competitionentry.PresentationStatusDeferred).
		SetDeferredSequence(maxSequence + 1).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("defer Competition Entry", err)
	}
	if _, err = transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(session.ProgramOutputRevisionEQ(params.ExpectedProgramRevision)).
		SetProgramCursor(index).
		AddProgramOutputRevision(1).
		Save(ctx); ent.IsNotFound(err) {
		return CompetitionEntry{}, ErrProgramRevision
	} else if err != nil {
		return CompetitionEntry{}, opaqueError("advance Program cursor after defer", err)
	}
	return competitionEntry(updated), nil
}

// RecordCompetitionTechnicalFailure records a bounded cause only.
func (transaction *CommandTx) RecordCompetitionTechnicalFailure(
	ctx context.Context,
	params TechnicalFailureParams,
) (CompetitionEntry, error) {
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	if strings.TrimSpace(params.Reason) == "" {
		return competitionEntry(entry), ErrCompetitionCrewReason
	}
	updated, err := entry.Update().
		SetTechnicalFailureReason(params.Reason).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("record Competition Technical Failure", err)
	}
	return competitionEntry(updated), nil
}

// ResolveCompetitionEntry records one final judging, visibility, and hold decision.
func (transaction *CommandTx) ResolveCompetitionEntry(
	ctx context.Context,
	params ResolveCompetitionEntryParams,
) (CompetitionEntry, error) {
	_, competition, err := transaction.competitionConfiguration(
		ctx, params.EventID, params.SessionID,
	)
	if err != nil {
		return CompetitionEntry{}, err
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	if strings.TrimSpace(params.CrewReason) == "" {
		return competitionEntry(entry), ErrCompetitionCrewReason
	}
	disposition := competitionentry.ResultDisposition(params.ResultDisposition)
	if disposition != competitionentry.ResultDispositionEligible &&
		disposition != competitionentry.ResultDispositionDisqualified &&
		disposition != competitionentry.ResultDispositionWithheld {
		return competitionEntry(entry), ErrCompetitionResolution
	}
	if disposition == competitionentry.ResultDispositionWithheld &&
		entry.PresentationStatus != competitionentry.PresentationStatusNotPresented {
		return competitionEntry(entry), ErrCompetitionResolution
	}
	if disposition == competitionentry.ResultDispositionEligible &&
		!entry.ResolutionRequired {
		return competitionEntry(entry), ErrCompetitionResolution
	}
	if disposition == competitionentry.ResultDispositionDisqualified &&
		entry.Disposition != competitionentry.DispositionIncluded {
		return competitionEntry(entry), ErrCompetitionResolution
	}
	if disposition == competitionentry.ResultDispositionDisqualified {
		resolvingNotPresented := entry.PresentationStatus ==
			competitionentry.PresentationStatusNotPresented && entry.ResolutionRequired
		lockedIncluded := !competition.EntryOrderLockedAt.IsZero() &&
			slices.Contains(competition.LockedEntryOrderIds, entry.ID)
		if !resolvingNotPresented && !lockedIncluded {
			return competitionEntry(entry), ErrCompetitionResolution
		}
	}
	update := entry.Update().
		SetResultDisposition(disposition).
		SetResolutionCrewReason(params.CrewReason).
		SetResolutionRequired(false).
		AddRevision(1)
	switch disposition {
	case competitionentry.ResultDispositionDisqualified:
		update.
			SetPublicDisqualificationMessage(params.PublicDisqualificationMessage).
			SetReleaseHold(true)
	case competitionentry.ResultDispositionWithheld:
		update.
			ClearPublicDisqualificationMessage().
			SetReleaseHold(true)
	default:
		update.
			ClearPublicDisqualificationMessage().
			SetReleaseHold(false)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("resolve Competition Entry", err)
	}
	if err := transaction.SupersedeCompetitionResultsDraft(
		ctx, params.EventID, params.SessionID, params.Now,
	); err != nil {
		return CompetitionEntry{}, err
	}
	return competitionEntry(updated), nil
}

// SetCompetitionEntryReleaseHold changes only the reversible release gate.
func (transaction *CommandTx) SetCompetitionEntryReleaseHold(
	ctx context.Context,
	params SetCompetitionEntryReleaseHoldParams,
) (CompetitionEntry, error) {
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	if entry.Disposition != competitionentry.DispositionIncluded ||
		strings.TrimSpace(params.CrewReason) == "" {
		return competitionEntry(entry), ErrCompetitionResolution
	}
	updated, err := entry.Update().
		SetReleaseHold(params.Hold).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("set Competition Entry Release Hold", err)
	}
	return competitionEntry(updated), nil
}

// PreflightCompetitionEnd lists the exact deferred Entries requiring confirmation.
func (installation *SQLite) PreflightCompetitionEnd(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionEndPreflight, error) {
	_, found, err := loadCompetitionConfiguration(
		ctx, installation.client.Session, installation.client.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionEndPreflight{}, err
	}
	entries, err := installation.client.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.CompetitionSessionIDEQ(sessionID),
			competitionentry.PresentationStatusEQ(competitionentry.PresentationStatusDeferred),
		).
		Order(ent.Asc(competitionentry.FieldDeferredSequence), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return CompetitionEndPreflight{}, opaqueError("load deferred Competition Entries", err)
	}
	return competitionEndPreflight(found.LiveStateRevision, entries)
}

func (transaction *CommandTx) confirmCompetitionEnd(
	ctx context.Context,
	eventID, sessionID int,
	confirmed bool,
	fingerprint string,
	now time.Time,
) error {
	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if err != nil {
		return opaqueError("load Competition End state", err)
	}
	entries, err := transaction.transaction.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.CompetitionSessionIDEQ(sessionID),
			competitionentry.PresentationStatusEQ(competitionentry.PresentationStatusDeferred),
		).
		Order(ent.Asc(competitionentry.FieldDeferredSequence), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return opaqueError("load deferred Competition Entries", err)
	}
	preflight, err := competitionEndPreflight(found.LiveStateRevision, entries)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if !confirmed || fingerprint == "" {
		return ErrDeferredEntriesConfirmation
	}
	if fingerprint != preflight.Fingerprint {
		return ErrDeferredEntriesPreviewStale
	}
	for _, entry := range entries {
		if _, err = entry.Update().
			SetPresentationStatus(competitionentry.PresentationStatusNotPresented).
			SetResolutionRequired(true).
			AddRevision(1).
			Save(ctx); err != nil {
			return opaqueError("mark Competition Entry Not Presented", err)
		}
	}
	return transaction.SupersedeCompetitionResultsDraft(ctx, eventID, sessionID, now)
}

func competitionEndPreflight(
	liveRevision int,
	entries []*ent.CompetitionEntry,
) (CompetitionEndPreflight, error) {
	type fingerprintEntry struct {
		ID       int `json:"id"`
		Revision int `json:"revision"`
	}
	input := struct {
		LiveRevision int                `json:"live_revision"`
		Entries      []fingerprintEntry `json:"entries"`
	}{LiveRevision: liveRevision, Entries: make([]fingerprintEntry, 0, len(entries))}
	result := CompetitionEndPreflight{
		DeferredEntries:      make([]CompetitionEntry, 0, len(entries)),
		RequiresConfirmation: len(entries) > 0,
	}
	for _, entry := range entries {
		input.Entries = append(input.Entries, fingerprintEntry{ID: entry.ID, Revision: entry.Revision})
		result.DeferredEntries = append(result.DeferredEntries, competitionEntry(entry))
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return CompetitionEndPreflight{}, errors.New("encode Competition End fingerprint")
	}
	sum := sha256.Sum256(encoded)
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, nil
}

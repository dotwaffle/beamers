package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

var (
	// ErrCompetitionNotFound means no published Competition matched the stable IDs.
	ErrCompetitionNotFound = errors.New("competition not found")
	// ErrCompetitionSubmissionClosed means the fixed Deadline has arrived.
	ErrCompetitionSubmissionClosed = errors.New("competition submissions are closed")
	// ErrCompetitionEntryNotFound means no retained Entry matched the stable IDs.
	ErrCompetitionEntryNotFound = errors.New("competition entry not found")
	// ErrCompetitionEntryRevision means an Entry command used a stale revision.
	ErrCompetitionEntryRevision = errors.New("competition entry revision conflict")
	// ErrLiveDispositionConfirmation means a live change lacked explicit confirmation.
	ErrLiveDispositionConfirmation = errors.New("live Competition disposition change requires confirmation")
)

// CompetitionEntry is one retained Competition submission.
type CompetitionEntry struct {
	ID                   int       `json:"id"`
	CompetitionSessionID int       `json:"competition_session_id"`
	Name                 string    `json:"name"`
	PublicDetails        string    `json:"public_details,omitempty"`
	CrewNotes            string    `json:"crew_notes,omitempty"`
	Disposition          string    `json:"disposition"`
	Revision             int       `json:"revision"`
	CreatedAt            time.Time `json:"created_at"`
}

// CompetitionState is the current fixed configuration and retained Entries.
type CompetitionState struct {
	EventID                     int
	SessionID                   int
	SubmissionDeadline          time.Time
	EffectiveDefaultDisposition string
	Entries                     []CompetitionEntry
}

// CreateCompetitionEntryParams contains one new Entry.
type CreateCompetitionEntryParams struct {
	EventID, SessionID  int
	Name, PublicDetails string
	CrewNotes           string
	Now                 time.Time
}

// UpdateCompetitionEntryParams contains one optimistic Entry content change.
type UpdateCompetitionEntryParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Name, PublicDetails         string
	CrewNotes                   string
	Now                         time.Time
}

// ChangeCompetitionEntryDispositionParams contains one participation change.
type ChangeCompetitionEntryDispositionParams struct {
	EventID, SessionID, EntryID int
	ExpectedRevision            int
	Disposition                 string
	ConfirmedLive               bool
	Now                         time.Time
}

// LoadCompetition returns current published Competition configuration and Entries.
func (installation *SQLite) LoadCompetition(ctx context.Context, eventID, sessionID int) (CompetitionState, error) {
	state, _, err := loadCompetitionConfiguration(
		ctx, installation.client.Session, installation.client.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionState{}, err
	}
	entries, err := installation.client.CompetitionEntry.Query().
		Where(competitionentry.CompetitionSessionIDEQ(sessionID)).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return CompetitionState{}, opaqueError("load Competition Entries", err)
	}
	for _, entry := range entries {
		state.Entries = append(state.Entries, competitionEntry(entry))
	}
	return state, nil
}

// CreateCompetitionEntry creates one Entry using the effective default disposition.
func (transaction *CommandTx) CreateCompetitionEntry(ctx context.Context, params CreateCompetitionEntryParams) (CompetitionEntry, error) {
	state, _, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	created, err := transaction.transaction.CompetitionEntry.Create().
		SetEventID(params.EventID).
		SetCompetitionSessionID(params.SessionID).
		SetName(params.Name).
		SetPublicDetails(params.PublicDetails).
		SetCrewNotes(params.CrewNotes).
		SetDisposition(competitionentry.Disposition(state.EffectiveDefaultDisposition)).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("create Competition Entry", err)
	}
	return competitionEntry(created), nil
}

// UpdateCompetitionEntry changes retained Entry content before the Deadline.
func (transaction *CommandTx) UpdateCompetitionEntry(ctx context.Context, params UpdateCompetitionEntryParams) (CompetitionEntry, error) {
	state, _, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	updated, err := transaction.transaction.CompetitionEntry.UpdateOne(entry).
		SetName(params.Name).
		SetPublicDetails(params.PublicDetails).
		SetCrewNotes(params.CrewNotes).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("update Competition Entry", err)
	}
	return competitionEntry(updated), nil
}

// ChangeCompetitionEntryDisposition changes participation with a live override.
func (transaction *CommandTx) ChangeCompetitionEntryDisposition(
	ctx context.Context,
	params ChangeCompetitionEntryDispositionParams,
) (CompetitionEntry, error) {
	state, found, err := transaction.competitionConfiguration(ctx, params.EventID, params.SessionID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if !params.Now.Before(state.SubmissionDeadline) {
		return CompetitionEntry{}, ErrCompetitionSubmissionClosed
	}
	presentationBegan, err := found.QueryRuns().Exist(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("load Competition presentation history", err)
	}
	if presentationBegan && !params.ConfirmedLive {
		return CompetitionEntry{}, ErrLiveDispositionConfirmation
	}
	entry, err := transaction.competitionEntry(ctx, params.EventID, params.SessionID, params.EntryID)
	if err != nil {
		return CompetitionEntry{}, err
	}
	if entry.Revision != params.ExpectedRevision {
		return competitionEntry(entry), ErrCompetitionEntryRevision
	}
	updated, err := transaction.transaction.CompetitionEntry.UpdateOne(entry).
		SetDisposition(competitionentry.Disposition(params.Disposition)).
		AddRevision(1).
		Save(ctx)
	if err != nil {
		return CompetitionEntry{}, opaqueError("change Competition Entry disposition", err)
	}
	return competitionEntry(updated), nil
}

func (transaction *CommandTx) competitionConfiguration(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionState, *ent.Session, error) {
	state, found, err := loadCompetitionConfiguration(
		ctx, transaction.transaction.Session, transaction.transaction.Event, eventID, sessionID,
	)
	if err != nil {
		return CompetitionState{}, nil, err
	}
	return state, found, nil
}

func loadCompetitionConfiguration(
	ctx context.Context,
	sessions *ent.SessionClient,
	events *ent.EventClient,
	eventID, sessionID int,
) (CompetitionState, *ent.Session, error) {
	found, err := sessions.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return CompetitionState{}, nil, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition", err)
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if ent.IsNotFound(err) || (err == nil && version.Type != sessionpublishedversion.TypeCompetition) {
		return CompetitionState{}, nil, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition version", err)
	}
	event, err := events.Get(ctx, eventID)
	if err != nil {
		return CompetitionState{}, nil, opaqueError("load Competition Event", err)
	}
	state := competitionState(
		eventID, sessionID, version.SubmissionDeadline, string(version.EntryDefaultDisposition),
		string(event.EntryDefaultDisposition), nil,
	)
	return state, found, nil
}

func (transaction *CommandTx) competitionEntry(
	ctx context.Context,
	eventID, sessionID, entryID int,
) (*ent.CompetitionEntry, error) {
	entry, err := transaction.transaction.CompetitionEntry.Query().Where(
		competitionentry.IDEQ(entryID),
		competitionentry.EventIDEQ(eventID),
		competitionentry.CompetitionSessionIDEQ(sessionID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrCompetitionEntryNotFound
	}
	if err != nil {
		return nil, opaqueError("load Competition Entry", err)
	}
	return entry, nil
}

func competitionState(
	eventID, sessionID int,
	deadline time.Time,
	override, eventDefault string,
	entries []*ent.CompetitionEntry,
) CompetitionState {
	effective := override
	if effective == "" {
		effective = eventDefault
	}
	state := CompetitionState{
		EventID: eventID, SessionID: sessionID, SubmissionDeadline: deadline,
		EffectiveDefaultDisposition: effective,
		Entries:                     make([]CompetitionEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		state.Entries = append(state.Entries, competitionEntry(entry))
	}
	return state
}

func competitionEntry(entry *ent.CompetitionEntry) CompetitionEntry {
	return CompetitionEntry{
		ID: entry.ID, CompetitionSessionID: entry.CompetitionSessionID,
		Name: entry.Name, PublicDetails: entry.PublicDetails, CrewNotes: entry.CrewNotes,
		Disposition: string(entry.Disposition), Revision: entry.Revision, CreatedAt: entry.CreatedAt,
	}
}

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
	// ErrProgramRevision means Program Output changed after it was observed.
	ErrProgramRevision = errors.New("program Channel revision conflict")
	// ErrProgramItem means Preview selected no current Competition Program Item.
	ErrProgramItem = errors.New("invalid Competition Program Item")
)

// ProgramItemKind identifies one built-in Competition Slide or Standby.
type ProgramItemKind string

const (
	// ProgramItemStandby identifies branded idle output.
	ProgramItemStandby ProgramItemKind = "Standby"
	// ProgramItemUpcoming identifies the pre-start Competition slide.
	ProgramItemUpcoming ProgramItemKind = "Upcoming"
	// ProgramItemStarting identifies the Competition opening slide.
	ProgramItemStarting ProgramItemKind = "Starting"
	// ProgramItemEntry identifies one Included Entry slide.
	ProgramItemEntry ProgramItemKind = "Entry"
	// ProgramItemEnding identifies the Competition closing slide.
	ProgramItemEnding ProgramItemKind = "Ending"
)

// ProgramItem is one exact selectable Competition presentation state.
type ProgramItem struct {
	Kind    ProgramItemKind `json:"kind"`
	EntryID int             `json:"entry_id,omitempty"`
	Title   string          `json:"title"`
}

// ProgramChannelState is durable Program Output plus its canonical context.
type ProgramChannelState struct {
	EventID     int           `json:"event_id"`
	SessionID   int           `json:"session_id"`
	Name        string        `json:"name"`
	Revision    int           `json:"revision"`
	LocationIDs []int         `json:"location_ids"`
	Items       []ProgramItem `json:"items"`
	Previous    ProgramItem   `json:"previous"`
	Current     ProgramItem   `json:"current"`
	Next        ProgramItem   `json:"next"`
	Output      ProgramItem   `json:"output"`
	TakenAt     time.Time     `json:"taken_at,omitzero"`
}

// TakeProgramItemParams commits one exact Preview as Program Output.
type TakeProgramItemParams struct {
	EventID, SessionID         int
	ExpectedRevision           int
	Item                       ProgramItem
	ExpectedEntryOrderRevision int
	EntryOrderFingerprint      string
	Now                        time.Time
}

// LoadProgramChannel returns one authorized Competition Program Channel.
func (installation *SQLite) LoadProgramChannel(
	ctx context.Context,
	eventID, sessionID int,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, installation.client.Session, installation.client.CompetitionEntry,
		installation.client.Event, eventID, sessionID,
	)
}

// LoadProgramChannel returns transaction-consistent Program Channel state.
func (transaction *CommandTx) LoadProgramChannel(
	ctx context.Context,
	eventID, sessionID int,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, transaction.transaction.Session, transaction.transaction.CompetitionEntry,
		transaction.transaction.Event, eventID, sessionID,
	)
}

// TakeProgramItem atomically commits Program Output and any first Entry lock.
func (transaction *CommandTx) TakeProgramItem(
	ctx context.Context,
	params TakeProgramItemParams,
) (ProgramChannelState, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return ProgramChannelState{}, err
	}
	state, err := loadProgramChannel(
		ctx, transaction.transaction.Session, transaction.transaction.CompetitionEntry,
		transaction.transaction.Event, params.EventID, params.SessionID,
	)
	if err != nil {
		return ProgramChannelState{}, err
	}
	if state.Revision != params.ExpectedRevision {
		return state, ErrProgramRevision
	}
	selected, index, ok := findProgramItem(state.Items, params.Item)
	if !ok {
		return ProgramChannelState{}, ErrProgramItem
	}
	if selected.Kind == ProgramItemEntry {
		if _, err = transaction.TakeCompetitionEntrySlide(ctx, TakeEntrySlideParams{
			EventID: params.EventID, SessionID: params.SessionID,
			ExpectedRevision:   params.ExpectedEntryOrderRevision,
			PreviewFingerprint: params.EntryOrderFingerprint,
			EntryID:            selected.EntryID, Now: params.Now,
		}); err != nil {
			return ProgramChannelState{}, err
		}
	}
	cursor := stateCursor(state)
	if index == cursor+1 {
		cursor = index
	}
	update := transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(session.ProgramOutputRevisionEQ(params.ExpectedRevision)).
		SetProgramOutputKind(session.ProgramOutputKind(selected.Kind)).
		SetProgramOutputRevision(params.ExpectedRevision + 1).
		SetProgramCursor(cursor).
		SetProgramOutputTakenAt(params.Now)
	if selected.Kind == ProgramItemEntry {
		update.SetProgramOutputEntryID(selected.EntryID)
	} else {
		update.ClearProgramOutputEntryID()
	}
	_, err = update.Save(ctx)
	if ent.IsNotFound(err) {
		return state, ErrProgramRevision
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError("commit Program Output", err)
	}
	return loadProgramChannel(
		ctx, transaction.transaction.Session, transaction.transaction.CompetitionEntry,
		transaction.transaction.Event, params.EventID, params.SessionID,
	)
}

func loadProgramChannel(
	ctx context.Context,
	sessions *ent.SessionClient,
	entriesClient *ent.CompetitionEntryClient,
	events *ent.EventClient,
	eventID, sessionID int,
) (ProgramChannelState, error) {
	_, found, err := loadCompetitionConfiguration(ctx, sessions, events, eventID, sessionID)
	if err != nil {
		return ProgramChannelState{}, err
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Competition", err)
	}
	entries, err := entriesClient.Query().
		Where(
			competitionentry.CompetitionSessionIDEQ(sessionID),
			competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Entries", err)
	}
	order, _, err := competitionEntryOrder(found, entries)
	if err != nil {
		return ProgramChannelState{}, err
	}
	items := competitionProgramItems(version.Title, order.EntryIDs, entries)
	locationIDs, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Locations", err)
	}
	state := ProgramChannelState{
		EventID: eventID, SessionID: sessionID, Name: version.Title,
		Revision: found.ProgramOutputRevision, LocationIDs: locationIDs, Items: items,
		Output: ProgramItem{
			Kind:    ProgramItemKind(found.ProgramOutputKind.String()),
			EntryID: valueOrZero(found.ProgramOutputEntryID),
		},
		TakenAt: found.ProgramOutputTakenAt,
	}
	state.Output.Title = programItemTitle(state.Output, version.Title, entries)
	setProgramContext(&state, found.ProgramCursor)
	return state, nil
}

func competitionProgramItems(
	title string,
	entryOrder []int,
	entries []*ent.CompetitionEntry,
) []ProgramItem {
	items := []ProgramItem{
		{Kind: ProgramItemUpcoming, Title: title + " upcoming"},
		{Kind: ProgramItemStarting, Title: title + " starting"},
	}
	for _, entryID := range entryOrder {
		item := ProgramItem{Kind: ProgramItemEntry, EntryID: entryID}
		item.Title = programItemTitle(item, title, entries)
		items = append(items, item)
	}
	return append(items,
		ProgramItem{Kind: ProgramItemEnding, Title: title + " ending"},
		ProgramItem{Kind: ProgramItemStandby, Title: "Standby"},
	)
}

func programItemTitle(item ProgramItem, competitionTitle string, entries []*ent.CompetitionEntry) string {
	switch item.Kind {
	case ProgramItemStandby:
		return "Standby"
	case ProgramItemUpcoming:
		return competitionTitle + " upcoming"
	case ProgramItemStarting:
		return competitionTitle + " starting"
	case ProgramItemEnding:
		return competitionTitle + " ending"
	case ProgramItemEntry:
		for _, entry := range entries {
			if entry.ID == item.EntryID {
				return entry.Name
			}
		}
	}
	return ""
}

func findProgramItem(items []ProgramItem, wanted ProgramItem) (ProgramItem, int, bool) {
	for index, item := range items {
		if item.Kind == wanted.Kind && item.EntryID == wanted.EntryID {
			return item, index, true
		}
	}
	return ProgramItem{}, 0, false
}

func stateCursor(state ProgramChannelState) int {
	for index, item := range state.Items {
		if programItemEqual(item, state.Current) {
			return index
		}
	}
	return -1
}

func setProgramContext(state *ProgramChannelState, cursor int) {
	if cursor >= 0 && cursor < len(state.Items) {
		state.Current = state.Items[cursor]
	}
	if cursor > 0 && cursor-1 < len(state.Items) {
		state.Previous = state.Items[cursor-1]
	}
	if cursor+1 >= 0 && cursor+1 < len(state.Items) {
		state.Next = state.Items[cursor+1]
	}
}

func programItemEqual(left, right ProgramItem) bool {
	return left.Kind == right.Kind && left.EntryID == right.EntryID
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

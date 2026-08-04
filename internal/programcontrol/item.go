package programcontrol

import (
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

// ItemKind identifies one built-in Competition Slide, Result Slide, or Standby.
type ItemKind string

const (
	// ItemStandby identifies branded idle output.
	ItemStandby ItemKind = ItemKind(store.ProgramItemStandby)
	// ItemUpcoming identifies the pre-start Competition slide.
	ItemUpcoming ItemKind = ItemKind(store.ProgramItemUpcoming)
	// ItemStarting identifies the Competition opening slide.
	ItemStarting ItemKind = ItemKind(store.ProgramItemStarting)
	// ItemEntry identifies one Included Entry slide.
	ItemEntry ItemKind = ItemKind(store.ProgramItemEntry)
	// ItemEnding identifies the Competition closing slide.
	ItemEnding ItemKind = ItemKind(store.ProgramItemEnding)
	// ItemResult identifies one locked Prizegiving Result Item.
	ItemResult ItemKind = ItemKind(store.ProgramItemResult)
)

// ResultDetail is the locked Prizegiving Result an Item presents. Program
// Output and Display output present the same durable Result truth, so both
// projections name one type rather than each restating its fields.
type ResultDetail = store.ProgramResult

// ResultRef names the Result Item a ResultDetail presents.
type ResultRef = store.PrizegivingResultItemRef

// Item is one exact selectable Program Item as live control exposes it.
type Item struct {
	Kind    ItemKind
	EntryID int
	Title   string
	Retry   bool
	Result  *ResultDetail
}

// Channel is one Program Channel's durable output with its canonical context.
type Channel struct {
	EventID   int
	SessionID int
	Name      string
	Revision  int
	Items     []Item
	Previous  Item
	Current   Item
	Next      Item
	Output    Item
	TakenAt   time.Time
}

// exposedItem projects one stored Program Item for callers outside this package.
func exposedItem(found store.ProgramItem) Item {
	return Item{
		Kind: ItemKind(found.Kind), EntryID: found.EntryID,
		Title: found.Title, Retry: found.Retry, Result: found.Result,
	}
}

// storedItem returns the durable form of one exposed Program Item.
func storedItem(found Item) store.ProgramItem {
	return store.ProgramItem{
		Kind: store.ProgramItemKind(found.Kind), EntryID: found.EntryID,
		Title: found.Title, Retry: found.Retry, Result: found.Result,
	}
}

// exposedChannel projects one stored Program Channel for callers outside this
// package.
func exposedChannel(found store.ProgramChannelState) Channel {
	channel := Channel{
		EventID: found.EventID, SessionID: found.SessionID, Name: found.Name,
		Revision: found.Revision, Previous: exposedItem(found.Previous),
		Current: exposedItem(found.Current), Next: exposedItem(found.Next),
		Output: exposedItem(found.Output), TakenAt: found.TakenAt,
	}
	if len(found.Items) == 0 {
		return channel
	}
	channel.Items = make([]Item, 0, len(found.Items))
	for _, item := range found.Items {
		channel.Items = append(channel.Items, exposedItem(item))
	}
	return channel
}

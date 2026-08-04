package programcontrol

import (
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestExposedItemsRoundTripThroughTheirDurableForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		item Item
	}{
		{name: "standby", item: Item{Kind: ItemStandby}},
		{name: "upcoming", item: Item{Kind: ItemUpcoming, Title: "Wild Compo"}},
		{name: "starting", item: Item{Kind: ItemStarting}},
		{name: "entry", item: Item{Kind: ItemEntry, EntryID: 4, Title: "Aurora"}},
		{name: "retried entry", item: Item{Kind: ItemEntry, EntryID: 4, Retry: true}},
		{name: "ending", item: Item{Kind: ItemEnding}},
		{name: "result", item: exposedItem(resultItem("jury"))},
		{name: "no item at all", item: Item{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stored := storedItem(testCase.item)
			if string(stored.Kind) != string(testCase.item.Kind) {
				t.Fatalf("stored Kind = %q, want %q", stored.Kind, testCase.item.Kind)
			}
			if restored := exposedItem(stored); restored != testCase.item {
				t.Fatalf("round-tripped item = %+v, want %+v", restored, testCase.item)
			}
		})
	}
}

func TestExposedChannelProjectsEveryCanonicalPosition(t *testing.T) {
	t.Parallel()
	taken := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	stored := store.ProgramChannelState{
		EventID: 3, SessionID: 7, Name: "Main Stage", Revision: 9,
		LocationIDs: []int{2},
		Items: []store.ProgramItem{
			{Kind: store.ProgramItemUpcoming},
			{Kind: store.ProgramItemEntry, EntryID: 4, Title: "Aurora"},
		},
		Previous: store.ProgramItem{Kind: store.ProgramItemStarting},
		Current:  store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4},
		Next:     store.ProgramItem{Kind: store.ProgramItemEnding},
		Output:   store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4},
		TakenAt:  taken,
	}
	channel := exposedChannel(stored)
	if channel.EventID != 3 || channel.SessionID != 7 ||
		channel.Name != "Main Stage" || channel.Revision != 9 || channel.TakenAt != taken {
		t.Fatalf("exposed Channel = %+v", channel)
	}
	if len(channel.Items) != 2 || channel.Items[1].Title != "Aurora" {
		t.Fatalf("exposed Channel Items = %+v", channel.Items)
	}
	if channel.Previous.Kind != ItemStarting || channel.Current.Kind != ItemEntry ||
		channel.Next.Kind != ItemEnding || channel.Output.EntryID != 4 {
		t.Fatalf("exposed canonical positions = %+v", channel)
	}
	if empty := exposedChannel(store.ProgramChannelState{}); empty.Items != nil {
		t.Fatalf("exposed empty Channel Items = %+v", empty.Items)
	}
}

func TestStateFallsBackToTheCanonicalNextItem(t *testing.T) {
	t.Parallel()
	service := &Service{}
	channel := store.ProgramChannelState{
		Next: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 6},
	}
	unselected := service.state(channel, controlState{})
	if unselected.Preview.Kind != ItemEntry || unselected.Preview.EntryID != 6 {
		t.Fatalf("Preview without a selection = %+v", unselected.Preview)
	}
	if unselected.Owner != nil || unselected.HandoverRequester != nil {
		t.Fatalf("unowned Channel state = %+v", unselected)
	}
	selected := service.state(channel, controlState{
		revision:   2,
		owner:      Owner{AccountID: 1, Name: "Ada", Connected: true},
		hasOwner:   true,
		requester:  Owner{AccountID: 2, Name: "Grace"},
		hasRequest: true,
		preview:    store.ProgramItem{Kind: store.ProgramItemUpcoming},
	})
	if selected.Preview.Kind != ItemUpcoming || selected.ControlRevision != 2 {
		t.Fatalf("selected Preview = %+v", selected)
	}
	if selected.Owner == nil || selected.Owner.Name != "Ada" ||
		selected.HandoverRequester == nil || selected.HandoverRequester.Name != "Grace" {
		t.Fatalf("owned Channel state = %+v", selected)
	}
}

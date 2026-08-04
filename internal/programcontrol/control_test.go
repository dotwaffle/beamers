package programcontrol

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	testOwner      = auth.Account{ID: 1, Name: "Ada"}
	testChallenger = auth.Account{ID: 2, Name: "Grace"}
)

func testTime() time.Time {
	return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
}

func ownedBy(actor auth.Account, connected bool) controlState {
	return controlState{
		revision: 3,
		owner:    Owner{AccountID: actor.ID, Name: actor.Name, Connected: connected},
		hasOwner: true,
	}
}

func resultItem(awardKey string) store.ProgramItem {
	return resultItemWithStatus(awardKey, prizegivingvalue.StageTaken)
}

func resultItemWithStatus(
	awardKey string,
	status prizegivingvalue.StageStatus,
) store.ProgramItem {
	return store.ProgramItem{
		Kind: store.ProgramItemResult,
		Result: &store.ProgramResult{
			Ref: store.PrizegivingResultItemRef{
				Kind: prizegivingvalue.ItemEventAward, AwardKey: awardKey,
			},
			Status: status,
		},
	}
}

func TestTransitionControlGuardsEveryOwnershipAction(t *testing.T) {
	t.Parallel()
	nextItem := store.ProgramItem{Kind: store.ProgramItemUpcoming}
	channel := store.ProgramChannelState{Next: nextItem}
	cases := []struct {
		name    string
		control controlState
		actor   auth.Account
		input   ControlInput
		wantErr error
		expect  func(*testing.T, controlState)
	}{
		{
			name:  "claim an unowned Channel",
			actor: testOwner,
			input: ControlInput{Action: ControlClaim},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if !result.hasOwner || result.owner.AccountID != testOwner.ID || !result.owner.Connected {
					t.Fatalf("claimed owner = %+v", result.owner)
				}
				if result.preview != nextItem {
					t.Fatalf("claimed Preview = %+v, want the canonical Next item", result.preview)
				}
			},
		},
		{
			name:    "claim a Channel another Crew Member owns",
			control: ownedBy(testChallenger, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlClaim},
			wantErr: ErrControlOwned,
		},
		{
			name: "reclaim without discarding the current Preview",
			control: controlState{
				revision: 3, owner: Owner{AccountID: testOwner.ID, Connected: false}, hasOwner: true,
				preview: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 9},
			},
			actor: testOwner,
			input: ControlInput{Action: ControlClaim},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if result.preview.EntryID != 9 {
					t.Fatalf("reclaimed Preview = %+v, want the selected Entry", result.preview)
				}
				if !result.owner.Connected {
					t.Fatal("reclaimed owner is not connected")
				}
			},
		},
		{
			name:    "request handover of an unowned Channel",
			actor:   testOwner,
			input:   ControlInput{Action: ControlRequestHandover},
			wantErr: ErrHandoverUnavailable,
		},
		{
			name:    "request handover from oneself",
			control: ownedBy(testOwner, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlRequestHandover},
			wantErr: ErrHandoverUnavailable,
		},
		{
			name:    "request handover from the current owner",
			control: ownedBy(testOwner, true),
			actor:   testChallenger,
			input:   ControlInput{Action: ControlRequestHandover},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if !result.hasRequest || result.requester.AccountID != testChallenger.ID {
					t.Fatalf("handover requester = %+v", result.requester)
				}
				if result.owner.AccountID != testOwner.ID {
					t.Fatalf("requesting handover replaced the owner with %+v", result.owner)
				}
			},
		},
		{
			name:    "hand over without a pending request",
			control: ownedBy(testOwner, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlHandover},
			wantErr: ErrHandoverUnavailable,
		},
		{
			name: "hand over as a Crew Member who is not the owner",
			control: func() controlState {
				control := ownedBy(testOwner, true)
				control.requester = Owner{AccountID: testChallenger.ID, Connected: true}
				control.hasRequest = true
				return control
			}(),
			actor:   testChallenger,
			input:   ControlInput{Action: ControlHandover},
			wantErr: ErrHandoverUnavailable,
		},
		{
			name: "hand over to the pending requester",
			control: func() controlState {
				control := ownedBy(testOwner, true)
				control.requester = Owner{AccountID: testChallenger.ID, Name: "Grace", Connected: true}
				control.hasRequest = true
				return control
			}(),
			actor: testOwner,
			input: ControlInput{Action: ControlHandover},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if result.owner.AccountID != testChallenger.ID || result.hasRequest {
					t.Fatalf("handover result = %+v", result)
				}
			},
		},
		{
			name:    "take over without confirmation",
			control: ownedBy(testOwner, true),
			actor:   testChallenger,
			input:   ControlInput{Action: ControlTakeover},
			wantErr: ErrTakeoverConfirmation,
		},
		{
			name: "take over a Channel another Crew Member owns",
			control: func() controlState {
				control := ownedBy(testOwner, true)
				control.requester = Owner{AccountID: testChallenger.ID, Connected: true}
				control.hasRequest = true
				return control
			}(),
			actor: testChallenger,
			input: ControlInput{Action: ControlTakeover, Confirmed: true},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if result.owner.AccountID != testChallenger.ID || result.hasRequest {
					t.Fatalf("takeover result = %+v", result)
				}
				if result.preview != nextItem {
					t.Fatalf("takeover Preview = %+v, want the canonical Next item", result.preview)
				}
			},
		},
		{
			name:    "take over one's own disconnected Channel",
			control: ownedBy(testOwner, false),
			actor:   testOwner,
			input:   ControlInput{Action: ControlTakeover, Confirmed: true},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if result.owner.AccountID != testOwner.ID || !result.owner.Connected {
					t.Fatalf("self takeover owner = %+v", result.owner)
				}
			},
		},
		{
			name:    "disconnect without owning the Channel",
			control: ownedBy(testChallenger, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlDisconnect},
			wantErr: ErrControlOwnerRequired,
		},
		{
			name:    "disconnect as the owner",
			control: ownedBy(testOwner, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlDisconnect},
			expect: func(t *testing.T, result controlState) {
				t.Helper()
				if !result.hasOwner || result.owner.Connected {
					t.Fatalf("disconnected owner = %+v", result.owner)
				}
			},
		},
		{
			name:    "unknown action",
			control: ownedBy(testOwner, true),
			actor:   testOwner,
			input:   ControlInput{Action: ControlAction("Seize")},
			wantErr: ErrHandoverUnavailable,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result, err := transitionControl(controlTransitionInput{
				control: testCase.control, actor: testCase.actor,
				input: testCase.input, channel: channel,
			})
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("transition error = %v, want %v", err, testCase.wantErr)
				}
				if result != testCase.control {
					t.Fatalf("rejected transition changed control state to %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("transition error = %v, want none", err)
			}
			if result.revision != testCase.control.revision {
				t.Fatalf("transition changed the control revision to %d", result.revision)
			}
			testCase.expect(t, result)
		})
	}
}

func TestControlErrorCodeClassifiesEveryOwnershipFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		code rejectionCode
	}{
		{ErrControlOwned, rejectionControlOwned},
		{ErrControlOwnerRequired, rejectionControlOwnerRequired},
		{ErrTakeoverConfirmation, rejectionTakeoverConfirmation},
		{ErrHandoverUnavailable, rejectionHandoverUnavailable},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.code), func(t *testing.T) {
			t.Parallel()
			if code := controlErrorCode(testCase.err); code != string(testCase.code) {
				t.Fatalf("classify %v = %q, want %q", testCase.err, code, testCase.code)
			}
			restored := controlError(controlErrorCode(testCase.err))
			if !errors.Is(restored, testCase.err) {
				t.Fatalf("replayed %q = %v, want %v", testCase.code, restored, testCase.err)
			}
		})
	}
}

func TestProgramRejectionsSurviveReplay(t *testing.T) {
	t.Parallel()
	for _, rejection := range programRejections.Rejections {
		t.Run(rejection.Code, func(t *testing.T) {
			t.Parallel()
			classified, known := programRejections.Code(rejection.Err)
			if !known || classified != rejection.Code {
				t.Fatalf("classify %v = %q, %t, want %q", rejection.Err, classified, known, rejection.Code)
			}
			if restored := takeError(rejection.Code); !errors.Is(restored, rejection.Err) {
				t.Fatalf("replayed %q = %v, want %v", rejection.Code, restored, rejection.Err)
			}
		})
	}
	if restored := takeError("invented"); restored == nil ||
		restored.Error() != "program Take rejected" {
		t.Fatalf("replayed unknown Program rejection = %v", restored)
	}
}

func TestTransitionResultRequiresTheActedItemOnStage(t *testing.T) {
	t.Parallel()
	selected := resultItem("jury")
	other := resultItem("audience")
	cases := []struct {
		name    string
		action  ResultAction
		current store.ProgramChannelState
	}{
		{name: "reveal an item that is not Program Output", action: ResultReveal},
		{name: "replay an item that is not Program Output", action: ResultReplayReveal},
		{name: "complete an item that is not Program Output", action: ResultSkipToFinal},
		{
			name:    "skip an item that is not the canonical Next item",
			action:  ResultSkipFromStage,
			current: store.ProgramChannelState{Output: selected, Next: other},
		},
		{
			name:    "unknown action",
			action:  ResultAction("Rewind"),
			current: store.ProgramChannelState{Output: selected, Next: selected},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			current := testCase.current
			if current.Output.Kind == "" {
				current = store.ProgramChannelState{Output: other, Next: other}
			}
			state, presentation, err := transitionResult(resultTransitionInput{
				action: testCase.action, selected: selected, channel: current, now: testTime(),
			})
			if !errors.Is(err, ErrResultTransition) {
				t.Fatalf("transition error = %v, want %v", err, ErrResultTransition)
			}
			if state.Ref.AwardKey != "jury" {
				t.Fatalf("rejected transition returned state %+v", state)
			}
			if presentation != (store.PrizegivingPresentationRun{}) {
				t.Fatalf("rejected transition returned presentation %+v", presentation)
			}
		})
	}
}

func TestUnresolvedResultInOutputHoldsTheChannel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		item       store.ProgramItem
		unresolved bool
	}{
		{name: "an Entry slide", item: store.ProgramItem{Kind: store.ProgramItemEntry}},
		{
			name: "a Result without stage state",
			item: store.ProgramItem{Kind: store.ProgramItemResult},
		},
		{name: "a taken Result", item: resultItem("jury"), unresolved: true},
		{
			name: "a revealed Result",
			item: resultItemWithStatus("jury", prizegivingvalue.StageRevealed),
		},
		{
			name: "a skipped Result",
			item: resultItemWithStatus("jury", prizegivingvalue.StageSkipped),
		},
		{
			name:       "a revealing Result",
			item:       resultItemWithStatus("jury", prizegivingvalue.StageRevealing),
			unresolved: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if unresolved := unresolvedResultInOutput(testCase.item); unresolved != testCase.unresolved {
				t.Fatalf("unresolved Result = %t, want %t", unresolved, testCase.unresolved)
			}
		})
	}
}

func TestSelectItemMatchesCanonicalIdentityOnly(t *testing.T) {
	t.Parallel()
	items := []store.ProgramItem{
		{Kind: store.ProgramItemUpcoming},
		{Kind: store.ProgramItemEntry, EntryID: 4, Title: "Aurora"},
		{Kind: store.ProgramItemEntry, EntryID: 4, Title: "Aurora", Retry: true},
		resultItem("jury"),
	}
	cases := []struct {
		name   string
		wanted store.ProgramItem
		found  bool
		title  string
	}{
		{
			name:   "an Entry slide",
			wanted: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4},
			found:  true, title: "Aurora",
		},
		{
			name:   "a retried Entry slide",
			wanted: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4, Retry: true},
			found:  true, title: "Aurora",
		},
		{
			name:   "an Entry that is not in the catalog",
			wanted: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 5},
		},
		{name: "a Result Item", wanted: resultItem("jury"), found: true},
		{name: "a Result Item with another reference", wanted: resultItem("audience")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			found, ok := selectItem(items, testCase.wanted)
			if ok != testCase.found {
				t.Fatalf("select item = %t, want %t", ok, testCase.found)
			}
			if ok && found.Title != testCase.title {
				t.Fatalf("selected item = %+v, want title %q", found, testCase.title)
			}
		})
	}
}

func TestControlReceiptRestoresProcessLocalOwnership(t *testing.T) {
	t.Parallel()
	control := controlState{
		revision:   5,
		owner:      Owner{AccountID: 1, Name: "Ada", Connected: true},
		hasOwner:   true,
		requester:  Owner{AccountID: 2, Name: "Grace"},
		hasRequest: true,
		preview:    store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 8},
	}
	encoded, err := json.Marshal(controlReceiptFrom(control))
	if err != nil {
		t.Fatalf("encode control receipt: %v", err)
	}
	var receipt controlReceipt
	if err = store.DecodeCommandReceipt(string(encoded), &receipt); err != nil {
		t.Fatalf("decode control receipt: %v", err)
	}
	if restored := receipt.control(); restored != control {
		t.Fatalf("restored control state = %+v, want %+v", restored, control)
	}
	var empty controlReceipt
	if restored := empty.control(); restored.hasOwner || restored.hasRequest {
		t.Fatalf("restored empty control state = %+v", restored)
	}
}

func TestProgramItemIdentitySeparatesCommandPayloads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		item store.ProgramItem
	}{
		{name: "standby", item: store.ProgramItem{Kind: store.ProgramItemStandby}},
		{name: "entry", item: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4}},
		{
			name: "retried entry",
			item: store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4, Retry: true},
		},
		{name: "jury result", item: resultItem("jury")},
		{name: "audience result", item: resultItem("audience")},
	}
	seen := make(map[string]string, len(cases))
	for _, testCase := range cases {
		identity := strings.Join(programItemIdentity(testCase.item), "\x00")
		if previous, duplicate := seen[identity]; duplicate {
			t.Fatalf("Program Item identity for %q collides with %q", testCase.name, previous)
		}
		seen[identity] = testCase.name
	}
	titled := store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4, Title: "Aurora"}
	plain := store.ProgramItem{Kind: store.ProgramItemEntry, EntryID: 4}
	if !slices.Equal(programItemIdentity(titled), programItemIdentity(plain)) {
		t.Fatal("Program Item identity depends on a mutable Title")
	}
}

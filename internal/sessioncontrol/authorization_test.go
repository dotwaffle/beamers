package sessioncontrol_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestSessionCommandsRefuseOperatorOutsideSessionLaneScope covers the six
// live Session control commands whose imperative Lane scope guard PR #245's
// Capability Table now judges instead: Start, End, Cancel, Adjust Target,
// Pull Forward, and Correct Live Details. An Operator who holds the Event
// but not the target Session's Lane must be refused with the same evidence
// any other Capability Table refusal leaves: a Rejected Audit Entry naming
// "session_scope_required", and a Command Receipt that answers a retry
// without deciding it again.
//
// One Session drives the whole story so each refusal is exercised at the
// lifecycle stage its command requires, in the order that stage is reached:
// Cancel and Start are refused while Scheduled, then the Producer actually
// starts it; Adjust Target, Correct Live Details, and End are refused while
// Live, then the Producer actually ends it; Pull Forward is refused once
// Ended. None of the refusals mutate the Session, so the Producer's real
// transitions always see the state their predecessor left.
func TestSessionCommandsRefuseOperatorOutsideSessionLaneScope(t *testing.T) {
	storage, producer, eventID := openSessionControlTest(t)
	sessions := publishSessionControlRundown(t, storage, producer, eventID)
	activateSessionControlEvent(t, storage, producer, eventID)
	service := newSessionControlService(t, storage)

	outsider := producer
	outsider.Administrator = false
	outsider.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	outsider.EventScopes = nil

	primary := sessions["primary"]

	refuse := func(t *testing.T, action string, run func() error) {
		t.Helper()
		if err := run(); !errors.Is(err, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf("refused %s = %v, want %v", action, err, sessioncontrol.ErrSessionScopeRequired)
		}
		if err := run(); !errors.Is(err, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf("retry of refused %s = %v, want the recorded refusal %v", action, err, sessioncontrol.ErrSessionScopeRequired)
		}
		entries := rejectedSessionControlAudits(t, storage, producer, action)
		if len(entries) != 1 {
			t.Fatalf(
				"Rejected Audit Entries for %s = %d, want exactly one recorded refusal "+
					"whose retry is answered from its Command Receipt", action, len(entries),
			)
		}
		if entries[0].Reason != "session_scope_required" {
			t.Errorf("Rejected Audit Entry reason for %s = %q, want %q", action, entries[0].Reason, "session_scope_required")
		}
	}

	refuse(t, "CancelSession", func() error {
		_, err := service.Cancel(t.Context(), outsider, sessioncontrol.CancelInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-cancel-scheduled",
			Confirmed: true,
		})
		return err
	})
	refuse(t, "StartSession", func() error {
		_, err := service.Start(t.Context(), outsider, sessioncontrol.StartInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-start",
		})
		return err
	})
	started, err := service.Start(t.Context(), producer, sessioncontrol.StartInput{
		EventID: eventID, SessionID: primary, CommandID: "start-primary",
	})
	if err != nil {
		t.Fatalf("start primary Session: %v", err)
	}

	refuse(t, "AdjustTarget", func() error {
		_, err := service.AdjustTarget(t.Context(), outsider, sessioncontrol.AdjustTargetInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-adjust-target",
			Adjustment: sessioncontrol.TargetAdjustment{Duration: 5 * time.Minute},
		})
		return err
	})
	refuse(t, "CorrectLiveDetails", func() error {
		_, err := service.CorrectLiveDetails(t.Context(), outsider, sessioncontrol.CorrectLiveDetailsInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-correct-live-details",
			Confirmed: true, Title: "New Title", UpdateFields: []string{"title"},
		})
		return err
	})
	refuse(t, "EndSession", func() error {
		_, err := service.End(t.Context(), outsider, sessioncontrol.EndInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-end",
		})
		return err
	})
	if _, err := service.End(t.Context(), producer, sessioncontrol.EndInput{
		EventID: eventID, SessionID: primary, CommandID: "end-primary",
		ExpectedLiveStateRevision: started.LiveStateRevision,
	}); err != nil {
		t.Fatalf("end primary Session: %v", err)
	}

	refuse(t, "PullForward", func() error {
		_, err := service.PullForward(t.Context(), outsider, sessioncontrol.PullForwardInput{
			EventID: eventID, SessionID: primary, CommandID: "refused-pull-forward",
		})
		return err
	})
}

// TestReinstateSessionGainsLaneScope is the D5 regression test: Matt's
// decision on issue #239 is that Reinstate Session gains the same
// Lanes-of-target scope as its siblings, replacing the CanProduceEvent-only
// guard it had before the Capability Table judged it. Reinstate has no
// current placement to judge — it replaces a Canceled Session's last
// placement outright with the caller's proposed Lanes — so the fixture
// Session (originally published to Lane A) is reinstated into Lane B, a
// different Lane, to tell "judged against the old placement" and "judged
// against the proposed placement" apart:
//
//   - An Operator who holds Lane A (the Session's Lane before cancellation)
//     but not Lane B (the proposed placement) is refused. Judging the old
//     placement would incorrectly pass this Operator.
//   - An Operator who holds Lane B but not Lane A can Reinstate, including
//     obtaining the required Preview Fingerprint themselves — proving both
//     the scope check and PreviewReinstate's own guard were rekeyed to the
//     proposed placement and to Operator authority, not left on Producer.
func TestReinstateSessionGainsLaneScope(t *testing.T) {
	storage, producer, eventID := openSessionControlTest(t)
	sessions := publishSessionControlRundown(t, storage, producer, eventID)
	lanes := sessionControlLanes(t, storage, producer, eventID)
	activateSessionControlEvent(t, storage, producer, eventID)
	service := newSessionControlService(t, storage)

	cancelable := sessions["cancelable"]
	started, err := service.Start(t.Context(), producer, sessioncontrol.StartInput{
		EventID: eventID, SessionID: cancelable, CommandID: "start-cancelable",
	})
	if err != nil {
		t.Fatalf("start cancelable Session: %v", err)
	}
	canceled, err := service.Cancel(t.Context(), producer, sessioncontrol.CancelInput{
		EventID: eventID, SessionID: cancelable, CommandID: "cancel-cancelable",
		ExpectedLiveStateRevision: started.LiveStateRevision, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("cancel cancelable Session: %v", err)
	}

	forecastStart := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	proposedLaneIDs := []int{lanes["B"]}
	proposedLocationIDs := []int{lanes["hall-b"]}

	// oldLaneOnly holds the Session's Lane before cancellation (Lane A), but
	// not the Lane the reinstatement proposes (Lane B).
	oldLaneOnly := producer
	oldLaneOnly.Administrator = false
	oldLaneOnly.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	oldLaneOnly.EventScopes = map[int]viewer.EventScope{
		eventID: {LaneIDs: map[int]struct{}{lanes["A"]: {}}},
	}

	// proposedLaneOnly holds the Lane the reinstatement proposes (Lane B),
	// but not the Session's Lane before cancellation (Lane A).
	proposedLaneOnly := producer
	proposedLaneOnly.Administrator = false
	proposedLaneOnly.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	proposedLaneOnly.EventScopes = map[int]viewer.EventScope{
		eventID: {LaneIDs: map[int]struct{}{lanes["B"]: {}}},
	}

	reinstate := func(actor auth.Account, commandID, fingerprint string) error {
		_, reinstateErr := service.Reinstate(t.Context(), actor, sessioncontrol.ReinstateInput{
			EventID: eventID, SessionID: cancelable, CommandID: commandID,
			ExpectedLiveStateRevision: canceled.LiveStateRevision,
			ForecastStart:             forecastStart,
			LaneIDs:                   proposedLaneIDs, LocationIDs: proposedLocationIDs,
			PreviewFingerprint: fingerprint, Confirmed: true,
		})
		return reinstateErr
	}

	if refusalErr := reinstate(oldLaneOnly, "refused-reinstate", ""); !errors.Is(refusalErr, sessioncontrol.ErrSessionScopeRequired) {
		t.Fatalf(
			"Reinstate by an Operator holding only the old Lane = %v, want %v "+
				"(scope must judge the proposed placement, not the old one)",
			refusalErr, sessioncontrol.ErrSessionScopeRequired,
		)
	}
	if refusalErr := reinstate(oldLaneOnly, "refused-reinstate", ""); !errors.Is(refusalErr, sessioncontrol.ErrSessionScopeRequired) {
		t.Fatalf("retry of refused Reinstate = %v, want the recorded refusal %v", refusalErr, sessioncontrol.ErrSessionScopeRequired)
	}
	entries := rejectedSessionControlAudits(t, storage, producer, "ReinstateSession")
	if len(entries) != 1 {
		t.Fatalf(
			"Rejected Audit Entries for ReinstateSession = %d, want exactly one recorded refusal "+
				"whose retry is answered from its Command Receipt", len(entries),
		)
	}
	if entries[0].Reason != "session_scope_required" {
		t.Errorf("Rejected Audit Entry reason for ReinstateSession = %q, want %q", entries[0].Reason, "session_scope_required")
	}

	preview, err := service.PreviewReinstate(t.Context(), proposedLaneOnly, sessioncontrol.PreviewReinstateInput{
		EventID: eventID, SessionID: cancelable,
		ForecastStart: forecastStart,
		LaneIDs:       proposedLaneIDs, LocationIDs: proposedLocationIDs,
	})
	if err != nil {
		t.Fatalf(
			"preview Reinstate as an Operator holding the proposed Lane = %v, want success "+
				"(PreviewReinstate must accept Operator authority, not still require Producer)", err,
		)
	}
	if err := reinstate(proposedLaneOnly, "reinstate-operator-with-proposed-lane", preview.Fingerprint); err != nil {
		t.Fatalf(
			"Reinstate by an Operator holding the proposed Lane = %v, want success", err,
		)
	}
}

// TestPullForwardAndAdjustTargetEnforceRippledLaneScope is the D14
// regression test. The store methods this Capability Table row's LoadFacts
// calls judge the full set of Lanes a timing ripple moves, not just the
// anchor Session's own Lane — reproducing what the imperative guard checked
// before it was deleted. An Operator who holds only the anchor's Lane, and
// not the Lane of a sibling Session the ripple moves, must still be refused,
// with the same "session_scope_required" evidence — a Rejected Audit Entry
// and a Command Receipt that answers a retry without deciding it again —
// every other scope refusal in this package leaves: if the table were
// judged against the anchor's Lane alone, this Operator would incorrectly
// pass.
func TestPullForwardAndAdjustTargetEnforceRippledLaneScope(t *testing.T) {
	storage, producer, eventID := openSessionControlTest(t)
	sessions := publishSessionControlRundown(t, storage, producer, eventID)
	lanes := sessionControlLanes(t, storage, producer, eventID)
	activateSessionControlEvent(t, storage, producer, eventID)
	service := newSessionControlService(t, storage)

	anchorOnly := producer
	anchorOnly.Administrator = false
	anchorOnly.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	anchorOnly.EventScopes = map[int]viewer.EventScope{
		eventID: {LaneIDs: map[int]struct{}{lanes["A"]: {}}},
	}

	t.Run("PullForward", func(t *testing.T) {
		pfAnchor := sessions["pfAnchor"]
		started, err := service.Start(t.Context(), producer, sessioncontrol.StartInput{
			EventID: eventID, SessionID: pfAnchor, CommandID: "start-pf-anchor",
		})
		if err != nil {
			t.Fatalf("start Pull Forward anchor: %v", err)
		}
		ended, err := service.End(t.Context(), producer, sessioncontrol.EndInput{
			EventID: eventID, SessionID: pfAnchor, CommandID: "end-pf-anchor",
			ExpectedLiveStateRevision: started.LiveStateRevision,
		})
		if err != nil {
			t.Fatalf("end Pull Forward anchor: %v", err)
		}
		pullForward := func() error {
			_, pullErr := service.PullForward(t.Context(), anchorOnly, sessioncontrol.PullForwardInput{
				EventID: eventID, SessionID: pfAnchor, CommandID: "refused-pull-forward-ripple",
				ExpectedLiveStateRevision: ended.LiveStateRevision,
			})
			return pullErr
		}
		if pullErr := pullForward(); !errors.Is(pullErr, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf(
				"Pull Forward by an Operator holding only the anchor's Lane = %v, want %v "+
					"(the rippled sibling's Lane must be demanded too)",
				pullErr, sessioncontrol.ErrSessionScopeRequired,
			)
		}
		if pullErr := pullForward(); !errors.Is(pullErr, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf("retry of refused Pull Forward = %v, want the recorded refusal %v", pullErr, sessioncontrol.ErrSessionScopeRequired)
		}
		entries := rejectedSessionControlAudits(t, storage, producer, "PullForward")
		if len(entries) != 1 {
			t.Fatalf(
				"Rejected Audit Entries for PullForward = %d, want exactly one recorded refusal "+
					"whose retry is answered from its Command Receipt", len(entries),
			)
		}
		if entries[0].Reason != "session_scope_required" {
			t.Errorf("Rejected Audit Entry reason for PullForward = %q, want %q", entries[0].Reason, "session_scope_required")
		}
		preview, err := service.PreviewPullForward(t.Context(), producer, sessioncontrol.PreviewPullForwardInput{
			EventID: eventID, SessionID: pfAnchor,
		})
		if err != nil {
			t.Fatalf("preview Pull Forward: %v", err)
		}
		if _, err := service.PullForward(t.Context(), producer, sessioncontrol.PullForwardInput{
			EventID: eventID, SessionID: pfAnchor, CommandID: "pull-forward-as-producer",
			ExpectedLiveStateRevision: ended.LiveStateRevision,
			PreviewFingerprint:        preview.Fingerprint, Confirmed: true,
		}); err != nil {
			t.Fatalf("Pull Forward as Producer: %v", err)
		}
	})

	t.Run("AdjustTarget", func(t *testing.T) {
		atAnchor := sessions["atAnchor"]
		started, err := service.Start(t.Context(), producer, sessioncontrol.StartInput{
			EventID: eventID, SessionID: atAnchor, CommandID: "start-at-anchor",
		})
		if err != nil {
			t.Fatalf("start Adjust Target anchor: %v", err)
		}
		adjustment := sessioncontrol.TargetAdjustment{Duration: 45 * time.Minute}
		adjustTarget := func() error {
			_, adjustErr := service.AdjustTarget(t.Context(), anchorOnly, sessioncontrol.AdjustTargetInput{
				EventID: eventID, SessionID: atAnchor, CommandID: "refused-adjust-target-ripple",
				Adjustment: adjustment,
			})
			return adjustErr
		}
		if adjustErr := adjustTarget(); !errors.Is(adjustErr, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf(
				"Adjust Target by an Operator holding only the anchor's Lane = %v, want %v "+
					"(the rippled sibling's Lane must be demanded too)",
				adjustErr, sessioncontrol.ErrSessionScopeRequired,
			)
		}
		if adjustErr := adjustTarget(); !errors.Is(adjustErr, sessioncontrol.ErrSessionScopeRequired) {
			t.Fatalf("retry of refused Adjust Target = %v, want the recorded refusal %v", adjustErr, sessioncontrol.ErrSessionScopeRequired)
		}
		entries := rejectedSessionControlAudits(t, storage, producer, "AdjustTarget")
		if len(entries) != 1 {
			t.Fatalf(
				"Rejected Audit Entries for AdjustTarget = %d, want exactly one recorded refusal "+
					"whose retry is answered from its Command Receipt", len(entries),
			)
		}
		if entries[0].Reason != "session_scope_required" {
			t.Errorf("Rejected Audit Entry reason for AdjustTarget = %q, want %q", entries[0].Reason, "session_scope_required")
		}
		preview, err := service.PreviewAdjustTarget(t.Context(), producer, sessioncontrol.PreviewAdjustTargetInput{
			EventID: eventID, SessionID: atAnchor, Adjustment: adjustment,
		})
		if err != nil {
			t.Fatalf("preview Adjust Target: %v", err)
		}
		if _, err := service.AdjustTarget(t.Context(), producer, sessioncontrol.AdjustTargetInput{
			EventID: eventID, SessionID: atAnchor, CommandID: "adjust-target-as-producer",
			ExpectedLiveStateRevision: started.LiveStateRevision,
			Adjustment:                adjustment, PreviewFingerprint: preview.Fingerprint, Confirmed: true,
		}); err != nil {
			t.Fatalf("Adjust Target as Producer: %v", err)
		}
	})
}

// TestPullForwardAndAdjustTargetPreserveLifecycleEvidence is a self-review
// regression test. AdjustSessionTargetLaneScope and PullForwardLaneScope
// resolve their Lane facts by running the same preview Apply runs, inside
// the same authorize-phase transaction, so a Session at the wrong lifecycle
// stage can now be discovered there instead of in Apply. Before
// ErrSessionLifecycleTransition was added to sessionAuthorizationRejections,
// a refusal discovered this way returned the same sentinel error but left no
// Command Receipt or Rejected Audit Entry — evidence this exact refusal
// always left when only Apply could reach it. Both commands, called on the
// Scheduled primary Session, must still refuse with
// "session_lifecycle_transition" and leave that evidence.
func TestPullForwardAndAdjustTargetPreserveLifecycleEvidence(t *testing.T) {
	storage, producer, eventID := openSessionControlTest(t)
	sessions := publishSessionControlRundown(t, storage, producer, eventID)
	activateSessionControlEvent(t, storage, producer, eventID)
	service := newSessionControlService(t, storage)

	primary := sessions["primary"]

	refuse := func(t *testing.T, action string, run func() error) {
		t.Helper()
		if err := run(); !errors.Is(err, sessioncontrol.ErrSessionLifecycleTransition) {
			t.Fatalf("%s on a Scheduled Session = %v, want %v", action, err, sessioncontrol.ErrSessionLifecycleTransition)
		}
		entries := rejectedSessionControlAudits(t, storage, producer, action)
		if len(entries) != 1 {
			t.Fatalf(
				"Rejected Audit Entries for %s = %d, want exactly one recorded refusal",
				action, len(entries),
			)
		}
		if entries[0].Reason != "session_lifecycle_transition" {
			t.Errorf("Rejected Audit Entry reason for %s = %q, want %q", action, entries[0].Reason, "session_lifecycle_transition")
		}
	}

	refuse(t, "PullForward", func() error {
		_, err := service.PullForward(t.Context(), producer, sessioncontrol.PullForwardInput{
			EventID: eventID, SessionID: primary, CommandID: "pull-forward-not-ended",
		})
		return err
	})
	refuse(t, "AdjustTarget", func() error {
		_, err := service.AdjustTarget(t.Context(), producer, sessioncontrol.AdjustTargetInput{
			EventID: eventID, SessionID: primary, CommandID: "adjust-target-not-live",
			Adjustment: sessioncontrol.TargetAdjustment{Duration: 5 * time.Minute},
		})
		return err
	})
}

// openSessionControlTest bootstraps storage and a Producer Account for one
// fresh Event, following the pattern the other Session command test suites
// (rundown, competition) already use for a durable command harness.
func openSessionControlTest(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir); err != nil {
		t.Fatalf("initialize Session Control storage: %v", err)
	}
	storage, err := store.Open(systemactor.NewContext(t.Context(), systemactor.HostMaintenance), dataDir)
	if err != nil {
		t.Fatalf("open Session Control storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close Session Control storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(
		systemactor.NewContext(t.Context(), systemactor.HostMaintenance),
		bootstrapHash, now, now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue Session Control bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		systemactor.NewContext(t.Context(), systemactor.HostMaintenance),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Administrator",
			NormalizedName: "administrator", PasswordHash: "password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Session Control Administrator: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Session Control Event", PlannedStartDate: "2026-08-21",
		PlannedEndDate: "2026-08-21", Timezone: "UTC", EventLocale: "en-US",
		EntryDefaultDisposition: "Included", CommandID: "create-session-control-event",
	})
	if err != nil {
		t.Fatalf("create Session Control Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), producer, event.ID, producer.ID, "Producer", "grant-session-control-producer",
	); err != nil {
		t.Fatalf("grant Session Control Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, producer, event.ID
}

// publishSessionControlRundown publishes two Lanes ("A", "B") sharing one
// Location and six Sessions spaced far enough apart in Lane "A" that none of
// them collides, and returns each Session's ID keyed by its fixture name:
//
//   - "primary": Lane A alone, drives the six-command basic refusal story.
//   - "cancelable": Lane A alone, driven to Canceled for the D5 Reinstate test.
//   - "pfAnchor"/"pfSibling": Lane A alone, then Lanes A and B, adjacent in
//     Lane A's sequence so ending pfAnchor early ripples a Forecast change
//     onto pfSibling — proving Pull Forward's scope demands pfSibling's Lane
//     B too, not just pfAnchor's own Lane A.
//   - "atAnchor"/"atSibling": the same shape, for Adjust Target's ripple.
func publishSessionControlRundown(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) map[string]int {
	t.Helper()
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	targetRefs := func(refs []string) []rundown.TargetRef {
		result := make([]rundown.TargetRef, 0, len(refs))
		for _, ref := range refs {
			result = append(result, rundown.TargetRef{Ref: ref})
		}
		return result
	}
	sessionWithLocations := func(ref, title string, laneRefs, locationRefs []string, start time.Time, duration time.Duration, startBoundary rundown.Boundary) rundown.SessionDraftInput {
		return rundown.SessionDraftInput{
			Ref: ref, Title: title, Type: rundown.SessionPresentation,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       start, PlannedEnd: start.Add(duration),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 10 * time.Minute,
			StartBoundary: startBoundary, EndBoundary: rundown.BoundarySoft,
			Lanes:     targetRefs(laneRefs),
			Locations: targetRefs(locationRefs),
		}
	}
	edited, err := commands.EditDraft(t.Context(), producer, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-session-control-draft",
		Locations: []rundown.LocationDraftInput{
			{Ref: "hall-a", Name: "Hall A"},
			{Ref: "hall-b", Name: "Hall B"},
		},
		Lanes: []rundown.LaneDraftInput{
			{Ref: "lane-a", Name: "Lane A", Location: rundown.TargetRef{Ref: "hall-a"}},
			{Ref: "lane-b", Name: "Lane B", Location: rundown.TargetRef{Ref: "hall-b"}},
		},
		Sessions: []rundown.SessionDraftInput{
			sessionWithLocations("primary", "Primary", []string{"lane-a"}, []string{"hall-a"}, base, time.Hour, rundown.BoundaryHard),
			sessionWithLocations("cancelable", "Cancelable", []string{"lane-a"}, []string{"hall-a"}, base.Add(2*time.Hour), time.Hour, rundown.BoundaryHard),
			sessionWithLocations("pf-anchor", "PF Anchor", []string{"lane-a"}, []string{"hall-a"}, base.Add(4*time.Hour), 30*time.Minute, rundown.BoundaryHard),
			sessionWithLocations("pf-sibling", "PF Sibling", []string{"lane-a", "lane-b"}, []string{"hall-a", "hall-b"}, base.Add(4*time.Hour+30*time.Minute), time.Hour, rundown.BoundarySoft),
			sessionWithLocations("at-anchor", "AT Anchor", []string{"lane-a"}, []string{"hall-a"}, base.Add(6*time.Hour), 30*time.Minute, rundown.BoundaryHard),
			sessionWithLocations("at-sibling", "AT Sibling", []string{"lane-a", "lane-b"}, []string{"hall-a", "hall-b"}, base.Add(6*time.Hour+30*time.Minute), time.Hour, rundown.BoundarySoft),
		},
	})
	if err != nil {
		t.Fatalf("create Session Control Draft: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), producer, rundown.PublishPreviewInput{
		EventID: eventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("preview Session Control Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), producer, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-session-control",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Session Control rundown: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("load Session Control crew rundown: %v", err)
	}
	byTitle := map[string]string{
		"Primary": "primary", "Cancelable": "cancelable",
		"PF Anchor": "pfAnchor", "PF Sibling": "pfSibling",
		"AT Anchor": "atAnchor", "AT Sibling": "atSibling",
	}
	result := make(map[string]int, len(byTitle))
	for _, found := range crew.Sessions {
		if name, ok := byTitle[found.Title]; ok {
			result[name] = found.ID
		}
	}
	if len(result) != len(byTitle) {
		t.Fatalf("Session Control crew rundown Sessions = %+v, want every fixture Session published", crew.Sessions)
	}
	return result
}

// sessionControlLanes returns the published Lane and Location IDs keyed by
// their fixture short names ("A", "B", "hall").
func sessionControlLanes(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) map[string]int {
	t.Helper()
	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("load Session Control crew rundown: %v", err)
	}
	result := make(map[string]int, len(crew.Lanes)+len(crew.Locations))
	for _, found := range crew.Lanes {
		switch found.Name {
		case "Lane A":
			result["A"] = found.ID
		case "Lane B":
			result["B"] = found.ID
		}
	}
	for _, found := range crew.Locations {
		switch found.Name {
		case "Hall A":
			result["hall-a"] = found.ID
		case "Hall B":
			result["hall-b"] = found.ID
		}
	}
	if len(result) != 4 {
		t.Fatalf("Session Control crew rundown Lanes/Locations = %+v, want Lane A, Lane B, Hall A, and Hall B", crew)
	}
	return result
}

// activateSessionControlEvent runs Preflight and Activate so live Session
// commands are accepted.
func activateSessionControlEvent(
	t *testing.T,
	storage *store.SQLite,
	producer auth.Account,
	eventID int,
) {
	t.Helper()
	activationService, err := activation.New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := activationService.Preflight(t.Context(), producer, eventID)
	if err != nil {
		t.Fatalf("preflight Session Control Event: %v", err)
	}
	if _, err = activationService.Activate(
		t.Context(), producer, activation.ActivateInput{
			EventID: eventID, CommandID: "activate-session-control-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate Session Control Event: %v", err)
	}
}

// newSessionControlService constructs the Session Control service under test.
func newSessionControlService(t *testing.T, storage *store.SQLite) *sessioncontrol.Service {
	t.Helper()
	publications, err := results.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	service, err := sessioncontrol.New(storage, publications, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Session Control service: %v", err)
	}
	return service
}

// rejectedSessionControlAudits returns the Rejected Audit Entries recorded
// for one command action.
func rejectedSessionControlAudits(
	t *testing.T,
	storage *store.SQLite,
	reader auth.Account,
	action string,
) []store.AuditEntry {
	t.Helper()
	entries, err := storage.ListAuditEntries(reader.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Session Control Audit Entries: %v", err)
	}
	rejected := make([]store.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == action && entry.Outcome == "Rejected" {
			rejected = append(rejected, entry)
		}
	}
	return rejected
}

package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"log/slog"
	"net"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
)

// newCapacityApplicationTB mirrors newCapacityApplication for benchmarks,
// which receive *testing.B rather than *testing.T.
func newCapacityApplicationTB(
	tb testing.TB,
	fixture capacityFixture,
) *application {
	tb.Helper()
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		tb.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		tb.Fatalf("create Program Output stream: %v", err)
	}
	application, err := newApplication(tb.Context(), applicationConfig{
		Config: Config{
			DataDir: fixture.dataDir, AttachmentsDir: filepath.Join(fixture.dataDir, "attachments"),
			BuildVersion: "realistic-capacity", Logger: slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
		},
		Installation: fixture.installation,
		ListenerAddress: &net.TCPAddr{
			IP: net.ParseIP("127.0.0.1"), Port: 8080,
		},
		DisplayStream: displayStream,
		ProgramStream: programStream,
	})
	if err != nil {
		tb.Fatalf("build realistic capacity application: %v", err)
	}
	tb.Cleanup(func() {
		if closeErr := application.Close(); closeErr != nil {
			tb.Errorf("close realistic capacity application: %v", closeErr)
		}
	})
	return application
}

// The realistic capacity fixture measures the read paths a real Event
// exercises: many actual Sessions distributed across many Lanes, moving
// through mixed lifecycles, grouped into Tracks, with a captured Public
// Schedule Baseline. Unlike prepareCapacityFixture (which represents its
// SessionsAndEntries envelope almost entirely as Competition Entries under
// two Sessions), this fixture is Session-heavy: every unit here is a
// distinct, individually publishable, individually progressed Session.
const (
	// A Draft Lane binds to exactly one Location (lane_drafts.location_id is
	// unique), so a realistic Event with 20 concurrent Lanes needs 20
	// Locations, one Lane apiece.
	realisticSessionLocations = 20
	realisticSessionLanes     = 20
	realisticSessionsPerLane  = 10
	realisticSessionCount     = realisticSessionLanes * realisticSessionsPerLane
	realisticSessionTracks    = 8
	realisticSessionDisplays  = 30
	realisticEventSlug        = "realistic-capacity-event"
	realisticSessionDuration  = 20 * time.Minute
	realisticSessionGap       = 5 * time.Minute
	realisticEndedCeiling     = 40
	realisticLiveCeiling      = 60
	realisticCanceledCeiling  = 70
)

// realisticSessionLifecycleTarget classifies a 1-indexed Session ordinal into
// the mixed lifecycle the fixture drives it to after Activation, matching
// how a real Event has Sessions in every state at once rather than only
// Scheduled ones.
func realisticSessionLifecycleTarget(ordinal int) string {
	switch {
	case ordinal <= realisticEndedCeiling:
		return "Ended"
	case ordinal <= realisticLiveCeiling:
		return "Live"
	case ordinal <= realisticCanceledCeiling:
		return "Canceled"
	default:
		return "Scheduled"
	}
}

func prepareRealisticCapacityFixture(tb testing.TB) capacityFixture {
	tb.Helper()
	t := tb
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize realistic installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open realistic installation: %v", err)
	}
	bootstrap, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue realistic bootstrap: %v", err)
	}
	session, err := installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Realistic Administrator",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap realistic Administrator: %v", err)
	}
	now := time.Now().UTC()
	event, err := installation.Events().Create(t.Context(), session.Account, events.CreateInput{
		Name: "Realistic Capacity Event", PlannedStartDate: now.Format(time.DateOnly),
		PlannedEndDate: now.AddDate(0, 0, 1).Format(time.DateOnly),
		Timezone:       "UTC", EventLocale: "en-US", ContentLanguage: "en",
		EventDayBoundary: "06:00", EntryDefaultDisposition: "Included",
		CommandID: "realistic-create-event",
	})
	if err != nil {
		t.Fatalf("create realistic Event: %v", err)
	}
	if _, err = installation.Events().GrantEventAccess(
		t.Context(),
		session.Account,
		event.ID,
		session.Account.ID,
		"Producer",
		"realistic-grant-producer",
	); err != nil {
		t.Fatalf("grant realistic Producer: %v", err)
	}
	actor, err := installation.Authentication().Authenticate(t.Context(), session.Token)
	if err != nil {
		t.Fatalf("refresh realistic authorization: %v", err)
	}
	session.Account = actor
	event, err = installation.Events().Update(t.Context(), actor, event.ID, events.CreateInput{
		Name: event.Name, Public: true, PublicSlug: realisticEventSlug,
		PlannedStartDate: event.PlannedStartDate, PlannedEndDate: event.PlannedEndDate,
		Timezone: event.Timezone, EventLocale: event.EventLocale,
		ContentLanguage: event.ContentLanguage, EventDayBoundary: event.EventDayBoundary,
		EntryDefaultDisposition:        event.EntryDefaultDisposition,
		SubmissionEligibility:          event.SubmissionEligibility,
		VotingMethod:                   event.VotingMethod,
		SelfVotePolicy:                 event.SelfVotePolicy,
		TargetAdjustmentPresetsSeconds: event.TargetAdjustmentPresetsSeconds,
		CommandID:                      "realistic-publish-event", ExpectedRevision: event.Revision,
	})
	if err != nil {
		t.Fatalf("publish realistic Event: %v", err)
	}
	edit := realisticCapacityDraft(event.ID, now)
	if _, err = installation.RundownCommands().EditDraft(t.Context(), actor, edit); err != nil {
		t.Fatalf("stage realistic Rundown: %v", err)
	}
	preview, err := installation.RundownQueries().PublishPreview(
		t.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: event.ID},
	)
	if err != nil {
		t.Fatalf("preview realistic Rundown: %v", err)
	}
	if len(preview.ValidationFailures) != 0 {
		t.Fatalf("realistic Publish validation = %v", preview.ValidationFailures)
	}
	if _, err = installation.RundownCommands().Publish(
		t.Context(),
		actor,
		rundown.PublishInput{
			EventID:   event.ID,
			CommandID: "realistic-publish-rundown",
			Confirmation: rundown.PublishConfirmation{
				DraftRevision:     preview.DraftRevision,
				PublishedRevision: preview.PublishedRevision,
				ChangeIDs:         preview.ChangeIDs,
				Fingerprint:       preview.Fingerprint,
			},
		},
	); err != nil {
		t.Fatalf("publish realistic Rundown: %v", err)
	}
	preflight, err := installation.Activation().Preflight(t.Context(), actor, event.ID)
	if err != nil {
		t.Fatalf("preflight realistic Event: %v", err)
	}
	if len(preflight.Blockers) != 0 {
		t.Fatalf("realistic Activation blockers = %+v", preflight.Blockers)
	}
	if _, err = installation.Activation().Activate(
		t.Context(),
		actor,
		activation.ActivateInput{
			EventID:      event.ID,
			CommandID:    "realistic-activate-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate realistic Event: %v", err)
	}
	published, err := installation.RundownQueries().CrewRundown(t.Context(), actor, event.ID)
	if err != nil {
		t.Fatalf("load realistic Rundown: %v", err)
	}
	if len(published.Sessions) != realisticSessionCount {
		t.Fatalf("realistic Session count = %d, want %d", len(published.Sessions), realisticSessionCount)
	}
	driveRealisticSessionLifecycles(t, installation, actor, event.ID, published.Sessions)
	captureRealisticScheduleBaseline(t, installation, actor, event.ID)

	var locationID int
	for _, location := range published.Locations {
		if location.Name == "Realistic Location 01" {
			locationID = location.ID
		}
	}
	if locationID == 0 {
		t.Fatal("realistic Location 01 identity not found")
	}
	if err = installation.Close(); err != nil {
		t.Fatalf("close realistic fixture before Display load: %v", err)
	}
	credentials := bulkLoadRealisticDisplays(
		t,
		filepath.Join(dataDir, "beamers.db"),
		event.ID,
		locationID,
		realisticSessionDisplays,
	)
	installation, err = operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("reopen realistic installation: %v", err)
	}
	actor, err = installation.Authentication().Authenticate(t.Context(), session.Token)
	if err != nil {
		t.Fatalf("authenticate realistic session: %v", err)
	}
	return capacityFixture{
		dataDir:            dataDir,
		installation:       installation,
		actor:              actor,
		sessionToken:       session.Token,
		eventID:            event.ID,
		displayCredentials: credentials,
	}
}

func realisticCapacityDraft(eventID int, now time.Time) rundown.EditDraftInput {
	locations := make([]rundown.LocationDraftInput, 0, realisticSessionLocations)
	for index := 1; index <= realisticSessionLocations; index++ {
		locations = append(locations, rundown.LocationDraftInput{
			Ref:  fmt.Sprintf("rloc-%02d", index),
			Name: fmt.Sprintf("Realistic Location %02d", index),
		})
	}
	lanes := make([]rundown.LaneDraftInput, 0, realisticSessionLanes)
	for index := 1; index <= realisticSessionLanes; index++ {
		locationRef := fmt.Sprintf("rloc-%02d", (index-1)%realisticSessionLocations+1)
		lanes = append(lanes, rundown.LaneDraftInput{
			Ref:      fmt.Sprintf("rlane-%02d", index),
			Name:     fmt.Sprintf("Realistic Lane %02d", index),
			Location: rundown.TargetRef{Ref: locationRef},
		})
	}
	tracks := make([]rundown.TrackDraftInput, 0, realisticSessionTracks)
	for index := 1; index <= realisticSessionTracks; index++ {
		tracks = append(tracks, rundown.TrackDraftInput{
			Ref:  fmt.Sprintf("rtrack-%02d", index),
			Name: fmt.Sprintf("Realistic Track %02d", index),
		})
	}
	base := now.Add(-time.Hour)
	sessions := make([]rundown.SessionDraftInput, 0, realisticSessionCount)
	for lane := 1; lane <= realisticSessionLanes; lane++ {
		laneRef := fmt.Sprintf("rlane-%02d", lane)
		locationRef := fmt.Sprintf("rloc-%02d", (lane-1)%realisticSessionLocations+1)
		for slot := 1; slot <= realisticSessionsPerLane; slot++ {
			ordinal := (lane-1)*realisticSessionsPerLane + slot
			trackRef := fmt.Sprintf("rtrack-%02d", (ordinal-1)%realisticSessionTracks+1)
			start := base.Add(time.Duration(slot-1) * (realisticSessionDuration + realisticSessionGap))
			sessions = append(sessions, rundown.SessionDraftInput{
				Ref:                fmt.Sprintf("rsession-%03d", ordinal),
				Title:              fmt.Sprintf("Realistic Session %03d", ordinal),
				Type:               rundown.SessionPresentation,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start,
				PlannedEnd:         start.Add(realisticSessionDuration),
				TimingPolicy:       rundown.TimingFixedDuration,
				MinimumDuration:    time.Minute,
				StartBoundary:      rundown.BoundarySoft,
				EndBoundary:        rundown.BoundarySoft,
				Lanes:              []rundown.TargetRef{{Ref: laneRef}},
				Locations:          []rundown.TargetRef{{Ref: locationRef}},
				Tracks:             []rundown.TargetRef{{Ref: trackRef}},
			})
		}
	}
	return rundown.EditDraftInput{
		EventID:   eventID,
		CommandID: "realistic-stage-rundown",
		Locations: locations,
		Lanes:     lanes,
		Tracks:    tracks,
		Sessions:  sessions,
	}
}

func driveRealisticSessionLifecycles(
	tb testing.TB,
	installation *operations.Installation,
	actor auth.Account,
	eventID int,
	sessions []rundown.CrewSession,
) {
	tb.Helper()
	t := tb
	control := installation.SessionControl()
	for _, item := range sessions {
		var ordinal int
		if _, scanErr := fmt.Sscanf(item.Title, "Realistic Session %d", &ordinal); scanErr != nil {
			t.Fatalf("parse realistic Session ordinal from %q: %v", item.Title, scanErr)
		}
		switch realisticSessionLifecycleTarget(ordinal) {
		case "Scheduled":
			continue
		case "Ended":
			state := startRealisticSession(t, control, actor, eventID, item.ID, ordinal)
			if _, err := control.End(t.Context(), actor, sessioncontrol.EndInput{
				EventID: eventID, SessionID: item.ID,
				CommandID:                 fmt.Sprintf("realistic-end-%03d", ordinal),
				ExpectedLiveStateRevision: state.LiveStateRevision,
			}); err != nil {
				t.Fatalf("end realistic Session %d: %v", ordinal, err)
			}
		case "Live":
			startRealisticSession(t, control, actor, eventID, item.ID, ordinal)
		case "Canceled":
			if _, err := control.Cancel(t.Context(), actor, sessioncontrol.CancelInput{
				EventID: eventID, SessionID: item.ID,
				CommandID:                 fmt.Sprintf("realistic-cancel-%03d", ordinal),
				ExpectedLiveStateRevision: item.LiveStateRevision,
				Confirmed:                 true,
				PublicCancellationMessage: "Realistic capacity fixture cancellation",
			}); err != nil {
				t.Fatalf("cancel realistic Session %d: %v", ordinal, err)
			}
		}
	}
}

func startRealisticSession(
	tb testing.TB,
	control *sessioncontrol.Service,
	actor auth.Account,
	eventID int,
	sessionID int,
	ordinal int,
) sessioncontrol.State {
	tb.Helper()
	t := tb
	state, err := control.Start(t.Context(), actor, sessioncontrol.StartInput{
		EventID: eventID, SessionID: sessionID,
		CommandID: fmt.Sprintf("realistic-start-%03d", ordinal),
	})
	if err != nil {
		t.Fatalf("start realistic Session %d: %v", ordinal, err)
	}
	return state
}

func captureRealisticScheduleBaseline(
	tb testing.TB,
	installation *operations.Installation,
	actor auth.Account,
	eventID int,
) {
	tb.Helper()
	t := tb
	preview, err := installation.ScheduleBaselineQueries().Preview(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("preview realistic Schedule Baseline: %v", err)
	}
	if len(preview.ValidationFailures) != 0 {
		t.Fatalf("realistic Schedule Baseline validation = %v", preview.ValidationFailures)
	}
	if _, err = installation.ScheduleBaselineCommands().Capture(
		t.Context(),
		actor,
		schedulebaseline.CaptureInput{
			EventID:               eventID,
			CommandID:             "realistic-capture-baseline",
			Confirmation:          preview.Confirmation,
			AcknowledgedEventName: preview.EventName,
		},
	); err != nil {
		t.Fatalf("capture realistic Schedule Baseline: %v", err)
	}
}

func bulkLoadRealisticDisplays(
	tb testing.TB,
	databasePath string,
	eventID int,
	locationID int,
	count int,
) []string {
	tb.Helper()
	t := tb
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open realistic database: %v", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close realistic database: %v", closeErr)
		}
	}()
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin realistic Display load: %v", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	displayStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO displays (name, created_at, enrolled_at) VALUES (?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare realistic Displays: %v", err)
	}
	defer func() {
		if closeErr := displayStatement.Close(); closeErr != nil {
			t.Errorf("close realistic Display statement: %v", closeErr)
		}
	}()
	credentialStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO display_credentials (token_hash, created_at, display_id)
		 VALUES (?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare realistic Display credentials: %v", err)
	}
	defer func() {
		if closeErr := credentialStatement.Close(); closeErr != nil {
			t.Errorf("close realistic Display credential statement: %v", closeErr)
		}
	}()
	assignmentStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO display_assignments
			(view_key, display_group_keys, created_at, updated_at, display_id, event_id, location_id)
		 VALUES (?, '[]', ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare realistic Display Assignments: %v", err)
	}
	defer func() {
		if closeErr := assignmentStatement.Close(); closeErr != nil {
			t.Errorf("close realistic Display Assignment statement: %v", closeErr)
		}
	}()
	credentials := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		now := time.Unix(int64(index), 0).UTC()
		result, execErr := displayStatement.ExecContext(
			t.Context(),
			fmt.Sprintf("Realistic Display %03d", index),
			now,
			now,
		)
		if execErr != nil {
			t.Fatalf("insert realistic Display %d: %v", index, execErr)
		}
		displayID, execErr := result.LastInsertId()
		if execErr != nil {
			t.Fatalf("read realistic Display %d identity: %v", index, execErr)
		}
		tokenBytes := sha256.Sum256(fmt.Appendf(nil, "realistic-display-%d", index))
		token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
		tokenHash := sha256.Sum256([]byte(token))
		if _, execErr = credentialStatement.ExecContext(
			t.Context(),
			hex.EncodeToString(tokenHash[:]),
			now,
			displayID,
		); execErr != nil {
			t.Fatalf("insert realistic Display credential %d: %v", index, execErr)
		}
		if _, execErr = assignmentStatement.ExecContext(
			t.Context(),
			displayviews.StageTimer,
			now,
			now,
			displayID,
			eventID,
			locationID,
		); execErr != nil {
			t.Fatalf("insert realistic Display Assignment %d: %v", index, execErr)
		}
		credentials = append(credentials, token)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit realistic Display load: %v", err)
	}
	return credentials
}

func TestRealisticCapacityFixtureBuildsMixedLifecycleSessions(t *testing.T) {
	fixture := prepareRealisticCapacityFixture(t)
	crewRundown, err := fixture.installation.RundownQueries().CrewRundown(
		t.Context(),
		fixture.actor,
		fixture.eventID,
	)
	if err != nil {
		t.Fatalf("load realistic Rundown: %v", err)
	}
	if len(crewRundown.Sessions) != realisticSessionCount {
		t.Fatalf("realistic Session count = %d, want %d", len(crewRundown.Sessions), realisticSessionCount)
	}
	if len(crewRundown.Lanes) != realisticSessionLanes {
		t.Fatalf("realistic Lane count = %d, want %d", len(crewRundown.Lanes), realisticSessionLanes)
	}
	if len(crewRundown.Tracks) != realisticSessionTracks {
		t.Fatalf("realistic Track count = %d, want %d", len(crewRundown.Tracks), realisticSessionTracks)
	}
	seen := map[string]int{}
	for _, item := range crewRundown.Sessions {
		seen[string(item.Lifecycle)]++
	}
	if seen["Ended"] == 0 || seen["Live"] == 0 || seen["Canceled"] == 0 || seen["Scheduled"] == 0 {
		t.Fatalf("realistic lifecycle distribution = %+v, want all four lifecycles represented", seen)
	}
	if len(fixture.displayCredentials) != realisticSessionDisplays {
		t.Fatalf(
			"realistic Display credential count = %d, want %d",
			len(fixture.displayCredentials),
			realisticSessionDisplays,
		)
	}
}

// BenchmarkCapacityRealisticPublicScheduleDirect measures the public
// Schedule build against the origin route directly - no coalescing cache
// front, unlike the load-generator's capacityScheduleCache - against a
// realistic Session count.
func BenchmarkCapacityRealisticPublicScheduleDirect(b *testing.B) {
	fixture := prepareRealisticCapacityFixture(b)
	application := newCapacityApplicationTB(b, fixture)
	origin := httptest.NewServer(application)
	b.Cleanup(origin.Close)
	client := &http.Client{Timeout: 30 * time.Second}
	scheduleURL := origin.URL + "/events/" + realisticEventSlug + "/schedule"

	for b.Loop() {
		request, err := http.NewRequestWithContext(b.Context(), http.MethodGet, scheduleURL, http.NoBody)
		if err != nil {
			b.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if closeErr != nil {
			b.Fatal(closeErr)
		}
		if response.StatusCode != http.StatusOK {
			b.Fatalf("direct public Schedule status = %d", response.StatusCode)
		}
	}
}

// BenchmarkCapacityRealisticCrewRundownLoad measures the Crew Rundown read
// path (backing every Planning, Control, and Entries page) against a
// realistic Session count, through the same Connect RPC the crew console
// load generator exercises.
func BenchmarkCapacityRealisticCrewRundownLoad(b *testing.B) {
	fixture := prepareRealisticCapacityFixture(b)
	application := newCapacityApplicationTB(b, fixture)
	origin := httptest.NewServer(application)
	b.Cleanup(origin.Close)
	client := &http.Client{
		Transport: capacityCookieTransport{
			base: http.DefaultTransport,
			cookie: &http.Cookie{
				Name: sessionCookieName, Value: fixture.sessionToken,
			},
		},
		Timeout: 30 * time.Second,
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(client, origin.URL)

	for b.Loop() {
		if _, err := rundownClient.GetCrewRundown(
			b.Context(),
			connect.NewRequest(&rundownv1.GetCrewRundownRequest{
				EventId: int64(fixture.eventID),
			}),
		); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCapacityRealisticDisplaySnapshotFanoutAfterPublish measures the
// full commit-to-fanout path this ticket exists to make measurable: each
// iteration re-Publishes one incremental Rundown change against a realistic
// Session and Lane count, then fans a Display Snapshot fetch out to every
// connected Display, exercising the batched Session Run lookups the
// rebuilt Snapshot depends on.
func BenchmarkCapacityRealisticDisplaySnapshotFanoutAfterPublish(b *testing.B) {
	fixture := prepareRealisticCapacityFixture(b)
	application := newCapacityApplicationTB(b, fixture)
	origin := httptest.NewServer(application)
	b.Cleanup(origin.Close)
	transport := &http.Transport{
		MaxIdleConns:        realisticSessionDisplays,
		MaxIdleConnsPerHost: realisticSessionDisplays,
		MaxConnsPerHost:     realisticSessionDisplays,
	}
	b.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	crewRundown, err := fixture.installation.RundownQueries().CrewRundown(
		b.Context(),
		fixture.actor,
		fixture.eventID,
	)
	if err != nil {
		b.Fatalf("load realistic Rundown before fanout benchmark: %v", err)
	}
	touchedSessionID := crewRundown.Sessions[0].ID
	draftRevision := crewRundown.DraftRevision

	iteration := 0
	for b.Loop() {
		iteration++
		editResult, editErr := fixture.installation.RundownCommands().EditDraft(
			b.Context(),
			fixture.actor,
			rundown.EditDraftInput{
				EventID:               fixture.eventID,
				CommandID:             fmt.Sprintf("realistic-refresh-edit-%d", iteration),
				ExpectedDraftRevision: draftRevision,
				Sessions: []rundown.SessionDraftInput{{
					ID: touchedSessionID, CrewNotes: fmt.Sprintf("bench iteration %d", iteration),
					UpdateFields: []string{"crew_notes"},
				}},
			},
		)
		if editErr != nil {
			b.Fatalf("stage realistic refresh Draft: %v", editErr)
		}
		draftRevision = editResult.DraftRevision
		preview, previewErr := fixture.installation.RundownQueries().PublishPreview(
			b.Context(),
			fixture.actor,
			rundown.PublishPreviewInput{EventID: fixture.eventID},
		)
		if previewErr != nil {
			b.Fatalf("preview realistic refresh Publish: %v", previewErr)
		}
		if _, publishErr := fixture.installation.RundownCommands().Publish(
			b.Context(),
			fixture.actor,
			rundown.PublishInput{
				EventID:   fixture.eventID,
				CommandID: fmt.Sprintf("realistic-refresh-publish-%d", iteration),
				Confirmation: rundown.PublishConfirmation{
					DraftRevision:     preview.DraftRevision,
					PublishedRevision: preview.PublishedRevision,
					ChangeIDs:         preview.ChangeIDs,
					Fingerprint:       preview.Fingerprint,
				},
			},
		); publishErr != nil {
			b.Fatalf("publish realistic refresh: %v", publishErr)
		}

		var failed atomic.Bool
		var requests sync.WaitGroup
		for _, credential := range fixture.displayCredentials {
			requests.Go(func() {
				if _, _, snapshotErr := fetchCapacitySnapshot(
					b.Context(),
					client,
					origin.URL,
					credential,
				); snapshotErr != nil {
					failed.Store(true)
				}
			})
		}
		requests.Wait()
		if failed.Load() {
			b.Fatal("fetch realistic Display Snapshot after Publish")
		}
	}
	b.ReportMetric(float64(realisticSessionDisplays), "snapshots/op")
}

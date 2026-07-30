package displays

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
)

func TestDisplaySessionUsesSharedPublicTimePresentation(t *testing.T) {
	forecastStart := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	forecastEnd := forecastStart.Add(time.Hour)
	communicatedStart := forecastStart.Add(4 * time.Minute)
	actualStart := forecastStart.Add(5 * time.Minute)
	found := store.DisplaySessionState{
		ID: 11, Title: "Live Session", AudienceVisibility: "Public",
		Lifecycle: "Live", ForecastStart: forecastStart, ForecastEnd: forecastEnd,
		PublicTime: publictime.Facts{
			Lifecycle: publictime.Live,
			Forecast:  publictime.Range{Start: forecastStart, End: forecastEnd},
			Actual:    publictime.OptionalRange{Start: &actualStart},
			Communicated: publictime.OptionalRange{
				Start: &communicatedStart,
			},
			RunDuration: time.Hour,
		},
	}

	session, selected, err := displaySession(
		store.DisplaySnapshotState{ViewKey: displayviews.EventOverview},
		found,
		forecastStart,
		time.UTC,
	)
	if err != nil {
		t.Fatalf("present Display Session: %v", err)
	}
	if !selected || session.PresentedStartLabel != publictime.LabelActualStart ||
		!session.PresentedStart.Equal(communicatedStart) ||
		session.PresentedEndLabel != publictime.LabelForecastEnd ||
		!session.orderAt.Equal(forecastStart) {
		t.Fatalf("Display Session = %+v, want normalized Actual Start and Forecast End", session)
	}
}

func TestDisplaySessionRejectsImpossiblePublicTimeState(t *testing.T) {
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	_, selected, err := displaySession(
		store.DisplaySnapshotState{ViewKey: displayviews.EventOverview},
		store.DisplaySessionState{
			ID: 12, AudienceVisibility: "Public", Lifecycle: "Live",
			ForecastStart: start, ForecastEnd: start.Add(time.Hour),
			PublicTime: publictime.Facts{
				Lifecycle:   publictime.Live,
				Forecast:    publictime.Range{Start: start, End: start.Add(time.Hour)},
				RunDuration: time.Hour,
			},
		},
		start,
		time.UTC,
	)
	if !errors.Is(err, publictime.ErrImpossibleState) || selected {
		t.Fatalf("impossible Display Session = selected %v, error %v", selected, err)
	}
}

func TestEnrollmentForBrowserPersistsAndReusesPendingMaterial(t *testing.T) {
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	service, err := New(storage, Config{
		Now: func() time.Time { return now }, Random: bytes.NewReader(make([]byte, enrollmentCodeBytes+displayTokenBytes)),
		EnrollmentTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}

	issued, err := service.EnrollmentForBrowser(t.Context(), "", "")
	if err != nil {
		t.Fatalf("issue Display Enrollment: %v", err)
	}
	if issued.Code != "AAAA-AAAA" || issued.Credential == "" || issued.ExpiresAt != now.Add(10*time.Minute) {
		t.Fatalf("issued Display Enrollment = %+v", issued)
	}
	reused, err := service.EnrollmentForBrowser(t.Context(), issued.Code, issued.Credential)
	if err != nil {
		t.Fatalf("reuse Display Enrollment: %v", err)
	}
	if reused.Code != issued.Code || reused.Credential != issued.Credential || reused.ExpiresAt != issued.ExpiresAt {
		t.Errorf("reused Display Enrollment = %+v; want %+v", reused, issued)
	}
}

func TestDisplayEnrollmentExpiresAndClaimIsSingleUse(t *testing.T) {
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if issueErr := storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); issueErr != nil {
		t.Fatalf("issue bootstrap: %v", issueErr)
	}
	created, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator", NormalizedName: "administrator",
		PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	random := append(bytes.Repeat([]byte{1}, enrollmentCodeBytes+displayTokenBytes),
		bytes.Repeat([]byte{2}, enrollmentCodeBytes+displayTokenBytes)...)
	clock := now
	clockStep := time.Duration(0)
	notifications := 0
	service, err := New(storage, Config{
		Now: func() time.Time {
			current := clock
			clock = clock.Add(clockStep)
			return current
		},
		Random: bytes.NewReader(random), EnrollmentTTL: 10 * time.Minute,
		NotifyDisplays: func() { notifications++ },
	})
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}

	expiring, err := service.EnrollmentForBrowser(t.Context(), "", "")
	if err != nil {
		t.Fatalf("issue expiring Enrollment: %v", err)
	}
	if _, currentErr := service.Current(t.Context(), expiring.Credential); !errors.Is(currentErr, ErrDisplayAuthentication) {
		t.Errorf("pending Display credential error = %v", currentErr)
	}
	nonAdministrator := administrator
	nonAdministrator.Administrator = false
	if _, claimErr := service.ClaimEnrollment(t.Context(), nonAdministrator, ClaimInput{
		Code: expiring.Code, Name: "Unauthorized", CommandID: "reject-unauthorized-display",
	}); !errors.Is(claimErr, ErrAdministratorRequired) {
		t.Errorf("unauthorized Display claim error = %v", claimErr)
	}
	clock = now.Add(11 * time.Minute)
	if _, claimErr := service.ClaimEnrollment(t.Context(), administrator, ClaimInput{
		Code: expiring.Code, Name: "Expired", CommandID: "reject-expired-display",
	}); !errors.Is(claimErr, ErrEnrollmentUnavailable) {
		t.Errorf("expired Display claim error = %v", claimErr)
	}
	if notifications != 0 {
		t.Fatalf("rejected Display claim notifications = %d, want 0", notifications)
	}

	issued, err := service.EnrollmentForBrowser(t.Context(), "", "")
	if err != nil {
		t.Fatalf("issue claimable Enrollment: %v", err)
	}
	input := ClaimInput{Code: issued.Code, Name: "Stage Right", CommandID: "claim-stage-right"}
	claimed, err := service.ClaimEnrollment(t.Context(), administrator, input)
	if err != nil {
		t.Fatalf("claim Display: %v", err)
	}
	replayed, err := service.ClaimEnrollment(t.Context(), nonAdministrator, input)
	if err != nil || replayed != claimed {
		t.Errorf("exact Display claim retry = %+v, %v; want %+v", replayed, err, claimed)
	}
	if notifications != 2 {
		t.Fatalf("successful Display claim notifications = %d, want 2", notifications)
	}
	if _, claimErr := service.ClaimEnrollment(t.Context(), administrator, ClaimInput{
		Code: issued.Code, Name: "Reused", CommandID: "reuse-stage-right-code",
	}); !errors.Is(claimErr, ErrEnrollmentUnavailable) {
		t.Errorf("reused Display code error = %v", claimErr)
	}
	if notifications != 2 {
		t.Fatalf("rejected Display reuse notifications = %d, want 2", notifications)
	}
	clockStep = 100 * time.Millisecond
	currentStarted := clock
	current, err := service.Current(t.Context(), issued.Credential)
	if err != nil || current.Display != claimed || !current.Standby {
		t.Errorf("claimed Display current state = %+v, %v", current, err)
	}
	if err = service.Authenticate(t.Context(), issued.Credential); err != nil {
		t.Errorf("authenticate claimed Display: %v", err)
	}
	if want := currentStarted.Add(clockStep / 2); !current.ServerTime.Equal(want) {
		t.Errorf("Display server time = %s, want request midpoint %s", current.ServerTime, want)
	}
}

func TestDisplayReenrollmentPreservesExistingIdentity(t *testing.T) {
	ctx := t.Context()
	dataDir := t.TempDir()
	if err := store.Initialize(ctx, dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(ctx, bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	account, err := storage.BootstrapAdministrator(ctx, store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash, Name: "Administrator", NormalizedName: "administrator",
		PasswordHash: "test-password-hash", SessionHash: strings.Repeat("s", 64),
		Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{ID: account.ID, Name: account.Name, Administrator: true}
	service, err := New(storage, Config{
		Now: func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat(
			[]byte{1},
			enrollmentCodeBytes+displayTokenBytes,
		)),
		EnrollmentTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}
	first, err := service.EnrollmentForBrowser(ctx, "", "")
	if err != nil {
		t.Fatalf("issue first Enrollment: %v", err)
	}
	original, err := service.ClaimEnrollment(ctx, administrator, ClaimInput{
		Code: first.Code, Name: "Stage", CommandID: "claim-stage",
	})
	if err != nil {
		t.Fatalf("claim original Display: %v", err)
	}
	if err = storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err = database.ExecContext(ctx, "DELETE FROM display_credentials"); err != nil {
		_ = database.Close()
		t.Fatalf("strip Display credentials: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	storage, err = store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close restored storage: %v", closeErr)
		}
	})
	service, err = New(storage, Config{
		Now: func() time.Time { return now.Add(time.Minute) },
		Random: bytes.NewReader(bytes.Repeat(
			[]byte{2},
			enrollmentCodeBytes+displayTokenBytes,
		)),
		EnrollmentTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create restored Display service: %v", err)
	}
	if err = service.Authenticate(ctx, first.Credential); !errors.Is(err, ErrDisplayAuthentication) {
		t.Fatalf("authenticate removed Display credential: %v", err)
	}
	recovery, err := service.EnrollmentForBrowser(ctx, "", "")
	if err != nil {
		t.Fatalf("issue recovery Enrollment: %v", err)
	}
	reenrolled, err := service.ClaimEnrollment(ctx, administrator, ClaimInput{
		Code: recovery.Code, DisplayID: original.ID, CommandID: "reenroll-stage",
	})
	if err != nil {
		t.Fatalf("re-Enroll Display: %v", err)
	}
	if reenrolled != original {
		t.Fatalf("re-Enrolled Display = %+v, want preserved %+v", reenrolled, original)
	}
	current, err := service.Current(ctx, recovery.Credential)
	if err != nil || current.Display != original {
		t.Fatalf("re-Enrolled current Display = %+v, %v", current.Display, err)
	}
}

func TestEnrollmentQRCodeDataURLContainsPNG(t *testing.T) {
	dataURL, err := EnrollmentQRCodeDataURL("https://beamers.example/admin/displays/enroll?code=ABCD-EFGH")
	if err != nil {
		t.Fatalf("render Enrollment QR code: %v", err)
	}
	encoded, found := strings.CutPrefix(dataURL, "data:image/png;base64,")
	if !found {
		t.Fatalf("Enrollment QR data URL = %q", dataURL)
	}
	contents, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode Enrollment QR data URL: %v", err)
	}
	image, err := png.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("decode Enrollment QR PNG: %v", err)
	}
	if image.Bounds().Dx() < 200 || image.Bounds().Dx() != image.Bounds().Dy() {
		t.Errorf("Enrollment QR dimensions = %v", image.Bounds())
	}
}

// TestCrewOnlySessionsReachCrewOverviewInFull separates the audience-facing Views
// from the crew one. ADR 0022's worked example is a Published, Crew Only
// soundcheck going Live without appearing on public Views, and the crew still has
// to be able to see it. Every other View reports only that the span is taken.
func TestCrewOnlySessionsReachCrewOverviewInFull(t *testing.T) {
	forecastStart := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	forecastEnd := forecastStart.Add(time.Hour)
	actualStart := forecastStart.Add(2 * time.Minute)
	found := store.DisplaySessionState{
		ID: 21, Title: "Private Soundcheck", AudienceVisibility: "CrewOnly",
		Lifecycle: "Live", ForecastStart: forecastStart, ForecastEnd: forecastEnd,
		LocationIDs: []int{7},
		PublicTime: publictime.Facts{
			Lifecycle:   publictime.Live,
			Forecast:    publictime.Range{Start: forecastStart, End: forecastEnd},
			Actual:      publictime.OptionalRange{Start: &actualStart},
			RunDuration: time.Hour,
		},
	}

	crew, selected, err := displaySession(
		store.DisplaySnapshotState{
			ViewKey:    displayviews.CrewOverview,
			LocationID: 7,
		},
		found,
		forecastStart,
		time.UTC,
	)
	if err != nil {
		t.Fatalf("present Crew Overview Session: %v", err)
	}
	if !selected || crew.Unavailable || crew.Title != "Private Soundcheck" {
		t.Errorf("Crew Overview Session = %+v, want the Crew Only Session in full", crew)
	}

	// Timeline is audience-facing, so the same Session becomes an opaque span.
	timeline, selected, err := displaySession(
		store.DisplaySnapshotState{ViewKey: displayviews.Timeline},
		found,
		forecastStart,
		time.UTC,
	)
	if err != nil {
		t.Fatalf("present Timeline Session: %v", err)
	}
	if !selected || !timeline.Unavailable {
		t.Errorf("Timeline Session = %+v, want an unavailable span", timeline)
	}
	if timeline.Title != "" {
		t.Errorf("Timeline leaked the Crew Only title %q", timeline.Title)
	}
	// The span still needs its edges, or a suppressed block has no width.
	if !timeline.ForecastStart.Equal(forecastStart) || !timeline.ForecastEnd.Equal(forecastEnd) {
		t.Errorf("Timeline span = %+v, want the Session's edges", timeline)
	}
}

func TestTimelineRetainsEndedSessionsForTheWholeEventDay(t *testing.T) {
	forecastStart := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	forecastEnd := forecastStart.Add(time.Hour)
	actualStart := forecastStart.Add(2 * time.Minute)
	actualEnd := forecastEnd.Add(3 * time.Minute)
	found := store.DisplaySessionState{
		ID: 22, Title: "Opening Keynote", AudienceVisibility: "Public",
		Lifecycle: "Ended", ForecastStart: forecastStart, ForecastEnd: forecastEnd,
		PublicTime: publictime.Facts{
			Lifecycle: publictime.Ended,
			Forecast:  publictime.Range{Start: forecastStart, End: forecastEnd},
			Actual: publictime.OptionalRange{
				Start: &actualStart,
				End:   &actualEnd,
			},
			RunDuration: time.Hour,
		},
	}
	now := forecastEnd.Add(4 * time.Hour)

	timeline, selected, err := displaySession(
		store.DisplaySnapshotState{ViewKey: displayviews.Timeline},
		found,
		now,
		time.UTC,
	)
	if err != nil {
		t.Fatalf("present ended Timeline Session: %v", err)
	}
	if !selected || timeline.Title != found.Title || timeline.Lifecycle != "Ended" {
		t.Fatalf("ended Timeline Session = %+v, selected %v", timeline, selected)
	}

	_, selected, err = displaySession(
		store.DisplaySnapshotState{ViewKey: displayviews.EventOverview},
		found,
		now,
		time.UTC,
	)
	if err != nil || selected {
		t.Fatalf("ended Event Overview Session selected = %v, error %v", selected, err)
	}
}

func TestCrewOverviewSessionsFollowTheAssignedLocation(t *testing.T) {
	forecastStart := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	forecastEnd := forecastStart.Add(time.Hour)
	found := store.DisplaySessionState{
		ID: 23, Title: "Soundcheck", AudienceVisibility: "CrewOnly",
		Lifecycle: "Scheduled", ForecastStart: forecastStart, ForecastEnd: forecastEnd,
		LocationIDs: []int{8},
		PublicTime: publictime.Facts{
			Lifecycle: publictime.Scheduled,
			Forecast:  publictime.Range{Start: forecastStart, End: forecastEnd},
		},
	}

	_, selected, err := displaySession(
		store.DisplaySnapshotState{
			ViewKey:    displayviews.CrewOverview,
			LocationID: 7,
		},
		found,
		forecastStart,
		time.UTC,
	)
	if err != nil || selected {
		t.Fatalf("off-Location Crew Session selected = %v, error %v", selected, err)
	}

	session, selected, err := displaySession(
		store.DisplaySnapshotState{
			ViewKey:    displayviews.CrewOverview,
			LocationID: 8,
		},
		found,
		forecastStart,
		time.UTC,
	)
	if err != nil || !selected || session.Title != found.Title {
		t.Fatalf("assigned Crew Session = %+v, selected %v, error %v", session, selected, err)
	}
}

// TestStageTimerProjectionFollowsTheLayout pins the Stage Timer projection to the
// Layout rather than to one View key. Crew Overview carries a Stage Timer Region
// beside its other Regions, and naming the Stage Timer View directly left that
// Region reporting that no Session was live however the Event was running.
func TestStageTimerProjectionFollowsTheLayout(t *testing.T) {
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	state := func(viewKey string) store.DisplaySnapshotState {
		return store.DisplaySnapshotState{
			ViewKey:    viewKey,
			LocationID: 7,
			Sessions: []store.DisplaySessionState{{
				ID: 31, Title: "Tracked Music", Lifecycle: "Live",
				AudienceVisibility: "Public", LocationIDs: []int{7},
				ForecastStart: start, ForecastEnd: start.Add(time.Hour),
				ActualStart:     start,
				RunPlannedStart: start,
				RunPlannedEnd:   start.Add(time.Hour),
				TimingPolicy:    string(rundown.TimingFixedEnd),
			}},
		}
	}

	for _, viewKey := range []string{displayviews.StageTimer, displayviews.CrewOverview} {
		timer, ok, err := projectStageTimer(state(viewKey), displayviews.DefaultConfiguration())
		if err != nil {
			t.Fatalf("project Stage Timer for %q: %v", viewKey, err)
		}
		if !ok || timer.SessionID != 31 {
			t.Errorf("View %q Stage Timer = %+v (present %t), want the live Session", viewKey, timer, ok)
		}
	}

	// A View without a Stage Timer Region still gets nothing to render.
	if _, ok, err := projectStageTimer(
		state(displayviews.EventOverview),
		displayviews.DefaultConfiguration(),
	); err != nil || ok {
		t.Errorf("Event Overview Stage Timer present = %t, error = %v; want absent", ok, err)
	}
}

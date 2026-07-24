package displays

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/publictime"
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
	service, err := New(storage, Config{
		Now: func() time.Time { return clock }, Random: bytes.NewReader(random), EnrollmentTTL: 10 * time.Minute,
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
	if _, claimErr := service.ClaimEnrollment(t.Context(), administrator, ClaimInput{
		Code: issued.Code, Name: "Reused", CommandID: "reuse-stage-right-code",
	}); !errors.Is(claimErr, ErrEnrollmentUnavailable) {
		t.Errorf("reused Display code error = %v", claimErr)
	}
	current, err := service.Current(t.Context(), issued.Credential)
	if err != nil || current.Display != claimed || !current.Standby {
		t.Errorf("claimed Display current state = %+v, %v", current, err)
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

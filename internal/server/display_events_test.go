package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

func TestDisplayInvalidationReauthenticatesEachStream(t *testing.T) {
	application, sessionToken, installation, stream := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)
	actor, err := installation.Authentication().Authenticate(t.Context(), sessionToken)
	if err != nil {
		t.Fatalf("authenticate Administrator: %v", err)
	}
	claim := func(name string) displays.Enrollment {
		enrollment, claimErr := installation.Displays().EnrollmentForBrowser(t.Context(), "", "")
		if claimErr != nil {
			t.Fatalf("issue %s Display Enrollment: %v", name, claimErr)
		}
		_, claimErr = installation.Displays().ClaimEnrollment(
			t.Context(),
			actor,
			displays.ClaimInput{
				Code: enrollment.Code, Name: name, CommandID: "claim-" + name,
			},
		)
		if claimErr != nil {
			t.Fatalf("claim %s Display: %v", name, claimErr)
		}
		return enrollment
	}
	revoked := claim("revoked")
	authorized := claim("authorized")

	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	revokedResponse, revokedReader := openTestDisplayStream(t, client, server.URL, revoked.Credential)
	t.Cleanup(func() { _ = revokedResponse.Body.Close() })
	authorizedResponse, authorizedReader := openTestDisplayStream(
		t,
		client,
		server.URL,
		authorized.Credential,
	)
	t.Cleanup(func() { _ = authorizedResponse.Body.Close() })

	database, err := sql.Open("sqlite", filepath.Join(application.config.DataDir, "beamers.db"))
	if err != nil {
		t.Fatalf("open Display credential database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	hash := sha256.Sum256([]byte(revoked.Credential))
	result, err := database.ExecContext(
		t.Context(),
		"UPDATE display_credentials SET revoked_at = ? WHERE token_hash = ?",
		time.Now().UTC(),
		hex.EncodeToString(hash[:]),
	)
	if err != nil {
		t.Fatalf("revoke Display credential: %v", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		t.Fatalf("revoked Display credentials = %d, %v; want 1", affected, affectedErr)
	}

	stream.Notify()
	if err = readCapacityInvalidation(authorizedReader); err != nil {
		t.Fatalf("authorized Display invalidation: %v", err)
	}
	if err = readCapacityInvalidation(revokedReader); !errors.Is(err, io.EOF) {
		t.Fatalf("revoked Display invalidation error = %v, want EOF", err)
	}
}

func openTestDisplayStream(
	t *testing.T,
	client *http.Client,
	baseURL string,
	credential string,
) (*http.Response, *bufio.Reader) {
	t.Helper()
	snapshot, _, err := fetchCapacitySnapshot(t.Context(), client, baseURL, credential)
	if err != nil {
		t.Fatalf("fetch initial Display Snapshot: %v", err)
	}
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+"/display/events?stream_id="+snapshot.StreamID+
			"&after="+snapshot.StreamPosition,
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create Display stream request: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: displayCookieName, Value: credential})
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open Display stream: %v", err)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			_ = response.Body.Close()
			t.Fatalf("read initial Display heartbeat: %v", readErr)
		}
		if line == "\n" {
			return response, reader
		}
	}
}

func TestDisplayInvalidationProjectionIsShared(t *testing.T) {
	cache := &displayInvalidationCache{}
	cursor := displaystream.Cursor{StreamID: "test", Position: 1}
	var loads atomic.Int32
	var wait sync.WaitGroup
	for range 500 {
		wait.Go(func() {
			_, err := cache.current(t.Context(), cursor, func(context.Context) (displayInvalidation, error) {
				loads.Add(1)
				return displayInvalidation{PublishedRevision: 7}, nil
			})
			if err != nil {
				t.Errorf("load invalidation projection: %v", err)
			}
		})
	}
	wait.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("invalidation projection loads = %d, want 1", got)
	}
}

func BenchmarkDisplayInvalidationFanout(b *testing.B) {
	for b.Loop() {
		cache := &displayInvalidationCache{}
		cursor := displaystream.Cursor{StreamID: "benchmark", Position: 1}
		var loads atomic.Int32
		var failed atomic.Bool
		var wait sync.WaitGroup
		for range 500 {
			wait.Go(func() {
				_, err := cache.current(b.Context(), cursor, func(context.Context) (displayInvalidation, error) {
					loads.Add(1)
					return displayInvalidation{PublishedRevision: 7}, nil
				})
				if err != nil {
					failed.Store(true)
				}
			})
		}
		wait.Wait()
		if failed.Load() {
			b.Fatal("load invalidation projection")
		}
		if got := loads.Load(); got != 1 {
			b.Fatalf("invalidation projection loads = %d, want 1", got)
		}
	}
}

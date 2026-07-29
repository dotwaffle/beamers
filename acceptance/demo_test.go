package acceptance_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemoCommandStartsDisposableAndPersistentInstallations(t *testing.T) {
	bin := buildBeamers(t)
	help := exec.CommandContext(t.Context(), bin, "demo", "--help")
	helpOutput, helpErr := help.CombinedOutput()
	if helpErr != nil || !strings.Contains(string(helpOutput), `(default "0.0.0.0:8080")`) {
		t.Fatalf("demo help error = %v, output = %q", helpErr, helpOutput)
	}

	disposable := startDemo(t, bin)
	assertDemoSignIn(t, disposable.address, true)
	disposable.stop(t)
	if _, statErr := os.Stat(disposable.dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("disposable demo data remains at %q: %v", disposable.dataDir, statErr)
	}

	dataDir := filepath.Join(t.TempDir(), "demo")
	persistent := startDemo(t, bin, "--data-dir", dataDir)
	assertDemoSignIn(t, persistent.address, true)
	persistent.stop(t)
	if _, statErr := os.Stat(filepath.Join(dataDir, "beamers.db")); statErr != nil {
		t.Fatalf("persistent demo database: %v", statErr)
	}
	restarted := startBeamers(t, bin, dataDir)
	assertDemoSignIn(t, restarted.address, false)
	restarted.stop(t)

	command := exec.CommandContext(
		t.Context(),
		bin,
		"demo",
		"--data-dir", dataDir,
		"--listen", "127.0.0.1:0",
	)
	output, commandErr := command.CombinedOutput()
	if commandErr == nil || !strings.Contains(string(output), "already initialized") {
		t.Fatalf("second persistent demo error = %v, output = %q", commandErr, output)
	}
}

func assertDemoSignIn(t *testing.T, address string, expectWarning bool) {
	t.Helper()
	client := authenticatedClient(t)
	root := getFrontendPage(t, client, address, "/")
	if root.status != http.StatusOK ||
		strings.Contains(root.body, "Security warning") != expectWarning ||
		!strings.Contains(strings.ToLower(root.body), "demo") ||
		!strings.Contains(root.body, `href="/events/revision-demo"`) {
		t.Fatalf("demo root = %d %q", root.status, root.body)
	}
	schedule := getFrontendPage(t, client, address, "/events/revision-demo/schedule")
	for _, want := range []string{
		"Revision Demo",
		"Opening",
		"Shader Showdown",
		"Tracked Music",
		"Closing",
		"2099-08-21",
		"2099-08-22",
		"Main Hall",
		"Main Stage",
		"General",
		`href="/events/revision-demo/schedule/sessions/1"`,
	} {
		if schedule.status != http.StatusOK || !strings.Contains(schedule.body, want) {
			t.Fatalf("demo Schedule lacks %q: %d %q", want, schedule.status, schedule.body)
		}
	}
	released := getFrontendPage(t, client, address, "/events/revision-demo/results")
	for _, want := range []string{"Oldschool Demo", "Oldschool Demo Entry"} {
		if released.status != http.StatusOK || !strings.Contains(released.body, want) {
			t.Fatalf("demo Results lack %q: %d %q", want, released.status, released.body)
		}
	}
	for handle, displayName := range map[string]string{
		"administrator": "Demo Administrator",
		"attendee":      "Demo Attendee",
		"observer":      "Demo Observer",
		"operator":      "Demo Operator",
		"producer":      "Demo Producer",
		"voter":         "Demo Voter",
	} {
		client := authenticatedClient(t)
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		signInPage := getFrontendPage(t, client, address, "/sign-in")
		signIn := postFrontendForm(t, client, address, "/sign-in", url.Values{
			"csrf_token": {requireFrontendCSRF(t, signInPage)},
			"handle":     {handle},
			"password":   {"demo"},
		})
		if signIn.status != http.StatusSeeOther {
			t.Fatalf("%s demo sign-in = %d %q", handle, signIn.status, signIn.body)
		}
		signedIn := getFrontendPage(t, client, address, "/")
		if !strings.Contains(signedIn.body, displayName) {
			t.Fatalf("%s signed-in root = %q", handle, signedIn.body)
		}
		for _, href := range []string{"/profile", "/my-participation"} {
			if !strings.Contains(signedIn.body, `href="`+href+`"`) {
				t.Fatalf("%s signed-in root lacks %s navigation", handle, href)
			}
		}
		hasBackstage := strings.Contains(signedIn.body, `href="/backstage"`)
		wantBackstage := handle == "administrator" || handle == "observer" ||
			handle == "operator" || handle == "producer"
		if hasBackstage != wantBackstage {
			t.Fatalf(
				"%s Backstage navigation = %t, want %t",
				handle,
				hasBackstage,
				wantBackstage,
			)
		}
		assertDemoAccountJourney(t, client, address, handle)
	}
}

func assertDemoAccountJourney(t *testing.T, client *http.Client, address, handle string) {
	t.Helper()
	type pageExpectation struct {
		path  string
		wants []string
	}
	var pages []pageExpectation
	switch handle {
	case "administrator":
		pages = []pageExpectation{
			{"/backstage/themes", []string{"Active:", "Theme Revision #1", "— active"}},
			{"/admin/audit", []string{"CreateEvent", "RedeemVotingKey", "ReleaseStandaloneResults"}},
		}
	case "attendee":
		pages = []pageExpectation{
			{"/my-schedule", []string{"My Schedule", "Opening"}},
			{"/my-participation", []string{"Attendee Shader", "attendee-shader.zip", "Create Entry"}},
		}
	case "operator":
		pages = []pageExpectation{
			{"/backstage/events/1/operations", []string{"Tracked Music", "Main Hall Display", "main-stage"}},
		}
	case "producer":
		pages = []pageExpectation{
			{"/backstage/events/1/competitions/2/entries", []string{"Attendee Shader", "attendee-shader.zip"}},
			{"/backstage/events/1/results", []string{"Oldschool Demo", "Final"}},
			{"/backstage/events/1/theme", []string{"Active:", "Event Theme Revision #1", "— active"}},
			{"/backstage/events/1/voting-keys", []string{"Key inventory", "2099", "Redeemed", "Available"}},
		}
	case "voter":
		pages = []pageExpectation{
			{"/voting", []string{"Open Ballots", "Tracked Music"}},
			{"/voting/3?event_id=1", []string{"Tracked Music Ballot", "Tracked Music Entry", "Save Vote"}},
		}
	}
	for _, page := range pages {
		found := getFrontendPage(t, client, address, page.path)
		for _, want := range page.wants {
			if found.status != http.StatusOK || !strings.Contains(found.body, want) {
				t.Fatalf("%s %s lacks %q: %d %q", handle, page.path, want, found.status, found.body)
			}
		}
	}
}

func startDemo(t *testing.T, bin string, args ...string) *runningServer {
	t.Helper()
	commandArgs := append([]string{"demo", "--listen", "0.0.0.0:0"}, args...)
	command := exec.CommandContext(t.Context(), bin, commandArgs...)
	stderr, pipeErr := command.StderrPipe()
	if pipeErr != nil {
		t.Fatalf("capture demo stderr: %v", pipeErr)
	}
	if startErr := command.Start(); startErr != nil {
		t.Fatalf("start demo: %v", startErr)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	type startup struct {
		address, dataDir string
		warned           bool
		err              error
	}
	started := make(chan startup, 1)
	go func() {
		var found startup
		sent := false
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
				DataDir string `json:"data_dir"`
				Level   string `json:"level"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			if entry.Message == "demo mode enabled" {
				found.dataDir = entry.DataDir
				found.warned = entry.Level == "WARN"
			}
			if entry.Message == "server listening" {
				found.address = entry.Address
			}
			if !sent && found.address != "" && found.dataDir != "" && found.warned {
				started <- found
				sent = true
			}
		}
		if !sent {
			found.err = scanner.Err()
			started <- found
		}
	}()
	select {
	case found := <-started:
		if found.err != nil || found.address == "" || found.dataDir == "" || !found.warned {
			t.Fatalf("demo startup = %+v", found)
		}
		host, port, splitErr := net.SplitHostPort(found.address)
		boundIP := net.ParseIP(host)
		if splitErr != nil || boundIP == nil || !boundIP.IsUnspecified() {
			t.Fatalf("demo listen address = %q, error = %v", found.address, splitErr)
		}
		found.address = net.JoinHostPort("127.0.0.1", port)
		server := &runningServer{
			address: found.address,
			bin:     bin,
			dataDir: found.dataDir,
			cmd:     command,
			done:    done,
		}
		t.Cleanup(func() {
			if server.cmd.Process != nil {
				_ = server.cmd.Process.Kill()
			}
		})
		return server
	case commandErr := <-done:
		t.Fatalf("demo exited during startup: %v", commandErr)
	case <-time.After(10 * time.Second):
		t.Fatal("demo did not announce startup")
	}
	return nil
}

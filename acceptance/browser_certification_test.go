package acceptance_test

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
)

//go:embed browser_audit.js
var browserAuditJavaScript string

type webDriver struct {
	client         *http.Client
	endpoint       string
	sessionID      string
	browserVersion string
}

type browserPageEvidence struct {
	Surface           string   `json:"surface"`
	Title             string   `json:"title"`
	Language          string   `json:"language"`
	Main              bool     `json:"main"`
	Heading           bool     `json:"heading"`
	Structure         bool     `json:"structure"`
	KeyboardOperable  bool     `json:"keyboard_operable"`
	FocusVisible      bool     `json:"focus_visible"`
	ReducedMotion     bool     `json:"reduced_motion"`
	NonColorStatus    bool     `json:"non_color_status"`
	UnlabeledControls []string `json:"unlabeled_controls,omitempty"`
	SmallTargets      []string `json:"small_targets,omitempty"`
	ContrastFailures  []string `json:"contrast_failures,omitempty"`
}

type browserCertificationConfig struct {
	Engine            string
	Role              string
	ExpectedMajor     int
	BrowserBinary     string
	WebDriverBinary   string
	WebDriverEndpoint string
	WebDriverVersion  string
	ReportPath        string
}

type browserProgramOutputEvidence struct {
	Kind     string `json:"kind"`
	EntryID  string `json:"entry_id,omitempty"`
	Title    string `json:"title"`
	Revision string `json:"revision"`
}

type browserDisplayOutputEvidence struct {
	DisplayID      string                       `json:"display_id"`
	ProgramOutput  browserProgramOutputEvidence `json:"program_output"`
	StreamID       string                       `json:"stream_id"`
	StreamPosition string                       `json:"stream_position"`
	Acknowledged   bool                         `json:"acknowledged"`
}

type browserDisplayCertification struct {
	driverEndpoint string
	enrollmentCode string
	credential     string
	driver         *webDriver
	committedFrame string
}

type browserCertificationReport struct {
	Commit                         string                         `json:"commit"`
	Engine                         string                         `json:"engine"`
	Role                           string                         `json:"role"`
	BrowserVersion                 string                         `json:"browser_version"`
	WebDriverVersion               string                         `json:"webdriver_version"`
	GitHubRunID                    string                         `json:"github_run_id,omitempty"`
	RunnerOS                       string                         `json:"runner_os,omitempty"`
	RunnerArchitecture             string                         `json:"runner_architecture,omitempty"`
	GeneratedAt                    time.Time                      `json:"generated_at"`
	Pages                          []browserPageEvidence          `json:"pages"`
	CrewCommandCommitted           bool                           `json:"crew_command_committed"`
	DisplaysConnectedBeforeCommand []string                       `json:"displays_connected_before_command"`
	ProgramOutput                  browserProgramOutputEvidence   `json:"program_output"`
	Displays                       []browserDisplayOutputEvidence `json:"displays"`
	DisplayRetainedFrame           bool                           `json:"display_retained_frame"`
	DisplayReconnected             bool                           `json:"display_reconnected"`
	KioskEvidence                  bool                           `json:"kiosk_evidence"`
	ManualAccessibility            bool                           `json:"manual_accessibility"`
}

func (report browserCertificationReport) validate() error {
	commit, err := hex.DecodeString(report.Commit)
	if err != nil || len(commit) != 20 {
		return errors.New("certified commit is not a full Git SHA")
	}
	switch {
	case !report.CrewCommandCommitted:
		return errors.New("Crew command was not committed")
	case len(report.DisplaysConnectedBeforeCommand) != 2:
		return fmt.Errorf("Displays connected before command = %d, want 2",
			len(report.DisplaysConnectedBeforeCommand))
	case report.ProgramOutput.Kind == "" ||
		report.ProgramOutput.Title == "" ||
		report.ProgramOutput.Revision == "":
		return errors.New("Program Output is missing")
	case len(report.Displays) != 2:
		return fmt.Errorf("Display outputs = %d, want 2", len(report.Displays))
	case !report.DisplayRetainedFrame:
		return errors.New("Display did not retain its committed frame")
	case !report.DisplayReconnected:
		return errors.New("Display did not reconnect after restart")
	}
	connected := make(map[string]bool, 2)
	for _, displayID := range report.DisplaysConnectedBeforeCommand {
		if displayID == "" || connected[displayID] {
			return errors.New("connected Display identities are incomplete")
		}
		connected[displayID] = true
	}
	seen := make(map[string]bool, 2)
	for _, display := range report.Displays {
		switch {
		case !connected[display.DisplayID], seen[display.DisplayID]:
			return errors.New("Display output identity was not connected before command")
		case display.ProgramOutput != report.ProgramOutput:
			return errors.New("Display output does not match Program Output")
		case display.StreamID == "" || display.StreamPosition == "":
			return errors.New("Display output cursor is missing")
		case !display.Acknowledged:
			return errors.New("Display output was not acknowledged")
		}
		seen[display.DisplayID] = true
	}
	pageCounts := make(map[string]int)
	for _, page := range report.Pages {
		if err := page.validate(); err != nil {
			return fmt.Errorf("validate %s page evidence: %w", page.Surface, err)
		}
		pageCounts[page.Surface]++
	}
	for _, required := range []struct {
		surface string
		count   int
	}{
		{"demo-anonymous", 1},
		{"demo-display", 1},
		{"demo-results", 1},
		{"demo-attendee", 1},
		{"demo-submission", 1},
		{"demo-voter", 1},
		{"demo-producer", 1},
		{"demo-administrator", 1},
		{"demo-operator", 1},
		{"frontend", 1},
		{"event", 1},
		{"schedule", 1},
		{"results", 1},
		{"backstage", 1},
		{"installation", 1},
		{"planning", 1},
		{"operations", 1},
		{"results-management", 1},
		{"administration", 1},
		{"voting-keys", 1},
		{"final-files", 1},
		{"enrollment", 1},
		{"display", 2},
		{"crew_control", 2},
		{"control", 1},
		{"override_confirmation", 1},
	} {
		if pageCounts[required.surface] != required.count {
			return fmt.Errorf(
				"%s page evidence = %d, want %d",
				required.surface,
				pageCounts[required.surface],
				required.count,
			)
		}
	}
	return nil
}

func TestBrowserCertificationReportRequiresDurableTwoDisplayTake(t *testing.T) {
	validReport := func() browserCertificationReport {
		programOutput := browserProgramOutputEvidence{
			Kind: "PROGRAM_ITEM_KIND_STARTING", Title: "Opening Keynote", Revision: "1",
		}
		return browserCertificationReport{
			Commit:                         strings.Repeat("a", 40),
			CrewCommandCommitted:           true,
			DisplaysConnectedBeforeCommand: []string{"1", "2"},
			ProgramOutput:                  programOutput,
			Displays: []browserDisplayOutputEvidence{
				{
					DisplayID: "1", ProgramOutput: programOutput,
					StreamID: "stream", StreamPosition: "1", Acknowledged: true,
				},
				{
					DisplayID: "2", ProgramOutput: programOutput,
					StreamID: "stream", StreamPosition: "1", Acknowledged: true,
				},
			},
			Pages: []browserPageEvidence{
				validBrowserPageEvidence("demo-anonymous"),
				validBrowserPageEvidence("demo-display"),
				validBrowserPageEvidence("demo-results"),
				validBrowserPageEvidence("demo-attendee"),
				validBrowserPageEvidence("demo-submission"),
				validBrowserPageEvidence("demo-voter"),
				validBrowserPageEvidence("demo-producer"),
				validBrowserPageEvidence("demo-administrator"),
				validBrowserPageEvidence("demo-operator"),
				validBrowserPageEvidence("frontend"),
				validBrowserPageEvidence("event"),
				validBrowserPageEvidence("schedule"),
				validBrowserPageEvidence("backstage"),
				validBrowserPageEvidence("installation"),
				validBrowserPageEvidence("planning"),
				validBrowserPageEvidence("operations"),
				validBrowserPageEvidence("results-management"),
				validBrowserPageEvidence("administration"),
				validBrowserPageEvidence("voting-keys"),
				validBrowserPageEvidence("final-files"),
				validBrowserPageEvidence("crew_control"),
				validBrowserPageEvidence("crew_control"),
				validBrowserPageEvidence("control"),
				validBrowserPageEvidence("override_confirmation"),
				validBrowserPageEvidence("display"),
				validBrowserPageEvidence("display"),
				validBrowserPageEvidence("enrollment"),
				validBrowserPageEvidence("results"),
			},
			DisplayRetainedFrame: true,
			DisplayReconnected:   true,
		}
	}
	if err := validReport().validate(); err != nil {
		t.Fatalf("complete browser certification report: %v", err)
	}

	tests := map[string]func(*browserCertificationReport){
		"missing certified commit": func(report *browserCertificationReport) {
			report.Commit = ""
		},
		"invalid certified commit": func(report *browserCertificationReport) {
			report.Commit = "deadbeef"
		},
		"missing Crew command": func(report *browserCertificationReport) {
			report.CrewCommandCommitted = false
		},
		"missing connected Display": func(report *browserCertificationReport) {
			report.DisplaysConnectedBeforeCommand = report.DisplaysConnectedBeforeCommand[:1]
		},
		"duplicate connected Display": func(report *browserCertificationReport) {
			report.DisplaysConnectedBeforeCommand[1] = "1"
		},
		"empty connected Display": func(report *browserCertificationReport) {
			report.DisplaysConnectedBeforeCommand[1] = ""
		},
		"missing Program Output": func(report *browserCertificationReport) {
			report.ProgramOutput = browserProgramOutputEvidence{}
		},
		"missing Display output": func(report *browserCertificationReport) {
			report.Displays = report.Displays[:1]
		},
		"unconnected Display output": func(report *browserCertificationReport) {
			report.Displays[1].DisplayID = "3"
		},
		"duplicate Display output": func(report *browserCertificationReport) {
			report.Displays[1].DisplayID = "1"
		},
		"mismatched Display output": func(report *browserCertificationReport) {
			report.Displays[1].ProgramOutput.Title = "Standby"
		},
		"missing Display cursor": func(report *browserCertificationReport) {
			report.Displays[1].StreamPosition = ""
		},
		"missing acknowledgment": func(report *browserCertificationReport) {
			report.Displays[1].Acknowledged = false
		},
		"missing browser evidence": func(report *browserCertificationReport) {
			report.Pages = nil
		},
		"missing demo voter evidence": func(report *browserCertificationReport) {
			for index, page := range report.Pages {
				if page.Surface == "demo-voter" {
					report.Pages = append(report.Pages[:index], report.Pages[index+1:]...)
					return
				}
			}
		},
		"missing table surface evidence": func(report *browserCertificationReport) {
			for index, page := range report.Pages {
				if page.Surface == "administration" {
					report.Pages = append(report.Pages[:index], report.Pages[index+1:]...)
					return
				}
			}
		},
		"missing enrollment evidence": func(report *browserCertificationReport) {
			report.Pages = report.Pages[:3]
		},
		"invalid browser evidence": func(report *browserCertificationReport) {
			report.Pages[0].Title = ""
		},
		"missing retained frame": func(report *browserCertificationReport) {
			report.DisplayRetainedFrame = false
		},
		"missing reconnect": func(report *browserCertificationReport) {
			report.DisplayReconnected = false
		},
	}
	for name, breakReport := range tests {
		t.Run(name, func(t *testing.T) {
			report := validReport()
			breakReport(&report)
			if err := report.validate(); err == nil {
				t.Fatal("browser certification report accepted incomplete evidence")
			}
		})
	}
}

func TestBrowserCertificationWorktreeMustBeClean(t *testing.T) {
	t.Parallel()
	if err := validateBrowserCertificationWorktree(nil); err != nil {
		t.Fatalf("clean worktree rejected: %v", err)
	}
	if err := validateBrowserCertificationWorktree([]byte(" M acceptance/test.go\n")); err == nil {
		t.Fatal("dirty worktree accepted")
	}
	if err := validateBrowserCertificationWorktree([]byte("?? local.go\n")); err == nil {
		t.Fatal("untracked source accepted")
	}
}

func validBrowserPageEvidence(surface string) browserPageEvidence {
	evidence := browserPageEvidence{
		Surface: surface, Title: "Evidence", Language: "en", Main: true, Heading: true,
		Structure: true,
	}
	if surface == "display" || surface == "demo-display" {
		evidence.Structure = false
		evidence.ReducedMotion = true
		evidence.NonColorStatus = true
	} else {
		evidence.KeyboardOperable = true
		evidence.FocusVisible = true
	}
	return evidence
}

func (evidence browserPageEvidence) validate() error {
	var findings []string
	if evidence.Title == "" {
		findings = append(findings, "missing title")
	}
	if evidence.Language == "" {
		findings = append(findings, "missing language")
	}
	if !evidence.Main {
		findings = append(findings, "missing main landmark")
	}
	if !evidence.Heading {
		findings = append(findings, "missing heading")
	}
	switch evidence.Surface {
	case "display":
		if !evidence.ReducedMotion {
			findings = append(findings, "missing reduced motion")
		}
		if !evidence.NonColorStatus {
			findings = append(findings, "missing non-color status")
		}
	case "demo-display":
		if !evidence.ReducedMotion {
			findings = append(findings, "missing reduced motion")
		}
	default:
		if !evidence.Structure {
			findings = append(findings, "invalid page structure")
		}
		if !evidence.KeyboardOperable {
			findings = append(findings, "missing keyboard operation")
		}
		if !evidence.FocusVisible {
			findings = append(findings, "missing visible focus")
		}
	}
	if len(evidence.UnlabeledControls) > 0 {
		findings = append(
			findings,
			"unlabeled controls: "+strings.Join(evidence.UnlabeledControls, ", "),
		)
	}
	if len(evidence.SmallTargets) > 0 {
		findings = append(
			findings,
			"small targets: "+strings.Join(evidence.SmallTargets, ", "),
		)
	}
	if len(evidence.ContrastFailures) > 0 {
		findings = append(
			findings,
			"contrast failures: "+strings.Join(evidence.ContrastFailures, ", "),
		)
	}
	if len(findings) == 0 {
		return nil
	}
	return errors.New(strings.Join(findings, "; "))
}

func newWebDriver(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	capabilities map[string]any,
) (*webDriver, error) {
	driver := &webDriver{client: client, endpoint: strings.TrimRight(endpoint, "/")}
	value, err := driver.command(ctx, http.MethodPost, "/session", map[string]any{
		"capabilities": map[string]any{"alwaysMatch": capabilities},
	})
	if err != nil {
		return nil, err
	}
	var session struct {
		SessionID    string `json:"sessionId"`
		Capabilities struct {
			BrowserVersion string `json:"browserVersion"`
		} `json:"capabilities"`
	}
	if err = json.Unmarshal(value, &session); err != nil {
		return nil, fmt.Errorf("decode WebDriver session: %w", err)
	}
	if session.SessionID == "" || session.Capabilities.BrowserVersion == "" {
		return nil, errors.New("WebDriver returned an incomplete session")
	}
	driver.sessionID = session.SessionID
	driver.browserVersion = session.Capabilities.BrowserVersion
	return driver, nil
}

func (driver *webDriver) command(
	ctx context.Context,
	method string,
	path string,
	input any,
) (json.RawMessage, error) {
	var body io.Reader = http.NoBody
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encode WebDriver request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		driver.endpoint+path,
		body,
	)
	if err != nil {
		return nil, fmt.Errorf("create WebDriver request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := driver.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send WebDriver request: %w", err)
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read WebDriver response: %w", err)
	}
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode WebDriver response: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		var protocolError struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err = json.Unmarshal(envelope.Value, &protocolError); err != nil {
			return nil, fmt.Errorf("decode WebDriver protocol error: %w", err)
		}
		return nil, fmt.Errorf(
			"WebDriver %s: %s",
			protocolError.Error,
			protocolError.Message,
		)
	}
	return envelope.Value, nil
}

func (driver *webDriver) auditPage(
	ctx context.Context,
	surface string,
) (browserPageEvidence, error) {
	value, err := driver.command(
		ctx,
		http.MethodPost,
		"/session/"+url.PathEscape(driver.sessionID)+"/execute/sync",
		map[string]any{
			"script": "return (" + browserAuditJavaScript + ")(arguments[0]);",
			"args":   []any{surface},
		},
	)
	if err != nil {
		return browserPageEvidence{}, err
	}
	var evidence browserPageEvidence
	if err = json.Unmarshal(value, &evidence); err != nil {
		return browserPageEvidence{}, fmt.Errorf("decode browser page evidence: %w", err)
	}
	if err = evidence.validate(); err != nil {
		return browserPageEvidence{}, fmt.Errorf("%s accessibility: %w", surface, err)
	}
	return evidence, nil
}

func (driver *webDriver) navigate(ctx context.Context, target string) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/url"),
		map[string]string{"url": target},
	)
	return err
}

func (driver *webDriver) setWindowSize(ctx context.Context, width int) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/window/rect"),
		map[string]int{"width": width, "height": 900},
	)
	return err
}

func (driver *webDriver) addCookie(ctx context.Context, cookie *http.Cookie) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/cookie"),
		map[string]any{"cookie": map[string]any{
			"name":     cookie.Name,
			"value":    cookie.Value,
			"path":     cookie.Path,
			"secure":   cookie.Secure,
			"httpOnly": cookie.HttpOnly,
		}},
	)
	return err
}

func (driver *webDriver) pressKey(ctx context.Context, key string) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/actions"),
		map[string]any{"actions": []any{map[string]any{
			"type": "key",
			"id":   "keyboard",
			"actions": []any{
				map[string]string{"type": "keyDown", "value": key},
				map[string]string{"type": "keyUp", "value": key},
			},
		}}},
	)
	return err
}

func (driver *webDriver) evaluateBool(
	ctx context.Context,
	script string,
	args ...any,
) (bool, error) {
	value, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/execute/sync"),
		map[string]any{"script": script, "args": append([]any{}, args...)},
	)
	if err != nil {
		return false, err
	}
	var found bool
	if err = json.Unmarshal(value, &found); err != nil {
		return false, fmt.Errorf("decode WebDriver boolean: %w", err)
	}
	return found, nil
}

func (driver *webDriver) evaluateString(
	ctx context.Context,
	script string,
) (string, error) {
	value, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/execute/sync"),
		map[string]any{"script": script, "args": []any{}},
	)
	if err != nil {
		return "", err
	}
	var found string
	if err = json.Unmarshal(value, &found); err != nil {
		return "", fmt.Errorf("decode WebDriver string: %w", err)
	}
	return found, nil
}

func (driver *webDriver) waitFor(
	ctx context.Context,
	timeout time.Duration,
	script string,
	args ...any,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		found, err := driver.evaluateBool(ctx, script, args...)
		if err == nil && found {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return errors.Join(ctx.Err(), err)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (driver *webDriver) execute(ctx context.Context, script string, args ...any) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/execute/sync"),
		map[string]any{"script": script, "args": append([]any{}, args...)},
	)
	return err
}

func (driver *webDriver) addVirtualAuthenticator(
	ctx context.Context,
	transport string,
) (string, error) {
	value, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/webauthn/authenticator"),
		map[string]any{
			"protocol":            "ctap2",
			"transport":           transport,
			"hasResidentKey":      true,
			"hasUserVerification": true,
			"isUserConsenting":    true,
			"isUserVerified":      true,
		},
	)
	if err != nil {
		return "", err
	}
	var authenticatorID string
	if err = json.Unmarshal(value, &authenticatorID); err != nil {
		return "", fmt.Errorf("decode virtual authenticator ID: %w", err)
	}
	if authenticatorID == "" {
		return "", errors.New("WebDriver returned an empty virtual authenticator ID")
	}
	return authenticatorID, nil
}

func (driver *webDriver) removeVirtualAuthenticator(
	ctx context.Context,
	authenticatorID string,
) error {
	_, err := driver.command(
		ctx,
		http.MethodDelete,
		driver.sessionPath("/webauthn/authenticator/")+url.PathEscape(authenticatorID),
		nil,
	)
	return err
}

func (driver *webDriver) deleteCookie(ctx context.Context, name string) error {
	_, err := driver.command(
		ctx,
		http.MethodDelete,
		driver.sessionPath("/cookie/")+url.PathEscape(name),
		nil,
	)
	return err
}

func (driver *webDriver) close(ctx context.Context) error {
	_, err := driver.command(ctx, http.MethodDelete, driver.sessionPath(""), nil)
	if err == nil {
		driver.sessionID = ""
	}
	return err
}

func (driver *webDriver) sessionPath(suffix string) string {
	return "/session/" + url.PathEscape(driver.sessionID) + suffix
}

func TestBrowserCertification(t *testing.T) {
	if os.Getenv("BEAMERS_BROWSER_CERTIFICATION") != "1" {
		t.Skip("set BEAMERS_BROWSER_CERTIFICATION=1 to run real-browser certification")
	}
	config := browserConfigFromEnvironment(t)
	crewDriverEndpoint, webDriverVersion := startBrowserDriver(t, config)
	secondCrewDriverEndpoint, _ := startBrowserDriver(t, config)
	displays := make([]browserDisplayCertification, 2)
	for index := range displays {
		displays[index].driverEndpoint, _ = startBrowserDriver(t, config)
	}
	client := &http.Client{Timeout: 10 * time.Second}

	bin := buildBrowserBeamers(t, "browser-certification")
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	bootstrapToken := strings.TrimSpace(
		runBeamersOutput(t, bin, "bootstrap", "--data-dir", dataDir),
	)
	administrator := authenticatedClient(t)
	server := startBeamers(t, bin, dataDir)
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/auth/bootstrap",
		map[string]string{
			"bootstrap_token": bootstrapToken,
			"name":            "Ada Admin",
			"password":        "correct horse battery staple",
		},
		http.StatusCreated,
		"",
	)
	publicSessionID := prepareActiveSchedule(t, administrator, server)
	crewSessionID, _ := addCompetitionSession(t, administrator, server)
	prepareReleasedBrowserResults(t, administrator, server, crewSessionID)
	prizegivingSessionID := prepareBrowserPrizegiving(t, administrator, server)
	for index := range displays {
		displays[index].enrollmentCode, displays[index].credential =
			prepareBrowserEnrollment(t, server)
	}
	_, browserPort, splitErr := net.SplitHostPort(server.address)
	if splitErr != nil {
		t.Fatalf("split browser server address: %v", splitErr)
	}
	browserOrigin := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("localhost", browserPort),
	}
	origin := browserOrigin.String()
	administrator.Jar.SetCookies(
		browserOrigin,
		administrator.Jar.Cookies(&url.URL{Scheme: "http", Host: server.address}),
	)

	report := browserCertificationReport{
		Commit:             browserCertificationCommit(t),
		Engine:             config.Engine,
		Role:               config.Role,
		WebDriverVersion:   webDriverVersion,
		GitHubRunID:        os.Getenv("GITHUB_RUN_ID"),
		RunnerOS:           os.Getenv("RUNNER_OS"),
		RunnerArchitecture: os.Getenv("RUNNER_ARCH"),
		GeneratedAt:        time.Now().UTC(),
	}
	report.Pages = append(
		report.Pages,
		certifyDemoBrowserJourneys(t, client, crewDriverEndpoint, config, bin)...,
	)
	crewDriver := startBrowserSession(t, client, crewDriverEndpoint, config)
	report.BrowserVersion = crewDriver.browserVersion
	if major := browserMajor(t, crewDriver.browserVersion); major != config.ExpectedMajor {
		t.Fatalf(
			"browser major = %d from %q, want %d",
			major,
			crewDriver.browserVersion,
			config.ExpectedMajor,
		)
	}
	assertResponsivePageWidths(t, crewDriver, origin+"/", 320, 375, 768, 1024, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/events/revision-2099", 320, 1440)
	assertResponsivePageWidths(
		t,
		crewDriver,
		origin+"/events/revision-2099/schedule",
		320,
		1440,
	)
	assertResponsivePageWidths(
		t,
		crewDriver,
		origin+"/events/revision-2099/results",
		320,
		1440,
	)
	assertResponsivePageZoom(t, crewDriver, origin+"/events/revision-2099/schedule", 1024, 2)
	assertResponsivePageZoom(t, crewDriver, origin+"/events/revision-2099/schedule", 1280, 4)
	certifyLiveScheduleUpdate(
		t,
		crewDriver,
		administrator,
		server,
		publicSessionID,
	)
	report.Pages = append(
		report.Pages,
		certifyFrontendTheme(t, crewDriver, origin),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/events/revision-2099",
			"event",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/events/revision-2099/schedule",
			"schedule",
		),
		certifyResultsPage(t, crewDriver, origin),
	)
	addBrowserCookie(t, crewDriver, browserCookie(
		t,
		administrator,
		origin,
		"beamers_session",
		"/",
	))
	assertEventNavigationModes(t, crewDriver, origin)
	certifyFrontendNavigationJourney(
		t,
		crewDriver,
		origin,
		publicSessionID,
		crewSessionID,
	)
	certifyWebAuthnAvailability(t, crewDriver, origin)
	if config.Engine == "chromium" {
		certifyWebAuthnCeremonies(t, crewDriver, origin)
	}
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/installation", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/planning", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/operations", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/control", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/results", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/administration", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/voting-keys", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/final-files", 320, 1440)
	assertResponsivePageZoom(t, crewDriver, origin+"/backstage/events/1/results", 1280, 4)
	assertBackstageNavigationModes(t, crewDriver, origin)
	report.Pages = append(
		report.Pages,
		certifyInteractivePage(t, crewDriver, origin+"/backstage", "backstage"),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/installation",
			"installation",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/planning",
			"planning",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/operations",
			"operations",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/results",
			"results-management",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/administration",
			"administration",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/voting-keys",
			"voting-keys",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/final-files",
			"final-files",
		),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/admin/displays/enroll?code="+url.QueryEscape(displays[0].enrollmentCode),
			"enrollment",
		),
	)
	for index, display := range displays {
		claimBrowserEnrollment(
			t, administrator, server, display.enrollmentCode, index+1,
			fmt.Sprintf("Browser Certification %d", index+1),
		)
	}

	for index := range displays {
		report.Pages = append(report.Pages, startCertifiedBrowserDisplay(
			t, client, config, origin, &displays[index],
		))
	}
	secondCrewDriver, secondCrewEvidence := openSecondaryCrewControl(
		t,
		client,
		config,
		secondCrewDriverEndpoint,
		administrator,
		origin,
		prizegivingSessionID,
	)
	report.Pages = append(report.Pages, secondCrewEvidence)
	crewEvidence, connectedDisplayIDs, programOutput := certifyCrewControl(
		t, crewDriver, origin, prizegivingSessionID,
	)
	report.Pages = append(report.Pages, crewEvidence)
	report.CrewCommandCommitted = true
	report.DisplaysConnectedBeforeCommand = connectedDisplayIDs
	report.ProgramOutput = programOutput
	assertSecondaryCrewControl(t, secondCrewDriver, programOutput)
	closeBrowserSession(t, secondCrewDriver)
	programClient := beginBrowserReveal(
		t, administrator, server, prizegivingSessionID,
	)
	overrideDriver := startBrowserSession(
		t, client, secondCrewDriverEndpoint, config,
	)
	if err := overrideDriver.navigate(t.Context(), origin+"/events/revision-2099/schedule"); err != nil {
		t.Fatalf("navigate Override console to cookie origin: %v", err)
	}
	addBrowserCookie(t, overrideDriver, browserCookie(
		t,
		administrator,
		origin,
		"beamers_session",
		"/",
	))
	controlEvidence, confirmationEvidence, programOutput := certifyOverrideConfirmation(
		t,
		overrideDriver,
		origin,
		displays,
		programClient,
		prizegivingSessionID,
	)
	report.Pages = append(report.Pages, controlEvidence, confirmationEvidence)
	report.ProgramOutput = programOutput
	closeBrowserSession(t, overrideDriver)

	for index := range displays {
		report.Displays = append(report.Displays, captureBrowserDisplayOutput(
			t, &displays[index], programOutput,
		))
	}
	crewURL := origin + "/crew/program/" + strconv.FormatInt(prizegivingSessionID, 10) +
		"?event_id=1"
	acknowledgmentsReady := `return ` +
		`[...document.querySelectorAll("#displays li[data-delivery]")].length === 2 && ` +
		`[...document.querySelectorAll("#displays li[data-delivery]")].every(` +
		`(display) => display.dataset.delivery === "applied");`
	for deadline := time.Now().Add(15 * time.Second); ; {
		err := crewDriver.navigate(t.Context(), crewURL)
		if err == nil {
			err = crewDriver.waitFor(
				t.Context(),
				time.Until(deadline),
				`return document.querySelector("#connection-status").textContent !== `+
					`"Loading authoritative state…";`,
			)
		}
		if err == nil {
			var ready bool
			ready, err = crewDriver.evaluateBool(t.Context(), acknowledgmentsReady)
			if err == nil && ready {
				break
			}
			if err == nil {
				err = errors.New("acknowledgments not ready")
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for two Display acknowledgments: %v", err)
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("wait for two Display acknowledgments: %v", t.Context().Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	acknowledgedJSON, err := crewDriver.evaluateString(
		t.Context(),
		`return JSON.stringify([...document.querySelectorAll(`+
			`"#displays li[data-display-id][data-delivery=applied]")].map(`+
			`(display) => display.dataset.displayId));`,
	)
	if err != nil {
		t.Fatalf("read acknowledged Displays: %v", err)
	}
	var acknowledgedDisplayIDs []string
	if err = json.Unmarshal([]byte(acknowledgedJSON), &acknowledgedDisplayIDs); err != nil {
		t.Fatalf("decode acknowledged Displays: %v", err)
	}
	acknowledged := make(map[string]bool, len(acknowledgedDisplayIDs))
	for _, displayID := range acknowledgedDisplayIDs {
		acknowledged[displayID] = true
	}
	for index := range report.Displays {
		report.Displays[index].Acknowledged = acknowledged[report.Displays[index].DisplayID]
	}
	closeBrowserSession(t, crewDriver)
	forcedColorsDriver := startForcedColorsBrowserSession(
		t,
		client,
		crewDriverEndpoint,
		config,
	)
	certifyForcedColors(t, forcedColorsDriver, origin)
	closeBrowserSession(t, forcedColorsDriver)

	server.stop(t)
	for index := range displays {
		assertBrowserDisplayFrame(
			t, &displays[index], "disconnected", 15*time.Second, "while disconnected",
		)
	}
	report.DisplayRetainedFrame = true

	restarted := startBeamersAt(t, bin, dataDir, server.address)
	for index := range displays {
		display := &displays[index]
		assertBrowserDisplayFrame(
			t, display, "connected", 30*time.Second, "after compatible restart",
		)
		closeBrowserSession(t, display.driver)
	}
	report.DisplayReconnected = true
	restarted.stop(t)
	writeBrowserCertificationReport(t, config.ReportPath, report)
}

func certifyWebAuthnAvailability(t *testing.T, driver *webDriver, origin string) {
	t.Helper()
	if err := driver.navigate(t.Context(), origin+"/profile"); err != nil {
		t.Fatalf("navigate to WebAuthn Profile: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return window.isSecureContext && `+
			`typeof PublicKeyCredential === "function" && `+
			`document.querySelector("[data-webauthn-register]").hidden === false && `+
			`document.querySelector("[data-webauthn-status]").textContent === `+
			`"WebAuthn is available.";`,
	); err != nil {
		t.Fatalf("certify secure-context WebAuthn availability: %v", err)
	}
}

func certifyWebAuthnCeremonies(t *testing.T, driver *webDriver, origin string) {
	t.Helper()
	register := func(name, transport string) string {
		t.Helper()
		authenticatorID, err := driver.addVirtualAuthenticator(t.Context(), transport)
		if err != nil {
			t.Fatalf("add %s WebAuthn authenticator: %v", transport, err)
		}
		if err = driver.navigate(t.Context(), origin+"/profile"); err != nil {
			t.Fatalf("navigate to WebAuthn Profile: %v", err)
		}
		if err = driver.execute(
			t.Context(),
			`const input = document.querySelector("[data-webauthn-name]");`+
				`input.value = arguments[0];`+
				`document.querySelector("[data-webauthn-register]").click();`,
			name,
		); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if err = driver.waitFor(
			t.Context(),
			15*time.Second,
			`return location.pathname === "/profile" && `+
				`document.body.textContent.includes(arguments[0]);`,
			name,
		); err != nil {
			t.Fatalf("wait for %s registration: %v", name, err)
		}
		return authenticatorID
	}

	passkeyID := register("Browser passkey", "internal")
	if err := driver.removeVirtualAuthenticator(t.Context(), passkeyID); err != nil {
		t.Fatalf("remove passkey authenticator: %v", err)
	}
	securityKeyID := register("Browser security key", "usb")
	t.Cleanup(func() {
		if err := driver.removeVirtualAuthenticator(t.Context(), securityKeyID); err != nil &&
			driver.sessionID != "" {
			t.Errorf("remove security-key authenticator: %v", err)
		}
	})

	if err := driver.deleteCookie(t.Context(), "beamers_session"); err != nil {
		t.Fatalf("delete Account session cookie: %v", err)
	}
	if err := driver.navigate(t.Context(), origin+"/sign-in"); err != nil {
		t.Fatalf("navigate to WebAuthn sign-in: %v", err)
	}
	if err := driver.execute(
		t.Context(),
		`const input = document.querySelector("[name=handle]");`+
			`input.value = "Ada Admin";`+
			`document.querySelector("[data-webauthn-sign-in]").click();`,
	); err != nil {
		t.Fatalf("sign in with WebAuthn: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return location.pathname === "/" && document.body.textContent.includes("Ada Admin");`,
	); err != nil {
		t.Fatalf("wait for WebAuthn sign-in: %v", err)
	}

	if err := driver.navigate(t.Context(), origin+"/profile"); err != nil {
		t.Fatalf("navigate to WebAuthn Profile for revocation: %v", err)
	}
	if err := driver.execute(
		t.Context(),
		`const item = [...document.querySelectorAll("li")].find(`+
			`element => element.textContent.includes("Browser passkey"));`+
			`item.querySelector("button").click();`,
	); err != nil {
		t.Fatalf("revoke browser passkey: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return location.pathname === "/profile" && `+
			`[...document.querySelectorAll("li")].some(element => `+
			`element.textContent.includes("Browser passkey") && `+
			`element.textContent.includes("revoked"));`,
	); err != nil {
		t.Fatalf("wait for browser passkey revocation: %v", err)
	}
}

func certifyOverrideConfirmation(
	t *testing.T,
	driver *webDriver,
	origin string,
	displays []browserDisplayCertification,
	programClient programv1connect.ProgramControlServiceClient,
	sessionID int64,
) (browserPageEvidence, browserPageEvidence, browserProgramOutputEvidence) {
	t.Helper()
	target := origin + "/backstage/events/1/control"
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to Backstage control: %v", err)
	}
	focusKeyboardControl(t, driver, "Backstage control")
	controlEvidence, err := driver.auditPage(t.Context(), "control")
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := driver.evaluateString(
		t.Context(),
		`const form = document.querySelector(`+
			`'form[action$="/emergency-alerts/confirmation"]'); `+
			`form.querySelector('textarea[name="text"]').value = "Browser confirmation"; `+
			`form.querySelector('button[type="submit"]').click(); `+
			`return "submitted";`,
	)
	if err != nil || submitted != "submitted" {
		t.Fatalf("submit Emergency Alert preview = %q, %v", submitted, err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.querySelector("h1").textContent === "Confirm Emergency Alert" && `+
			`document.querySelectorAll(`+
			`'main li').length === 2;`,
	); err != nil {
		t.Fatalf("wait for resolved Emergency Alert confirmation: %v", err)
	}
	focusKeyboardControl(t, driver, "Override confirmation")
	confirmationEvidence, err := driver.auditPage(t.Context(), "override_confirmation")
	if err != nil {
		t.Fatal(err)
	}
	focused, err := driver.evaluateBool(
		t.Context(),
		`const button = document.querySelector('button[type="submit"]'); `+
			`button.focus(); return document.activeElement === button;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus Emergency Alert confirmation = %t, %v", focused, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("activate Emergency Alert confirmation: %v", err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return location.pathname === "/backstage/events/1/control";`,
	); err != nil {
		details, detailErr := driver.evaluateString(
			t.Context(),
			`return JSON.stringify({url: location.href, body: document.body.innerText});`,
		)
		t.Fatalf(
			"wait for Emergency Alert activation: %v; page = %s, %v",
			err,
			details,
			detailErr,
		)
	}
	for index := range displays {
		if err = displays[index].driver.waitFor(
			t.Context(),
			15*time.Second,
			`return document.querySelector("main").dataset.overrideKind === "EmergencyAlert";`,
		); err != nil {
			t.Fatalf("Display %d did not apply Emergency Alert: %v", index+1, err)
		}
	}
	assertBrowserRevealPaused(t, programClient, sessionID)
	clicked, err := driver.evaluateString(
		t.Context(),
		`const link = document.querySelector('a[href*="/clear-confirmation"]'); `+
			`link.click(); return "clicked";`,
	)
	if err != nil || clicked != "clicked" {
		t.Fatalf("open Emergency Alert clear confirmation = %q, %v", clicked, err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.querySelector("h1").textContent === "Confirm Emergency Alert clear";`,
	); err != nil {
		t.Fatalf("wait for Emergency Alert clear confirmation: %v", err)
	}
	focused, err = driver.evaluateBool(
		t.Context(),
		`const button = document.querySelector('button[type="submit"]'); `+
			`button.focus(); return document.activeElement === button;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus Emergency Alert clear confirmation = %t, %v", focused, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("clear Emergency Alert confirmation: %v", err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return location.pathname === "/backstage/events/1/control";`,
	); err != nil {
		t.Fatalf("wait for Emergency Alert clear: %v", err)
	}
	for index := range displays {
		if err = displays[index].driver.waitFor(
			t.Context(),
			15*time.Second,
			`return document.querySelector("main").dataset.overrideKind !== "EmergencyAlert";`,
		); err != nil {
			t.Fatalf("Display %d did not clear Emergency Alert: %v", index+1, err)
		}
	}
	programOutput := waitForBrowserRevealCompletion(t, programClient, sessionID)
	return controlEvidence, confirmationEvidence, programOutput
}

func assertBrowserRevealPaused(
	t *testing.T,
	client programv1connect.ProgramControlServiceClient,
	sessionID int64,
) {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-timer.C:
	}
	channel, err := client.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: sessionID,
		}),
	)
	if err != nil ||
		channel.Msg.GetChannel().GetProgramOutput().GetResult().GetStatus() !=
			programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING {
		t.Fatalf("Result Reveal did not remain paused under Emergency Alert: %+v, %v", channel, err)
	}
}

func waitForBrowserRevealCompletion(
	t *testing.T,
	client programv1connect.ProgramControlServiceClient,
	sessionID int64,
) browserProgramOutputEvidence {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		channel, err := client.GetProgramChannel(
			t.Context(),
			connect.NewRequest(&programv1.GetProgramChannelRequest{
				EventId: 1, SessionId: sessionID,
			}),
		)
		if err == nil &&
			channel.Msg.GetChannel().GetProgramOutput().GetResult().GetStatus() ==
				programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALED {
			output := channel.Msg.GetChannel().GetProgramOutput()
			entryID := ""
			if output.GetEntryId() != 0 {
				entryID = strconv.FormatInt(output.GetEntryId(), 10)
			}
			return browserProgramOutputEvidence{
				Kind: output.GetKind().String(), EntryID: entryID, Title: output.GetTitle(),
				Revision: strconv.FormatInt(channel.Msg.GetChannel().GetLiveStateRevision(), 10),
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Result Reveal did not resume after Emergency clear: %+v, %v", channel, err)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func openSecondaryCrewControl(
	t *testing.T,
	client *http.Client,
	config browserCertificationConfig,
	driverEndpoint string,
	administrator *http.Client,
	origin string,
	sessionID int64,
) (*webDriver, browserPageEvidence) {
	t.Helper()
	driver := startBrowserSession(t, client, driverEndpoint, config)
	if err := driver.navigate(t.Context(), origin+"/events/revision-2099/schedule"); err != nil {
		t.Fatalf("navigate second Crew console to cookie origin: %v", err)
	}
	addBrowserCookie(t, driver, browserCookie(
		t,
		administrator,
		origin,
		"beamers_session",
		"/",
	))
	target := origin + "/crew/program/" + strconv.FormatInt(sessionID, 10) +
		"?event_id=1"
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate second Crew console: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.querySelector("#connection-status").textContent.includes("revision");`,
	); err != nil {
		t.Fatalf("wait for second Crew console: %v", err)
	}
	focusKeyboardControl(t, driver, "second Crew control")
	evidence, err := driver.auditPage(t.Context(), "crew_control")
	if err != nil {
		t.Fatal(err)
	}
	return driver, evidence
}

func assertSecondaryCrewControl(
	t *testing.T,
	driver *webDriver,
	output browserProgramOutputEvidence,
) {
	t.Helper()
	script := `return document.querySelector("#owner").textContent.includes("Ada Admin") && ` +
		`document.querySelector('[data-item="programOutput"]').textContent === ` +
		strconv.Quote(output.Title) + `;`
	if err := driver.waitFor(t.Context(), 15*time.Second, script); err != nil {
		t.Fatalf("second Crew console did not observe committed Program Output: %v", err)
	}
}

func prepareReleasedBrowserResults(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	competitionID int64,
) int64 {
	t.Helper()
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		client,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	entry, err := competitionClient.CreateEntry(
		t.Context(),
		connect.NewRequest(&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "create-browser-results-entry",
			Name:      "Browser Certified Result",
		}),
	)
	if err != nil {
		t.Fatalf("create browser Results Entry: %v", err)
	}
	placement := int64(1)
	resultsClient := resultsv1connect.NewResultsServiceClient(
		client,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	draft, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:        "save-browser-results",
			ExpectedRevision: 0,
			Disposition:      resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
			Standings: []*resultsv1.CompetitionResultStanding{{
				EntryId:   entry.Msg.GetEntry().GetId(),
				Standing:  resultsv1.ResultStanding_RESULT_STANDING_PLACED,
				Placement: &placement, DisplayOrder: 1,
			}},
		}),
	)
	if err != nil {
		t.Fatalf("save browser Results: %v", err)
	}
	if _, err = resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:        "ready-browser-results",
			ExpectedRevision: draft.Msg.GetDraft().GetRevision(),
		}),
	); err != nil {
		t.Fatalf("ready browser Results: %v", err)
	}
	if _, err = resultsClient.ReleaseStandaloneResults(
		t.Context(),
		connect.NewRequest(&resultsv1.ReleaseStandaloneResultsRequest{
			EventId: 1, CompetitionSessionId: competitionID,
			CommandId: "release-browser-results",
		}),
	); err != nil {
		t.Fatalf("release browser Results: %v", err)
	}
	return entry.Msg.GetEntry().GetId()
}

func prepareBrowserPrizegiving(
	t *testing.T,
	client *http.Client,
	server *runningServer,
) int64 {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(),
		connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before browser Prizegiving: %v", err)
	}
	plannedStart := time.Date(2099, 8, 21, 14, 0, 0, 0, time.UTC)
	location := &rundownv1.TargetRef{
		Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLocations()[0].GetId()},
	}
	lane := &rundownv1.TargetRef{
		Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLanes()[0].GetId()},
	}
	edited, err := rundownClient.EditDraft(
		t.Context(),
		connect.NewRequest(&rundownv1.EditDraftRequest{
			EventId: 1, CommandId: "add-browser-prizegiving",
			ExpectedDraftRevision: current.Msg.GetDraftRevision(),
			Sessions: []*rundownv1.SessionDraft{
				{
					Ref: "browser-prizegiving-source", Title: "Browser Prizegiving Source",
					Type:                    rundownv1.SessionType_SESSION_TYPE_COMPETITION,
					AudienceVisibility:      rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
					PlannedStart:            timestamppb.New(plannedStart.Add(-time.Hour)),
					PlannedEnd:              timestamppb.New(plannedStart),
					TimingPolicy:            rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
					MinimumDuration:         durationpb.New(30 * time.Minute),
					StartBoundary:           rundownv1.Boundary_BOUNDARY_HARD,
					EndBoundary:             rundownv1.Boundary_BOUNDARY_HARD,
					Lanes:                   []*rundownv1.TargetRef{lane},
					Locations:               []*rundownv1.TargetRef{location},
					SubmissionDeadline:      timestamppb.New(plannedStart.Add(-90 * time.Minute)),
					EntryDefaultDisposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
				},
				{
					Ref: "browser-prizegiving", Title: "Browser Prizegiving",
					Type:               rundownv1.SessionType_SESSION_TYPE_CEREMONY,
					AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
					PlannedStart:       timestamppb.New(plannedStart),
					PlannedEnd:         timestamppb.New(plannedStart.Add(time.Hour)),
					TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
					MinimumDuration:    durationpb.New(30 * time.Minute),
					StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
					EndBoundary:        rundownv1.Boundary_BOUNDARY_HARD,
					Lanes:              []*rundownv1.TargetRef{lane},
					Locations:          []*rundownv1.TargetRef{location},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("add browser Prizegiving Sessions: %v", err)
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-browser-prizegiving")
	current, err = rundownClient.GetCrewRundown(
		t.Context(),
		connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("reload browser Prizegiving Sessions: %v", err)
	}
	var competitionID, ceremonyID int64
	for _, session := range current.Msg.GetSessions() {
		switch session.GetTitle() {
		case "Browser Prizegiving Source":
			competitionID = session.GetId()
		case "Browser Prizegiving":
			ceremonyID = session.GetId()
		}
	}
	if competitionID == 0 || ceremonyID == 0 {
		t.Fatalf("browser Prizegiving Session IDs = competition %d, ceremony %d",
			competitionID, ceremonyID)
	}

	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	entry, err := competitionClient.CreateEntry(
		t.Context(),
		connect.NewRequest(&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "create-browser-prizegiving-entry",
			Name:      "Browser Reveal Winner",
		}),
	)
	if err != nil {
		t.Fatalf("create browser Prizegiving Entry: %v", err)
	}
	placement := int64(1)
	resultsClient := resultsv1connect.NewResultsServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	draft, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:        "save-browser-prizegiving-results",
			ExpectedRevision: 0,
			Disposition:      resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
			Standings: []*resultsv1.CompetitionResultStanding{{
				EntryId: entry.Msg.GetEntry().GetId(), Standing: resultsv1.ResultStanding_RESULT_STANDING_PLACED,
				Placement: &placement, DisplayOrder: 1,
			}},
		}),
	)
	if err != nil {
		t.Fatalf("save browser Prizegiving Results: %v", err)
	}
	if _, err = resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID,
			CommandId:        "ready-browser-prizegiving-results",
			ExpectedRevision: draft.Msg.GetDraft().GetRevision(),
		}),
	); err != nil {
		t.Fatalf("ready browser Prizegiving Results: %v", err)
	}
	if _, err = resultsClient.DesignatePrizegiving(
		t.Context(),
		connect.NewRequest(&resultsv1.DesignatePrizegivingRequest{
			EventId: 1, CeremonySessionId: ceremonyID,
			CommandId: "designate-browser-prizegiving",
		}),
	); err != nil {
		t.Fatalf("designate browser Prizegiving: %v", err)
	}
	item := &resultsv1.ResultItem{
		Kind:                 resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
		CompetitionSessionId: competitionID, DisplayOrder: 1,
		RevealMethod: resultsv1.RevealMethod_REVEAL_METHOD_SEQUENTIAL_PODIUM,
	}
	plan, err := resultsClient.SavePrizegivingPlan(
		t.Context(),
		connect.NewRequest(&resultsv1.SavePrizegivingPlanRequest{
			EventId: 1, CeremonySessionId: ceremonyID,
			CommandId:             "save-browser-prizegiving-plan",
			CompetitionSessionIds: []int64{competitionID},
			Sequence:              []*resultsv1.ResultItem{item},
			PublicationOrder: []*resultsv1.ResultItemRef{{
				Kind: item.GetKind(), CompetitionSessionId: competitionID, DisplayOrder: 1,
			}},
			ResultsTextTemplate: &resultsv1.ResultsTextTemplate{
				Revision: 1, Source: "{{.EventTitle}}\n",
			},
			ReleasePolicy: resultsv1.ResultsReleasePolicy_RESULTS_RELEASE_POLICY_PROGRESSIVE_ON_REVEAL,
		}),
	)
	if err != nil {
		t.Fatalf("save browser Prizegiving plan: %v", err)
	}
	if _, err = resultsClient.RunPrizegivingPreflight(
		t.Context(),
		connect.NewRequest(&resultsv1.RunPrizegivingPreflightRequest{
			EventId: 1, CeremonySessionId: ceremonyID,
			CommandId:        "lock-browser-prizegiving-plan",
			ExpectedRevision: plan.Msg.GetPlan().GetRevision(),
		}),
	); err != nil {
		t.Fatalf("lock browser Prizegiving plan: %v", err)
	}
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	expectedLiveStateRevision := int64(0)
	if _, err = sessionClient.StartSession(
		t.Context(),
		connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: ceremonyID,
			CommandId:                 "start-browser-prizegiving",
			ExpectedLiveStateRevision: &expectedLiveStateRevision,
		}),
	); err != nil {
		t.Fatalf("start browser Prizegiving: %v", err)
	}
	return ceremonyID
}

func beginBrowserReveal(
	t *testing.T,
	httpClient *http.Client,
	server *runningServer,
	sessionID int64,
) programv1connect.ProgramControlServiceClient {
	t.Helper()
	client := programv1connect.NewProgramControlServiceClient(
		httpClient, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := client.GetProgramChannel(
		t.Context(),
		connect.NewRequest(&programv1.GetProgramChannelRequest{
			EventId: 1, SessionId: sessionID,
		}),
	)
	if err != nil {
		t.Fatalf("load browser Prizegiving Program Channel: %v", err)
	}
	channel := current.Msg.GetChannel()
	if channel.GetProgramOutput().GetResult() == nil {
		t.Fatalf("browser Prizegiving Program Output is not a Result: %+v", channel)
	}
	revealing, err := client.ActOnResult(
		t.Context(),
		connect.NewRequest(&programv1.ActOnResultRequest{
			EventId: 1, SessionId: sessionID,
			CommandId:                    "reveal-browser-prizegiving-result",
			Action:                       programv1.ResultAction_RESULT_ACTION_REVEAL,
			Item:                         channel.GetProgramOutput(),
			ExpectedProgramRevision:      channel.GetLiveStateRevision(),
			ExpectedControlStateRevision: channel.GetControlStateRevision(),
		}),
	)
	if err != nil ||
		revealing.Msg.GetChannel().GetProgramOutput().GetResult().GetStatus() !=
			programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING {
		t.Fatalf("begin browser Result Reveal: %+v, %v", revealing, err)
	}
	return client
}

func certifyResultsPage(
	t *testing.T,
	driver *webDriver,
	origin string,
) browserPageEvidence {
	t.Helper()
	evidence := certifyInteractivePage(
		t,
		driver,
		origin+"/events/revision-2099/results",
		"results",
	)
	metadata, err := driver.evaluateString(
		t.Context(),
		`return document.documentElement.lang + "|" + `+
			`document.documentElement.dataset.locale;`,
	)
	if err != nil || metadata != "en-GB|en-GB" {
		t.Fatalf("public Results language metadata = %q, %v", metadata, err)
	}
	return evidence
}

func startCertifiedBrowserDisplay(
	t *testing.T,
	client *http.Client,
	config browserCertificationConfig,
	origin string,
	display *browserDisplayCertification,
) browserPageEvidence {
	t.Helper()
	display.driver = startBrowserSession(t, client, display.driverEndpoint, config)
	if err := display.driver.navigate(t.Context(), origin+"/events/revision-2099/schedule"); err != nil {
		t.Fatalf("navigate to Display cookie origin: %v", err)
	}
	for _, path := range []string{"/display", "/beamers.display.v1.DisplayService"} {
		addBrowserCookie(t, display.driver, &http.Cookie{
			Name:     "beamers_display",
			Value:    display.credential,
			Path:     path,
			HttpOnly: true,
		})
	}
	if err := display.driver.navigate(t.Context(), origin+"/display"); err != nil {
		t.Fatalf("navigate to Display: %v", err)
	}
	if err := display.driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.documentElement.dataset.connection === "connected";`,
	); err != nil {
		t.Fatalf("wait for connected Display: %v", err)
	}
	evidence, err := display.driver.auditPage(t.Context(), "display")
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func captureBrowserDisplayOutput(
	t *testing.T,
	display *browserDisplayCertification,
	programOutput browserProgramOutputEvidence,
) browserDisplayOutputEvidence {
	t.Helper()
	outputScript := `return document.querySelector(` +
		`'[data-widget="program-output"] h1')?.textContent === ` +
		strconv.Quote(programOutput.Title) + `;`
	if err := display.driver.waitFor(
		t.Context(), 15*time.Second, outputScript,
	); err != nil {
		t.Fatalf("wait for committed Program Output on Display: %v", err)
	}
	outputJSON, err := display.driver.evaluateString(
		t.Context(),
		`const main = document.querySelector("main"); `+
			`return JSON.stringify({`+
			`display_id: main.dataset.displayId, `+
			`program_output: {`+
			`kind: main.dataset.programOutputKind, `+
			`entry_id: main.dataset.programOutputEntryId, `+
			`title: document.querySelector('[data-widget="program-output"] h1').textContent, `+
			`revision: main.dataset.programOutputRevision`+
			`}, `+
			`stream_id: main.dataset.streamId, `+
			`stream_position: main.dataset.streamPosition, `+
			`acknowledged: false`+
			`});`,
	)
	if err != nil {
		t.Fatalf("read Display Program Output: %v", err)
	}
	var output browserDisplayOutputEvidence
	if err = json.Unmarshal([]byte(outputJSON), &output); err != nil {
		t.Fatalf("decode Display Program Output: %v", err)
	}
	display.committedFrame, err = display.driver.evaluateString(
		t.Context(),
		`return document.querySelector("main").textContent;`,
	)
	if err != nil {
		t.Fatalf("read committed Display frame: %v", err)
	}
	return output
}

func assertBrowserDisplayFrame(
	t *testing.T,
	display *browserDisplayCertification,
	connection string,
	timeout time.Duration,
	failure string,
) {
	t.Helper()
	script := `return document.documentElement.dataset.connection === ` +
		strconv.Quote(connection) + `;`
	if err := display.driver.waitFor(t.Context(), timeout, script); err != nil {
		t.Fatalf("wait for %s Display: %v", connection, err)
	}
	frame, err := display.driver.evaluateString(
		t.Context(),
		`return document.querySelector("main").textContent;`,
	)
	if err != nil {
		t.Fatalf("read %s Display frame: %v", connection, err)
	}
	if frame != display.committedFrame {
		t.Fatalf("Display changed its committed frame %s", failure)
	}
}

func browserConfigFromEnvironment(t *testing.T) browserCertificationConfig {
	t.Helper()
	expectedMajor, err := strconv.Atoi(os.Getenv("BEAMERS_BROWSER_MAJOR"))
	if err != nil || expectedMajor <= 0 {
		t.Fatal("BEAMERS_BROWSER_MAJOR must be a positive browser major")
	}
	config := browserCertificationConfig{
		Engine:            os.Getenv("BEAMERS_BROWSER_ENGINE"),
		Role:              os.Getenv("BEAMERS_BROWSER_ROLE"),
		ExpectedMajor:     expectedMajor,
		BrowserBinary:     os.Getenv("BEAMERS_BROWSER_BINARY"),
		WebDriverBinary:   os.Getenv("BEAMERS_WEBDRIVER_BINARY"),
		WebDriverEndpoint: os.Getenv("BEAMERS_WEBDRIVER_ENDPOINT"),
		WebDriverVersion:  os.Getenv("BEAMERS_WEBDRIVER_VERSION"),
		ReportPath:        os.Getenv("BEAMERS_BROWSER_REPORT"),
	}
	if config.Engine != "chromium" && config.Engine != "firefox" {
		t.Fatalf("unsupported BEAMERS_BROWSER_ENGINE %q", config.Engine)
	}
	if config.Role != "current" && config.Role != "previous" {
		t.Fatalf("unsupported BEAMERS_BROWSER_ROLE %q", config.Role)
	}
	if config.WebDriverEndpoint == "" {
		for _, required := range []struct {
			name string
			path string
		}{
			{"BEAMERS_BROWSER_BINARY", config.BrowserBinary},
			{"BEAMERS_WEBDRIVER_BINARY", config.WebDriverBinary},
		} {
			if required.path == "" {
				t.Fatalf("%s is required", required.name)
			}
			if _, err = exec.LookPath(required.path); err != nil {
				t.Fatalf("%s: %v", required.name, err)
			}
		}
	} else if config.WebDriverVersion == "" {
		t.Fatal("BEAMERS_WEBDRIVER_VERSION is required with BEAMERS_WEBDRIVER_ENDPOINT")
	}
	if config.ReportPath == "" {
		t.Fatal("BEAMERS_BROWSER_REPORT is required")
	}
	return config
}

func startBrowserDriver(
	t *testing.T,
	config browserCertificationConfig,
) (string, string) {
	t.Helper()
	if config.WebDriverEndpoint != "" {
		return config.WebDriverEndpoint, config.WebDriverVersion
	}
	versionCommand := exec.CommandContext(
		t.Context(),
		config.WebDriverBinary,
		"--version",
	)
	versionOutput, err := versionCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("read WebDriver version: %v\n%s", err, versionOutput)
	}
	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatalf("reserve WebDriver address: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("WebDriver listener address = %T, want *net.TCPAddr", listener.Addr())
	}
	port := address.Port
	if err = listener.Close(); err != nil {
		t.Fatalf("release WebDriver address: %v", err)
	}
	args := []string{"--port", strconv.Itoa(port)}
	if config.Engine == "chromium" {
		args = []string{"--port=" + strconv.Itoa(port)}
	}
	logPath := filepath.Join(t.TempDir(), "webdriver.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create WebDriver log: %v", err)
	}
	command := exec.CommandContext(t.Context(), config.WebDriverBinary, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start WebDriver: %v", err)
	}
	var processErr error
	done := make(chan struct{})
	go func() {
		processErr = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("WebDriver process did not stop")
		}
		if closeErr := logFile.Close(); closeErr != nil {
			t.Errorf("close WebDriver log: %v", closeErr)
		}
	})
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	statusClient := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, requestErr := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			endpoint+"/status",
			http.NoBody,
		)
		if requestErr != nil {
			t.Fatalf("create WebDriver status request: %v", requestErr)
		}
		response, requestErr := statusClient.Do(request)
		if requestErr == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if err = errors.Join(readErr, closeErr); err != nil {
				t.Fatalf("read WebDriver status response: %v", err)
			}
			if response.StatusCode == http.StatusOK {
				return endpoint, strings.TrimSpace(string(versionOutput))
			}
		}
		select {
		case <-done:
			t.Fatalf(
				"WebDriver exited before readiness: %v\n%s",
				processErr,
				browserDriverLog(t, logPath),
			)
		case <-deadline.C:
			t.Fatalf(
				"WebDriver did not become ready:\n%s",
				browserDriverLog(t, logPath),
			)
		case <-ticker.C:
		}
	}
}

func browserDriverLog(t *testing.T, path string) string {
	t.Helper()
	found, err := os.ReadFile(path)
	if err != nil {
		return "read WebDriver log: " + err.Error()
	}
	return string(found)
}

func startBrowserSession(
	t *testing.T,
	client *http.Client,
	endpoint string,
	config browserCertificationConfig,
) *webDriver {
	t.Helper()
	return startBrowserSessionWithForcedColors(t, client, endpoint, config, false)
}

func startForcedColorsBrowserSession(
	t *testing.T,
	client *http.Client,
	endpoint string,
	config browserCertificationConfig,
) *webDriver {
	t.Helper()
	return startBrowserSessionWithForcedColors(t, client, endpoint, config, true)
}

func startBrowserSessionWithForcedColors(
	t *testing.T,
	client *http.Client,
	endpoint string,
	config browserCertificationConfig,
	forcedColors bool,
) *webDriver {
	t.Helper()
	capabilities := map[string]any{"browserName": "chrome"}
	switch config.Engine {
	case "chromium":
		capabilities["webauthn:virtualAuthenticators"] = true
		args := []string{
			"--headless=new",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--force-prefers-reduced-motion=reduce",
			"--user-data-dir=" + t.TempDir(),
			"--window-size=1280,720",
		}
		if forcedColors {
			args = append(args, "--force-high-contrast")
		}
		options := map[string]any{
			"args": args,
		}
		if config.BrowserBinary != "" {
			options["binary"] = config.BrowserBinary
		}
		capabilities["goog:chromeOptions"] = options
	case "firefox":
		capabilities["browserName"] = "firefox"
		preferences := map[string]any{"ui.prefersReducedMotion": 1}
		if forcedColors {
			preferences["browser.display.document_color_use"] = 2
			preferences["browser.display.use_system_colors"] = true
		}
		options := map[string]any{
			"args":  []string{"-headless"},
			"prefs": preferences,
		}
		if config.BrowserBinary != "" {
			options["binary"] = config.BrowserBinary
		}
		capabilities["moz:firefoxOptions"] = options
	}
	driver, err := newWebDriver(t.Context(), client, endpoint, capabilities)
	if err != nil {
		t.Fatalf("start %s WebDriver session: %v", config.Engine, err)
	}
	t.Cleanup(func() {
		if driver.sessionID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := driver.close(ctx); closeErr != nil {
			t.Errorf("clean up browser session: %v", closeErr)
		}
	})
	return driver
}

func certifyInteractivePage(
	t *testing.T,
	driver *webDriver,
	target string,
	surface string,
) browserPageEvidence {
	t.Helper()
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to %s: %v", surface, err)
	}
	focusKeyboardControl(t, driver, surface)
	evidence, err := driver.auditPage(t.Context(), surface)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func certifyDemoBrowserJourneys(
	t *testing.T,
	client *http.Client,
	driverEndpoint string,
	config browserCertificationConfig,
	bin string,
) []browserPageEvidence {
	t.Helper()
	demo := startDemo(t, bin)
	defer demo.stop(t)
	driver := startBrowserSession(t, client, driverEndpoint, config)
	defer closeBrowserSession(t, driver)
	_, port, err := net.SplitHostPort(demo.address)
	if err != nil {
		t.Fatalf("split demo browser address: %v", err)
	}
	origin := "http://" + net.JoinHostPort("localhost", port)
	browserOrigin, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("parse demo browser origin: %v", err)
	}
	evidence := []browserPageEvidence{
		certifyInteractivePage(t, driver, origin+"/", "demo-anonymous"),
	}
	if err = driver.navigate(t.Context(), origin+"/display"); err != nil {
		t.Fatalf("navigate to demo Display: %v", err)
	}
	displayEvidence, err := driver.auditPage(t.Context(), "demo-display")
	if err != nil {
		t.Fatal(err)
	}
	evidence = append(
		evidence,
		displayEvidence,
		certifyInteractivePage(
			t,
			driver,
			origin+"/events/revision-demo/results",
			"demo-results",
		),
	)
	for _, journey := range []struct {
		handle, path, surface string
		backstage             bool
	}{
		{"attendee", "/my-schedule", "demo-attendee", false},
		{"attendee", "/my-participation", "demo-submission", false},
		{"voter", "/voting/3?event_id=1", "demo-voter", false},
		{"producer", "/backstage/events/1/results", "demo-producer", true},
		{"administrator", "/backstage/themes", "demo-administrator", true},
		{"operator", "/backstage/events/1/operations", "demo-operator", true},
		{"observer", "/backstage/events/1", "demo-observer", true},
	} {
		account := authenticatedClient(t)
		assertJSONRequest(
			t,
			account,
			demo.address,
			"/auth/sign-in",
			map[string]string{"name": journey.handle, "password": "demo"},
			http.StatusNoContent,
			"",
		)
		account.Jar.SetCookies(
			browserOrigin,
			account.Jar.Cookies(&url.URL{Scheme: "http", Host: demo.address}),
		)
		addBrowserCookie(t, driver, browserCookie(
			t,
			account,
			origin,
			"beamers_session",
			"/",
		))
		pageEvidence := certifyInteractivePage(
			t,
			driver,
			origin+journey.path,
			journey.surface,
		)
		if journey.surface == "demo-voter" {
			assertResponsivePageWidths(t, driver, origin+journey.path, 320)
			assertResponsivePageZoom(t, driver, origin+journey.path, 1280, 4)
		}
		actualPath, err := driver.evaluateString(
			t.Context(),
			`return location.pathname + location.search;`,
		)
		if err != nil {
			t.Fatalf("read %s demo browser path: %v", journey.handle, err)
		}
		if actualPath != journey.path {
			t.Fatalf(
				"%s demo browser path = %q, want %q",
				journey.handle,
				actualPath,
				journey.path,
			)
		}
		hasBackstage, err := driver.evaluateBool(
			t.Context(),
			`return Boolean(document.querySelector(`+
				`'nav[aria-label="Primary"] a[href="/backstage"]'));`,
		)
		if err != nil {
			t.Fatalf("read %s demo browser navigation: %v", journey.handle, err)
		}
		if hasBackstage != journey.backstage {
			t.Fatalf(
				"%s demo Backstage navigation = %t, want %t",
				journey.handle,
				hasBackstage,
				journey.backstage,
			)
		}
		if journey.handle == "producer" ||
			journey.handle == "operator" ||
			journey.handle == "observer" {
			role, roleErr := driver.evaluateString(
				t.Context(),
				`return document.querySelector(".backstage-links p")?.textContent.trim() || "";`,
			)
			wantRole := strings.ToUpper(journey.handle[:1]) + journey.handle[1:]
			if roleErr != nil || role != wantRole {
				t.Fatalf(
					"%s Backstage role = %q, %v; want %q",
					journey.handle,
					role,
					roleErr,
					wantRole,
				)
			}
			clickBrowserLink(
				t,
				driver,
				"Sessions and Competitions",
				"/backstage/events/1/sessions",
			)
			hasCompetitionWorkflow, workflowErr := driver.evaluateBool(
				t.Context(),
				`return [...document.querySelectorAll("main a")].some(`+
					`link => link.textContent.trim() === "Competition Entries and Attachments");`,
			)
			wantCompetitionWorkflow := journey.handle != "observer"
			if workflowErr != nil ||
				hasCompetitionWorkflow != wantCompetitionWorkflow {
				t.Fatalf(
					"%s Competition workflow = %t, %v; want %t",
					journey.handle,
					hasCompetitionWorkflow,
					workflowErr,
					wantCompetitionWorkflow,
				)
			}
			if wantCompetitionWorkflow {
				path, pathErr := driver.evaluateString(
					t.Context(),
					`return [...document.querySelectorAll("main a")].find(`+
						`link => link.textContent.trim() === `+
						`"Competition Entries and Attachments").getAttribute("href");`,
				)
				if pathErr != nil {
					t.Fatalf("read %s Competition workflow: %v", journey.handle, pathErr)
				}
				clickBrowserLink(
					t,
					driver,
					"Competition Entries and Attachments",
					path,
				)
			}
		}
		switch journey.surface {
		case "demo-submission":
			if err = driver.execute(
				t.Context(),
				`const form = document.querySelector('form input[name="entry_name"]').form;`+
					`form.elements.entry_name.value = "Browser Certified Entry";`+
					`form.requestSubmit();`,
			); err != nil {
				t.Fatalf("create demo submission: %v", err)
			}
			if err = driver.waitFor(
				t.Context(),
				10*time.Second,
				`return document.body.textContent.includes("Browser Certified Entry");`,
			); err != nil {
				t.Fatalf("observe created demo submission: %v", err)
			}
		case "demo-voter":
			if err = driver.execute(
				t.Context(),
				`const vote = document.querySelector('input[name="value"][value="4"]');`+
					`document.documentElement.dataset.browserCertificationPending = "1";`+
					`vote.checked = true; vote.form.requestSubmit();`,
			); err != nil {
				t.Fatalf("save demo vote: %v", err)
			}
			if err = driver.waitFor(
				t.Context(),
				10*time.Second,
				`const vote = document.querySelector('input[name="value"][value="4"]');`+
					`return !document.documentElement.dataset.browserCertificationPending && `+
					`vote && vote.checked;`,
			); err != nil {
				t.Fatalf("observe saved demo vote: %v", err)
			}
		}
		evidence = append(evidence, pageEvidence)
		if err = driver.deleteCookie(t.Context(), "beamers_session"); err != nil {
			t.Fatalf("clear %s demo browser session: %v", journey.handle, err)
		}
	}
	return evidence
}

func certifyFrontendTheme(
	t *testing.T,
	driver *webDriver,
	origin string,
) browserPageEvidence {
	t.Helper()
	evidence := certifyInteractivePage(t, driver, origin+"/", "frontend")
	if err := driver.waitFor(
		t.Context(),
		10*time.Second,
		`return document.fonts.status === "loaded" && `+
			`document.fonts.check('16px "Open Sans"') && `+
			`document.fonts.check('700 16px "Chakra Petch"');`,
	); err != nil {
		t.Fatalf("load bundled Frontend fonts: %v", err)
	}
	themeReady, err := driver.evaluateBool(
		t.Context(),
		`const body = getComputedStyle(document.body);`+
			`const heading = getComputedStyle(document.querySelector("h1"));`+
			`const stars = getComputedStyle(document.body, "::before");`+
			`const pause = [...document.querySelectorAll("button")].find(`+
			`(button) => button.textContent.trim() === "Pause effects");`+
			`return body.fontFamily.includes("Open Sans") && `+
			`heading.fontFamily.includes("Chakra Petch") && `+
			`matchMedia("(prefers-reduced-motion: reduce)").matches && `+
			`Number.parseFloat(stars.animationDuration) === 0 && `+
			`Boolean(pause) && pause.getAttribute("aria-pressed") === "false";`,
	)
	if err != nil || !themeReady {
		t.Fatalf("certify base Frontend Theme = %t, %v", themeReady, err)
	}
	return evidence
}

func certifyForcedColors(
	t *testing.T,
	driver *webDriver,
	origin string,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), origin+"/"); err != nil {
		t.Fatalf("navigate to forced-colors Frontend: %v", err)
	}
	forced, err := driver.evaluateBool(
		t.Context(),
		`return matchMedia("(forced-colors: active)").matches && `+
			`getComputedStyle(document.body, "::before").display === "none" && `+
			`getComputedStyle(document.querySelector("main")).boxShadow === "none";`,
	)
	if err != nil || !forced {
		t.Fatalf("certify forced-colors Frontend = %t, %v", forced, err)
	}
	focusKeyboardControl(t, driver, "forced-colors Frontend")
	evidence, err := driver.auditPage(t.Context(), "frontend")
	if err != nil {
		t.Fatal(err)
	}
	if err = evidence.validate(); err != nil {
		t.Fatalf("validate forced-colors Frontend: %v", err)
	}
}

func assertResponsivePageWidths(
	t *testing.T,
	driver *webDriver,
	target string,
	widths ...int,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to responsive page: %v", err)
	}
	for _, width := range widths {
		if err := driver.setWindowSize(t.Context(), width); err != nil {
			t.Fatalf("set browser width %d: %v", width, err)
		}
		fits, err := driver.evaluateBool(
			t.Context(),
			`return document.documentElement.scrollWidth <= window.innerWidth;`,
		)
		if err != nil {
			t.Fatalf("inspect browser width %d: %v", width, err)
		}
		if !fits {
			t.Fatalf("page overflows horizontally at %d pixels", width)
		}
	}
}

func assertResponsivePageZoom(
	t *testing.T,
	driver *webDriver,
	target string,
	width int,
	zoom int,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to zoomed page: %v", err)
	}
	equivalentWidth := width / zoom
	if err := driver.setWindowSize(t.Context(), equivalentWidth); err != nil {
		t.Fatalf("set %d%% zoom-equivalent browser width %d: %v", zoom*100, equivalentWidth, err)
	}
	fits, err := driver.evaluateBool(
		t.Context(),
		`return document.documentElement.scrollWidth <= window.innerWidth;`,
	)
	if err != nil {
		t.Fatalf("inspect page at %d%% zoom: %v", zoom*100, err)
	}
	if !fits {
		t.Fatalf("page overflows horizontally at %d pixels and %d%% zoom", width, zoom*100)
	}
}

func certifyLiveScheduleUpdate(
	t *testing.T,
	driver *webDriver,
	administrator *http.Client,
	server *runningServer,
	sessionID int64,
) {
	t.Helper()
	target := "http://" + server.address + "/events/revision-2099/schedule?location=1"
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to live Schedule: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		5*time.Second,
		`const source = document.body["htmx-internal-data"]?.sseEventSource; `+
			`return source?.readyState === EventSource.OPEN;`,
	); err != nil {
		t.Fatalf("wait for live Schedule connection: %v", err)
	}
	focused, err := driver.evaluateBool(
		t.Context(),
		`const control = document.querySelector("#schedule-location"); `+
			`control.focus(); `+
			`document.querySelector("#schedule").dataset.certificationRefresh = "pending"; `+
			`return document.activeElement === control;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus live Schedule filter = %t, %v", focused, err)
	}
	if focused, err = driver.evaluateBool(
		t.Context(),
		`window.htmx.trigger(document.querySelector("#schedule"), "sse:schedule"); return true;`,
	); err != nil || !focused {
		t.Fatalf("trigger Schedule focus-preservation refresh = %t, %v", focused, err)
	}
	if err = driver.waitFor(
		t.Context(),
		5*time.Second,
		`const schedule = document.querySelector("#schedule"); `+
			`return !schedule.dataset.certificationRefresh && `+
			`document.activeElement?.id === "schedule-location";`,
	); err != nil {
		t.Fatalf("wait for Schedule filter focus restoration: %v", err)
	}
	focused, err = driver.evaluateBool(
		t.Context(),
		fmt.Sprintf(
			`const link = document.querySelector("#schedule-session-%d"); `+
				`link.focus(); return document.activeElement === link;`,
			sessionID,
		),
	)
	if err != nil || !focused {
		t.Fatalf("focus live Schedule Session link = %t, %v", focused, err)
	}
	client := rundownv1connect.NewRundownServiceClient(
		administrator,
		"http://"+server.address,
		connect.WithProtoJSON(),
	)
	current, err := client.GetCrewRundown(
		t.Context(),
		connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before browser Schedule update: %v", err)
	}
	edited, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "browser-live-schedule",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Live Browser Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit browser Schedule: %v", err)
	}
	publishEditedDraft(t, client, edited.Msg, "publish-browser-live-schedule")
	if err = driver.waitFor(
		t.Context(),
		5*time.Second,
		`return location.search === "?location=1" && `+
			`document.querySelector("#schedule").textContent.includes("Live Browser Keynote") && `+
			`document.querySelector("#schedule-status[role=status][aria-live=polite]")?.textContent === "Schedule updated.";`,
	); err != nil {
		t.Fatalf("wait for browser Schedule invalidation: %v", err)
	}
	focused, err = driver.evaluateBool(
		t.Context(),
		fmt.Sprintf(`return document.activeElement?.id === "schedule-session-%d";`, sessionID),
	)
	if err != nil || !focused {
		t.Fatalf("live Schedule Session focus after invalidation = %t, %v", focused, err)
	}
	focused, err = driver.evaluateBool(
		t.Context(),
		fmt.Sprintf(
			`document.querySelector("#schedule-session-%d").remove(); `+
				`document.body.dispatchEvent(new CustomEvent("htmx:afterSwap", `+
				`{bubbles: true, detail: {target: document.querySelector("#schedule")}})); `+
				`return document.activeElement?.id === "schedule-heading";`,
			sessionID,
		),
	)
	if err != nil || !focused {
		t.Fatalf("focus fallback after live Schedule item removal = %t, %v", focused, err)
	}
}

func assertBackstageNavigationModes(
	t *testing.T,
	driver *webDriver,
	origin string,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), origin+"/backstage"); err != nil {
		t.Fatalf("navigate to Backstage: %v", err)
	}
	for _, check := range []struct {
		width      int
		wantDrawer bool
	}{
		{width: 320, wantDrawer: true},
		{width: 1440, wantDrawer: false},
	} {
		if err := driver.setWindowSize(t.Context(), check.width); err != nil {
			t.Fatalf("set Backstage width %d: %v", check.width, err)
		}
		drawer, err := driver.evaluateBool(
			t.Context(),
			`return getComputedStyle(document.querySelector(".backstage-drawer")).display !== "none";`,
		)
		if err != nil {
			t.Fatalf("inspect Backstage drawer at %d pixels: %v", check.width, err)
		}
		sidebar, err := driver.evaluateBool(
			t.Context(),
			`return getComputedStyle(document.querySelector(".backstage-sidebar")).display !== "none";`,
		)
		if err != nil {
			t.Fatalf("inspect Backstage sidebar at %d pixels: %v", check.width, err)
		}
		if drawer != check.wantDrawer || sidebar == check.wantDrawer {
			t.Fatalf(
				"Backstage navigation at %d pixels = drawer %t, sidebar %t",
				check.width,
				drawer,
				sidebar,
			)
		}
		equivalent, err := driver.evaluateBool(
			t.Context(),
			`const links = selector => [...document.querySelectorAll(selector + " a")].map(`+
				`link => link.textContent.trim() + "|" + link.getAttribute("href"));`+
				`return JSON.stringify(links(".backstage-drawer")) === `+
				`JSON.stringify(links(".backstage-sidebar"));`,
		)
		if err != nil || !equivalent {
			t.Fatalf(
				"Backstage navigation parity at %d pixels = %t, %v",
				check.width,
				equivalent,
				err,
			)
		}
	}
	if err := driver.navigate(
		t.Context(),
		origin+"/backstage/events/1/results",
	); err != nil {
		t.Fatalf("navigate to Backstage Results: %v", err)
	}
	current, err := driver.evaluateBool(
		t.Context(),
		`return document.querySelectorAll(`+
			`'.backstage-links a[aria-current="page"]').length === 2 && `+
			`document.querySelector('.breadcrumbs [aria-current="page"]')?.textContent.trim() === `+
			`"Results and Prizegiving";`,
	)
	if err != nil || !current {
		t.Fatalf("Backstage active navigation and breadcrumbs = %t, %v", current, err)
	}
	err = driver.navigate(
		t.Context(),
		origin+"/backstage/events/1/control/emergency-alerts#emergency-alerts",
	)
	if err != nil {
		t.Fatalf("navigate to Backstage Emergency Alerts: %v", err)
	}
	current, err = driver.evaluateBool(
		t.Context(),
		`return document.querySelectorAll(`+
			`'.backstage-links a[aria-current="page"]').length === 2 && `+
			`document.querySelector('.breadcrumbs [aria-current="page"]')?.textContent.trim() === `+
			`"Emergency Alerts" && location.hash === "#emergency-alerts";`,
	)
	if err != nil || !current {
		t.Fatalf("Backstage Emergency navigation and breadcrumbs = %t, %v", current, err)
	}
}

func assertEventNavigationModes(
	t *testing.T,
	driver *webDriver,
	origin string,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), origin+"/events/revision-2099"); err != nil {
		t.Fatalf("navigate to public Event: %v", err)
	}
	if err := driver.setWindowSize(t.Context(), 320); err != nil {
		t.Fatalf("set Event mobile width: %v", err)
	}
	mobile, err := driver.evaluateBool(t.Context(), `
		const drawer = document.querySelector(".event-drawer");
		const sidebar = document.querySelector(".event-sidebar");
		const links = selector => Array.from(
			document.querySelectorAll(selector),
			link => link.href,
		);
		return getComputedStyle(drawer).display !== "none" &&
			getComputedStyle(sidebar).display === "none" &&
			JSON.stringify(links(".event-drawer a")) ===
				JSON.stringify(links(".event-sidebar a"));`)
	if err != nil || !mobile {
		t.Fatalf("mobile Event navigation = %t, %v", mobile, err)
	}
	focused, err := driver.evaluateBool(
		t.Context(),
		`const summary = document.querySelector(".event-drawer summary");
		summary.focus();
		return document.activeElement === summary;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus mobile Event drawer = %t, %v", focused, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("open mobile Event drawer: %v", err)
	}
	if err = driver.waitFor(
		t.Context(),
		5*time.Second,
		`return document.querySelector(".event-drawer").open;`,
	); err != nil {
		t.Fatalf("mobile Event drawer did not open: %v", err)
	}
	if err = driver.setWindowSize(t.Context(), 1440); err != nil {
		t.Fatalf("set Event desktop width: %v", err)
	}
	desktop, err := driver.evaluateBool(t.Context(), `
		const drawer = document.querySelector(".event-drawer");
		const sidebar = document.querySelector(".event-sidebar");
		return getComputedStyle(drawer).display === "none" &&
			getComputedStyle(sidebar).display !== "none";`)
	if err != nil || !desktop {
		t.Fatalf("desktop Event navigation = %t, %v", desktop, err)
	}
}

func certifyFrontendNavigationJourney(
	t *testing.T,
	driver *webDriver,
	origin string,
	sessionID int64,
	competitionID int64,
) {
	t.Helper()
	if err := driver.navigate(t.Context(), origin+"/"); err != nil {
		t.Fatalf("navigate to Frontend root: %v", err)
	}
	clickBrowserLink(t, driver, "Revision 2099", "/events/revision-2099")
	clickBrowserLink(t, driver, "Event Schedule", "/events/revision-2099/schedule")
	clickBrowserLink(
		t,
		driver,
		"Live Browser Keynote",
		"/events/revision-2099/schedule/sessions/"+strconv.FormatInt(sessionID, 10),
	)
	clickBrowserLink(t, driver, "Competitions", "/events/revision-2099/competitions")
	clickBrowserLink(
		t,
		driver,
		"Demo Competition",
		"/events/revision-2099/competitions/"+strconv.FormatInt(competitionID, 10),
	)
	clickBrowserLink(t, driver, "Event Results", "/events/revision-2099/results")
	clickBrowserLink(t, driver, "Profile", "/profile")
	clickBrowserLink(t, driver, "My Participation", "/my-participation")
	clickBrowserLink(t, driver, "Backstage", "/backstage")
	clickBrowserLink(t, driver, "Events", "/")
}

func clickBrowserLink(
	t *testing.T,
	driver *webDriver,
	label string,
	expectedPath string,
) {
	t.Helper()
	clicked, err := driver.evaluateBool(
		t.Context(),
		`const label = arguments[0];
		const link = Array.from(document.querySelectorAll("a")).find(candidate =>
			candidate.textContent.trim() === label &&
			candidate.getClientRects().length > 0
		);
		if (!link) return false;
		link.click();
		return true;`,
		label,
	)
	if err != nil || !clicked {
		t.Fatalf("click %q link = %t, %v", label, clicked, err)
	}
	if err = driver.waitFor(
		t.Context(),
		5*time.Second,
		`return location.pathname === arguments[0];`,
		expectedPath,
	); err != nil {
		t.Fatalf("wait for %q link to %q: %v", label, expectedPath, err)
	}
}

func focusKeyboardControl(t *testing.T, driver *webDriver, surface string) {
	t.Helper()
	for range 10 {
		if err := driver.pressKey(t.Context(), "\uE004"); err != nil {
			t.Fatalf("Tab through %s: %v", surface, err)
		}
		focused, err := driver.evaluateBool(
			t.Context(),
			`const active = document.activeElement;`+
				`return active && active !== document.body && `+
				`active.matches("a[href],button,input,select,textarea,[tabindex]");`,
		)
		if err != nil {
			t.Fatalf("inspect %s keyboard focus: %v", surface, err)
		}
		if focused {
			return
		}
	}
	t.Fatalf("Tab did not reach a control on %s", surface)
}

func certifyCrewControl(
	t *testing.T,
	driver *webDriver,
	origin string,
	sessionID int64,
) (browserPageEvidence, []string, browserProgramOutputEvidence) {
	t.Helper()
	target := origin + "/crew/program/" + strconv.FormatInt(sessionID, 10) +
		"?event_id=1"
	if err := driver.navigate(t.Context(), target); err != nil {
		t.Fatalf("navigate to Crew control: %v", err)
	}
	if waitErr := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.querySelector("#connection-status").textContent !== `+
			`"Loading authoritative state…";`,
	); waitErr != nil {
		details, detailErr := driver.evaluateString(
			t.Context(),
			`return JSON.stringify({`+
				`url: location.href, `+
				`status: document.querySelector("#connection-status")?.textContent, `+
				`scripts: [...document.scripts].map((script) => script.src), `+
				`resources: performance.getEntriesByType("resource").map((entry) => entry.name)`+
				`});`,
		)
		t.Fatalf(
			"wait for Crew control: %v; page = %s, %v",
			waitErr,
			details,
			detailErr,
		)
	}
	status, err := driver.evaluateString(
		t.Context(),
		`return document.querySelector("#connection-status").textContent;`,
	)
	if err != nil {
		t.Fatalf("read Crew control status: %v", err)
	}
	if !strings.Contains(status, "revision") {
		t.Fatalf("Crew control status = %q, want revision", status)
	}
	focusKeyboardControl(t, driver, "Crew control")
	evidence, err := driver.auditPage(t.Context(), "crew_control")
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return [...document.querySelectorAll("#displays li[data-delivery]")].length === 2 && `+
			`[...document.querySelectorAll("#displays li[data-delivery]")].every(`+
			`(display) => display.dataset.delivery === "applied");`,
	); err != nil {
		t.Fatalf("wait for two connected consuming Displays: %v", err)
	}
	connectedJSON, err := driver.evaluateString(
		t.Context(),
		`return JSON.stringify([...document.querySelectorAll(`+
			`"#displays li[data-display-id]")].map(`+
			`(display) => display.dataset.displayId));`,
	)
	if err != nil {
		t.Fatalf("read connected consuming Displays: %v", err)
	}
	var connectedDisplayIDs []string
	if err = json.Unmarshal([]byte(connectedJSON), &connectedDisplayIDs); err != nil {
		t.Fatalf("decode connected consuming Displays: %v", err)
	}
	focused, err := driver.evaluateBool(
		t.Context(),
		`const button = document.querySelector('[data-control-action="CONTROL_ACTION_CLAIM"]');`+
			`button.focus(); return document.activeElement === button;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus Crew Claim control = %t, %v", focused, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("activate Crew Claim control: %v", err)
	}
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.querySelector("#owner").textContent.includes("Ada Admin");`,
	); err != nil {
		t.Fatalf("wait for committed Crew Claim command: %v", err)
	}
	selected, err := driver.evaluateString(
		t.Context(),
		`const buttons = [...document.querySelectorAll("#program-items button")].filter(`+
			`(candidate) => candidate.textContent !== "standby");`+
			`const button = buttons.find(`+
			`(candidate) => candidate.getAttribute("aria-pressed") === "false") ?? buttons[0];`+
			`if (!button) return ""; button.focus(); return button.textContent;`,
	)
	if err != nil || selected == "" {
		t.Fatalf("focus Crew Preview control = %q, %v", selected, err)
	}
	alreadySelected, err := driver.evaluateBool(
		t.Context(),
		`return document.activeElement.getAttribute("aria-pressed") === "true";`,
	)
	if err != nil {
		t.Fatalf("inspect Crew Preview control: %v", err)
	}
	if !alreadySelected {
		if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
			t.Fatalf("activate Crew Preview control: %v", err)
		}
	}
	previewScript := `return document.querySelector('[data-item="preview"]').textContent === ` +
		strconv.Quote(selected) + `;`
	if err = driver.waitFor(t.Context(), 15*time.Second, previewScript); err != nil {
		t.Fatalf("wait for committed Crew Preview command: %v", err)
	}
	focused, err = driver.evaluateBool(
		t.Context(),
		`const button = document.querySelector("#take");`+
			`button.focus(); return document.activeElement === button;`,
	)
	if err != nil || !focused {
		t.Fatalf("focus Crew Take control = %t, %v", focused, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("activate Crew Take control: %v", err)
	}
	takenScript := `return document.querySelector('[data-item="programOutput"]').textContent === ` +
		strconv.Quote(selected) + `;`
	if err = driver.waitFor(t.Context(), 15*time.Second, takenScript); err != nil {
		details, detailErr := driver.evaluateString(
			t.Context(),
			`return JSON.stringify({`+
				`status: document.querySelector("#connection-status")?.textContent, `+
				`preview: document.querySelector('[data-item="preview"]')?.textContent, `+
				`programOutput: document.querySelector('[data-item="programOutput"]')?.textContent, `+
				`displays: [...document.querySelectorAll("#displays li")].map(`+
				`(display) => ({text: display.textContent, delivery: display.dataset.delivery}))`+
				`});`,
		)
		t.Fatalf(
			"wait for durable Crew Take on two Displays: %v; page = %s, %v",
			err,
			details,
			detailErr,
		)
	}
	programOutputJSON, err := driver.evaluateString(
		t.Context(),
		`const output = document.querySelector('[data-item="programOutput"]'); `+
			`return JSON.stringify({`+
			`kind: output.dataset.kind, `+
			`entry_id: output.dataset.entryId, `+
			`title: output.textContent, `+
			`revision: output.dataset.revision`+
			`});`,
	)
	if err != nil {
		t.Fatalf("read committed Program Output: %v", err)
	}
	var programOutput browserProgramOutputEvidence
	if err = json.Unmarshal([]byte(programOutputJSON), &programOutput); err != nil {
		t.Fatalf("decode committed Program Output: %v", err)
	}
	return evidence, connectedDisplayIDs, programOutput
}

func prepareBrowserEnrollment(
	t *testing.T,
	server *runningServer,
) (string, string) {
	t.Helper()
	client := authenticatedClient(t)
	response := get(t, client, server.address, "/display")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read browser Display Enrollment: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Display Enrollment = %d %q", response.StatusCode, body)
	}
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(string(body))
	if code == "" {
		t.Fatalf("Display Enrollment code missing: %s", body)
	}
	cookie := browserCookie(
		t,
		client,
		"http://"+server.address+"/display",
		"beamers_display",
		"/display",
	)
	return code, cookie.Value
}

func claimBrowserEnrollment(
	t *testing.T,
	administrator *http.Client,
	server *runningServer,
	code string,
	displayID int,
	name string,
) {
	t.Helper()
	page := get(
		t,
		administrator,
		server.address,
		"/admin/displays/enroll?code="+url.QueryEscape(code),
	)
	build := page.Header.Get("X-Beamers-Build")
	closeErr := page.Body.Close()
	if closeErr != nil {
		t.Fatalf("close Display claim page: %v", closeErr)
	}
	if page.StatusCode != http.StatusOK || build == "" {
		t.Fatalf("Display claim page = %d, build %q", page.StatusCode, build)
	}
	response := postForm(t, administrator, server.address, url.Values{
		"code":          {code},
		"name":          {name},
		"command_id":    {fmt.Sprintf("claim-browser-certification-%d", displayID)},
		"build_version": {build},
	})
	body, readErr := io.ReadAll(response.Body)
	closeErr = response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Display claim result: %v", err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("claim Display = %d %q", response.StatusCode, body)
	}
	assertJSONRequest(
		t,
		administrator,
		server.address,
		fmt.Sprintf("/admin/displays/%d/assign", displayID),
		map[string]any{
			"event_id": 1, "location_id": 1, "view_key": "competition-output",
			"command_id": fmt.Sprintf("assign-browser-certification-%d", displayID),
		},
		http.StatusOK,
		fmt.Sprintf(
			"{\"display_id\":%d,\"event_id\":1,\"location_id\":1,"+
				"\"view_key\":\"competition-output\"}\n",
			displayID,
		),
	)
}

func browserCookie(
	t *testing.T,
	client *http.Client,
	target string,
	name string,
	path string,
) *http.Cookie {
	t.Helper()
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse browser cookie URL: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(targetURL) {
		if cookie.Name == name {
			cookie.Path = path
			return cookie
		}
	}
	t.Fatalf("browser cookie %q missing for %s", name, target)
	return nil
}

func addBrowserCookie(t *testing.T, driver *webDriver, cookie *http.Cookie) {
	t.Helper()
	if err := driver.addCookie(t.Context(), cookie); err != nil {
		t.Fatalf("add browser cookie %q: %v", cookie.Name, err)
	}
}

func closeBrowserSession(t *testing.T, driver *webDriver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.close(ctx); err != nil {
		t.Fatalf("close browser session: %v", err)
	}
}

func browserMajor(t *testing.T, version string) int {
	t.Helper()
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil {
		t.Fatalf("parse browser version %q: %v", version, err)
	}
	return major
}

func buildBrowserBeamers(t *testing.T, version string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "beamers")
	command := exec.CommandContext(
		t.Context(),
		"go",
		"build",
		"-o",
		bin,
		"-ldflags",
		"-X github.com/dotwaffle/beamers/internal/buildinfo.version="+version,
		"../cmd/beamers",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build browser Beamers: %v\n%s", err, output)
	}
	return bin
}

func browserCertificationCommit(t *testing.T) string {
	t.Helper()
	if commit := os.Getenv("GITHUB_SHA"); commit != "" {
		return commit
	}
	status := exec.CommandContext(
		t.Context(),
		"git",
		"status",
		"--porcelain",
		"--untracked-files=all",
	)
	output, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("read browser certification worktree: %v\n%s", err, output)
	}
	if err = validateBrowserCertificationWorktree(output); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	output, err = command.Output()
	if err != nil {
		t.Fatalf("read browser certification commit: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func validateBrowserCertificationWorktree(status []byte) error {
	if len(bytes.TrimSpace(status)) != 0 {
		return errors.New("browser certification requires a clean worktree")
	}
	return nil
}

func writeBrowserCertificationReport(
	t *testing.T,
	path string,
	report browserCertificationReport,
) {
	t.Helper()
	if err := report.validate(); err != nil {
		t.Fatalf("validate browser certification report: %v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode browser certification report: %v", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create browser certification report directory: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatalf("create browser certification report without overwrite: %v", err)
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		t.Fatalf("write browser certification report: %v", err)
	}
}

func TestWebDriverSessionUsesW3CProtocol(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost || request.URL.Path != "/session" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(
			`{"value":{"sessionId":"session-1","capabilities":{"browserVersion":"147.0.1"}}}`,
		))
	}))
	t.Cleanup(server.Close)

	driver, err := newWebDriver(
		t.Context(),
		server.Client(),
		server.URL,
		map[string]any{"browserName": "chrome"},
	)
	if err != nil {
		t.Fatalf("start WebDriver session: %v", err)
	}
	if driver.sessionID != "session-1" || driver.browserVersion != "147.0.1" {
		t.Fatalf("WebDriver session = %+v", driver)
	}
}

func TestWebDriverReportsProtocolErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(
			`{"value":{"error":"session not created","message":"browser failed to start"}}`,
		))
	}))
	t.Cleanup(server.Close)

	_, err := newWebDriver(
		t.Context(),
		server.Client(),
		server.URL,
		map[string]any{"browserName": "chrome"},
	)
	if err == nil || !strings.Contains(err.Error(), "browser failed to start") {
		t.Fatalf("WebDriver error = %v", err)
	}
}

func TestBrowserPageEvidenceFailsClosed(t *testing.T) {
	t.Parallel()
	err := (browserPageEvidence{
		Surface:           "enrollment",
		UnlabeledControls: []string{"input[name=name]"},
		SmallTargets:      []string{"button"},
		ContrastFailures:  []string{"main"},
	}).validate()
	if err == nil {
		t.Fatal("incomplete page evidence passed")
	}
	found := err.Error()
	for _, want := range []string{
		"title",
		"language",
		"main landmark",
		"heading",
		"page structure",
		"keyboard operation",
		"visible focus",
		"unlabeled controls",
		"small targets",
		"contrast failures",
	} {
		if !strings.Contains(found, want) {
			t.Errorf("page evidence error %q does not contain %q", found, want)
		}
	}
}

func TestDisplayPageEvidenceRequiresNonColorReducedMotionStatus(t *testing.T) {
	t.Parallel()
	err := (browserPageEvidence{
		Surface:  "display",
		Title:    "Standby",
		Language: "en",
		Main:     true,
		Heading:  true,
	}).validate()
	if err == nil {
		t.Fatal("incomplete Display evidence passed")
	}
	found := err.Error()
	if !strings.Contains(found, "reduced motion") ||
		!strings.Contains(found, "non-color status") {
		t.Fatalf("Display evidence error = %q", found)
	}
}

func TestWebDriverAuditsServedPageEvidence(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/session":
			_, _ = response.Write([]byte(
				`{"value":{"sessionId":"session-1","capabilities":{"browserVersion":"147.0.1"}}}`,
			))
		case "/session/session-1/execute/sync":
			_, _ = response.Write([]byte(
				`{"value":{"surface":"schedule","title":"Schedule","language":"en-GB",` +
					`"main":true,"heading":true,"structure":true,` +
					`"keyboard_operable":true,"focus_visible":true}}`,
			))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	driver, err := newWebDriver(
		t.Context(),
		server.Client(),
		server.URL,
		map[string]any{"browserName": "chrome"},
	)
	if err != nil {
		t.Fatalf("start WebDriver session: %v", err)
	}
	evidence, err := driver.auditPage(t.Context(), "schedule")
	if err != nil {
		t.Fatalf("audit served page: %v", err)
	}
	if evidence.Title != "Schedule" || evidence.Language != "en-GB" {
		t.Fatalf("page evidence = %+v", evidence)
	}
}

func TestWebDriverUsesNavigationCookieKeyboardAndScriptCommands(t *testing.T) {
	t.Parallel()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case "/session":
			_, _ = response.Write([]byte(
				`{"value":{"sessionId":"session-1","capabilities":{"browserVersion":"147.0.1"}}}`,
			))
		case "/session/session-1/execute/sync":
			var payload struct {
				Args []any `json:"args"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode script command: %v", err)
			}
			if payload.Args == nil {
				t.Error("script command args = null, want []")
			}
			_, _ = response.Write([]byte(`{"value":true}`))
		default:
			_, _ = response.Write([]byte(`{"value":null}`))
		}
	}))
	t.Cleanup(server.Close)

	driver, err := newWebDriver(
		t.Context(),
		server.Client(),
		server.URL,
		map[string]any{"browserName": "chrome"},
	)
	if err != nil {
		t.Fatalf("start WebDriver session: %v", err)
	}
	if err = driver.navigate(t.Context(), "http://beamers.test/"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err = driver.addCookie(t.Context(), &http.Cookie{
		Name: "beamers_session", Value: "credential", Path: "/", HttpOnly: true,
	}); err != nil {
		t.Fatalf("add cookie: %v", err)
	}
	if err = driver.pressKey(t.Context(), "\uE004"); err != nil {
		t.Fatalf("press Tab: %v", err)
	}
	found, err := driver.evaluateBool(t.Context(), "return true;")
	if err != nil || !found {
		t.Fatalf("evaluate boolean = %t, %v", found, err)
	}
	if err = driver.execute(t.Context(), "return;"); err != nil {
		t.Fatalf("execute script: %v", err)
	}
	if err = driver.close(t.Context()); err != nil {
		t.Fatalf("close WebDriver: %v", err)
	}
	want := []string{
		"/session",
		"/session/session-1/url",
		"/session/session-1/cookie",
		"/session/session-1/actions",
		"/session/session-1/execute/sync",
		"/session/session-1/execute/sync",
		"/session/session-1",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("WebDriver paths = %v, want %v", paths, want)
	}
}

func TestWebDriverUsesVirtualAuthenticatorCommands(t *testing.T) {
	t.Parallel()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		paths = append(paths, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/session":
			_, _ = response.Write([]byte(
				`{"value":{"sessionId":"session-1","capabilities":{"browserVersion":"147.0.1"}}}`,
			))
		case "/session/session-1/webauthn/authenticator":
			var configuration map[string]any
			if err := json.NewDecoder(request.Body).Decode(&configuration); err != nil {
				t.Errorf("decode virtual authenticator configuration: %v", err)
			}
			if configuration["protocol"] != "ctap2" ||
				configuration["transport"] != "usb" ||
				configuration["isUserConsenting"] != true {
				t.Errorf("virtual authenticator configuration = %v", configuration)
			}
			_, _ = response.Write([]byte(`{"value":"authenticator-1"}`))
		default:
			_, _ = response.Write([]byte(`{"value":null}`))
		}
	}))
	t.Cleanup(server.Close)

	driver, err := newWebDriver(
		t.Context(),
		server.Client(),
		server.URL,
		map[string]any{"browserName": "chrome", "webauthn:virtualAuthenticators": true},
	)
	if err != nil {
		t.Fatalf("start WebDriver session: %v", err)
	}
	authenticatorID, err := driver.addVirtualAuthenticator(t.Context(), "usb")
	if err != nil || authenticatorID != "authenticator-1" {
		t.Fatalf("add virtual authenticator = %q, %v", authenticatorID, err)
	}
	if err = driver.deleteCookie(t.Context(), "beamers_session"); err != nil {
		t.Fatalf("delete cookie: %v", err)
	}
	if err = driver.removeVirtualAuthenticator(t.Context(), authenticatorID); err != nil {
		t.Fatalf("remove virtual authenticator: %v", err)
	}
	want := []string{
		"POST /session",
		"POST /session/session-1/webauthn/authenticator",
		"DELETE /session/session-1/cookie/beamers_session",
		"DELETE /session/session-1/webauthn/authenticator/authenticator-1",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("WebDriver paths = %v, want %v", paths, want)
	}
}

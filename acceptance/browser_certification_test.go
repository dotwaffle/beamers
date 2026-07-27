package acceptance_test

import (
	"bytes"
	"context"
	_ "embed"
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

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
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
	var crewPages, displayPages, enrollmentPages, resultsPages int
	for _, page := range report.Pages {
		if err := page.validate(); err != nil {
			return fmt.Errorf("validate %s page evidence: %w", page.Surface, err)
		}
		switch page.Surface {
		case "crew_control":
			crewPages++
		case "display":
			displayPages++
		case "enrollment":
			enrollmentPages++
		case "results":
			resultsPages++
		}
	}
	if crewPages != 1 || displayPages != 2 || enrollmentPages != 1 ||
		resultsPages != 1 {
		return fmt.Errorf(
			"browser evidence has %d Crew, %d Display, %d Enrollment, and %d Results pages, want 1, 2, 1, and 1",
			crewPages,
			displayPages,
			enrollmentPages,
			resultsPages,
		)
	}
	return nil
}

func TestBrowserCertificationReportRequiresDurableTwoDisplayTake(t *testing.T) {
	validReport := func() browserCertificationReport {
		programOutput := browserProgramOutputEvidence{
			Kind: "PROGRAM_ITEM_KIND_STARTING", Title: "Opening Keynote", Revision: "1",
		}
		return browserCertificationReport{
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
				validBrowserPageEvidence("crew_control"),
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

func validBrowserPageEvidence(surface string) browserPageEvidence {
	evidence := browserPageEvidence{
		Surface: surface, Title: "Evidence", Language: "en", Main: true, Heading: true,
	}
	if surface == "display" {
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
	if evidence.Surface == "display" {
		if !evidence.ReducedMotion {
			findings = append(findings, "missing reduced motion")
		}
		if !evidence.NonColorStatus {
			findings = append(findings, "missing non-color status")
		}
	} else {
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

func (driver *webDriver) setWindowSize(ctx context.Context, width, height int) error {
	_, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/window/rect"),
		map[string]int{"width": width, "height": height},
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
) (bool, error) {
	value, err := driver.command(
		ctx,
		http.MethodPost,
		driver.sessionPath("/execute/sync"),
		map[string]any{"script": script, "args": []any{}},
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
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		found, err := driver.evaluateBool(ctx, script)
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
	prepareActiveSchedule(t, administrator, server)
	crewSessionID, _ := addCompetitionSession(t, administrator, server)
	prepareReleasedBrowserResults(t, administrator, server, crewSessionID)
	for index := range displays {
		displays[index].enrollmentCode, displays[index].credential =
			prepareBrowserEnrollment(t, server)
	}
	origin := "http://" + server.address

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
	assertResponsivePageWidths(t, crewDriver, origin+"/schedule", 320, 1440)
	report.Pages = append(
		report.Pages,
		certifyFrontendTheme(t, crewDriver, origin),
		certifyInteractivePage(t, crewDriver, origin+"/schedule", "schedule"),
		certifyResultsPage(t, crewDriver, origin, crewSessionID),
	)
	addBrowserCookie(t, crewDriver, browserCookie(
		t,
		administrator,
		origin,
		"beamers_session",
		"/",
	))
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage", 320, 1440)
	assertResponsivePageWidths(t, crewDriver, origin+"/backstage/events/1/planning", 320, 1440)
	assertBackstageNavigationModes(t, crewDriver, origin)
	report.Pages = append(
		report.Pages,
		certifyInteractivePage(t, crewDriver, origin+"/backstage", "backstage"),
		certifyInteractivePage(
			t,
			crewDriver,
			origin+"/backstage/events/1/planning",
			"planning",
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

	crewEvidence, connectedDisplayIDs, programOutput := certifyCrewControl(
		t, crewDriver, origin, crewSessionID,
	)
	report.Pages = append(report.Pages, crewEvidence)
	report.CrewCommandCommitted = true
	report.DisplaysConnectedBeforeCommand = connectedDisplayIDs
	report.ProgramOutput = programOutput

	for index := range displays {
		report.Displays = append(report.Displays, captureBrowserDisplayOutput(
			t, &displays[index], programOutput,
		))
	}
	crewURL := origin + "/crew/program/" + strconv.FormatInt(crewSessionID, 10) +
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

func prepareReleasedBrowserResults(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	competitionID int64,
) {
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
}

func certifyResultsPage(
	t *testing.T,
	driver *webDriver,
	origin string,
	competitionID int64,
) browserPageEvidence {
	t.Helper()
	evidence := certifyInteractivePage(
		t,
		driver,
		origin+"/results/events/1/standalone/"+
			strconv.FormatInt(competitionID, 10),
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
	if err := display.driver.navigate(t.Context(), origin+"/schedule"); err != nil {
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
		if err := driver.setWindowSize(t.Context(), width, 900); err != nil {
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
		if err := driver.setWindowSize(t.Context(), check.width, 900); err != nil {
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
		`const button = [...document.querySelectorAll("#program-items button")].find(`+
			`(candidate) => candidate.getAttribute("aria-pressed") === "false" && `+
			`candidate.textContent !== "standby");`+
			`if (!button) return ""; button.focus(); return button.textContent;`,
	)
	if err != nil || selected == "" {
		t.Fatalf("focus Crew Preview control = %q, %v", selected, err)
	}
	if err = driver.pressKey(t.Context(), "\uE007"); err != nil {
		t.Fatalf("activate Crew Preview control: %v", err)
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
	command := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("read browser certification commit: %v", err)
	}
	return strings.TrimSpace(string(output))
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
					`"main":true,"heading":true,"keyboard_operable":true,"focus_visible":true}}`,
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
	if err = driver.navigate(t.Context(), "http://beamers.test/schedule"); err != nil {
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
	if err = driver.close(t.Context()); err != nil {
		t.Fatalf("close WebDriver: %v", err)
	}
	want := []string{
		"/session",
		"/session/session-1/url",
		"/session/session-1/cookie",
		"/session/session-1/actions",
		"/session/session-1/execute/sync",
		"/session/session-1",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("WebDriver paths = %v, want %v", paths, want)
	}
}

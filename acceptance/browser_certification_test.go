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
	Engine          string
	Role            string
	ExpectedMajor   int
	BrowserBinary   string
	WebDriverBinary string
	ReportPath      string
}

type browserCertificationReport struct {
	Commit               string                `json:"commit"`
	Engine               string                `json:"engine"`
	Role                 string                `json:"role"`
	BrowserVersion       string                `json:"browser_version"`
	WebDriverVersion     string                `json:"webdriver_version"`
	GitHubRunID          string                `json:"github_run_id,omitempty"`
	RunnerOS             string                `json:"runner_os,omitempty"`
	RunnerArchitecture   string                `json:"runner_architecture,omitempty"`
	GeneratedAt          time.Time             `json:"generated_at"`
	Pages                []browserPageEvidence `json:"pages"`
	CrewCommandCommitted bool                  `json:"crew_command_committed"`
	DisplayRetainedFrame bool                  `json:"display_retained_frame"`
	DisplayReconnected   bool                  `json:"display_reconnected"`
	KioskEvidence        bool                  `json:"kiosk_evidence"`
	ManualAccessibility  bool                  `json:"manual_accessibility"`
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
	webDriverEndpoint, webDriverVersion := startBrowserDriver(t, config)
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
	enrollmentCode, displayCredential := prepareBrowserEnrollment(
		t,
		server,
	)
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
	driver := startBrowserSession(t, client, webDriverEndpoint, config)
	report.BrowserVersion = driver.browserVersion
	if major := browserMajor(t, driver.browserVersion); major != config.ExpectedMajor {
		t.Fatalf(
			"browser major = %d from %q, want %d",
			major,
			driver.browserVersion,
			config.ExpectedMajor,
		)
	}
	report.Pages = append(
		report.Pages,
		certifyInteractivePage(t, driver, origin+"/schedule", "schedule"),
	)
	addBrowserCookie(t, driver, browserCookie(
		t,
		administrator,
		origin,
		"beamers_session",
		"/",
	))
	report.Pages = append(report.Pages,
		certifyInteractivePage(
			t,
			driver,
			origin+"/admin/displays/enroll?code="+url.QueryEscape(enrollmentCode),
			"enrollment",
		),
		certifyCrewControl(t, driver, origin, crewSessionID),
	)
	report.CrewCommandCommitted = true
	closeBrowserSession(t, driver)

	claimBrowserEnrollment(t, administrator, server, enrollmentCode)
	driver = startBrowserSession(t, client, webDriverEndpoint, config)
	if err := driver.navigate(t.Context(), origin+"/schedule"); err != nil {
		t.Fatalf("navigate to Display cookie origin: %v", err)
	}
	for _, path := range []string{"/display", "/beamers.display.v1.DisplayService"} {
		addBrowserCookie(t, driver, &http.Cookie{
			Name:     "beamers_display",
			Value:    displayCredential,
			Path:     path,
			HttpOnly: true,
		})
	}
	if err := driver.navigate(t.Context(), origin+"/display"); err != nil {
		t.Fatalf("navigate to Display: %v", err)
	}
	if err := driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.documentElement.dataset.connection === "connected";`,
	); err != nil {
		t.Fatalf("wait for connected Display: %v", err)
	}
	displayEvidence, err := driver.auditPage(t.Context(), "display")
	if err != nil {
		t.Fatal(err)
	}
	report.Pages = append(report.Pages, displayEvidence)
	committedFrame, err := driver.evaluateString(
		t.Context(),
		`return document.querySelector("main").textContent;`,
	)
	if err != nil {
		t.Fatalf("read committed Display frame: %v", err)
	}
	server.stop(t)
	if err = driver.waitFor(
		t.Context(),
		15*time.Second,
		`return document.documentElement.dataset.connection === "disconnected";`,
	); err != nil {
		t.Fatalf("wait for disconnected Display: %v", err)
	}
	retainedFrame, err := driver.evaluateString(
		t.Context(),
		`return document.querySelector("main").textContent;`,
	)
	if err != nil {
		t.Fatalf("read disconnected Display frame: %v", err)
	}
	if retainedFrame != committedFrame {
		t.Fatal("Display replaced its committed frame while disconnected")
	}
	report.DisplayRetainedFrame = true

	restarted := startBeamersAt(t, bin, dataDir, server.address)
	if err = driver.waitFor(
		t.Context(),
		30*time.Second,
		`return document.documentElement.dataset.connection === "connected";`,
	); err != nil {
		t.Fatalf("wait for Display recovery after restart: %v", err)
	}
	recoveredFrame, err := driver.evaluateString(
		t.Context(),
		`return document.querySelector("main").textContent;`,
	)
	if err != nil {
		t.Fatalf("read recovered Display frame: %v", err)
	}
	if recoveredFrame != committedFrame {
		t.Fatal("Display changed its committed frame after compatible restart")
	}
	report.DisplayReconnected = true
	closeBrowserSession(t, driver)
	restarted.stop(t)
	writeBrowserCertificationReport(t, config.ReportPath, report)
}

func browserConfigFromEnvironment(t *testing.T) browserCertificationConfig {
	t.Helper()
	expectedMajor, err := strconv.Atoi(os.Getenv("BEAMERS_BROWSER_MAJOR"))
	if err != nil || expectedMajor <= 0 {
		t.Fatal("BEAMERS_BROWSER_MAJOR must be a positive browser major")
	}
	config := browserCertificationConfig{
		Engine:          os.Getenv("BEAMERS_BROWSER_ENGINE"),
		Role:            os.Getenv("BEAMERS_BROWSER_ROLE"),
		ExpectedMajor:   expectedMajor,
		BrowserBinary:   os.Getenv("BEAMERS_BROWSER_BINARY"),
		WebDriverBinary: os.Getenv("BEAMERS_WEBDRIVER_BINARY"),
		ReportPath:      os.Getenv("BEAMERS_BROWSER_REPORT"),
	}
	if config.Engine != "chromium" && config.Engine != "firefox" {
		t.Fatalf("unsupported BEAMERS_BROWSER_ENGINE %q", config.Engine)
	}
	if config.Role != "current" && config.Role != "previous" {
		t.Fatalf("unsupported BEAMERS_BROWSER_ROLE %q", config.Role)
	}
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
	capabilities := map[string]any{"browserName": "chrome"}
	switch config.Engine {
	case "chromium":
		capabilities["goog:chromeOptions"] = map[string]any{
			"binary": config.BrowserBinary,
			"args": []string{
				"--headless=new",
				"--no-sandbox",
				"--disable-dev-shm-usage",
				"--force-prefers-reduced-motion=reduce",
				"--window-size=1280,720",
			},
		}
	case "firefox":
		capabilities["browserName"] = "firefox"
		capabilities["moz:firefoxOptions"] = map[string]any{
			"binary": config.BrowserBinary,
			"args":   []string{"-headless"},
			"prefs":  map[string]any{"ui.prefersReducedMotion": 1},
		}
	}
	driver, err := newWebDriver(t.Context(), client, endpoint, capabilities)
	if err != nil {
		t.Fatalf("start %s WebDriver session: %v", config.Engine, err)
	}
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
	if err := driver.pressKey(t.Context(), "\uE004"); err != nil {
		t.Fatalf("Tab through %s: %v", surface, err)
	}
	evidence, err := driver.auditPage(t.Context(), surface)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func certifyCrewControl(
	t *testing.T,
	driver *webDriver,
	origin string,
	sessionID int64,
) browserPageEvidence {
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
	if err = driver.pressKey(t.Context(), "\uE004"); err != nil {
		t.Fatalf("Tab through Crew control: %v", err)
	}
	evidence, err := driver.auditPage(t.Context(), "crew_control")
	if err != nil {
		t.Fatal(err)
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
	return evidence
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
		"name":          {"Browser Certification"},
		"command_id":    {"claim-browser-certification"},
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

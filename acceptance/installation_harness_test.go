package acceptance_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type runningServer struct {
	address       string
	publicAddress string
	bin           string
	dataDir       string
	cmd           *exec.Cmd
	done          chan error
}

var beamersTestBinary string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "beamers-acceptance-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare acceptance binary: %v\n", err)
		os.Exit(1)
	}
	beamersTestBinary = filepath.Join(directory, "beamers")
	cmd := exec.CommandContext(
		context.Background(),
		"go",
		"build",
		"-o",
		beamersTestBinary,
		"../cmd/beamers",
	)
	if output, buildErr := cmd.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build acceptance binary: %v\n%s", buildErr, output)
		_ = os.RemoveAll(directory)
		os.Exit(1)
	}

	code := m.Run()
	if err = os.RemoveAll(directory); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "remove acceptance binary: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func buildBeamers(t *testing.T) string {
	t.Helper()
	if beamersTestBinary == "" {
		t.Fatal("acceptance binary is unavailable")
	}
	return beamersTestBinary
}

func runBeamers(t *testing.T, bin string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run beamers %v: %v\n%s", args, err, output)
	}
}

func runBeamersOutput(t *testing.T, bin string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run beamers %v: %v", args, err)
	}
	return string(output)
}

func runBeamersFails(t *testing.T, bin string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("beamers %v succeeded; output:\n%s", args, output)
	}
}

func startBeamers(t *testing.T, bin, dataDir string) *runningServer {
	t.Helper()
	return startBeamersAt(t, bin, dataDir, "127.0.0.1:0")
}

func startBeamersAt(t *testing.T, bin, dataDir, listenAddress string) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAt(t, bin, dataDir, "", listenAddress)
}

func startBeamersWithAttachments(
	t *testing.T,
	bin, dataDir, attachmentsDir string,
) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAt(
		t,
		bin,
		dataDir,
		attachmentsDir,
		"127.0.0.1:0",
	)
}

func startBeamersWithAttachmentsAt(
	t *testing.T,
	bin, dataDir, attachmentsDir, listenAddress string,
) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAndPublicAt(
		t, bin, dataDir, attachmentsDir, listenAddress, false,
	)
}

func startBeamersWithPublicListener(t *testing.T, bin, dataDir string) *runningServer {
	t.Helper()
	return startBeamersWithAttachmentsAndPublicAt(
		t, bin, dataDir, "", "127.0.0.1:0", true,
	)
}

func startBeamersWithAttachmentsAndPublicAt(
	t *testing.T,
	bin, dataDir, attachmentsDir, listenAddress string,
	separatePublic bool,
) *runningServer {
	t.Helper()

	args := []string{"serve", "--data-dir", dataDir, "--listen", listenAddress}
	if attachmentsDir != "" {
		args = append(args, "--attachments-dir", attachmentsDir)
	}
	if separatePublic {
		args = append(args, "--public-listen", "127.0.0.1:0")
	}
	cmd := exec.CommandContext(t.Context(), bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("capture beamers stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start beamers: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	server := &runningServer{bin: bin, dataDir: dataDir, cmd: cmd, done: done}
	t.Cleanup(func() {
		if server.cmd.Process != nil {
			_ = server.cmd.Process.Kill()
		}
	})
	server.address, server.publicAddress = waitForListeningAddresses(
		t, stderr, done, separatePublic,
	)
	return server
}

func waitForListeningAddresses(
	t *testing.T,
	stderr io.Reader,
	done <-chan error,
	separatePublic bool,
) (string, string) {
	t.Helper()

	type result struct {
		privateAddress string
		publicAddress  string
		err            error
	}
	listening := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		var privateAddress, publicAddress string
		for scanner.Scan() {
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			switch entry.Message {
			case "server listening":
				privateAddress = entry.Address
			case "public server listening":
				publicAddress = entry.Address
			}
			if privateAddress != "" && (!separatePublic || publicAddress != "") {
				listening <- result{
					privateAddress: privateAddress,
					publicAddress:  publicAddress,
				}
				return
			}
		}
		listening <- result{err: scanner.Err()}
	}()

	select {
	case got := <-listening:
		if got.err != nil {
			t.Fatalf("read server startup: %v", got.err)
		}
		if got.privateAddress == "" || separatePublic && got.publicAddress == "" {
			t.Fatal("server exited without announcing its address")
		}
		return got.privateAddress, got.publicAddress
	case err := <-done:
		t.Fatalf("server exited during startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not announce its address")
	}
	return "", ""
}

func assertProbe(t *testing.T, address, path, wantBody string) {
	t.Helper()
	result := requestProbe(t.Context(), address, path, 5*time.Second)
	assertProbeResult(t, path, result, http.StatusOK, wantBody)
}

func installationThemeForm(csrf, action, commandID string) url.Values {
	return url.Values{
		"csrf_token":       {csrf},
		"action":           {action},
		"command_id":       {commandID},
		"brand_asset":      {"signal"},
		"background_color": {"#080b15"},
		"surface_color":    {"#141a2c"},
		"border_color":     {"#7180aa"},
		"text_color":       {"#f1f5ff"},
		"muted_color":      {"#c2cbe0"},
		"accent_color":     {"#62ebcb"},
		"link_color":       {"#79d7ff"},
		"focus_color":      {"#ffdf6e"},
		"live_color":       {"#ff6b5e"},
		"warning_color":    {"#f5b544"},
		"danger_color":     {"#ff86a2"},
		"success_color":    {"#5ee38b"},
		"background":       {"nebula"},
		"typeface":         {"demoscene"},
		"transition":       {"fade"},
		"effect":           {"starfield"},
		"motion":           {"subtle"},
	}
}

func eventThemeForm(csrf, action, commandID string) url.Values {
	return url.Values{
		"csrf_token": {csrf},
		"action":     {action},
		"command_id": {commandID},
	}
}

func browserCSRFToken(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
) string {
	t.Helper()

	response := get(t, client, address, path)
	defer func() { _ = response.Body.Close() }()
	_ = readResponseBody(t, response)
	pageURL, err := url.Parse("http://" + address + path)
	if err != nil {
		t.Fatalf("parse browser page URL: %v", err)
	}
	for _, cookie := range client.Jar.Cookies(pageURL) {
		if cookie.Name == "beamers_csrf" {
			return cookie.Value
		}
	}
	t.Fatal("browser page did not set a CSRF cookie")
	return ""
}

func postBrowserForm(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	values url.Values,
) (int, string) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+path,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create browser form request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("submit browser form: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, readResponseBody(t, response)
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func assertGETContains(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	want string,
) {
	t.Helper()

	response := get(t, client, address, path)
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, want) {
		t.Fatalf("GET %s = %d %q, want %d containing %q", path, response.StatusCode, body, http.StatusOK, want)
	}
}

func postForm(
	t *testing.T,
	client *http.Client,
	address string,
	values url.Values,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+address+"/admin/displays/enroll",
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create form request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send form request: %v", err)
	}
	return response
}

func crewBuild(t *testing.T, client *http.Client, address string) string {
	t.Helper()

	response := get(t, client, address, "/admin/displays")
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close crew build response: %v", closeErr)
	}
	buildVersion := response.Header.Get("X-Beamers-Build")
	if buildVersion == "" {
		t.Fatal("crew response does not identify the server build")
	}
	return buildVersion
}

func assertRecoveryProbes(t *testing.T, address string) {
	t.Helper()
	assertProbe(t, address, "/livez", "live\n")
	readiness := requestProbe(t.Context(), address, "/readyz", 5*time.Second)
	assertProbeResult(t, "/readyz", readiness, http.StatusServiceUnavailable, "not ready\n")
}

func assertRecoveryDiagnostics(t *testing.T, address, wantStorage, wantDetail string) {
	t.Helper()
	response := get(t, authenticatedClient(t), address, "/diagnostics")
	var diagnostic struct {
		Mode    string `json:"mode"`
		Storage string `json:"storage"`
		Detail  string `json:"detail"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&diagnostic)
	closeErr := response.Body.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		t.Fatalf("read recovery diagnostics: %v", err)
	}
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Cache-Control") != "no-store" ||
		diagnostic.Mode != "recovery" ||
		diagnostic.Storage != wantStorage ||
		diagnostic.Detail == "" ||
		len(diagnostic.Detail) > 512 ||
		!strings.Contains(diagnostic.Detail, wantDetail) {
		t.Fatalf("recovery diagnostics = %d %+v, headers %v", response.StatusCode, diagnostic, response.Header)
	}
}

func assertLoopbackAddress(t *testing.T, address string) {
	t.Helper()
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse server address %q: %v", address, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("recovery server host = %q, want 127.0.0.1", host)
	}
}

type probeResult struct {
	status int
	body   string
	err    error
}

func requestProbe(ctx context.Context, address, path string, timeout time.Duration) probeResult {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, http.NoBody)
	if err != nil {
		return probeResult{err: err}
	}
	response, err := client.Do(request)
	if err != nil {
		return probeResult{err: err}
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return probeResult{err: errors.Join(err, closeErr)}
	}
	return probeResult{status: response.StatusCode, body: string(body)}
}

func authenticatedClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func assertJSONRequest(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	body any,
	wantStatus int,
	wantBody string,
) http.Header {
	t.Helper()

	result := requestJSON(t.Context(), client, address, path, body)
	if result.err != nil {
		t.Fatalf("POST %s: %v", path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Fatalf(
			"POST %s = %d %q, want %d %q",
			path,
			result.status,
			result.body,
			wantStatus,
			wantBody,
		)
	}
	return result.header
}

func assertDisplayListContains(
	t *testing.T,
	client *http.Client,
	address string,
	want string,
) {
	t.Helper()

	const path = "/admin/displays"
	response := get(t, client, address, path)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
		t.Fatalf("GET %s = %d %q, want %d containing %q", path, response.StatusCode, body, http.StatusOK, want)
	}
}

func assertJSONMethodRequest(
	t *testing.T,
	method string,
	client *http.Client,
	address string,
	path string,
	body any,
	wantStatus int,
	wantBody string,
) {
	t.Helper()

	result := requestJSONMethod(t.Context(), method, client, address, path, body)
	if result.err != nil {
		t.Fatalf("%s %s: %v", method, path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Fatalf(
			"%s %s = %d %q, want %d %q",
			method, path, result.status, result.body, wantStatus, wantBody,
		)
	}
}

type jsonResponse struct {
	header http.Header
	status int
	body   string
	err    error
}

type displaySnapshotState struct {
	ProtocolVersion      string `json:"protocolVersion"`
	AssetVersion         string `json:"assetVersion"`
	StreamID             string `json:"streamId"`
	StreamPosition       string `json:"streamPosition"`
	ActiveEventID        string `json:"activeEventId"`
	ActivationGeneration string `json:"activationGeneration"`
	PublishedRevision    string `json:"publishedRevision"`
	StageMessage         struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"stageMessage"`
	TechnicalDifficulties struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"technicalDifficulties"`
	UrgentNotice struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"urgentNotice"`
	EmergencyAlert struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"emergencyAlert"`
	Standby               bool   `json:"standby"`
	SnapshotToken         string `json:"snapshotToken"`
	ProgramOutputRevision string `json:"programOutputRevision"`
	ProgramOutput         struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	} `json:"programOutput"`
	Composition struct {
		Layout struct {
			Key             string `json:"key"`
			RotationSeconds int    `json:"rotationSeconds"`
			Regions         []struct {
				Name       string `json:"name"`
				Widget     string `json:"widget"`
				Persistent bool   `json:"persistent"`
			} `json:"regions"`
		} `json:"layout"`
	} `json:"composition"`
}

type displayHealth struct {
	clockOffsetMilliseconds      int64
	clockUncertaintyMilliseconds int64
	rendererUnstable             bool
}

func readDisplaySnapshot(
	t *testing.T,
	client *http.Client,
	address string,
) displaySnapshotState {
	t.Helper()

	result := requestJSON(
		t.Context(),
		client,
		address,
		"/beamers.display.v1.DisplayService/GetSnapshot",
		map[string]any{},
	)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("Get Display Snapshot = %d %q, %v", result.status, result.body, result.err)
	}
	var decoded struct {
		Snapshot displaySnapshotState `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(result.body), &decoded); err != nil {
		t.Fatalf("decode Display Snapshot: %v", err)
	}
	return decoded.Snapshot
}

func acknowledgeDisplaySnapshot(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
) {
	t.Helper()

	acknowledgeDisplaySnapshotWithHealth(
		t,
		client,
		address,
		snapshot,
		displayHealth{},
	)
}

func acknowledgeDisplaySnapshotWithHealth(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
	health displayHealth,
) {
	t.Helper()

	result := requestDisplayAcknowledgment(t, client, address, snapshot, health)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("Acknowledge Display state = %d %q, %v", result.status, result.body, result.err)
	}
}

func requestDisplayAcknowledgment(
	t *testing.T,
	client *http.Client,
	address string,
	snapshot displaySnapshotState,
	health displayHealth,
) jsonResponse {
	t.Helper()

	result := requestJSON(
		t.Context(),
		client,
		address,
		"/beamers.display.v1.DisplayService/Acknowledge",
		map[string]any{
			"protocol_version":          snapshot.ProtocolVersion,
			"asset_version":             snapshot.AssetVersion,
			"stream_id":                 snapshot.StreamID,
			"stream_position":           snapshot.StreamPosition,
			"active_event_id":           snapshot.ActiveEventID,
			"activation_generation":     snapshot.ActivationGeneration,
			"published_revision":        snapshot.PublishedRevision,
			"stage_message_id":          protoJSONInteger(snapshot.StageMessage.ID),
			"stage_message_revision":    protoJSONInteger(snapshot.StageMessage.Revision),
			"technical_difficulties_id": protoJSONInteger(snapshot.TechnicalDifficulties.ID),
			"technical_difficulties_revision": protoJSONInteger(
				snapshot.TechnicalDifficulties.Revision,
			),
			"urgent_notice_id":       protoJSONInteger(snapshot.UrgentNotice.ID),
			"urgent_notice_revision": protoJSONInteger(snapshot.UrgentNotice.Revision),
			"emergency_alert_id":     protoJSONInteger(snapshot.EmergencyAlert.ID),
			"emergency_alert_revision": protoJSONInteger(
				snapshot.EmergencyAlert.Revision,
			),
			"standby":                        snapshot.Standby,
			"clock_offset_milliseconds":      health.clockOffsetMilliseconds,
			"clock_uncertainty_milliseconds": health.clockUncertaintyMilliseconds,
			"renderer_unstable":              health.rendererUnstable,
			"snapshot_token":                 snapshot.SnapshotToken,
		},
	)
	return result
}

func protoJSONInteger(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

func requestJSON(
	ctx context.Context,
	client *http.Client,
	address string,
	path string,
	body any,
) jsonResponse {
	return requestJSONMethod(ctx, http.MethodPost, client, address, path, body)
}

func requestMultipart(
	ctx context.Context,
	client *http.Client,
	address, path string,
	fields map[string]string,
	filename, mediaType string,
	content []byte,
) jsonResponse {
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return jsonResponse{err: err}
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return jsonResponse{err: err}
	}
	if _, err = part.Write(content); err != nil {
		return jsonResponse{err: err}
	}
	if err = writer.Close(); err != nil {
		return jsonResponse{err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, &encoded)
	if err != nil {
		return jsonResponse{err: err}
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(request)
	if err != nil {
		return jsonResponse{err: err}
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return jsonResponse{err: err}
	}
	return jsonResponse{header: response.Header.Clone(), status: response.StatusCode, body: string(responseBody)}
}

func requestJSONMethod(
	ctx context.Context,
	method string,
	client *http.Client,
	address string,
	path string,
	body any,
) jsonResponse {
	encoded, err := json.Marshal(body)
	if err != nil {
		return jsonResponse{err: errors.Join(errors.New("encode JSON request"), err)}
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://"+address+path,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return jsonResponse{err: errors.Join(errors.New("create JSON request"), err)}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return jsonResponse{err: err}
	}
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return jsonResponse{err: err}
	}
	return jsonResponse{
		header: response.Header.Clone(),
		status: response.StatusCode,
		body:   string(responseBody),
	}
}

func assertProtectedSessionCookie(t *testing.T, headers http.Header) {
	t.Helper()

	cookie := headers.Get("Set-Cookie")
	for _, attribute := range []string{"Path=/", "Expires=", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(cookie, attribute) {
			t.Errorf("session cookie %q does not contain %q", cookie, attribute)
		}
	}
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Errorf("authentication Cache-Control = %q, want no-store", got)
	}
}

func assertAuthenticated(t *testing.T, client *http.Client, address, wantName string) {
	t.Helper()

	response := get(t, client, address, "/auth/session")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close session response: %v", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/session status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var got struct {
		Name          string `json:"name"`
		Administrator bool   `json:"administrator"`
	}
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if got.Name != wantName || !got.Administrator {
		t.Errorf("session = %+v, want name %q and Administrator", got, wantName)
	}
}

func assertSessionRejected(t *testing.T, client *http.Client, address string) {
	t.Helper()

	response := get(t, client, address, "/auth/session")
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close rejected session response: %v", err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read rejected session response: %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized || string(body) != "authentication required\n" {
		t.Errorf(
			"GET /auth/session = %d %q, want %d %q",
			response.StatusCode,
			body,
			http.StatusUnauthorized,
			"authentication required\n",
		)
	}
}

func get(t *testing.T, client *http.Client, address, path string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+address+path,
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create GET %s: %v", path, err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return response
}

func assertGETResponse(
	t *testing.T,
	client *http.Client,
	address string,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := get(t, client, address, path)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read GET %s response: %v", path, err)
	}
	if response.StatusCode != wantStatus || string(body) != wantBody {
		t.Errorf(
			"GET %s = %d %q, want %d %q",
			path, response.StatusCode, body, wantStatus, wantBody,
		)
	}
}

func assertProbeResult(t *testing.T, path string, result probeResult, wantStatus int, wantBody string) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("request %s: %v", path, result.err)
	}
	if result.status != wantStatus || result.body != wantBody {
		t.Errorf("GET %s = %d %q, want %d %q", path, result.status, result.body, wantStatus, wantBody)
	}
}

func (server *runningServer) stop(t *testing.T) {
	t.Helper()

	if err := server.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop beamers: %v", err)
	}
	select {
	case err := <-server.done:
		if err != nil {
			t.Fatalf("beamers shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("beamers did not stop after %s", 10*time.Second)
	}
	server.cmd.Process = nil
}

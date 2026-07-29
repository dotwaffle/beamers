package acceptance_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestExecutableStopsOnINTAndTERMWithinDeclaredBudgets(t *testing.T) {
	bin := buildBeamers(t)
	for _, test := range []struct {
		name   string
		signal os.Signal
		budget string
	}{
		{name: "INT ten seconds", signal: os.Interrupt, budget: "10s"},
		{name: "TERM ten seconds", signal: syscall.SIGTERM, budget: "10s"},
		{name: "INT thirty seconds", signal: os.Interrupt, budget: "30s"},
		{name: "TERM thirty seconds", signal: syscall.SIGTERM, budget: "30s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "data")
			runBeamers(t, bin, "init", "--data-dir", dataDir)
			process := startShutdownProcess(t, bin,
				"--data-dir", dataDir,
				"--shutdown-timeout", test.budget,
			)

			started := time.Now()
			if err := process.cmd.Process.Signal(test.signal); err != nil {
				t.Fatalf("signal beamers: %v", err)
			}
			if err := waitForShutdownProcess(process, 5*time.Second); err != nil {
				t.Fatalf("beamers shutdown: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Errorf("idle shutdown took %s, want under 3s", elapsed)
			}
			logs := process.logs.String()
			for _, fragment := range []string{
				`"phase":"readiness"`,
				`"phase":"traffic_drain"`,
				`"phase":"in_flight"`,
				`"finalizer":"storage"`,
				`"phase":"shutdown"`,
			} {
				if !strings.Contains(logs, fragment) {
					t.Errorf("shutdown logs missing %s:\n%s", fragment, logs)
				}
			}
		})
	}
}

func TestExecutableSecondSignalForcesTermination(t *testing.T) {
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		select {
		case requested <- struct{}{}:
		default:
		}
		<-release
		response.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(release)
		collector.Close()
	}()

	bin := buildBeamers(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runBeamers(t, bin, "init", "--data-dir", dataDir)
	process := startShutdownProcess(t, bin,
		"--data-dir", dataDir,
		"--shutdown-timeout", "30s",
		"--telemetry-export-timeout", "30s",
		"--otlp-endpoint", collector.URL,
	)
	if err := process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send first signal: %v", err)
	}
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("telemetry finalizer did not start")
	}
	if err := process.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send second signal: %v", err)
	}
	err := waitForShutdownProcess(process, 3*time.Second)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("second signal exit error = %v, want signal exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Fatalf("second signal wait status = %v, want SIGINT", exitErr.Sys())
	}
}

func TestExecutableDrainsBusyDisplayWithinDeclaredBudgets(t *testing.T) {
	administrator, initial := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, initial)
	displayClient := enrollAndAssignDisplay(
		t,
		administrator,
		initial,
		"Shutdown Display",
		"event-overview",
	)
	bin, dataDir := initial.bin, initial.dataDir
	initial.stop(t)

	for _, test := range []struct {
		name        string
		signal      os.Signal
		budget      string
		minimumTime time.Duration
		maximumTime time.Duration
	}{
		{
			name: "INT ten seconds", signal: os.Interrupt, budget: "10s",
			maximumTime: 3 * time.Second,
		},
		{
			name: "TERM thirty seconds", signal: syscall.SIGTERM, budget: "30s",
			minimumTime: 9 * time.Second, maximumTime: 13 * time.Second,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startShutdownProcess(t, bin,
				"--data-dir", dataDir,
				"--shutdown-timeout", test.budget,
			)
			streamClient := &http.Client{Jar: displayClient.Jar}
			request, err := http.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"http://"+process.address+"/display/events",
				http.NoBody,
			)
			if err != nil {
				t.Fatalf("create Display stream request: %v", err)
			}
			response, err := streamClient.Do(request)
			if err != nil {
				t.Fatalf("open Display stream: %v", err)
			}
			defer func() {
				_ = response.Body.Close()
			}()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("Display stream status = %d, want 200", response.StatusCode)
			}
			reader := bufio.NewReader(response.Body)
			if heartbeat, readErr := reader.ReadString('\n'); readErr != nil ||
				heartbeat != ": heartbeat\n" {
				t.Fatalf("Display heartbeat = %q, %v", heartbeat, readErr)
			}
			closed := make(chan error, 1)
			go func() {
				_, copyErr := io.Copy(io.Discard, reader)
				closed <- errors.Join(copyErr, response.Body.Close())
			}()

			started := time.Now()
			if err = process.cmd.Process.Signal(test.signal); err != nil {
				t.Fatalf("signal beamers: %v", err)
			}
			waitForReadinessWithdrawal(t, process)
			if test.budget == "30s" {
				schedule := get(
					t,
					&http.Client{Timeout: time.Second},
					process.address,
					"/",
				)
				if closeErr := schedule.Body.Close(); closeErr != nil {
					t.Errorf("close Schedule response: %v", closeErr)
				}
				if schedule.StatusCode != http.StatusOK {
					t.Errorf("Schedule during steering = %d, want 200", schedule.StatusCode)
				}
				select {
				case err = <-closed:
					t.Fatalf("Display stream closed before steering window: %v", err)
				case <-time.After(250 * time.Millisecond):
				}
			}
			select {
			case err = <-closed:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("Display stream close: %v", err)
				}
			case <-time.After(test.maximumTime):
				t.Fatal("Display stream did not close within shutdown profile")
			}
			if err = waitForShutdownProcess(process, 2*time.Second); err != nil {
				t.Fatalf("beamers shutdown: %v", err)
			}
			elapsed := time.Since(started)
			if elapsed < test.minimumTime || elapsed > test.maximumTime {
				t.Errorf(
					"busy shutdown took %s, want %s through %s",
					elapsed,
					test.minimumTime,
					test.maximumTime,
				)
			}
			logs := process.logs.String()
			previous := -1
			for _, phase := range []string{
				`"phase":"readiness"`,
				`"phase":"traffic_drain"`,
				`"phase":"in_flight"`,
				`"phase":"finalize"`,
				`"phase":"shutdown"`,
			} {
				position := strings.Index(logs, phase)
				if position <= previous {
					t.Errorf("shutdown phase %s out of order:\n%s", phase, logs)
				}
				previous = position
			}
			wantStatus := `"phase":"traffic_drain","status":"skipped"`
			if test.budget == "30s" {
				wantStatus = `"phase":"traffic_drain","status":"timeout"`
			}
			if !strings.Contains(logs, wantStatus) {
				t.Errorf("shutdown logs missing %s:\n%s", wantStatus, logs)
			}
		})
	}

	restarted := startBeamers(t, bin, dataDir)
	assertGETResponse(
		t,
		&http.Client{Timeout: time.Second},
		restarted.address,
		"/readyz",
		http.StatusOK,
		"ready\n",
	)
	assertDisplayListContains(
		t,
		administrator,
		restarted.address,
		`"name":"Shutdown Display"`,
	)
	restarted.stop(t)
}

func waitForReadinessWithdrawal(t *testing.T, process *shutdownProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := requestProbe(t.Context(), process.address, "/readyz", 100*time.Millisecond)
		if result.err == nil && result.status == http.StatusServiceUnavailable {
			return
		}
		if strings.Contains(process.logs.String(), `"phase":"readiness"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("readiness was not withdrawn")
}

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

type shutdownProcess struct {
	address string
	cmd     *exec.Cmd
	done    chan error
	logs    *synchronizedBuffer
}

func startShutdownProcess(t *testing.T, bin string, args ...string) *shutdownProcess {
	t.Helper()
	command := exec.CommandContext(t.Context(), bin, append([]string{"serve"}, args...)...)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("capture beamers stderr: %v", err)
	}
	if err = command.Start(); err != nil {
		t.Fatalf("start beamers: %v", err)
	}
	process := &shutdownProcess{
		cmd:  command,
		done: make(chan error, 1),
		logs: &synchronizedBuffer{},
	}
	listening := make(chan string, 1)
	stderrDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := append(scanner.Bytes(), '\n')
			_, _ = process.logs.Write(line)
			var entry struct {
				Message string `json:"msg"`
				Address string `json:"address"`
			}
			if json.Unmarshal(line, &entry) == nil &&
				entry.Message == "server listening" {
				select {
				case listening <- entry.Address:
				default:
				}
			}
		}
		stderrDone <- scanner.Err()
	}()
	go func() {
		scanErr := <-stderrDone
		process.done <- errors.Join(command.Wait(), scanErr)
	}()
	t.Cleanup(func() {
		_ = process.cmd.Process.Kill()
	})
	select {
	case process.address = <-listening:
	case err = <-process.done:
		t.Fatalf("beamers exited before listening: %v\n%s", err, process.logs.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("beamers did not listen:\n%s", process.logs.String())
	}
	return process
}

func waitForShutdownProcess(process *shutdownProcess, timeout time.Duration) error {
	select {
	case err := <-process.done:
		return err
	case <-time.After(timeout):
		_ = process.cmd.Process.Kill()
		return errors.New("shutdown timed out")
	}
}

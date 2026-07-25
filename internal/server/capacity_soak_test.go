package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/types/known/durationpb"
	_ "modernc.org/sqlite"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/rundown"
)

const (
	publicPollingInterval   = 12 * time.Second
	publicFreshnessBound    = 15 * time.Second
	capacityStageDuration   = 2 * time.Hour
	capacityTimerSkewBound  = 250 * time.Millisecond
	capacityCommandInterval = 2 * time.Second
	capacityWarmup          = 5 * time.Second
)

type capacityProfile struct {
	Name      string
	Envelope  capacityEnvelope
	Certified bool
}

func capacityProfileNamed(name string) (capacityProfile, error) {
	profile := capacityProfile{Name: name, Certified: true}
	switch name {
	case "netuk":
		profile.Envelope = capacityEnvelope{
			Locations: 6, Lanes: 3, SessionsAndEntries: 75,
			Displays: 20, CrewConsoles: 10, PublicReaders: 500,
		}
	case "nova":
		profile.Envelope = capacityEnvelope{
			Locations: 4, Lanes: 2, SessionsAndEntries: 250,
			Displays: 15, CrewConsoles: 5, PublicReaders: 250,
		}
	case "fosdem":
		profile.Envelope = capacityEnvelope{
			Locations: 40, Lanes: 40, SessionsAndEntries: 1_500,
			Displays: 150, CrewConsoles: 75, PublicReaders: 10_000,
		}
	case "revision":
		profile.Envelope = capacityEnvelope{
			Locations: 4, Lanes: 4, SessionsAndEntries: 750,
			Displays: 30, CrewConsoles: 40, PublicReaders: 2_000,
		}
	case "rated":
		profile.Envelope = capacityEnvelope{
			Locations: 64, Lanes: 64, SessionsAndEntries: 5_000,
			Displays: 250, CrewConsoles: 100, PublicReaders: 10_000,
		}
	case "stress":
		profile.Certified = false
		profile.Envelope = capacityEnvelope{
			Locations: 64, Lanes: 64, SessionsAndEntries: 25_000,
			Displays: 500, CrewConsoles: 200, PublicReaders: 10_000,
		}
	default:
		return capacityProfile{}, fmt.Errorf("unknown capacity profile %q", name)
	}
	return profile, nil
}

func selectedCapacityProfile(t *testing.T) capacityProfile {
	t.Helper()
	name := os.Getenv("BEAMERS_CAPACITY_PROFILE")
	if name == "" {
		name = "stress"
	}
	profile, err := capacityProfileNamed(name)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

type capacityTarget struct {
	Origin             string           `json:"origin"`
	Profile            string           `json:"profile"`
	Envelope           capacityEnvelope `json:"envelope"`
	Hardware           capacityHardware `json:"hardware"`
	ServerHostname     string           `json:"server_hostname"`
	ProbeToken         string           `json:"probe_token"`
	SessionToken       string           `json:"session_token"`
	EventID            int              `json:"event_id"`
	LiveSessionID      int              `json:"live_session_id"`
	DisplayCredentials []string         `json:"display_credentials"`
}

func capacityCertificationError(profile capacityProfile, role string) error {
	if !profile.Certified {
		return fmt.Errorf("capacity profile %q is diagnostic only", profile.Name)
	}
	if role != "" && role != "combined" {
		return errors.New("capacity certification requires the combined hosted topology")
	}
	if os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" ||
		runtime.GOOS != "linux" ||
		runtime.GOARCH != "amd64" {
		return errors.New("capacity certification requires a GitHub-hosted Linux AMD64 runner")
	}
	return nil
}

func TestCapacityProfileRejectsUnknown(t *testing.T) {
	if _, err := capacityProfileNamed("unknown"); err == nil {
		t.Fatal("unknown capacity profile accepted")
	}
}

func TestCapacityLoadArrivalsRemainOrderedWithJitter(t *testing.T) {
	started := time.Unix(0, 0)
	previous := started.Add(-time.Second)
	for arrival := range 200 {
		scheduled := capacityLoadArrival(
			started,
			arrival%100,
			arrival/100,
			100,
			2*time.Second,
		)
		if !scheduled.After(previous) {
			t.Fatalf("arrival %d at %s follows %s", arrival, scheduled, previous)
		}
		previous = scheduled
	}
}

func TestCapacityFreshnessStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	target := make(chan capacityFreshnessResult)
	done := make(chan struct{})
	go func() {
		observeCapacityFreshness(ctx, &capacityScheduleCache{}, "", time.Now(), target)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("freshness observer did not stop")
	}
}

func TestCapacityFanoutSummarizesEachCommandFirst(t *testing.T) {
	found := summarizeCapacityFanout([][]time.Duration{
		{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
		{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond},
	})
	if len(found.PerCommand) != 2 ||
		found.PerCommand[0].P50MS != 20 ||
		found.PerCommand[1].P95MS != 300 ||
		found.P50.P50MS != 20 ||
		found.P95.P95MS != 300 ||
		found.Maximum.MaxMS != 300 {
		t.Fatalf("fanout summary = %+v", found)
	}
}

func TestCapacityCertificationAcceptsHostedCombinedTopology(t *testing.T) {
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	profile, err := capacityProfileNamed("rated")
	if err != nil {
		t.Fatal(err)
	}
	if err = capacityCertificationError(profile, "combined"); err != nil {
		t.Fatalf("GitHub-hosted combined certification rejected: %v", err)
	}
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	if err = capacityCertificationError(profile, "combined"); err == nil {
		t.Fatal("self-hosted certification accepted")
	}
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	if err = capacityCertificationError(profile, "generator"); err == nil {
		t.Fatal("remote certification accepted")
	}
	profile, err = capacityProfileNamed("stress")
	if err != nil {
		t.Fatal(err)
	}
	if err = capacityCertificationError(profile, "combined"); err == nil {
		t.Fatal("stress-profile certification accepted")
	}
}

func TestCapacityEvidenceRejectsIncompleteProvenanceAndUnboundedStress(t *testing.T) {
	hardware := capacityHardware{
		GOOS: "linux", GOARCH: "amd64", CPUs: 4, CPUModel: "test",
		MemoryBytes: 8 << 30, StorageSource: "/dev/root", Filesystem: "ext4",
		NonRotational: true,
	}
	runner := capacityRunner{
		Environment: "github-hosted", Name: "runner", OS: "Linux", Arch: "X64",
		GitHubRunID: "1", GitHubSHA: strings.Repeat("a", 40),
	}
	if err := capacityProvenanceError(hardware, runner); err != nil {
		t.Fatalf("complete provenance rejected: %v", err)
	}
	hardware.CPUModel = ""
	if err := capacityProvenanceError(hardware, runner); err == nil {
		t.Fatal("missing CPU model accepted")
	}
	hardware.CPUModel = "test"
	envelope := capacityEnvelope{
		Displays: 500, CrewConsoles: 200, PublicReaders: 10_000,
	}
	resources := capacityResources{
		MaxProcessBytes: 1 << 30, MaxGoroutines: 11_000, MaxOpenFiles: 1_000,
	}
	if err := capacityResourceBoundError(hardware, envelope, resources); err != nil {
		t.Fatalf("bounded stress resources rejected: %v", err)
	}
	resources.MaxProcessBytes = 7 << 30
	if err := capacityResourceBoundError(hardware, envelope, resources); err == nil {
		t.Fatal("unbounded stress memory accepted")
	}
}

func TestCapacityEnvelope(t *testing.T) {
	if os.Getenv("BEAMERS_CAPACITY_SOAK") != "1" {
		t.Skip("set BEAMERS_CAPACITY_SOAK=1 to run the reference capacity soak")
	}
	profile := selectedCapacityProfile(t)
	certify := os.Getenv("BEAMERS_CAPACITY_CERTIFY") == "1"
	role := os.Getenv("BEAMERS_CAPACITY_ROLE")
	if certify {
		if err := capacityCertificationError(profile, role); err != nil {
			t.Fatal(err)
		}
	}
	duration := capacityDuration(t, certify)
	switch role {
	case "", "combined":
		runCombinedCapacity(t, profile, duration, certify)
	case "server":
		serveCapacityTarget(t, profile, duration)
	case "generator":
		runRemoteCapacity(t, profile, duration, certify)
	default:
		t.Fatalf("unknown BEAMERS_CAPACITY_ROLE %q", role)
	}
}

func runCombinedCapacity(
	t *testing.T,
	profile capacityProfile,
	duration time.Duration,
	certify bool,
) {
	t.Helper()
	fixture := prepareCapacityFixture(t, profile.Envelope)
	application, displayStream := newCapacityApplication(t, fixture)
	probe := newCapacityProbe(t, application)
	origin := httptest.NewServer(probe)
	t.Cleanup(origin.Close)
	target := capacityTargetForFixture(
		t,
		origin.URL,
		profile,
		fixture,
		probe.token,
	)
	runCapacityLoad(t, target, duration, certify)
	if profile.Envelope.SessionsAndEntries >= testedSessionsEntries {
		verifyCapacityWarning(
			t,
			application,
			fixture,
			displayStream,
			profile.Envelope,
		)
	}
}

func newCapacityApplication(
	t *testing.T,
	fixture capacityFixture,
) (*application, *displaystream.Hub) {
	t.Helper()
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		t.Fatalf("create Program Output stream: %v", err)
	}
	application, err := newApplication(t.Context(), applicationConfig{
		Config: Config{
			DataDir: fixture.dataDir, AttachmentsDir: filepath.Join(fixture.dataDir, "attachments"),
			BuildVersion: "capacity", Logger: slog.New(slog.DiscardHandler),
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
		t.Fatalf("build capacity application: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := application.Close(); closeErr != nil {
			t.Errorf("close capacity application: %v", closeErr)
		}
	})
	return application, displayStream
}

func runCapacityLoad(
	t *testing.T,
	target capacityTarget,
	duration time.Duration,
	certify bool,
) {
	t.Helper()
	profile, err := capacityProfileNamed(target.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if target.Envelope != profile.Envelope {
		t.Fatalf(
			"capacity target envelope = %+v, want profile %+v",
			target.Envelope,
			profile.Envelope,
		)
	}
	envelope := target.Envelope
	transport := &http.Transport{
		MaxIdleConns:        envelope.Displays + envelope.CrewConsoles + 100,
		MaxIdleConnsPerHost: envelope.Displays + envelope.CrewConsoles + 100,
		MaxConnsPerHost:     envelope.Displays + envelope.CrewConsoles + 100,
		IdleConnTimeout:     30 * time.Second,
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	cache := newCapacityScheduleCache(target.Origin, client)
	cacheServer := httptest.NewServer(cache)
	t.Cleanup(cacheServer.Close)
	primeCapacitySchedule(t, client, cacheServer.URL)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	applied := make(chan capacityDisplayApplied, envelope.Displays)
	backgroundErr := make(chan error, 1)
	displayReady := make(chan struct{}, envelope.Displays)
	var displayRequests atomic.Int64
	startCapacityDisplays(
		ctx,
		client,
		target.Origin,
		target.DisplayCredentials,
		target.LiveSessionID,
		applied,
		displayReady,
		backgroundErr,
		&displayRequests,
	)
	waitForCapacityDisplays(t, displayReady, envelope.Displays, backgroundErr)

	authenticatedClient := &http.Client{
		Transport: capacityCookieTransport{
			base: transport,
			cookie: &http.Cookie{
				Name: sessionCookieName, Value: target.SessionToken,
			},
		},
		Timeout: 30 * time.Second,
	}
	var crewRequests atomic.Int64
	var publicRequests atomic.Int64
	publicReady := make(chan struct{})
	var loadGroup sync.WaitGroup
	loadGroup.Add(2)
	go func() {
		defer loadGroup.Done()
		runCapacityCrewLoad(
			ctx,
			authenticatedClient,
			target.Origin,
			target.EventID,
			envelope.CrewConsoles,
			backgroundErr,
			&crewRequests,
		)
	}()
	go func() {
		defer loadGroup.Done()
		runCapacityPublicLoad(
			ctx,
			client,
			cacheServer.URL,
			envelope.PublicReaders,
			backgroundErr,
			&publicRequests,
			publicReady,
		)
	}()
	select {
	case <-publicReady:
	case backgroundError := <-backgroundErr:
		t.Fatalf("start capacity public readers: %v", backgroundError)
	case <-time.After(publicPollingInterval + 30*time.Second):
		t.Fatal("capacity public readers did not become ready")
	}

	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		authenticatedClient,
		target.Origin,
	)
	commandInterval := capacityCommandInterval
	fanoutTimeout := 3 * time.Second
	if !profile.Certified {
		commandInterval = 20 * time.Second
		fanoutTimeout = 30 * time.Second
	}
	metrics := runCapacityCommands(
		ctx,
		t,
		sessionClient,
		cache,
		capacityFixture{
			eventID:       target.EventID,
			liveSessionID: target.LiveSessionID,
		},
		envelope.Displays,
		duration,
		commandInterval,
		fanoutTimeout,
		applied,
		backgroundErr,
	)
	cancel()
	loadGroup.Wait()
	select {
	case backgroundError := <-backgroundErr:
		t.Fatalf("capacity background load: %v", backgroundError)
	default:
	}

	generatorHostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("read generator hostname: %v", err)
	}
	resources := readCapacityResources(t, client, target)
	runner := inspectCapacityRunner()
	report := capacityReport{
		Profile:  target.Profile,
		Hardware: target.Hardware,
		Runner:   runner,
		Duration: duration.String(),
		Envelope: envelope,
		Topology: capacityTopology{
			ServerHostname:    target.ServerHostname,
			GeneratorHostname: generatorHostname,
			SeparateMachines:  target.ServerHostname != generatorHostname,
		},
		Resources:               resources,
		LiveCommand:             summarizeCapacityLatency(metrics.liveCommands),
		DisplayCommit:           summarizeCapacityFanout(metrics.displayCommit),
		DisplayOperator:         summarizeCapacityFanout(metrics.displayOperator),
		StageTimerMaximumSkewMS: durationMilliseconds(metrics.maximumTimerSkew),
		PublicFreshness:         summarizeCapacityLatency(metrics.publicFreshness),
		CrewRequests:            crewRequests.Load(),
		DisplayRequests:         displayRequests.Load(),
		PublicRequests:          publicRequests.Load(),
	}
	writeCapacityReport(t, report)
	if certify {
		requireCapacityProvenance(t, target.Hardware, runner)
		report.Topology.Certified = verifyCapacityThresholds(t, metrics)
		writeCapacityReport(t, report)
	} else if profile.Name == "stress" {
		verifyCapacityResourceBounds(t, target.Hardware, envelope, resources)
	}
	t.Logf("capacity report: %+v", report)
}

func capacityTargetForFixture(
	t *testing.T,
	origin string,
	profile capacityProfile,
	fixture capacityFixture,
	probeToken string,
) capacityTarget {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("read server hostname: %v", err)
	}
	return capacityTarget{
		Origin:             origin,
		Profile:            profile.Name,
		Envelope:           profile.Envelope,
		Hardware:           inspectCapacityHardware(t, fixture.dataDir),
		ServerHostname:     hostname,
		ProbeToken:         probeToken,
		SessionToken:       fixture.sessionToken,
		EventID:            fixture.eventID,
		LiveSessionID:      fixture.liveSessionID,
		DisplayCredentials: fixture.displayCredentials,
	}
}

func serveCapacityTarget(
	t *testing.T,
	profile capacityProfile,
	duration time.Duration,
) {
	t.Helper()
	fixture := prepareCapacityFixture(t, profile.Envelope)
	application, _ := newCapacityApplication(t, fixture)
	probe := newCapacityProbe(t, application)
	address := os.Getenv("BEAMERS_CAPACITY_LISTEN")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", address)
	if err != nil {
		t.Fatalf("listen for capacity generator: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	origin := os.Getenv("BEAMERS_CAPACITY_ORIGIN")
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	target := capacityTargetForFixture(t, origin, profile, fixture, probe.token)
	targetPath := os.Getenv("BEAMERS_CAPACITY_TARGET")
	if targetPath == "" {
		t.Fatal("BEAMERS_CAPACITY_TARGET is required for the capacity server")
	}
	writeCapacityTarget(t, targetPath, target)

	server := &http.Server{
		Handler:           probe,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	t.Logf("capacity server ready; copy %s securely to the generator", targetPath)
	select {
	case <-probe.done:
	case err = <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve capacity target: %v", err)
		}
	case <-time.After(duration + 5*time.Minute):
		t.Fatal("capacity generator did not finish before the server deadline")
	}
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shut down capacity server: %v", err)
	}
}

func runRemoteCapacity(
	t *testing.T,
	profile capacityProfile,
	duration time.Duration,
	certify bool,
) {
	t.Helper()
	targetPath := os.Getenv("BEAMERS_CAPACITY_TARGET")
	if targetPath == "" {
		t.Fatal("BEAMERS_CAPACITY_TARGET is required for the capacity generator")
	}
	target := readCapacityTarget(t, targetPath)
	if err := validateCapacityTarget(target); err != nil {
		t.Fatal(err)
	}
	if target.Profile != profile.Name {
		t.Fatalf(
			"capacity target profile = %q, generator profile = %q",
			target.Profile,
			profile.Name,
		)
	}
	t.Cleanup(func() {
		finishCapacityTarget(t, target)
	})
	runCapacityLoad(t, target, duration, certify)
}

func validateCapacityTarget(target capacityTarget) error {
	profile, err := capacityProfileNamed(target.Profile)
	if err != nil {
		return err
	}
	if target.Envelope != profile.Envelope ||
		target.ProbeToken == "" ||
		target.SessionToken == "" ||
		target.EventID <= 0 ||
		target.LiveSessionID <= 0 ||
		len(target.DisplayCredentials) != target.Envelope.Displays {
		return errors.New("capacity target is incomplete or does not match its profile")
	}
	origin, err := url.Parse(target.Origin)
	if err != nil || origin.Hostname() == "" {
		return errors.New("capacity target has an invalid origin")
	}
	if origin.Scheme == "https" {
		return nil
	}
	address := net.ParseIP(origin.Hostname())
	if origin.Scheme != "http" ||
		(origin.Hostname() != "localhost" && (address == nil || !address.IsLoopback())) {
		return errors.New("capacity target must use HTTPS or a loopback HTTP tunnel")
	}
	return nil
}

func writeCapacityTarget(t *testing.T, path string, target capacityTarget) {
	t.Helper()
	encoded, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		t.Fatalf("encode capacity target: %v", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create capacity target directory: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create capacity target without overwrite: %v", err)
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		t.Fatalf("write capacity target: %v", err)
	}
}

func readCapacityTarget(t *testing.T, path string) capacityTarget {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("inspect capacity target: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatal("capacity target must not be readable by group or other users")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capacity target: %v", err)
	}
	var target capacityTarget
	if err = json.Unmarshal(content, &target); err != nil {
		t.Fatalf("decode capacity target: %v", err)
	}
	return target
}

const capacityProbeHeader = "X-Beamers-Capacity-Probe"

type capacityProbe struct {
	next      http.Handler
	token     string
	resources *capacityResourceSampler
	done      chan struct{}
	finish    sync.Once
}

func newCapacityProbe(t *testing.T, next http.Handler) *capacityProbe {
	t.Helper()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatalf("create capacity probe token: %v", err)
	}
	return &capacityProbe{
		next:      next,
		token:     hex.EncodeToString(token[:]),
		resources: newCapacityResourceSampler(t.Context()),
		done:      make(chan struct{}),
	}
}

func (probe *capacityProbe) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/capacity-probe/resources":
		if request.Method != http.MethodGet ||
			request.Header.Get(capacityProbeHeader) != probe.token {
			http.NotFound(response, request)
			return
		}
		resources, err := probe.resources.current()
		if err != nil {
			http.Error(response, "capacity resource evidence unavailable", http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		if err = json.NewEncoder(response).Encode(resources); err != nil {
			return
		}
	case "/capacity-probe/finish":
		if request.Method != http.MethodPost ||
			request.Header.Get(capacityProbeHeader) != probe.token {
			http.NotFound(response, request)
			return
		}
		probe.finish.Do(func() {
			close(probe.done)
		})
		response.WriteHeader(http.StatusNoContent)
	default:
		probe.next.ServeHTTP(response, request)
	}
}

type capacityResources struct {
	MaxHeapAllocBytes int64 `json:"max_heap_alloc_bytes"`
	MaxProcessBytes   int64 `json:"max_process_bytes"`
	MaxGoroutines     int   `json:"max_goroutines"`
	MaxOpenFiles      int   `json:"max_open_files"`
}

type capacityResourceSampler struct {
	mu           sync.Mutex
	currentValue capacityResources
	err          error
}

func newCapacityResourceSampler(ctx context.Context) *capacityResourceSampler {
	sampler := &capacityResourceSampler{}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := sampler.sample(); err != nil {
				sampler.fail(err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return sampler
}

func (sampler *capacityResourceSampler) sample() error {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return fmt.Errorf("read process resource use: %w", err)
	}
	openFiles, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("count process file descriptors: %w", err)
	}
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	sampler.currentValue.MaxHeapAllocBytes = max(
		sampler.currentValue.MaxHeapAllocBytes,
		int64(memory.Alloc),
	)
	sampler.currentValue.MaxProcessBytes = max(
		sampler.currentValue.MaxProcessBytes,
		usage.Maxrss*1024,
	)
	sampler.currentValue.MaxGoroutines = max(
		sampler.currentValue.MaxGoroutines,
		runtime.NumGoroutine(),
	)
	sampler.currentValue.MaxOpenFiles = max(
		sampler.currentValue.MaxOpenFiles,
		len(openFiles),
	)
	return nil
}

func (sampler *capacityResourceSampler) fail(err error) {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	if sampler.err == nil {
		sampler.err = err
	}
}

func (sampler *capacityResourceSampler) current() (capacityResources, error) {
	if err := sampler.sample(); err != nil {
		sampler.fail(err)
	}
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	return sampler.currentValue, sampler.err
}

func readCapacityResources(
	t *testing.T,
	client *http.Client,
	target capacityTarget,
) capacityResources {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		target.Origin+"/capacity-probe/resources",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create capacity resource request: %v", err)
	}
	request.Header.Set(capacityProbeHeader, target.ProbeToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("read capacity resources: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		t.Fatalf("capacity resources status = %d", response.StatusCode)
	}
	var resources capacityResources
	decodeErr := json.NewDecoder(response.Body).Decode(&resources)
	closeErr := response.Body.Close()
	if err = errors.Join(decodeErr, closeErr); err != nil {
		t.Fatalf("decode capacity resources: %v", err)
	}
	if resources.MaxHeapAllocBytes == 0 ||
		resources.MaxProcessBytes == 0 ||
		resources.MaxGoroutines == 0 {
		t.Fatalf("capacity resource evidence = %+v", resources)
	}
	return resources
}

func finishCapacityTarget(t *testing.T, target capacityTarget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.Origin+"/capacity-probe/finish",
		http.NoBody,
	)
	if err != nil {
		t.Errorf("create capacity finish request: %v", err)
		return
	}
	request.Header.Set(capacityProbeHeader, target.ProbeToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Errorf("finish capacity target: %v", err)
		return
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Errorf("close capacity finish response: %v", closeErr)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("capacity finish status = %d", response.StatusCode)
	}
}

func BenchmarkCapacityDisplaySnapshotFanout(b *testing.B) {
	profile, err := capacityProfileNamed("rated")
	if err != nil {
		b.Fatal(err)
	}
	envelope := profile.Envelope
	fixture := prepareCapacityFixture(b, envelope)
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		b.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		b.Fatalf("create Program Output stream: %v", err)
	}
	application, err := newApplication(b.Context(), applicationConfig{
		Config: Config{
			DataDir: fixture.dataDir, AttachmentsDir: filepath.Join(fixture.dataDir, "attachments"),
			BuildVersion: "benchmark", Logger: slog.New(slog.DiscardHandler),
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
		b.Fatalf("build capacity application: %v", err)
	}
	b.Cleanup(func() {
		if closeErr := application.Close(); closeErr != nil {
			b.Errorf("close capacity application: %v", closeErr)
		}
	})
	origin := httptest.NewServer(application)
	b.Cleanup(origin.Close)
	transport := &http.Transport{
		MaxIdleConns:        envelope.Displays,
		MaxIdleConnsPerHost: envelope.Displays,
		MaxConnsPerHost:     envelope.Displays,
	}
	b.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	for _, benchmark := range []struct {
		name  string
		limit int
	}{
		{name: "mode=serialized", limit: 1},
		{name: "mode=pooled", limit: envelope.Displays},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			for b.Loop() {
				semaphore := make(chan struct{}, benchmark.limit)
				var failed atomic.Bool
				var requests sync.WaitGroup
				for _, credential := range fixture.displayCredentials {
					requests.Go(func() {
						semaphore <- struct{}{}
						_, _, snapshotErr := fetchCapacitySnapshot(
							b.Context(),
							client,
							origin.URL,
							credential,
						)
						<-semaphore
						if snapshotErr != nil {
							failed.Store(true)
						}
					})
				}
				requests.Wait()
				if failed.Load() {
					b.Fatal("fetch capacity Display Snapshot")
				}
			}
			b.ReportMetric(float64(envelope.Displays), "snapshots/op")
		})
	}
}

type capacityFixture struct {
	dataDir            string
	installation       *operations.Installation
	actor              auth.Account
	sessionToken       string
	eventID            int
	liveSessionID      int
	competitionID      int
	displayCredentials []string
}

func prepareCapacityFixture(tb testing.TB, envelope capacityEnvelope) capacityFixture {
	tb.Helper()
	t := tb
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize capacity installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open capacity installation: %v", err)
	}
	bootstrap, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue capacity bootstrap: %v", err)
	}
	const password = "correct horse battery staple"
	session, err := installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Capacity Administrator",
		password,
	)
	if err != nil {
		t.Fatalf("bootstrap capacity Administrator: %v", err)
	}
	now := time.Now().UTC()
	event, err := installation.Events().Create(t.Context(), session.Account, events.CreateInput{
		Name: "Capacity Event", PlannedStartDate: now.Format(time.DateOnly),
		PlannedEndDate: now.AddDate(0, 0, 1).Format(time.DateOnly),
		Timezone:       "UTC", EventLocale: "en-US", ContentLanguage: "en",
		EventDayBoundary: "06:00", EntryDefaultDisposition: "Included",
		CommandID: "capacity-create-event",
	})
	if err != nil {
		t.Fatalf("create capacity Event: %v", err)
	}
	if _, err = installation.Events().GrantEventAccess(
		t.Context(),
		session.Account,
		event.ID,
		session.Account.ID,
		"Producer",
		"capacity-grant-producer",
	); err != nil {
		t.Fatalf("grant capacity Producer: %v", err)
	}
	actor, err := installation.Authentication().Authenticate(
		t.Context(),
		session.Token,
	)
	if err != nil {
		t.Fatalf("refresh capacity authorization: %v", err)
	}
	session.Account = actor
	edit := capacityDraft(event.ID, now, envelope)
	if _, err = installation.RundownCommands().EditDraft(
		t.Context(),
		session.Account,
		edit,
	); err != nil {
		t.Fatalf("stage capacity Rundown: %v", err)
	}
	preview, err := installation.RundownQueries().PublishPreview(
		t.Context(),
		session.Account,
		rundown.PublishPreviewInput{EventID: event.ID},
	)
	if err != nil {
		t.Fatalf("preview capacity Rundown: %v", err)
	}
	if len(preview.ValidationFailures) != 0 {
		t.Fatalf("capacity Publish validation = %v", preview.ValidationFailures)
	}
	if _, err = installation.RundownCommands().Publish(
		t.Context(),
		session.Account,
		rundown.PublishInput{
			EventID:   event.ID,
			CommandID: "capacity-publish-rundown",
			Confirmation: rundown.PublishConfirmation{
				DraftRevision:     preview.DraftRevision,
				PublishedRevision: preview.PublishedRevision,
				ChangeIDs:         preview.ChangeIDs,
				Fingerprint:       preview.Fingerprint,
			},
		},
	); err != nil {
		t.Fatalf("publish capacity Rundown: %v", err)
	}
	published, err := installation.RundownQueries().CrewRundown(
		t.Context(),
		session.Account,
		event.ID,
	)
	if err != nil {
		t.Fatalf("load capacity Rundown: %v", err)
	}
	var liveSessionID, competitionID, locationID int
	for _, location := range published.Locations {
		if location.Name == "Location 01" {
			locationID = location.ID
		}
	}
	for _, item := range published.Sessions {
		switch item.Title {
		case "Capacity Stage":
			liveSessionID = item.ID
		case "Capacity Competition":
			competitionID = item.ID
		}
	}
	if liveSessionID == 0 || competitionID == 0 || locationID == 0 {
		t.Fatalf(
			"capacity identities = live %d competition %d location %d",
			liveSessionID,
			competitionID,
			locationID,
		)
	}
	if err = installation.Close(); err != nil {
		t.Fatalf("close capacity fixture before bulk load: %v", err)
	}
	credentials := bulkLoadCapacityRecords(
		t,
		filepath.Join(dataDir, "beamers.db"),
		event.ID,
		competitionID,
		locationID,
		envelope,
	)
	installation, err = operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("reopen capacity installation: %v", err)
	}
	actor, err = installation.Authentication().Authenticate(
		t.Context(),
		session.Token,
	)
	if err != nil {
		t.Fatalf("authenticate capacity session: %v", err)
	}
	preflight, err := installation.Activation().Preflight(
		t.Context(),
		actor,
		event.ID,
	)
	if err != nil {
		t.Fatalf("preflight capacity Event: %v", err)
	}
	if len(preflight.Blockers) != 0 {
		t.Fatalf("capacity Activation blockers = %+v", preflight.Blockers)
	}
	if _, err = installation.Activation().Activate(
		t.Context(),
		actor,
		activation.ActivateInput{
			EventID:      event.ID,
			CommandID:    "capacity-activate-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate capacity Event: %v", err)
	}
	counts, err := installation.Capacity(t.Context())
	if err != nil {
		t.Fatalf("count capacity fixture: %v", err)
	}
	if counts.Locations != envelope.Locations ||
		counts.Lanes != envelope.Lanes ||
		counts.Displays != envelope.Displays ||
		counts.Sessions+counts.Entries != envelope.SessionsAndEntries {
		t.Fatalf("capacity fixture counts = %+v", counts)
	}
	return capacityFixture{
		dataDir:            dataDir,
		installation:       installation,
		actor:              actor,
		sessionToken:       session.Token,
		eventID:            event.ID,
		liveSessionID:      liveSessionID,
		competitionID:      competitionID,
		displayCredentials: credentials,
	}
}

func capacityDraft(
	eventID int,
	now time.Time,
	envelope capacityEnvelope,
) rundown.EditDraftInput {
	locations := make([]rundown.LocationDraftInput, 0, envelope.Locations)
	lanes := make([]rundown.LaneDraftInput, 0, envelope.Lanes)
	for index := 1; index <= envelope.Locations; index++ {
		locationRef := fmt.Sprintf("location-%02d", index)
		locations = append(locations, rundown.LocationDraftInput{
			Ref:  locationRef,
			Name: fmt.Sprintf("Location %02d", index),
		})
	}
	for index := 1; index <= envelope.Lanes; index++ {
		locationRef := fmt.Sprintf("location-%02d", (index-1)%envelope.Locations+1)
		lanes = append(lanes, rundown.LaneDraftInput{
			Ref:      fmt.Sprintf("lane-%02d", index),
			Name:     fmt.Sprintf("Lane %02d", index),
			Location: rundown.TargetRef{Ref: locationRef},
		})
	}
	start := now.Add(-time.Minute)
	return rundown.EditDraftInput{
		EventID:   eventID,
		CommandID: "capacity-stage-rundown",
		Locations: locations,
		Lanes:     lanes,
		Sessions: []rundown.SessionDraftInput{
			{
				Ref:                "capacity-stage",
				Title:              "Capacity Stage",
				Type:               rundown.SessionPresentation,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start,
				PlannedEnd:         start.Add(capacityStageDuration),
				TimingPolicy:       rundown.TimingFixedDuration,
				MinimumDuration:    time.Minute,
				StartBoundary:      rundown.BoundarySoft,
				EndBoundary:        rundown.BoundarySoft,
				Lanes:              []rundown.TargetRef{{Ref: "lane-01"}},
				Locations:          []rundown.TargetRef{{Ref: "location-01"}},
			},
			{
				Ref:                "capacity-competition",
				Title:              "Capacity Competition",
				Type:               rundown.SessionCompetition,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start.Add(3 * time.Hour),
				PlannedEnd:         start.Add(5 * time.Hour),
				TimingPolicy:       rundown.TimingFixedDuration,
				MinimumDuration:    time.Minute,
				StartBoundary:      rundown.BoundarySoft,
				EndBoundary:        rundown.BoundarySoft,
				SubmissionDeadline: now.Add(time.Hour),
				EntryDefault:       rundown.EntryIncluded,
				Lanes:              []rundown.TargetRef{{Ref: "lane-02"}},
				Locations:          []rundown.TargetRef{{Ref: "location-02"}},
			},
		},
	}
}

func bulkLoadCapacityRecords(
	tb testing.TB,
	databasePath string,
	eventID int,
	competitionID int,
	locationID int,
	envelope capacityEnvelope,
) []string {
	tb.Helper()
	t := tb
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open capacity database: %v", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close capacity database: %v", closeErr)
		}
	}()
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin capacity bulk load: %v", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	entryStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO competition_entries
			(name, public_details, disposition, created_at, event_id, competition_session_id)
		 VALUES (?, ?, 'Included', ?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare capacity Entries: %v", err)
	}
	defer func() {
		if closeErr := entryStatement.Close(); closeErr != nil {
			t.Errorf("close capacity Entry statement: %v", closeErr)
		}
	}()
	for index := 1; index <= envelope.SessionsAndEntries-2; index++ {
		if _, err = entryStatement.ExecContext(
			t.Context(),
			fmt.Sprintf("Entry %05d", index),
			"Capacity fixture",
			time.Unix(int64(index), 0).UTC(),
			eventID,
			competitionID,
		); err != nil {
			t.Fatalf("insert capacity Entry %d: %v", index, err)
		}
	}
	displayStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO displays (name, created_at, enrolled_at) VALUES (?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare capacity Displays: %v", err)
	}
	defer func() {
		if closeErr := displayStatement.Close(); closeErr != nil {
			t.Errorf("close capacity Display statement: %v", closeErr)
		}
	}()
	credentialStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO display_credentials (token_hash, created_at, display_id)
		 VALUES (?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare capacity Display credentials: %v", err)
	}
	defer func() {
		if closeErr := credentialStatement.Close(); closeErr != nil {
			t.Errorf("close capacity Display credential statement: %v", closeErr)
		}
	}()
	assignmentStatement, err := transaction.PrepareContext(
		t.Context(),
		`INSERT INTO display_assignments
			(view_key, display_group_keys, created_at, updated_at, display_id, event_id, location_id)
		 VALUES (?, '[]', ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare capacity Display Assignments: %v", err)
	}
	defer func() {
		if closeErr := assignmentStatement.Close(); closeErr != nil {
			t.Errorf("close capacity Display Assignment statement: %v", closeErr)
		}
	}()
	credentials := make([]string, 0, envelope.Displays)
	for index := 1; index <= envelope.Displays; index++ {
		now := time.Unix(int64(index), 0).UTC()
		result, execErr := displayStatement.ExecContext(
			t.Context(),
			fmt.Sprintf("Display %03d", index),
			now,
			now,
		)
		if execErr != nil {
			t.Fatalf("insert capacity Display %d: %v", index, execErr)
		}
		displayID, execErr := result.LastInsertId()
		if execErr != nil {
			t.Fatalf("read capacity Display %d identity: %v", index, execErr)
		}
		tokenBytes := sha256.Sum256(fmt.Appendf(nil, "capacity-display-%d", index))
		token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
		tokenHash := sha256.Sum256([]byte(token))
		if _, execErr = credentialStatement.ExecContext(
			t.Context(),
			hex.EncodeToString(tokenHash[:]),
			now,
			displayID,
		); execErr != nil {
			t.Fatalf("insert capacity Display credential %d: %v", index, execErr)
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
			t.Fatalf("insert capacity Display Assignment %d: %v", index, execErr)
		}
		credentials = append(credentials, token)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit capacity bulk load: %v", err)
	}
	return credentials
}

type capacityCookieTransport struct {
	base   http.RoundTripper
	cookie *http.Cookie
}

func (transport capacityCookieTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.AddCookie(transport.cookie)
	return transport.base.RoundTrip(cloned)
}

type capacityDisplayApplied struct {
	at               time.Time
	clockOffset      time.Duration
	clockUncertainty time.Duration
	timerAnchor      time.Time
	liveRevision     int64
}

func startCapacityDisplays(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	credentials []string,
	liveSessionID int,
	applied chan<- capacityDisplayApplied,
	ready chan<- struct{},
	backgroundErr chan<- error,
	requests *atomic.Int64,
) {
	for _, credential := range credentials {
		go runCapacityDisplay(
			ctx,
			client,
			baseURL,
			credential,
			liveSessionID,
			applied,
			ready,
			backgroundErr,
			requests,
		)
	}
}

func runCapacityDisplay(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	credential string,
	liveSessionID int,
	applied chan<- capacityDisplayApplied,
	ready chan<- struct{},
	backgroundErr chan<- error,
	requests *atomic.Int64,
) {
	snapshot, clock, err := fetchCapacitySnapshot(ctx, client, baseURL, credential)
	if err != nil {
		sendCapacityError(ctx, backgroundErr, err)
		return
	}
	for range 2 {
		if clock.uncertainty <= capacityTimerSkewBound {
			break
		}
		var candidate capacityClock
		snapshot, candidate, err = fetchCapacitySnapshot(ctx, client, baseURL, credential)
		if err != nil {
			sendCapacityError(ctx, backgroundErr, err)
			return
		}
		clock = clock.selectSample(candidate)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		baseURL+"/display/events?stream_id="+snapshot.StreamID+
			"&after="+snapshot.StreamPosition,
		http.NoBody,
	)
	if err != nil {
		sendCapacityError(ctx, backgroundErr, err)
		return
	}
	request.AddCookie(&http.Cookie{Name: displayCookieName, Value: credential})
	streamClient := &http.Client{Transport: client.Transport}
	response, err := streamClient.Do(request)
	if err != nil {
		sendCapacityError(ctx, backgroundErr, fmt.Errorf("open Display stream: %w", err))
		return
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && ctx.Err() == nil {
			sendCapacityError(
				ctx,
				backgroundErr,
				fmt.Errorf("close Display stream: %w", closeErr),
			)
		}
	}()
	if response.StatusCode != http.StatusOK {
		sendCapacityError(
			ctx,
			backgroundErr,
			fmt.Errorf("Display stream status = %d", response.StatusCode),
		)
		return
	}
	select {
	case ready <- struct{}{}:
	case <-ctx.Done():
		return
	}
	reader := bufio.NewReader(response.Body)
	for {
		if err = readCapacityInvalidation(reader); err != nil {
			if ctx.Err() == nil {
				sendCapacityError(ctx, backgroundErr, err)
			}
			return
		}
		snapshot, candidateClock, snapshotErr := fetchCapacitySnapshot(
			ctx,
			client,
			baseURL,
			credential,
		)
		if snapshotErr != nil {
			sendCapacityError(ctx, backgroundErr, snapshotErr)
			return
		}
		clock = clock.selectSample(candidateClock)
		if snapshot.StageTimer == nil {
			sendCapacityError(
				ctx,
				backgroundErr,
				errors.New("capacity Display has no Stage Timer after live command"),
			)
			return
		}
		timerAnchor, parseErr := time.Parse(time.RFC3339Nano, snapshot.StageTimer.Anchor)
		if parseErr != nil {
			sendCapacityError(ctx, backgroundErr, fmt.Errorf("parse Stage Timer anchor: %w", parseErr))
			return
		}
		liveRevision, parseErr := capacityLiveRevision(snapshot, liveSessionID)
		if parseErr != nil {
			sendCapacityError(ctx, backgroundErr, parseErr)
			return
		}
		requests.Add(1)
		select {
		case applied <- capacityDisplayApplied{
			at:               time.Now(),
			clockOffset:      clock.offset(),
			clockUncertainty: clock.uncertainty,
			timerAnchor:      timerAnchor,
			liveRevision:     liveRevision,
		}:
		case <-ctx.Done():
			return
		}
		if ackErr := acknowledgeCapacitySnapshot(
			ctx,
			client,
			baseURL,
			credential,
			snapshot,
			clock,
		); ackErr != nil {
			sendCapacityError(ctx, backgroundErr, ackErr)
			return
		}
		requests.Add(1)
	}
}

func readCapacityInvalidation(reader *bufio.Reader) error {
	event := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read Display invalidation: %w", err)
		}
		line = strings.TrimSpace(line)
		if value, found := strings.CutPrefix(line, "event:"); found {
			event = strings.TrimSpace(value)
		}
		if line == "" && event == "invalidate" {
			return nil
		}
	}
}

type capacitySnapshot struct {
	ServerTime           string `json:"serverTime"`
	ProtocolVersion      string `json:"protocolVersion"`
	AssetVersion         string `json:"assetVersion"`
	StreamID             string `json:"streamId"`
	StreamPosition       string `json:"streamPosition"`
	ActiveEventID        string `json:"activeEventId"`
	ActivationGeneration string `json:"activationGeneration"`
	PublishedRevision    string `json:"publishedRevision"`
	Standby              bool   `json:"standby"`
	SnapshotToken        string `json:"snapshotToken"`
	StageTimer           *struct {
		Anchor string `json:"anchor"`
	} `json:"stageTimer"`
	Sessions []struct {
		ID                string `json:"id"`
		LiveStateRevision string `json:"liveStateRevision"`
	} `json:"sessions"`
	StageMessage struct {
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
}

type capacityClock struct {
	localReference  time.Time
	serverReference time.Time
	uncertainty     time.Duration
}

func (clock capacityClock) selectSample(candidate capacityClock) capacityClock {
	if candidate.uncertainty <= capacityTimerSkewBound ||
		candidate.uncertainty < clock.uncertainty {
		return candidate
	}
	return clock
}

func (clock capacityClock) offset() time.Duration {
	now := time.Now()
	return clock.serverReference.Add(now.Sub(clock.localReference)).Sub(now)
}

func fetchCapacitySnapshot(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	credential string,
) (capacitySnapshot, capacityClock, error) {
	started := time.Now()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+"/beamers.display.v1.DisplayService/GetSnapshot",
		strings.NewReader("{}"),
	)
	if err != nil {
		return capacitySnapshot{}, capacityClock{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: displayCookieName, Value: credential})
	response, err := client.Do(request)
	if err != nil {
		return capacitySnapshot{}, capacityClock{}, fmt.Errorf("fetch Display Snapshot: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		return capacitySnapshot{}, capacityClock{}, fmt.Errorf(
			"fetch Display Snapshot = %d %q",
			response.StatusCode,
			body,
		)
	}
	var decoded struct {
		Snapshot capacitySnapshot `json:"snapshot"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&decoded)
	closeErr := response.Body.Close()
	if err = errors.Join(decodeErr, closeErr); err != nil {
		return capacitySnapshot{}, capacityClock{}, fmt.Errorf("decode Display Snapshot: %w", err)
	}
	finished := time.Now()
	serverTime, err := time.Parse(time.RFC3339Nano, decoded.Snapshot.ServerTime)
	if err != nil {
		return capacitySnapshot{}, capacityClock{}, fmt.Errorf("parse Display server time: %w", err)
	}
	midpoint := finished.Add(-finished.Sub(started) / 2)
	return decoded.Snapshot, capacityClock{
		localReference:  midpoint,
		serverReference: serverTime,
		uncertainty:     finished.Sub(started) / 2,
	}, nil
}

func acknowledgeCapacitySnapshot(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	credential string,
	snapshot capacitySnapshot,
	clock capacityClock,
) error {
	body, err := json.Marshal(map[string]any{
		"protocol_version":          snapshot.ProtocolVersion,
		"asset_version":             snapshot.AssetVersion,
		"stream_id":                 snapshot.StreamID,
		"stream_position":           snapshot.StreamPosition,
		"active_event_id":           snapshot.ActiveEventID,
		"activation_generation":     snapshot.ActivationGeneration,
		"published_revision":        snapshot.PublishedRevision,
		"stage_message_id":          capacityProtoInteger(snapshot.StageMessage.ID),
		"stage_message_revision":    capacityProtoInteger(snapshot.StageMessage.Revision),
		"technical_difficulties_id": capacityProtoInteger(snapshot.TechnicalDifficulties.ID),
		"technical_difficulties_revision": capacityProtoInteger(
			snapshot.TechnicalDifficulties.Revision,
		),
		"urgent_notice_id":               capacityProtoInteger(snapshot.UrgentNotice.ID),
		"urgent_notice_revision":         capacityProtoInteger(snapshot.UrgentNotice.Revision),
		"emergency_alert_id":             capacityProtoInteger(snapshot.EmergencyAlert.ID),
		"emergency_alert_revision":       capacityProtoInteger(snapshot.EmergencyAlert.Revision),
		"standby":                        snapshot.Standby,
		"clock_offset_milliseconds":      clock.offset().Milliseconds(),
		"clock_uncertainty_milliseconds": clock.uncertainty.Milliseconds(),
		"renderer_unstable":              false,
		"snapshot_token":                 snapshot.SnapshotToken,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+"/beamers.display.v1.DisplayService/Acknowledge",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: displayCookieName, Value: credential})
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("acknowledge Display Snapshot: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
		return fmt.Errorf(
			"acknowledge Display Snapshot = %d %q",
			response.StatusCode,
			body,
		)
	}
	return response.Body.Close()
}

func capacityProtoInteger(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

func capacityLiveRevision(snapshot capacitySnapshot, sessionID int) (int64, error) {
	for _, session := range snapshot.Sessions {
		id, err := strconv.Atoi(session.ID)
		if err != nil {
			return 0, fmt.Errorf("parse Display Session ID: %w", err)
		}
		if id != sessionID {
			continue
		}
		revision, err := strconv.ParseInt(session.LiveStateRevision, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse Display Session revision: %w", err)
		}
		return revision, nil
	}
	return 0, errors.New("live Session missing from Display Snapshot")
}

func waitForCapacityDisplays(
	t *testing.T,
	ready <-chan struct{},
	want int,
	backgroundErr <-chan error,
) {
	t.Helper()
	for range want {
		select {
		case err := <-backgroundErr:
			t.Fatalf("start capacity Displays: %v", err)
		case <-ready:
		case <-time.After(30 * time.Second):
			t.Fatalf("capacity Displays ready fewer than %d", want)
		}
	}
}

func runCapacityCrewLoad(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	eventID int,
	consoles int,
	backgroundErr chan<- error,
	requests *atomic.Int64,
) {
	rundownClient := rundownv1connect.NewRundownServiceClient(client, baseURL)
	started := time.Now()
	var group sync.WaitGroup
	for arrival := 0; ; arrival++ {
		index := arrival % consoles
		cycle := arrival / consoles
		if !waitForCapacityArrival(
			ctx,
			capacityLoadArrival(started, index, cycle, consoles, 2*time.Second),
		) {
			break
		}
		group.Go(func() {
			_, err := rundownClient.GetCrewRundown(
				ctx,
				connect.NewRequest(&rundownv1.GetCrewRundownRequest{
					EventId: int64(eventID),
				}),
			)
			if err != nil && ctx.Err() == nil {
				sendCapacityError(ctx, backgroundErr, fmt.Errorf("Crew console: %w", err))
				return
			}
			if err == nil {
				requests.Add(1)
			}
		})
	}
	group.Wait()
}

func runCapacityPublicLoad(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	readers int,
	backgroundErr chan<- error,
	requests *atomic.Int64,
	ready chan<- struct{},
) {
	var readyReaders atomic.Int64
	var group sync.WaitGroup
	started := time.Now()
	for index := range readers {
		group.Go(func() {
			firstRequest := true
			for cycle := 0; ; cycle++ {
				if !waitForCapacityArrival(
					ctx,
					capacityLoadArrival(
						started,
						index,
						cycle,
						readers,
						publicPollingInterval,
					),
				) {
					return
				}
				request, err := http.NewRequestWithContext(
					ctx,
					http.MethodHead,
					baseURL+"/schedule",
					http.NoBody,
				)
				if err == nil {
					var response *http.Response
					response, err = client.Do(request)
					if err == nil {
						err = response.Body.Close()
						if response.StatusCode != http.StatusOK {
							err = fmt.Errorf(
								"public reader status = %d",
								response.StatusCode,
							)
						}
					}
				}
				if err != nil && ctx.Err() == nil {
					sendCapacityError(ctx, backgroundErr, err)
					return
				}
				if err == nil {
					requests.Add(1)
					if firstRequest {
						firstRequest = false
						if readyReaders.Add(1) == int64(readers) {
							close(ready)
						}
					}
				}
			}
		})
	}
	group.Wait()
}

func capacityLoadArrival(
	started time.Time,
	index int,
	cycle int,
	workers int,
	interval time.Duration,
) time.Time {
	spacing := interval / time.Duration(workers)
	arrival := cycle*workers + index
	jitter := time.Duration((arrival*137)%201-100) * spacing / 400
	return started.Add(time.Duration(arrival)*spacing + jitter)
}

func waitForCapacityArrival(ctx context.Context, scheduled time.Time) bool {
	timer := time.NewTimer(max(time.Until(scheduled), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type capacityMetrics struct {
	liveCommands     []time.Duration
	displayCommit    [][]time.Duration
	displayOperator  [][]time.Duration
	publicFreshness  []time.Duration
	maximumTimerSkew time.Duration
}

func runCapacityCommands(
	ctx context.Context,
	t *testing.T,
	client sessionv1connect.SessionControlServiceClient,
	cache *capacityScheduleCache,
	fixture capacityFixture,
	displays int,
	duration time.Duration,
	commandInterval time.Duration,
	fanoutTimeout time.Duration,
	applied <-chan capacityDisplayApplied,
	backgroundErr <-chan error,
) capacityMetrics {
	t.Helper()
	started := time.Now()
	deadline := started.Add(duration)
	metrics := capacityMetrics{}
	revision := int64(0)
	freshnessResults := make(chan capacityFreshnessResult, 512)
	commandIndex := 0
	for {
		scheduled := capacityCommandArrival(started, commandIndex, commandInterval)
		if scheduled.After(deadline) {
			break
		}
		fingerprint := ""
		if commandIndex > 0 {
			preview, err := client.PreviewAdjustTarget(
				ctx,
				connect.NewRequest(&sessionv1.PreviewAdjustTargetRequest{
					EventId:   int64(fixture.eventID),
					SessionId: int64(fixture.liveSessionID),
					Adjustment: &sessionv1.PreviewAdjustTargetRequest_Custom{
						Custom: durationpb.New(time.Second),
					},
				}),
			)
			if err != nil {
				t.Fatalf("preview capacity target adjustment: %v", err)
			}
			fingerprint = preview.Msg.GetPreviewFingerprint()
		}
		select {
		case <-ctx.Done():
			t.Fatal("capacity soak canceled")
		case <-time.After(max(time.Until(scheduled), 0)):
		}
		if lag := time.Since(scheduled); lag > commandInterval {
			t.Fatalf("capacity generator fell %s behind fixed-rate arrivals", lag)
		}
		previousETag := cache.etag()
		commandStarted := time.Now()
		command, err := executeCapacityCommand(
			ctx,
			client,
			fixture,
			commandIndex,
			revision,
			fingerprint,
		)
		if err != nil {
			t.Fatalf("execute capacity command %d: %v", commandIndex, err)
		}
		revision = command.revision
		commandCompleted := time.Now()
		metrics.liveCommands = append(
			metrics.liveCommands,
			commandCompleted.Sub(commandStarted),
		)
		operatorFanout := make([]time.Duration, 0, displays)
		commitFanout := make([]time.Duration, 0, displays)
		clockOffsets := make([]time.Duration, 0, displays)
		for range displays {
			select {
			case result := <-applied:
				if result.liveRevision != revision {
					t.Fatalf(
						"Display live revision = %d, want committed %d",
						result.liveRevision,
						revision,
					)
				}
				operatorFanout = append(
					operatorFanout,
					max(result.at.Sub(commandStarted), 0),
				)
				commitFanout = append(
					commitFanout,
					max(result.at.Sub(command.serverAt.Add(-result.clockOffset)), 0),
				)
				clockOffsets = append(clockOffsets, result.clockOffset)
				timerSkew := absoluteDuration(
					result.timerAnchor.Sub(command.timerAnchor),
				) + result.clockUncertainty
				metrics.maximumTimerSkew = max(
					metrics.maximumTimerSkew,
					timerSkew,
				)
			case err := <-backgroundErr:
				t.Fatalf("capacity Display application: %v", err)
			case <-time.After(fanoutTimeout):
				t.Fatalf(
					"capacity Displays did not apply committed output within %s",
					fanoutTimeout,
				)
			}
		}
		metrics.displayOperator = append(metrics.displayOperator, operatorFanout)
		metrics.displayCommit = append(metrics.displayCommit, commitFanout)
		go observeCapacityFreshness(
			ctx,
			cache,
			previousETag,
			command.serverAt.Add(-medianCapacityDuration(clockOffsets)),
			freshnessResults,
		)
		commandIndex++
	}
	for range commandIndex {
		select {
		case <-ctx.Done():
			t.Fatal("capacity soak canceled")
		case err := <-backgroundErr:
			t.Fatalf("capacity background load: %v", err)
		case result := <-freshnessResults:
			if result.err != nil {
				t.Fatal(result.err)
			}
			metrics.publicFreshness = append(metrics.publicFreshness, result.latency)
		}
	}
	return metrics
}

func medianCapacityDuration(values []time.Duration) time.Duration {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

type capacityCommandResult struct {
	revision    int64
	serverAt    time.Time
	timerAnchor time.Time
}

func executeCapacityCommand(
	ctx context.Context,
	client sessionv1connect.SessionControlServiceClient,
	fixture capacityFixture,
	index int,
	revision int64,
	fingerprint string,
) (capacityCommandResult, error) {
	if index == 0 {
		response, err := client.StartSession(
			ctx,
			connect.NewRequest(&sessionv1.StartSessionRequest{
				EventId:                   int64(fixture.eventID),
				SessionId:                 int64(fixture.liveSessionID),
				CommandId:                 "capacity-start-session",
				ExpectedLiveStateRevision: new(int64),
			}),
		)
		if err != nil {
			return capacityCommandResult{}, err
		}
		actualStart := response.Msg.GetState().GetActualStart()
		if actualStart == nil || actualStart.CheckValid() != nil {
			return capacityCommandResult{}, errors.New("started Session has no valid Actual Start")
		}
		return capacityCommandResult{
			revision:    response.Msg.GetState().GetLiveStateRevision(),
			serverAt:    actualStart.AsTime(),
			timerAnchor: actualStart.AsTime().Add(capacityStageDuration),
		}, nil
	}
	response, err := client.AdjustTarget(
		ctx,
		connect.NewRequest(&sessionv1.AdjustTargetRequest{
			EventId:                   int64(fixture.eventID),
			SessionId:                 int64(fixture.liveSessionID),
			CommandId:                 fmt.Sprintf("capacity-adjust-target-%04d", index),
			ExpectedLiveStateRevision: new(revision),
			Adjustment: &sessionv1.AdjustTargetRequest_Custom{
				Custom: durationpb.New(time.Second),
			},
			PreviewFingerprint:    fingerprint,
			Confirmed:             true,
			HardBoundaryConfirmed: true,
		}),
	)
	if err != nil {
		return capacityCommandResult{}, err
	}
	forecastEnd := response.Msg.GetForecastEnd()
	adjustedAt := response.Msg.GetAdjustedAt()
	if forecastEnd == nil || forecastEnd.CheckValid() != nil ||
		adjustedAt == nil || adjustedAt.CheckValid() != nil {
		return capacityCommandResult{}, errors.New("adjusted Session has invalid timing evidence")
	}
	return capacityCommandResult{
		revision:    response.Msg.GetState().GetLiveStateRevision(),
		serverAt:    adjustedAt.AsTime(),
		timerAnchor: forecastEnd.AsTime(),
	}, nil
}

func capacityCommandArrival(
	started time.Time,
	index int,
	interval time.Duration,
) time.Time {
	if index == 0 {
		return started.Add(capacityWarmup)
	}
	jitter := time.Duration((index*137)%501-250) * time.Millisecond
	return started.Add(
		capacityWarmup + time.Duration(index)*interval + jitter,
	)
}

type capacityFreshnessResult struct {
	latency time.Duration
	err     error
}

func observeCapacityFreshness(
	ctx context.Context,
	cache *capacityScheduleCache,
	previousETag string,
	committed time.Time,
	target chan<- capacityFreshnessResult,
) {
	deadline := committed.Add(publicFreshnessBound + time.Second)
	for time.Now().Before(deadline) {
		if etag := cache.etag(); etag != "" && etag != previousETag {
			sendCapacityFreshnessResult(
				ctx,
				target,
				capacityFreshnessResult{latency: time.Since(committed)},
			)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	sendCapacityFreshnessResult(ctx, target, capacityFreshnessResult{
		err: fmt.Errorf("public Schedule remained stale beyond %s", publicFreshnessBound),
	})
}

func sendCapacityFreshnessResult(
	ctx context.Context,
	target chan<- capacityFreshnessResult,
	result capacityFreshnessResult,
) {
	select {
	case target <- result:
	case <-ctx.Done():
	}
}

type capacityScheduleCache struct {
	mu        sync.RWMutex
	origin    string
	client    *http.Client
	body      []byte
	etagValue string
	expires   time.Time
}

func newCapacityScheduleCache(origin string, client *http.Client) *capacityScheduleCache {
	return &capacityScheduleCache{origin: origin, client: client}
}

func (cache *capacityScheduleCache) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	cache.mu.RLock()
	if time.Now().Before(cache.expires) {
		etag := cache.etagValue
		body := cache.body
		cache.mu.RUnlock()
		writeCapacityScheduleCache(response, request, etag, body)
		return
	}
	cache.mu.RUnlock()

	cache.mu.Lock()
	if time.Now().After(cache.expires) {
		if err := cache.refresh(request.Context()); err != nil {
			cache.mu.Unlock()
			http.Error(response, err.Error(), http.StatusBadGateway)
			return
		}
	}
	etag := cache.etagValue
	body := cache.body
	cache.mu.Unlock()
	writeCapacityScheduleCache(response, request, etag, body)
}

func writeCapacityScheduleCache(
	response http.ResponseWriter,
	request *http.Request,
	etag string,
	body []byte,
) {
	response.Header().Set("Cache-Control", "public, max-age=12")
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("ETag", etag)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(body)
}

func (cache *capacityScheduleCache) refresh(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		cache.origin+"/schedule",
		http.NoBody,
	)
	if err != nil {
		return err
	}
	if cache.etagValue != "" {
		request.Header.Set("If-None-Match", cache.etagValue)
	}
	response, err := cache.client.Do(request)
	if err != nil {
		return err
	}
	cache.expires = time.Now().Add(publicPollingInterval)
	if response.StatusCode == http.StatusNotModified {
		return response.Body.Close()
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return fmt.Errorf("origin Schedule status = %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if joinedErr := errors.Join(err, closeErr); joinedErr != nil {
		return joinedErr
	}
	cache.body = body
	cache.etagValue = response.Header.Get("ETag")
	if cache.etagValue == "" {
		return errors.New("origin Schedule has no ETag")
	}
	return nil
}

func (cache *capacityScheduleCache) etag() string {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.etagValue
}

func primeCapacitySchedule(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+"/schedule",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create capacity Schedule cache request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("prime capacity Schedule cache: %v", err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read capacity Schedule cache: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
		t.Fatalf(
			"prime capacity Schedule cache = %d, ETag %q",
			response.StatusCode,
			response.Header.Get("ETag"),
		)
	}
}

func verifyCapacityThresholds(t *testing.T, metrics capacityMetrics) bool {
	t.Helper()
	passed := true
	live := summarizeCapacityLatency(metrics.liveCommands)
	if live.Samples < 200 {
		passed = false
		t.Errorf("live command samples = %d, want at least 200", live.Samples)
	}
	if live.P50MS > 100 {
		passed = false
		t.Errorf("durable live command p50 = %dms, want <= 100ms", live.P50MS)
	}
	if live.P95MS > 250 {
		passed = false
		t.Errorf("durable live command p95 = %dms, want <= 250ms", live.P95MS)
	}
	if live.P99MS > 500 {
		passed = false
		t.Errorf("durable live command p99 = %dms, want <= 500ms", live.P99MS)
	}
	if live.MaxMS > 2_000 {
		passed = false
		t.Errorf("durable live command maximum = %dms, want <= 2000ms", live.MaxMS)
	}
	for index, fanout := range metrics.displayCommit {
		display := summarizeCapacityLatency(fanout)
		if display.P50MS > 250 ||
			display.P95MS > 500 ||
			display.P99MS > 1_000 ||
			display.MaxMS > 2_000 {
			passed = false
			t.Errorf(
				"command %d commit-to-Display fanout = %+v, want <= 250/500/1000/2000ms",
				index,
				display,
			)
		}
	}
	for index, fanout := range metrics.displayOperator {
		if display := summarizeCapacityLatency(fanout); display.P95MS > 750 {
			passed = false
			t.Errorf(
				"command %d operator-to-Display p95 = %dms, want <= 750ms",
				index,
				display.P95MS,
			)
		}
	}
	if metrics.maximumTimerSkew > capacityTimerSkewBound {
		passed = false
		t.Errorf(
			"Stage Timer maximum skew = %s, want <= 250ms",
			metrics.maximumTimerSkew,
		)
	}
	freshness := summarizeCapacityLatency(metrics.publicFreshness)
	if freshness.P50MS > 7_000 ||
		freshness.P95MS > 13_000 ||
		freshness.MaxMS > 15_000 {
		passed = false
		t.Errorf(
			"public freshness = %+v, want p50/p95/max <= 7000/13000/15000ms",
			freshness,
		)
	}
	return passed
}

func verifyCapacityWarning(
	t *testing.T,
	application http.Handler,
	fixture capacityFixture,
	stream *displaystream.Hub,
	envelope capacityEnvelope,
) {
	t.Helper()
	before, err := fixture.installation.Capacity(t.Context())
	if err != nil {
		t.Fatalf("count capacity before warning: %v", err)
	}
	created, err := fixture.installation.Competition().CreateEntry(
		t.Context(),
		fixture.actor,
		competition.CreateEntryInput{
			EventID:       fixture.eventID,
			SessionID:     fixture.competitionID,
			CommandID:     "capacity-cross-tested-count",
			Name:          "Beyond tested envelope",
			PublicDetails: "retained without refusal",
		},
	)
	if err != nil {
		t.Fatalf("create Entry beyond tested envelope: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Entry beyond tested envelope was not retained")
	}
	after, err := fixture.installation.Capacity(t.Context())
	if err != nil {
		t.Fatalf("count capacity after warning: %v", err)
	}
	if after.Sessions+after.Entries != before.Sessions+before.Entries+1 {
		t.Fatalf("capacity count after accepted Entry = %+v, before %+v", after, before)
	}
	response := httptest.NewRecorder()
	application.ServeHTTP(
		response,
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/diagnostics",
			http.NoBody,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("capacity diagnostics status = %d", response.Code)
	}
	var diagnostics normalDiagnosticsResponse
	if err = json.NewDecoder(response.Body).Decode(&diagnostics); err != nil {
		t.Fatalf("decode capacity diagnostics: %v", err)
	}
	if diagnostics.Capacity.Status != "warning" ||
		!slices.ContainsFunc(
			diagnostics.Capacity.Warnings,
			func(warning capacityWarning) bool {
				return warning.Code == "sessions_and_entries" &&
					warning.Observed == envelope.SessionsAndEntries+1
			},
		) {
		t.Fatalf("capacity diagnostics = %+v", diagnostics.Capacity)
	}
	if stream.SubscriberCount() > envelope.Displays {
		t.Fatalf("capacity warning refused or duplicated Display streams")
	}
}

func sendCapacityError(
	ctx context.Context,
	target chan<- error,
	err error,
) {
	select {
	case target <- err:
	case <-ctx.Done():
	default:
	}
}

func capacityDuration(t *testing.T, certify bool) time.Duration {
	t.Helper()
	value := os.Getenv("BEAMERS_CAPACITY_DURATION")
	if value == "" {
		value = "1m"
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < time.Minute || duration > 30*time.Minute {
		t.Fatalf("BEAMERS_CAPACITY_DURATION must be between 1m and 30m")
	}
	if certify && duration < 10*time.Minute {
		t.Fatal("certification capacity duration must be at least 10m")
	}
	return duration
}

type capacityHardware struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	CPUs          int    `json:"cpus"`
	CPUModel      string `json:"cpu_model"`
	MemoryBytes   int64  `json:"memory_bytes"`
	StorageSource string `json:"storage_source"`
	Filesystem    string `json:"filesystem"`
	NonRotational bool   `json:"non_rotational_storage"`
}

func inspectCapacityHardware(t *testing.T, dataPath string) capacityHardware {
	t.Helper()
	hardware := capacityHardware{
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		CPUs:        runtime.GOMAXPROCS(0),
		CPUModel:    linuxCPUModel(),
		MemoryBytes: linuxMemoryBytes(),
	}
	hardware.StorageSource, hardware.Filesystem = linuxMount(dataPath)
	hardware.NonRotational = linuxNonRotational(dataPath) ||
		githubHostedNonRotational(hardware.StorageSource, hardware.Filesystem)
	if hardware.Filesystem == "tmpfs" {
		hardware.NonRotational = false
	}
	return hardware
}

func linuxCPUModel() string {
	content, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		name, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(name) == "model name" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func githubHostedNonRotational(source, filesystem string) bool {
	return os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted" &&
		source == "/dev/root" &&
		filesystem == "ext4"
}

func TestGitHubHostedNonRotational(t *testing.T) {
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	if !githubHostedNonRotational("/dev/root", "ext4") {
		t.Fatal("GitHub-hosted synthetic root should use its documented SSD")
	}
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	if githubHostedNonRotational("/dev/root", "ext4") {
		t.Fatal("self-hosted synthetic root must be inspected")
	}
}

func linuxMount(path string) (string, string) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", ""
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", ""
	}
	var source, filesystem, selected string
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		separator := slices.Index(fields, "-")
		if len(fields) < 5 || separator < 0 || separator+2 >= len(fields) {
			continue
		}
		mountPoint := strings.ReplaceAll(fields[4], `\040`, " ")
		if path != mountPoint &&
			!strings.HasPrefix(path, strings.TrimSuffix(mountPoint, "/")+"/") {
			continue
		}
		if len(mountPoint) <= len(selected) {
			continue
		}
		selected = mountPoint
		filesystem = fields[separator+1]
		source = fields[separator+2]
	}
	return source, filesystem
}

func linuxNonRotational(path string) bool {
	var status syscall.Stat_t
	if err := syscall.Stat(path, &status); err != nil {
		return false
	}
	device := status.Dev
	major := (device&0x00000000000fff00)>>8 |
		(device&0xfffff00000000000)>>32
	minor := device&0x00000000000000ff |
		(device&0x00000ffffff00000)>>12
	devicePath := fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)
	value, err := os.ReadFile(filepath.Join(devicePath, "queue", "rotational"))
	if err != nil {
		resolved, resolveErr := filepath.EvalSymlinks(devicePath)
		if resolveErr != nil {
			return false
		}
		value, err = os.ReadFile(filepath.Join(
			filepath.Dir(resolved),
			"queue",
			"rotational",
		))
	}
	return err == nil && strings.TrimSpace(string(value)) == "0"
}

func linuxMemoryBytes() int64 {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total int64
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kilobytes, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr == nil {
				total = kilobytes * 1024
			}
			break
		}
	}
	limit, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return total
	}
	limited, err := strconv.ParseInt(strings.TrimSpace(string(limit)), 10, 64)
	if err == nil && limited > 0 && (total == 0 || limited < total) {
		return limited
	}
	return total
}

type capacityRunner struct {
	Environment string `json:"environment"`
	Name        string `json:"name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	GitHubRunID string `json:"github_run_id"`
	GitHubSHA   string `json:"github_sha"`
}

func inspectCapacityRunner() capacityRunner {
	return capacityRunner{
		Environment: os.Getenv("RUNNER_ENVIRONMENT"),
		Name:        os.Getenv("RUNNER_NAME"),
		OS:          os.Getenv("RUNNER_OS"),
		Arch:        os.Getenv("RUNNER_ARCH"),
		GitHubRunID: os.Getenv("GITHUB_RUN_ID"),
		GitHubSHA:   os.Getenv("GITHUB_SHA"),
	}
}

func requireCapacityProvenance(
	t *testing.T,
	hardware capacityHardware,
	runner capacityRunner,
) {
	t.Helper()
	if err := capacityProvenanceError(hardware, runner); err != nil {
		t.Fatal(err)
	}
}

func capacityProvenanceError(
	hardware capacityHardware,
	runner capacityRunner,
) error {
	if hardware.CPUs <= 0 ||
		hardware.CPUModel == "" ||
		hardware.MemoryBytes <= 0 ||
		hardware.StorageSource == "" ||
		hardware.Filesystem == "" ||
		runner.Environment != "github-hosted" ||
		runner.Name == "" ||
		runner.OS != "Linux" ||
		runner.Arch != "X64" ||
		runner.GitHubRunID == "" ||
		runner.GitHubSHA == "" {
		return fmt.Errorf(
			"capacity certification provenance is incomplete: hardware=%+v runner=%+v",
			hardware,
			runner,
		)
	}
	return nil
}

func verifyCapacityResourceBounds(
	t *testing.T,
	hardware capacityHardware,
	envelope capacityEnvelope,
	resources capacityResources,
) {
	t.Helper()
	if err := capacityResourceBoundError(hardware, envelope, resources); err != nil {
		t.Fatal(err)
	}
}

func capacityResourceBoundError(
	hardware capacityHardware,
	envelope capacityEnvelope,
	resources capacityResources,
) error {
	maxGoroutines := envelope.PublicReaders +
		4*envelope.Displays +
		2*envelope.CrewConsoles +
		2_048
	maxOpenFiles := 4*envelope.Displays +
		2*envelope.CrewConsoles +
		2_048
	if hardware.MemoryBytes <= 0 ||
		resources.MaxProcessBytes > hardware.MemoryBytes*8/10 ||
		resources.MaxGoroutines > maxGoroutines ||
		resources.MaxOpenFiles > maxOpenFiles {
		return fmt.Errorf(
			"capacity stress resources exceed bounds: hardware=%+v envelope=%+v resources=%+v",
			hardware,
			envelope,
			resources,
		)
	}
	return nil
}

type capacityLatency struct {
	Samples int   `json:"samples"`
	P50MS   int64 `json:"p50_ms"`
	P95MS   int64 `json:"p95_ms"`
	P99MS   int64 `json:"p99_ms"`
	MaxMS   int64 `json:"max_ms"`
}

type capacityFanout struct {
	PerCommand []capacityLatency `json:"per_command"`
	P50        capacityLatency   `json:"p50_across_commands"`
	P95        capacityLatency   `json:"p95_across_commands"`
	P99        capacityLatency   `json:"p99_across_commands"`
	Maximum    capacityLatency   `json:"maximum_across_commands"`
}

func summarizeCapacityFanout(commands [][]time.Duration) capacityFanout {
	found := capacityFanout{PerCommand: make([]capacityLatency, 0, len(commands))}
	p50 := make([]time.Duration, 0, len(commands))
	p95 := make([]time.Duration, 0, len(commands))
	p99 := make([]time.Duration, 0, len(commands))
	maximum := make([]time.Duration, 0, len(commands))
	for _, command := range commands {
		summary := summarizeCapacityLatency(command)
		found.PerCommand = append(found.PerCommand, summary)
		p50 = append(p50, time.Duration(summary.P50MS)*time.Millisecond)
		p95 = append(p95, time.Duration(summary.P95MS)*time.Millisecond)
		p99 = append(p99, time.Duration(summary.P99MS)*time.Millisecond)
		maximum = append(maximum, time.Duration(summary.MaxMS)*time.Millisecond)
	}
	found.P50 = summarizeCapacityLatency(p50)
	found.P95 = summarizeCapacityLatency(p95)
	found.P99 = summarizeCapacityLatency(p99)
	found.Maximum = summarizeCapacityLatency(maximum)
	return found
}

func summarizeCapacityLatency(values []time.Duration) capacityLatency {
	if len(values) == 0 {
		return capacityLatency{}
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return capacityLatency{
		Samples: len(sorted),
		P50MS:   durationMilliseconds(capacityPercentile(sorted, 50)),
		P95MS:   durationMilliseconds(capacityPercentile(sorted, 95)),
		P99MS:   durationMilliseconds(capacityPercentile(sorted, 99)),
		MaxMS:   durationMilliseconds(sorted[len(sorted)-1]),
	}
}

func capacityPercentile(sorted []time.Duration, percentile int) time.Duration {
	index := int(math.Ceil(float64(percentile)*float64(len(sorted))/100)) - 1
	return sorted[max(index, 0)]
}

func durationMilliseconds(value time.Duration) int64 {
	return int64(math.Ceil(float64(value) / float64(time.Millisecond)))
}

func absoluteDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

type capacityEnvelope struct {
	Locations          int `json:"locations"`
	Lanes              int `json:"lanes"`
	Displays           int `json:"displays"`
	CrewConsoles       int `json:"crew_consoles"`
	SessionsAndEntries int `json:"sessions_and_entries"`
	PublicReaders      int `json:"public_readers"`
}

type capacityTopology struct {
	ServerHostname    string `json:"server_hostname"`
	GeneratorHostname string `json:"generator_hostname"`
	SeparateMachines  bool   `json:"separate_machines"`
	Certified         bool   `json:"certified"`
}

type capacityReport struct {
	Profile                 string            `json:"profile"`
	Hardware                capacityHardware  `json:"hardware"`
	Runner                  capacityRunner    `json:"runner"`
	Duration                string            `json:"duration"`
	Envelope                capacityEnvelope  `json:"envelope"`
	Topology                capacityTopology  `json:"topology"`
	Resources               capacityResources `json:"resources"`
	LiveCommand             capacityLatency   `json:"live_command"`
	DisplayCommit           capacityFanout    `json:"display_commit"`
	DisplayOperator         capacityFanout    `json:"display_operator"`
	StageTimerMaximumSkewMS int64             `json:"stage_timer_maximum_skew_ms"`
	PublicFreshness         capacityLatency   `json:"public_freshness"`
	CrewRequests            int64             `json:"crew_requests"`
	DisplayRequests         int64             `json:"display_requests"`
	PublicRequests          int64             `json:"public_requests"`
}

func writeCapacityReport(t *testing.T, report capacityReport) {
	t.Helper()
	path := os.Getenv("BEAMERS_CAPACITY_REPORT")
	if path == "" {
		return
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode capacity report: %v", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create capacity report directory: %v", err)
	}
	if err = os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write capacity report: %v", err)
	}
}

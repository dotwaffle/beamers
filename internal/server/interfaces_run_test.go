package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/operations"
)

func TestRunTerminatesTLSAndIsolatesPublicListener(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "installation")
	if err := operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	privateAddress := reserveTCPAddress(t)
	publicAddress := reserveTCPAddress(t)
	certificatePath, keyPath := writeTestCertificate(t)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Config{
			DataDir: dataDir, ListenAddress: privateAddress, PublicListen: publicAddress,
			BuildVersion: "test", ShutdownTimeout: 5 * time.Second,
			Logger:         slog.New(slog.DiscardHandler),
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  noop.NewMeterProvider(),
			Propagator:     propagation.TraceContext{},
			TLSCertificate: certificatePath,
			TLSPrivateKey:  keyPath,
		})
	}()
	t.Cleanup(cancel)

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("default HTTP transport is not *http.Transport")
	}
	transport := baseTransport.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    x509.NewCertPool(),
	}
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatalf("read test certificate: %v", err)
	}
	if !transport.TLSClientConfig.RootCAs.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("load test certificate")
	}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	waitForHTTPSReady(t, client, privateAddress)
	publicClient := &http.Client{Timeout: time.Second}
	waitForPublicSchedule(t, publicClient, publicAddress)

	privateRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"https://"+privateAddress+"/auth/session",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create TLS Crew request: %v", err)
	}
	privateResponse, err := client.Do(privateRequest)
	if err != nil {
		t.Fatalf("GET TLS Crew session: %v", err)
	}
	if closeErr := privateResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close TLS Crew response: %v", closeErr)
	}
	if privateResponse.StatusCode != http.StatusUnauthorized ||
		privateResponse.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf(
			"TLS Crew response = %d, headers %v",
			privateResponse.StatusCode,
			privateResponse.Header,
		)
	}

	publicAuthRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+publicAddress+"/auth/session",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create public-listener Crew request: %v", err)
	}
	publicAuth, err := publicClient.Do(publicAuthRequest)
	if err != nil {
		t.Fatalf("GET public-listener Crew route: %v", err)
	}
	if closeErr := publicAuth.Body.Close(); closeErr != nil {
		t.Fatalf("close public-listener Crew response: %v", closeErr)
	}
	if publicAuth.StatusCode != http.StatusNotFound {
		t.Fatalf("public-listener Crew route = %d, want %d", publicAuth.StatusCode, http.StatusNotFound)
	}
	publicScheduleRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+publicAddress+"/schedule",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create public-listener Schedule request: %v", err)
	}
	publicSchedule, err := publicClient.Do(publicScheduleRequest)
	if err != nil {
		t.Fatalf("GET public-listener Schedule: %v", err)
	}
	if closeErr := publicSchedule.Body.Close(); closeErr != nil {
		t.Fatalf("close public-listener Schedule response: %v", closeErr)
	}
	if publicSchedule.StatusCode != http.StatusOK {
		t.Fatalf("public-listener Schedule = %d, want %d", publicSchedule.StatusCode, http.StatusOK)
	}

	cancel()
	if err = <-result; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("stop TLS server: %v", err)
	}
}

func waitForPublicSchedule(t *testing.T, client *http.Client, address string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+address+"/schedule",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create public readiness request: %v", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("public server did not become ready")
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP address: %v", err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("release TCP address: %v", err)
	}
	return address
}

func waitForHTTPSReady(t *testing.T, client *http.Client, address string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"https://"+address+"/readyz",
			http.NoBody,
		)
		if err != nil {
			t.Fatalf("create TLS readiness request: %v", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("TLS server did not become ready")
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test TLS key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create test TLS certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal test TLS key: %v", err)
	}
	certificatePath := filepath.Join(t.TempDir(), "certificate.pem")
	keyPath := filepath.Join(t.TempDir(), "private-key.pem")
	if err = os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: der,
	}), 0o600); err != nil {
		t.Fatalf("write test TLS certificate: %v", err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: keyDER,
	}), 0o600); err != nil {
		t.Fatalf("write test TLS key: %v", err)
	}
	return certificatePath, keyPath
}

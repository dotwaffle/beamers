package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

func TestRegisterConnectRouteOwnsMethodAndBodyAdmission(t *testing.T) {
	mux := http.NewServeMux()
	backendCalls := 0
	err := registerConnectRoute(mux, connectRouteConfig{
		name: "test", authentication: &auth.Service{},
		listenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
		tracerProvider:  tracenoop.NewTracerProvider(), meterProvider: metricnoop.NewMeterProvider(),
		propagator:       propagation.TraceContext{},
		errorInterceptor: connectapi.RequestIDInterceptor(), validationInterceptor: connectapi.RequestIDInterceptor(),
		maxBodyBytes: 4,
		build: func(...connect.HandlerOption) (string, http.Handler) {
			return "/test", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				backendCalls++
				if _, readErr := io.ReadAll(request.Body); readErr != nil {
					var maximum *http.MaxBytesError
					if errors.As(readErr, &maximum) {
						http.Error(response, "request too large", http.StatusRequestEntityTooLarge)
						return
					}
					http.Error(response, "read request", http.StatusBadRequest)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			})
		},
	})
	if err != nil {
		t.Fatalf("register Connect route: %v", err)
	}

	get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/test", http.NoBody)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed || backendCalls != 0 {
		t.Fatalf("GET response/calls = %d/%d, want 405/0", getResponse.Code, backendCalls)
	}

	post := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/test", strings.NewReader("12345"))
	postResponse := httptest.NewRecorder()
	mux.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusRequestEntityTooLarge || backendCalls != 1 {
		t.Fatalf("large POST response/calls = %d/%d, want 413/1", postResponse.Code, backendCalls)
	}
}

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestInstrumentInboundHTTPSkipsConnectRoutes confirms otelhttp does not
// record its own span for Connect RPC paths. otelconnect's interceptor
// already instruments those end to end (see registerConnectRoute); a
// second, outer otelhttp span for the same request would both duplicate
// exported spans and metric series, and would extract and trust inbound
// trace context ahead of otelconnect's own remote-context handling,
// which is exactly what ADR 0035 says the two layers must avoid.
func TestInstrumentInboundHTTPSkipsConnectRoutes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(t.Context())
	})

	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	handler := instrumentInboundHTTP(Config{
		TracerProvider: tracerProvider,
		MeterProvider:  metricnoop.NewMeterProvider(),
		Propagator:     propagation.TraceContext{},
	}, "beamers.test", next)

	connectRequest := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/beamers.competition.v1.CompetitionService/List", http.NoBody,
	)
	handler.ServeHTTP(httptest.NewRecorder(), connectRequest)

	plainRequest := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/diagnostics", http.NoBody,
	)
	handler.ServeHTTP(httptest.NewRecorder(), plainRequest)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want exactly 1 for the non-Connect request", len(spans))
	}
	var sawPath bool
	for _, attribute := range spans[0].Attributes() {
		if string(attribute.Key) == "url.path" || string(attribute.Key) == "http.target" {
			if attribute.Value.AsString() != "/diagnostics" {
				t.Fatalf("recorded span for path %q, want /diagnostics", attribute.Value.AsString())
			}
			sawPath = true
		}
	}
	if !sawPath {
		t.Fatalf("recorded span has no url.path/http.target attribute: %+v", spans[0].Attributes())
	}
}

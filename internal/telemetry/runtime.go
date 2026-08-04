// Package telemetry owns optional bounded OpenTelemetry export.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	queueSize = 2048
	batchSize = 512
)

// Config contains the explicit OTLP export boundary.
type Config struct {
	Endpoint       string
	ServiceVersion string
	Stderr         io.Writer
	SampleRatio    float64
	ExportTimeout  time.Duration
	// Level gates both the stderr and OTLP-exported log sinks. A nil Level
	// keeps the historical fixed slog.LevelInfo behavior.
	Level *slog.LevelVar
}

// Runtime supplies one stderr-first logger and shared signal providers.
type Runtime struct {
	logger          *slog.Logger
	tracerProvider  trace.TracerProvider
	meterProvider   metric.MeterProvider
	traceSDK        *sdktrace.TracerProvider
	metricSDK       *sdkmetric.MeterProvider
	logSDK          *log.LoggerProvider
	shutdownTimeout time.Duration
	shutdownOnce    sync.Once
	shutdownErr     error
}

// New creates a disabled runtime when Endpoint is empty.
func New(ctx context.Context, config Config) (*Runtime, error) {
	if config.Stderr == nil {
		return nil, errors.New("telemetry stderr is required")
	}
	level := config.Level
	if level == nil {
		level = &slog.LevelVar{}
	}
	stderrHandler := slog.NewJSONHandler(config.Stderr, &slog.HandlerOptions{Level: level})
	if config.Endpoint == "" {
		return &Runtime{
			logger:         slog.New(stderrHandler),
			tracerProvider: tracenoop.NewTracerProvider(),
			meterProvider:  metricnoop.NewMeterProvider(),
		}, nil
	}
	if config.ServiceVersion == "" {
		return nil, errors.New("telemetry service version is required")
	}
	if math.IsNaN(config.SampleRatio) || config.SampleRatio < 0 || config.SampleRatio > 1 {
		return nil, errors.New("telemetry sample ratio must be between zero and one")
	}
	if config.ExportTimeout <= 0 {
		return nil, errors.New("telemetry export timeout must be positive")
	}
	endpoints, err := signalEndpoints(config.Endpoint)
	if err != nil {
		return nil, err
	}
	identity, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", "beamers"),
		attribute.String("service.version", config.ServiceVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	traceExporter, err := otlptracehttp.New(
		ctx,
		otlptracehttp.WithEndpointURL(endpoints.traces),
		otlptracehttp.WithTimeout(config.ExportTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(
		ctx,
		otlpmetrichttp.WithEndpointURL(endpoints.metrics),
		otlpmetrichttp.WithTimeout(config.ExportTimeout),
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create metric exporter: %w", err),
			traceExporter.Shutdown(context.WithoutCancel(ctx)),
		)
	}
	logExporter, err := otlploghttp.New(
		ctx,
		otlploghttp.WithEndpointURL(endpoints.logs),
		otlploghttp.WithTimeout(config.ExportTimeout),
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create log exporter: %w", err),
			metricExporter.Shutdown(context.WithoutCancel(ctx)),
			traceExporter.Shutdown(context.WithoutCancel(ctx)),
		)
	}

	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(identity),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(
			traceExporter,
			sdktrace.WithMaxQueueSize(queueSize),
			sdktrace.WithMaxExportBatchSize(batchSize),
			sdktrace.WithExportTimeout(config.ExportTimeout),
		)),
	)
	metricProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(identity),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			metricExporter,
			sdkmetric.WithInterval(10*time.Second),
			sdkmetric.WithTimeout(config.ExportTimeout),
		)),
	)
	logProvider := log.NewLoggerProvider(
		log.WithResource(identity),
		log.WithProcessor(log.NewBatchProcessor(
			logExporter,
			log.WithMaxQueueSize(queueSize),
			log.WithExportMaxBatchSize(batchSize),
			log.WithExportTimeout(config.ExportTimeout),
		)),
	)
	return &Runtime{
		logger: slog.New(slog.NewMultiHandler(
			stderrHandler,
			boundedLogHandler{
				level: level,
				next: otelslog.NewHandler(
					"github.com/dotwaffle/beamers",
					otelslog.WithLoggerProvider(logProvider),
				),
			},
		)),
		tracerProvider:  traceProvider,
		meterProvider:   metricProvider,
		traceSDK:        traceProvider,
		metricSDK:       metricProvider,
		logSDK:          logProvider,
		shutdownTimeout: config.ExportTimeout,
	}, nil
}

// Logger returns the authoritative structured logger.
func (runtime *Runtime) Logger() *slog.Logger {
	return runtime.logger
}

// TracerProvider returns the shared trace provider.
func (runtime *Runtime) TracerProvider() trace.TracerProvider {
	return runtime.tracerProvider
}

// MeterProvider returns the shared metric provider.
func (runtime *Runtime) MeterProvider() metric.MeterProvider {
	return runtime.meterProvider
}

// Propagator returns the bounded W3C propagation formats.
func (*Runtime) Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

// Enabled reports whether OTLP export is configured.
func (runtime *Runtime) Enabled() bool {
	return runtime.traceSDK != nil
}

// Shutdown flushes all enabled providers within ctx.
func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if !runtime.Enabled() {
		return nil
	}
	runtime.shutdownOnce.Do(func() {
		shutdownContext, cancel := context.WithTimeout(ctx, runtime.shutdownTimeout)
		defer cancel()
		results := make(chan error, 3)
		go func() { results <- runtime.logSDK.Shutdown(shutdownContext) }()
		go func() { results <- runtime.metricSDK.Shutdown(shutdownContext) }()
		go func() { results <- runtime.traceSDK.Shutdown(shutdownContext) }()
		runtime.shutdownErr = errors.Join(<-results, <-results, <-results)
	})
	return runtime.shutdownErr
}

type endpoints struct {
	traces  string
	metrics string
	logs    string
}

func signalEndpoints(raw string) (endpoints, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return endpoints{}, errors.New("OTLP endpoint must be an HTTP URL without credentials, query, or fragment")
	}
	base := strings.TrimRight(raw, "/")
	return endpoints{
		traces: base + "/v1/traces", metrics: base + "/v1/metrics", logs: base + "/v1/logs",
	}, nil
}

type boundedLogHandler struct {
	next  slog.Handler
	level slog.Leveler
}

func (handler boundedLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if handler.level != nil && level < handler.level.Level() {
		return false
	}
	return handler.next.Enabled(ctx, level)
}

func (handler boundedLogHandler) Handle(ctx context.Context, record slog.Record) error {
	filtered := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		if bounded, ok := boundedLogAttribute(attribute); ok {
			filtered.AddAttrs(bounded)
		}
		return true
	})
	return handler.next.Handle(ctx, filtered)
}

func (handler boundedLogHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	filtered := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		if bounded, ok := boundedLogAttribute(attribute); ok {
			filtered = append(filtered, bounded)
		}
	}
	return boundedLogHandler{next: handler.next.WithAttrs(filtered), level: handler.level}
}

func (handler boundedLogHandler) WithGroup(string) slog.Handler {
	return handler
}

// maxBoundedAttributeRunes caps the length of exported free-text attributes
// (error detail and the rollback Backup path) so one oversized value cannot
// inflate an OTLP export batch.
const maxBoundedAttributeRunes = 1024

// boundedLogAttribute reports whether attr may leave the process boundary
// and, when it may, the exact value to export. Free-text attributes are
// truncated rather than rejected outright so operators still see the start
// of an overlong error or path.
func boundedLogAttribute(attr slog.Attr) (slog.Attr, bool) {
	switch attr.Key {
	case "component", "mode", "phase", "status", "finalizer", "command":
		if attr.Value.Kind() != slog.KindString {
			return slog.Attr{}, false
		}
		return attr, true
	case "budget_ms", "elapsed_ms", "remaining_ms":
		if attr.Value.Kind() != slog.KindInt64 && attr.Value.Kind() != slog.KindUint64 {
			return slog.Attr{}, false
		}
		return attr, true
	case "rollback_backup":
		if attr.Value.Kind() != slog.KindString {
			return slog.Attr{}, false
		}
		return slog.String(attr.Key, truncateRunes(attr.Value.String(), maxBoundedAttributeRunes)), true
	case "error":
		message := errorAttributeMessage(attr.Value)
		if message == "" {
			return slog.Attr{}, false
		}
		return slog.String(attr.Key, truncateRunes(message, maxBoundedAttributeRunes)), true
	default:
		return slog.Attr{}, false
	}
}

// errorAttributeMessage extracts free text from an "error" attribute value,
// which is ordinarily an error but may already have been reduced to a
// string by an intermediate slog.Handler.
func errorAttributeMessage(value slog.Value) string {
	resolved := value.Resolve()
	switch resolved.Kind() {
	case slog.KindString:
		return resolved.String()
	case slog.KindAny:
		if err, ok := resolved.Any().(error); ok && err != nil {
			return err.Error()
		}
	}
	return ""
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…(truncated)"
}

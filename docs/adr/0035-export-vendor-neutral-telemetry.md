# Export vendor-neutral telemetry

Version one instruments the service with OpenTelemetry traces and metrics and supports optional OTLP export.
The service starts and remains fully operational without a collector or telemetry backend.
Health and readiness endpoints are independent from telemetry export.

Application code uses one structured `slog.Logger`.
Its handler always writes to standard error and, when OTLP log export is configured, also forwards the same records through `go.opentelemetry.io/contrib/bridges/otelslog`.
Logs, traces, and metrics share service and resource identity.
Context-aware log calls preserve correlation with active trace and span context where available.

The OpenTelemetry log path is additive rather than authoritative because its Go API and bridge remain less mature than traces and metrics.
Export uses bounded, asynchronous batching.
Backpressure, queue exhaustion, collector failure, and shutdown timeout may drop telemetry but never block or fail live control; standard-error logging remains available.
Deployments avoid ingesting both the standard-error copy and the OTLP copy into the same backend.

Telemetry excludes credentials, secrets, attachment contents, and unbounded content attributes.
Metrics use bounded-cardinality dimensions.
Sampling and export destinations are configurable, and production deployment may use any compatible OTLP backend.

An optional development profile runs Grafana's `docker-otel-lgtm` stack for local traces, metrics, logs, and profiling experiments.
It is not production infrastructure.
Go `pprof` endpoints are opt-in and restricted to an administrative or loopback listener; continuous profiling remains optional.

Connect handlers use `connectrpc.com/otelconnect` with explicit tracer and meter providers.
Remote trace context is untrusted by default, peer-address attributes and per-message trace events are disabled, and additional attributes use an allowlist.
HTTP and RPC instrumentation are configured to avoid duplicate spans and redundant high-cardinality metric families.

The Ent database handle is backed by an `XSAM/otelsql`-instrumented `database/sql` connection when telemetry is enabled.
It emits selected operation spans and bounded connection-pool metrics.
Row-iteration, connection-reset, health-check, and other low-value spans are suppressed.
Bind values, DSNs, and credentials are never recorded; SQL commenter is disabled.
Query-text capture is disabled by default and may be enabled only after its statements and cardinality have been reviewed.

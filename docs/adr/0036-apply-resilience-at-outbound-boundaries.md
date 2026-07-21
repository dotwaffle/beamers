# Apply resilience at outbound boundaries

Beamers treats resilience as a boundary-specific contract rather than applying blanket retries.
Domain durability, idempotency, stale-command rejection, SQLite transactions, and any future transactional outbox remain explicit application behavior.

Failsafe-Go may implement retry, timeout, circuit-breaker, bulkhead, and fallback policies for a concrete outbound dependency.
The dependency is added only when a version-one adapter needs such a policy.
It does not wrap arbitrary database transactions, live commands, telemetry exporters that already manage delivery, or other operations whose retry semantics are unknown.

Every outbound operation has a deadline and propagates cancellation.
Retries use an allowlist of classified transient failures, bounded attempts and elapsed time, exponential backoff, and jitter.
Retrying a mutating operation requires a stable idempotency key accepted by the destination.
Mutations are never hedged.

Circuit breakers and bulkheads isolate individual dependencies or destinations without sharing unbounded per-tenant state.
Fallbacks must have explicit domain semantics, such as serving a known stale snapshot; an invented success response is not a fallback.

Policy attempts, latency, rejection, circuit state, and exhaustion are exposed through bounded-cardinality OpenTelemetry signals.
Tests cover classification, attempt limits, cancellation, idempotency, policy composition, and recovery.

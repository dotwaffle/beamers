# Separate liveness, readiness, and diagnostics

Beamers exposes a coarse unauthenticated liveness probe that reports whether the process can respond without consulting dependencies.
It remains healthy in storage-degraded and local recovery modes so a supervisor does not turn a recoverable fault into a restart loop.

A separate coarse readiness probe succeeds only when the authoritative database is open, uses a supported schema, and is usable for normal operation.
Failed readiness is a status signal; it never initiates restart, restore, promotion, or failover within Beamers.
The absence of an Active Event is an operational state, not a service-readiness failure.

Detailed component status, including storage, replication, backup, telemetry, and live-stream state, is available only through an authenticated administrative view or local diagnostics.
Public probes disclose no configuration, paths, versions, Event data, or failure details.
Telemetry export remains outside both probe dependencies as established by ADR 0035.

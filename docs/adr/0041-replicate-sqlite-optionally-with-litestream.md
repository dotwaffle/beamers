# Replicate SQLite optionally with Litestream

Beamers may embed Litestream as an optional, disabled-by-default continuous replication adapter for its authoritative SQLite database.
The default server and Display builds remain CGo-free, and embedded replication uses the same `modernc.org/sqlite` driver as the application.
Litestream's library API is pinned and isolated behind the replication module because it is not yet a stable application interface.

Replication is asynchronous disaster-recovery protection, not command durability, backup replacement, or automatic failover.
A replication error never blocks live control.
Beamers exposes replication position, lag, last successful synchronization, and errors through health information and bounded OpenTelemetry signals.
Graceful shutdown attempts a bounded final synchronization and reports failure rather than hanging.

A Litestream replica is a full-fidelity copy that includes authentication secrets and other sensitive database content.
It cannot satisfy the default sanitized-backup policy in ADR 0011 and therefore depends on external storage encryption, access control, and retention.
Beamers retains its separate, versioned backup and restore mechanism.

Restore and promotion are explicit administrative operations.
Beamers never automatically replaces an existing or corrupt local database from a replica, and replication never creates a second live-control authority.

Future read replicas may use Litestream VFS through a separate, optional CGo-enabled build.
They remain potentially stale, read-only consumers for reporting or similar workloads; they cannot serve authoritative live state, accept commands, or participate in automatic failover.
CGo is not required by the authoritative venue server.

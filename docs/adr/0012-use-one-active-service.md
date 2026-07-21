# Use one active service

The initial system has one active venue-local service.
It restarts against its durable SQLite state on ordinary process or host recovery and creates frequent safe backups for manual restoration on a standby device.
Displays continue their last known Views during authority loss and reconnect through a stable local hostname when restored credentials permit.
A default sanitized backup instead requires Administrator re-bootstrap and Display re-Enrollment.
Automated active/passive or active/active failover is deferred to avoid election, promotion, and split-brain behavior in the first version.
Optional asynchronous replication under ADR 0041 provides disaster-recovery material without creating another active service.

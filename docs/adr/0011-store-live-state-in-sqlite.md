# Store live state in SQLite

The venue-local service stores authoritative Event configuration and live state in SQLite.
Its transactional single-file database fits a single active service and supports straightforward local backup and restart without requiring an external database at the Venue.
A separate versioned Event export/import format provides interchange and archival portability; consumers do not exchange raw database files.

Backup and restore remain a separate disaster-recovery mechanism; restoring a backup does not create a Draft or perform import conflict resolution.
Backups include Events, Drafts, live state, account identities, roles and scopes, audit history, service configuration, and Display identities and Assignments.
Optional continuous replication under ADR 0041 copies the full SQLite database and does not replace this backup format or its sanitization controls.

By default, backups exclude authentication material, including password hashes, WebAuthn credentials, active login sessions, Display credentials, and other private keys or tokens.
An explicit option may include that material when rapid credential-preserving recovery is required.
A sanitized Restore therefore requires Administrator re-bootstrap and Display re-Enrollment.
The application does not encrypt backup files; Crew Members may apply external encryption and storage controls.

An Administrator-only Restore validates integrity and compatibility, snapshots any readable state being replaced, and swaps the restored state atomically.
A local server-console command then generates a short-lived, single-use code to establish one new Administrator credential.
No default password or permanent recovery account exists.

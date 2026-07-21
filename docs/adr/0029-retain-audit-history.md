# Retain Audit History

Version one retains application Audit Entries for the installation's lifetime.
They are append-only and have no automatic expiry or per-entry deletion.
This keeps live decisions, publication, corrections, permissions, and recovery actions explainable after an Event rather than allowing selective history removal.

Audit Entries contain the actor, server time, action, target, result, and any domain-required reason or note.
They never contain passwords, password hashes, authentication credentials, private keys, bearer tokens, or Attachment file contents.
File metadata and integrity identifiers may be recorded when relevant.

Authenticated live commands rejected as stale, unauthorized, validation-blocked, or conflicting takeover attempts also create Audit Entries.
Exact idempotent retries remain one logical audited command rather than creating noise.
Malformed requests, probes, and unauthenticated transport failures belong in operational logs instead of domain audit history.

Version one enforces append-only behavior through the application and does not claim cryptographic tamper evidence.
Hash chaining and an external append-only sink would add key, checkpoint, or infrastructure dependencies to an otherwise venue-local system.
A server administrator with direct SQLite access remains outside this guarantee; audit export may be added later.

Backups include audit history under the existing backup policy.
Any future whole-Event retention or purge mechanism requires a separate explicit design; version one does not infer it from age, Event dates, or Active Event changes.

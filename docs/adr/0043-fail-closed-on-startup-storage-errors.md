# Fail closed on startup storage errors

If the authoritative database is corrupt, unreadable, incompatible, or otherwise unsafe at startup, Beamers enters a local-only recovery mode and serves no Crew, public, Display, or live-control interface.
It exposes bounded diagnostics and permits only an explicit restore whose source passes integrity and compatibility validation.
It never creates a replacement at an existing database path or restores from a backup or replica automatically.

First-run setup is available only when the data directory is genuinely uninitialized.
Existing installation markers, database artifacts, replication metadata, or other owned state make an absent database a recovery condition, not a new installation.
Recovery preserves the suspect state for diagnosis and never overwrites it without a separately confirmed administrative action.

When database-backed authentication is unavailable, recovery authority comes from host operating-system access rather than a web credential.
An offline CLI requires exclusive access to the data directory, validates the selected source, shows the exact replacement plan, and requires explicit confirmation.
It moves existing state to a non-overwriting quarantine location before atomically installing the restored state.
Beamers has no permanent recovery password or remote recovery endpoint.

Kong is the preferred Go command-line parser while it remains suitable, but is not an architectural dependency.
CLI structs adapt parsed input to application operations; recovery, migration, backup, and diagnostic behavior does not depend on Kong types.

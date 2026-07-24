# Coordinate maintenance inside Beamers

Beamers uses one executable for serving, Backup, Restore, migration, and host-authorized recovery; it does not ship a separate administrative utility whose version may drift from the deployment.
While healthy storage can authenticate an Administrator, Backup and Restore are available through an administrative maintenance surface.
When storage cannot authenticate safely, equivalent guarded subcommands of the same executable use host operating-system authority.

Safe committed migrations run automatically during startup Maintenance Mode only after Beamers creates and verifies a Backup.
Beamers migrates and sanity-checks staged state before reopening normal Interfaces.
Destructive, rollback-incompatible, or unclassified plans require a re-authenticated Administrator preview and approval, a new verified Full-Fidelity Backup, explicit consequences, and an Audit Entry; host authority is the fallback when the old installation cannot provide those controls.

The default Sanitized Backup and explicit Full-Fidelity Backup are conventional versioned archives containing the database, every referenced immutable Attachment Version, a compatibility manifest, and integrity hashes.
The Attachment Store is a configurable local root and may reside on a different filesystem from SQLite.
Web Administrators download a verified archive or select an operator-configured export destination, while only a host-authorized subcommand may name an arbitrary server path.
Beamers does not encrypt Backup files, and a Full-Fidelity Backup requires recent re-authentication plus an explicit protection acknowledgment.

Restore stages and validates the database and Attachment Store together.
A durable cutover journal coordinates cross-filesystem quarantine and installation, resumes or rolls back after interruption, and prevents Beamers from serving mixed generations.
Healthy Restore runs inside the main process behind a minimal Maintenance Mode shell; unsafe-startup Restore remains a host-authorized subcommand.
Normal compatibility follows declared reader and writer ranges, while a host-only forced unsupported Restore preserves all inputs, reports unknown schema elements, requires a reason and prominent acknowledgment, and makes no safety claim.

During Maintenance Mode, interactive Crew Clients show the maintenance state and return to their requested page when readiness returns.
Output Displays retain their last committed frame and show a discreet, accessible Stale marker distinct from Disconnected.
Final Files Export remains a separate derived publication capability and is not part of Backup or Restore.

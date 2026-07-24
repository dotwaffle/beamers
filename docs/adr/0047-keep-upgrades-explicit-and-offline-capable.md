# Keep upgrades explicit and offline-capable

Status: superseded by ADR-0051

Version one performs no automatic update discovery, download, installation, or restart.
It exposes its build version without contacting an update service.
Administrators obtain and verify release binaries or images externally, so an installation can be maintained without internet access.

An upgrade stops the service, runs the explicit migration preflight, creates and verifies a backup, applies committed migrations, then starts the new version and verifies readiness.
Normal startup never migrates implicitly.
If validation fails, rollback uses the prior binary or image together with the pre-upgrade backup because version one has no down migrations.
This extends ADR 0034's schema policy to the complete operational upgrade flow.

Migration preflight blocks by default when persisted state indicates live operation, including a Live Session, active Competition presentation, Result Reveal, or Emergency Alert.
An urgent crash, security, or data-visibility fix may use an explicit force-live option.
That path requires exclusive database access, a mandatory reason, a newly verified backup, a prominent confirmation, and an Audit Entry before migration begins.
It preserves the live state rather than implicitly ending, clearing, or otherwise repairing it.

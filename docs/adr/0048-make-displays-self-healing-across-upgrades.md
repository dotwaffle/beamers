# Make Displays self-healing across upgrades

Displays never require a manual browser refresh for an ordinary server restart, upgrade, transport interruption, or recoverable rendering failure.
They keep their last rendered frame, show the disconnection indicator, reconnect with bounded backoff and jitter, obtain an authoritative snapshot, and resume live updates.
Asset URLs are content-addressed so cached files from different builds cannot be mixed.

The Display state protocol remains backward-compatible within version one where practical.
Every connection and snapshot identifies its protocol version.
If a new server cannot serve the running client, the Display first verifies that the new entry document is reachable, then performs a controlled reload of its server-hosted assets automatically.
Failed preflight or reload attempts retain the old frame where the browser permits and retry without forming a reload loop.

Crew pages identify their server build as well.
A stale page may continue to read compatible information, but the server rejects its mutations with a reload-required result rather than interpreting an obsolete command shape.
The page then reloads automatically while preserving any safe unsent input when possible.

Page code handles transport, snapshot-gap, protocol, asset, and recoverable renderer failures.
A kiosk process supervisor handles browser-process exit.
Revoked Enrollment credentials, unsupported browser behavior, operating-system failure, and hardware failure remain explicit intervention cases; self-healing does not weaken authentication or conceal an unrecoverable fault.

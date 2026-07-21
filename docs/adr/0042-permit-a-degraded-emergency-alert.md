# Permit a degraded Emergency Alert

If the authoritative SQLite database becomes unavailable or unwritable after startup, Beamers rejects every ordinary state-changing command, including Urgent Notice and Technical Difficulties commands.
Displays continue their last committed state.
As a narrow safety exception, an already-authenticated Crew Member whose applicable Emergency Alert capability was validated before the failure may explicitly activate or clear an in-memory Emergency Alert.

The control surface labels this path as severely degraded and nondurable before confirmation.
The alert reaches currently connected Displays and is included in Display snapshots while the process remains alive, but it may disappear on restart.
No new login, permission grant, target expansion, or other authority change is accepted while authorization cannot be revalidated.

Beamers emits the failure and degraded commands immediately to standard error and configured OpenTelemetry sinks.
It retains their command identities and audit details in memory and attempts to persist the resulting active state, Command Receipts, and Audit Entries when storage recovers.
If the process exits first, that evidence and any active degraded alert are lost.

This is an explicit exception to the transactional durability rules in ADRs 0028 and 0040.
It exists only so a storage failure cannot prevent urgent safety communication; the Venue's emergency systems remain authoritative.

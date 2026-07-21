# Adapt shutdown to the platform budget

Beamers honors the hosting platform's normal graceful-stop budget rather than requiring a longer one.
Official deployment configuration tells the process its available budget because a termination signal carries no deadline.
Docker and Compose use their ten-second Linux default; Kubernetes uses its thirty-second default.
Custom deployments may provide a longer budget.
The verified platform behavior is recorded in
[the shutdown research](../research/shutdown-grace-periods.md).

On termination, Beamers immediately marks readiness false.
If there are no active application requests or persistent client connections, it skips traffic draining and begins final synchronization and closure.
Otherwise, under the thirty-second profile, it continues accepting commands for up to ten seconds so traffic steering can converge, then rejects new mutations, notifies and closes persistent streams, and lets in-flight work finish.

The final ten seconds are reserved for bounded Litestream and telemetry flushing and clean storage closure.
Reaching that reserve skips any unfinished drain phase.
Under the ten-second Docker profile, Beamers therefore rejects new mutations and begins final shutdown immediately.
Every phase advances early when its work is complete.
Kubernetes deployments use no sleeping `preStop` hook because it would consume the same grace period before Beamers receives the termination signal.

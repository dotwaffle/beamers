# Stream revisioned state with SSE

Version one uses server-sent events to notify Displays and Crew consoles about authoritative state changes.
ConnectRPC remains responsible for commands, Display acknowledgments, and complete state snapshots.
ADR 0053 extends the same invalidation and recovery model to public and signed-in Frontend browsers.
Cacheable complete pages remain available for direct navigation and recovery.

Each stream is authenticated and scoped using the same Account, Event Grant, or Display identity as its snapshot endpoint.
Events carry a monotonic stream position and the authoritative entity or channel revision needed to reject stale application.
They contain bounded state or invalidation data rather than becoming an independent event store.

A client obtains a complete snapshot before consuming live updates.
On reconnection, unknown stream position, detected sequence gap, Active Event change, or incompatible client state, it obtains another snapshot before resuming.
Correctness never depends on retaining every SSE notification.

Heartbeat comments keep intermediaries and clients aware of connection health.
The server bounds each subscriber's queue and disconnects slow consumers rather than blocking durable commands; those clients recover through snapshots.
Connection, lag, gap, resnapshot, and disconnect outcomes are observable.

SSE is a delivery choice rather than a domain constraint.
A later renderer or deployment may adopt direct WebSocket delivery without changing snapshot, revision, authorization, or acknowledgment semantics.
NATS remains a possible future outbox and integration transport, not a version-one browser dependency.

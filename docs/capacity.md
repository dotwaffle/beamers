# Capacity verification

Beamers tests the version-one envelope on Linux AMD64 with at least four CPUs, 8 GB of memory, and non-rotational local storage.
The test performs real SQLite commits and refuses to certify tmpfs or rotating storage.
The GitHub-hosted reference runner reports its documented SSD-backed ext4 root as the synthetic `/dev/root`; self-hosted runners must expose inspectable device metadata.

The `Capacity` workflow is manual because it is a sustained release gate, not a per-commit unit test:

```sh
gh workflow run Capacity --ref main -f duration=10m
```

Use at least ten minutes for release evidence.
The workflow uploads `capacity.json` with the runner facts, workload counts, sample counts, and latency percentiles.

The fixture contains:

- one Active Event;
- 64 Locations and 64 Lanes;
- one live Presentation and one Competition with 24,998 Included Entries;
- 500 persistent Display event streams;
- bursts from 200 concurrent Crew consoles;
- 10,000 public readers behind one coalescing 12-second cache.

Each live command commits through the Connect API.
Every Display receives the SSE invalidation, fetches and decodes the committed Snapshot, then acknowledges it asynchronously, matching browser order.
The proxy refreshes the public Schedule from its ETag and proves that readers observe changes within the polling interval.

The gate requires:

- live-command durable acknowledgment at or below 250 milliseconds p95;
- Display application at or below 500 milliseconds p95 and one second p99;
- Stage Timer skew at or below 250 milliseconds;
- public freshness within 15 seconds.

For a non-certifying local correctness run:

```sh
BEAMERS_CAPACITY_SOAK=1 \
BEAMERS_CAPACITY_DURATION=1m \
go test ./internal/server -run '^TestCapacityEnvelope$' -count=1 -timeout 15m
```

Local timing is evidence only when `BEAMERS_REFERENCE_HARDWARE=1` also passes the hardware gate.

`GET /diagnostics` and authenticated `GET /admin/diagnostics` expose bounded capacity counts.
Crossing the tested Location or Lane, Session plus Entry, or Display count changes capacity status to `warning`; Beamers still commits valid data.
Crew and downstream public-reader concurrency are delivery-topology measurements verified by the workload, not installation counts the origin can infer through a coalescing cache.

# Capacity verification

Beamers certifies representative and rated Event profiles.
It retains a larger stress profile to expose scaling failures without promising the rated latency objective.

## Workload profiles

The named profiles are reproducible planning models, not claims about the exact infrastructure or staffing of the example Events.
[NetUK3](https://www.netuk.org/netuk3) and its [timetable](https://indico.netuk.org/event/3/timetable/) calibrate a typical conference.
[Nova 2026](https://2026.novaparty.org/) calibrates a typical demoparty.
The [FOSDEM 2026 schedule](https://fosdem.org/2026/schedule/) calibrates a large conference, while [Revision 2026 results](https://www.pouet.net/party_results.php?which=1550&when=2026) help calibrate a large competition-oriented Event.

| Profile | Locations | Concurrent Lanes | Sessions + Entries | Displays | Active Crew | Downstream public readers |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| NetUK typical conference | 6 | 3 | 75 | 20 | 10 | 500 |
| Nova typical demoparty | 4 | 2 | 250 | 15 | 5 | 250 |
| FOSDEM large conference | 40 | 40 | 1,500 | 150 | 75 | 10,000 |
| Revision large demoparty | 4 | 4 | 750 | 30 | 40 | 2,000 |
| Rated ceiling | 64 | 64 | 5,000 | 250 | 100 | 10,000 |
| Diagnostic stress | 64 | 64 | 25,000 | 500 | 200 | 10,000 |

Public-reader counts describe clients downstream of a coalescing conditional cache.
They are not 10,000 origin connections.
Display counts describe connected protocol clients that receive and decode committed Snapshots, not 500 Chromium rendering processes.

Representative profiles and the rated ceiling carry the latency objective.
The diagnostic stress profile must preserve correctness, progress, and bounded resource use while reporting the same measurements, but its latency is diagnostic.

## Certification topology

The reference server is Linux AMD64 with four assigned CPU cores, 8 GB RAM, and durable local SSD storage with working `fsync`.
The workload generator runs on a separate machine so request generation, public caching, and Display clients do not consume the server's assigned CPUs.
The run uses fixed-rate arrivals with jitter after warmup rather than a response-dependent synchronized loop.

Each rated run samples at least 200 independent live commands.
Percentiles use the nearest-rank method.
Each command first produces p50, p95, p99, and maximum Display fanout latency; those per-command results are then summarized across commands rather than pooling every command-Display observation.
The commit-to boundary uses the server's command decision timestamp immediately before the durable transaction, conservatively including commit time rather than understating delivery latency.

The rated objectives are:

| Measurement | p50 | p95 | p99 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Durable live-command acknowledgment | 100 ms | 250 ms | 500 ms | 2 s |
| Commit to decoded Display Snapshot | 250 ms | 500 ms | 1 s | 2 s |
| Operator command to decoded Display Snapshot | — | 750 ms | — | — |
| Public freshness after commit | 7 s | 13 s | — | 15 s |

Online Stage Timer skew must remain at or below 250 milliseconds.
Public freshness timing begins after the mutation commits.

### Run a certification

Use an isolated server and generator with synchronized clocks.
The server listens on loopback; expose it to the generator through an SSH tunnel so the temporary credentials never cross plaintext networking.

On the server:

```sh
umask 077
BEAMERS_CAPACITY_SOAK=1 \
BEAMERS_CAPACITY_ROLE=server \
BEAMERS_CAPACITY_CERTIFY=1 \
BEAMERS_CAPACITY_PROFILE=rated \
BEAMERS_CAPACITY_DURATION=10m \
BEAMERS_CAPACITY_LISTEN=127.0.0.1:8080 \
BEAMERS_CAPACITY_ORIGIN=http://127.0.0.1:8080 \
BEAMERS_CAPACITY_TARGET=/secure/path/capacity-target.json \
go test ./internal/server -run '^TestCapacityEnvelope$' -count=1 -timeout 20m -v
```

While it waits, securely copy `capacity-target.json` to the generator with mode `0600` and start an SSH tunnel from generator port 8080 to server port 8080.
Then run on the generator:

```sh
BEAMERS_CAPACITY_SOAK=1 \
BEAMERS_CAPACITY_ROLE=generator \
BEAMERS_CAPACITY_CERTIFY=1 \
BEAMERS_CAPACITY_PROFILE=rated \
BEAMERS_CAPACITY_DURATION=10m \
BEAMERS_CAPACITY_TARGET=./capacity-target.json \
BEAMERS_CAPACITY_REPORT=./capacity.json \
go test ./internal/server -run '^TestCapacityEnvelope$' -count=1 -timeout 20m -v
```

Repeat for `netuk`, `nova`, `fosdem`, and `revision`.
The generator signals the server to stop after collecting server resource maxima.
Delete both copies of `capacity-target.json` after the run because they contain temporary session and Display credentials.
Certification fails on a shared hostname, a GitHub-hosted generator, a diagnostic stress profile, non-reference server hardware, fewer than 200 live commands, or any latency objective.

## GitHub-hosted diagnostics

The manual `Capacity` workflow is a sustained correctness and regression diagnostic:

```sh
gh workflow run Capacity --ref main -f duration=10m
```

Use at least ten minutes when collecting diagnostic evidence.
The workflow uploads `capacity.json` with the runner facts, workload counts, sample counts, and latency percentiles.
It does not certify the rated envelope because the shared hosted runner colocates the server, clients, cache, and load generator.

For a non-certifying local correctness run:

```sh
BEAMERS_CAPACITY_SOAK=1 \
BEAMERS_CAPACITY_ROLE=combined \
BEAMERS_CAPACITY_PROFILE=netuk \
BEAMERS_CAPACITY_DURATION=1m \
go test ./internal/server -run '^TestCapacityEnvelope$' -count=1 -timeout 15m
```

`GET /diagnostics` and authenticated `GET /admin/diagnostics` expose bounded capacity counts.
Crossing the tested Location or Lane, Session plus Entry, or Display count changes capacity status to `warning`; Beamers still commits valid data.
Crew and downstream public-reader concurrency are delivery-topology measurements verified by the workload, not installation counts the origin can infer through a coalescing cache.

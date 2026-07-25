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

Certification runs on GitHub's standard `ubuntu-24.04` hosted runners.
Each profile records the runner environment, name, operating system, architecture, workflow run, commit, CPU count and model, memory, filesystem, storage source, and storage type so results retain their provenance.
Server and workload generator share the runner; the generator still uses fixed-rate arrivals with jitter after warmup rather than a response-dependent synchronized loop.

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

The manual `Capacity` workflow certifies every representative and rated profile and runs the diagnostic stress profile:

```sh
gh workflow run Capacity --ref main -f profile=all
```

The default dispatch certifies only the rated profile.
With `profile=all`, the rated profile runs first; only a pass starts the other representative profiles and stress in parallel.
Certification fails when a representative or rated run is shorter than five minutes, collects fewer than 200 live commands, or misses a latency objective.
The stress profile remains diagnostic and cannot certify.
It fails if peak process memory exceeds 80 percent of runner memory or if goroutine or open-file maxima exceed the workload-derived bounds.
The workflow requires one `capacity.json` artifact per profile with runner facts, workload counts, sample counts, resource maxima, and latency percentiles.
Pass `-f profile=stress` or another profile name to rerun only one failed profile.

## Local diagnostics

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

# Calibrate capacity around Event profiles

ADR 0031 treated an intentionally extreme workload as the ordinary capacity target and applied one latency contract to every scale.
Beamers instead certifies representative and rated Event profiles, while retaining the former envelope as a diagnostic stress profile.
This supersedes ADR 0031.

The representative profiles are calibrated against typical and large conference and demoparty shapes.
The rated ceiling is one Active Event with 64 Locations, 64 concurrent Lanes, 5,000 combined Sessions and Entries, 250 connected Displays, 100 active Crew consoles, and 10,000 downstream public readers using cacheable snapshots and bounded revisioned SSE invalidation.
The stress profile retains 25,000 records, 500 Displays, and 200 Crew consoles to expose scaling failures, but it does not carry the rated latency objective.
Counts remain tested targets rather than hard configuration limits.

Certification uses standard GitHub-hosted Linux AMD64 runners and records exact runner, workflow, CPU, memory, filesystem, and storage provenance.
The colocated generator produces fixed-rate load with jitter after warmup.
The diagnostic stress profile applies explicit process-memory, goroutine, and open-file bounds derived from the runner and workload.

Representative and rated runs sample at least 200 independent live commands and report nearest-rank p50, p95, p99, and maximum latency.
Display delivery is measured both from commit to decoded Snapshot and from operator command to decoded Snapshot.
Each command reports its Display fanout distribution before results are summarized across commands, preventing one slow whole-fleet update from disappearing inside pooled samples.
Protocol clients provide bulk load; a real Chromium kiosk remains separate release evidence under ADR 0049.
The public-reader profile holds its SSE connections open and resnapshots a representative subset after invalidation.

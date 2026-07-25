# Calibrate capacity around Event profiles

ADR 0031 treated an intentionally extreme workload as the ordinary capacity target and applied one latency contract to every scale.
Beamers instead certifies representative and rated Event profiles, while retaining the former envelope as a diagnostic stress profile.
This supersedes ADR 0031.

The representative profiles are calibrated against typical and large conference and demoparty shapes.
The rated ceiling is one Active Event with 64 Locations, 64 concurrent Lanes, 5,000 combined Sessions and Entries, 250 connected Displays, 100 active Crew consoles, and 10,000 downstream public readers behind a coalescing cache.
The stress profile retains 25,000 records, 500 Displays, and 200 Crew consoles to expose scaling failures, but it does not carry the rated latency objective.
Counts remain tested targets rather than hard configuration limits.

Certification uses a dedicated Linux AMD64 server with four assigned CPU cores, 8 GB RAM, and durable local SSD storage.
A separate machine generates fixed-rate load with jitter after warmup.
GitHub-hosted runs remain useful correctness and regression diagnostics, but shared runner scheduling and a colocated load generator cannot certify capacity.

Representative and rated runs sample at least 200 independent live commands and report nearest-rank p50, p95, p99, and maximum latency.
Display delivery is measured both from commit to decoded Snapshot and from operator command to decoded Snapshot.
Each command reports its Display fanout distribution before results are summarized across commands, preventing one slow whole-fleet update from disappearing inside pooled samples.
Protocol clients provide bulk load; a real Chromium kiosk remains separate release evidence under ADR 0049.

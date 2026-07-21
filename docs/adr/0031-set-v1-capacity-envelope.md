# Set the Version-One Capacity Envelope

Version one is designed and load-tested for one Active Event with up to 64 concurrent Lanes or Locations, 500 connected Displays, 200 concurrent Crew consoles, 25,000 combined Sessions and Entries, and 10,000 public readers using cacheable polling.
These are tested targets rather than configured hard limits.

The Session and Entry target intentionally exceeds expected real Events and provides headroom for imports and long-lived installations.
Public-reader scale assumes conditional responses and ordinary proxy or browser caching rather than rendering every poll from SQLite.
Attachment capacity is governed separately by available storage and configured quotas rather than by the record-count envelope.

An installation operating beyond the tested envelope receives an operational warning rather than losing data or refusing configuration solely because a count was exceeded.

On documented reference hardware at that envelope, live commands target a durable server acknowledgment within 250 milliseconds at the 95th percentile.
Connected Displays target application of committed output within 500 milliseconds at the 95th percentile and one second at the 99th percentile.
Online Stage Timer skew from server time targets at most 250 milliseconds.
Public freshness remains bounded by the configured polling interval.

Reference server hardware is Linux on x86-64 with at least four CPU cores, 8 GB of RAM, and durable local storage with working `fsync`; SSD is the recommended performance baseline.
Raspberry Pi devices are Display or operator console candidates, not authoritative-server reference hardware.
Local development can validate correctness on the available four-disk ZFS RAID-Z of hard disks, but that environment alone does not certify the SSD-based latency targets.

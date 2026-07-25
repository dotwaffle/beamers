# Gate releases on system behavior

Version-one releases require deterministic domain tests covering timing, daylight-saving transitions, ripple, Competition, Results, and Override rules; real SQLite integration tests covering migrations, privacy, idempotency, backup, and restore; and browser-level tests carrying Crew commands through a durable server commit to multiple Displays.

Fault tests cover restart, disconnect, stale clients, storage failure, and forced mid-Event upgrade.
The documented representative and rated capacity profiles receive sustained certification on standard GitHub-hosted runners with exact runner provenance.
The reference Chromium kiosk receives a separate real-hardware smoke test that may be recorded manually or on a self-hosted runner.
Accessibility combines automated checks with representative manual keyboard, screen-reader, touch, zoom, contrast, and reduced-motion review as required by ADR 0032.

A failure on a critical version-one path blocks release.
Optional integrations block only when included in that release; in particular, Chromecast acceptance is required only if the stretch receiver ships.

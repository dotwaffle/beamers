# Beamers agent instructions

## Agent skills

### Issue tracker

Issues and PRDs are tracked in GitHub Issues.
See `docs/agents/issue-tracker.md`.

### Triage labels

Use the canonical `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, and `wontfix` labels.
See `docs/agents/triage-labels.md`.

### Domain docs

This repo uses a single-context domain documentation layout.
See `docs/agents/domain.md`.

### Pre-v1 schema changes

Until the first v1 release, regenerate `internal/store/migrations/0001_baseline.sql` for every schema change; do not add `0002`.
See ADR 0034.

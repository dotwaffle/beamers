# Beamers design

## Status and authority

This document records the shared product and system design for Beamers version one.
It is the overview from which implementation planning may proceed.

The [domain glossary](../CONTEXT.md) defines the project's accepted language.
The [architecture decision records](#decision-index) preserve the detailed decisions and their rationale.
If this overview is ambiguous, use the glossary for terminology and the relevant ADR for behavior.

## Product overview

Beamers is a venue-local application for operating a live Event and communicating its actual state.
It owns the authoritative Rundown after optional one-way import, rather than merely displaying a timetable owned elsewhere.

Crew Members use it to publish a Schedule, progress Sessions, adjust live timing, control stage and Competition output, issue temporary Overrides, collect scoped file submissions, and release reviewed results.
Attendees see current and upcoming information on venue Displays and a public read-only Schedule.
Speakers receive a Stage Timer and discreet Stage Messages.

The central promise is that every surface reflects what the crew says is happening now.
A Session does not become Live or Ended merely because its Planned Time has passed.
An authorized Crew Member normally makes that transition explicitly, and the committed state then reaches all relevant surfaces.

The service remains useful on an isolated venue network without internet access.
One venue-local process and SQLite database are the live authority for one Active Event.
Displays render locally in browsers and recover automatically from interrupted connectivity.

## Version-one outcomes

Version one must provide:

- An authoritative multi-Lane Rundown with Draft, selective Publish, and CSV or iCalendar import.
- Public and Crew-only Schedule views whose current state follows live operation.
- Explicit Session start, timing adjustment, End Now, cancellation, reinstatement, and optional Pull Forward controls.
- Event Overview, Location Signage, Stage Timer, Competition Output, and Standby Views.
- Inputless Display Enrollment, Event-specific Assignment, grouped targeting, and automatic recovery.
- Emergency Alert, Urgent Notice, Stage Message, and Technical Difficulties Overrides.
- Competition submission, review, ordering, Preview, Take, replay, deferral, and resolution workflows.
- Scoped Presentation and Entry upload links with immutable Attachment Versions, Final and Primary selection, release controls, and deadlines.
- Crew-managed Competition results, Awards, Prizegiving sequences, deterministic reveal methods, and public HTML, `results.txt`, and JSON publication.
- Named Crew Accounts, Event-scoped roles and capabilities, durable command receipts, and lifetime Audit Entries.
- Backup, restore, migration, diagnostics, telemetry, and tested shutdown and upgrade behavior.

### Explicitly deferred

Version one does not include:

- Attendee Accounts, admission vote keys, voting, automated tallying, or Audience Choice derivation.
- Automatic Session progression as the default; optional per-Session start or end automation is future work.
- Bidirectional schedule synchronization or dedicated pretalx and frab integrations.
- Generic file sharing, a WebDAV or copyparty-facing download Interface, or external write access to canonical Attachments.
- A visual Layout or slide-template editor, arbitrary HTML or CSS Themes, portrait-specific Layouts, or arbitrary aspect-ratio guarantees.
- A Location Signage Live Feed, centrally rendered broadcast video output, or external recording, streaming, lighting, and webhook integrations.
- GraphQL, Datastar, NATS, or WebSocket delivery without a concrete consumer that justifies them.
- Automatic failover, multiple active services, automatic replica promotion, or an authoritative read replica.
- Automatic update discovery or installation.
- A supported Linux ARM64 server build, a tested Kubernetes deployment profile, or a supported Fly.io profile.

A dedicated Chromecast receiver is a version-one stretch goal.
Its absence does not block version-one acceptance, but if it ships it must pass its release tests.

## Domain model

### Event structure

An Event has one authoritative Rundown.
The Rundown contains one or more Lanes, each bound to exactly one Location and able to progress independently.
A Track is only a thematic grouping and never controls timing.

An installation may store past, current, and future Events but designates exactly one Active Event for live commands and Display output.
Activation Preflight blocks invalid Published structure and warns about operational incompleteness.
Display Assignment remains Event-specific and is never silently inherited from the previously Active Event.

A Session is the unit of planned live content.
Initial Session types include Presentation, Competition, Break, Activity, Ceremony, Performance, and Hold.
A Session may belong to multiple Lanes as one Shared Session, allowing lunch, ceremonies, or other synchronized content to span Locations without creating divergent copies.

A Slot is a planned time envelope, not the content placed within it.
The attendee-facing projection of relevant Rundown information is always called the Schedule.

### Independent Session state axes

Session state is deliberately not one overloaded status:

| Axis | Values | Purpose |
| --- | --- | --- |
| Structural | Draft or Published | Whether planned structure is authoritative for live operation. |
| Audience Visibility | Public or Crew Only | Whether attendee-facing surfaces may disclose the Session. |
| Lifecycle | Scheduled, Live, Ended, or Canceled | What is happening operationally. |

Published Sessions are retained rather than deleted.
A never-Published, unreferenced Draft Session may be deleted.
A Canceled Session can later be reinstated and rescheduled without losing metadata or prior Session Runs.

Each execution attempt creates a Session Run.
Its Run Snapshot preserves the exact published context used at start, while immutable Run Amendments can correct descriptive details without rewriting history.

### Time model

Every Session can have four distinct time layers:

- Planned Time records the original intended start and end.
- Forecast Time records the current operational expectation after adjustments.
- Communicated Time records the last Forecast Time shown publicly.
- Actual Time records when a Session Run really started and ended.

Crew surfaces may show all relevant layers.
Public surfaces avoid distracting precision: for a Session planned to last more than ten minutes, an Actual start or end within two minutes of its Communicated Time is presented as on time.

Each Event has one IANA timezone, an Event Locale, and a configurable Event Day Boundary.
Ambiguous daylight-saving local times explicitly select the earlier or later occurrence.
Nonexistent times and Event Day Boundaries resolve according to the documented timezone rules.
Elapsed durations and live countdowns use instants and monotonic elapsed time rather than wall-clock subtraction.

### Timing and ripple

Each Session independently selects a Timing Policy:

- Fixed End targets one resolved instant.
- Fixed Duration adds an elapsed duration to Actual Start.
- Manual End has no countdown target and shows elapsed live time.

Start and end boundaries are independently Hard or Soft.
Automatic recalculation may move or compress only Soft Boundaries and never below a Session's Minimum Duration.
The default Minimum Duration equals planned duration, so compression is opt-in.

An Adjust Target command previews all downstream effects before moving a live target.
It accepts Event-configured adjustment presets, such as ten minutes, or a custom value in either direction.
Crossing a Hard Boundary requires a prominent warning and explicit confirmation.
Reaching a target does not end the Session; the Stage Timer instead shows positive overtime.

End Now records the actual end without moving later Sessions.
Pull Forward is a separate, explicit action that may move eligible later Soft Boundaries earlier.
This preserves breaks or Shared Sessions that must resume across all Lanes at a common fixed time.

### Competition and Entry model

A Competition is a Session containing ordered Entries.
Its fixed Submission Deadline closes new Entries and updates regardless of later Forecast changes.
Crew may open a bounded, audited Reopen Window for one existing Entry.

Entry Disposition is Pending, Included, or Rejected.
The Event decides whether new Entries default to Pending or Included.
Optional Entry Review is separate from disposition and, when required, blocks Competition start until every participating Entry's current contents have been reviewed.
It is disabled by default.
Unresolved Pending Entries also block Competition start.

Included Entries are ordered by submission order, manual order, or deterministic shuffle.
The order locks when the first Entry Slide is Taken.
Competition control always shows Previous, Current, and Next slides plus current Program Output before an operator advances.
An operator may select and replay any specific slide without changing the canonical cursor.

Defer Entry moves an Entry to a retry queue without rewriting locked order.
Ending with deferred Entries requires confirmation and marks them Not Presented with Resolution Required.
Prizegiving and relevant public release remain blocked until each is resolved, such as public inclusion, Technical Failure handling, Disqualification, or Withholding.
Technical Failure does not by itself decide judging eligibility, public visibility, or Attachment release.

Rejection excludes an Entry from presentation.
Disqualification may occur after presentation, preserves true history, and is publicly visible unless a separate accepted outcome applies.
Withholding is available for a Not Presented Entry whose existence and files should remain private, such as a technical failure that may be resubmitted elsewhere.

### Attachments

Beamers owns canonical Attachment files, metadata, visibility, and integrity.
Replacing a file creates an immutable Attachment Version.

Scoped, revocable Upload Links permit uploads to one Presentation or Entry without creating an attendee Account.
A sole upload becomes Primary automatically.
Preflight also marks it Final by default unless the Event's review policy requires crew approval.

File Delivery Required defaults on for Competitions and off for Presentations.
When enabled, it blocks Start until every participating Entry, or the Presentation itself, has a Final Primary Attachment.
A Presentation Upload Link closes at Actual Start unless a Producer configured an earlier fixed Upload Deadline.

Multiple Attachments may each have a Final Version, but only one Attachment Version is Primary for an Entry or Presentation.
The Primary is selected by crew as the default operational file.
Uploaders are told to package artifacts into one archive when they must be delivered together.

Final Versions default to Public release eligibility.
An uploader must make a deliberate choice to keep one Crew Only, but cannot choose release timing.
Event and Competition policy releases eligible files On Live, On Ended, or on an Event Release Cue.
A Producer may apply a Release Hold independently of the uploader's eligibility choice.

### Results and Prizegiving

Each Competition has a versioned Results Draft and a Results Disposition of Pending, Publish, or No Public Results.
A Producer marks one exact revision Ready.
Any relevant change clears Ready and requires review again.

Eligible Entries receive an explicit Placed or Unplaced Result Standing.
Placements, including ties, are authoritative and are never inferred from numeric scores.
Scores may be Decimal or Duration, Public or Crew Only, Optional or Required, and Higher Wins, Lower Wins, or Informational.
Competition and Event Awards are modeled independently of Placement.

A Ceremony Session may be designated a Prizegiving.
Its explicit Results Sequence can contain Competition results, No Public Results statuses, Competition Awards promoted to separate reveals, and Event Awards.
Preflight locks the reviewed source revisions, sequence, publication order, reveal seeds, and Results Text Template revision.
An unassigned Competition can instead use a Producer-triggered Standalone Results Release after the same readiness and resolution checks.

Program Output can reveal results through Static Result, Sequential Podium, or Animated Score Bars.
Every Reveal Method reaches the same immutable result, supports Skip to Final, and has a deterministic reduced-motion fallback.
Random-looking methods store a seed so preview, rehearsal, replay, reconnect, and restart are reproducible.

Take first places an unrevealed Results Slide in Program Output.
A separate Reveal action reaches the immutable final state and, under Progressive on Reveal, publishes it.
Skip from Stage deliberately omits a Result Item from Program Output but queues its ordinary publication for ceremony completion.
Ending a Prizegiving is blocked until every Result Item has either been revealed or explicitly skipped.

Results can publish All at Cue, Progressive on Reveal, or At Ceremony End.
Progressive on Reveal is the default.
Results release and Attachment Release Policy remain independent.
The public Results Publication is produced from one immutable revision as HTML, event-level UTF-8 `results.txt`, and versioned JSON.
The application supplies a safe default text template and permits previewed Event customization, including ASCII-art decoration.
Templates use Go `text/template` against a documented immutable view model and have no filesystem, network, command, or application-service access.

Public release is monotonic.
Replaying or navigating backward cannot retract a reveal or release.
Post-release changes use a reviewed Results Correction and retain prior revisions.

## User surfaces and live operation

### Planning and publishing

CSV and iCalendar imports create or update Draft proposals only.
Import References aid duplicate detection but never become canonical Session identity or authorize an automatic overwrite.

Multiple Crew Members may edit shared Draft state using expected Draft Revisions.
A Publish Preview binds an exact selection and displays its dependencies and impact.
Publish commits that valid selection atomically; blocked or conflicting work remains in Draft.

Live commands bypass Draft because they describe current reality.
They remain revision-checked, idempotent, durable, and audited.

### Public Schedule

The responsive, unauthenticated public Schedule shows current and upcoming Public Sessions, Forecast Times, Locations, Lanes, and Tracks.
Day, Location, Lane, and Track filters use shareable URLs rather than Accounts or server-side preferences.
Each Public Session has a stable deep link across renaming, retiming, cancellation, and reinstatement.
A Crew Only or unknown Session returns the same generic not-found response.

The Schedule defaults to Event time and may offer attendee-local conversion without changing Event-day grouping.
It is always available on the venue network and may be exposed externally as a deployment choice.
Cacheable conditional polling about every 15 seconds keeps it current without placing public readers on the immediate live stream.

### Displays and Views

A Display is an enrolled screen identity.
Enrollment persists across Events, while Assignment binds that Display to its Active Event Location and normal View.
Inputless devices may show a QR code that an authorized Crew Member scans to complete Enrollment and Assignment; manual and pre-provisioned methods remain available.

A View is composed from a Layout of named Regions and Widgets.
A Rotation Widget cycles through timed View Pages while persistent Widgets remain visible.
The built-in catalog is:

- Event Overview for multi-Lane Schedule and rotating Event information.
- Location Signage with roughly 70 percent rotating content plus persistent Location, Now/Next, and digital clock.
- Stage Timer with live target, countdown or elapsed time, overtime, Emphasis, adjustment notices, and Stage Messages.
- Competition Output for Competition and Results Program Output.
- Standby for an enrolled but unassigned or idle Display.

Location Signage does not disclose a Crew Only Session; it shows only that the Location is unavailable and until when.
Version one guarantees landscape 16:9 Layouts from 720p through 4K and safe degradation on 16:10.

Displays run in borderless browser or kiosk mode, with Chromium as the reference implementation.
They render time locally from server synchronization, retain their last committed frame when disconnected, and show a slowly pulsing connection indicator to distinguish disconnection from a frozen renderer.
Reconnection obtains a complete authoritative snapshot before resuming live updates.
Crew and control Views certify the current and previous major Chromium and Firefox releases.
The public Schedule and phone-based Enrollment additionally certify current Safari.
All Crew controls support touch and keyboard.
Go WebAssembly and WASI are not version-one dependencies, but the Display state protocol remains renderer-neutral for later experiments or native receivers.

### Program control

A Program Channel is a logical live presentation channel, not necessarily a video signal.
It has one Control Owner at a time and feeds one or more Views or Displays.
Version-one Program Items are Competition Slides, Results Slides, and Standby; Beamers is not a generic slide switcher.

Preview is crew-only.
Take durably commits Preview as Program Output before notifying Displays and does not wait for every Display acknowledgment.
Control surfaces show committed output and individual Display application status separately.

Commands carry an expected Live State Revision and stable command identity.
Stale commands are rejected with the current state, and an exact retry returns its original result rather than applying twice.
Control takeover is explicit and audited.

### Overrides

Overrides temporarily supersede normal Views without changing the Rundown or Session state.
They target logical entities such as Displays, Display Groups, Locations, Views, or Program Channels, resolving to current consuming Displays.

The priority model is:

1. Emergency Alert: persistent, fullscreen, and safety-critical.
2. Urgent Notice: prompt operational information, as Replace or Overlay, optionally expiring.
3. Stage Message: one-way, non-queued crew Overlay with presets, emphasis, and expiry.
4. Technical Difficulties: a fullscreen wait message that does not alter timing.

Replace suppresses lower-priority content; Overlay composes over it.
Clearing an Override restores the underlying current View rather than a stale copy.
A fullscreen Replace Override covering every consumer pauses an active timed Result Reveal and its progressive publication until coverage clears.

## System architecture

Beamers is a modular monolith.
One Go process owns domain rules, rendering, transport Interfaces, live distribution, and persistence so live transitions can remain transactional and easy to operate at a venue.

```text
Public browsers   Crew consoles   Displays   Offline CLI
       |                |             |           |
       +------ HTML / Connect / SSE / commands ---+
                            |
                    Transport adapters
                            |
              Command module / Query modules
                    |               |
             Domain transitions     +----> projections and snapshots
                    |
                  Ent store
                    |
       SQLite + Attachment ownership + Audit Entries
                    |
           post-commit notifications ----> SSE clients
```

### Module responsibilities

| Module | Responsibility and Interface |
| --- | --- |
| Transport adapters | Translate HTTP, ConnectRPC, SSE, and CLI inputs and outputs without owning domain transitions. |
| Command module | Provide the deep write Interface: establish identity, verify revisions and idempotency, apply rules, transact state and evidence, then notify. |
| Domain transitions | Compute deterministic timing, ripple, Competition, Results, and Override decisions from explicit state, command values, and time without I/O. |
| Query modules | Produce purpose-specific public Schedule, Crew, control, Results, and Display projections instead of generic CRUD data. |
| Identity and policy | Establish the viewer and enforce Event Grants, scopes, and capabilities, with Ent privacy as the final authorization decision. |
| Ent store | Contain generated entities, transactions, migrations, and direct database access; Ent types do not cross this seam. |
| Rendering | Produce complete and partial HTML with templ and render live Display behavior with small, purpose-built JavaScript modules. |
| Live distribution | Send bounded, revisioned SSE notifications and heartbeats; complete state always comes from an authorized snapshot. |
| Operations | Provide backup, restore, migration, recovery, optional replication, health, diagnostics, and telemetry adapters. |

There is no generic repository Interface solely for mocking.
The concrete Ent store is tested against temporary real SQLite databases.
A clock is an explicit seam, allowing pure domain tests to control time and daylight-saving transitions.

### Web and transport choices

Server-owned HTML uses templ.
Htmx adds modest form, filtering, navigation, validation, and partial-replacement behavior while direct routes remain useful complete pages.
Small JavaScript modules handle clocks, Display rendering, presentation effects, and recovery behavior that is not a natural HTML exchange.

Programmatic commands and queries use versioned Protocol Buffer contracts over ConnectRPC, normally using Connect's JSON encoding in browsers.
Server-rendered handlers call application modules directly rather than making loopback RPCs.
Version one begins with unary RPCs.

SSE carries authenticated, revisioned change notifications and heartbeats to Displays and Crew consoles.
Connect provides complete snapshots, commands, and Display acknowledgments.
A client resnapshots after reconnect, a sequence gap, an Active Event change, or incompatible state.
Slow subscribers are disconnected and recover from a snapshot rather than blocking commands.

The unauthenticated public Schedule uses cacheable conditional polling about every 15 seconds.
Immediate push is reserved for Displays and Crew consoles in version one.

Pinned web assets are embedded in the Go binary.
Production rendering does not depend on a CDN, Node toolchain, or internet connection.

## Data, consistency, and history

SQLite is the authoritative store.
Ent defines the persistence schema and generated access code, using `modernc.org/sqlite` so the supported server build remains CGo-free.
Committed versioned migrations, not automatic schema creation, upgrade existing databases.

Every state-changing command follows one transaction pipeline:

1. Establish actor and stable command identity.
2. Check authorization, expected revision, and prior Command Receipt.
3. Load the required authoritative state.
4. Apply deterministic domain rules.
5. Commit resulting state, Command Receipt, Audit Entry, and any outbox record atomically.
6. Notify live consumers only after commit.

Audit Entries are append-only at the application level and retained for the installation lifetime.
They identify actors, targets, actions, and outcomes but exclude credentials, secrets, and Attachment contents.
Disabled Accounts retain stable historical identity.

SSE is not an event store and clients are not authorities.
Correctness depends on durable revisions and snapshots, not delivery of every notification.

## Identity, authorization, and network security

Every Crew Member uses an individual Account.
Version one supports regular credential-based Accounts; passkeys and hardware authenticators are future additions.

Administrator is installation-wide and does not imply Event access.
Producer, Operator, and Observer are Event roles granted through Event Grants.
Capabilities such as unreleased Results access, Override control, Lane control, Display Group control, and high-impact actions can be scoped separately.

Ent privacy policies are the final authorization boundary for operational entities.
Handlers may repeat checks for clearer errors but cannot grant access denied by policy.
Privacy bypass is confined to narrow migration, bootstrap, and internal system paths and is inaccessible from request-derived contexts.

Crew origins require TLS, either directly or through an explicitly trusted reverse proxy.
Any insecure venue-LAN mode is explicit and prominently warned.
Public and private listeners may be separated.
Same-origin protections, request-size limits, deadlines, rate limits, secure cookies, and strict Content Security Policy apply independently of transport choice.

Public Schedule and published Results are unauthenticated.
Crew Only resources return a generic not-found response rather than confirming existence.
Upload Links are unguessable scoped credentials, not Accounts.

## Resilience and recovery

### Display continuity

Displays retain their last committed frame across server restarts, upgrades, transport interruption, and recoverable rendering failures.
They reconnect with bounded backoff and jitter, verify protocol compatibility, resnapshot, and reload content-addressed assets automatically when required.
Ordinary recovery never requires a manual refresh.

Completed Results Reveals restore directly to final state after reconnect or restart.
A still-running deterministic Reveal may resume from its server-timestamped position or restart its animation, but neither behavior can change released state.

### Storage failure

If SQLite becomes unavailable after startup, ordinary mutations fail closed and Displays retain last committed state.
As one narrow safety exception, a previously authorized Crew Member may issue or clear an explicitly nondurable in-memory Emergency Alert for currently connected Displays.
The control surface labels this degraded path prominently, and the service attempts to persist its evidence if storage recovers.

Unsafe startup storage enters local-only recovery mode.
No Crew, public, Display, or live-control Interface is served.
The `beamers` executable uses host operating-system authority to validate and explicitly install a Restore while preserving suspect state in a non-overwriting quarantine location.
Beamers never silently creates over, replaces, restores, or promotes data.
Healthy administrative maintenance and host recovery are adapters over the same Backup and Restore operations.

### Backup and replication

Versioned conventional archives contain a database snapshot, every referenced immutable Attachment Version, compatibility metadata, and integrity hashes.
The Attachment Store defaults inside the data directory but may use a separately configured local root.
Backups can exclude authentication secrets while retaining Account names, roles, and related structure; this Sanitized Backup is the default.
Beamers does not encrypt backups, so deployments apply external encryption and access control where required.
Full-Fidelity Backup requires recent Administrator re-authentication and explicit acknowledgment of its external protection requirements.
Restore validates integrity and compatibility, stages both database and Attachments, and uses a durable journal to coordinate non-overwriting cross-filesystem cutover.
A Sanitized Restore requires Administrator re-bootstrap and Display re-Enrollment.
Final Files Export is a separate reproducible public/archive projection and is never authoritative recovery material.

Optional embedded Litestream replication is disabled by default and isolated behind an adapter.
It provides asynchronous full-fidelity disaster-recovery copies, not command durability, sanitized backup, automatic failover, or a second authority.
Its copies include authentication secrets and therefore require appropriate external storage protection.

### Health and shutdown

The unauthenticated liveness probe reports only that the process responds and remains healthy during recoverable storage faults.
Readiness requires a usable authoritative database and supported schema.
The absence of an Active Event is an operational state, not a readiness failure.
Detailed storage, replication, backup, telemetry, and stream diagnostics require administrative authentication or local access.

Shutdown uses the hosting platform's declared grace budget.
Readiness becomes false immediately.
An idle service skips traffic drain; otherwise a 30-second profile may continue accepting commands for up to 10 seconds while steering converges, then rejects mutations and drains streams and requests.
The final 10 seconds are reserved for bounded Litestream and telemetry flushes and storage closure.
The default 10-second Docker profile begins final shutdown immediately.

## Observability and dependency resilience

One structured `slog.Logger` writes authoritatively to standard error.
When configured, `otelslog` also exports the same records over OTLP so logs correlate with OpenTelemetry traces and metrics.
Telemetry is optional, bounded, and never blocks live control.

Connect instrumentation uses `otelconnect` with conservative attribute and remote-context handling.
Database instrumentation uses `otelsql` with query text, bind values, DSNs, credentials, and low-value spans disabled.
Telemetry excludes secrets, Attachment contents, and unbounded-cardinality attributes.

The optional development profile uses Grafana's `docker-otel-lgtm` for local inspection.
Restricted `pprof` may be enabled during development or diagnosis; continuous profiling is optional.

Resilience policy belongs at a concrete outbound adapter.
Every outbound operation has a deadline and cancellation.
Retries require classified transient failures, bounded attempts and elapsed time, jittered backoff, and destination-supported idempotency for mutations.
Failsafe-Go may implement these policies when a version-one dependency needs them, but does not wrap arbitrary database transactions or existing telemetry delivery.

## Deployment and upgrades

The supported server target is Linux AMD64.
Beamers ships as a standalone CGo-free binary and an OCI image.
Tested modes are direct execution, a supplied systemd service example, and Docker Compose with explicit persistent storage.

Raspberry Pi devices are Display or operator-console candidates, not authoritative servers.
Local hard-disk ZFS RAID-Z development validates correctness but does not certify the SSD-based latency objective.

Upgrades are manual and offline-capable.
Safe committed migrations run automatically in Maintenance Mode after a verified Backup, against staged state that must pass sanity checks before readiness returns.
Destructive, rollback-incompatible, or unclassified plans require a re-authenticated Administrator preview and approval or a host-authorized fallback.
Declared reader and writer compatibility ranges allow an older binary to retain post-upgrade data where the migration contract permits it.
Rollback otherwise restores the pre-upgrade Backup with the prior binary because version one has no down migrations.

Live operational state normally blocks migration.
An urgent crash, security, or data-visibility fix may use a guarded force-live option requiring exclusive database access, a mandatory reason, a fresh verified Full-Fidelity Backup, prominent confirmation, and an Audit Entry.

## Quality attributes and release gates

### Capacity and latency

The tested version-one envelope is one Active Event with:

- 64 concurrent Lanes or Locations.
- 500 connected Displays.
- 200 concurrent Crew consoles.
- 25,000 combined Sessions and Entries.
- 10,000 public readers using cacheable conditional polling.

These are tested targets, not hard configuration limits.
Attachment capacity follows storage and configured quotas.

On reference Linux AMD64 hardware with at least four CPU cores, 8 GB RAM, and durable SSD storage, live commands target durable acknowledgment within 250 milliseconds at p95.
Connected Displays target committed-output application within 500 milliseconds at p95 and one second at p99.
Online Stage Timer skew targets at most 250 milliseconds.

### Accessibility and localization

Public, Display, and Crew surfaces target WCAG 2.2 AA.
Keyboard use, focus, touch targets, screen readers, zoom, contrast, reduced motion, and text over variable media are release concerns rather than optional polish.
Themes must preserve required contrast, using Contrast Scrims where content varies.

Version one user-interface copy is English.
Content accepts Unicode and carries Event Locale and optional Content Language metadata.
Dates, numbers, and public metadata follow Event Locale while stored instants remain timezone-safe.

### Release verification

A release requires:

- Deterministic domain tests for timing, daylight-saving changes, ripple, Competitions, Results, and Overrides.
- Real SQLite integration tests for migrations, Ent privacy, idempotency, backup, and restore.
- Browser tests from Crew command through durable commit to multiple Displays.
- Fault tests for restart, disconnect, stale clients, storage failure, and forced live upgrade.
- Sustained load and soak testing at the documented capacity envelope.
- A real-hardware Chromium kiosk smoke test.
- Automated and representative manual accessibility review.

A critical version-one path failure blocks release.
An optional integration is a gate only if that integration ships.

## Decision index

### Product, Rundown, and time

- [ADR 0001: Own the live Rundown](adr/0001-own-the-live-rundown.md)
- [ADR 0002: Let crew control Session progression](adr/0002-let-crew-control-session-progression.md)
- [ADR 0003: Separate Timing Policy from boundary rigidity](adr/0003-separate-timing-policy-from-boundaries.md)
- [ADR 0004: Preserve three time layers](adr/0004-preserve-three-time-layers.md)
- [ADR 0005: Isolate Lane timing](adr/0005-isolate-lane-timing.md)
- [ADR 0015: Import into Drafts](adr/0015-import-into-drafts.md)
- [ADR 0016: Use one Event timezone](adr/0016-use-one-event-timezone.md)
- [ADR 0017: Synchronize Displays to server time](adr/0017-synchronize-displays-to-server-time.md)
- [ADR 0022: Separate Session state axes](adr/0022-separate-session-state-axes.md)
- [ADR 0023: Preserve Session Run history](adr/0023-preserve-session-run-history.md)
- [ADR 0024: Activate one Event at a time](adr/0024-activate-one-event-at-a-time.md)

### Displays, public surfaces, and Overrides

- [ADR 0006: Render Views in browsers](adr/0006-render-views-in-browsers.md)
- [ADR 0007: Unify temporary content as Overrides](adr/0007-unify-temporary-content-as-overrides.md)
- [ADR 0008: Enroll inputless Displays](adr/0008-enroll-inputless-displays.md)
- [ADR 0018: Compose Views from Regions](adr/0018-compose-views-from-regions.md)
- [ADR 0019: Provide a read-only public Schedule](adr/0019-provide-a-read-only-public-schedule.md)
- [ADR 0020: Separate public and crew data](adr/0020-separate-public-and-crew-data.md)
- [ADR 0032: Target WCAG 2.2 AA](adr/0032-target-wcag-2-2-aa.md)
- [ADR 0033: Limit version-one localization](adr/0033-limit-version-one-localization.md)
- [ADR 0048: Make Displays self-healing across upgrades](adr/0048-make-displays-self-healing-across-upgrades.md)

### Competitions, Attachments, and Results

- [ADR 0009: Control Competition Slides explicitly](adr/0009-control-competition-slides-explicitly.md)
- [ADR 0021: Own Attachment files](adr/0021-own-attachment-files.md)
- [ADR 0025: Fix Competition Submission Deadlines](adr/0025-fix-competition-submission-deadlines.md)
- [ADR 0026: Stage Results before Prizegiving](adr/0026-stage-results-before-prizegiving.md)
- [ADR 0027: Serialize live control by Program Channel](adr/0027-serialize-live-control-by-program-channel.md)

### Authority, identity, and consistency

- [ADR 0010: Run live authority at the venue](adr/0010-run-live-authority-at-the-venue.md)
- [ADR 0011: Store live state in SQLite](adr/0011-store-live-state-in-sqlite.md)
- [ADR 0012: Use one active service](adr/0012-use-one-active-service.md)
- [ADR 0013: Identify Crew Members individually](adr/0013-identify-crew-members-individually.md)
- [ADR 0014: Stage structural edits](adr/0014-stage-structural-edits.md)
- [ADR 0028: Reject stale live commands](adr/0028-reject-stale-live-commands.md)
- [ADR 0029: Retain audit history](adr/0029-retain-audit-history.md)
- [ADR 0030: Require secure Crew origins](adr/0030-require-secure-crew-origins.md)
- [ADR 0042: Permit a degraded Emergency Alert](adr/0042-permit-a-degraded-emergency-alert.md)

### Application architecture

- [ADR 0034: Use Ent with pure-Go SQLite](adr/0034-use-ent-with-pure-go-sqlite.md)
- [ADR 0035: Export vendor-neutral telemetry](adr/0035-export-vendor-neutral-telemetry.md)
- [ADR 0036: Apply resilience at outbound boundaries](adr/0036-apply-resilience-at-outbound-boundaries.md)
- [ADR 0037: Use Connect for typed Interfaces](adr/0037-use-connect-for-typed-apis.md)
- [ADR 0038: Stream revisioned state with SSE](adr/0038-stream-revisioned-state-with-sse.md)
- [ADR 0039: Render server-owned HTML with templ](adr/0039-render-server-owned-html-with-templ.md)
- [ADR 0040: Center writes on a command module](adr/0040-center-writes-on-a-command-module.md)
- [ADR 0041: Replicate SQLite optionally with Litestream](adr/0041-replicate-sqlite-optionally-with-litestream.md)

### Operations and release

- [ADR 0031: Set the version-one capacity envelope](adr/0031-set-v1-capacity-envelope.md)
- [ADR 0043: Fail closed on startup storage errors](adr/0043-fail-closed-on-startup-storage-errors.md)
- [ADR 0044: Separate liveness, readiness, and diagnostics](adr/0044-separate-liveness-readiness-and-diagnostics.md)
- [ADR 0045: Adapt shutdown to the platform budget](adr/0045-adapt-shutdown-to-the-platform-budget.md)
- [ADR 0046: Support AMD64 native and Compose deployments](adr/0046-support-amd64-native-and-compose-deployments.md)
- [ADR 0047: Keep upgrades explicit and offline-capable](adr/0047-keep-upgrades-explicit-and-offline-capable.md)
- [ADR 0049: Gate releases on system behavior](adr/0049-gate-releases-on-system-behavior.md)

## Supporting research

- [Ent feature survey](research/ent-feature-survey.md)
- [Shutdown grace-period research](research/shutdown-grace-periods.md)

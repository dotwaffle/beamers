# Draft Capability Table and imperative authorization inventory

This is the Stage 1 deliverable of the Phase 2 authorization program: the first systematic end-to-end read of the imperative authorization layer.
It proposes a closed Capability enum, drafts the Capability Table for every state-changing command action, inventories every exported store entrypoint by the actor classes that reach it, and records the discrepancies the read found.

Nothing here changes behavior.
Everything here describes what the code enforces today and what the table should say once Stage 2 lands the evaluator.
Where the current behavior is wrong, this document says so and proposes a deliberate resolution rather than normalizing it silently.

## Enumeration method

Every count in this document is reproducible from the repository root with the command beside it.
The counts are stated so a reviewer can check the inventory for completeness rather than trust it.

| Population | Command | Count |
| --- | --- | ---: |
| Lines mentioning `command.Execute` | `grep -rn 'command\.Execute' --include='*.go' --exclude='*_test.go' .` | 76 |
| State-changing command executions | `grep -rn 'command\.Execute(' --include='*.go' --exclude='*_test.go' .` | 75 |
| Exported store entrypoints | `grep -rn '^func ([A-Za-z_0-9]* \*\?\(SQLite\|CommandTx\)) [A-Z][A-Za-z0-9_]*' --include='*.go' --exclude='*_test.go' internal/store/` | 238 |
| Lines mentioning `systemContext` | `grep -rn 'systemContext(' --include='*.go' --exclude='*_test.go' .` | 203 |
| Store blanket allow decisions | the same, less the declaration at `internal/store/sqlite.go:65` | 202 |
| Raw Ent allow decisions | `grep -rn 'privacy.DecisionContext' --include='*.go' --exclude='*_test.go' .` | 3 |
| Rejection tables | `grep -rn 'RejectionTable{' --include='*.go' --exclude='*_test.go' internal/` | 16 |
| Viewer reads inside the store | `grep -rn 'viewer.FromContext' --include='*.go' --exclude='*_test.go' internal/store/` | 6 |
| Store-side capability or scope checks | call sites of the five store predicates listed below | 19 |

Two further populations are derived rather than grepped, and the derivation is stated where they appear.
The two `command.Execute` counts differ by one because `internal/auth/accounts.go:293` mentions the function in a comment rather than calling it; there are 75 real call sites.
Those 75 expand to 103 distinct action names recorded in Command Receipts, because ten of them are shared helpers that take the action name as an argument and two of those compose the name from an enum; the draft Capability Table lists every action name and names the helper it passes through.
The 238 entrypoints are grouped by source file in the store entrypoint actor-class inventory, and the per-file totals sum to 238.

Of the 6 `viewer.FromContext` reads inside the store, 5 are authorization predicates and 1 is attribution only: `internal/store/results.go:687` reads the acting Account ID to record authorship on a superseded Results Draft, and refuses only because a missing viewer means it cannot attribute the write.

## What the imperative layer enforces today

### The viewer

`internal/viewer/viewer.go` is the whole vocabulary.
An `Identity` carries an Account ID, an installation-wide `Administrator` flag, a role per Event, and an `EventScope` per Event holding granted Lane IDs, granted Display Group keys, and granted Capabilities.
The grantable Capability set is exactly `EmergencyAlert`, `ViewResults`, and `ManageResults`, and `HasCapability` refuses any name outside it.

Four predicates express every authorization question the code can currently ask:

- `CanProduceEvent(eventID)` — the Event Grant names the Producer role.
- `CanOperateLane(eventID, laneID)` — Producer, or Operator whose granted Lane IDs contain the Lane.
- `CanOperateDisplayGroup(eventID, key)` — Producer, or Operator whose granted Display Group keys contain the key.
- `HasCapability(eventID, capability)` — Producer always; Observer only for `ViewResults`; Operator only when the Event Grant lists the Capability explicitly.

The identity enters the request context in exactly one place, `internal/auth/auth.go:319` (`Account.Context`).
No other non-test code calls `viewer.NewContext`.

### The tripwire

`ent/schema/privacy.go` holds one global rule mixed into every schema.
It denies any query or mutation whose context carries neither a viewer identity naming an Account nor an explicit store decision.
It is a fail-closed check that authorization was decided somewhere, not a check of what was decided.

The explicit decision is minted by `systemContext` at `internal/store/sqlite.go:64`, which wraps `privacy.DecisionContext(ctx, privacy.Allow)`. 203 non-test call sites use it.
Two further sites mint the raw allow decision without going through `systemContext`, both inside schema invariant hooks: `ent/schema/session_invariants.go:44` and `ent/schema/lane_invariants.go:48`.

### The store-side checks

Five unexported predicates constitute the entire imperative authorization layer inside the store:

| Predicate | Location | Rule | Sentinel |
| --- | --- | --- | --- |
| `requireSessionLaneScope` | `internal/store/live_session.go:702` | Producer passes; otherwise every Lane of the target Session must be granted, and an empty Lane list refuses | `ErrSessionScopeRequired` |
| `requireSessionControlScope` | `internal/store/live_session.go:694` | loads the Session's Lanes, then defers to `requireSessionLaneScope` | `ErrSessionScopeRequired` |
| `canOperateOverrideTarget` | `internal/store/display_overrides.go:790` | Producer passes; a Lane target is judged by `CanOperateLane`; every other target is judged by `CanOperateDisplayGroup` on a derived key | `ErrDisplayOverrideScope` |
| `canOperateDisplayGroup` | `internal/store/display_overrides.go:1284` | `CanOperateDisplayGroup` on a literal Display Group key | `ErrDisplayOverrideScope` |
| `hasEmergencyAlertCapability` | `internal/store/display_overrides.go:808` | `HasCapability(EmergencyAlert)` | `ErrDisplayOverrideScope` |

They are invoked from 19 call sites inside 16 exported entrypoints, across 5 source files.
Every other one of the 238 exported store entrypoints enforces no Capability and no scope: it trusts its caller for those.

"No Capability and no scope" is the precise claim, and it is narrower than "no checks".
Several store entrypoints outside these 16 do enforce **ownership or eligibility** — that the acting Account owns the thing being changed, or that policy admits it — for example `accountOwnsUploadTarget` in `internal/store/attachments.go`, `ErrCompetitionSubmitterRequired` and `ErrCompetitionSubmissionIneligible` in `internal/store/competition.go`, `ErrPresentationSubmitterRequired` in `internal/store/presentation_submissions.go`, and `ErrVotingIneligible` in `internal/store/ballots.go`.
Those are not Capability checks and do not consult an Event Grant, which is why they stay in the store as domain invariants rather than becoming table rows.

The 16 entrypoints that enforce a Capability or a scope are:

| File | Entrypoints | Reached how |
| --- | --- | --- |
| `live_session.go` | `StartSession`, `EndSession`, `CorrectLiveDetails` | directly |
| `session_cancellation.go` | `CancelSession` | directly |
| `session_target.go` | `PreviewSessionTarget`, `AdjustSessionTarget` | through `previewSessionTarget` |
| `pull_forward.go` | `PreviewPullForward`, `PullForward` | through `previewPullForward` |
| `display_overrides.go` | `PreviewStageMessage`, `PreviewTechnicalDifficulties`, `PreviewPriorityOverride`, `ListActiveDisplayOverrides`, `ActivateStageMessage`, `ActivateTechnicalDifficulties`, `ActivatePriorityOverride`, `ClearDisplayOverride` | directly |

`ReinstateSession` and `PersistDegradedEmergencyAlert` are conspicuously absent from that list; both are recorded in the discrepancy list.

This is the central finding of the read.
ADR 0059 declared the store and command surface the sole enforced authorization boundary, but the store enforces authorization for live Session control and Display Overrides only.
For every other area the effective boundary is the service layer, and for some areas it is the HTTP route guard.

### The service-layer checks

Outside the store, authorization is enforced by service functions calling the four viewer predicates through `auth.Account`, which re-wraps them.
`auth.Account.CanOperateEvent` has no `viewer.Identity` equivalent: it reports Producer or Operator without consulting any scope, and `internal/programcontrol` uses it as the sole gate for Program Channel control.

Three enforcement idioms coexist, and which one a package uses determines whether a refusal leaves evidence:

1. **Inside `Apply`, returned as a classified rejection.**
   The refusal commits a Command Receipt and a Rejected Audit Entry, and a replay restores the same sentinel.
   Used by `events`, `auth`, `activation`, `displays`, `competition`, `sessioncontrol`, `attachments` (release paths), `overrides`, `presentation`, `programcontrol`, `results` (Producer paths).
2. **Inside `Apply`, with a hand-written code literal rather than a `RejectionTable`.**
   Evidence is still committed, but the two directions of the mapping are written separately.
   Used by `rundown`, `themes`, `eventthemes`, `eventinterchange`, `schedulebaseline`.
3. **Before `command.Execute`.**
   The command is never opened, so there is no Command Receipt, no Audit Entry, and no durable proof the refusal happened.
   Used by `results` for both Capability gates, by `voting` for every Producer gate, by `attachments` for the upload gate, and by every package for its preview and read paths.

Idiom 3 on a state-changing path is the evidence gap ADR 0061 exists to close.

### The transport guards

The browser and RPC surfaces carry their own guards, which run before the service function.
They remain in place under this program: the spec places handler-level authorization changes out of scope.
They matter here only because for some command actions the transport guard is the *only* role check, so the Capability Table row must supply one where the service layer currently supplies none.
The draft Capability Table flags every such row.

## Proposed closed Capability enum

The enum below is derived from what the imperative layer enforces, not from what the product might want.
Every constant exists because some current check distinguishes it from its neighbors; where two areas enforce the same authority under different names, the enum names it once and the discrepancy list records it.

Rows speak only Capabilities, as ADR 0061 requires.
Roles expand to Capability sets at evaluation: Producer to every Event Capability, Operator to the scoped operation Capabilities plus explicitly granted ones, Observer to the viewing Capabilities plus a granted `ViewResults`.
The grantable set stays exactly `EmergencyAlert`, `ViewResults`, and `ManageResults`; every other constant is reachable only through role expansion.

### Installation Capabilities

These are held only by the installation Administrator and carry scope dimension `none`.

| Capability | Enforced today by | Covers |
| --- | --- | --- |
| `AdministerAccounts` | `ErrAdministratorRequired` in `auth` | Create Account, Disable Account, recovery token issue, registration policy, bootstrap |
| `AdministerEvents` | `ErrAdministratorRequired` in `events` | Create Event, Event Grant creation, Event Slug Alias pruning |
| `AdministerActiveEvent` | `ErrAdministratorRequired` in `activation` | Activate Event |
| `AdministerDisplays` | `ErrAdministratorRequired` in `displays` | Display Enrollment, Display assignment |
| `AdministerInstallationThemes` | `ErrAdministratorRequired` in `themes` | Installation Theme revision create and activate |
| `AdministerInterchange` | `ErrAdministratorRequired` in `eventinterchange` | Event Interchange import |

Six constants where the code has one `Administrator` boolean.
Splitting them is a proposal, not a description: today any Administrator may do all six.
The split is worth making because the enum is the vocabulary Stage 2 rows are written in, and a single `AdministerInstallation` constant would make every administrative row indistinguishable.
Role expansion maps the `Administrator` flag to all six, so behavior is unchanged.

### Event Capabilities, scope dimension `Event-wide`

| Capability | Enforced today by | Covers |
| --- | --- | --- |
| `ViewEventCrew` | `ErrEventAccessDenied`, `ErrCrewRequired` | Crew Rundown, Crew Event Overview, crew Display reads |
| `ConfigureEvent` | `ErrEventAccessDenied` in `events` | Update Event |
| `ConfigureRundown` | `ErrEventAccessDenied` in `rundown` | Draft edit, Draft Session delete, Draft history revert and discard, CSV and iCalendar import, Publish |
| `ConfigureCompetition` | `ErrProducerRequired` in `competition` | Competition readiness, Submission Eligibility, Entry order, Entry create and update, disposition change |
| `ManageAttachments` | `ErrProducerRequired` in `attachments` | Attachment upload, version release, Entry attachment readiness, Attachment Release configuration and cues |
| `ManageEventThemes` | `ErrProducerRequired` in `eventthemes` | Event Theme revision create and activate |
| `ManageVoting` | `ErrProducerRequired` in `voting` | Voting Key issue and revoke, voting window configure, open, and close |
| `ManagePresentations` | `ErrProducerRequired` in `presentation` | Reopen Window create and update, Submitter assignment |
| `CaptureScheduleBaseline` | `ErrProducerRequired` in `schedulebaseline` | Public Schedule baseline capture |
| `ExportInterchange` | `ErrEventAccessDenied` in `eventinterchange` | Event Interchange export |
| `ConfigureOverrides` | `ErrProducerRequired` in `overrides` | Stage Message preset configuration |
| `ViewResults` | `ErrViewRequired` | Results Previews, workspaces, Correction history, Prizegiving plans and previews |
| `ManageResults` | `ErrManageRequired`, `ErrProducerRequired` in `results` | Results Draft save, Competition Award save, Event Award save, readiness marking, publication, correction, Prizegiving designation |

`ViewResults` and `ManageResults` are the two constants that already exist as grantable Capabilities and already appear in the code under those names.
The other eleven are new names for authorities the code currently expresses as a Producer role check.

### Event Capabilities, scope dimension `Lanes-of-target`

| Capability | Enforced today by | Covers |
| --- | --- | --- |
| `OperateSession` | `ErrSessionScopeRequired`, `ErrOperatorRequired` in `sessioncontrol` | Take, End Now, Cancel Session, Reinstate Session, Adjust Target, Pull Forward, Correct Live Details |
| `OperateCompetitionEntry` | `ErrOperatorRequired` in `competition` | Defer Entry, resolve Entry, technical failure, release hold |

`OperateCompetitionEntry` is declared `Lanes-of-target` even though the code enforces it Event-wide today.
That is a deliberate proposed change, recorded as discrepancy D4.

### Event Capabilities, scope dimension `DisplayGroups-of-target`

| Capability | Enforced today by | Covers |
| --- | --- | --- |
| `OperateDisplayOverride` | `ErrDisplayOverrideScope` | Send Stage Message, Technical Difficulties, Urgent Notice, Clear Override |
| `EmergencyAlert` | `ErrDisplayOverrideScope` plus `HasCapability(EmergencyAlert)` | Emergency Alert activation and clearing, including the degraded path |
| `OperateProgramChannel` | `ErrOperatorRequired` in `programcontrol` | Take Program Output, Select Program Preview, Change Program Control, Prizegiving result cues |

`OperateProgramChannel` is declared `DisplayGroups-of-target` although the code enforces it Event-wide through `CanOperateEvent`.
That is a deliberate proposed change, recorded as discrepancy D3.

### Scope dimension summary

Four dimensions cover every enforced rule, as ADR 0061 anticipated:

- `none` — installation authority; no Event facts needed.
- `Event-wide` — the Event Grant must name the Event; no Lane or Display Group facts needed.
- `Lanes-of-target` — every Lane the target Session occupies must be granted, and a target with no Lanes refuses.
- `DisplayGroups-of-target` — the Display Group keys the Override target resolves to must be granted.

The empty-Lane refusal in `requireSessionLaneScope` is a real rule, not an accident: a Session with no Lane placement cannot be judged, so it is refused rather than allowed.
The table must preserve it, and the type-enforced pairing ADR 0061 describes makes it expressible as "scoped row with an empty fact set refuses".

## Store entrypoint actor-class inventory

All 238 exported entrypoints are grouped by source file below.
The per-file entrypoint counts sum to 238 and the per-file `systemContext` counts sum to 203, so every site is accounted for and every site lives in `internal/store`.
Two of the rows carry `systemContext` sites but export no entrypoint at all — `commands.go` (2) and `prizegiving_reveal_pause.go` (1) — and one of the 2 sites attributed to `sqlite.go` is the declaration of `systemContext` itself rather than a use of it.

Actor classes are determined by tracing callers, not by inspecting the entrypoint.
The classes are the ones ADR 0061 names — viewer, Display, public visitor, Backup, migration, replication, command replay, host maintenance — with one addition the read forced: **account holder**, an authenticated visitor acting on their own submissions or profile on a public surface rather than as crew.
The account holder is not a Crew Member and reaches entrypoints no Event Grant covers, so collapsing it into "viewer" would misdeclare `AccountOwnsUploadTarget`, `LoadAccountCompetitionSubmissions`, `LoadAccountPresentationSubmissions`, and the favorite-Session entrypoints.

### Inventory

| File | Entrypoints | `systemContext` sites | Actor classes |
| --- | ---: | ---: | --- |
| `activation.go` | 4 | 2 | viewer |
| `attachment_release.go` | 8 | 6 | viewer, public visitor |
| `attachments.go` | 9 | 9 | viewer, account holder |
| `audit.go` | 2 | 2 | viewer, host maintenance |
| `auth.go` | 24 | 20 | public visitor, viewer, host maintenance |
| `ballots.go` | 10 | 11 | public visitor, viewer |
| `capacity.go` | 1 | 1 | host maintenance, viewer |
| `command_tx.go` | 9 | 3 | command replay, and transitively every write actor; migration through `ProbeCommandEvidence` |
| `competition.go` | 15 | 9 | viewer, account holder |
| `competition_exceptions.go` | 5 | 0 | viewer |
| `competition_order.go` | 3 | 0 | viewer |
| `csv_import.go` | 6 | 2 | viewer |
| `display.go` | 7 | 7 | Display, viewer |
| `display_acknowledgment.go` | 1 | 1 | Display |
| `display_configuration.go` | 2 | 0 | viewer |
| `display_overrides.go` | 11 | 14 | viewer |
| `display_snapshot.go` | 1 | 1 | Display |
| `draft_edit.go` | 1 | 2 | viewer, host maintenance |
| `draft_history.go` | 2 | 2 | viewer |
| `draft_session.go` | 1 | 2 | viewer |
| `event_themes.go` | 7 | 6 | public visitor, viewer, Display |
| `events.go` | 12 | 22 | public visitor, viewer, host maintenance |
| `favorite_session.go` | 2 | 2 | account holder |
| `federation.go` | 3 | 3 | public visitor, viewer |
| `live_session.go` | 4 | 5 | viewer |
| `migrations.go` | 1 | 0 | Backup |
| `presentation_submissions.go` | 4 | 4 | viewer, account holder |
| `prizegiving.go` | 8 | 6 | viewer |
| `program_channel.go` | 7 | 1 | viewer |
| `public_schedule.go` | 1 | 1 | public visitor |
| `public_schedule_baseline.go` | 3 | 3 | viewer |
| `pull_forward.go` | 2 | 2 | viewer |
| `results.go` | 11 | 8 | viewer |
| `results_correction.go` | 4 | 4 | viewer |
| `results_publication.go` | 8 | 9 | public visitor, viewer |
| `rundown.go` | 9 | 5 | viewer, host maintenance |
| `session_cancellation.go` | 1 | 1 | viewer |
| `session_reinstatement.go` | 2 | 3 | viewer |
| `session_target.go` | 2 | 2 | viewer |
| `snapshot.go` | 1 | 0 | Backup, migration |
| `sqlite.go` | 3 | 2 | host maintenance, Backup, viewer, migration |
| `themes.go` | 7 | 5 | public visitor, viewer, Display |
| `upgrade.go` | 2 | 1 | migration, host maintenance |
| `voting_keys.go` | 4 | 4 | public visitor, viewer |
| `webauthn.go` | 8 | 7 | public visitor, viewer |
| `commands.go` | 0 | 2 | command replay |
| `prizegiving_reveal_pause.go` | 0 | 1 | viewer |
| **Total** | **238** | **201** | |

### What the inventory shows

**Replication reaches no store entrypoint at all.** `internal/replication` calls only installation-level open and close helpers and the underlying replication tool; it never calls a method on `*SQLite` or `*CommandTx`.
It is still a System Actor worth naming, because it holds the database, but no entrypoint declaration will name it.

**`command_tx.go` is the widest surface.**
Its 9 entrypoints are called by exactly one package, `internal/command`, but through it every write actor in the system reaches them.
The entrypoint declaration for the command lifecycle therefore cannot usefully narrow by actor class; it narrows by being the only path that writes a Command Receipt.

**Sixteen files are reached by more than one actor class**, and they are exactly the files carrying the heaviest `systemContext` use: `events.go` (22 sites, three classes), `auth.go` (20 sites, three classes), `themes.go` and `event_themes.go` (three classes each, including Display), `ballots.go`, `voting_keys.go`, `results_publication.go`, `attachment_release.go`, `webauthn.go`, and `federation.go` (public visitor plus viewer), `display.go` (Display plus viewer), `audit.go` and `capacity.go` (host maintenance plus viewer), `draft_edit.go` and `rundown.go` (viewer plus host maintenance), `snapshot.go` (Backup plus migration), and `sqlite.go` (four classes).
These are where a Stage 2 entrypoint declaration earns its keep: today a Display read and a public visitor read of the same theme entrypoint are indistinguishable.

**Four files are reached by exactly one non-viewer class** and are the cleanest early declarations: `display_snapshot.go` and `display_acknowledgment.go` (Display), `public_schedule.go` (public visitor), `migrations.go` (Backup).

**Three files run entirely under caller-supplied identity with zero bypass** — `competition_exceptions.go`, `competition_order.go`, and `display_configuration.go` — and are the only entrypoint files where the tripwire is unconditionally in force.

**Six exported entrypoints have no caller outside `internal/store`**: `CommandTx.TakeCompetitionEntrySlide`, `CommandTx.SupersedeCompetitionResultsDraft`, `SQLite.LoadProgramChannel`, `CommandTx.LoadProgramChannel`, `SQLite.LoadDisplayStatus`, and `CommandTx.RecordOutcome`.
They still need declarations, because an exported entrypoint with no declared actor class is exactly the hole ADR 0061 closes; the honest declaration is that they accept no external actor.

**No store entrypoint takes a viewer identity as a parameter.**
Authorization facts reach the store only through the context, and the store discards them at 203 sites.
This is why the Stage 2 entrypoint declaration has to be a property of the entrypoint rather than an argument convention.

## Draft Capability Table

### How the rows were derived

The grep for `command.Execute` returns 76 lines, but one of them is a comment: `internal/auth/accounts.go:293` mentions `command.Execute's` return value in prose.
There are **75 real call sites**, confirmed by `grep -rn 'command\.Execute(' --include='*.go' --exclude='*_test.go' . | wc -l`.

Those 75 call sites carry **103 distinct action names**, because ten of them are shared helpers that take the action name as an argument, and two of those compose the recorded name by concatenating an enum value.
`internal/programcontrol/programcontrol.go:338` records `"ChangeProgramControl" + string(input.Action)` over the five `ControlAction` values `Claim`, `RequestHandover`, `Handover`, `Takeover`, and `Disconnect`.
`internal/programcontrol/programcontrol.go:410` records `"ActOnPrizegivingResult" + string(input.Action)` over the four `ResultAction` values `Reveal`, `ReplayReveal`, `SkipToFinal`, and `SkipFromStage`.
Those two entries therefore stand for nine recorded action names, not two.

The table declares one row per authorization rule, so it carries 99 rows covering all 103 names: the two Program Channel rows are families whose members share a rule.
Whether the Stage 2 completeness check keys on exact recorded action strings or on families is a decision the table cannot make for itself, and it is recorded as an open question — keying on exact strings would require nine rows here and would make the enum concatenation a hazard, since adding a `ControlAction` value would silently add an unrowed action.

The shared helpers and the actions they carry are:

| Helper | Location | Actions carried |
| --- | --- | ---: |
| `overrides.execute` | `internal/overrides/overrides.go:518` | 6 |
| `overrides.persistDegradedCommand` | `internal/overrides/degraded.go:172` | 2 replays |
| `competition.executeEntryCommand` | `internal/competition/competition.go:1192` | 10 |
| `sessioncontrol.execute` | `internal/sessioncontrol/sessioncontrol.go:1098` | 3 |
| `programcontrol.runControlCommand` | `internal/programcontrol/programcontrol.go:679` | 2 |
| `programcontrol.runChannelCommand` | `internal/programcontrol/programcontrol.go:978` | 3 |
| `programcontrol.auditOperatorRejection` | `internal/programcontrol/programcontrol.go:439` | refusal receipts for the 5 above |
| `presentation.execute` | `internal/presentation/presentation.go:275` | 2 |
| `rundown.changeDraftHistory` | `internal/rundown/history.go:55` | 2 |
| `voting.changeWindow` | `internal/voting/ballots.go:144` | 2 |
| `attachments.storeVersion` | `internal/attachments/attachments.go:422` | 1 action, 2 callers, different rules |

The **Evidence** column records what a refusal produces today.
`receipt` means a Command Receipt and a Rejected Audit Entry commit.
`none` means the refusal happens before `command.Execute` and nothing durable records it.
`hard` means the refusal is returned as a plain error from inside `Apply`, which aborts the command without committing a rejection.
`none` and `hard` on a state-changing action are the gap ADR 0061 closes; every such row becomes `receipt` in Stage 2 with no other behavior change.

### Installation administration

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `CreateAccount` | `AdministerAccounts` | none | not needed | Administrator flag in `Apply`, `internal/auth/accounts.go:399` | receipt |
| `DisableAccount` | `AdministerAccounts` | none | not needed | Administrator flag in `Apply`, `internal/auth/accounts.go:481` | receipt |
| `IssueAccountRecoveryToken` | `AdministerAccounts` | none | not needed | Administrator flag in `Apply`, `internal/auth/recovery.go:243` | receipt |
| `CreateEvent` | `AdministerEvents` | none | Event does not yet exist | Administrator flag in `Apply`, `internal/events/events.go:225` | receipt |
| `CreateEventGrant` | `AdministerEvents` | none | Lane IDs come from input and are validated, not loaded | Administrator flag in `Apply`, `internal/events/events.go:431` | receipt |
| `PruneEventSlugAlias` | `AdministerEvents` | none | not needed | Administrator flag in `Apply`, `internal/events/events.go:350` | receipt |
| `ActivateEvent` | `AdministerActiveEvent` | none | `LoadActivationPreflight`, `internal/activation/activation.go:145` | Administrator flag in `Apply`, `internal/activation/activation.go:142` | receipt |
| `EnrollDisplay` | `AdministerDisplays` | none | not needed | Administrator flag in `Apply`, `internal/displays/displays.go:832` | receipt |
| `AssignDisplay` | `AdministerDisplays` | none | Display Group keys shape-validated from input, not loaded | Administrator flag in `Apply`, `internal/displays/displays.go:773` | receipt |
| `CreateInstallationThemeRevision` | `AdministerInstallationThemes` | none | not needed | Administrator flag in `Apply`, `internal/themes/themes.go:156` | receipt |
| `ActivateInstallationThemeRevision` | `AdministerInstallationThemes` | none | `LoadInstallationThemeRevision`, `ListActiveEventThemeConfigs`, `internal/themes/themes.go:215` | Administrator flag in `Apply`, `internal/themes/themes.go:210` | receipt |
| `ImportEventInterchange` | `AdministerInterchange` | none | creates the Event in the transaction, then self-grants Producer | Administrator flag in `Apply`, `internal/eventinterchange/interchange.go:223` | receipt |

### Event configuration

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `UpdateEvent` | `ConfigureEvent` | Event-wide | revision guard only | `CanProduceEvent` in `Apply`, `internal/events/events.go:523` | receipt |
| `ConfigureDisplays` | `ConfigureEvent` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/events/events.go:627` | receipt |
| `ExportEventInterchange` | `ExportInterchange` | Event-wide | `LoadEventInterchange`, `internal/eventinterchange/interchange.go:144` | `CanProduceEvent` in `Apply`, `internal/eventinterchange/interchange.go:141` | receipt |
| `CreateEventThemeRevision` | `ManageEventThemes` | Event-wide | not loaded | hand-rolled Producer role read in `Apply`, `internal/eventthemes/eventthemes.go:212` | receipt |
| `ActivateEventThemeRevision` | `ManageEventThemes` | Event-wide | `LoadEventThemeRevision`, `internal/eventthemes/eventthemes.go:264` | hand-rolled Producer role read in `Apply`, `internal/eventthemes/eventthemes.go:259` | receipt |
| `CapturePublicScheduleBaseline` | `CaptureScheduleBaseline` | Event-wide | `LoadPublicScheduleBaselineState`, `internal/schedulebaseline/schedulebaseline.go:213` | `CanProduceEvent` in `Apply`, `internal/schedulebaseline/schedulebaseline.go:210` | receipt |

### Rundown

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `EditDraft` | `ConfigureRundown` | Event-wide | structural references validated in the store | `CanProduceEvent` in `Apply`, `internal/rundown/rundown.go:286` | receipt |
| `DeleteDraftSession` | `ConfigureRundown` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/rundown/delete_session.go:64` | receipt |
| `DiscardDraftChanges` | `ConfigureRundown` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/rundown/history.go:65` | **hard** |
| `RevertDraftChange` | `ConfigureRundown` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/rundown/history.go:65` | **hard** |
| `Publish` | `ConfigureRundown` | Event-wide | `LoadPublishState`, `internal/rundown/publish.go:200` | `CanProduceEvent` in `Apply`, `internal/rundown/publish.go:197` | receipt |
| `ImportCSV` | `ConfigureRundown` | Event-wide | `LoadCSVImportState`, `internal/rundown/csv_import.go:149` | `CanProduceEvent` in `Apply`, `internal/rundown/csv_import.go:146` | receipt |
| `ImportICalendar` | `ConfigureRundown` | Event-wide | `LoadICalendarImportState`, `internal/rundown/ical_import.go:110` | `CanProduceEvent` in `Apply`, `internal/rundown/ical_import.go:107` | receipt |

### Live Session control

Every row here is `Lanes-of-target`, and the facts already exist: the store loads the Session Run Snapshot inside the transaction and `requireSessionLaneScope` judges its Lane IDs.
This is the one area whose scope facts are already loaded exactly where ADR 0061 wants them.

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `StartSession` | `OperateSession` | Lanes-of-target | Run Snapshot Lane IDs, `internal/store/live_session.go:173` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `EndSession` | `OperateSession` | Lanes-of-target | Session Lanes, `internal/store/live_session.go:262` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `CancelSession` | `OperateSession` | Lanes-of-target | Session Lanes, `internal/store/session_cancellation.go:46` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `AdjustTarget` | `OperateSession` | Lanes-of-target | Run Snapshot Lane IDs, `internal/store/session_target.go:212` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `PullForward` | `OperateSession` | Lanes-of-target | Run Snapshot Lane IDs, `internal/store/pull_forward.go:166` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `CorrectLiveDetails` | `OperateSession` | Lanes-of-target | Session Lanes, `internal/store/live_session.go:424` | `CanOperateEvent` in `Apply` plus store Lane scope | receipt |
| `ReinstateSession` | `OperateSession` | Lanes-of-target | Lane IDs arrive from the caller's input; the store revalidates placement but never checks scope | `CanProduceEvent` in `Apply`, `internal/sessioncontrol/sessioncontrol.go:707` | receipt |

### Program Channel control

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `TakeProgramOutput` | `OperateProgramChannel` | DisplayGroups-of-target | channel read **before** `Execute`, `internal/programcontrol/programcontrol.go:966` | `CanOperateEvent` before `Execute`, `:961` | receipt via `auditOperatorRejection` |
| `SelectProgramPreview` | `OperateProgramChannel` | DisplayGroups-of-target | channel read before `Execute`, `:661` and `:670` | `CanOperateEvent` before `Execute`, `:656` | receipt via `auditOperatorRejection` |
| `ChangeProgramControl` + one of `Claim`, `RequestHandover`, `Handover`, `Takeover`, `Disconnect` (5 names) | `OperateProgramChannel` | DisplayGroups-of-target | channel read before `Execute` | `CanOperateEvent` before `Execute`, `:656` | receipt via `auditOperatorRejection` |
| `ActOnPrizegivingResult` + one of `Reveal`, `ReplayReveal`, `SkipToFinal`, `SkipFromStage` (4 names) | `OperateProgramChannel` | DisplayGroups-of-target | channel read before `Execute` | `CanOperateEvent` before `Execute`, `:961` | receipt via `auditOperatorRejection` |
| `DeferCompetitionEntry` | `OperateCompetitionEntry` | Lanes-of-target | channel read before `Execute` | `CanOperateEvent` before `Execute`, `:961` | receipt via `auditOperatorRejection` |
| `ReconcileProgressiveResultsPublication` | `OperateProgramChannel` | DisplayGroups-of-target | plan and channel read **before** `Execute`, `:516` and `:526` | `CanOperateEvent` in the caller, `:479` | none |

`programcontrol` is the only area that opens a second, separate `command.Execute` purely to record a refusal decided elsewhere (`auditOperatorRejection`, `:439`).
It reaches the right outcome by a route the table makes unnecessary: once the evaluator sits inside `Execute`, the refusal receipt is the ordinary path.

### Display Overrides

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `ConfigureStageMessages` | `ConfigureOverrides` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/overrides/overrides.go:225` | receipt |
| `SendStageMessage` | `OperateDisplayOverride` | DisplayGroups-of-target | resolved target group key in the transaction, `internal/store/display_overrides.go:616` | store `canOperateDisplayGroup` only | receipt |
| `ActivateTechnicalDifficulties` | `OperateDisplayOverride` | DisplayGroups-of-target | resolved target group key, `internal/store/display_overrides.go:646` | store `canOperateDisplayGroup` only | receipt |
| `ActivateUrgentNotice` | `OperateDisplayOverride` | DisplayGroups-of-target | resolved target, `internal/store/display_overrides.go:676` | store `canOperateOverrideTarget` only | receipt |
| `ActivateEmergencyAlert` | `EmergencyAlert` | DisplayGroups-of-target | resolved target plus preview fingerprint, `internal/store/display_overrides.go:676` | store `canOperateOverrideTarget` and `hasEmergencyAlertCapability` | receipt |
| `ClearDisplayOverride` | `OperateDisplayOverride`, plus `EmergencyAlert` when clearing an Emergency Alert | DisplayGroups-of-target | resolved target, `internal/store/display_overrides.go:972` | store `canOperateOverrideTarget` and `hasEmergencyAlertCapability` | receipt |
| `ActivateEmergencyAlert` (degraded replay) | `EmergencyAlert` | DisplayGroups-of-target | decided in memory at capture time; the replay re-persists a fixed outcome | `canOperateDegradedEmergency`, `internal/overrides/degraded.go:631` | receipt at persist, **none** at refusal |
| `ClearDisplayOverride` (degraded replay) | `EmergencyAlert` | DisplayGroups-of-target | as above | `canOperateDegradedEmergency` | receipt at persist, **none** at refusal |

### Competition

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `ConfigureCompetitionReadiness` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `internal/competition/competition.go:648` | **hard** |
| `ConfigureCompetitionSubmissionEligibility` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:709` | receipt |
| `ConfigureCompetitionEntryOrder` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:594` | **hard** |
| `SetEntryAttachmentReadiness` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1111` | **hard** |
| `CreateCompetitionEntry` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `UpdateCompetitionEntry` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `AssignCompetitionEntrySubmitter` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `ChangeCompetitionEntryDisposition` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `ReviewCompetitionEntry` | `ConfigureCompetition` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `ResolveCompetitionEntry` | `OperateCompetitionEntry` | Lanes-of-target | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `SetCompetitionEntryReleaseHold` | `OperateCompetitionEntry` | Lanes-of-target | not loaded | `CanProduceEvent` in `Apply`, `:1202` | receipt |
| `RecordCompetitionTechnicalFailure` | `OperateCompetitionEntry` | Lanes-of-target | not loaded | `CanOperateEvent` in `Apply`, `:1202` | receipt |
| `CreateSubmittedCompetitionEntry` | none, account holder self-service | none | store checks Submitter ownership | acting Account must exist, `:1202` | receipt |
| `UpdateSubmittedCompetitionEntry` | none, account holder self-service | none | store checks Submitter ownership | acting Account must exist, `:1202` | receipt |

### Results

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `SaveCompetitionResultsDraft` | `ManageResults` | Event-wide | `LoadCompetitionResultsDraft`, `LoadVotingTally`, `LoadCompetitionResultsReviewState`, `internal/results/draft_application.go:79` | `HasCapability(ManageResults)` **before** `Execute`, `:52` | **none** |
| `SaveEventAwardsDraft` | `ManageResults` | Event-wide | not loaded | `HasCapability(ManageResults)` **before** `Execute`, `internal/results/event_award_application.go:81` | **none** |
| `SaveCompetitionAwards` | `ManageResults`, with Producer expansion additionally required for award promotions | Event-wide | `LoadCompetitionResultsDraft`, `internal/results/competition_award_application.go:61` | `HasCapability(ManageResults)` before `Execute` at `:35`; `CanProduceEvent` in `Apply` at `:67` | **none** then **hard** |
| `MarkCompetitionResultsReady` | `ManageResults` | Event-wide | `LoadCompetitionResultsReviewState`, `internal/results/draft_application.go:169` | `CanProduceEvent` before `Execute` at `:139` and again in `Apply` at `:166` | **none** then **hard** |
| `MarkEventAwardsReady` | `ManageResults` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/results/event_award_application.go:135` | **none** |
| `DesignatePrizegiving` | `ManageResults` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/results/prizegiving_designation_application.go:43` | **none** |
| `SavePrizegivingPlan` | `ManageResults` | Event-wide | `LoadPrizegivingDefaultOrderState`, `internal/results/prizegiving_application.go:158` | `CanProduceEvent` **before** `Execute`, `:132` | **none** |
| `RunPrizegivingPreflight` | `ManageResults` | Event-wide | `LoadPrizegivingPreflightState`, `internal/results/prizegiving_application.go:233` | `CanProduceEvent` **before** `Execute`, `:207` | **none** |
| `FirePrizegivingResultsCue` | `ManageResults` | Event-wide | publication and plan state in the transaction | `CanProduceEvent` in `Apply`, `internal/results/publication_application.go:103` | receipt |
| `ReleaseStandaloneResults` | `ManageResults` | Event-wide | `LoadResultsPublication`, `LoadStandaloneResultsReleaseState`, `:164` | `CanProduceEvent` in `Apply`, `:161` | receipt |
| `ReleaseStandaloneEventAwards` | `ManageResults` | Event-wide | `LoadResultsPublication`, `LoadEventAwardsDraft`, `:318` | `CanProduceEvent` in `Apply`, `:315` | receipt |
| `SaveResultsCorrection` | `ManageResults` | Event-wide | `LoadResultsCorrection`, `internal/results/correction_application.go:234` | `CanProduceEvent` in `Apply`, `:231` | receipt |
| `PublishResultsCorrection` | `ManageResults` | Event-wide | `LoadResultsCorrection`, `:336` | `CanProduceEvent` in `Apply`, `:333` | receipt |
| `ReviewResultsCorrection` | `ManageResults` | Event-wide | `LoadResultsCorrection`, `:468` | `CanProduceEvent` in `Apply`, `:465` | receipt |

### Attachments

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `UploadAttachment`, crew caller | `ManageAttachments` | Event-wide | upload target resolved in the transaction | `CanProduceEvent` **before** `Execute`, `internal/attachments/attachments.go:320` | **none** |
| `UploadAttachment`, account holder caller | none, self-service | none | store checks upload-target ownership and openness | `accountOwnsUploadTarget` in the store | receipt |
| `ConfigureEventAttachmentRelease` | `ManageAttachments` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:772` | receipt |
| `ConfigureCompetitionAttachmentRelease` | `ManageAttachments` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:818` | receipt |
| `SetAttachmentVersionRelease` | `ManageAttachments` | Event-wide | not loaded | `CanProduceEvent` in `Apply`, `:864` | receipt |
| `FireEventAttachmentReleaseCue` | `ManageAttachments` | Event-wide | preview fingerprint revalidated in the transaction | `CanProduceEvent` in `Apply`, `:932` | receipt |
| `CreateReopenWindow` | `ManagePresentations` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `:1051` | **none** |
| `ExtendReopenWindow` | `ManagePresentations` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `:1109` | **none** |
| `CloseReopenWindow` | `ManagePresentations` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `:1109` | **none** |

### Presentations

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `AssignPresentationSubmitter` | `ManagePresentations` | Event-wide | not loaded | `CanProduceEvent` evaluated eagerly and passed in as a bool, rechecked in `Apply`, `internal/presentation/presentation.go:287` | receipt |
| `UpdatePresentationSubmission` | none, account holder self-service | none | store scopes the write by the acting Account | a literal `true` is passed as the authorization bool, `:236` | receipt |

### Voting

| Action | Capability | Scope | Plan loads scope facts | Today's guard | Evidence |
| --- | --- | --- | --- | --- | --- |
| `IssueVotingKeys` | `ManageVoting` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/voting/voting.go:102` | **none** |
| `RevokeVotingKey` | `ManageVoting` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/voting/voting.go:242` | **none** |
| `ConfigureCompetitionVoting` | `ManageVoting` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/voting/ballots.go:61` | **none** |
| `OpenCompetitionVoting` | `ManageVoting` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/voting/ballots.go:129` | **none** |
| `CloseCompetitionVoting` | `ManageVoting` | Event-wide | not loaded | `CanProduceEvent` **before** `Execute`, `internal/voting/ballots.go:129` | **none** |
| `RedeemVotingKey` | none, account holder self-service | none | the key token digest is the credential | acting Account must exist | receipt |
| `CastCompetitionVote` | none, account holder self-service | none | eligibility and window state resolved in the transaction | acting Account must exist | receipt |

### Account self-service

These rows exist because the actions are state-changing commands, not because they need an Event Capability.
Their rule is ownership, which the table expresses as Capability `none` and scope `none`, with the store enforcing that the target is the acting Account.
Recording them keeps the completeness check honest: a row saying "no Capability, ownership enforced in the store" is a declaration, whereas an absent row is a hole.

| Action | Capability | Scope | Today's guard | Evidence |
| --- | --- | --- | --- | --- |
| `UpdateAccountProfile` | none, ownership | none | acts on the acting Account | receipt |
| `ReplaceRecoveryCodes` | none, ownership | none | acting Account must exist, checked before `Execute` | **none** |
| `RegisterWebAuthnCredential` | none, ownership | none | the ceremony's Account must equal the acting Account, `internal/auth/web_authn.go:174` | receipt |
| `RemovePasswordCredential` | none, ownership | none | acts on the acting Account | receipt |
| `RevokeWebAuthnCredential` | none, ownership | none | store scopes the write by the acting Account | receipt |
| `LinkFederatedIdentity` | none, ownership | none | acting Account must exist, checked before `Execute` | receipt |
| `RecoverAccount` | none, unauthenticated | none | the recovery secret is the credential | receipt |

### Row count

12 installation rows, 6 Event configuration rows, 7 Rundown rows, 7 live Session rows, 6 Program Channel rows, 8 Display Override rows, 14 Competition rows, 14 Results rows, 9 Attachment rows, 2 Presentation rows, 7 Voting rows, and 7 account self-service rows: **99 rows** covering **103 distinct recorded action names**.

The row count and the name count differ in both directions, and both differences are deliberate:

- Two Program Channel rows are families covering nine recorded names, because the action name is built by concatenating an enum value onto a prefix and every member shares one rule.
  That is 7 more names than rows.
- Three actions carry two rows each — `UploadAttachment`, `ActivateEmergencyAlert`, and `ClearDisplayOverride` — because two callers reach each under different rules.
  That is 3 more rows than names.

Whether the three double rows collapse is discrepancy D8; whether the two families are legitimate is an open question for Stage 2.

## Sentinel and rejection code coverage

The ticket requires that every authorization-related sentinel and rejection code appear in the table draft.
This section is that mapping.
No code in it may change during Stage 2: the codes are the durable contract recorded in Command Receipts, and parity is proven by producing exactly the codes produced today.

### Role and authority sentinels

| Sentinel | Declared in | Code today | Capability it becomes |
| --- | --- | --- | --- |
| `ErrAdministratorRequired` | `auth`, `events`, `activation`, `displays`, `themes`, `eventinterchange` (6 declarations) | `administrator_required` | `AdministerAccounts`, `AdministerEvents`, `AdministerActiveEvent`, `AdministerDisplays`, `AdministerInstallationThemes`, `AdministerInterchange` by area |
| `ErrProducerRequired` | `attachments`, `competition`, `eventthemes`, `overrides`, `results`, `schedulebaseline`, `sessioncontrol`, `voting`, `presentation` (9 declarations) | `producer_required`, and none at all in `voting` | `ManageAttachments`, `ConfigureCompetition`, `ManageEventThemes`, `ConfigureOverrides`, `ManageResults`, `CaptureScheduleBaseline`, `OperateSession`, `ManageVoting`, `ManagePresentations` by area |
| `ErrOperatorRequired` | `competition`, `programcontrol`, `sessioncontrol` (3 declarations) | `operator_required`, and `program_operator_required` in `programcontrol` | `OperateCompetitionEntry`, `OperateProgramChannel`, `OperateSession` |
| `ErrEventAccessDenied` | `store`, `rundown`, `eventinterchange` (3 declarations; `events` aliases the store one) | `event_access_denied` | `ConfigureRundown`, `ConfigureEvent`, `ExportInterchange` |
| `ErrCrewRequired` | `displays` | none | `ViewEventCrew` |
| `ErrGrantRoleRequired` | `events` | `grant_role_required` | validation inside `AdministerEvents`, not a Capability |

Five logical rules, 22 sentinel declarations.
Producer authority alone is expressed by nine sentinels and two different codes.

### Capability sentinels

| Sentinel | Declared in | Code today | Capability it becomes |
| --- | --- | --- | --- |
| `ErrManageRequired` | `internal/results/application.go:18` | **none** | `ManageResults` |
| `ErrViewRequired` | `internal/results/application.go:16` | **none** | `ViewResults` |

`ErrManageRequired` gates state-changing actions and has no code, which is exactly the evidence gap ADR 0061 names.
`ErrViewRequired` gates reads only; ADR 0061 says read entrypoints refuse with plain domain errors and no receipt, so it correctly stays codeless.

### Scope sentinels

| Sentinel | Declared in | Code today | Scope dimension it becomes |
| --- | --- | --- | --- |
| `ErrSessionScopeRequired` | `internal/store/live_session.go:38`, aliased in `sessioncontrol` | `session_scope_required` | Lanes-of-target |
| `ErrDisplayOverrideScope` | `internal/store/display_overrides.go:26`, aliased as `overrides.ErrScopeDenied` | `override_scope_denied` | DisplayGroups-of-target |
| `ErrEventGrantLaneMismatch` | `internal/store/events.go:38`, aliased in `events` | `event_grant_lane_mismatch` | grant validation, not an evaluation dimension |

### Ownership and eligibility sentinels

These are authorization-shaped but decide "is this your thing" rather than "do you hold this Capability".
They stay where they are; the table records them so no reviewer mistakes their absence for an omission.

| Sentinel | Declared in | Code today |
| --- | --- | --- |
| `ErrCompetitionSubmitterRequired` / `competition.ErrSubmitterRequired` | `store`, `competition` | `submitter_required` |
| `ErrPresentationSubmitterRequired` / `presentation.ErrSubmitterRequired` | `store`, `presentation` | `submitter_required` |
| `ErrCompetitionSubmissionIneligible` / `ErrSubmissionIneligible` | `store`, `competition` | `submission_ineligible` |
| `ErrVotingIneligible` | `store`, `voting` | `voting_ineligible` |
| `ErrLastAdministrator` | `store` | `last_administrator` |
| `ErrFinalCredential` | `store` | `final_credential` |
| `ErrControlOwned` | `programcontrol` | `program_control_owned` |
| `ErrControlOwnerRequired` | `programcontrol` | `program_control_owner_required` |
| `ErrAlreadyEligible` | `voting` | `voting_already_eligible` |
| `ErrSubmissionEligibilityRevision` | `competition` | `stale_submission_eligibility` |

### Authentication codes, recorded for completeness

Authentication is not authorization, but the two are easily conflated and one authentication code has a property no other code has.
`store.ErrInvalidSession` classifies as `credential_not_found` in `accountRejections` (`internal/auth/accounts.go:517`) and is the only non-test rejection that sets `Restored`, so a replay returns `auth.ErrInvalidSession` rather than the storage sentinel that was classified.
The evaluator must not disturb this: it is the one place where the classify direction and the restore direction deliberately differ.

### Authorization codes written without a rejection table

Five packages write authorization codes as string literals rather than through a `command.RejectionTable`, so the classify direction and the restore direction are separate code paths that can disagree:

| Package | Code | Where |
| --- | --- | --- |
| `rundown` | `event_access_denied` | `rundown.go:287`, `publish.go:198`, `delete_session.go:65`, `csv_import.go:147`, `ical_import.go:108` |
| `themes` | `administrator_required` | `themes.go:157`, `themes.go:211` |
| `eventthemes` | `producer_required` | `eventthemes.go:213`, `eventthemes.go:260` |
| `eventinterchange` | `administrator_required`, `event_access_denied` | `interchange.go:224`, `:142`, `:769` |
| `schedulebaseline` | `producer_required` | `schedulebaseline.go:262` |

## Discrepancy list

Each entry records what the read found, why it matters, and a proposed deliberate resolution for Stage 2.
None of these is normalized silently.
Entries marked **behavior change** cannot be justified by parity tests alone, because the current behavior is the thing being changed; each needs an explicit decision before Stage 2 encodes it.

### D1 — One rule, twenty-two sentinels

Producer authority is declared as `ErrProducerRequired` in nine packages, Administrator authority as `ErrAdministratorRequired` in six, Operator authority as `ErrOperatorRequired` in three, and Event access as `ErrEventAccessDenied` in three.
Each declaration is a separate `errors.New`, so `errors.Is` never relates them, and `presentation`'s message reads "producer role required" where the other eight read "producer authority required".

**Resolution.**
The table names one Capability per rule and the evaluator returns one sentinel per Capability.
The durable codes stay exactly as they are, because they are the Command Receipt contract.
The per-package sentinels are deleted in Stage 3 with their areas, not consolidated early, so each deletion stays bisectable.

### D2 — One rule, two durable codes

Producer authority refuses as `producer_required` in eight packages and as `event_access_denied` in three (`rundown`, `events`, `eventinterchange`).
Operator authority refuses as `operator_required` in two packages and `program_operator_required` in one.
A client cannot tell from the code alone which rule refused it.

**Resolution.**
Keep both codes at Stage 2; parity forbids changing them.
Record that unifying them is a client-visible change requiring its own decision, and that the table is the place where the duplication becomes visible enough to act on.

### D3 — Program Channel control ignores scope entirely — **behavior change**

`programcontrol` authorizes all six of its actions — including `ReconcileProgressiveResultsPublication`, whose check sits in the caller at `:479` — with `auth.Account.CanOperateEvent`, which reports Producer or Operator and consults no `EventScope` at all.
An Operator granted a single Display Group can take Program Output on every Program Channel in the Event.
This contradicts user story 3 directly, and user story 4 has no implementation at all.

**Resolution.**
The table declares `OperateProgramChannel` as `DisplayGroups-of-target`.
Stage 2 must add the fact loading to the plan, resolving the Program Channel to its consuming Displays' Display Groups.
This narrows existing authority, so it is a deliberate behavior change and needs a decision recorded before it lands.

### D4 — Live Competition Entry actions are Event-wide where the analogous Session actions are Lane-scoped — **behavior change**

`Defer Entry`, `ResolveCompetitionEntry`, `SetCompetitionEntryReleaseHold`, and `RecordCompetitionTechnicalFailure` are live operations on a Session's Entries, but they authorize Event-wide through `CanOperateEvent` or `CanProduceEvent`.
The analogous live Session actions in the same Lane are Lane-scoped.
User story 2 lists Defer Entry among the commands that must respect Lane boundaries.

**Resolution.**
Declare `OperateCompetitionEntry` as `Lanes-of-target`, with the plan supplying the Lanes of the Entry's Competition Session.
Behavior change; narrows authority.

### D5 — Reinstate Session has no scope check at all — **behavior change**

`internal/store/session_reinstatement.go:187` maps `errors.Is(err, privacy.Deny)` to `ErrSessionScopeRequired`.
That mapping was meaningful when Ent carried per-entity privacy policies.
Since those policies were removed, the tripwire denies only when authorization was never decided, so the branch can no longer express "this Session is outside your Lanes" — it fires only when there is no viewer at all.
Its three siblings (`session_target.go:190`, `pull_forward.go:139`, `session_cancellation.go:37`) carry the same dead mapping **and** a real `requireSessionLaneScope` call.
Reinstate carries only the dead one.
Separately, `sessioncontrol.Reinstate` guards with `CanProduceEvent`, so scoped Operators cannot reinstate at all even within their own Lanes, while every other live Session command admits them.

**Resolution.**
Declare `ReinstateSession` as `OperateSession` / `Lanes-of-target` like its siblings.
Delete the four vestigial `privacy.Deny` mappings, which are dead code that reads as enforcement.
Whether Operators gain Reinstate is a product decision: the table's consistent answer is yes, and user story 2 lists Reinstate Session among the Operator commands, so the recommendation is to admit them.

### D6 — Indirect Override targets are judged by a synthetic key instead of by the Displays they reach — **behavior change**

`displayOverrideTargetKey` (`internal/store/display_overrides.go:772`) maps Location, Program Channel, and Display targets to synthetic keys of the form `programchannel:12`, and `canOperateOverrideTarget` then asks `CanOperateDisplayGroup` with that string.
The target is never resolved to the Display Groups of the Displays that actually consume it, which is what user story 4 requires.

It would be wrong to say those targets are unreachable for non-Producers.
`validDisplayGroupKey` (`internal/store/display_overrides.go:1268`) explicitly permits `:` in a key, and `GrantEventAccess` stores whatever Display Group keys it is given after shape validation only, so an Administrator can grant the literal string `programchannel:12` and thereby authorize that one target.
The defect is subtler than unreachability: authority over an indirect target is expressed as a grant of an opaque string that happens to encode a database identifier, rather than as a grant over the Displays the Override will actually disturb.
An Operator granted the Display Group that a Program Channel feeds still cannot target that Channel, and an Operator granted `programchannel:12` keeps that authority even after the Channel is repointed at Displays they do not operate.

**Resolution.**
The plan resolves indirect targets to the consuming Displays' Display Group keys and supplies those as the scope facts, which is what the `DisplayGroups-of-target` dimension means.
This both widens authority (a Display Group grant starts covering the Channels feeding it) and narrows it (a synthetic-key grant stops meaning anything), so it needs an explicit decision and a migration note for any installation that granted synthetic keys.
The Lane target type is already handled correctly and separately by `CanOperateLane`.

### D7 — Two rules for one class of Override

`ActivateStageMessage` and `ActivateTechnicalDifficulties` judge scope with `canOperateDisplayGroup`, which takes a literal Display Group key and has no Lane branch.
`ActivatePriorityOverride` and `ClearDisplayOverride` judge scope with `canOperateOverrideTarget`, which does have one.
The same class of action is therefore governed by two different rules depending on Override kind.

**Resolution.**
One rule for all four rows: `canOperateOverrideTarget` semantics, generalized by the evaluator.
Because Stage Message and Technical Difficulties targets are always literal Display Group keys today, this is parity-preserving for them.

### D8 — The degraded Emergency Alert path is a hand copy of the store rule

`internal/overrides/degraded.go:631` (`canOperateDegradedEmergency`) and `:652` (`degradedTargetKey`) reimplement `canOperateOverrideTarget` and `displayOverrideTargetKey` in a second place, so the two can drift.
The degraded refusal is also decided in memory with no durable evidence, and only the accepted commands are persisted on recovery.

**Resolution.**
One evaluator, called by both paths.
The degraded path evaluates once at capture time and persists the outcome — including a refusal — as a Command Receipt on recovery, which is consistent with the replay semantics ADR 0061 sets out.
This also settles the three double rows counted in the draft Capability Table: `ActivateEmergencyAlert` and `ClearDisplayOverride` collapse to one row each once both paths share the evaluator, and `UploadAttachment` stays split because its two callers are genuinely different actors, crew and account holder.

### D9 — Twenty-four state-changing actions leave no evidence when refused

Refusals that produce no Command Receipt and no Audit Entry, either because they fire before `command.Execute` or because they abort `Apply` with a hard error:

| Evidence | Actions |
| --- | --- |
| **none**, refused before `Execute` | `SaveCompetitionResultsDraft`, `SaveEventAwardsDraft`, `SaveCompetitionAwards`, `MarkCompetitionResultsReady`, `MarkEventAwardsReady`, `DesignatePrizegiving`, `SavePrizegivingPlan`, `RunPrizegivingPreflight`, `IssueVotingKeys`, `RevokeVotingKey`, `ConfigureCompetitionVoting`, `OpenCompetitionVoting`, `CloseCompetitionVoting`, `UploadAttachment` (crew), `CreateReopenWindow`, `ExtendReopenWindow`, `CloseReopenWindow`, `ReplaceRecoveryCodes`, `ReconcileProgressiveResultsPublication` |
| **hard**, aborted inside `Apply` | `DiscardDraftChanges`, `RevertDraftChange`, `ConfigureCompetitionReadiness`, `ConfigureCompetitionEntryOrder`, `SetEntryAttachmentReadiness`, plus the second checks in `MarkCompetitionResultsReady` and `SaveCompetitionAwards` |

The Results and Voting entries are the clearest cases: a Crew Member without `ManageResults` who tries to save a Results Draft, and a non-Producer who tries to issue Voting Keys, both vanish without trace.

**Resolution.**
Every one becomes an ordinary table row evaluated inside `command.Execute`, which makes the receipt and the Rejected Audit Entry automatic.
This is the program's central purpose and needs no separate decision.
It is a behavior change only in that evidence now exists; the accept and refuse outcomes are unchanged.

### D10 — `ErrManageRequired` has no durable code

Neither Results Capability sentinel appears in any of the 16 rejection tables.
`ManageResults` gates state-changing actions, so refusing it must become classifiable.

**Resolution.**
Mint one new code for the `ManageResults` refusal.
`ErrViewRequired` stays codeless by design, because it gates reads and ADR 0061 keeps read refusals evidence-free.
A new code is not a parity violation, because today there is no code to preserve.

### D11 — Five packages classify authorization outside the rejection table

`rundown`, `themes`, `eventthemes`, `eventinterchange`, and `schedulebaseline` write their authorization codes as string literals in both directions, outside the both-directions guarantee `command.RejectionTable` exists to provide.
`internal/rundown/history.go:66` is the sharpest case: it returns `ErrEventAccessDenied` as a plain error from inside `Apply`, aborting the command, while its four sibling paths in the same package commit `event_access_denied` as a rejection.
The same refusal on `DiscardDraftChanges` and on `Publish` therefore produces different durable outcomes.

**Resolution.**
Fixed as a side effect of D9: the evaluator classifies uniformly, so the hand-written literals are deleted with their areas in Stage 3.

### D12 — `eventthemes` hand-rolls the Producer test

`internal/eventthemes/eventthemes.go:329` compares the Event role to `viewer.Producer` directly instead of calling `CanProduceEvent`.
It agrees with the predicate today and is a divergence waiting to happen.

**Resolution.**
Deleted with the area in Stage 3; no separate change.

### D13 — Two areas check the same rule twice with different evidence

`MarkCompetitionResultsReady` checks `CanProduceEvent` before `Execute` and again inside `Apply`; `SaveCompetitionAwards` checks `ManageResults` before `Execute` and `CanProduceEvent` inside.
Whichever fires first determines whether the refusal is recorded, so the same rule produces different evidence depending on the input.

**Resolution.**
One evaluation per action inside `Execute`.
`SaveCompetitionAwards` needs a decision the table cannot make for it: its in-`Apply` check is a genuine second rule, requiring Producer expansion specifically for award promotions.
Either the row's Capability becomes the stricter one for all callers, or the promotion rule stays an in-`Apply` domain invariant rather than an authorization rule.
The recommendation is the latter: promotion is a property of the change, not of the actor, so it belongs with the invariants ADR 0061 explicitly leaves in the store.

### D14 — Six exported store entrypoints have no external caller, and replication reaches none

`CommandTx.TakeCompetitionEntrySlide`, `CommandTx.SupersedeCompetitionResultsDraft`, `SQLite.LoadProgramChannel`, `CommandTx.LoadProgramChannel`, `SQLite.LoadDisplayStatus`, and `CommandTx.RecordOutcome` are exported but called only from inside `internal/store`.
Separately, `internal/replication` calls no store entrypoint at all, so one of the System Actors ADR 0061 names will have no entrypoint declaring it.

**Resolution.**
Declare the six as accepting no external actor class, which is an honest declaration rather than an omission, and unexport them if Stage 2 finds no reason to keep them exported.
Keep replication in the System Actor enum: it holds the database file and is a real caller boundary even though it is not a store caller.

### D15 — One command path depends on its caller having wrapped the viewer context

`internal/attachments/attachments.go:422` calls `command.Execute` with a plain `ctx` rather than the viewer-wrapped context every other call site uses.
It is correct today because both callers wrap before calling `storeVersion`, but nothing enforces that, and the tripwire would only catch it if no store decision were minted either.

**Resolution.**
Not an authorization defect today.
Record it because the Stage 2 evaluator reads the viewer from the context, which turns this implicit contract into a load-bearing one.

### D16 — The store is not the boundary ADR 0059 declared

16 of 238 exported store entrypoints enforce a Capability or scope.
The remaining 222 trust their callers, and 203 of them actively discard the viewer with `systemContext`.
ADR 0059's statement that the store and command surface are the sole enforced boundary is true of the command surface and false of the store.

**Resolution.**
This is the gap the two-level declaration closes, and it is why ADR 0061's second level exists.
No change of intent is needed; the discrepancy is recorded so that the entrypoint declarations of ticket 238 are understood as new enforcement rather than as documentation of existing enforcement.

## Open questions for Stage 2

1. D3, D4, D5, and D6 are behavior changes.
   Each needs an explicit decision before the table encodes it, because parity tests cannot justify them.
2. The six installation Capabilities are a proposed split of one `Administrator` flag.
   If reviewers prefer one `AdministerInstallation` constant, the table loses expressiveness but nothing else changes.
3. `ViewEventCrew` appears in the enum but has no state-changing action, so it has no row.
   It exists for the read entrypoints ADR 0061 says reuse the evaluator.
   Stage 2 should confirm read declarations are in scope for the same enum.
4. The account holder actor class is not in ADR 0061's System Actor enum, and correctly so — it is an authenticated person, not a system caller.
   But it is also not a Crew Member, so the entrypoint declarations need a name for it.
5. Does the completeness check key on exact recorded action names or on rule families?
   Two Program Channel rows cover nine names built by concatenating an enum value onto a prefix.
   Keying on exact names makes the check strict but turns every new `ControlAction` or `ResultAction` value into a silently unrowed action, which is the failure mode user story 18 exists to prevent.
   Keying on families needs the check to understand the prefix.
   The recommendation is to stop composing action names by concatenation and record the full name explicitly, which makes exact-name keying safe.

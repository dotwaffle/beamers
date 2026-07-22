# Stage structural edits before Publish

Structural changes such as Session details, Planned Time, Lane membership, and Competition Entries are made in a Draft.
Publish previews the diff and affected Lanes and Displays before making those changes authoritative.
Live operational commands such as start, End Now, Adjust Target, Take, Pull Forward, and Override activation remain immediate, avoiding publication overhead during operation without letting ordinary editing mistakes reach Displays instantly.
Cancel Session is likewise an immediate, confirmation-gated, audited live command; it does not create a Draft and requires no reason.
A Crew Member may attach a Public Cancellation Message and separate Crew Notes; public surfaces show "Canceled" when no public message is supplied.
Reinstate Session reverses that command immediately while preserving all Session metadata and prior Session Runs.
Planned Time remains unchanged, and the Crew Member chooses a new Forecast Time for the reinstated Session.

Each Event has one shared Draft rather than an editor lock or personal branches.
Draft edits use optimistic revisions over their relevant state.
Concurrent edits to independent fields may proceed, while a stale edit to a field changed by another Crew Member is rejected and refreshed instead of using last-write-wins.
The expected Draft Revision identifies the editor's base snapshot rather than acting as a global compare-and-swap lock.
When the current revision has advanced, Beamers checks immutable Draft Change history since that base and rejects only an edit whose targeted facts overlap an intervening effective change.
Every successful Draft Edit still advances the Event's monotonic Draft Revision exactly once.
A scalar value is one Draft Fact, while each membership in an unordered relationship is a separate fact so independent additions may proceed concurrently.
Conflicting changes to the same membership are rejected; entity deletion overlaps every fact and relationship involving that entity.
An ordered collection is one Draft Fact unless its specific domain model defines finer ordering operations.

Beamers stores the current Draft as materialized working state and stores Published structural state separately as the operational authority.
Each Draft Edit updates the materialized Draft and appends immutable Draft Change history in one transaction.
Ordinary Draft reads do not reconstruct state by replaying history; superseded, reverted, and Published changes remain evidence rather than an event-sourcing interface.
Each Event owns exactly one persisted Rundown, including while that Rundown is empty.
The Rundown carries separate monotonic Draft and Published revisions; the existing Event revision continues to cover Event configuration and does not invalidate Publish Preview by itself.
Draft Change history uses one relational envelope for identity, Event, Draft Edit, revision, kind, target, Draft Fact key, outcome, and dependency edges.
A canonical versioned before-and-after payload preserves evidence and supports Preview and Revert, but it is neither an executable generic patch nor the authority from which ordinary state is replayed.
Typed materialized state and its relational constraints remain authoritative.
Each Location, Lane, Track, and Session has one stable identity record with immutable Event ownership.
Its editable values and relationships live in separate typed Draft and Published state records rather than duplicated state-prefixed columns on the identity record.
A new identity initially has only Draft state; Publish creates or advances Published state without replacing that identity.
Published state is append-only by changed entity: each Publish creates an immutable typed version for every identity whose selected facts changed and associates it with the resulting Published Revision.
Unchanged identities receive no duplicate version, and each identity identifies its current Published version for ordinary projections.
Earlier Published versions remain directly queryable rather than requiring Draft Change replay.
Published relationships reference stable identities rather than particular version rows.
A historical projection at Published Revision R resolves each related identity to its latest Published version at or before R; the current projection uses each identity's current version.
This avoids duplicating an otherwise unchanged version merely because a related identity changed.
Run Snapshots still copy the exact operational values used for that Session Run.

Creating structural information in Draft allocates its permanent identity when the Draft Edit commits.
Later Draft Changes and batch-local references use that identity immediately, and Publish changes its authority without replacing or remapping it.
If never-Published information is later deleted under its domain rules, its identifier is not reused.
Deletion of never-Published Draft structure never cascades implicitly.
It succeeds only when the target is unreferenced or the same atomic Draft Edit explicitly removes or deletes every dependency.
Once Published, a Location, Lane, or Track leaves the current Rundown only through Structural Retirement in Publish and only when no current Draft, Published, or Live dependency requires it.
Retirement removes it from current projections while preserving identity and Publish history; a later Draft may reinstate the same identity.

Session Deletion is available only while a Session has never been Published and has no references.
Once Published, its stable identity and history are retained; Crew Members use Cancel Session or Audience Visibility of Crew Only instead.
Version one adds no Session archival state.
A later administrative archive view may hide completed Event material without deleting or changing its history.

Ordinary Publish does not alter a currently Live Session.
A confirmation-gated, audited Live Detail Correction may change only its title, speaker, or public description and updates current public Displays.
The original Run Snapshot remains immutable; a Run Amendment records the correction.
Structural and timing changes continue to use their specific live commands or apply to a future Session Run.

The same correction becomes the Session's authoritative descriptive value so a future Run or reinstatement does not restore the error.
Any existing Draft that touches a corrected field becomes a review conflict rather than overwriting the live correction on Publish.

A conflict does not force unrelated Draft work to wait.
A Crew Member may form a reviewed Publish Selection; the application validates its dependencies and applies that selection atomically.
Blocked, conflicting, and deliberately unselected changes remain in the Draft.
A Publish Preview expands the requested changes to their transitive dependency closure and visibly distinguishes every automatically included change.
The normalized dependency-closed selection is part of the preview fingerprint; Publish never discovers or silently adds another dependency after confirmation.
A partial Publish advances Published state with the selected effective changes, then rebases materialized Draft state onto that new baseline while preserving every unselected effective change.
The transaction advances the Event's Draft and Published revisions once each, and the selected Draft Changes remain in history with a Published outcome.

Publish confirmation is bound to the exact Draft and Published revisions used by its Publish Preview.
If either changes before confirmation, the service rejects the action without mutation and requires a fresh Preview; Crew Members never approve one diff while applying another.

Every Publish audit record includes actor, time, selected diff, and previewed impact.
A Crew Member may add a Publish Note, but routine publication requires no written reason.
Mandatory reasons remain limited to exceptional domain actions that define one.

Drafts retain per-change history and support ordinary undo, discard, and Draft Revert because those changes have produced no live effect.
Draft history is linear: when a later Draft Change replaces the same proposed fact, the earlier change becomes superseded and cannot be selected for Publish.
Draft Revert appends a new effective Draft Change instead of reactivating or erasing an earlier history entry.
Materialized Draft state and its effective pending change represent the net difference from the Published baseline.
If Revert restores that baseline exactly, no effective change remains selectable for the fact, while the Revert and superseded changes remain immutable history.
This does not weaken the prohibition on generic Undo for live commands or released data.

Live controls provide no generic Undo.
Reversible effects use explicit, audited domain actions such as Reinstate Session, Clear Override, Reveal Replay, or Results Correction.
Those actions preserve the original history, and the application never represents an externally visible effect as erased merely because a later action corrected it.

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

Publish confirmation is bound to the exact Draft and Published revisions used by its Publish Preview.
If either changes before confirmation, the service rejects the action without mutation and requires a fresh Preview; Crew Members never approve one diff while applying another.

Every Publish audit record includes actor, time, selected diff, and previewed impact.
A Crew Member may add a Publish Note, but routine publication requires no written reason.
Mandatory reasons remain limited to exceptional domain actions that define one.

Drafts retain per-change history and support ordinary undo, discard, and Draft Revert because those changes have produced no live effect.
This does not weaken the prohibition on generic Undo for live commands or released data.

Live controls provide no generic Undo.
Reversible effects use explicit, audited domain actions such as Reinstate Session, Clear Override, Reveal Replay, or Results Correction.
Those actions preserve the original history, and the application never represents an externally visible effect as erased merely because a later action corrected it.

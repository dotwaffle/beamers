# Preserve Session Run history

Each Start creates a Session Run with its own Actual Time and outcome rather than overwriting a Session's prior execution.
This lets a Crew Member cancel a Live Session, reinstate the same Session at a new Forecast Time, and retain its identity, metadata, and earlier partial Run.
Planned Time remains the original plan while the Run history records what actually happened.
The public Schedule shows one Rescheduled listing at the new Forecast Time, identifies its previous time, and does not retain a duplicate Canceled listing.
Complete cancellation and Session Run history remains available to crew.
Reinstatement retains the Session's public deep link as part of that identity.

At Start, each Session Run captures an immutable Run Snapshot of the published title, type, Public Details, Lanes, Locations, Timing Policy, boundaries, and Competition Entry order.
Later edits and reinstatement change current Session state without rewriting that history.
Crew Notes and Attachment contents are not duplicated into the snapshot; it retains references to the exact immutable Attachment versions used.

Reinstatement requires a Placement Preview before it takes effect.
The Crew Member may fit the Session into a gap, move it to another Lane and Location, or ripple eligible Soft Boundaries.
The preview identifies affected Sessions, prevents silent Location collisions, and applies the existing warned confirmation before moving any Hard Boundary.

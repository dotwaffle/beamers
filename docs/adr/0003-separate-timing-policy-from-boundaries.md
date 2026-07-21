# Separate Timing Policy from boundary rigidity

Each Session has a Fixed End, Fixed Duration, or Manual End Timing Policy for its live countdown and independently hard or soft start and end boundaries for downstream recalculation.
Automatic timing adjustments may move soft boundaries but never hard ones.
An authorized Crew Member may still extend a live Session across a hard boundary after the application previews the impact, warns clearly, and receives explicit confirmation.

Adjust Target may move a Live Session's target later or earlier using Event-configurable presets or a custom value.
It previews downstream impact and uses the same hard-boundary warning.
It cannot set the target before current server time; the operator uses End Now to end immediately.
After a successful adjustment, each affected Stage Timer briefly shows a Timer Adjustment Notice such as "Time adjusted: +5:00" or "-2:00" before returning to its normal clock.
This reflects timer state and is not a Stage Message.

Fixed Duration adds real elapsed duration to a Session Run's Actual Start.
Fixed End targets a resolved absolute instant.
Both rules and their countdowns therefore remain correct across daylight-saving clock changes.

Reaching zero does not implicitly end a Session unless that Session's optional automation is enabled.
While it remains Live, Stage Timer continues upward as clearly styled positive overtime such as `+00:01`, preserving the difference between a timing target and actual live state.

For Manual End, Stage Timer shows elapsed time since Actual Start as its primary clock.
Any Forecast End remains visible only as separate context and is not presented as a countdown target.

An Event defines default remaining-time Timer Thresholds and their accessible Emphasis.
Session types and individual Sessions may override that list.
A threshold longer than the live target duration is ignored, allowing the same defaults to serve both short and long Sessions without firing immediately.
Version one uses accessible visual changes only and emits no threshold sounds; deliberate audio or external cue integrations remain future work.

Ending a Session early changes its actual end only; it does not automatically move later Sessions.
The Crew Member may separately pull later Sessions forward, which recalculates only eligible soft boundaries.

Automatic compression cannot reduce a Session below its Minimum Duration.
The minimum initially equals the planned duration, making Sessions non-compressible unless an organizer explicitly allows compression for an individual Session or through a Session-type default.

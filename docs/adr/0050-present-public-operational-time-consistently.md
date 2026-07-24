# Present public operational time consistently

Every attendee-facing surface uses one Public Time Presentation to choose semantic times and labels from Planned Time, Forecast Time, Actual Time, Communicated Time, and the Public Schedule Baseline.
The presentation does not choose which Sessions a View includes or format times for locale and timezone.
This supersedes ADR 0004 only where its public-presentation rules derive "was" from Planned Time.

Scheduled Sessions present Forecast Time.
Live Sessions present Actual Start normalized to Communicated Time by the Public Time Tolerance, together with Forecast End; a surface may express that end as an absolute time or time remaining.
Ended Sessions present normalized Actual Time.
Canceled Sessions present their last Forecast Time rather than partial Actual Time.
For Live and Ended Sessions, tolerance eligibility uses the immutable Run Snapshot duration.
Without Communicated Time, presentation uses exact Actual Time instead of inferring a comparison time.
Impossible lifecycle state is an error: public Schedule becomes unavailable, Displays retain their last valid frame, and the failure is observable rather than silently falling back to Forecast Time.

An Event may have one Public Schedule Baseline.
A Producer captures it from the current Published Revision when attendee-facing venue operations open, using a preview followed by explicit confirmation.
The preview identifies the Event and every Public Session with its Forecast Start.
Capture records all eligible Sessions or none, and an empty baseline is valid.
The Event need not be Active, but confirmation for a non-Active Event requires an additional acknowledgment naming it.
Capture is final rather than staging; it cannot be repeated or edited.
If the Published Revision changes after preview, confirmation fails as stale.

The baseline belongs to the Event rather than a revision and contains only each Session's Forecast Start.
A Session first made Public after capture receives its baseline atomically with that publication.
An existing entry never changes when later revisions move the Session, including across Event Days, or when the Session leaves and returns to public visibility.
The capture command is audited with actor, timestamp, source Published Revision, and Session count.
A dedicated Public Schedule Baseline command module owns preview and capture, while the pure Public Time Presentation module only interprets facts.
Rundown publication adds later baseline entries through the shared persistence invariant rather than invoking the capture command.

An included Scheduled, Live, Ended, or Canceled Session is eligible to show its baseline start as "Was:" when it renders differently from the current presented start.
The baseline always means the first captured attendee-facing start, never Planned Time or the immediately previous Forecast Time.
Each surface may omit "Was:" when space is constrained.
A missing baseline does not block publishing or public presentation; it produces a prominent crew warning and no attendee-facing historical context.

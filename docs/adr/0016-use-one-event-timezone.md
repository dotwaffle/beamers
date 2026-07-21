# Use one canonical Event timezone

Each Event has one canonical IANA timezone.
Crew Members enter Planned Time in that zone, while Forecast Time and Actual Time are stored as absolute instants and rendered in the Event zone. iCalendar imports honor UTC and TZID values; floating values use the Event timezone with a warning.
Ambiguous or nonexistent local times around daylight-saving transitions require explicit resolution before Publish.
A repeated local time requires choosing its earlier or later occurrence; a nonexistent time is rejected until corrected.
Crew and public Schedules show the timezone abbreviation or UTC offset around a transition so the selected occurrence remains visible.
Session durations and countdowns use real elapsed time: Fixed Duration adds to Actual Start, while Fixed End points to an explicitly resolved instant.
The Event's Planned Date Range is advisory: a Session outside it requires prominent warning and confirmation but remains publishable for overnight overruns or later reinstatement.

Each Event also has an Event Day Boundary used only to group Sessions in Schedules and crew interfaces.
It defaults to local midnight but may be moved, for example to 06:00, so an after-midnight demoparty Session remains in the preceding program day.
The boundary never alters absolute timestamps or their true local calendar dates.
When a custom boundary groups after-midnight Sessions with the preceding Event day, the Schedule inserts an explicit midnight divider showing the new local calendar date.
If the configured boundary is nonexistent during a clock jump, it resolves to the first valid instant afterward; if it is repeated, it resolves to the later occurrence.
Publish and Activation Preflight show the resolved instant and warn the Crew Member.

Public browser Schedules default to Event time and may offer a clearly labeled viewer-local conversion.
Event-day grouping remains based on Event time, and the Event zone remains visible when conversion is enabled.
Venue Displays and crew interfaces always use Event time.

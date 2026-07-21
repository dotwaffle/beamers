# Provide a read-only public Schedule

The initial application serves a responsive, unauthenticated Schedule showing current and upcoming Sessions, Forecast Times, Locations, Lanes, and Tracks.
It is available on the Venue network; organizers may expose it externally as a deployment choice.
Attendee accounts, favorites, personalized notifications, feedback, and other conference-app features are out of scope.
The Schedule defaults to Event time and may offer attendee-local conversion without an Account; Event-day grouping remains in Event time.
Day, Location, Lane, and Track filters are encoded in shareable URLs rather than attendee accounts or server-side session state.
The unfiltered URL shows the complete current and upcoming Schedule.

The preferred v1 delivery is a cacheable HTTP snapshot with conditional polling about every fifteen seconds.
Immediate push remains reserved for Crew and Displays.
This public transport is not a domain constraint: server-sent events or WebSockets may replace polling if implementation or deployment evidence makes them preferable without changing visible Schedule semantics.

Each Public Session has a stable deep link that survives renaming, retiming, cancellation, and reinstatement.
A Public Canceled Session and its message remain available there.
If its Audience Visibility changes to Crew Only, the public URL returns the same generic not-found response as an unknown Session and reveals no details.

Attendee Accounts, admission vote keys, Competition voting methods, and vote tallying are explicitly deferred beyond the initial release.
Their possible future introduction does not justify adding attendee or voting state to v1; crew-managed Prizegiving and public Results remain in scope.

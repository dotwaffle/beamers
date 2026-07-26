# Provide a read-only public Schedule

Superseded by ADR 0053 for attendee scope and public transport.
Its Schedule content, stable-link, and non-disclosure rules remain in force.

The initial application serves a responsive, unauthenticated Schedule showing current and upcoming Sessions, Forecast Times, Locations, Lanes, and Tracks.
It is available on the Venue network; organizers may expose it externally as a deployment choice.
ADR 0054 adds optional Accounts and Favorite Sessions without making them prerequisites for this Schedule.
Personalized notifications, feedback, and other conference-app features remain out of scope.
The Schedule defaults to Event time and may offer attendee-local conversion without an Account; Event-day grouping remains in Event time.
Day, Location, Lane, and Track filters are encoded in shareable URLs rather than server-side preference state.
The unfiltered URL shows the complete current and upcoming Schedule.

Complete public pages remain cacheable snapshots.
ADR 0053 uses revisioned SSE invalidation to update an open Schedule and recover from complete snapshots.

Each Public Session has a stable deep link that survives renaming, retiming, cancellation, and reinstatement.
A Public Canceled Session and its message remain available there.
If its Audience Visibility changes to Crew Only, the public URL returns the same generic not-found response as an unknown Session and reveals no details.

ADR 0054 adds optional attendee Accounts and ADR 0055 adds voting.
Anonymous Schedule browsing remains in scope and unchanged.

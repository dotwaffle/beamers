# Make the browser Frontend primary

Beamers version one is a browser-first application.
Every attendee and Crew workflow is completable through one responsive shell.
The root route lists public Events, features the public Active Event, and offers sign-in or Account controls.
An unbootstrapped installation instead exposes the one-time host-authorized setup flow.

Public Event visibility is independent of Active Event authority.
A Producer controls whether their Event appears in the Public Event Listing.
Public URLs use an Event Slug rather than a numeric identifier.
Renaming creates an Event Slug Alias that redirects until an Administrator explicitly prunes it.

Server-owned HTML and ordinary HTTP forms are the baseline.
Htmx supplies progressive partial-page behavior and its official extension consumes revisioned SSE updates.
Complete snapshots remain authoritative and restore state after reconnect or a detected gap.
Public browsing, sign-in, submissions, and Backstage forms work without JavaScript where live behavior is not essential.
Voting and Displays may require JavaScript.

Frontend routes and authorized Backstage routes form separate network Interfaces even when one listener serves both.
An installation may expose the Frontend while keeping Backstage private.
HTTPS is the default but not mandatory.
Explicit insecure non-loopback operation carries persistent warnings and loses browser capabilities that require secure contexts.

This supersedes ADR 0019's read-only attendee scope and conditional-polling preference.
It extends ADRs 0020, 0038, and 0039 without changing their route-authorization, snapshot, or server-rendering boundaries.
The added Account and voting behavior is specified separately because it has independent identity and tallying consequences.

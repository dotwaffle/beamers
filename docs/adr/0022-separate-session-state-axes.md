# Separate Session state axes

A Session has independent publication, lifecycle, and Audience Visibility.
Draft and Published describe whether structural information is authoritative.
Scheduled, Live, Ended, and Canceled describe runtime lifecycle.
Public and Crew Only determine whether a Published Session appears on audience surfaces.
Upcoming, due, late, and overrunning remain derived timing labels.
This permits, for example, a Published, Crew Only soundcheck to become Live without appearing on public Views.
A Published, Public Session remains on the Schedule when Canceled so attendees can see what changed, but it no longer participates in Location Signage's Now/Next calculations.
It is removed from public surfaces only by separately changing its Audience Visibility to Crew Only.
Cancel Session applies to Scheduled and Live Sessions; canceling a Live Session ends its current Session Run without introducing a separate Aborted lifecycle state.

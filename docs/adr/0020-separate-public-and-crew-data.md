# Separate public and crew data

Sessions and Entries distinguish explicit Public Details from Crew Notes.
Public Details may include titles, descriptions, displayed participants or authors, Tracks, Locations, and audience timing.
Contact information, technical requirements, Crew Notes, file paths, and internal status remain crew only.
Unknown imported fields default to crew only, and Publish previews exactly what becomes public.
A Crew Only Session is omitted from the public Schedule, but public Location Signage shows a neutral room-unavailable notice while that Session occupies its Location.

The single Go service may expose optional separate network listeners.
A public listener serves only allowlisted unauthenticated public routes such as Schedule and released Results, while Crew and Display routes remain on a private listener.
A simple venue may use one listener, but external publication should normally route only the public listener through its proxy or firewall.
This is route isolation within one active service, not a second public process.

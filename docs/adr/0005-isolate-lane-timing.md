# Isolate timing within each Lane

An Event has one Rundown divided into one or more Lanes, each representing an independently progressing sequence of Sessions such as a room or stage program.
Extensions, compression, and Pull Forward normally recalculate only the affected Lane, so delay in one room cannot silently move another room.
A Shared Session may be included in selected Lanes as one entity with common timing and live state, anchoring those Lanes to boundaries such as the end of lunch.
Tracks are thematic groupings only and do not participate in timing.
Each Lane is bound to exactly one Location, and a Location has at most one Lane in a Rundown.
A later Draft may reassign a Lane to another Location without replacing Lane identity; Publish validates the resulting one-to-one bindings and rejects changes unsafe for current Live Sessions.
A venue area hosting simultaneous programs is represented by named sub-Locations rather than parallel Lanes claiming the same Location.
Session Locations are explicit and separate from Lane membership, defaulting to the Lane's Location.
This lets a Shared Session synchronize several Lanes while occurring elsewhere; its explicit Locations drive occupancy and Location Signage directions.

# Activate one Event at a time

One installation stores multiple past, current, and future Events but designates exactly one Active Event to drive live commands and Display output.
This allows preparation and historical access without making targeting, permissions, and recovery ambiguous.
Organizations needing simultaneous independent Events run separate installations.
Activation is a routing and readiness designation that may occur days before the planned Event date range; it does not start or end an Event, and v1 has no separate Event lifecycle.
Changing the Active Event does not archive or lock the previous Event; activation governs live authority only.
A switch is blocked while Sessions or Overrides are active unless an Administrator explicitly confirms the warned override.
Active Event selection is an installation-level Administrator action rather than an Event permission.

Display Enrollment persists across Events, but Assignment to a Location and View belongs to one Event.
Activating an Event never silently inherits another Event's Assignment.
Any unassigned Display presents Standby and is highlighted to Crew Members; setup may explicitly copy Assignments from another Event.

Activation Preflight blocks an Event with no valid Published Rundown, invalid references or timezone, or an assigned View that cannot render.
Unassigned or offline Displays, empty Lanes, and suspicious Event dates produce warnings but do not block.
The existing guard for Live Sessions and active Overrides is separately forceable by an Administrator.

After preflight, activation commits a single new activation generation.
Connected Displays switch directly to their assigned View without going blank, while unassigned Displays show Standby.
Each Display acknowledges the generation and the crew dashboard reports its status.
An offline Display necessarily retains its last-known output and disconnection indicator, then adopts the new Active Event immediately after reconnecting.

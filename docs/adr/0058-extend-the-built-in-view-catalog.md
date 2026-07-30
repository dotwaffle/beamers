# Extend the built-in View catalog

The built-in View catalog gains Timeline and Crew Overview.
Both answer questions the existing catalog answers poorly: how the whole Event day is tracking, and what a crew member in a back room needs without a control surface.

Timeline presents the Event day as proportional blocks rather than rows.
Its Regions are a persistent branding header, a persistent Now/Next summary, a persistent Timeline Widget, and a persistent digital clock.
Proportional blocks communicate compression and slippage at a glance where a list communicates order only.
Timeline is an audience-facing View and applies the same Crew Only suppression as Location Signage: it shows that a span is unavailable and until when, never the Session occupying it.

Crew Overview presents current Session timing and progress, the next Session, and a compact Schedule.
Its Regions are a persistent branding header, a persistent Stage Timer, a persistent Now/Next Region, a rotating Schedule Region, and a persistent digital clock.
It carries no controls, so it remains a Display rather than a console.
Stage Timer already serves a presenter looking at one countdown; Crew Overview serves a technician who also needs to know what follows.

The catalog remains closed.
These are built-in Views with fixed Layouts and Regions, not a step toward the deferred visual Layout and slide-template editor.
Version one still guarantees landscape sixteen-by-nine Layouts from 720p through 4K and safe degradation on sixteen-by-ten.

Each new View is assignable, may specialize an Event Theme variant, and participates in Override targeting and resolution exactly as the existing Views do.

This extends ADR 0018.
It preserves that decision's Layout and Region separation, rotation model, Override composition, and deferral of authored Layouts.

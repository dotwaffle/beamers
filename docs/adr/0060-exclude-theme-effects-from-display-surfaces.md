# Exclude Theme Effects from Display surfaces

Theme Effects (the ambient ornamental animation a Theme may select, such as the starfield) do not apply to Display surfaces.
A Display renders the Nebula backdrop, its Region fills, and its content type scale from the active Event Theme's palette, but never plays that Theme's Effect or carries its Motion level as anything beyond the transition it already derives (fade or none).

A Display is unattended, runs for the length of an Event, and is watched from across a room rather than operated at arm's length.
An ornamental animation designed for a browsing visitor adds moving content a venue cannot pause, has no accessibility control surface to suppress locally beyond the browser's own reduced-motion preference, and competes for attention with the schedule information the Display exists to show.
The Nebula backdrop already supplies a Display-appropriate ambient texture derived from the Theme's surface color via `color-mix`; a second, separate decorative animation on top of it is redundant motion, not an accessibility feature.

This has been the actual behavior since Displays first read Event Themes: `displayviews.Theme`, the struct a Display renderer consumes, has never carried an Effect field, so a Theme's `Effect` and `Motion` selections were already silently dropped when composing a Display's presentation.
This ADR records that omission as a deliberate decision rather than an oversight, so a future change does not thread `Effect` onto Displays believing it closes a gap.

Reduced Effects and browser reduced-motion continue to suppress the transition and progress-bar motion a Display does render, unchanged by this decision.

This extends ADR 0056.
It preserves that decision's Theme inheritance, contrast gating, and reduced-motion behavior, and narrows which of a Theme's fields reach a Display surface.

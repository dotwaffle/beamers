# Compose Views from Regions

A View uses a Layout of named Regions, each containing a Widget.
A Rotation Widget cycles through ordered View Pages while other Widgets remain persistent.
The outside-Location signage preset gives roughly seventy percent of the screen to rotating Event content while keeping the Location name, Now/Next information, and a digital clock continuously visible.
Overrides cover this composition without altering it.

The initial release provides responsive built-in Layout presets with configurable Regions and rotation timing.
An Event Theme controls branding, colors, fonts, backgrounds, and transitions.
A visual Layout and slide-template editor is an intended post-version-one feature.
Its authoring model and whether it permits arbitrary HTML or CSS remain deferred so version one preserves rendering reliability and limits its security and support surface.

The initial built-in View catalog is Event Overview, Location Signage, Stage Timer, Competition Output, and Standby.
Technical Difficulties and alerts remain Overrides.
A later Live Feed Widget may occupy the rotating Region of Location Signage when a Venue video source is healthy, falling back to its normal rotation when unavailable while persistent border Widgets remain visible.
Video protocol selection is deferred.

Version one guarantees landscape 16:9 Layouts from 720p through 4K and degrades safely on 16:10 Displays.
Portrait-specific and arbitrary-aspect Layouts are deferred.

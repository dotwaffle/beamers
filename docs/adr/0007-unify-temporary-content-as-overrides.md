# Unify temporary content as Overrides

Urgent messages, technical-difficulties slides, and similar temporary content use one typed Override mechanism.
An Override targets the Event, selected Locations or Lanes, or individual Displays; chooses a presentation such as fullscreen or lower third; and restores each Display's assigned View when cleared.
Fixed type priorities prevent routine content from hiding urgent information.

Applicable Overrides remain in a priority stack for each Display.
Clearing the visible Override reveals the next-highest active Override, or the assigned View when none remain, instead of discarding unrelated temporary content.

Overrides target logical Display Groups, including Event-wide, public, crew, Location, Lane, Program Channel, custom, and individual scopes.
A Program Channel resolves to every Display currently consuming its Program Output.
The Crew Member sees the resolved Displays before activation so private or urgent content is less likely to reach the wrong audience.

Logical targets remain live while an Override is active.
Event, public, crew, Location, Lane, Program Channel, and custom Display Group membership is continuously re-resolved: a Display joining the scope receives the Override and one leaving it loses the Override.
A direct individual-Display target remains fixed.
The crew dashboard keeps showing the current resolved set and reports membership changes after activation.

An Emergency Alert is a distinct highest-priority, persistent fullscreen type whose clearing requires confirmation.
It supplements rather than replaces the Venue's emergency systems.
An Urgent Notice carries non-safety operational information, may be fullscreen or a banner, and may expire automatically.

Each Override chooses Replace or Overlay presentation.
Replace suppresses lower-priority content with a fullscreen presentation.
Overlay composes a banner or lower third over the otherwise visible lower-priority content.
This allows an Urgent Notice to appear above a technical-difficulties replacement while an Emergency Alert still suppresses everything below it.

A fullscreen Replace Override pauses an active timed Result Reveal only when it covers every Display consuming that Program Channel.
Clearing the coverage resumes the Reveal from its remaining duration.
Partial coverage and Overlay Overrides do not pause it.
Session timing and other Event clocks continue normally beneath every Override.

A Stage Message is a one-way Overlay restricted to crew Display Groups.
Each Display has at most one active Stage Message; a newly delivered message replaces the one currently visible there rather than queuing behind it.
Messages auto-clear after an Event-configurable duration of ten seconds by default, or may be sent Until Cleared.
The system confirms delivery to the Display and retains crew history, but does not require a human acknowledgment in the initial design.

An Event may define Stage Message Presets containing text plus target and duration defaults for quick live use.
Presets do not prevent an authorized Crew Member from composing a free-form Stage Message.

A Stage Message chooses Normal, Attention, or Urgent Emphasis, and a Preset may provide its default.
Emphasis changes accessible visual styling, using more than color alone, but does not change Override priority or imply Emergency Alert semantics.

Technical Difficulties is a display-only Replace Override.
Activating or clearing it does not pause, extend, start, or end a Session; Crew Members make any timing changes separately.
A later preset may combine explicit actions without coupling their underlying behavior.

Activating an Emergency Alert first previews its content and resolved targets, then requires a deliberate two-second hold or an explicit keyboard-accessible confirmation.
Clearing it requires a separate confirmation.
Two-person approval is not required, preserving response speed for small Events.

# Render Views in browsers

Displays use a fullscreen browser to load a View, receive live Event state, and render locally.
This supports commodity TVs through attached browser-capable devices and single-board computers while keeping bandwidth low and allowing each Display to show distinct content.
A centrally rendered video output may be added later as an integration for broadcast workflows, rather than becoming the primary Display protocol.

Documented Chromium kiosk operation on Raspberry Pi and ordinary Linux devices is the version-one reference Display environment.
A dedicated Chromecast receiver is a version-one stretch goal: it may ship if its platform lifecycle is tractable, but deferring it does not block version-one acceptance.
A managed SBC kiosk image is likewise not required for version one.

The reference renderer uses browser-native HTML, CSS, and a small JavaScript module for live updates, clocks, and presentation effects.
Go WebAssembly is not a version-one dependency: the standard browser target still requires a JavaScript bootstrap and browser interop, while adding another binary/runtime artifact to cache, diagnose, and update.
WASI targets non-browser hosts and does not provide the browser DOM.
The Display state protocol remains renderer-neutral so a later Go WebAssembly experiment or alternate native receiver can be tested without changing server semantics.

Version one certifies the current and previous major Chromium and Firefox releases for Crew and control Views, with Chromium kiosk as the Display reference.
Public Schedule and phone-based Display Enrollment additionally certify current Safari.
All Crew controls work with both touch and keyboard.

If a Display loses its server connection, it keeps its last known View and continues timers from absolute timestamps rather than going blank.
It shows a corner disconnection indicator, while the crew dashboard reports the Display as offline.
The indicator may pulse slowly to demonstrate that rendering is still alive, but must become static when reduced motion is requested.
A full snapshot replaces stale state after reconnection before live updates resume.
If the Active Event changed while disconnected, that snapshot switches the Display directly to the new Event's assigned View or Standby.

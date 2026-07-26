# Version controlled Themes

Beamers ships a polished demoscene-forward base Theme rather than treating visual design as optional customization.
It self-hosts Chakra Petch for headings and Open Sans for body text.
Version one selects from bundled fonts and does not accept font uploads.

The base feeds an Installation Theme for root, authentication, Public Profiles, and the Backstage shell.
An Event Theme inherits from it for public pages and Displays.
Controlled Event variants may specialize signage surfaces.
Themes configure design tokens, branding assets, backgrounds, bundled type choices, transitions, effect presets, and motion level.
They cannot inject arbitrary HTML, CSS, or JavaScript.

Editing creates an immutable Draft Theme Revision.
Preview renders representative browser pages and Displays.
Activation selects one revision and blocks known contrast or legibility failures.
Rollback activates an earlier revision.
Emergency Alerts always use a certified built-in presentation.

Animation is decorative and subordinate to access needs.
Ambient effects expose a visible pause control.
Browser reduced-motion and forced-color preferences override Theme choices automatically.
Reduced Effects is also a persistent visitor preference and a Display setting.
Accessibility is part of every Theme rather than a separate overlay or plain rescue mode.

This extends ADRs 0018 and 0032.
It preserves their Layout separation, contrast requirements, reduced-motion behavior, and certified Emergency Alert presentation.

# Ship a neutral base Theme with bundled Presets

The built-in base Theme is neutral and venue-agnostic rather than demoscene-forward.
It presents a precise, high-contrast, low-ornament dark surface that a conference can ship unchanged.
Beamers serves demoparties and conferences equally, so privileging one visual culture in the shipped default made the other reconfigure before its first Event.

Beamers bundles named Theme Presets.
A Preset is a fixed, reviewed `Config` value carrying colors, background, bundled type choice, transition, effect, and motion level.
Version one bundles Base, Demoscene, and Conference.
Demoscene restores the saturated accent, bundled display heading face, decorative background, and starfield that the earlier default assumed.

Selecting a Preset populates a Draft Theme Revision with that Preset's values.
It introduces no new rendering path, no new stylesheet, and no new Theme field.
The Producer or Administrator may then edit any populated value before activating.
Preset selection is an authoring convenience, not a separate presentation mode, so a Revision created from a Preset is indistinguishable from one typed by hand.

Presets are subject to every existing Theme rule.
Activation still blocks known contrast and legibility failures.
Themes still cannot inject arbitrary HTML, CSS, or JavaScript, and still select only bundled fonts.
Emergency Alerts still use the certified built-in presentation regardless of the selected Preset.

Spacing scale, type scale, radii, elevation, and interface density are fixed in the base stylesheet and are not Theme fields.
They are craft rather than branding, and freezing them keeps the legibility guarantees of ADR 0032 enforceable.
A Theme that could resize or re-space text could render an interface unreadable without failing any contrast check.

Semantic state colors for live, warning, danger, and success are Theme fields, because Event brands do disagree about them.
Each ships with a fixed ink color and is used as a filled surface behind that ink, never as small text over a Theme-controlled surface.
Activation validates each state color against its own ink and against the background and surface colors.

This extends ADR 0056.
It preserves that decision's Revision model, inheritance, preview, rollback, contrast gating, prohibition on arbitrary code, and reduced-motion behavior.

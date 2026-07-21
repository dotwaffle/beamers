# Target WCAG 2.2 Level AA

Version one targets WCAG 2.2 Level AA for public Schedule and Results, phone-based Display Enrollment, and all Crew interfaces.
This includes semantic structure, keyboard operation without traps, visible focus, programmatic labels, accessible validation errors, sufficient contrast, non-color cues, reduced motion, appropriate language metadata, and usable touch targets.

Non-interactive Display Views apply every relevant success criterion, especially contrast, text legibility, non-color status communication, and reduced motion.
Criteria that require interactive page structure do not create artificial controls on unattended signage.

Theme Preview validates configured foreground/background pairs against the AA contrast requirements and blocks activation when a known pair fails.
It reports the failing pair and may suggest alternatives, but never silently changes the Event's brand colors.

Text-bearing Regions over image or video backgrounds require a configured Contrast Scrim strong enough to satisfy the same checks; the application does not pretend static pixel sampling can guarantee every frame.
Emergency Alerts use a built-in certified presentation instead of Event imagery or unverified Theme color combinations.

Accessibility is part of version-one acceptance rather than optional polish.
Automated checks are necessary but insufficient; representative keyboard, screen-reader, touch, zoom, contrast, and reduced-motion scenarios require manual validation on the certified browser matrix.

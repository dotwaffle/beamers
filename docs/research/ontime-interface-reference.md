# Ontime interface reference

Surveyed 2026-07-30 against Ontime's official website, documentation, screenshots, and source.
This is a frontend/design reference for Beamers, not a proposal to copy Ontime's architecture or dependencies.

## Overall model

Ontime presents one rundown through separate surfaces for production teams (Editor, Cuesheet, Operator) and automated displays (Stage Timer, Backstage, Countdown, Studio, Timeline).
The separation lets each role see the same event data at an appropriate information density.
[Official view gallery](https://www.getontime.no/#features)
[interface documentation](https://docs.getontime.no/interface/overview/)

## Useful patterns

### Stage timer: one dominant fact

- The Stage Timer makes the running timer overwhelmingly dominant, then layers in a small wall clock, progress, and current/next cards.
  Its source keeps those elements independently hideable and swaps the main timer for a full-screen message when needed.
  [screenshot](https://www.getontime.no/images/screenshots/slide-timer.png) [view source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/timer/Timer.tsx#L149-L213)
- Warning, danger, pause, and overtime are state changes on the timer itself: color changes for warning/danger, reduced opacity when paused, and an overtime color plus viewport outline when finished.
  This preserves the numeric layout while making state visible at distance.
  [timer styles](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/timer/Timer.scss#L20-L24) [phase styles](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/timer/Timer.scss#L101-L132)
- The timer can count up, count down, or show a clock; overtime may continue,
  freeze at zero, or freeze into a short message.
  [timer options](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/timer/timer.options.ts#L20-L78)

### Clocks: status around the clock, not inside it

- The Studio view pairs a large radial wall clock with a separate `ON AIR`
  indicator, leaving the clock face itself uncluttered.
  [screenshot](https://www.getontime.no/images/screenshots/slide-studio.png)
  [clock source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/studio/StudioClock.tsx#L21-L60)
- Its adjacent cards summarize started time, over/under, expected end, now/next, the running timer, three auxiliary timers, and both message channels.
  This is a compact production overview rather than a presenter display.
  [studio cards](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/studio/StudioTimers.tsx#L19-L118)

### Operator and production UX: dense, followable, recoverable

- The Operator view is designed for phones and tablets, automatically follows
  playback, and permits role-specific titles, notes, or custom fields.
  [Operator documentation](https://docs.getontime.no/interface/production/operator/)
- Manual scrolling temporarily disengages automatic following; a visible follow control returns the operator to the live item.
  That makes the automatic behavior recoverable instead of trapping the user.
  [follow behavior](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/operator/Operator.tsx#L44-L105) [follow control](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/operator/Operator.tsx#L239-L242)
- Rows expose direct operational states—`LIVE`, `DONE`, `DUE`, or time until
  expected start—beside cue, schedule, duration, delay, and subscribed fields.
  [row source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/operator/operator-event/OperatorEvent.tsx#L89-L142)
  [state labels](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/operator/operator-event/OperatorEvent.tsx#L155-L201)
- The full Editor screenshot uses three simultaneous work areas: transport and timer/message controls, rundown navigation, and selected-event details.
  The lighter Operator view removes editing chrome while retaining live context and departmental fields.
  [Editor screenshot](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/views/1-editor.png) [Operator screenshot](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/views/3-op.png)

### Backstage and schedule layouts: progressive disclosure by audience

- Backstage combines current-event timing, next event, a compact schedule,
  progress, and optional project information; it omits the Editor's controls.
  [screenshot](https://www.getontime.no/images/screenshots/slide-backstage.png)
  [view source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/views/backstage/Backstage.tsx#L106-L184)
- Timeline trades detailed rows for proportional blocks across the workday,
  while keeping live/next/following summaries above the timeline.
  [Timeline documentation](https://docs.getontime.no/interface/automated/timeline/)
  [screenshot](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/views/6-timeline.png)
- Planned times remain the authored schedule; expected times combine current offset with upcoming durations.
  Ontime displays both so a production can retain the plan while communicating the current trajectory.
  [timer concepts](https://docs.getontime.no/concepts/timers/) [delay management](https://docs.getontime.no/quick-tips/managing-delays/)

### Messages and alerts: explicit escalation levels

- Ontime separates a full-screen Timer Message from a smaller Secondary
  Message, with independent visibility controls.
  [message controls](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/control/message/MessageControl.tsx#L22-L70)
- Operators can additionally blink the active timer content or black out the display.
  The control panel previews timer type, phase, message, secondary source, blink, and blackout before or while changing them.
  [control source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/control/message/TimerViewControl.tsx#L12-L92) [preview source](https://github.com/cpvalente/ontime/blob/master/apps/client/src/features/control/message/TimerPreview.tsx#L22-L109)
- Secondary text can carry a quiet presenter reminder or data from another
  system, while the full-screen channel remains available for urgent
  intervention.
  [secondary-message documentation](https://docs.getontime.no/features/secondary-message/)

## Screenshot set

- [Stage Timer](https://www.getontime.no/images/screenshots/slide-timer.png):
  long-distance hierarchy and now/next context.
- [Studio](https://www.getontime.no/images/screenshots/slide-studio.png):
  clock, on-air state, schedule health, and message visibility.
- [Backstage](https://www.getontime.no/images/screenshots/slide-backstage.png):
  crew-facing progress plus schedule.
- [Editor](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/views/1-editor.png):
  high-density production workspace.
- [Operator](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/views/3-op.png):
  live-following rundown with departmental fields.
- [Delay management](https://github.com/cpvalente/ontime-docs/blob/main/src/assets/screenshots/features/rundown__delay-management.png):
  planned, expected, offset, and scheduled-delay vocabulary in context.

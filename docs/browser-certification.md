# Browser and Accessibility Certification

The `Browsers` workflow runs the current and previous major Chromium and Firefox releases against real served Beamers pages.
It records the exact browser, driver, runner, and commit versions in downloadable JSON evidence.

The hosted check covers public Schedule, Enrollment controls, Crew Program control, Display rendering, keyboard activation, visible focus, labels, touch targets, contrast, language metadata, reduced motion, non-color connection state, retained content during disconnect, and recovery after a compatible server restart.

Hosted evidence does not certify Safari, kiosk fullscreen, kiosk hardware, an actual version upgrade, screen readers, touch input, or browser zoom.

## Run the hosted check locally

With Docker or Podman:

```sh
scripts/check-browser-chromium.sh
```

Set `CONTAINER_RUNTIME=podman` to use Podman.
Set `BEAMERS_SELENIUM_IMAGE` to test a specific Selenium Chromium image.
Set `BEAMERS_BROWSER_RUNS` to repeat the certification against one container.

Install a supported browser and its W3C WebDriver, then run:

```sh
BEAMERS_BROWSER_CERTIFICATION=1 \
BEAMERS_BROWSER_ENGINE=chromium \
BEAMERS_BROWSER_ROLE=current \
BEAMERS_BROWSER_MAJOR=147 \
BEAMERS_BROWSER_BINARY=/path/to/chrome \
BEAMERS_WEBDRIVER_BINARY=/path/to/chromedriver \
BEAMERS_BROWSER_REPORT=artifacts/browser.json \
go test ./acceptance -run '^TestBrowserCertification$' -count=1 -timeout 25m -v
```

Use `firefox` and `geckodriver` for Firefox.
Never edit or combine reports: each run creates one new file and refuses to overwrite existing evidence.

## Current Safari manual run

Use the current production Safari release on an iPhone-sized viewport and record:

- Safari, operating-system, Beamers, and commit versions.
- Public Schedule navigation, language, contrast, legibility, 200% and 400% zoom.
- Display Enrollment code presentation and Administrator claim form.
- Native validation announcements, labels, visible focus, and 44 CSS-pixel targets.
- Touch input, reduced-motion behavior, and VoiceOver reading order.
- Screenshots or video plus a written pass/fail result for every check.

## Real Chromium kiosk run

Use production-equivalent Display hardware and Chromium, not a protocol-only client:

```sh
chromium \
  --kiosk \
  --no-first-run \
  --user-data-dir=/var/lib/beamers-kiosk \
  https://beamers.example/display
```

Record hardware, operating-system, Chromium, Beamers, and commit versions.
Verify:

- Fullscreen startup with no browser chrome.
- The last committed content remains visible during a server disconnect.
- Connection state is understandable without color.
- The Display reconnects after a compatible server restart.
- The Display reloads the new client after a real Beamers upgrade.
- Keyboard escape paths and operating-system recovery remain available to authorized operators.

Keep timestamps, screenshots or video, server logs, browser logs, and a written pass/fail result.

## Representative manual accessibility review

Across Schedule, Enrollment, Crew control, and Display, review:

- Keyboard order, operation, traps, and visible focus.
- VoiceOver plus one desktop screen reader appropriate to the deployment.
- Labels, instructions, validation errors, and status announcements.
- Touch targets and touch-only operation.
- Reflow and legibility at 200% and 400% zoom.
- Text and non-text contrast, including configured Display themes and scrims.
- Non-color status, document language, reading order, and reduced motion.

Automated results support this review; they do not replace it.
Attach the completed manual and kiosk evidence to the certification issue before closing it.

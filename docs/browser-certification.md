# Browser and Accessibility Certification

The `Browsers` workflow runs the newest two major Chromium and Firefox releases available as official Selenium standalone images against real served Beamers pages.
It records the exact browser, driver, runner, and commit versions in downloadable JSON evidence.

The hosted check renders anonymous, attendee, voter, Producer, Operator, Administrator, Display, Theme, submission, and released Results surfaces against a representative demo installation.
It creates a submission and saves a vote through the served browser interface.
It also covers public Schedule and released Results, Enrollment controls, an Override targeting confirmation, and two simultaneous Crew consoles observing a Program Claim, Preview selection, and durable Take to two connected browser Displays.
It starts a timed Result Reveal, proves an Emergency Alert pauses it beyond its original duration on both Displays, then clears the Alert and proves the Reveal resumes.
It requires both Displays to render and acknowledge the exact committed Program Output.
It also covers keyboard activation, visible focus, labels, touch targets, contrast, forced colors, language metadata, pause controls, 200% zoom, reduced motion, non-color connection state, retained content during disconnect, and recovery after a compatible server restart.
The JSON report fails validation unless every required surface is present and identifies the exact certified commit.

Hosted evidence does not certify Safari, kiosk fullscreen, kiosk hardware, an actual version upgrade, screen readers, touch input, or 400% zoom.

## Role-to-workflow matrix

Every hosted role journey starts at `/`, then follows visible rendered links.
Hard-coded deep URLs may set up responsive checks, but they do not count as workflow discovery evidence.

| Role | Rendered-link journey | Command and authorization evidence |
| --- | --- | --- |
| Anonymous attendee | Events → Event overview → Schedule, Competitions, Session, Competition, and Results | Public navigation and non-disclosure: `TestBrowserFollowsCanonicalPublicEventJourney`; registration and Reduced Effects: `TestBrowserSetupAndSessionSurviveRestart` and `TestBrowserRegistrationProfileAndDisablement` |
| Account | Events → Profile, My Participation, and My Schedule | Profile, credentials, recovery, sign-out, and favorites: `TestBrowserSetupAndSessionSurviveRestart`, `TestBrowserWebAuthnCredentialsSurviveRestartAndRevokeIndependently`, `TestBrowserRegistrationProfileAndDisablement`, `TestBrowserRecoversAccountWithoutEmail`, and `TestBrowserBuildsPrivateMyScheduleFromFavoriteSessions` |
| Submitter Account | Events → My Participation → View Competition → Manage My Entry, or Schedule → Presentation | Entry submission, upload, Presentation upload, and Reopen Window access: `TestAccountSubmissionsHonorPolicyOwnershipAndReopenWindows` |
| Voting Eligible attendee | Events → My Participation → Vote | Voting Key redemption and Ballot save: `TestVotingKeysIssueRedeemAndSurviveRestart` and `TestLiveCompetitionBallotUpdatesAndSurvivesRestart` |
| Producer | Events → Backstage → Event overview, settings, Displays, Sessions and Competitions, live operations, Program Output, Results, Voting Keys, and Event Theme | Event, Rundown, Competition, live, Results, release, Display, and Theme commands: the Producer rows in the command inventory below |
| Scoped Operator | Events → Backstage → Event overview, Sessions and Competitions, live operations, and Program Output | Scoped controls succeed while Producer configuration is absent: `TestBackstageNavigationReflectsAuthorityAndInterface`, `TestBrowserOperatesSessionDurably`, `TestBrowserPreviewsAdjustsCancelsAndReinstatesSession`, and `TestBrowserControlsProgramOutputAndOverrides` |
| Observer | Events → Backstage → Event overview and Sessions and Competitions | Crew state remains visible while command controls and Producer destinations are absent: `TestBackstageNavigationReflectsAuthorityAndInterface` |
| Administrator | Events → Backstage → Create Event, Accounts and Event Grants, Installation Theme, Registration, and Backups and diagnostics; authorized Event → Final Files | Installation, Account, Grant, activation, Display, Theme, backup, restore, and export commands: the Administrator rows in the command inventory below |

The hosted real-browser report uses the `demo-anonymous`, `demo-attendee`, `demo-submission`, `demo-voter`, `demo-producer`, `demo-operator`, `demo-observer`, and `demo-administrator` surfaces for this matrix.
Producer and Operator journeys prove authorized Competition workflow links.
The Observer journey proves those command links are absent.
`TestBackstageNavigationReflectsEffectiveAuthority` and `TestBackstageControlHidesUnavailableActions` prove temporarily blocked authorized actions remain visible with reasons.
`TestBackstageNavigationReflectsAuthorityAndInterface` proves unauthorized routes and controls remain unavailable through the launched executable.

### Human command inventory

Adding or removing a human-facing command requires updating its owning row and acceptance test.
These tests drive the launched executable through served browser routes rather than application internals.

| Owning workflow | Successful command coverage | No-JavaScript baseline |
| --- | --- | --- |
| Account and identity | Setup, registration, sign-in, sign-out, Profile, recovery, Recovery Codes, password and WebAuthn credentials: `TestBrowserSetupAndSessionSurviveRestart`, `TestBrowserWebAuthnCredentialsSurviveRestartAndRevokeIndependently`, `TestBrowserRegistrationProfileAndDisablement`, and `TestBrowserRecoversAccountWithoutEmail` | All except WebAuthn registration and authentication; credential listing and removal work without JavaScript |
| Participation and voting | Favorites, Voting Keys, Ballots, Entry submissions, Presentation uploads, Attachments, and Reopen Windows: `TestBrowserBuildsPrivateMyScheduleFromFavoriteSessions`, `TestVotingKeysIssueRedeemAndSurviveRestart`, `TestLiveCompetitionBallotUpdatesAndSurvivesRestart`, and `TestAccountSubmissionsHonorPolicyOwnershipAndReopenWindows` | Favorites, Voting Keys, submissions, uploads, attachments, Reopen Windows, and Ballot forms; live Ballot updates use JavaScript |
| Event configuration | Create and update Event, slug aliases, Event settings, Display settings, Attachment release policy and cue, and activation: `TestBrowserPublishesEventsUnderCurrentSlugs`, `TestBrowserEventOverviewAndSettings`, `TestBrowserConfiguresEventDisplays`, `TestBrowserControlsEventAttachmentRelease`, and `TestBrowserPreflightsAndActivatesEvent` | Yes |
| Rundown and Competition | Draft edits, imports, Publish, Entry management, review, readiness, release policy, ordering, deferral, and resolution: `TestBrowserPlansAndPublishesEvent`, `TestBrowserManagesCompetitionEntries`, and `TestBrowserDefersAndResolvesCompetitionEntries` | Yes |
| Live operations | Start, End, target adjustment, pull-forward, Cancel, Reinstate, Program control, Preview, Take, Override, and Emergency Alert: `TestBrowserOperatesSessionDurably`, `TestBrowserPreviewsAdjustsCancelsAndReinstatesSession`, and `TestBrowserControlsProgramOutputAndOverrides` | Ordinary forms; Program streaming and Display rendering use JavaScript |
| Results and Prizegiving | Draft, Ready review, disposition, Prizegiving plan and preflight, reveal, publication, and correction: `TestBrowserStagesAndReviewsCompetitionResults` and `TestBrowserPublishesAndCorrectsStandaloneResults` | Yes |
| Themes | Installation Theme and Event Theme draft, preview, activation, rollback, inheritance, and validation: `TestAdministratorRevisesPreviewsActivatesAndRestoresInstallationTheme` and `TestProducerActivatesInheritedEventThemeAcrossPublicSchedule` | Yes |
| Installation administration | Accounts, Event Grants, registration policy, Active Event, Display Enrollment and assignment, backups, restore preparation, and diagnostics: `TestBrowserAdministersAccountsAndEventGrants`, `TestBrowserPreflightsAndActivatesEvent`, `TestBrowserAdministersDisplaysAndRecovery`, and `TestBackstageOperatesBackupsAndDiagnostics` | Yes |
| Final Files | Preview, digest-bound ZIP download, stale preview rejection, and archive verification: `TestBackstageExportsFinalFiles` | Yes |

### No-JavaScript acceptance

The full-process command tests above use ordinary HTTP navigation and form submission without executing page JavaScript.
This proves public browsing, setup, registration, sign-in, recovery, Profile, My Schedule, submissions, uploads, Event configuration, planning, Results, administration, backup, restore preparation, and other ordinary Backstage forms retain their server-owned baseline.
WebAuthn ceremonies, voting live updates, Program control streaming, and Display rendering receive separate JavaScript-enabled browser certification because their live behavior requires it.

## Release gate

The `Release` workflow requires all evidence to identify the exact candidate commit:

- a successful `Browsers` push run with current and previous Chromium and Firefox artifacts;
- a successful full `Capacity` dispatch with every representative, rated, and diagnostic stress artifact;
- closed manual certification issue #51 with a trusted `Certified commit: ...` line.

The release job then reruns the canonical full-process and deployment checks.
This keeps CSRF, session, route-interface, credential, federation, Ballot, public non-disclosure, live-stream, capacity, browser, and manual certification failures release-blocking.

## Run the hosted check locally

With Docker or Podman:

```sh
scripts/check-browser.sh
```

Set `CONTAINER_RUNTIME=podman` to use Podman.
Set `BEAMERS_SELENIUM_IMAGE` to test a specific Selenium image.
Set `BEAMERS_BROWSER_ENGINE=firefox` to use Firefox.
Chromium is the default.
Set `BEAMERS_BROWSER_ROLE` and `BEAMERS_BROWSER_MAJOR` to reproduce one hosted matrix entry exactly.
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
Do not edit or combine reports.
Each run creates a new file and does not overwrite existing evidence.
Local evidence also requires a clean worktree so the recorded commit identifies the tested source.

## Current Safari manual run

Use the current production Safari release on an iPhone-sized viewport and record:

- Safari, operating-system, Beamers, and commit versions.
- Public Schedule and released Results navigation, language, locale-sensitive presentation, contrast, legibility, 200% and 400% zoom.
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

Start every applicable journey at the root or signed-in landing page and use only rendered links.
Record the role, destination path, authorized controls present, unauthorized controls absent, and any temporarily blocked action with its displayed reason.

Across the role-to-workflow matrix, Enrollment, Crew control, and Display, review:

- Keyboard order, operation, traps, and visible focus.
- VoiceOver plus one desktop screen reader appropriate to the deployment.
- Labels, instructions, validation errors, and status announcements.
- Touch targets and touch-only operation.
- Reflow and legibility at 200% and 400% zoom.
- Text and non-text contrast, including configured Display themes and scrims.
- Non-color status, document language, reading order, and reduced motion.
- The visual state vocabulary: that every badge carries its state as text and shape as well as color, that a Session's publication, Audience Visibility, and lifecycle axes read as three separate states, that Preview and Program Output remain distinguishable without color, and that Stage Timer Emphasis and overtime announce themselves by label as well as by color and viewport outline.

Automated results support this review.
They do not replace it.
Record the exact Beamers commit, browser and operating-system versions, hardware where relevant, screenshots or video, and a written result for every row.
Attach the completed role matrix, Safari, manual accessibility, touch, zoom, and kiosk evidence to issue #51 before closing it.
Mark unperformed rows as pending.
Do not infer manual evidence from hosted automation.

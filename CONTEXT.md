# Live Event Operations

Language for coordinating a live event's program and communicating its current state to crew members, participants, and attendees.

## Language

**Event**: A live gathering with a Planned Date Range, managed as a coherent whole and described by one Rundown.
_Avoid_: Session

**Planned Date Range**: The advisory local-calendar span in which an Event is expected to take place.
Sessions outside it require warning and confirmation but remain publishable.
_Avoid_: Hard Boundary

**Event Day Boundary**: The configurable local time at which Schedule grouping advances to the next Event day.
It defaults to midnight; a nonexistent boundary uses the first valid instant after the jump, while a repeated boundary uses its later occurrence.
_Avoid_: Event timezone, Hard Boundary

**Event Locale**: The Event's default language tag and regional formatting conventions for public content, dates, numbers, and document metadata.
_Avoid_: Event timezone, translated content

**Content Language**: An optional language tag for Event content that differs from the Event Locale.
_Avoid_: UI translation

**Active Event**: The sole stored Event currently authorized to drive live commands and Display output from an installation.
Activation is a readiness designation, not an Event lifecycle state.
_Avoid_: Published Event, started Event, open Event

**Activation Preflight**: The review before changing the Active Event that blocks invalid published state and warns about operational incompleteness.
_Avoid_: Publish, Event start

**Account**: An installation-wide identity used by one person to authenticate.
_Avoid_: Event role, Display identity

**Disabled Account**: A retired Account whose sessions, credentials, and Event Grants are revoked while its stable identity remains available to Audit Entries.
_Avoid_: deleted Account, anonymized actor

**Event Grant**: The association of an Account with its role, scopes, and additional capabilities for one Event.
_Avoid_: Account, Administrator

**Crew Member**: An authenticated person acting through an Account under the Administrator role or an Event Grant.
_Avoid_: Attendee, Display

**Audit Entry**: An immutable record of who performed a relevant action, when, against what, and with what result, excluding credentials, secrets, and file contents.
_Avoid_: application log, mutable note

**Command Receipt**: The durable identity, canonical payload hash, and original outcome of one state-changing command, committed atomically with its effects and Audit Entry.
_Avoid_: transport acknowledgment, duplicate Audit Entry

**Administrator**: The installation-wide role responsible for service configuration, Accounts, backups, Display Enrollment, Active Event selection, and Event Grants.
It grants no Event crew access by itself.
_Avoid_: Producer

**Backup**: A versioned integrity-checked archive of one installation's authoritative database and referenced Attachment Versions.
It is disaster-recovery material, not Event interchange or continuous replication.
_Avoid_: Event Export, Litestream replica

**Sanitized Backup**: The default Backup form that preserves identities and operational history while excluding authentication secrets.
It requires Administrator re-bootstrap and Display re-Enrollment after Restore.
_Avoid_: anonymized export

**Full-Fidelity Backup**: An explicitly requested Backup that includes authentication secrets for credential-preserving recovery.
_Avoid_: Sanitized Backup

**Restore**: The explicit replacement of an installation with one validated Backup while preserving the replaced state for rollback or diagnosis.
_Avoid_: Import, Revert

**Maintenance Mode**: The bounded service state in which operational state is intentionally frozen while Backup, Restore, migration, or validation work prevents normal commands.
_Avoid_: Disconnected, recovery mode

**Producer**: The Event role authorized to configure an Event and use all of its live controls.
_Avoid_: Administrator

**Operator**: The Event role authorized to use live controls for assigned Lanes and Display Groups.
_Avoid_: Crew Member, Producer

**Observer**: The Event role with read-only access to crew information.
_Avoid_: Operator

**Venue**: The overall physical site hosting an Event and containing its Locations.
_Avoid_: Location

**Location**: A named operational area within a Venue, such as a room, stage, hall, foyer, or infodesk.
Within one Rundown, it is bound to at most one Lane.
_Avoid_: Venue, space

**Rundown**: The authoritative live plan for an Event, containing one or more independently progressing Lanes.
It has separate Draft and Published Revisions independent of Event configuration revision.
_Avoid_: Schedule, timetable, track

**Lane**: An independently progressing sequence of Sessions within a Rundown, bound to exactly one Location.
That binding may change through Publish without replacing the Lane's identity; parallel programs use distinct Locations and Lanes.
_Avoid_: Rundown, track

**Track**: An optional thematic grouping of Sessions that has no effect on live timing or progression.
_Avoid_: Rundown, lane

**Session**: A planned unit of live-event content within a Rundown, assigned to one or more Lanes, and occurring in one or more explicit Locations.
A Session defaults to the Location of its Lane.
_Avoid_: Rundown Item, event, slot

**Session Deletion**: Permanent removal of a never-Published, unreferenced Session from Draft state.
Once Published, a Session cannot be deleted.
_Avoid_: Cancel Session, Crew Only, archival

**Structural Retirement**: Publish removal of a Location, Lane, or Track from the current Rundown while retaining its stable identity and history for later inspection or reinstatement.
It is allowed only when no current Draft, Published, or Live dependency requires that structure.
_Avoid_: Session Deletion, Cancel Session, hard deletion

**Scheduled**: The lifecycle state of a Published Session awaiting its next start.
_Avoid_: Draft, proposed

**Live**: The lifecycle state of a Session whose current Session Run is taking place.
_Avoid_: Published

**Ended**: The lifecycle state of a Session that took place and is now complete.
_Avoid_: Canceled

**Canceled**: The lifecycle state of a Session that will not start or continue unless reinstated.
A Public Session remains on the Schedule marked Canceled but is excluded from Now/Next.
_Avoid_: Ended

**Cancel Session**: The confirmed live command that immediately moves a Scheduled or Live Session to Canceled.
Canceling a Live Session ends its current Session Run.
The command requires no reason and may carry a Public Cancellation Message and Crew Notes.
_Avoid_: Delete, unpublish

**Public Cancellation Message**: Optional attendee-facing text accompanying a Canceled Session; when absent, public surfaces show "Canceled."
_Avoid_: Crew Notes

**Reinstate Session**: The confirmed live command that immediately returns a Canceled Session to Scheduled while preserving its metadata and Session Runs.
Its Planned Time remains unchanged and the Crew Member chooses a new Forecast Time.
_Avoid_: Restore

**Rescheduled**: The audience-facing status of a reinstated Session assigned a new Forecast Time.
The Schedule shows one listing at the new time and identifies its previous time without duplicating the Session.
_Avoid_: Canceled copy, replacement Session

**Placement Preview**: The crew-only forecast of collisions and timing ripple produced by placing a reinstated Session at a proposed Forecast Time, Lane, and Location.
_Avoid_: Publish, automatic placement

**Audience Visibility**: The designation that makes a Published Session Public or Crew Only.
_Avoid_: Lifecycle state

**Crew Only**: Audience Visibility that excludes a Session from public Views and includes it on authorized crew Views.
When it occupies a public Location, Location Signage discloses only that the room is unavailable and until when.
_Avoid_: Hidden, Production Only

**Public Details**: The Session or Entry information approved for attendee-facing Views and the Schedule.
_Avoid_: Crew Notes

**Crew Notes**: The Session or Entry information restricted to authenticated crew users.
_Avoid_: Public Details

**Crew Reason**: A mandatory crew-only explanation recorded with a high-impact action such as Disqualify Entry.
_Avoid_: Public message, Crew Notes

**Attachment**: A logical file associated with an Event, Session, or Entry, retaining immutable Attachment Versions.
_Avoid_: Attachment Version, Crew Note

**Attachment Version**: One immutable uploaded revision of an Attachment.
_Avoid_: Attachment

**Attachment Store**: The installation's configured authoritative collection of immutable Attachment Version bytes.
_Avoid_: Final Files Export, Backup

**Final Files Export**: A reproducible human-navigable projection of selected Final Versions for external publication or archival.
It is disposable and never authoritative.
_Avoid_: Attachment Store, Backup

**Upload Link**: An unguessable, revocable credential granting an uploader access to one Presentation or Entry until its submission access closes.
_Avoid_: Account, public upload

**Upload Deadline**: An optional fixed instant closing a Presentation's Upload Link before it starts; without one, the link closes at Actual Start.
_Avoid_: Submission Deadline, Forecast Time

**Final Version**: The Attachment Version approved for operational delivery.
Multiple Attachments may each have a Final Version; non-final versions are excluded by default.
_Avoid_: Primary Attachment

**Primary Attachment**: The sole Attachment Version selected as the default file to use.
A sole upload is selected automatically, but it must become Final before delivery; related artifacts that must be used together are uploaded as one archive.
_Avoid_: Final Version

**File Delivery Required**: An optional Competition or Presentation policy that blocks Start unless every participating Entry, or the Presentation itself, has a Final Primary Attachment.
It defaults on for Competitions and off for Presentations.
_Avoid_: Require Entry Review

**Release Eligibility**: The designation allowing a Final Version to be released publicly or retaining it as Crew Only.
A Final Version defaults to Public unless the uploader deliberately chooses Crew Only.
_Avoid_: Audience Visibility, Release Policy

**Release Hold**: A Producer-controlled suspension preventing Attachment release or public access without changing the underlying Release Eligibility.
_Avoid_: Crew Only, deletion

**Release Policy**: The Event default, optionally overridden by a Competition, that releases eligible Final Versions On Live, On Ended, or On Event Release Cue.
_Avoid_: Release Eligibility

**Event Release Cue**: The Producer-controlled release action fired manually or when a configured ceremony Session becomes Live.
_Avoid_: fixed release time

**Results Draft**: The crew-only ordered placements and optional awards prepared for one Competition before a Producer marks them Ready.
_Avoid_: Public results, voting tally

**Ready Results**: The exact reviewed Results Draft revision approved for release.
Any relevant change creates a new revision and clears Ready.
_Avoid_: Published results

**Results Disposition**: A Competition's Pending, Publish, or No Public Results decision.
Pending is unresolved; Publish requires Ready Results; No Public Results requires a Crew Reason and remains publicly listed with a neutral status or optional public explanation.
_Avoid_: Entry Disposition, Results Publication Status

**Results Correction**: A reviewed replacement for already public results, carrying a mandatory Crew Reason and optional public correction note without retriggering Attachment release.
_Avoid_: direct result edit

**Results Publication**: The immutable public rendering of a released Results revision as HTML, event-level UTF-8 `results.txt`, and versioned JSON.
_Avoid_: Results Draft, operator CSV export

**Results Publication Status**: Unpublished before any result is released, Partial while progressive release is underway, and Final when every result in that publication's scope is resolved and released.
_Avoid_: Ready Results, Final Version

**Results Text Template**: The previewable template used to render `results.txt`, with a default provided and Event customization supporting plain-text and ASCII-art presentation.
_Avoid_: Results Slide template

**Placement**: An ordinal Competition result.
Tied Entries share a Placement and the next rank uses competition ranking, such as 1, 2, 2, 4.
_Avoid_: Award, display order

**Result Standing**: An eligible Entry's explicit Placed or Unplaced classification in a Results Draft.
Unplaced is distinct from omission, rejection, or disqualification.
_Avoid_: Entry Disposition, Placement

**Score Visibility**: The Competition setting making exact numeric scores Public or Crew Only.
Crew-only scores may drive a Reveal but are omitted from every public format.
_Avoid_: Audience Visibility, Results Access

**Score**: One Entry's exact Decimal or Duration value using its Competition's score type, unit, and display precision.
Version one stores no judge breakdown.
_Avoid_: Placement, arbitrary score text

**Score Requirement**: The Competition setting Optional or Required for eligible Entries.
Required blocks Ready when any eligible Entry lacks a Score.
_Avoid_: Score Visibility, File Delivery Required

**Score Interpretation**: The Competition setting Higher Wins, Lower Wins, or Informational, used to validate and present scores without deriving authoritative Placements.
_Avoid_: Placement, Score Visibility

**Award**: A named Competition- or Event-scoped recognition assigned to one or more Award Recipients independently of Placement.
_Avoid_: Placement

**Award Recipient**: An Entry reference or explicit display name receiving an Award.
_Avoid_: Participant Account, synthetic Entry

**Event Awards Draft**: The versioned crew-only set of Event Awards and their Prizegiving or standalone release assignments.
_Avoid_: Competition Results Draft

**Ready Event Awards**: The exact reviewed Event Awards Draft revision for one Prizegiving or standalone release path.
Changes within that path clear its Ready state.
_Avoid_: Ready Results

**Results Access**: The Event capabilities to View or Manage unreleased Results Drafts.
Producers receive both by default; other roles require explicit grants.
_Avoid_: Observer role, public results

**Prizegiving**: A Ceremony Session designated to reveal results for a selected set of Competitions.
A Competition belongs to at most one Prizegiving.
_Avoid_: generic Ceremony, Competition

**Results Sequence**: The explicit order in which a Prizegiving presents Result Items, initially placing Competitions by Planned Time followed by Event Awards and locking when the ceremony begins.
_Avoid_: Competition Entry Order, Rundown

**Results Publication Order**: The explicit order of Competitions and Awards in public Results Publications, defaulting to Competition Planned Time and independent of Results Sequence.
_Avoid_: Results Sequence, Competition Entry Order

**Result Item**: One presentable unit in a Results Sequence: a Competition result, a No Public Results status, an Event Award, or a Competition Award promoted from its parent result.
_Avoid_: Result Slide, Program Item

**Standalone Results Release**: The Producer-triggered publication of an unassigned Competition's resolved Results Disposition after the same relevant Preflight checks.
_Avoid_: Prizegiving, Event Release Cue

**Results Preview**: A visibly watermarked, side-effect-free rendering of unreleased results for a Crew Member with Results Access.
_Avoid_: Program Output, Result Reveal

**Prizegiving Preflight**: The review that blocks a Prizegiving until every assigned Competition has a resolved Results Disposition, each Publish disposition has Ready Results, and every relevant Not Presented Entry has a final resolution.
_Avoid_: Competition preflight

**Results Reveal Policy**: The rule publishing Ready Results All at Cue, Progressive on Reveal, or At Ceremony End.
Progressive on Reveal is the default.
_Avoid_: Attachment Release Policy

**Result Reveal**: The operator-triggered transition from an unrevealed Results Slide to its true final result.
Completion or Skip to Final releases it under Progressive on Reveal.
_Avoid_: Take, Results Reveal Policy

**Reveal Replay**: An explicit rerun of a completed Result Reveal for presentation only.
It cannot change revealed or released state.
_Avoid_: Result Reveal, Back

**Reveal Method**: The visual presentation used by a Result Reveal without changing placements, release semantics, or exact result data.
The closest applicable Event, Prizegiving, Competition, or Result Item setting wins.
Every method defines a deterministic reduced-motion fallback.
_Avoid_: Results Reveal Policy

**Reveal Seed**: The recorded value making a random-looking Reveal Method reproducible for one Ready Results or Ready Event Awards revision without influencing results.
_Avoid_: voting seed, result data

**Shared Session**: A single Session included in multiple Lanes so those Lanes share its timing and live state.
Its explicit Locations, rather than its Lane membership, determine physical occupancy and attendee directions.
_Avoid_: Linked Sessions, duplicated Session

**Slot**: A planned time envelope to which a Session may be assigned.
_Avoid_: Session

**Planned Time**: The start and end intended for a Session before live timing adjustments.
An ambiguous local time identifies its earlier or later occurrence explicitly.
_Avoid_: Forecast Time, Actual Time

**Forecast Time**: The currently expected start and end after live timing adjustments.
_Avoid_: Planned Time, Actual Time

**Communicated Time**: The latest Forecast Time shown publicly before a Session starts or ends.
_Avoid_: Published Time, Planned Time

**Session Run**: One execution attempt of a Session, retaining its own Actual Time, outcome, and Run Snapshot when the Session is ended or canceled.
_Avoid_: Session, Slot

**Run Snapshot**: The immutable published Session context captured when a Session Run starts, including its identity, placement, timing rules, and Competition Entry order.
_Avoid_: current Session, backup

**Run Amendment**: An immutable audited descriptive correction applied after a Run Snapshot without rewriting that snapshot.
_Avoid_: Draft, structural edit

**Live Detail Correction**: The confirmation-gated live action that corrects a Session's title, speaker, or public description through a Run Amendment.
_Avoid_: Publish, timing adjustment

**Actual Time**: The recorded start and end of one Session Run as it happens.
_Avoid_: Planned Time, Forecast Time

**Public Time Tolerance**: For a Session whose Planned Time spans more than ten minutes, an Actual Time within two minutes of Communicated Time is presented publicly as on time.
Live and Ended Sessions use their Run Snapshot duration; exact Actual Time remains available to crew.
_Avoid_: Actual Time rounding

**Public Time Presentation**: The attendee-facing choice and labeling of Planned Time, Forecast Time, Actual Time, Communicated Time, and the Public Schedule Baseline according to Session lifecycle.
It changes neither the underlying time facts nor which Sessions a public View includes; any included Scheduled, Live, Ended, or Canceled Session may show “Was:” when its current presented start differs from its Public Schedule Baseline.
_Avoid_: Display Time, Normalized Time

**Public Schedule Baseline**: The Event-owned, immutable Forecast Start deliberately recorded by a Producer from the current Published Revision for each Public Session when attendee-facing venue operations open, or atomically when a Session later first becomes Public.
Entries survive revision and visibility changes; absence does not prevent public presentation but provides no “Was:” context.
_Avoid_: Event start, Original Published Time, Previous Forecast Time

**Import Reference**: An external CSV key or iCalendar UID retained as evidence for duplicate detection and explicit mapping without becoming Session identity.
_Avoid_: Session identity, synchronization key

**Draft**: Unpublished structural changes to an Event that do not affect live operation.
_Avoid_: Forecast Time

**Draft Fact**: The smallest part of Draft state considered independently for concurrent-edit conflicts, such as one scalar value or one unordered relationship membership.
An ordered collection is one Draft Fact unless its domain model defines finer independent parts.
_Avoid_: Draft Change, database column

**Draft Change**: The smallest proposed structural change that may be selected for Publish.
Its dependencies must be selected with it or already be Published.
_Avoid_: Draft Edit, database patch

**Draft Edit**: One atomic set of related structural changes applied to shared Draft state under one expected Draft Revision.
All changes succeed together, and changes within the set may refer to other new Draft elements in that same edit.
_Avoid_: Publish, partial Draft update, generic patch

**Draft Revision**: The monotonic version identifying one snapshot of shared Draft state.
An edit based on an older revision conflicts only when an intervening Draft Change overlaps a fact that edit targets.
_Avoid_: Live State Revision, Results Draft revision

**Published Revision**: The monotonic version identifying one exact authoritative state of an Event's Rundown after Publish.
_Avoid_: Draft Revision, Live State Revision

**Draft Revert**: An ordinary undo or discard expressed as a new Draft Change while the superseded unpublished changes remain in Draft history.
When it restores the Published baseline, no effective Draft Change remains selectable for that fact.
_Avoid_: live Undo, Results Correction

**Publish**: The deliberate action that atomically makes a reviewed, dependency-valid selection of Draft changes authoritative for live operation.
_Avoid_: Live command

**Publish Selection**: The reviewed, dependency-closed subset of a Draft chosen for one atomic Publish; blocked or conflicting changes remain in Draft.
_Avoid_: partial database commit, import selection

**Publish Preview**: The exact proposed diff, automatically included dependencies, and impact for a Publish Selection, bound to its Draft and Published revisions and invalidated when either changes.
_Avoid_: generic diff, import preview

**Publish Note**: Optional Crew context attached to an audited Publish; routine publication does not require a written reason.
_Avoid_: mandatory Crew Reason

**Published**: Structural Event information made authoritative through Publish.
_Avoid_: Draft, proposed

**Presentation**: A Session in which one or more Speakers deliver content to an audience.
_Avoid_: Speaker session

**Competition**: A Session in which an ordered set of Entries is presented.
_Avoid_: Event

**Submission Deadline**: The fixed instant after which a Competition accepts neither new Entries nor changes to existing Entries unless one is explicitly reopened.
_Avoid_: Forecast Time, Hard Boundary

**Entry**: One work submitted for possible presentation within a Competition.
_Avoid_: Session

**Entry Disposition**: The Pending, Included, or Rejected status determining whether an Entry participates in its Competition, independently of Entry Review.
It becomes immutable when the Entry's first Competition Slide is Taken.
_Avoid_: Entry Review, Audience Visibility

**Pending Entry**: An Entry that may still receive uploads but remains crew-only and cannot participate until its disposition is resolved.
_Avoid_: Included Entry

**Included Entry**: An Entry included in Competition order, Slides, participant lists, and eligible Attachment release.
_Avoid_: Pending Entry, Rejected Entry

**Entry Order Policy**: The Competition rule choosing Submission Order, Manual Order, or Deterministic Shuffle.
Deterministic Shuffle is the default and retains its seed.
_Avoid_: Track order, Competition Slide

**Locked Entry Order**: The immutable Included Entry sequence after the first Entry Slide is Taken.
_Avoid_: Entry Order Policy, Replay

**Defer Entry**: The live action that advances past an Included Entry without rewriting Locked Entry Order and places it in the Competition's deferred queue for later retry.
_Avoid_: Reject Entry, Replay

**Not Presented**: The outcome assigned to a deferred Entry when its Competition ends without it being presented.
_Avoid_: Rejected, Disqualified

**Resolution Required**: The crew-only provisional state of a Not Presented Entry awaiting a final outcome before Prizegiving and public release may proceed.
_Avoid_: Entry Review

**Technical Failure**: A resolution reason explaining why an Entry was Not Presented without itself determining judging eligibility, public visibility, or Attachment release.
_Avoid_: Disqualified, Technical Difficulties

**Withheld Entry**: A Not Presented Entry retained for crew but omitted from public participant lists and results, with a Release Hold and no Disqualified label.
_Avoid_: Rejected Entry, Disqualified Entry

**Withhold Entry**: The Producer action that records a Crew Reason and makes a Not Presented Entry Withheld without erasing its history.
_Avoid_: Disqualify Entry

**Rejected Entry**: An Entry retained for crew and audit but excluded from Competition presentation, public lists, and Attachment release; its Upload Link is closed.
Rejection may be reversed without automatically restoring upload access.
_Avoid_: Canceled Session, Pending Entry

**Disqualified Entry**: An Included Entry excluded from judging or results after Competition order locks, whether or not it was presented.
Its true presentation history remains, it is publicly identified as Disqualified, and it receives a Release Hold.
_Avoid_: Rejected Entry

**Disqualify Entry**: The Producer action that records a mandatory Crew Reason, marks a locked Included Entry Disqualified, and applies its Release Hold.
_Avoid_: Reject Entry

**Public Disqualification Message**: Optional attendee-facing text accompanying a Disqualified Entry; when absent, public surfaces show "Disqualified."
_Avoid_: Crew Reason

**Reopen Window**: A temporary, crew-authorized exception restoring submission access to one Presentation or existing Entry after closure.
It expires automatically and cannot remain open indefinitely.
_Avoid_: Deadline extension, Competition reopening

**Entry Review**: The Crew Member confirmation that an Entry's current contents are ready for its Competition.
Any subsequent change invalidates that review.
_Avoid_: Entry disposition, Final Version

**Require Entry Review**: An optional Competition policy that blocks Start until every participating, Included Entry has a current Entry Review.
It is disabled by default.
_Avoid_: Submission Deadline

**Competition Slide**: A presentable Competition state for upcoming, starting, an Entry, or ending.
_Avoid_: Entry

**Results Slide**: A Program Item presenting an unrevealed or revealed Competition result during Prizegiving, including a generated No Public Results status slide.
_Avoid_: Competition Slide, public results page

**Skip from Stage**: The explicit omission of a Result Item from Prizegiving Program Output without suppressing its publication at ceremony completion.
_Avoid_: Skip to Final, No Public Results

**Program Item**: Content that may be selected in Preview and Taken to a Program Channel.
Version one supports Competition Slides, Results Slides, and Standby.
_Avoid_: View Page, Override

**Program Channel**: A named, independently controlled live presentation channel whose content may feed one or more Views and Displays.
It is logical and need not be a video signal.
_Avoid_: Display Group, View

**Program Output**: The live content currently selected on one Program Channel and presented by its consuming audience Displays.
_Avoid_: Preview

**Preview**: A crew-only representation of content that is not currently in Program Output.
_Avoid_: Program Output

**Take**: The Crew Member action that durably commits Preview as a Program Channel's new Program Output without waiting for every consuming Display to apply it.
_Avoid_: Select, advance

**Control Owner**: The one authorized Crew Member permitted to manipulate a Program Channel at a given time.
Ownership does not restrict Emergency Overrides.
_Avoid_: Operator role, Producer role

**Live State Revision**: The version of an authoritative live domain aggregate that a command expects to change.
A mismatched revision makes the command stale.
_Avoid_: Draft revision, Results Draft revision

**Replay**: Taking a previously shown Competition Slide without moving the canonical sequence position.
_Avoid_: Rewind

**Speaker**: A person who delivers content during a Presentation.
_Avoid_: Presenter

**Timing Policy**: The rule that determines a live Session's target end: Fixed End, Fixed Duration, or Manual End.
An authorized Crew Member may override the target for an individual Session while it is live.
_Avoid_: Timer mode

**Fixed End**: A Timing Policy whose target end is one explicitly resolved instant.
_Avoid_: Fixed Duration

**Fixed Duration**: A Timing Policy whose target end is calculated by adding a real elapsed duration to the Session Run's Actual Start.
_Avoid_: Fixed End

**Manual End**: A Timing Policy with no automatic target end; the Session remains Live until an authorized Crew Member ends or cancels it.
_Avoid_: Operator Controlled, Open End

**Minimum Duration**: The shortest duration that automatic timing adjustments may assign to a Session.
_Avoid_: Compression limit

**Hard Boundary**: A Session start or end that automatic timing adjustments cannot move.
An authorized Crew Member may move it only through an explicit, warned override.
_Avoid_: Hard edge

**Soft Boundary**: A Session start or end that automatic timing adjustments may move or compress.
_Avoid_: Soft edge

**End Now**: The Crew Member action that immediately ends the live Session without moving later Session boundaries.
_Avoid_: Stop

**Adjust Target**: The Crew Member action that moves a Live Session's timing target later or earlier after previewing downstream impact.
It cannot set a target before now.
_Avoid_: End Now, Pull Forward

**Pull Forward**: An optional Crew Member action that moves eligible later Soft Boundaries earlier after a Session ends ahead of its target.
_Avoid_: End Now

**Display**: An addressable screen endpoint that presents Event information.
_Avoid_: View, monitor, screen

**Stale**: The connected Display condition in which its last committed frame remains valid but intentionally frozen during Maintenance Mode.
_Avoid_: Disconnected, Live

**Display Group**: A logical set of Displays that may be targeted together by an Override.
_Avoid_: View, Location

**Enrollment**: The process that establishes a Display's identity and persistent trust with the installation across Events.
_Avoid_: Assignment, pairing

**Assignment**: The Event-specific association of an enrolled Display with its Location and normal View.
An unassigned Display presents Standby.
_Avoid_: Enrollment

**View**: An assignable presentation of Event information that one or more Displays may show.
_Avoid_: Display

**View Page**: One content screen in a View's ordered, timed rotation.
_Avoid_: Competition Slide

**View Layout**: The spatial arrangement of Regions within a View.
_Avoid_: View Page

**Theme**: The Event's configurable branding, colors, fonts, backgrounds, and transitions applied to Views without changing their Layout or content.
_Avoid_: View Layout, slide template

**Contrast Scrim**: An opaque or translucent surface behind text that guarantees required contrast over variable image or video content.
_Avoid_: decorative overlay, Override

**Region**: A named area within a View Layout that contains a Widget.
_Avoid_: Location

**Widget**: A unit of live or configured content placed in a Region.
_Avoid_: View

**Rotation Widget**: A Widget that presents an ordered sequence of View Pages for configured durations.
_Avoid_: Override Stack

**Event Overview**: A public View combining multi-Lane Schedule information with rotating Event information.
_Avoid_: Location Signage

**Location Signage**: A public View that keeps its Location, Now/Next information, and clock visible around a rotating content Region.
It shows a neutral room-unavailable notice instead of details for an occupying Crew Only Session.
_Avoid_: Event Overview

**Stage Timer**: A crew View showing the live Session countdown, status, and Stage Messages.
After its target, it shows clearly styled positive overtime while the Session remains Live.
_Avoid_: Location Signage

**Timer Threshold**: A remaining-time point that changes Stage Timer Emphasis, inherited from Event to Session type to Session and ignored when longer than the target duration.
_Avoid_: Hard Boundary, Stage Message

**Timer Adjustment Notice**: A brief Stage Timer status explaining an Adjust Target change without creating a Stage Message.
_Avoid_: Stage Message, audit entry

**Competition Output**: The public View presenting Competition Slides as Program Output.
_Avoid_: Competition control View

**Standby**: A branded idle View or Program Item shown when no other normal content is assigned or selected.
_Avoid_: Technical Difficulties

**Live Feed**: An optional live video source shown within a View Region while available, with normal View content retained as its fallback.
_Avoid_: Program Output

**Override**: A temporary, prioritized presentation that supersedes the normal View on its target Displays and restores that View when cleared.
A Program Channel may be a target, resolving to its consuming Displays.
_Avoid_: View

**Override Stack**: The active Overrides ordered by priority and presentation mode for a Display.
_Avoid_: Override queue

**Replace**: An Override presentation mode that suppresses lower-priority content with a fullscreen presentation.
_Avoid_: Overlay

**Overlay**: An Override presentation mode that composes a banner or lower third over the otherwise visible lower-priority content.
_Avoid_: Replace

**Emergency Alert**: The highest-priority, persistent fullscreen Override for safety-critical information.
_Avoid_: Urgent Notice

**Urgent Notice**: An Override for operational information requiring prompt attention but not representing a safety emergency.
_Avoid_: Emergency Alert

**Stage Message**: A one-way, non-queued Overlay from a Crew Member to crew Displays for on-stage personnel.
It expires after the Event default unless sent Until Cleared.
_Avoid_: Speaker Message, Direct Message

**Stage Message Preset**: An Event-configured Stage Message text with target and duration defaults for fast reuse; the sender may still compose free-form messages.
_Avoid_: Override template

**Stage Message Emphasis**: Normal, Attention, or Urgent accessible styling for a Stage Message without changing Override priority or implying an Emergency Alert.
_Avoid_: Override priority, Emergency Alert

**Technical Difficulties**: A Replace Override asking viewers to wait while a technical problem is addressed, without changing Session timing or live state.
_Avoid_: Technical Hold

**Schedule**: The audience-facing presentation of relevant parts of the Rundown.
Within an Event-day group, it explicitly marks any local calendar-date rollover.
_Avoid_: Rundown in audience-facing language

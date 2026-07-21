# Stage results before Prizegiving

V1 stores a crew-only Results Draft for each Competition with ordered placements and optional award labels.
A Producer reviews and marks the draft Ready.
An Event may designate multiple Ceremony Sessions as Prizegivings and assign each Competition to at most one of them.
Prizegiving Preflight checks only its assigned Competitions and blocks while any Results Disposition is Pending, any Publish disposition lacks Ready Results, or any Not Presented Entry has Resolution Required.
Its Results Reveal Policy then governs release of those exact revisions; non-progressive release publishes the set atomically, while Progressive on Reveal publishes its revealed subset.
Vote collection and tallying are deferred because they depend on the future attendee Account and admission vote-key system.

Prizegiving assignment is optional.
An unassigned Competition may use a Producer-triggered Standalone Results Release after the same disposition, Ready Results when publishing, and Entry-resolution checks.
A Competition assigned to a Prizegiving cannot use the standalone path and is released only through that Prizegiving.

Each Prizegiving has an explicit Results Sequence of Result Items: Competition results, No Public Results statuses, Event Awards, and promoted Competition Awards.
The application initially orders Competitions by Planned Time followed by Event Awards; a Producer may rearrange them and reviews the sequence during Preflight.
The sequence locks when the Prizegiving begins.
Replay and Skip actions affect presentation without silently rewriting that canonical order.

Unreleased Results Drafts require Results Access.
Producers receive View and Manage Results by default; selected Operators may receive either, while Observers may receive View only.
Administrators have no implicit access without an Event Grant.
These restrictions end when results become public.

Ready applies to one exact Results Draft revision.
Changing placements, scores, Score Visibility, awards, Entry outcomes, disqualification, withholding, or required resolutions creates a new revision and clears Ready.
A Producer must review it again.
Prizegiving Preflight and Event Release Cue preview and release only the exact Ready revision.

Each Competition has a Results Disposition.
Pending is unresolved.
Publish requires Ready Results.
No Public Results counts as resolved without placements and requires a Producer-supplied Crew Reason, with an optional public explanation.
Its Competition heading remains in public HTML and `results.txt`, followed by that explanation or the neutral text "No results published."
This makes deliberate non-publication distinguishable from missing data.
The disposition is reversible until its first public release; a later change uses Results Correction.

No Public Results remains crew-only until the Competition reaches its normal release action.
An assigned Competition publishes the status through its Prizegiving; an unassigned one uses Standalone Results Release.
It does not leak through public Schedule or Results surfaces before that action.

An assigned No Public Results disposition generates a Results Slide in the Prizegiving sequence by default.
It shows the public explanation or neutral message.
Under Progressive on Reveal, taking that already-final slide publishes the status immediately.

Skip from Stage may explicitly omit any Result Item from Program Output while queuing its normal publication for Prizegiving completion.
Attempting to end a Prizegiving with an item neither revealed nor skipped is blocked with a warning that lists every unresolved item.
The operator must return to the sequence or skip those items deliberately; completion never drops them silently.

Released results are immutable.
A correction creates a Results Correction from the released revision with a mandatory Crew Reason and optional public correction note.
A Producer reviews and publishes it atomically; public results show when they were Corrected, prior revisions remain in crew history, and Attachment release is not triggered again.

Each public release produces one Results Publication from the same immutable revision: public HTML pages, an event-level UTF-8 `results.txt`, and versioned machine-readable JSON.
CSV may be offered as an operator export but is not a canonical public format because ties and Awards do not map cleanly to it.

The application provides a default Results Text Template.
An Event may select or customize alternative templates, including ASCII-art decoration, and preview the exact `results.txt` rendering before release.
The selected template and its rendered file are frozen into the Results Publication.
Later template changes affect only future publications; replacing an already published `results.txt` requires a Results Correction.

Results Text Templates use Go `text/template` against a documented, immutable publication view model and a small allowlist of formatting functions.
They have no filesystem, network, command-execution, or application-service access.
Parsing and rendering errors are reported through Preview rather than falling back silently to a different format.

Prizegiving Preflight validates and locks one Results Text Template revision for that Prizegiving.
Under Progressive on Reveal, each incremental Results Publication renders the currently public result set with that same revision.
Template edits cannot change formatting partway through a Prizegiving; they apply to a later Prizegiving or through a Results Correction.

Before the first public result, no Results Publication is exposed.
Progressive release creates Publications containing only results already revealed and marks them Partial; it never emits placeholders, names, or other data for unrevealed results.
Completing a Prizegiving marks its scoped Publication Final.
The event-level Publication remains Partial until every publishable Competition is released and every other Competition has No Public Results, then becomes Final.

Results Publications use an explicit Results Publication Order independent of stage Results Sequence.
It defaults to Competition Planned Time, so dramatic ceremony ordering does not make the final HTML, `results.txt`, or JSON awkward.
A Producer may edit publication order before the relevant Preflight locks it.
After the first publication in that scope, reordering requires a Results Correction.

Results support ties: tied Entries share a Placement and the next rank follows competition ranking, such as 1, 2, 2, 4.
Crew explicitly chooses display order within a tie without implying a hidden tiebreak.
Named Competition Awards and Event Awards, such as Audience Choice or Organizers' Choice, may each be assigned manually to one or more Award Recipients without a Placement.
A recipient may reference an Entry or carry an explicit display name, avoiding synthetic Entries for honors such as Best Speaker or lifetime achievement.

Every eligible Included Entry must have an explicit Result Standing before its Results Draft can become Ready.
Placed Entries receive an ordinal Placement; Unplaced Entries remain valid participants and appear after all Placements in public results.
Unplaced is not inferred from omission and is distinct from Rejected, Disqualified, or Withheld outcomes.
Unplaced Entries retain Locked Entry Order rather than sorting by score, avoiding an undeclared ranking.

Disqualified Entries appear in a separate public section after Unplaced Entries, retaining Locked Entry Order, public identity, and any Public Disqualification Message.
They show no Placement or score.
Rejected and Withheld Entries remain absent from public results.

Each Competition sets Score Visibility to Public or Crew Only, defaulting to Public.
Crew-only numeric scores may still drive Animated Score Bars, including relative bar lengths, but exact values are omitted from Program Output, public HTML, `results.txt`, and JSON.

When a Competition uses scores, it chooses one exact score type: Decimal or Duration, together with a unit and display precision.
Each Entry has at most one final Score of that type.
Binary floating point and arbitrary score text are not canonical representations.
Per-judge components and detailed scoring breakdowns are deferred beyond version one.

Score Requirement is Optional by default and may be set to Required.
Required blocks Ready while any eligible Entry lacks a Score.
Regardless of that setting, Animated Score Bars is valid only when every Entry it visualizes has a Score; Preflight identifies missing values before the Reveal begins.

Placements remain authoritative rather than being derived from numeric scores.
Each Competition declares Score Interpretation as Higher Wins, Lower Wins, or Informational so Preflight and Reveal Methods interpret the values correctly.
A contradiction between scores and Placements produces a confirmed warning but never silently reorders the results.

A Competition Award is embedded in its Competition result by default.
A Producer may promote it to an independent Result Item when it should receive a separate stage reveal and position that item explicitly in the Results Sequence.
Under Progressive on Reveal, the parent Competition result omits a promoted Award until its own Result Item is revealed.
That Reveal adds it to public HTML, `results.txt`, and JSON without republishing the parent placement data.

Event Awards are staged in a versioned Event Awards Draft.
Each Award is assigned to one Prizegiving or to a standalone release path.
A Producer reviews and marks a revision Ready per release path, independently of Competition Results Drafts.
This prevents editing Awards for a later ceremony from blocking the current one.
Relevant Preflight blocks until that path's Event Awards are Ready.
Changing an Award, recipient, or release assignment creates a new revision and clears Ready only for affected paths.

The deferred voting design must revisit thumbs voting, an optional coup de coeur vote scoped per Competition or Event, and whether Audience Choice derives from that vote or aggregate ranking.
V1 assigns Awards manually and makes none of those choices.

Each Prizegiving chooses a Results Reveal Policy: All at Cue, Progressive on Reveal, or At Ceremony End.
Progressive on Reveal is the default so public results remain synchronized with completed stage reveals without delaying everything until the end.
Attachment Release Policy remains independent.

Under Progressive on Reveal, Take first places an unrevealed Results Slide in Program Output.
A separate Reveal action runs its Reveal Method and reaches the immutable true result; completion or Skip to Final publishes that result.
Exact points may remain absent from the visual output even when the released results file contains them.
Different Reveal Methods share these semantics and cannot change placements or release behavior.

Reveal Method inheritance runs from Event default to Prizegiving override, Competition override, and finally Result Item override; Event Awards skip the Competition scope.
The closest configured value wins.
Initial built-ins are Static Result, Sequential Podium, and Animated Score Bars.
Animated Score Bars requires numeric scores in Ready Results.
Every method supports Skip to Final and reaches the same final output; additional methods may be added without changing Results data.
Every method also defines a deterministic reduced-motion fallback that preserves its final output and release semantics.
Reduced motion may be selected Event-wide or for an individual Display.

Random-looking methods store a Reveal Seed with the Result Item's exact reviewed source revision so Preview, rehearsal, and replay are reproducible.
Competition seeds belong to Ready Results; Event Award seeds belong to Ready Event Awards for their release path.
Regenerating a seed is an explicit pre-release action and clears only that source's Ready state; the seed never affects placements, Awards, or scores.

Revealed state is monotonic.
Navigating Back to a revealed Results Slide shows its final state and cannot conceal or retract it.
Reveal Replay is a separate, explicit presentation action that reruns the method without changing revealed state, public release state, or Attachment release.

Results Preview and rehearsal require Results Access and carry an unmistakable Preview watermark.
They render the same Ready Results or Ready Event Awards revision, Reveal Method, and Reveal Seed as Program Output but are side-effect-free: they cannot change Program Output, revealed state, public release, or Attachment release.

A reconnecting Display must not restart a completed Result Reveal.
While a Reveal is still running, resuming at its server-timestamped position is preferred but not required for version one; the Display may instead restart the same deterministic animation.
Either behavior is presentation-only and cannot alter revealed or released state.

Starting a Result Reveal durably records its server start time and duration.
After a venue-service restart, elapsed time determines whether it has completed.
A completed Reveal is restored directly to its final state and its Progressive release transition is applied exactly once.
A still-running Reveal may resume or deterministically restart under the same Display rule.

While a fullscreen Replace Override covers every Display consuming the Program Channel, an active timed Reveal and its Progressive release completion are paused.
Clearing full coverage resumes the remaining duration.
Partial coverage and Overlay Overrides do not pause it.

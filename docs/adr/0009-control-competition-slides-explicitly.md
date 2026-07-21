# Control Competition Slides explicitly

A Competition is a Session containing ordered Entries and generated Slides for upcoming, starting, each Included Entry, and ending.
An authorized Crew Member explicitly advances the live presentation, while Forecast Time derives the upcoming countdown and the current Entry derives the previous Entry shown alongside it.
The control View shows Program Output with previous and next Slides so the impact of navigation is visible before action.
A later slide picker may present any specific Slide again without changing Entry order, such as replaying a faulty Entry after the normal sequence.
Media-player and API automation may be added later without changing explicit crew control as the default.

Navigation follows a Preview/Program workflow on a Program Channel.
Selecting previous, next, or an arbitrary Slide changes Preview only; Take explicitly sends Preview to Program Output, then Preview defaults to the next intended Slide.
Emergency Overrides remain immediate and bypass Take.

Replaying a previously shown Slide does not move the canonical sequence position.
After Replay, Preview returns to the Slide that originally followed Program Output; a future explicit Continue From Here action may deliberately move the sequence position.

An Entry's Disposition becomes immutable when its first Competition Slide is Taken.
A presented Entry cannot be rewritten as Rejected; later exclusion from judging or results uses Disqualified and preserves presentation history.
The public Competition history retains the Entry with a Disqualified label.
Disqualified may also resolve a Not Presented Entry after Competition End; in both cases the record states accurately whether its Slide was ever Taken.

Each Competition chooses Submission Order, Manual Order, or Deterministic Shuffle as its Entry Order Policy; Deterministic Shuffle is the default.
Its seed is recorded so the order is reproducible.
Crew previews the generated order and may make audited adjustments before live operation.
The sequence becomes Locked Entry Order when the first Entry Slide is Taken and is not rewritten afterward.

Defer Entry advances the canonical cursor without rewriting Locked Entry Order and adds the Entry to a separate deferred queue.
After normal Entries, Preview offers deferred Entries in defer order for retry.
If an Entry Slide was already Taken, the existing Replay action is used instead of deferral.

Attempting to end a Competition with deferred Entries shows a warned confirmation listing them.
Canceling returns to the Competition; confirming ends it and marks each deferred Entry Not Presented with Resolution Required.
That provisional state is crew-only.
Prizegiving preflight blocks until every such Entry has a final resolution, after which public results and any configured Attachment release may proceed.

Technical Failure is a resolution reason rather than an automatic disqualification.
Organizers may still judge the Entry from its file, and its Attachments remain governed by Release Policy unless a separate Release Hold is applied.

Withhold Entry is a distinct Producer resolution requiring a Crew Reason.
It omits a Not Presented Entry from public participant lists and results, applies a Release Hold, and shows no Disqualified label.
The crew record remains intact, including the Technical Failure reason when applicable, so the work may be submitted elsewhere later.

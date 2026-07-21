# Fix Competition submission deadlines independently

Each Competition may have a Submission Deadline stored as an explicitly resolved instant.
It does not move when Forecast Time ripples or the Competition is rescheduled; a Crew Member must deliberately change it.
At the deadline, new Entries and changes to existing Entries close.
An authorized Crew Member may grant a Reopen Window to one existing Entry without reopening the Competition or accepting new Entries.
A Reopen Window defaults to 30 minutes, requires an explicit expiry, and may be closed early or extended.
Its grant, reason, expiry, and resulting changes are audited; it cannot remain open indefinitely.

A Competition may enable Require Entry Review, which is disabled by default.
When enabled, every Included Entry must have a Crew Member's current Entry Review before Competition Start.
Any change, including one made during a Reopen Window, invalidates the prior review and blocks Start until the Entry is reviewed again.

Entry Disposition is independent from review.
An Event configures whether new Entries default to Pending or Included, with an optional Competition override.
Pending Entries may receive uploads but remain crew-only and block Competition Start until changed to Included or Rejected.
Included Entries participate in the Competition.
Rejected Entries remain available to crew and audit but are excluded from presentation, public lists, and Attachment release, and their Upload Links close.
An authorized Crew Member may return a Rejected Entry to Pending or Included while preserving audit history, but upload access remains closed until explicitly reissued or reopened.
Once the Competition is Live, an Entry Disposition change requires a Producer's warned override, and it becomes immutable after the Entry's first Slide is Taken.
A later Disqualified outcome preserves that Entry's presentation history while excluding it from judging or results.
The public history retains a Disqualified label and Attachment access is suspended through a separately reversible Release Hold.
Disqualify Entry is a Producer action requiring a Crew Reason and allowing a separate optional Public Disqualification Message.
Without one, public surfaces show only "Disqualified."

A Not Presented Entry may instead be resolved as Withheld.
This Producer action requires a Crew Reason, omits the Entry from public lists and results, and applies Release Hold without marking it Disqualified.

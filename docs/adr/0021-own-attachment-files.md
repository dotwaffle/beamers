# Own Attachment files

The application owns canonical Attachment files and their metadata, visibility, and integrity.
Replacing a file creates a new Attachment version rather than silently modifying the original.
A future read-only WebDAV endpoint, copyparty export, or similar adapter may expose Competition Entries for convenient crew download, but external file services do not write canonical storage.
A Run Snapshot references the exact immutable Attachment versions used without duplicating their contents.
Version one includes the scoped speaker and Competition Entry upload workflow described below; general-purpose file sharing remains deferred.

Each logical Attachment retains immutable versions and may designate one Final Version for operational delivery.
An Entry or Session may therefore have multiple Final Versions, such as PDF and PPTX slides.
An authorized Crew Member selects the Primary Attachment used by default on stage; a sole upload is selected automatically.
Primary may be selected before its version is Final, but it cannot be delivered until then.
By default, Session preflight marks a sole Primary upload Final.
A Competition using Require Entry Review does not auto-finalize it until its current contents have been reviewed.
All Final Versions remain available, while non-final versions are excluded from delivery by default.

Preflight automates only unambiguous selection.
A sole upload becomes Primary and, subject to review policy, Final.
If exactly one version is Final and no Primary exists, it becomes Primary.
Multiple Finals without a Primary, or a Primary that is not Final, require crew resolution.
These conditions block Start only when File Delivery Required is enabled for the Competition or Presentation, applying to each participating Entry or the Presentation itself.
There is exactly one Primary per attachment owner; uploaders are told to package artifacts in an archive when they must be used or distributed together.
File Delivery Required defaults on for Competitions and off for Presentations.
Each Event may change those Session-type defaults, and an individual Session may override its inherited value.

Final Version selection defaults its Release Eligibility to Public; an uploader must deliberately choose Crew Only to prevent eventual public release.
Uploaders cannot choose release timing.
An Event Release Policy, optionally overridden by a Competition, releases eligible Final Versions when the Session becomes Live, when it becomes Ended, or on an Event Release Cue.
A Producer may fire that cue manually or bind it to a selected ceremony Session becoming Live, avoiding a fragile fixed wall-clock release time.

Only Included Entries participate in operational delivery or public Attachment release.
Pending and Rejected Entries remain crew-only; rejection also closes the Entry's Upload Link.

The initial Attachment workflow for speaker and entrant uploads uses one unguessable, revocable Upload Link scoped to a Presentation or Entry rather than an attendee Account.
It grants no access to other submissions and expires when submission access closes; a Reopen Window temporarily restores access for its scoped target.
Rotation invalidates a shared link.
Audit attributes changes to that upload identity, and Crew Members may upload on the contributor's behalf.
Multi-Competition attendee Accounts and voting remain future work.

A Presentation's Upload Link closes at Actual Start unless a Producer configures an earlier fixed Upload Deadline, which does not move with Forecast Time.
After closure, a temporary Reopen Window may restore access under the same audited, automatically expiring rules used for Competition Entries.

Disqualifying a locked Included Entry applies a Release Hold without changing the uploader's underlying Release Eligibility choices.
The hold blocks pending release and disables existing public links, although it cannot retract files already downloaded.
A Producer may explicitly lift the hold after review.
The mandatory Crew Reason for disqualification remains private; only an optional Public Disqualification Message is shown alongside the public label.
Withholding a Not Presented Entry likewise applies a Release Hold, but omits the Entry from public lists and results instead of labeling it Disqualified.

An Event Release Cue bound to Prizegiving cannot fire while any Not Presented Entry has Resolution Required.
Resolving all such Entries is therefore required before public results and configured Attachment releases proceed.

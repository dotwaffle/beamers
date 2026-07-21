# Reject Stale Live Commands

Every live domain aggregate, such as a Session, Program Channel, or Override Stack, has its own Live State Revision.
Event-wide revisions would make unrelated rooms conflict, while independent revisions for every record would make coherent multi-record actions fragile.

Every live command carries the revisions observed by its control View.
The venue service applies it only if every affected aggregate still has its expected revision, validating and changing them atomically when a command spans more than one.
If any state changed meanwhile, the service rejects the command without mutation and returns current state so the control View can refresh.
It does not guess whether the operator's original intent remains safe.

Every live command also has a unique command identity.
Retrying that exact command returns its original outcome rather than applying its effect again.
This makes transport retries and repeated clicks idempotent while preserving the explicit corrective-action model for genuinely new operator intent.

The client generates the Command ID before its first attempt.
The service commits a Command Receipt containing that ID, a canonical payload hash, and outcome metadata atomically with the state change and Audit Entry.
It never returns success before that commit.
A crash before commit leaves no Receipt and a retry executes normally; a lost response after commit is recovered by returning the recorded original outcome.

Reusing a Command ID with a different canonical payload is rejected and audited as a conflict.
An exact retry creates no second Audit Entry.
Command Receipts share the installation-lifetime retention of audit history and exclude sensitive response bodies.
A lost post-commit live notification does not change the command outcome; SSE consumers recover through sequence-gap detection and authoritative snapshots.

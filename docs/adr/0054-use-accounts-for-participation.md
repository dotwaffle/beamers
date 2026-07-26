# Use Accounts for optional participation

Public Event, Schedule, and Results browsing does not require an Account.
Optional installation-wide Accounts add Favorite Sessions, Competition Entry submission, voting when eligible, Public Profile selection, and authorized Backstage access.
One Account may hold different Event Grants across Events and may also be an Administrator.

Open self-registration is the default and needs only an Account Handle, Display Name, and Password Credential.
An installation may close registration.
An enabled Account may hold multiple Password, WebAuthn, and Federated Identity Credentials and must retain at least one.
Federated identities use provider plus immutable subject and are never automatically linked by handle, display name, or email.
Existing Accounts authenticate before linking a provider.
Provider configuration and secrets remain host-managed.

Browser authentication uses revocable server-side sessions in protected cookies and CSRF protection on mutations.
Accounts generate single-use Recovery Codes.
An Administrator may issue an audited short-lived recovery token after offline verification.
Host bootstrap remains the last-Administrator recovery path.

Competition Entry submission is Account-only.
An Entry or Presentation has at most one Submitter Account, while its public credits remain independent.
A Producer may create a Crew Managed Entry or Presentation and later assign an existing Account.
Self-service Presentation proposals and review are deferred.

Disabling an Account revokes its sessions, Credentials, and Event Grants and detaches private personal data.
Audit identity, credited Entries, released Results, and other Event history remain.

This extends ADR 0013 from Crew identity to optional attendee participation.
It supersedes ADR 0021's Upload Link workflow.
ADR 0021's canonical Attachment ownership, immutable versions, and release controls remain in force.

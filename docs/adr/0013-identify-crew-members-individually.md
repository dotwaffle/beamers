# Identify Crew Members individually

Control surfaces require individual Accounts so permissions can be revoked and every state-changing action can record who acted, when, on what, and with what outcome.
Displays authenticate through Enrollment credentials, while public read-only Views may remain open.
Regular account login is sufficient initially.
Passkeys and roaming security keys may be added later through WebAuthn; enabling them on the Venue network requires a stable HTTPS origin.

Authorization starts with four fixed roles.
Administrators manage the service, Accounts, and Display Enrollment.
Producers configure Events and use all live controls.
Operators use live controls only for assigned Lanes and Display Groups.
Observers have read-only crew access.
Accounts are installation-wide, as is the Administrator role.
Producer, Operator, and Observer roles are assigned through Event Grants, allowing one Account to have different access to different Events.
Administrator authority alone grants no access to Event crew data or live controls.
An Administrator may grant their own Account an Event role, and that action is audited.
Custom role construction is deferred.

Retiring a Crew Member uses Disable Account rather than deletion.
Disabling immediately revokes active sessions, authentication credentials, and Event Grants, while retaining the stable Account identity and display name referenced by Audit Entries.
Hard deletion and anonymization are deferred beyond version one.

Emergency Alert activation is a separately granted capability rather than an implicit consequence of role or Lane scope.
Producers receive it by default, Administrators may grant it to selected Operators, and Observers cannot receive it.
The capability and its scopes belong to the Event Grant.

Unreleased Results Drafts use separate View Results and Manage Results capabilities rather than ordinary crew-read access.
Producers receive both by default.
Selected Operators may receive either; Observers may receive View Results only.
Administrator authority alone grants neither without an Event Grant.
Released results are public.

# Serialize Live Control by Program Channel

A Program Channel is a named, independently controlled live presentation channel.
It is logical rather than necessarily a video signal.
Its Program Output may be consumed by one or more Views and Displays; autonomous Views such as rotating hallway signage need not consume a Program Channel.

Version one is not a generic slide switcher.
Its Program Items are built-in Competition Slides, Results Slides, and Standby.
Schedule and Event-information rotations remain autonomous Views, while alerts and Technical Difficulties remain Overrides.
A post-version-one template editor may broaden the Program Item catalog.

Each Program Channel has at most one active Control Owner.
Only that authorized Crew Member may manipulate its Preview and Program Output.
Other authorized Crew Members may monitor the channel and request handover.
The owner may hand it over voluntarily; a different authorized Crew Member may also perform an explicit, confirmed takeover.

One Crew Member may own multiple Program Channels simultaneously so a small crew can operate several rooms from one desk.
A combined dashboard keeps every owned channel visible.
Navigating away or closing the control View warns about owned channels but does not release them implicitly.

If the Control Owner disconnects, ownership does not expire automatically.
The channel marks that owner Disconnected and leaves Preview and Program Output unchanged.
Another authorized Crew Member may perform an immediate confirmed takeover.
A returning former owner remains a monitor unless ownership is handed back, preventing two consoles from controlling the channel after reconnection.

After a venue-service restart, durable Program Output and other live state are restored, but Control Ownership is not.
Each Program Channel starts unowned and must be claimed explicitly by an authorized Crew Member, avoiding ghost ownership from a console session that may no longer exist.

An unsent Preview selection is not restored after restart.
Preview is reconstructed as the next intended item in the canonical sequence following the restored Program Output.
An operator must deliberately select any exceptional or previously shown item again before taking it.

A Take succeeds when the venue server durably commits the new Program Output.
It does not wait for every consuming Display to acknowledge rendering it.
The control View reports each Display as applied, lagging, or offline so delivery problems are visible without allowing one failed Display to block live control.

Ownership is per Program Channel, so independent rooms or stages remain independently controllable.
It does not restrict authorized Emergency Overrides, which remain available even when their operator does not own the affected Program Channels.

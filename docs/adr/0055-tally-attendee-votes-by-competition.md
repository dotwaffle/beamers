# Tally attendee Votes by Competition

Voting Eligibility is Event-scoped.
Venue crew issue a single-use Voting Key after admission verification.
A signed-in Account redeems it once for durable eligibility across the Event's open Competitions.
Submission Eligibility remains a separate Competition policy and defaults to all Accounts.

Each Competition inherits a Voting Method from the Event and may override it before voting opens.
Opening the Voting Window freezes the method and Self-Vote Policy.
Version one provides Range Voting from one through five.
The domain stores the method choice without introducing a speculative implementation Interface.

An Entry becomes votable when its first Competition Slide is Taken.
Live updates add it to eligible Ballots without requiring a browser refresh.
Once an Account casts any Vote in the Competition, each omitted votable Entry receives the neutral value three during tallying.
The default Self-Vote Policy Allows a Submitter Account's explicit Vote.
The alternative prevents an explicit self-Vote and contributes the same neutral value when the Submitter Account participates in the Competition.

Ballots remain account-bound and private.
Voters see their own Votes.
Crew see eligibility, participation, and completion while voting is open but not provisional aggregates or rankings.
Audit records voting lifecycle actions without Vote values.

Closing the Voting Window creates one immutable Voting Tally from the frozen method.
The tally seeds a Results Draft.
A Producer reviews placements, resolves ties, and follows the existing Ready and release workflow.
Any override requires a Crew Reason and Audit Entry.
Voting never publishes Results or makes a Placement authoritative by itself.

This supersedes the voting deferrals in ADRs 0019 and 0026.
It preserves ADR 0026's reviewed Results Draft and Prizegiving release boundaries.

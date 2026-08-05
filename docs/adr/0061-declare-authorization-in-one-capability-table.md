# Declare authorization in one capability table on the command path

This is the design pass ADR 0059 deferred: the declarative capability table for the store and command surface.
The declaration is two-level.
Every state-changing command action holds one row in a single table of pure Go struct literals stating its required Capability and its Event scope dimension, and one evaluator applies that table inside `command.Execute`.
Every store entrypoint separately declares which actor classes may reach it: a viewer identity or named System Actors.
A completeness check asserts that every registered command action has exactly one row and every named capability belongs to the closed enum.

Rows speak only capabilities.
Roles expand to capability sets at evaluation time — Producer to every Event capability, Operator to scoped operation plus explicit grants, Observer to viewing plus a granted ViewResults — so the table has one requirement kind and adding a role never rewrites rows.
Event Grants keep storing roles, and the grantable capability set stays exactly EmergencyAlert, ViewResults, and ManageResults; finer grantable capabilities are a product decision deferred until the table shows a need.
A mixed vocabulary of role floors and capabilities was rejected because it preserves two evaluation branches and leaks the grant model into every row that names a role.

A row declares its scope dimension — none, Event-wide, Lanes of the target, or Display Groups of the target — and the command's plan phase, which already loads the target inside the transaction, supplies the concrete lane or display-group facts.
The pairing is type-enforced so a scoped row cannot evaluate without its facts.
Letting the evaluator load facts itself was rejected because it duplicates reads the plan already performs and can disagree with the plan's view of the target.

Because the evaluator sits inside `command.Execute`, every capability and scope refusal commits a Command Receipt and a Rejected Audit Entry like any other rejection, which closes a real gap: Manage Results refusals previously fired in the service layer before `command.Execute` and left no evidence.
The evaluator runs after the receipt lookup, so replaying a duplicate command returns its recorded outcome without re-evaluating authorization; a receipt is the durable truth of a command that already happened, and revocation affects new commands only.
Read entrypoints reuse the evaluator but refuse with plain domain errors, because only state-changing commands leave receipts.

The blanket store-side allow decision is replaced by a closed set of System Actors — Display, public visitor, Backup, migration, replication, command replay, host maintenance — minted once where each caller enters the system by the sole constructor permitted to create the Ent allow decision.
The ADR 0059 tripwire tightens accordingly: it accepts a viewer identity or a named System Actor and nothing else, so an unmediated allow decision fails closed and the anonymous bypass idiom cannot quietly return.

The imperative checks scattered through store methods and the duplicate pre-command service checks are deleted area by area once rejection-code parity with the table is proven, leaving exactly one authority; keeping them as defense in depth was rejected as the dual-authority state ADR 0059 exists to end.
Append-only guarantees for published versions and Results Publications are an invariant rather than an authorization concern, and are enforced separately by an Ent mutation hook that denies updates and deletions outright.

Rollout is read, then specify, then migrate: first a systematic end-to-end read of the imperative layer produces the capability enum and a reviewable draft table, then the evaluator and named actors land mirroring existing behavior under parity tests, then the old checks are removed in bisectable per-area changes.

This completes the end state ADR 0059 defined, and serves ADR 0013 and ADR 0040 by making every authorization refusal on the command path produce the evidence those decisions require.

## As landed

The program shipped in tickets 234 through 242, and two mechanisms deviate from the text above; the decisions themselves are unchanged.

First, no allow decision is minted anywhere.
The plan to route every system caller through one constructor wrapping `privacy.DecisionContext` was dropped, because Ent evaluates an explicit context decision before any policy rule, so a bare allow decision would wave past the tripwire it exists to serve.
Instead `internal/systemactor` names the actor in the context, and the tripwire enforces naming from three independent positions — the privacy policy, an unconditional mutation hook, and a query interceptor — so neither queries nor mutations can proceed unnamed, and a walking test holds `privacy.DecisionContext` to zero call sites.
Every exported store entrypoint declares the actor classes it accepts in one hand-maintained map checked at the boundary.

Second, `UploadAttachment` did not stay split.
One row serves both callers: the crew path demands `ManageAttachments` through the row's `TargetCapabilities`, and the account-holder path is admitted by the table unconditionally because its rule is upload-target ownership, which the store enforces as a domain invariant.

The discrepancy resolutions the draft table deferred were decided as follows: Program Channel commands are judged by the Display Group keys of the Channel's consuming Displays (D3); live Competition Entry actions are judged by the Lanes of the Entry's Session (D4), including Reinstate, which regained the Lane scope it had lost (D5); indirect Override targets resolve to the Display Groups of their consuming Displays rather than to synthetic keys (D6); the degraded Emergency Alert path asks the same `authz.Holds` and `authz.InScope` questions the table asks (D8); every refusal the draft found evidence-free is now a durable rejection on the command path (D9); and the Producer floors layered over `ManageResults` rows are gone, so a grant suffices (D13).
A walking test now fails any file outside the viewer, auth, authz, and server packages that asks a viewer authority predicate directly, which is what keeps the table the sole authority.

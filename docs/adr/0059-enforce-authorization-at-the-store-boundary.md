# Enforce authorization at the store boundary

Authorization is enforced imperatively where the viewer exists: handlers and the command path resolve the acting Account and its Event Grants before store methods run, and store methods add capability checks where an action demands one.
The Ent privacy layer defined alongside the schema duplicates this authority declaratively, but nearly every store query runs under an explicit allow decision that bypasses it.
Two authorization systems exist; the more rigorous one is switched off at runtime while still carrying full maintenance cost and implying a guarantee the runtime does not deliver.

This decision makes the store and command surface the sole enforced authorization boundary.
The Ent privacy layer's role narrows to a fail-closed tripwire: one global rule denies any query that carries neither a viewer identity nor an explicit decision context.
The per-entity policies and grant-filtering rules are removed rather than left as unenforced documentation.

Row-level enforcement at the ORM fits systems where nearly all queries act for a signed-in principal.
Most Beamers query volume has no viewer: public Event pages, enrolled Displays, projections and snapshots, replication, Backup, migrations, and command replay.
An ORM seam therefore makes opting out the dominant call pattern, which is how the bypass became the store's default idiom.
Enforcement at the store boundary also keeps refusals expressible as domain rejections that produce Command Receipts and Audit Entries, where a policy denial deep inside a query cannot, and it keeps policy evaluation off the hottest read paths.

The intended end state is a declarative capability table at the store and command surface: each entrypoint states its required capability and Event scope, one evaluator applies the table, and the capability checks currently scattered through store methods migrate into it.
That table needs its own design pass before implementation and is not part of this decision.
Until it lands, the imperative checks remain authoritative.

This refines ADR 0034's use of Ent by fixing where its privacy machinery may be relied upon, and serves the accountability goals of ADR 0013 and ADR 0040 by keeping refusals on the command path that records who acted and with what outcome.

# Ent feature survey

Surveyed 2026-07-21 against official Ent documentation only.

## Recommendation

Keep version one deliberately small: Ent schemas and generated typed access, committed versioned migrations, default-deny privacy, explicit transactions, database-backed constraints and indexes, optimistic revisions where stale writes matter, and tests that exercise both generated behavior and the committed migration history.

Treat hooks and interceptors as in-process middleware, not durable workflow machinery.
External webhooks, recording control, and DMX control need a durable post-commit outbox.

## Recommended version-one foundations

### Schema, constraints, and indexes

- Use small mixins for genuinely universal concerns: timestamps, immutable IDs, Event ownership, and the default-deny policy.
  A mixin can contribute fields, edges, indexes, hooks, policies, and annotations; mixin hooks and policies run before schema-specific ones.
  Keep domain-specific fields out of mixins to avoid hidden coupling.
  [Mixin](https://entgo.io/docs/schema-mixin/)
- Express required/optional shape, defaults, immutability, enum values, and simple validators in fields.
  Fields are required by default.
  Validators are generated application checks, so also encode integrity that must survive alternate writers as database constraints in reviewed migrations.
  [Fields](https://entgo.io/docs/schema-fields/) [check constraints](https://entgo.io/docs/schema-annotations/#check-constraints)
- Define unique and composite indexes in schemas, including edge-field indexes for common scoped lookups.
  SQLite supports partial indexes through Ent SQL annotations.
  Derive indexes from concrete read paths; do not pre-index every filterable field.
  [Indexes](https://entgo.io/docs/schema-indexes/)
- Continue the ADR 0034 versioned workflow.
  Generate SQL from the Ent schema, review it, commit it with `atlas.sum`, replay it on a clean SQLite database, and validate/lint it in CI.
  Never call `Schema.Create` during normal startup.
  [Versioned migrations](https://entgo.io/docs/versioned-migrations/) [migration safety](https://entgo.io/docs/versioned/verifying-safety/)

### Transactions and concurrency

- Put each domain command, its Audit Entry, and any outbox rows in one explicit `client.Tx` transaction.
  Centralize rollback-on-error/panic and commit-error handling in one helper, and pass `tx.Client()` only within the callback.
  [Transactions](https://entgo.io/docs/transactions/)
- Implement optimistic locking for Draft revisions and stale live-command rejection with an explicit monotonically increasing version field.
  Update with both ID and expected-version predicates, increment the version in the same statement, and treat zero affected rows as a conflict.
  Ent documents this as an application pattern rather than a built-in switch; every writer, including escape hatches, must obey it.
  [Optimistic locking](https://entgo.io/blog/2021/07/22/database-locking-techniques-with-ent/)
- Enable `sql/upsert` only for an identified atomic idempotency need, such as a natural-key deduplication record.
  It generates `ON CONFLICT` support, including bulk upsert.
  Do not use it to bypass expected-revision checks or silently overwrite live state.
  [Feature flags: upsert](https://entgo.io/docs/feature-flags/#upsert)

### Privacy and query paths

- Keep privacy default-deny for every operational schema and both query and mutation paths.
  End policy chains with deny; require an authenticated viewer; test missing-viewer, cross-Event, role, capability, Lane, and Display cases.
  Ent evaluates schema policies on Ent queries and mutations, with `Allow`, `Deny`, and `Skip` short-circuit semantics.
  [Privacy](https://entgo.io/docs/privacy/)
- Enable `entql` for privacy filter rules where one shared Event-scope predicate must constrain both reads and writes.
  Keep construction inside trusted policy code in v1; do not expose arbitrary dynamic predicates to clients.
  [Privacy filter rules](https://entgo.io/docs/privacy/#filter-rules) [Feature flags: EntQL](https://entgo.io/docs/feature-flags/#entql-filtering)
- Forbid raw SQL on request-derived paths.
  Ent's `sql/execquery` API explicitly bypasses hooks, privacy, and validators.
  Keep migration/bootstrap use isolated, and wrap any unavoidable application escape hatch in a narrow package with an explicit system context, transaction contract, review, and tests.
  [Feature flags: SQL raw API](https://entgo.io/docs/feature-flags/#sql-raw-api)

### Hooks, outbox, and external effects

- Use mutation hooks sparingly for deterministic, in-process cross-cutting checks or instrumentation.
  Hooks wrap mutations and run at the application level, not in the database; they do not observe writes that bypass Ent.
  [Hooks](https://entgo.io/docs/hooks/)
- Never call a webhook, recorder, or DMX endpoint directly from a mutation hook or transaction hook.
  A call before commit can escape even if the transaction rolls back; a call after commit has a crash gap; retries can duplicate it.
- Instead, write an outbox row in the same transaction as the authoritative state and Audit Entry.
  After commit, a dispatcher claims rows, performs the external action, records attempts/results, and retries with stable idempotency keys.
  A post-commit transaction hook may wake the dispatcher, but the durable row—not the wake-up—is the delivery guarantee.
  Ent transaction hooks can wrap commit and rollback, including code after the underlying commit, which defines the timing but does not remove that crash gap.
  [Transaction hooks](https://entgo.io/docs/transactions/#hooks)

### Testing

- Use generated `enttest` helpers for fast schema/query tests, while adapting the SQLite connection to the selected `modernc.org/sqlite` driver.
  Ent's example opens an in-memory SQLite database and automatically creates the schema.
  [Testing](https://entgo.io/docs/testing/)
- Do not let auto-created test schemas stand in for migration tests.
  Separately replay every committed migration into a fresh database, upgrade representative older fixtures, and compare the resulting schema with the Ent target.
  Exercise constraints, indexes, foreign keys, privacy matrices, transaction rollback, optimistic conflicts, raw-SQL isolation, and outbox crash/retry/idempotency.
  [Versioned migrations](https://entgo.io/docs/versioned-migrations/) [CI](https://entgo.io/docs/ci/)

## Optional version-one experiments

- **Interceptors:** trial them for query timing, tracing, or a defensive result limit.
  Interceptors wrap final query execution; traversers act at each graph traversal step.
  Do not use either as the authorization boundary—privacy owns that—and avoid hidden business filters.
  [Interceptors](https://entgo.io/docs/interceptors/)
- **Broader EntQL:** prototype an allow-listed internal crew filter builder if static generated predicates become painful.
  Bound fields, operators, complexity, and result size; preserve privacy predicates independently.
  [Feature flags: EntQL](https://entgo.io/docs/feature-flags/#entql-filtering)
- **Selective upsert:** benchmark one idempotency/dedup path and retain it only
  if it makes the concurrency contract clearer than create-and-handle-conflict.
  [Feature flags: upsert](https://entgo.io/docs/feature-flags/#upsert)

## Post-version-one only

- **GraphQL/entgql:** defer until a GraphQL product requirement exists.
  `entgql` can generate schema and Relay-oriented pagination/filtering, collect fields to avoid N+1 queries, and provide transactional mutations, but it adds another code-generation surface and can couple the public API to persistence shape.
  If adopted, annotate exposure explicitly, pin compatible Ent/contrib/gqlgen versions, review generated schema diffs, cap query complexity, and retain Ent privacy beneath every resolver.
  [GraphQL integration](https://entgo.io/docs/graphql/)
- **General client-supplied EntQL:** defer an open-ended query language until
  authorization composition, cost limits, stable API semantics, and abuse tests
  are designed.
- **General raw SQL/modifier access:** defer beyond the isolated migration and bootstrap boundary.
  Custom SQL modifiers and raw execution increase the number of paths that can evade generated invariants and policy.
  [Feature flags](https://entgo.io/docs/feature-flags/)
- **Synchronous hook-driven integrations:** do not add them later either; extend
  the outbox with destination-specific delivery policy instead.

## First implementation slice

1. Freeze Ent code-generation flags: privacy, EntQL for policy filters,
   schema snapshot, versioned migrations, and only justified upsert support.
2. Add the universal mixins and two representative operational schemas with
   database constraints/indexes and terminal-deny policies.
3. Add one transaction helper, one expected-revision update, and the
   Audit Entry plus outbox write in the same transaction.
4. Prove the privacy matrix, stale-write conflict, rollback atomicity, committed
   migration replay, and outbox retry/idempotency before expanding the model.

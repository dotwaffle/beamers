# Center writes on a command module

All application state changes pass through a deep command module.
Transport adapters translate HTTP, Connect, and future integration inputs into domain commands; they do not contain domain transitions or persistence orchestration.

For each command, the module establishes actor and command identity, verifies idempotency and expected revisions, loads required state, applies deterministic domain rules, persists the resulting state with its Audit Entry and any outbox record, commits, and only then notifies live consumers.
The module returns a stable result describing the committed outcome.

Domain transition logic accepts explicit state, command values, and time and returns decisions without performing I/O.
Production and test clocks form a real seam.
Pure tests exercise timing, ripple, Competition, and live-control rules without a database.

The persistence implementation uses the concrete Ent store.
Generated Ent entities do not escape the store module, and transport types do not enter it.
There is no generic repository interface solely to permit mocks: persistence, privacy, migrations, and transaction behavior are tested using temporary real SQLite databases.
Direct Ent mutations are confined to the store, migration, and bootstrap modules.

Read paths use dedicated query projections for public Schedule, Crew, control, and Display needs rather than exposing generic CRUD or rebuilding domain state inside handlers.

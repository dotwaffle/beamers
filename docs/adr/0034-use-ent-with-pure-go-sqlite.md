# Use Ent with pure-Go SQLite

Version one uses Ent for its persistence schema and generated data-access code, with `modernc.org/sqlite` as the SQLite driver.
Production builds therefore do not require CGo.
SQLite remains the authoritative store described in ADR 0011.

Schema changes use versioned migration files generated from the Ent schema and committed with the application.
Normal service startup never invokes Ent's automatic `Schema.Create` migration path.

An explicit migration command performs a preflight, requires a verified backup, and applies every pending migration transactionally where SQLite permits.
It refuses a database with an unknown or newer schema version.
Version one does not provide down migrations; operational rollback restores the verified backup and runs the prior application binary.

Ent's schema remains the source for application persistence behavior, while the committed migration history remains the source for upgrading existing databases.

Ent privacy policies form the final application authorization boundary for all operational entities.
They deny queries and mutations without an authenticated viewer and enforce Event Grants, roles, Lane and Display scopes, and separately granted capabilities.
Request handlers may repeat checks to return clearer errors, but cannot grant access that the privacy policy denies.

Ent's explicit privacy-decision bypass is restricted to narrowly isolated migration, bootstrap, and internal system paths.
Request-derived contexts cannot select or inherit it.
Policy tests cover every role, capability, and scope, including missing-viewer and cross-Event cases.

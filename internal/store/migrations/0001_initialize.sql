-- Create "installations" table
CREATE TABLE `installations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL
);
-- Create "beamers_schema_migrations" table
CREATE TABLE `beamers_schema_migrations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `version` integer NOT NULL,
  `name` text NOT NULL,
  `checksum` text NOT NULL,
  `applied_at` datetime NOT NULL,
  CONSTRAINT `schema_migrations_checksum_length` CHECK (length(checksum) = 64)
);
-- Create index "beamers_schema_migrations_version_key" to table: "beamers_schema_migrations"
CREATE UNIQUE INDEX `beamers_schema_migrations_version_key` ON `beamers_schema_migrations` (`version`);
-- Create index "beamers_schema_migrations_name_key" to table: "beamers_schema_migrations"
CREATE UNIQUE INDEX `beamers_schema_migrations_name_key` ON `beamers_schema_migrations` (`name`);

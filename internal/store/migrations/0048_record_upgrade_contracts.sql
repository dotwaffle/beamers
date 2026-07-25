-- Add explicit upgrade safety and compatibility contracts.
ALTER TABLE `beamers_schema_migrations` ADD COLUMN `safety` text NOT NULL DEFAULT 'Baseline';
ALTER TABLE `beamers_schema_migrations` ADD COLUMN `minimum_reader_schema_version` integer NOT NULL DEFAULT 48;
ALTER TABLE `beamers_schema_migrations` ADD COLUMN `minimum_writer_schema_version` integer NOT NULL DEFAULT 48;

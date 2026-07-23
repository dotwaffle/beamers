-- Add column "program_output_kind" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_output_kind` text NOT NULL DEFAULT 'Standby';
-- Add column "program_output_entry_id" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_output_entry_id` integer NULL;
-- Add column "program_output_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_output_revision` integer NOT NULL DEFAULT 0;
-- Add column "program_cursor" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_cursor` integer NOT NULL DEFAULT -1;
-- Add column "program_output_taken_at" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_output_taken_at` datetime NULL;

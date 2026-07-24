-- Add column "program_output_result" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `program_output_result` json NULL;
-- Add column "operation_revision" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `operation_revision` integer NOT NULL DEFAULT 0;
-- Add column "item_states" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `item_states` json NULL;

-- Add column "communicated_start" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `communicated_start` datetime NULL;
-- Add column "communicated_end" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `communicated_end` datetime NULL;

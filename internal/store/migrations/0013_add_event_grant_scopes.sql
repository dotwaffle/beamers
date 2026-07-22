-- Add column "lane_ids" to table: "event_grants"
ALTER TABLE `event_grants` ADD COLUMN `lane_ids` json NULL;
-- Add column "display_group_keys" to table: "event_grants"
ALTER TABLE `event_grants` ADD COLUMN `display_group_keys` json NULL;
-- Add column "capabilities" to table: "event_grants"
ALTER TABLE `event_grants` ADD COLUMN `capabilities` json NULL;

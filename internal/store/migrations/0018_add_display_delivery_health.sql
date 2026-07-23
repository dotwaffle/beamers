-- Add column "applied_asset_version" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_asset_version` text NOT NULL DEFAULT '';
-- Add column "applied_standby" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_standby` bool NOT NULL DEFAULT true;
-- Add column "clock_offset_milliseconds" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `clock_offset_milliseconds` integer NOT NULL DEFAULT 0;
-- Add column "clock_uncertainty_milliseconds" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `clock_uncertainty_milliseconds` integer NOT NULL DEFAULT 0;
-- Add column "renderer_unstable" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `renderer_unstable` bool NOT NULL DEFAULT false;

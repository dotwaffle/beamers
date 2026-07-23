-- Add column "applied_urgent_notice_id" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_urgent_notice_id` integer NOT NULL DEFAULT 0;
-- Add column "applied_urgent_notice_revision" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_urgent_notice_revision` integer NOT NULL DEFAULT 0;
-- Add column "applied_emergency_alert_id" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_emergency_alert_id` integer NOT NULL DEFAULT 0;
-- Add column "applied_emergency_alert_revision" to table: "displays"
ALTER TABLE `displays` ADD COLUMN `applied_emergency_alert_revision` integer NOT NULL DEFAULT 0;
-- Add column "target_type" to table: "display_overrides"
ALTER TABLE `display_overrides` ADD COLUMN `target_type` text NOT NULL DEFAULT 'DisplayGroup';
-- Add column "target_id" to table: "display_overrides"
ALTER TABLE `display_overrides` ADD COLUMN `target_id` integer NOT NULL DEFAULT 0;
-- Add column "presentation" to table: "display_overrides"
ALTER TABLE `display_overrides` ADD COLUMN `presentation` text NOT NULL DEFAULT 'Overlay';
-- Existing Technical Difficulties Overrides are fullscreen replacements.
UPDATE `display_overrides` SET `presentation` = 'Replace' WHERE `kind` = 'TechnicalDifficulties';

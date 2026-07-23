-- Add column "target_adjustment_presets" to table: "events"
ALTER TABLE `events` ADD COLUMN `target_adjustment_presets` text NOT NULL DEFAULT '[-300,300,600]';
-- Add column "forecast_start" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `forecast_start` datetime NULL;
-- Add column "forecast_end" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `forecast_end` datetime NULL;
-- Add column "target_adjustment_seconds" to table: "session_runs"
ALTER TABLE `session_runs` ADD COLUMN `target_adjustment_seconds` integer NOT NULL DEFAULT 0;
-- Add column "target_adjusted_at" to table: "session_runs"
ALTER TABLE `session_runs` ADD COLUMN `target_adjusted_at` datetime NULL;

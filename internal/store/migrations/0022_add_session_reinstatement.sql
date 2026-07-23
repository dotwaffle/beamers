-- Add column "previous_forecast_start" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `previous_forecast_start` datetime NULL;
-- Add column "forecast_lane_ids" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `forecast_lane_ids` json NULL;
-- Add column "forecast_location_ids" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `forecast_location_ids` json NULL;

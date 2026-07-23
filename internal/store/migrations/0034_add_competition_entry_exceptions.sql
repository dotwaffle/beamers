-- Add column "presentation_status" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `presentation_status` text NOT NULL DEFAULT 'Scheduled';
-- Preserve presentation history created before explicit presentation status.
UPDATE `competition_entries` SET `presentation_status` = 'Presented' WHERE `first_presented_at` IS NOT NULL;
-- Add column "deferred_sequence" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `deferred_sequence` integer NULL;
-- Add column "resolution_required" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `resolution_required` bool NOT NULL DEFAULT false;
-- Add column "result_disposition" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `result_disposition` text NOT NULL DEFAULT 'Eligible';
-- Add column "technical_failure_reason" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `technical_failure_reason` text NULL;
-- Add column "resolution_crew_reason" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `resolution_crew_reason` text NULL;
-- Add column "public_disqualification_message" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `public_disqualification_message` text NULL;
-- Add column "release_hold" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `release_hold` bool NOT NULL DEFAULT false;

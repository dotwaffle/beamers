-- Add column "public_cancellation_message" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `public_cancellation_message` text NULL;
-- Add column "cancellation_crew_notes" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `cancellation_crew_notes` text NULL;

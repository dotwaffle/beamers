-- Add column "corrected_title" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `corrected_title` text NULL;
-- Add column "corrected_speaker" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `corrected_speaker` text NULL;
-- Add column "corrected_public_details" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `corrected_public_details` text NULL;
-- Add column "speaker" to table: "session_drafts"
ALTER TABLE `session_drafts` ADD COLUMN `speaker` text NULL;
-- Add column "speaker" to table: "session_published_versions"
ALTER TABLE `session_published_versions` ADD COLUMN `speaker` text NULL;
-- Create "session_run_amendments" table
CREATE TABLE `session_run_amendments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_account_id` integer NOT NULL,
  `details_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_run_id` integer NOT NULL,
  CONSTRAINT `session_run_amendments_session_runs_amendments` FOREIGN KEY (`session_run_id`) REFERENCES `session_runs` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionrunamendment_session_run_id" to table: "session_run_amendments"
CREATE INDEX `sessionrunamendment_session_run_id` ON `session_run_amendments` (`session_run_id`);

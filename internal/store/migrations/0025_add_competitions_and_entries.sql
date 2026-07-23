-- Add column "entry_default_disposition" to table: "events"
ALTER TABLE `events` ADD COLUMN `entry_default_disposition` text NOT NULL DEFAULT 'Pending';
-- Add column "submission_deadline" to table: "session_drafts"
ALTER TABLE `session_drafts` ADD COLUMN `submission_deadline` datetime NULL;
-- Add column "entry_default_disposition" to table: "session_drafts"
ALTER TABLE `session_drafts` ADD COLUMN `entry_default_disposition` text NULL;
-- Add column "submission_deadline" to table: "session_published_versions"
ALTER TABLE `session_published_versions` ADD COLUMN `submission_deadline` datetime NULL;
-- Add column "entry_default_disposition" to table: "session_published_versions"
ALTER TABLE `session_published_versions` ADD COLUMN `entry_default_disposition` text NULL;
-- Create "competition_entries" table
CREATE TABLE `competition_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `disposition` text NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_entries_sessions_competition_entries` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_entries_events_competition_entries` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "competitionentry_competition_session_id_created_at" to table: "competition_entries"
CREATE INDEX `competitionentry_competition_session_id_created_at` ON `competition_entries` (`competition_session_id`, `created_at`);

-- Add column "awards" to table: "competition_results_drafts"
ALTER TABLE `competition_results_drafts` ADD COLUMN `awards` json NULL;
-- Create "event_awards_drafts" table
CREATE TABLE `event_awards_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `awards` json NULL,
  `path_states` json NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `event_awards_drafts_events_event_awards_drafts` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "eventawardsdraft_event_id_revision" to table: "event_awards_drafts"
CREATE UNIQUE INDEX `eventawardsdraft_event_id_revision` ON `event_awards_drafts` (`event_id`, `revision`);

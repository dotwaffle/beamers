-- Add column "revision" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `revision` integer NOT NULL DEFAULT 0;
-- Add column "competition_session_ids" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `competition_session_ids` json NULL;
-- Add column "sequence" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `sequence` json NULL;
-- Add column "publication_order" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `publication_order` json NULL;
-- Add column "results_text_template" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `results_text_template` json NULL;
-- Add column "locked" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `locked` bool NOT NULL DEFAULT false;
-- Add column "preflight_lock" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `preflight_lock` json NULL;
-- Add column "locked_by_account_id" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `locked_by_account_id` integer NULL;
-- Add column "locked_at" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `locked_at` datetime NULL;
-- Create "prizegiving_competitions" table
CREATE TABLE `prizegiving_competitions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `prizegiving_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `prizegiving_competitions_sessions_prizegiving_assignment` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegiving_competitions_prizegivings_competitions` FOREIGN KEY (`prizegiving_id`) REFERENCES `prizegivings` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegiving_competitions_events_prizegiving_competitions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "prizegiving_competitions_competition_session_id_key" to table: "prizegiving_competitions"
CREATE UNIQUE INDEX `prizegiving_competitions_competition_session_id_key` ON `prizegiving_competitions` (`competition_session_id`);
-- Create index "prizegivingcompetition_event_id_prizegiving_id_competition_session_id" to table: "prizegiving_competitions"
CREATE UNIQUE INDEX `prizegivingcompetition_event_id_prizegiving_id_competition_session_id` ON `prizegiving_competitions` (`event_id`, `prizegiving_id`, `competition_session_id`);

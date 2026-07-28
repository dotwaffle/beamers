-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "voting_tallies" table
CREATE TABLE `voting_tallies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `method` text NOT NULL,
  `self_vote_policy` text NOT NULL,
  `participating` integer NOT NULL,
  `entries` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `voting_tallies_sessions_voting_tallies` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `voting_tallies_events_voting_tallies` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "votingtally_competition_session_id" to table: "voting_tallies"
CREATE UNIQUE INDEX `votingtally_competition_session_id` ON `voting_tallies` (`competition_session_id`);
-- Create index "votingtally_event_id_competition_session_id" to table: "voting_tallies"
CREATE INDEX `votingtally_event_id_competition_session_id` ON `voting_tallies` (`event_id`, `competition_session_id`);
-- Create "new_competition_results_drafts" table
CREATE TABLE `new_competition_results_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `disposition` text NOT NULL,
  `no_public_crew_reason` text NULL,
  `public_explanation` text NULL,
  `score_type` text NOT NULL,
  `score_visibility` text NOT NULL DEFAULT 'Public',
  `score_unit` text NULL,
  `score_precision` integer NOT NULL DEFAULT 0,
  `score_requirement` text NOT NULL DEFAULT 'Optional',
  `score_interpretation` text NOT NULL DEFAULT 'Informational',
  `awards` json NULL,
  `tally_override_crew_reason` text NULL,
  `ready_by_account_id` integer NULL,
  `ready_at` datetime NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  `voting_tally_id` integer NULL,
  CONSTRAINT `competition_results_drafts_voting_tallies_results_drafts` FOREIGN KEY (`voting_tally_id`) REFERENCES `voting_tallies` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `competition_results_drafts_sessions_competition_results_drafts` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_results_drafts_events_competition_results_drafts` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Copy rows from old table "competition_results_drafts" to new temporary table "new_competition_results_drafts"
INSERT INTO `new_competition_results_drafts` (`id`, `revision`, `disposition`, `no_public_crew_reason`, `public_explanation`, `score_type`, `score_visibility`, `score_unit`, `score_precision`, `score_requirement`, `score_interpretation`, `awards`, `ready_by_account_id`, `ready_at`, `created_by_account_id`, `created_at`, `event_id`, `competition_session_id`) SELECT `id`, `revision`, `disposition`, `no_public_crew_reason`, `public_explanation`, `score_type`, `score_visibility`, `score_unit`, `score_precision`, `score_requirement`, `score_interpretation`, `awards`, `ready_by_account_id`, `ready_at`, `created_by_account_id`, `created_at`, `event_id`, `competition_session_id` FROM `competition_results_drafts`;
-- Drop "competition_results_drafts" table after copying rows
DROP TABLE `competition_results_drafts`;
-- Rename temporary table "new_competition_results_drafts" to "competition_results_drafts"
ALTER TABLE `new_competition_results_drafts` RENAME TO `competition_results_drafts`;
-- Create index "competitionresultsdraft_competition_session_id_revision" to table: "competition_results_drafts"
CREATE UNIQUE INDEX `competitionresultsdraft_competition_session_id_revision` ON `competition_results_drafts` (`competition_session_id`, `revision`);
-- Create index "competitionresultsdraft_event_id_competition_session_id_revision" to table: "competition_results_drafts"
CREATE INDEX `competitionresultsdraft_event_id_competition_session_id_revision` ON `competition_results_drafts` (`event_id`, `competition_session_id`, `revision`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

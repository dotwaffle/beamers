-- Create "competition_result_standings" table
CREATE TABLE `competition_result_standings` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `standing` text NOT NULL,
  `placement` integer NULL,
  `display_order` integer NOT NULL,
  `decimal_score` text NULL,
  `duration_score_nanos` integer NULL,
  `entry_id` integer NOT NULL,
  `results_draft_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_result_standings_sessions_competition_result_standings` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_events_competition_result_standings` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_competition_results_drafts_standings` FOREIGN KEY (`results_draft_id`) REFERENCES `competition_results_drafts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_competition_entries_result_standings` FOREIGN KEY (`entry_id`) REFERENCES `competition_entries` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "competitionresultstanding_results_draft_id_entry_id" to table: "competition_result_standings"
CREATE UNIQUE INDEX `competitionresultstanding_results_draft_id_entry_id` ON `competition_result_standings` (`results_draft_id`, `entry_id`);
-- Create index "competitionresultstanding_results_draft_id_display_order" to table: "competition_result_standings"
CREATE UNIQUE INDEX `competitionresultstanding_results_draft_id_display_order` ON `competition_result_standings` (`results_draft_id`, `display_order`);
-- Create index "competitionresultstanding_competition_session_id_results_draft_id" to table: "competition_result_standings"
CREATE INDEX `competitionresultstanding_competition_session_id_results_draft_id` ON `competition_result_standings` (`competition_session_id`, `results_draft_id`);
-- Create "competition_results_drafts" table
CREATE TABLE `competition_results_drafts` (
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
  `ready_by_account_id` integer NULL,
  `ready_at` datetime NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_results_drafts_sessions_competition_results_drafts` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_results_drafts_events_competition_results_drafts` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "competitionresultsdraft_competition_session_id_revision" to table: "competition_results_drafts"
CREATE UNIQUE INDEX `competitionresultsdraft_competition_session_id_revision` ON `competition_results_drafts` (`competition_session_id`, `revision`);
-- Create index "competitionresultsdraft_event_id_competition_session_id_revision" to table: "competition_results_drafts"
CREATE INDEX `competitionresultsdraft_event_id_competition_session_id_revision` ON `competition_results_drafts` (`event_id`, `competition_session_id`, `revision`);

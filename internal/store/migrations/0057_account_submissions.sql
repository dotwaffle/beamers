-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Add column "submission_eligibility" to table: "events"
ALTER TABLE `events` ADD COLUMN `submission_eligibility` text NOT NULL DEFAULT 'AllAccounts';
-- Add column "submission_eligibility_override" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `submission_eligibility_override` text NULL;
-- Add column "submission_eligibility_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `submission_eligibility_revision` integer NOT NULL DEFAULT 0;
-- Create "new_competition_entries" table
CREATE TABLE `new_competition_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `disposition` text NOT NULL,
  `upload_closed_at` datetime NULL,
  `content_revision` integer NOT NULL DEFAULT 1,
  `reviewed_content_revision` integer NULL,
  `reviewed_by_account_id` integer NULL,
  `reviewed_at` datetime NULL,
  `first_presented_at` datetime NULL,
  `presentation_status` text NOT NULL DEFAULT 'Scheduled',
  `deferred_sequence` integer NULL,
  `resolution_required` bool NOT NULL DEFAULT false,
  `result_disposition` text NOT NULL DEFAULT 'Eligible',
  `technical_failure_reason` text NULL,
  `resolution_crew_reason` text NULL,
  `public_disqualification_message` text NULL,
  `release_hold` bool NOT NULL DEFAULT false,
  `revision` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `submitter_account_id` integer NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_entries_sessions_competition_entries` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_entries_events_competition_entries` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_entries_accounts_competition_entries` FOREIGN KEY (`submitter_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "competition_entries" to new temporary table "new_competition_entries"
INSERT INTO `new_competition_entries` (`id`, `name`, `public_details`, `crew_notes`, `disposition`, `upload_closed_at`, `content_revision`, `reviewed_content_revision`, `reviewed_by_account_id`, `reviewed_at`, `first_presented_at`, `presentation_status`, `deferred_sequence`, `resolution_required`, `result_disposition`, `technical_failure_reason`, `resolution_crew_reason`, `public_disqualification_message`, `release_hold`, `revision`, `created_at`, `event_id`, `competition_session_id`) SELECT `id`, `name`, `public_details`, `crew_notes`, `disposition`, `upload_closed_at`, `content_revision`, `reviewed_content_revision`, `reviewed_by_account_id`, `reviewed_at`, `first_presented_at`, `presentation_status`, `deferred_sequence`, `resolution_required`, `result_disposition`, `technical_failure_reason`, `resolution_crew_reason`, `public_disqualification_message`, `release_hold`, `revision`, `created_at`, `event_id`, `competition_session_id` FROM `competition_entries`;
-- Drop "competition_entries" table after copying rows
DROP TABLE `competition_entries`;
-- Rename temporary table "new_competition_entries" to "competition_entries"
ALTER TABLE `new_competition_entries` RENAME TO `competition_entries`;
-- Create index "competitionentry_competition_session_id_created_at" to table: "competition_entries"
CREATE INDEX `competitionentry_competition_session_id_created_at` ON `competition_entries` (`competition_session_id`, `created_at`);
-- Create "voting_eligibilities" table
CREATE TABLE `voting_eligibilities` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `voting_eligibilities_events_voting_eligibilities` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `voting_eligibilities_accounts_voting_eligibilities` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "votingeligibility_event_id_account_id" to table: "voting_eligibilities"
CREATE UNIQUE INDEX `votingeligibility_event_id_account_id` ON `voting_eligibilities` (`event_id`, `account_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

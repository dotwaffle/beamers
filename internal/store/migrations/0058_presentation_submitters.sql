-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Preserve cross-Event Session membership checks across the table rebuild.
DROP TRIGGER `session_draft_lanes_same_event_insert`;
DROP TRIGGER `session_draft_locations_same_event_insert`;
DROP TRIGGER `session_draft_tracks_same_event_insert`;
DROP TRIGGER `session_published_lanes_same_event_insert`;
DROP TRIGGER `session_published_locations_same_event_insert`;
DROP TRIGGER `session_published_tracks_same_event_insert`;
-- Create "new_sessions" table
CREATE TABLE `new_sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `lifecycle` text NOT NULL DEFAULT 'Scheduled',
  `live_state_revision` integer NOT NULL DEFAULT 0,
  `forecast_start` datetime NULL,
  `forecast_end` datetime NULL,
  `communicated_start` datetime NULL,
  `communicated_end` datetime NULL,
  `previous_forecast_start` datetime NULL,
  `forecast_lane_ids` json NULL,
  `forecast_location_ids` json NULL,
  `public_cancellation_message` text NULL,
  `cancellation_crew_notes` text NULL,
  `corrected_title` text NULL,
  `corrected_speaker` text NULL,
  `corrected_public_details` text NULL,
  `presentation_submission_revision` integer NOT NULL DEFAULT 0,
  `require_entry_review` bool NOT NULL DEFAULT false,
  `submission_eligibility_override` text NULL,
  `submission_eligibility_revision` integer NOT NULL DEFAULT 0,
  `file_delivery_required` bool NULL,
  `readiness_revision` integer NOT NULL DEFAULT 0,
  `entry_order_policy` text NOT NULL DEFAULT 'DeterministicShuffle',
  `entry_order_seed` integer NOT NULL DEFAULT 0,
  `entry_order_manual_ids` json NULL,
  `locked_entry_order_ids` json NULL,
  `entry_order_locked_at` datetime NULL,
  `entry_order_revision` integer NOT NULL DEFAULT 0,
  `program_output_kind` text NOT NULL DEFAULT 'Standby',
  `program_output_entry_id` integer NULL,
  `program_output_result` json NULL,
  `program_output_revision` integer NOT NULL DEFAULT 0,
  `program_cursor` integer NOT NULL DEFAULT -1,
  `program_output_taken_at` datetime NULL,
  `attachment_release_policy_override` text NULL,
  `attachment_release_revision` integer NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL,
  `submitter_account_id` integer NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `sessions_events_sessions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `sessions_accounts_submitted_presentations` FOREIGN KEY (`submitter_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "sessions" to new temporary table "new_sessions"
INSERT INTO `new_sessions` (`id`, `lifecycle`, `live_state_revision`, `forecast_start`, `forecast_end`, `communicated_start`, `communicated_end`, `previous_forecast_start`, `forecast_lane_ids`, `forecast_location_ids`, `public_cancellation_message`, `cancellation_crew_notes`, `corrected_title`, `corrected_speaker`, `corrected_public_details`, `require_entry_review`, `submission_eligibility_override`, `submission_eligibility_revision`, `file_delivery_required`, `readiness_revision`, `entry_order_policy`, `entry_order_seed`, `entry_order_manual_ids`, `locked_entry_order_ids`, `entry_order_locked_at`, `entry_order_revision`, `program_output_kind`, `program_output_entry_id`, `program_output_result`, `program_output_revision`, `program_cursor`, `program_output_taken_at`, `attachment_release_policy_override`, `attachment_release_revision`, `created_at`, `event_id`) SELECT `id`, `lifecycle`, `live_state_revision`, `forecast_start`, `forecast_end`, `communicated_start`, `communicated_end`, `previous_forecast_start`, `forecast_lane_ids`, `forecast_location_ids`, `public_cancellation_message`, `cancellation_crew_notes`, `corrected_title`, `corrected_speaker`, `corrected_public_details`, `require_entry_review`, `submission_eligibility_override`, `submission_eligibility_revision`, `file_delivery_required`, `readiness_revision`, `entry_order_policy`, `entry_order_seed`, `entry_order_manual_ids`, `locked_entry_order_ids`, `entry_order_locked_at`, `entry_order_revision`, `program_output_kind`, `program_output_entry_id`, `program_output_result`, `program_output_revision`, `program_cursor`, `program_output_taken_at`, `attachment_release_policy_override`, `attachment_release_revision`, `created_at`, `event_id` FROM `sessions`;
-- Drop "sessions" table after copying rows
DROP TABLE `sessions`;
-- Rename temporary table "new_sessions" to "sessions"
ALTER TABLE `new_sessions` RENAME TO `sessions`;
-- Create index "session_submitter_account_id" to table: "sessions"
CREATE INDEX `session_submitter_account_id` ON `sessions` (`submitter_account_id`);
CREATE TRIGGER `session_draft_lanes_same_event_insert`
BEFORE INSERT ON `session_draft_lanes`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_drafts` ON `session_drafts`.`session_id` = `sessions`.`id` WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
CREATE TRIGGER `session_draft_locations_same_event_insert`
BEFORE INSERT ON `session_draft_locations`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_drafts` ON `session_drafts`.`session_id` = `sessions`.`id` WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
CREATE TRIGGER `session_draft_tracks_same_event_insert`
BEFORE INSERT ON `session_draft_tracks`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_drafts` ON `session_drafts`.`session_id` = `sessions`.`id` WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `tracks` WHERE `id` = NEW.`track_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
CREATE TRIGGER `session_published_lanes_same_event_insert`
BEFORE INSERT ON `session_published_version_lanes`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_published_versions` ON `session_published_versions`.`session_id` = `sessions`.`id` WHERE `session_published_versions`.`id` = NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
CREATE TRIGGER `session_published_locations_same_event_insert`
BEFORE INSERT ON `session_published_version_locations`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_published_versions` ON `session_published_versions`.`session_id` = `sessions`.`id` WHERE `session_published_versions`.`id` = NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
CREATE TRIGGER `session_published_tracks_same_event_insert`
BEFORE INSERT ON `session_published_version_tracks`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions` JOIN `session_published_versions` ON `session_published_versions`.`session_id` = `sessions`.`id` WHERE `session_published_versions`.`id` = NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `tracks` WHERE `id` = NEW.`track_id`)
BEGIN SELECT RAISE(ABORT, 'Session membership must belong to the same Event'); END;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

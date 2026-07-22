-- Backfill the required empty Rundown for Events created before this invariant.
INSERT INTO `rundowns` (`draft_revision`, `published_revision`, `event_id`)
SELECT 0, 0, `id` FROM `events`
WHERE NOT EXISTS (SELECT 1 FROM `rundowns` WHERE `rundowns`.`event_id` = `events`.`id`);
-- Create "draft_changes" table
CREATE TABLE `draft_changes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `kind` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `fact_key` text NOT NULL,
  `payload_json` text NOT NULL,
  `status` text NOT NULL DEFAULT 'Effective',
  `published_revision` integer NULL,
  `created_at` datetime NOT NULL,
  `draft_edit_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `draft_changes_events_draft_changes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_changes_draft_edits_changes` FOREIGN KEY (`draft_edit_id`) REFERENCES `draft_edits` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftchange_event_id_revision" to table: "draft_changes"
CREATE INDEX `draftchange_event_id_revision` ON `draft_changes` (`event_id`, `revision`);
-- Create index "draftchange_event_id_target_type_target_id_fact_key_status" to table: "draft_changes"
CREATE INDEX `draftchange_event_id_target_type_target_id_fact_key_status` ON `draft_changes` (`event_id`, `target_type`, `target_id`, `fact_key`, `status`);
-- Create "draft_change_dependencies" table
CREATE TABLE `draft_change_dependencies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `change_id` integer NOT NULL,
  `depends_on_id` integer NOT NULL,
  CONSTRAINT `draft_change_dependencies_draft_changes_dependents` FOREIGN KEY (`depends_on_id`) REFERENCES `draft_changes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_change_dependencies_draft_changes_dependencies` FOREIGN KEY (`change_id`) REFERENCES `draft_changes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftchangedependency_change_id_depends_on_id" to table: "draft_change_dependencies"
CREATE UNIQUE INDEX `draftchangedependency_change_id_depends_on_id` ON `draft_change_dependencies` (`change_id`, `depends_on_id`);
-- Create "draft_edits" table
CREATE TABLE `draft_edits` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `actor_account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `draft_edits_events_draft_edits` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_edits_accounts_draft_edits` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftedit_event_id_revision" to table: "draft_edits"
CREATE UNIQUE INDEX `draftedit_event_id_revision` ON `draft_edits` (`event_id`, `revision`);

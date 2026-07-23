-- Add column "stage_message_presets" to table: "events"
ALTER TABLE `events` ADD COLUMN `stage_message_presets` text NOT NULL DEFAULT '[]';
-- Add column "stage_message_default_duration_seconds" to table: "events"
ALTER TABLE `events` ADD COLUMN `stage_message_default_duration_seconds` integer NOT NULL DEFAULT 10;
-- Add column "stage_message_configuration_revision" to table: "events"
ALTER TABLE `events` ADD COLUMN `stage_message_configuration_revision` integer NOT NULL DEFAULT 0;
-- Add applied Override delivery columns to "displays"
ALTER TABLE `displays` ADD COLUMN `applied_stage_message_id` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_stage_message_revision` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_technical_difficulties_id` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_technical_difficulties_revision` integer NOT NULL DEFAULT 0;
-- Add column "display_group_keys" to table: "display_assignments"
ALTER TABLE `display_assignments` ADD COLUMN `display_group_keys` json NULL;
-- Create "display_overrides" table
CREATE TABLE `display_overrides` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `target_group_key` text NOT NULL,
  `kind` text NOT NULL,
  `text` text NOT NULL,
  `emphasis` text NOT NULL DEFAULT 'Normal',
  `preset_key` text NULL,
  `until_cleared` bool NOT NULL DEFAULT false,
  `expires_at` datetime NULL,
  `cleared_at` datetime NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `display_overrides_events_display_overrides` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayoverride_event_id_kind_target_group_key_created_at" to table: "display_overrides"
CREATE INDEX `displayoverride_event_id_kind_target_group_key_created_at` ON `display_overrides` (`event_id`, `kind`, `target_group_key`, `created_at`);
-- Create index "displayoverride_event_id_cleared_at_expires_at" to table: "display_overrides"
CREATE INDEX `displayoverride_event_id_cleared_at_expires_at` ON `display_overrides` (`event_id`, `cleared_at`, `expires_at`);
-- Create "display_override_states" table
CREATE TABLE `display_override_states` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `kind` text NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `updated_at` datetime NOT NULL,
  `display_id` integer NOT NULL,
  `override_id` integer NOT NULL,
  CONSTRAINT `display_override_states_display_overrides_states` FOREIGN KEY (`override_id`) REFERENCES `display_overrides` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_override_states_displays_override_states` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayoverridestate_event_id_display_id_kind" to table: "display_override_states"
CREATE UNIQUE INDEX `displayoverridestate_event_id_display_id_kind` ON `display_override_states` (`event_id`, `display_id`, `kind`);
-- Create index "displayoverridestate_override_id" to table: "display_override_states"
CREATE INDEX `displayoverridestate_override_id` ON `display_override_states` (`override_id`);

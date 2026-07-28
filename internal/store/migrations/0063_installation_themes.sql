-- Create "installation_theme_revisions" table
CREATE TABLE `installation_theme_revisions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `config` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL
);
-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_installations" table
CREATE TABLE `new_installations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `activation_generation` integer NOT NULL DEFAULT 0,
  `active_event_id` integer NULL,
  `active_theme_revision_id` integer NULL,
  CONSTRAINT `installations_installation_theme_revisions_active_theme_revision` FOREIGN KEY (`active_theme_revision_id`) REFERENCES `installation_theme_revisions` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `installations_events_active_event` FOREIGN KEY (`active_event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "installations" to new temporary table "new_installations"
INSERT INTO `new_installations` (`id`, `created_at`, `activation_generation`, `active_event_id`) SELECT `id`, `created_at`, `activation_generation`, `active_event_id` FROM `installations`;
-- Drop "installations" table after copying rows
DROP TABLE `installations`;
-- Rename temporary table "new_installations" to "installations"
ALTER TABLE `new_installations` RENAME TO `installations`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

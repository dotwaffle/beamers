-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_installations" table
CREATE TABLE `new_installations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `activation_generation` integer NOT NULL DEFAULT 0,
  `active_event_id` integer NULL,
  CONSTRAINT `installations_events_active_event` FOREIGN KEY (`active_event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "installations" to new temporary table "new_installations"
INSERT INTO `new_installations` (`id`, `created_at`) SELECT `id`, `created_at` FROM `installations`;
-- Drop "installations" table after copying rows
DROP TABLE `installations`;
-- Rename temporary table "new_installations" to "installations"
ALTER TABLE `new_installations` RENAME TO `installations`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

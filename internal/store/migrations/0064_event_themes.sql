-- Create "event_theme_revisions" table
CREATE TABLE `event_theme_revisions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `config` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  CONSTRAINT `event_theme_revisions_events_theme_revisions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
CREATE INDEX `eventthemerevision_event_id` ON `event_theme_revisions` (`event_id`);
-- Add Event active Theme selection
ALTER TABLE `events` ADD COLUMN `active_theme_revision_id` integer NULL REFERENCES `event_theme_revisions` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL;
-- Prevent selecting a Theme Revision owned by another Event.
CREATE TRIGGER `events_active_theme_revision_owner_insert`
BEFORE INSERT ON `events`
WHEN NEW.`active_theme_revision_id` IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `event_theme_revisions`
    WHERE `id` = NEW.`active_theme_revision_id`
      AND `event_id` = NEW.`id`
  )
BEGIN
  SELECT RAISE(ABORT, 'active Event Theme Revision belongs to another Event');
END;
CREATE TRIGGER `events_active_theme_revision_owner_update`
BEFORE UPDATE OF `active_theme_revision_id` ON `events`
WHEN NEW.`active_theme_revision_id` IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `event_theme_revisions`
    WHERE `id` = NEW.`active_theme_revision_id`
      AND `event_id` = NEW.`id`
  )
BEGIN
  SELECT RAISE(ABORT, 'active Event Theme Revision belongs to another Event');
END;

-- Create "event_slugs" table
CREATE TABLE `event_slugs` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `slug` text NOT NULL,
  `exposed` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `event_slugs_events_slugs` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "event_slugs_slug_key" to table: "event_slugs"
CREATE UNIQUE INDEX `event_slugs_slug_key` ON `event_slugs` (`slug`);
-- Reserve current Event Slugs in the shared namespace.
INSERT INTO `event_slugs` (`slug`, `exposed`, `created_at`, `event_id`)
SELECT `public_slug`, `public`, `created_at`, `id`
FROM `events`
WHERE `public_slug` IS NOT NULL AND `public_slug` <> '';

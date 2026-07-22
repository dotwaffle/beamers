-- Create "locations" table
CREATE TABLE `locations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `locations_events_locations` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "location_drafts" table
CREATE TABLE `location_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `location_id` integer NOT NULL,
  CONSTRAINT `location_drafts_locations_draft` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "location_drafts_location_id_key" to table: "location_drafts"
CREATE UNIQUE INDEX `location_drafts_location_id_key` ON `location_drafts` (`location_id`);
-- Create "location_published_versions" table
CREATE TABLE `location_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `location_published_versions_locations_published_versions` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "locationpublishedversion_location_id_published_revision" to table: "location_published_versions"
CREATE UNIQUE INDEX `locationpublishedversion_location_id_published_revision` ON `location_published_versions` (`location_id`, `published_revision`);
-- Create "rundowns" table
CREATE TABLE `rundowns` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `draft_revision` integer NOT NULL DEFAULT 0,
  `published_revision` integer NOT NULL DEFAULT 0,
  `event_id` integer NOT NULL,
  CONSTRAINT `rundowns_events_rundown` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "rundowns_event_id_key" to table: "rundowns"
CREATE UNIQUE INDEX `rundowns_event_id_key` ON `rundowns` (`event_id`);

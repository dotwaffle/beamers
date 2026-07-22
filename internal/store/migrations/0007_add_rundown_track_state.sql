-- Create "tracks" table
CREATE TABLE `tracks` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `tracks_events_tracks` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "track_drafts" table
CREATE TABLE `track_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `track_id` integer NOT NULL,
  CONSTRAINT `track_drafts_tracks_draft` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "track_drafts_track_id_key" to table: "track_drafts"
CREATE UNIQUE INDEX `track_drafts_track_id_key` ON `track_drafts` (`track_id`);
-- Create "track_published_versions" table
CREATE TABLE `track_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `track_id` integer NOT NULL,
  CONSTRAINT `track_published_versions_tracks_published_versions` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "trackpublishedversion_track_id_published_revision" to table: "track_published_versions"
CREATE UNIQUE INDEX `trackpublishedversion_track_id_published_revision` ON `track_published_versions` (`track_id`, `published_revision`);

-- Create "sessions" table
CREATE TABLE `sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `sessions_events_sessions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "session_drafts" table
CREATE TABLE `session_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `title` text NOT NULL,
  `type` text NOT NULL,
  `audience_visibility` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `planned_start` datetime NOT NULL,
  `planned_end` datetime NOT NULL,
  `timing_policy` text NOT NULL,
  `minimum_duration_seconds` integer NOT NULL,
  `start_boundary` text NOT NULL,
  `end_boundary` text NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `session_drafts_sessions_draft` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "session_drafts_session_id_key" to table: "session_drafts"
CREATE UNIQUE INDEX `session_drafts_session_id_key` ON `session_drafts` (`session_id`);
-- Create "session_published_versions" table
CREATE TABLE `session_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `title` text NOT NULL,
  `type` text NOT NULL,
  `audience_visibility` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `planned_start` datetime NOT NULL,
  `planned_end` datetime NOT NULL,
  `timing_policy` text NOT NULL,
  `minimum_duration_seconds` integer NOT NULL,
  `start_boundary` text NOT NULL,
  `end_boundary` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `session_published_versions_sessions_published_versions` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionpublishedversion_session_id_published_revision" to table: "session_published_versions"
CREATE UNIQUE INDEX `sessionpublishedversion_session_id_published_revision` ON `session_published_versions` (`session_id`, `published_revision`);
-- Create "session_draft_lanes" table
CREATE TABLE `session_draft_lanes` (
  `session_draft_id` integer NOT NULL,
  `lane_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `lane_id`),
  CONSTRAINT `session_draft_lanes_lane_id` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_lanes_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_draft_locations" table
CREATE TABLE `session_draft_locations` (
  `session_draft_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `location_id`),
  CONSTRAINT `session_draft_locations_location_id` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_locations_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_draft_tracks" table
CREATE TABLE `session_draft_tracks` (
  `session_draft_id` integer NOT NULL,
  `track_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `track_id`),
  CONSTRAINT `session_draft_tracks_track_id` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_tracks_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_lanes" table
CREATE TABLE `session_published_version_lanes` (
  `session_published_version_id` integer NOT NULL,
  `lane_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `lane_id`),
  CONSTRAINT `session_published_version_lanes_lane_id` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_lanes_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_locations" table
CREATE TABLE `session_published_version_locations` (
  `session_published_version_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `location_id`),
  CONSTRAINT `session_published_version_locations_location_id` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_locations_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_tracks" table
CREATE TABLE `session_published_version_tracks` (
  `session_published_version_id` integer NOT NULL,
  `track_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `track_id`),
  CONSTRAINT `session_published_version_tracks_track_id` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_tracks_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Enforce immutable Event ownership across Session memberships.
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

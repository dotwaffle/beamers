-- Create "lanes" table
CREATE TABLE `lanes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `lanes_events_lanes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "lane_drafts" table
CREATE TABLE `lane_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `lane_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `lane_drafts_locations_lane_drafts` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `lane_drafts_lanes_draft` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "lane_drafts_lane_id_key" to table: "lane_drafts"
CREATE UNIQUE INDEX `lane_drafts_lane_id_key` ON `lane_drafts` (`lane_id`);
-- Create index "lanedraft_location_id" to table: "lane_drafts"
CREATE UNIQUE INDEX `lanedraft_location_id` ON `lane_drafts` (`location_id`) WHERE NOT retired;
-- Create "lane_published_versions" table
CREATE TABLE `lane_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `lane_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `lane_published_versions_locations_lane_published_versions` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `lane_published_versions_lanes_published_versions` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "lanepublishedversion_lane_id_published_revision" to table: "lane_published_versions"
CREATE UNIQUE INDEX `lanepublishedversion_lane_id_published_revision` ON `lane_published_versions` (`lane_id`, `published_revision`);
-- Enforce immutable Event ownership across Lane placements.
CREATE TRIGGER `lane_drafts_same_event_insert`
BEFORE INSERT ON `lane_drafts`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;
CREATE TRIGGER `lane_drafts_same_event_update`
BEFORE UPDATE OF `lane_id`, `location_id` ON `lane_drafts`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;
CREATE TRIGGER `lane_published_versions_same_event_insert`
BEFORE INSERT ON `lane_published_versions`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;

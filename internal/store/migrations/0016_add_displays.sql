-- Create "displays" table
CREATE TABLE `displays` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `created_at` datetime NOT NULL,
  `enrolled_at` datetime NOT NULL
);
-- Create "display_assignments" table
CREATE TABLE `display_assignments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `view_key` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `display_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `display_assignments_locations_display_assignments` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_assignments_events_display_assignments` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_assignments_displays_assignments` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayassignment_display_id_event_id" to table: "display_assignments"
CREATE UNIQUE INDEX `displayassignment_display_id_event_id` ON `display_assignments` (`display_id`, `event_id`);
-- Create index "displayassignment_event_id_location_id" to table: "display_assignments"
CREATE INDEX `displayassignment_event_id_location_id` ON `display_assignments` (`event_id`, `location_id`);
-- Create "display_credentials" table
CREATE TABLE `display_credentials` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  `display_id` integer NOT NULL,
  CONSTRAINT `display_credentials_displays_credentials` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "display_credentials_token_hash_key" to table: "display_credentials"
CREATE UNIQUE INDEX `display_credentials_token_hash_key` ON `display_credentials` (`token_hash`);
-- Create "display_enrollments" table
CREATE TABLE `display_enrollments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `code_hash` text NOT NULL,
  `credential_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `used_at` datetime NULL
);
-- Create index "display_enrollments_code_hash_key" to table: "display_enrollments"
CREATE UNIQUE INDEX `display_enrollments_code_hash_key` ON `display_enrollments` (`code_hash`);
-- Create index "display_enrollments_credential_hash_key" to table: "display_enrollments"
CREATE UNIQUE INDEX `display_enrollments_credential_hash_key` ON `display_enrollments` (`credential_hash`);

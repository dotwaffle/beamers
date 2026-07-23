-- Create "upload_links" table
CREATE TABLE `upload_links` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `token_hash` text NOT NULL,
  `revoked_at` datetime NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `upload_links_events_upload_links` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "upload_links_token_hash_key" to table: "upload_links"
CREATE UNIQUE INDEX `upload_links_token_hash_key` ON `upload_links` (`token_hash`);
-- Create index "uploadlink_event_id_target_type_target_id_created_at" to table: "upload_links"
CREATE INDEX `uploadlink_event_id_target_type_target_id_created_at` ON `upload_links` (`event_id`, `target_type`, `target_id`, `created_at`);

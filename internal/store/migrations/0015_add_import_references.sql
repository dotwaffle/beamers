-- Create "import_references" table
CREATE TABLE `import_references` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `source_format` text NOT NULL,
  `record_type` text NOT NULL,
  `external_key` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `import_references_events_import_references` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "importreference_target_type_target_id" to table: "import_references"
CREATE INDEX `importreference_target_type_target_id` ON `import_references` (`target_type`, `target_id`);
-- Create index "importreference_event_id_source_format_record_type_external_key" to table: "import_references"
CREATE UNIQUE INDEX `importreference_event_id_source_format_record_type_external_key` ON `import_references` (`event_id`, `source_format`, `record_type`, `external_key`);

-- Create "public_schedule_baselines" table
CREATE TABLE `public_schedule_baselines` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `source_published_revision` integer NOT NULL,
  `captured_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `public_schedule_baselines_events_public_schedule_baseline` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "public_schedule_baselines_event_id_key" to table: "public_schedule_baselines"
CREATE UNIQUE INDEX `public_schedule_baselines_event_id_key` ON `public_schedule_baselines` (`event_id`);
-- Create "public_schedule_baseline_entries" table
CREATE TABLE `public_schedule_baseline_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `forecast_start` datetime NOT NULL,
  `source_published_revision` integer NOT NULL,
  `recorded_at` datetime NOT NULL,
  `baseline_id` integer NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `public_schedule_baseline_entries_sessions_public_schedule_baseline_entry` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `public_schedule_baseline_entries_public_schedule_baselines_entries` FOREIGN KEY (`baseline_id`) REFERENCES `public_schedule_baselines` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "public_schedule_baseline_entries_session_id_key" to table: "public_schedule_baseline_entries"
CREATE UNIQUE INDEX `public_schedule_baseline_entries_session_id_key` ON `public_schedule_baseline_entries` (`session_id`);

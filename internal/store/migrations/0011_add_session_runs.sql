-- Add column "lifecycle" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `lifecycle` text NOT NULL DEFAULT 'Scheduled';
-- Add column "live_state_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `live_state_revision` integer NOT NULL DEFAULT 0;
-- Create "session_runs" table
CREATE TABLE `session_runs` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actual_start` datetime NOT NULL,
  `actual_end` datetime NULL,
  `snapshot_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `session_runs_sessions_runs` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionrun_session_id_actual_end" to table: "session_runs"
CREATE INDEX `sessionrun_session_id_actual_end` ON `session_runs` (`session_id`, `actual_end`);

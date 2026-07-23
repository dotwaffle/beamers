-- Add column "outcome" to table: "session_runs"
ALTER TABLE `session_runs` ADD COLUMN `outcome` text NULL;
-- Create "session_cancellations" table
CREATE TABLE `session_cancellations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `session_run_id` integer NULL,
  `public_message` text NULL,
  `crew_notes` text NULL,
  `forecast_start` datetime NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `session_cancellations_sessions_cancellations` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessioncancellation_session_id_created_at" to table: "session_cancellations"
CREATE INDEX `sessioncancellation_session_id_created_at` ON `session_cancellations` (`session_id`, `created_at`);

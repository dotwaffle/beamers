-- Create "prizegivings" table
CREATE TABLE `prizegivings` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `ceremony_session_id` integer NOT NULL,
  CONSTRAINT `prizegivings_sessions_prizegiving` FOREIGN KEY (`ceremony_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegivings_events_prizegivings` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "prizegivings_ceremony_session_id_key" to table: "prizegivings"
CREATE UNIQUE INDEX `prizegivings_ceremony_session_id_key` ON `prizegivings` (`ceremony_session_id`);
-- Create index "prizegiving_event_id_ceremony_session_id" to table: "prizegivings"
CREATE UNIQUE INDEX `prizegiving_event_id_ceremony_session_id` ON `prizegivings` (`event_id`, `ceremony_session_id`);

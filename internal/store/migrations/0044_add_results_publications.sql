-- Create "results_publications" table
CREATE TABLE `results_publications` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `scope` text NOT NULL,
  `scope_session_id` integer NOT NULL,
  `revision` integer NOT NULL,
  `release_policy` text NOT NULL,
  `status` text NOT NULL,
  `items` json NOT NULL,
  `prizegiving_lock` json NULL,
  `created_by_account_id` integer NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `results_publications_events_results_publications` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "resultspublication_event_id_scope_scope_session_id_revision" to table: "results_publications"
CREATE UNIQUE INDEX `resultspublication_event_id_scope_scope_session_id_revision` ON `results_publications` (`event_id`, `scope`, `scope_session_id`, `revision`);
-- Create index "resultspublication_event_id_scope_scope_session_id" to table: "results_publications"
CREATE INDEX `resultspublication_event_id_scope_scope_session_id` ON `results_publications` (`event_id`, `scope`, `scope_session_id`);

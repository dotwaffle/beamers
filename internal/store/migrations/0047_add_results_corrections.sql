-- Add column "results_correction_revision" to table: "results_publications"
ALTER TABLE `results_publications` ADD COLUMN `results_correction_revision` integer NULL;
-- Create "results_corrections" table
CREATE TABLE `results_corrections` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `scope` text NOT NULL,
  `scope_session_id` integer NOT NULL,
  `revision` integer NOT NULL,
  `base_publication_revision` integer NOT NULL,
  `status` text NOT NULL,
  `publication_order` json NOT NULL,
  `items_json` text NOT NULL,
  `results_text_template` json NOT NULL,
  `crew_reason` text NOT NULL,
  `public_note` text NULL,
  `published_results_revision` integer NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `results_corrections_events_results_corrections` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "resultscorrection_event_id_scope_scope_session_id_revision" to table: "results_corrections"
CREATE UNIQUE INDEX `resultscorrection_event_id_scope_scope_session_id_revision` ON `results_corrections` (`event_id`, `scope`, `scope_session_id`, `revision`);
-- Create index "resultscorrection_event_id_scope_scope_session_id" to table: "results_corrections"
CREATE INDEX `resultscorrection_event_id_scope_scope_session_id` ON `results_corrections` (`event_id`, `scope`, `scope_session_id`);

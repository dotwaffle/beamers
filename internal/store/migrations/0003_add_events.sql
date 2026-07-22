-- Create "audit_entries" table
CREATE TABLE `audit_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `result` text NOT NULL,
  `actor_account_id` integer NOT NULL,
  CONSTRAINT `audit_entries_accounts_audit_entries` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "events" table
CREATE TABLE `events` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `planned_start_date` text NOT NULL,
  `planned_end_date` text NOT NULL,
  `timezone` text NOT NULL,
  `event_locale` text NOT NULL,
  `content_language` text NULL,
  `event_day_boundary` text NOT NULL,
  `created_at` datetime NOT NULL
);
-- Create "event_grants" table
CREATE TABLE `event_grants` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `role` text NOT NULL,
  `created_at` datetime NOT NULL,
  `account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `event_grants_events_grants` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `event_grants_accounts_event_grants` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "eventgrant_event_id_account_id" to table: "event_grants"
CREATE UNIQUE INDEX `eventgrant_event_id_account_id` ON `event_grants` (`event_id`, `account_id`);

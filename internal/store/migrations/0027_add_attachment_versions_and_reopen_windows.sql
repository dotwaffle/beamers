-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_audit_entries" table
CREATE TABLE `new_audit_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_kind` text NOT NULL DEFAULT 'Account',
  `actor_upload_link_id` integer NULL,
  `created_at` datetime NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `result` text NOT NULL,
  `reason` text NULL,
  `note` text NULL,
  `actor_account_id` integer NULL,
  CONSTRAINT `audit_entries_accounts_audit_entries` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "audit_entries" to new temporary table "new_audit_entries"
INSERT INTO `new_audit_entries` (`id`, `created_at`, `action`, `target_type`, `target_id`, `result`, `reason`, `note`, `actor_account_id`) SELECT `id`, `created_at`, `action`, `target_type`, `target_id`, `result`, `reason`, `note`, `actor_account_id` FROM `audit_entries`;
-- Drop "audit_entries" table after copying rows
DROP TABLE `audit_entries`;
-- Rename temporary table "new_audit_entries" to "audit_entries"
ALTER TABLE `new_audit_entries` RENAME TO `audit_entries`;
-- Create "attachments" table
CREATE TABLE `attachments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `owner_type` text NOT NULL,
  `owner_id` integer NOT NULL,
  `name` text NOT NULL,
  `created_at` datetime NOT NULL
);
-- Create index "attachment_event_id_owner_type_owner_id_name" to table: "attachments"
CREATE UNIQUE INDEX `attachment_event_id_owner_type_owner_id_name` ON `attachments` (`event_id`, `owner_type`, `owner_id`, `name`);
-- Create "attachment_versions" table
CREATE TABLE `attachment_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `version` integer NOT NULL,
  `original_filename` text NOT NULL,
  `media_type` text NULL,
  `size_bytes` integer NOT NULL,
  `sha256` text NOT NULL,
  `storage_key` text NOT NULL,
  `uploader_type` text NOT NULL,
  `uploader_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `attachment_id` integer NOT NULL,
  CONSTRAINT `attachment_versions_attachments_versions` FOREIGN KEY (`attachment_id`) REFERENCES `attachments` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "attachmentversion_attachment_id_version" to table: "attachment_versions"
CREATE UNIQUE INDEX `attachmentversion_attachment_id_version` ON `attachment_versions` (`attachment_id`, `version`);
-- Create "reopen_windows" table
CREATE TABLE `reopen_windows` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `reason` text NOT NULL,
  `expires_at` datetime NOT NULL,
  `closed_at` datetime NULL,
  `created_by_account_id` integer NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);
-- Create index "reopenwindow_event_id_target_type_target_id_created_at" to table: "reopen_windows"
CREATE INDEX `reopenwindow_event_id_target_type_target_id_created_at` ON `reopen_windows` (`event_id`, `target_type`, `target_id`, `created_at`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

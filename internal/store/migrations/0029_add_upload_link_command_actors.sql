-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_command_receipts" table
CREATE TABLE `new_command_receipts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_kind` text NOT NULL DEFAULT 'Account',
  `actor_upload_link_id` integer NULL,
  `command_id` text NOT NULL,
  `payload_hash` text NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `outcome_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `actor_account_id` integer NULL,
  CONSTRAINT `command_receipts_accounts_command_receipts` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Copy rows from old table "command_receipts" to new temporary table "new_command_receipts"
INSERT INTO `new_command_receipts` (`id`, `command_id`, `payload_hash`, `action`, `target_type`, `target_id`, `outcome_json`, `created_at`, `actor_account_id`) SELECT `id`, `command_id`, `payload_hash`, `action`, `target_type`, `target_id`, `outcome_json`, `created_at`, `actor_account_id` FROM `command_receipts`;
-- Drop "command_receipts" table after copying rows
DROP TABLE `command_receipts`;
-- Rename temporary table "new_command_receipts" to "command_receipts"
ALTER TABLE `new_command_receipts` RENAME TO `command_receipts`;
-- Create index "command_receipts_command_id_key" to table: "command_receipts"
CREATE UNIQUE INDEX `command_receipts_command_id_key` ON `command_receipts` (`command_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

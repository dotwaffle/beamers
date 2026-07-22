-- Add column "revision" to table: "events"
ALTER TABLE `events` ADD COLUMN `revision` integer NOT NULL DEFAULT 1;
-- Create "command_receipts" table
CREATE TABLE `command_receipts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `command_id` text NOT NULL,
  `payload_hash` text NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `outcome_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `actor_account_id` integer NOT NULL,
  CONSTRAINT `command_receipts_accounts_command_receipts` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "command_receipts_command_id_key" to table: "command_receipts"
CREATE UNIQUE INDEX `command_receipts_command_id_key` ON `command_receipts` (`command_id`);

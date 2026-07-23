-- Add column "entry_order_policy" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `entry_order_policy` text NOT NULL DEFAULT 'DeterministicShuffle';
-- Add column "entry_order_seed" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `entry_order_seed` integer NOT NULL DEFAULT 0;
UPDATE `sessions` SET `entry_order_seed` = `id`;
-- Add column "entry_order_manual_ids" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `entry_order_manual_ids` json NULL;
-- Add column "locked_entry_order_ids" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `locked_entry_order_ids` json NULL;
-- Add column "entry_order_locked_at" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `entry_order_locked_at` datetime NULL;
-- Add column "entry_order_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `entry_order_revision` integer NOT NULL DEFAULT 0;
-- Add column "first_presented_at" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `first_presented_at` datetime NULL;
-- Add column "locked_entry_order_ids" to table: "session_runs"
ALTER TABLE `session_runs` ADD COLUMN `locked_entry_order_ids` json NULL;

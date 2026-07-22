-- Add column "reason" to table: "audit_entries"
ALTER TABLE `audit_entries` ADD COLUMN `reason` text NULL;
-- Add column "note" to table: "audit_entries"
ALTER TABLE `audit_entries` ADD COLUMN `note` text NULL;

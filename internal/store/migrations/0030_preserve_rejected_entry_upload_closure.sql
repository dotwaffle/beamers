-- Add column "upload_closed_at" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `upload_closed_at` datetime NULL;

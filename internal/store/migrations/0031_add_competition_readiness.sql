-- Add column "require_entry_review" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `require_entry_review` bool NOT NULL DEFAULT false;
-- Add column "file_delivery_required" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `file_delivery_required` bool NULL;
-- Add column "readiness_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `readiness_revision` integer NOT NULL DEFAULT 0;
-- Add column "content_revision" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `content_revision` integer NOT NULL DEFAULT 1;
-- Add column "reviewed_content_revision" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `reviewed_content_revision` integer NULL;
-- Add column "reviewed_by_account_id" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `reviewed_by_account_id` integer NULL;
-- Add column "reviewed_at" to table: "competition_entries"
ALTER TABLE `competition_entries` ADD COLUMN `reviewed_at` datetime NULL;
-- Add column "final" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `final` bool NOT NULL DEFAULT false;
-- Add column "primary" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `primary` bool NOT NULL DEFAULT false;
-- Add column "readiness_revision" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `readiness_revision` integer NOT NULL DEFAULT 1;

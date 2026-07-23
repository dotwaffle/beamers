-- Add column "upload_deadline" to table: "session_drafts"
ALTER TABLE `session_drafts` ADD COLUMN `upload_deadline` datetime NULL;
-- Add column "upload_deadline" to table: "session_published_versions"
ALTER TABLE `session_published_versions` ADD COLUMN `upload_deadline` datetime NULL;

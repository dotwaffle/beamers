-- Add column "attachment_release_policy" to table: "events"
ALTER TABLE `events` ADD COLUMN `attachment_release_policy` text NOT NULL DEFAULT 'OnEnded';
-- Add column "attachment_release_cue_session_id" to table: "events"
ALTER TABLE `events` ADD COLUMN `attachment_release_cue_session_id` integer NULL;
-- Add column "attachment_release_cue_at" to table: "events"
ALTER TABLE `events` ADD COLUMN `attachment_release_cue_at` datetime NULL;
-- Add column "attachment_release_revision" to table: "events"
ALTER TABLE `events` ADD COLUMN `attachment_release_revision` integer NOT NULL DEFAULT 0;
-- Add column "attachment_release_policy_override" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `attachment_release_policy_override` text NULL;
-- Add column "attachment_release_revision" to table: "sessions"
ALTER TABLE `sessions` ADD COLUMN `attachment_release_revision` integer NOT NULL DEFAULT 0;
-- Add column "release_eligibility" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `release_eligibility` text NOT NULL DEFAULT 'Public';
-- Add column "release_hold" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `release_hold` bool NOT NULL DEFAULT false;
-- Add column "release_revision" to table: "attachment_versions"
ALTER TABLE `attachment_versions` ADD COLUMN `release_revision` integer NOT NULL DEFAULT 0;

-- Add current applied-state acknowledgment to enrolled Displays.
ALTER TABLE `displays` ADD COLUMN `applied_protocol_version` text NOT NULL DEFAULT '';
ALTER TABLE `displays` ADD COLUMN `applied_stream_id` text NOT NULL DEFAULT '';
ALTER TABLE `displays` ADD COLUMN `applied_stream_position` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_active_event_id` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_activation_generation` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_published_revision` integer NOT NULL DEFAULT 0;
ALTER TABLE `displays` ADD COLUMN `applied_at` datetime NULL;

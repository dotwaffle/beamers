-- Add column "public_slug" to table: "events"
ALTER TABLE `events` ADD COLUMN `public_slug` text NULL;
-- Add column "public" to table: "events"
ALTER TABLE `events` ADD COLUMN `public` bool NOT NULL DEFAULT false;
-- Create index "events_public_slug_key" to table: "events"
CREATE UNIQUE INDEX `events_public_slug_key` ON `events` (`public_slug`);

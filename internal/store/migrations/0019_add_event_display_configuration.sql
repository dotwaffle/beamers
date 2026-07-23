-- Add column "display_configuration" to table: "events"
ALTER TABLE `events` ADD COLUMN `display_configuration` text NOT NULL DEFAULT '{"rotation_seconds":15,"theme":{"branding":"","foreground_color":"#ffffff","background_color":"#101828","accent_color":"#1d4ed8","background":"solid","scrim_color":"#000000","scrim_opacity":85,"font":"sans","transition":"fade"}}';

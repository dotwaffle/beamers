-- Add column "release_policy" to table: "prizegivings"
ALTER TABLE `prizegivings` ADD COLUMN `release_policy` text NOT NULL DEFAULT 'ProgressiveOnReveal';

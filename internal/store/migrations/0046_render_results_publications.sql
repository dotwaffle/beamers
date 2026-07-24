-- Add column "results_text_template" to table: "results_publications"
ALTER TABLE `results_publications` ADD COLUMN `results_text_template` json NULL;
-- Add column "rendered_html" to table: "results_publications"
ALTER TABLE `results_publications` ADD COLUMN `rendered_html` text NULL;
-- Add column "rendered_text" to table: "results_publications"
ALTER TABLE `results_publications` ADD COLUMN `rendered_text` text NULL;
-- Add column "rendered_json" to table: "results_publications"
ALTER TABLE `results_publications` ADD COLUMN `rendered_json` text NULL;

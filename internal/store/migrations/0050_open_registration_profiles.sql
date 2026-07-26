-- Create "account_profiles" table
CREATE TABLE `account_profiles` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `normalized_handle` text NOT NULL,
  `display_name` text NOT NULL,
  `published` bool NOT NULL DEFAULT false,
  `selected_entries` json NULL,
  `account_id` integer NOT NULL,
  CONSTRAINT `account_profiles_accounts_profile` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "account_profiles_normalized_handle_key" to table: "account_profiles"
CREATE UNIQUE INDEX `account_profiles_normalized_handle_key` ON `account_profiles` (`normalized_handle`);
-- Create index "account_profiles_account_id_key" to table: "account_profiles"
CREATE UNIQUE INDEX `account_profiles_account_id_key` ON `account_profiles` (`account_id`);
-- Create "released_profile_entries" table
CREATE TABLE `released_profile_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `entry_id` integer NOT NULL,
  `name` text NOT NULL
);
-- Create index "released_profile_entries_entry_id_key" to table: "released_profile_entries"
CREATE UNIQUE INDEX `released_profile_entries_entry_id_key` ON `released_profile_entries` (`entry_id`);
INSERT OR REPLACE INTO `released_profile_entries` (`entry_id`, `name`)
SELECT
  json_extract(`entry`.`value`, '$.entry_id'),
  json_extract(`entry`.`value`, '$.name')
FROM `results_publications` AS `publication`,
  json_each(`publication`.`rendered_json`, '$.items') AS `item`,
  json_each(json_extract(`item`.`value`, '$.competition.placed')) AS `entry`
UNION
SELECT
  json_extract(`entry`.`value`, '$.entry_id'),
  json_extract(`entry`.`value`, '$.name')
FROM `results_publications` AS `publication`,
  json_each(`publication`.`rendered_json`, '$.items') AS `item`,
  json_each(json_extract(`item`.`value`, '$.competition.unplaced')) AS `entry`
UNION
SELECT
  json_extract(`entry`.`value`, '$.entry_id'),
  json_extract(`entry`.`value`, '$.name')
FROM `results_publications` AS `publication`,
  json_each(`publication`.`rendered_json`, '$.items') AS `item`,
  json_each(json_extract(`item`.`value`, '$.competition.disqualified')) AS `entry`;
-- Create "registration_policies" table
CREATE TABLE `registration_policies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `registration_open` bool NOT NULL DEFAULT true
);
INSERT INTO `registration_policies` (`registration_open`) VALUES (true);

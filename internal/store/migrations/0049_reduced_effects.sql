-- Create "account_preferences" table
CREATE TABLE `account_preferences` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `reduced_effects` bool NOT NULL DEFAULT false,
  `account_id` integer NOT NULL,
  CONSTRAINT `account_preferences_accounts_preference` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "account_preferences_account_id_key" to table: "account_preferences"
CREATE UNIQUE INDEX `account_preferences_account_id_key` ON `account_preferences` (`account_id`);

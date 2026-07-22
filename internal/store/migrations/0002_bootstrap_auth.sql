-- Create "accounts" table
CREATE TABLE `accounts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `normalized_name` text NOT NULL,
  `administrator` bool NOT NULL,
  `created_at` datetime NOT NULL,
  `disabled_at` datetime NULL
);
-- Create index "accounts_normalized_name_key" to table: "accounts"
CREATE UNIQUE INDEX `accounts_normalized_name_key` ON `accounts` (`normalized_name`);
-- Create "password_credentials" table
CREATE TABLE `password_credentials` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `account_id` integer NOT NULL,
  `password_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  CONSTRAINT `password_credentials_accounts_password_credential` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "password_credentials_account_id_key" to table: "password_credentials"
CREATE UNIQUE INDEX `password_credentials_account_id_key` ON `password_credentials` (`account_id`);
-- Create "account_sessions" table
CREATE TABLE `account_sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `account_id` integer NOT NULL,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  CONSTRAINT `account_sessions_accounts_sessions` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "account_sessions_token_hash_key" to table: "account_sessions"
CREATE UNIQUE INDEX `account_sessions_token_hash_key` ON `account_sessions` (`token_hash`);
-- Create "bootstrap_credentials" table
CREATE TABLE `bootstrap_credentials` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `used_at` datetime NULL
);
-- Create index "bootstrap_credentials_token_hash_key" to table: "bootstrap_credentials"
CREATE UNIQUE INDEX `bootstrap_credentials_token_hash_key` ON `bootstrap_credentials` (`token_hash`);

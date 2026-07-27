-- Create "recovery_codes" table
CREATE TABLE `recovery_codes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `code_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `used_at` datetime NULL,
  `account_id` integer NOT NULL,
  CONSTRAINT `recovery_codes_accounts_recovery_codes` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "recovery_codes_code_hash_key" to table: "recovery_codes"
CREATE UNIQUE INDEX `recovery_codes_code_hash_key` ON `recovery_codes` (`code_hash`);
-- Create "recovery_tokens" table
CREATE TABLE `recovery_tokens` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `used_at` datetime NULL,
  `account_id` integer NOT NULL,
  CONSTRAINT `recovery_tokens_accounts_recovery_tokens` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "recovery_tokens_token_hash_key" to table: "recovery_tokens"
CREATE UNIQUE INDEX `recovery_tokens_token_hash_key` ON `recovery_tokens` (`token_hash`);

-- Create "federated_identities" table
CREATE TABLE `federated_identities` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `provider` text NOT NULL,
  `subject` text NOT NULL,
  `created_at` datetime NOT NULL,
  `last_used_at` datetime NULL,
  `revoked_at` datetime NULL,
  `account_id` integer NOT NULL,
  CONSTRAINT `federated_identities_accounts_federated_identities` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "federatedidentity_provider_subject" to table: "federated_identities"
CREATE UNIQUE INDEX `federatedidentity_provider_subject` ON `federated_identities` (`provider`, `subject`);

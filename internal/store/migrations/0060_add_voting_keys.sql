-- Create protected Event Voting Key persistence.
CREATE TABLE `voting_keys` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  `redeemed_at` datetime NULL,
  `redeemed_by_account_id` integer NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `voting_keys_events_voting_keys` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `voting_keys_accounts_redeemed_voting_keys` FOREIGN KEY (`redeemed_by_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "voting_keys_token_hash_key" to table: "voting_keys"
CREATE UNIQUE INDEX `voting_keys_token_hash_key` ON `voting_keys` (`token_hash`);
-- Create index "votingkey_event_id_created_at" to table: "voting_keys"
CREATE INDEX `votingkey_event_id_created_at` ON `voting_keys` (`event_id`, `created_at`);

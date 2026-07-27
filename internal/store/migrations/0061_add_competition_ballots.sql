-- Add Event voting defaults and Competition window state.
ALTER TABLE `events` ADD COLUMN `voting_method` text NOT NULL DEFAULT 'Range1To5';
ALTER TABLE `events` ADD COLUMN `self_vote_policy` text NOT NULL DEFAULT 'Allowed';
ALTER TABLE `sessions` ADD COLUMN `voting_method_override` text NULL;
ALTER TABLE `sessions` ADD COLUMN `self_vote_policy_override` text NULL;
ALTER TABLE `sessions` ADD COLUMN `voting_window_state` text NOT NULL DEFAULT 'Unopened';
ALTER TABLE `sessions` ADD COLUMN `frozen_voting_method` text NULL;
ALTER TABLE `sessions` ADD COLUMN `frozen_self_vote_policy` text NULL;
ALTER TABLE `sessions` ADD COLUMN `voting_revision` integer NOT NULL DEFAULT 0;
ALTER TABLE `sessions` ADD COLUMN `voting_opened_at` datetime NULL;
ALTER TABLE `sessions` ADD COLUMN `voting_closed_at` datetime NULL;

-- Create private Account Ballots.
CREATE TABLE `votes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `value` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  `account_id` integer NOT NULL,
  `entry_id` integer NOT NULL,
  CONSTRAINT `votes_events_votes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_sessions_votes` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_accounts_votes` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_competition_entries_votes` FOREIGN KEY (`entry_id`) REFERENCES `competition_entries` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
CREATE UNIQUE INDEX `vote_competition_session_id_account_id_entry_id` ON `votes` (`competition_session_id`, `account_id`, `entry_id`);
CREATE INDEX `vote_competition_session_id_account_id` ON `votes` (`competition_session_id`, `account_id`);

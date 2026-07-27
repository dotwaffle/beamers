-- Create "favorite_sessions" table
CREATE TABLE `favorite_sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `account_id` integer NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `favorite_sessions_accounts_favorite_sessions` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `favorite_sessions_sessions_favorites` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "favoritesession_account_id_session_id" to table: "favorite_sessions"
CREATE UNIQUE INDEX `favoritesession_account_id_session_id` ON `favorite_sessions` (`account_id`, `session_id`);

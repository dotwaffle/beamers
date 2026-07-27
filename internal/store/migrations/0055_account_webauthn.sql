-- Add column "webauthn_user_handle" to table: "accounts"
ALTER TABLE `accounts` ADD COLUMN `webauthn_user_handle` blob NULL;
-- Backfill stable opaque handles for Accounts created before WebAuthn support.
UPDATE `accounts` SET `webauthn_user_handle` = randomblob(64) WHERE `webauthn_user_handle` IS NULL;
-- Create index "accounts_webauthn_user_handle_key" to table: "accounts"
CREATE UNIQUE INDEX `accounts_webauthn_user_handle_key` ON `accounts` (`webauthn_user_handle`);
-- Create "web_authn_credentials" table
CREATE TABLE `web_authn_credentials` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `credential_id` blob NOT NULL,
  `name` text NOT NULL,
  `credential` blob NOT NULL,
  `attachment` text NULL,
  `created_at` datetime NOT NULL,
  `last_used_at` datetime NULL,
  `revoked_at` datetime NULL,
  `account_id` integer NOT NULL,
  CONSTRAINT `web_authn_credentials_accounts_webauthn_credentials` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "webauthncredential_credential_id" to table: "web_authn_credentials"
CREATE UNIQUE INDEX `webauthncredential_credential_id` ON `web_authn_credentials` (`credential_id`);

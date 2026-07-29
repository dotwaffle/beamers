-- Baseline the complete pre-v1 schema.
-- Create "beamers_schema_migrations" table
CREATE TABLE `beamers_schema_migrations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `version` integer NOT NULL,
  `name` text NOT NULL,
  `checksum` text NOT NULL,
  `applied_at` datetime NOT NULL,
  `safety` text NOT NULL DEFAULT 'Baseline',
  `minimum_reader_schema_version` integer NOT NULL DEFAULT 1,
  `minimum_writer_schema_version` integer NOT NULL DEFAULT 1,
  CONSTRAINT `schema_migrations_checksum_length` CHECK (length(checksum) = 64)
);
-- Create index "beamers_schema_migrations_version_key" to table: "beamers_schema_migrations"
CREATE UNIQUE INDEX `beamers_schema_migrations_version_key` ON `beamers_schema_migrations` (`version`);
-- Create index "beamers_schema_migrations_name_key" to table: "beamers_schema_migrations"
CREATE UNIQUE INDEX `beamers_schema_migrations_name_key` ON `beamers_schema_migrations` (`name`);
-- Create "accounts" table
CREATE TABLE `accounts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `normalized_name` text NOT NULL,
  `administrator` bool NOT NULL,
  `created_at` datetime NOT NULL,
  `disabled_at` datetime NULL,
  `webauthn_user_handle` blob NULL
);
-- Create index "accounts_normalized_name_key" to table: "accounts"
CREATE UNIQUE INDEX `accounts_normalized_name_key` ON `accounts` (`normalized_name`);
-- Create index "accounts_webauthn_user_handle_key" to table: "accounts"
CREATE UNIQUE INDEX `accounts_webauthn_user_handle_key` ON `accounts` (`webauthn_user_handle`);
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
-- Create "events" table
CREATE TABLE `events` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `planned_start_date` text NOT NULL,
  `planned_end_date` text NOT NULL,
  `timezone` text NOT NULL,
  `event_locale` text NOT NULL,
  `content_language` text NULL,
  `event_day_boundary` text NOT NULL,
  `created_at` datetime NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `display_configuration` text NOT NULL DEFAULT '{"rotation_seconds":15,"theme":{"branding":"","foreground_color":"#ffffff","background_color":"#101828","accent_color":"#1d4ed8","background":"solid","scrim_color":"#000000","scrim_opacity":85,"font":"sans","transition":"fade"}}',
  `target_adjustment_presets` text NOT NULL DEFAULT '[-300,300,600]',
  `entry_default_disposition` text NOT NULL DEFAULT 'Pending',
  `attachment_release_policy` text NOT NULL DEFAULT 'OnEnded',
  `attachment_release_cue_session_id` integer NULL,
  `attachment_release_cue_at` datetime NULL,
  `attachment_release_revision` integer NOT NULL DEFAULT 0,
  `stage_message_presets` text NOT NULL DEFAULT '[]',
  `stage_message_default_duration_seconds` integer NOT NULL DEFAULT 10,
  `stage_message_configuration_revision` integer NOT NULL DEFAULT 0,
  `public_slug` text NULL,
  `public` bool NOT NULL DEFAULT false,
  `submission_eligibility` text NOT NULL DEFAULT 'AllAccounts',
  `voting_method` text NOT NULL DEFAULT 'Range1To5',
  `self_vote_policy` text NOT NULL DEFAULT 'Allowed',
  `active_theme_revision_id` integer NULL,
  CONSTRAINT `0` FOREIGN KEY (`active_theme_revision_id`) REFERENCES `event_theme_revisions` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "events_public_slug_key" to table: "events"
CREATE UNIQUE INDEX `events_public_slug_key` ON `events` (`public_slug`);
-- Create "event_grants" table
CREATE TABLE `event_grants` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `role` text NOT NULL,
  `created_at` datetime NOT NULL,
  `account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `lane_ids` json NULL,
  `display_group_keys` json NULL,
  `capabilities` json NULL,
  CONSTRAINT `event_grants_accounts_event_grants` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `event_grants_events_grants` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "eventgrant_event_id_account_id" to table: "event_grants"
CREATE UNIQUE INDEX `eventgrant_event_id_account_id` ON `event_grants` (`event_id`, `account_id`);
-- Create "locations" table
CREATE TABLE `locations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `locations_events_locations` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "location_drafts" table
CREATE TABLE `location_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `location_id` integer NOT NULL,
  CONSTRAINT `location_drafts_locations_draft` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "location_drafts_location_id_key" to table: "location_drafts"
CREATE UNIQUE INDEX `location_drafts_location_id_key` ON `location_drafts` (`location_id`);
-- Create "location_published_versions" table
CREATE TABLE `location_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `location_published_versions_locations_published_versions` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "locationpublishedversion_location_id_published_revision" to table: "location_published_versions"
CREATE UNIQUE INDEX `locationpublishedversion_location_id_published_revision` ON `location_published_versions` (`location_id`, `published_revision`);
-- Create "rundowns" table
CREATE TABLE `rundowns` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `draft_revision` integer NOT NULL DEFAULT 0,
  `published_revision` integer NOT NULL DEFAULT 0,
  `event_id` integer NOT NULL,
  CONSTRAINT `rundowns_events_rundown` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "rundowns_event_id_key" to table: "rundowns"
CREATE UNIQUE INDEX `rundowns_event_id_key` ON `rundowns` (`event_id`);
-- Create "lanes" table
CREATE TABLE `lanes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `lanes_events_lanes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "lane_drafts" table
CREATE TABLE `lane_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `lane_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `lane_drafts_lanes_draft` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `lane_drafts_locations_lane_drafts` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "lane_drafts_lane_id_key" to table: "lane_drafts"
CREATE UNIQUE INDEX `lane_drafts_lane_id_key` ON `lane_drafts` (`lane_id`);
-- Create index "lanedraft_location_id" to table: "lane_drafts"
CREATE UNIQUE INDEX `lanedraft_location_id` ON `lane_drafts` (`location_id`) WHERE NOT retired;
-- Create "lane_published_versions" table
CREATE TABLE `lane_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `lane_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  CONSTRAINT `lane_published_versions_lanes_published_versions` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `lane_published_versions_locations_lane_published_versions` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "lanepublishedversion_lane_id_published_revision" to table: "lane_published_versions"
CREATE UNIQUE INDEX `lanepublishedversion_lane_id_published_revision` ON `lane_published_versions` (`lane_id`, `published_revision`);
-- Create "tracks" table
CREATE TABLE `tracks` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `tracks_events_tracks` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create "track_drafts" table
CREATE TABLE `track_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `track_id` integer NOT NULL,
  CONSTRAINT `track_drafts_tracks_draft` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "track_drafts_track_id_key" to table: "track_drafts"
CREATE UNIQUE INDEX `track_drafts_track_id_key` ON `track_drafts` (`track_id`);
-- Create "track_published_versions" table
CREATE TABLE `track_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `name` text NOT NULL,
  `retired` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `track_id` integer NOT NULL,
  CONSTRAINT `track_published_versions_tracks_published_versions` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "trackpublishedversion_track_id_published_revision" to table: "track_published_versions"
CREATE UNIQUE INDEX `trackpublishedversion_track_id_published_revision` ON `track_published_versions` (`track_id`, `published_revision`);
-- Create "session_drafts" table
CREATE TABLE `session_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `title` text NOT NULL,
  `type` text NOT NULL,
  `audience_visibility` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `planned_start` datetime NOT NULL,
  `planned_end` datetime NOT NULL,
  `timing_policy` text NOT NULL,
  `minimum_duration_seconds` integer NOT NULL,
  `start_boundary` text NOT NULL,
  `end_boundary` text NOT NULL,
  `session_id` integer NOT NULL,
  `speaker` text NULL,
  `submission_deadline` datetime NULL,
  `entry_default_disposition` text NULL,
  `upload_deadline` datetime NULL,
  CONSTRAINT `session_drafts_sessions_draft` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "session_drafts_session_id_key" to table: "session_drafts"
CREATE UNIQUE INDEX `session_drafts_session_id_key` ON `session_drafts` (`session_id`);
-- Create "session_published_versions" table
CREATE TABLE `session_published_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `published_revision` integer NOT NULL,
  `title` text NOT NULL,
  `type` text NOT NULL,
  `audience_visibility` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `planned_start` datetime NOT NULL,
  `planned_end` datetime NOT NULL,
  `timing_policy` text NOT NULL,
  `minimum_duration_seconds` integer NOT NULL,
  `start_boundary` text NOT NULL,
  `end_boundary` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  `speaker` text NULL,
  `submission_deadline` datetime NULL,
  `entry_default_disposition` text NULL,
  `upload_deadline` datetime NULL,
  CONSTRAINT `session_published_versions_sessions_published_versions` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionpublishedversion_session_id_published_revision" to table: "session_published_versions"
CREATE UNIQUE INDEX `sessionpublishedversion_session_id_published_revision` ON `session_published_versions` (`session_id`, `published_revision`);
-- Create "session_draft_lanes" table
CREATE TABLE `session_draft_lanes` (
  `session_draft_id` integer NOT NULL,
  `lane_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `lane_id`),
  CONSTRAINT `session_draft_lanes_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_lanes_lane_id` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_draft_locations" table
CREATE TABLE `session_draft_locations` (
  `session_draft_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `location_id`),
  CONSTRAINT `session_draft_locations_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_locations_location_id` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_draft_tracks" table
CREATE TABLE `session_draft_tracks` (
  `session_draft_id` integer NOT NULL,
  `track_id` integer NOT NULL,
  PRIMARY KEY (`session_draft_id`, `track_id`),
  CONSTRAINT `session_draft_tracks_session_draft_id` FOREIGN KEY (`session_draft_id`) REFERENCES `session_drafts` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_draft_tracks_track_id` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_lanes" table
CREATE TABLE `session_published_version_lanes` (
  `session_published_version_id` integer NOT NULL,
  `lane_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `lane_id`),
  CONSTRAINT `session_published_version_lanes_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_lanes_lane_id` FOREIGN KEY (`lane_id`) REFERENCES `lanes` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_locations" table
CREATE TABLE `session_published_version_locations` (
  `session_published_version_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `location_id`),
  CONSTRAINT `session_published_version_locations_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_locations_location_id` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "session_published_version_tracks" table
CREATE TABLE `session_published_version_tracks` (
  `session_published_version_id` integer NOT NULL,
  `track_id` integer NOT NULL,
  PRIMARY KEY (`session_published_version_id`, `track_id`),
  CONSTRAINT `session_published_version_tracks_session_published_version_id` FOREIGN KEY (`session_published_version_id`) REFERENCES `session_published_versions` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `session_published_version_tracks_track_id` FOREIGN KEY (`track_id`) REFERENCES `tracks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "draft_changes" table
CREATE TABLE `draft_changes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `kind` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `fact_key` text NOT NULL,
  `payload_json` text NOT NULL,
  `status` text NOT NULL DEFAULT 'Effective',
  `published_revision` integer NULL,
  `created_at` datetime NOT NULL,
  `draft_edit_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `draft_changes_draft_edits_changes` FOREIGN KEY (`draft_edit_id`) REFERENCES `draft_edits` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_changes_events_draft_changes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftchange_event_id_revision" to table: "draft_changes"
CREATE INDEX `draftchange_event_id_revision` ON `draft_changes` (`event_id`, `revision`);
-- Create index "draftchange_event_id_target_type_target_id_fact_key_status" to table: "draft_changes"
CREATE INDEX `draftchange_event_id_target_type_target_id_fact_key_status` ON `draft_changes` (`event_id`, `target_type`, `target_id`, `fact_key`, `status`);
-- Create "draft_change_dependencies" table
CREATE TABLE `draft_change_dependencies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `change_id` integer NOT NULL,
  `depends_on_id` integer NOT NULL,
  CONSTRAINT `draft_change_dependencies_draft_changes_dependencies` FOREIGN KEY (`change_id`) REFERENCES `draft_changes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_change_dependencies_draft_changes_dependents` FOREIGN KEY (`depends_on_id`) REFERENCES `draft_changes` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftchangedependency_change_id_depends_on_id" to table: "draft_change_dependencies"
CREATE UNIQUE INDEX `draftchangedependency_change_id_depends_on_id` ON `draft_change_dependencies` (`change_id`, `depends_on_id`);
-- Create "draft_edits" table
CREATE TABLE `draft_edits` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `actor_account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `draft_edits_accounts_draft_edits` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `draft_edits_events_draft_edits` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "draftedit_event_id_revision" to table: "draft_edits"
CREATE UNIQUE INDEX `draftedit_event_id_revision` ON `draft_edits` (`event_id`, `revision`);
-- Create "session_runs" table
CREATE TABLE `session_runs` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actual_start` datetime NOT NULL,
  `actual_end` datetime NULL,
  `snapshot_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  `target_adjustment_seconds` integer NOT NULL DEFAULT 0,
  `target_adjusted_at` datetime NULL,
  `outcome` text NULL,
  `locked_entry_order_ids` json NULL,
  CONSTRAINT `session_runs_sessions_runs` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionrun_session_id_actual_end" to table: "session_runs"
CREATE INDEX `sessionrun_session_id_actual_end` ON `session_runs` (`session_id`, `actual_end`);
-- Create "session_run_amendments" table
CREATE TABLE `session_run_amendments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_account_id` integer NOT NULL,
  `details_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `session_run_id` integer NOT NULL,
  CONSTRAINT `session_run_amendments_session_runs_amendments` FOREIGN KEY (`session_run_id`) REFERENCES `session_runs` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessionrunamendment_session_run_id" to table: "session_run_amendments"
CREATE INDEX `sessionrunamendment_session_run_id` ON `session_run_amendments` (`session_run_id`);
-- Create "import_references" table
CREATE TABLE `import_references` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `source_format` text NOT NULL,
  `record_type` text NOT NULL,
  `external_key` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `import_references_events_import_references` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "importreference_target_type_target_id" to table: "import_references"
CREATE INDEX `importreference_target_type_target_id` ON `import_references` (`target_type`, `target_id`);
-- Create index "importreference_event_id_source_format_record_type_external_key" to table: "import_references"
CREATE UNIQUE INDEX `importreference_event_id_source_format_record_type_external_key` ON `import_references` (`event_id`, `source_format`, `record_type`, `external_key`);
-- Create "displays" table
CREATE TABLE `displays` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `created_at` datetime NOT NULL,
  `enrolled_at` datetime NOT NULL,
  `applied_protocol_version` text NOT NULL DEFAULT '',
  `applied_stream_id` text NOT NULL DEFAULT '',
  `applied_stream_position` integer NOT NULL DEFAULT 0,
  `applied_active_event_id` integer NOT NULL DEFAULT 0,
  `applied_activation_generation` integer NOT NULL DEFAULT 0,
  `applied_published_revision` integer NOT NULL DEFAULT 0,
  `applied_at` datetime NULL,
  `applied_asset_version` text NOT NULL DEFAULT '',
  `applied_standby` bool NOT NULL DEFAULT true,
  `clock_offset_milliseconds` integer NOT NULL DEFAULT 0,
  `clock_uncertainty_milliseconds` integer NOT NULL DEFAULT 0,
  `renderer_unstable` bool NOT NULL DEFAULT false,
  `applied_stage_message_id` integer NOT NULL DEFAULT 0,
  `applied_stage_message_revision` integer NOT NULL DEFAULT 0,
  `applied_technical_difficulties_id` integer NOT NULL DEFAULT 0,
  `applied_technical_difficulties_revision` integer NOT NULL DEFAULT 0,
  `applied_urgent_notice_id` integer NOT NULL DEFAULT 0,
  `applied_urgent_notice_revision` integer NOT NULL DEFAULT 0,
  `applied_emergency_alert_id` integer NOT NULL DEFAULT 0,
  `applied_emergency_alert_revision` integer NOT NULL DEFAULT 0
);
-- Create "display_assignments" table
CREATE TABLE `display_assignments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `view_key` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `display_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `location_id` integer NOT NULL,
  `display_group_keys` json NULL,
  CONSTRAINT `display_assignments_displays_assignments` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_assignments_events_display_assignments` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_assignments_locations_display_assignments` FOREIGN KEY (`location_id`) REFERENCES `locations` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayassignment_display_id_event_id" to table: "display_assignments"
CREATE UNIQUE INDEX `displayassignment_display_id_event_id` ON `display_assignments` (`display_id`, `event_id`);
-- Create index "displayassignment_event_id_location_id" to table: "display_assignments"
CREATE INDEX `displayassignment_event_id_location_id` ON `display_assignments` (`event_id`, `location_id`);
-- Create "display_credentials" table
CREATE TABLE `display_credentials` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  `display_id` integer NOT NULL,
  CONSTRAINT `display_credentials_displays_credentials` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "display_credentials_token_hash_key" to table: "display_credentials"
CREATE UNIQUE INDEX `display_credentials_token_hash_key` ON `display_credentials` (`token_hash`);
-- Create "display_enrollments" table
CREATE TABLE `display_enrollments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `code_hash` text NOT NULL,
  `credential_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `used_at` datetime NULL
);
-- Create index "display_enrollments_code_hash_key" to table: "display_enrollments"
CREATE UNIQUE INDEX `display_enrollments_code_hash_key` ON `display_enrollments` (`code_hash`);
-- Create index "display_enrollments_credential_hash_key" to table: "display_enrollments"
CREATE UNIQUE INDEX `display_enrollments_credential_hash_key` ON `display_enrollments` (`credential_hash`);
-- Create "session_cancellations" table
CREATE TABLE `session_cancellations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `session_run_id` integer NULL,
  `public_message` text NULL,
  `crew_notes` text NULL,
  `forecast_start` datetime NOT NULL,
  `created_at` datetime NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `session_cancellations_sessions_cancellations` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessioncancellation_session_id_created_at" to table: "session_cancellations"
CREATE INDEX `sessioncancellation_session_id_created_at` ON `session_cancellations` (`session_id`, `created_at`);
-- Create "audit_entries" table
CREATE TABLE `audit_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_kind` text NOT NULL DEFAULT 'Account',
  `actor_upload_link_id` integer NULL,
  `created_at` datetime NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `result` text NOT NULL,
  `reason` text NULL,
  `note` text NULL,
  `actor_account_id` integer NULL,
  CONSTRAINT `audit_entries_accounts_audit_entries` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create "attachments" table
CREATE TABLE `attachments` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `owner_type` text NOT NULL,
  `owner_id` integer NOT NULL,
  `name` text NOT NULL,
  `created_at` datetime NOT NULL
);
-- Create index "attachment_event_id_owner_type_owner_id_name" to table: "attachments"
CREATE UNIQUE INDEX `attachment_event_id_owner_type_owner_id_name` ON `attachments` (`event_id`, `owner_type`, `owner_id`, `name`);
-- Create "attachment_versions" table
CREATE TABLE `attachment_versions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `version` integer NOT NULL,
  `original_filename` text NOT NULL,
  `media_type` text NULL,
  `size_bytes` integer NOT NULL,
  `sha256` text NOT NULL,
  `storage_key` text NOT NULL,
  `uploader_type` text NOT NULL,
  `uploader_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `attachment_id` integer NOT NULL,
  `final` bool NOT NULL DEFAULT false,
  `primary` bool NOT NULL DEFAULT false,
  `readiness_revision` integer NOT NULL DEFAULT 1,
  `release_eligibility` text NOT NULL DEFAULT 'Public',
  `release_hold` bool NOT NULL DEFAULT false,
  `release_revision` integer NOT NULL DEFAULT 0,
  CONSTRAINT `attachment_versions_attachments_versions` FOREIGN KEY (`attachment_id`) REFERENCES `attachments` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "attachmentversion_attachment_id_version" to table: "attachment_versions"
CREATE UNIQUE INDEX `attachmentversion_attachment_id_version` ON `attachment_versions` (`attachment_id`, `version`);
-- Create "reopen_windows" table
CREATE TABLE `reopen_windows` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `target_type` text NOT NULL,
  `target_id` integer NOT NULL,
  `reason` text NOT NULL,
  `expires_at` datetime NOT NULL,
  `closed_at` datetime NULL,
  `created_by_account_id` integer NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);
-- Create index "reopenwindow_event_id_target_type_target_id_created_at" to table: "reopen_windows"
CREATE INDEX `reopenwindow_event_id_target_type_target_id_created_at` ON `reopen_windows` (`event_id`, `target_type`, `target_id`, `created_at`);
-- Create "command_receipts" table
CREATE TABLE `command_receipts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `actor_kind` text NOT NULL DEFAULT 'Account',
  `actor_upload_link_id` integer NULL,
  `command_id` text NOT NULL,
  `payload_hash` text NOT NULL,
  `action` text NOT NULL,
  `target_type` text NOT NULL,
  `target_id` text NOT NULL,
  `outcome_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `actor_account_id` integer NULL,
  CONSTRAINT `command_receipts_accounts_command_receipts` FOREIGN KEY (`actor_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "command_receipts_command_id_key" to table: "command_receipts"
CREATE UNIQUE INDEX `command_receipts_command_id_key` ON `command_receipts` (`command_id`);
-- Create "display_overrides" table
CREATE TABLE `display_overrides` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `target_group_key` text NOT NULL,
  `kind` text NOT NULL,
  `text` text NOT NULL,
  `emphasis` text NOT NULL DEFAULT 'Normal',
  `preset_key` text NULL,
  `until_cleared` bool NOT NULL DEFAULT false,
  `expires_at` datetime NULL,
  `cleared_at` datetime NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `target_type` text NOT NULL DEFAULT 'DisplayGroup',
  `target_id` integer NOT NULL DEFAULT 0,
  `presentation` text NOT NULL DEFAULT 'Overlay',
  CONSTRAINT `display_overrides_events_display_overrides` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayoverride_event_id_kind_target_group_key_created_at" to table: "display_overrides"
CREATE INDEX `displayoverride_event_id_kind_target_group_key_created_at` ON `display_overrides` (`event_id`, `kind`, `target_group_key`, `created_at`);
-- Create index "displayoverride_event_id_cleared_at_expires_at" to table: "display_overrides"
CREATE INDEX `displayoverride_event_id_cleared_at_expires_at` ON `display_overrides` (`event_id`, `cleared_at`, `expires_at`);
-- Create "display_override_states" table
CREATE TABLE `display_override_states` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `kind` text NOT NULL,
  `revision` integer NOT NULL DEFAULT 1,
  `updated_at` datetime NOT NULL,
  `display_id` integer NOT NULL,
  `override_id` integer NOT NULL,
  CONSTRAINT `display_override_states_displays_override_states` FOREIGN KEY (`display_id`) REFERENCES `displays` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `display_override_states_display_overrides_states` FOREIGN KEY (`override_id`) REFERENCES `display_overrides` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "displayoverridestate_event_id_display_id_kind" to table: "display_override_states"
CREATE UNIQUE INDEX `displayoverridestate_event_id_display_id_kind` ON `display_override_states` (`event_id`, `display_id`, `kind`);
-- Create index "displayoverridestate_override_id" to table: "display_override_states"
CREATE INDEX `displayoverridestate_override_id` ON `display_override_states` (`override_id`);
-- Create "public_schedule_baselines" table
CREATE TABLE `public_schedule_baselines` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `source_published_revision` integer NOT NULL,
  `captured_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `public_schedule_baselines_events_public_schedule_baseline` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "public_schedule_baselines_event_id_key" to table: "public_schedule_baselines"
CREATE UNIQUE INDEX `public_schedule_baselines_event_id_key` ON `public_schedule_baselines` (`event_id`);
-- Create "public_schedule_baseline_entries" table
CREATE TABLE `public_schedule_baseline_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `forecast_start` datetime NOT NULL,
  `source_published_revision` integer NOT NULL,
  `recorded_at` datetime NOT NULL,
  `baseline_id` integer NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `public_schedule_baseline_entries_public_schedule_baselines_entries` FOREIGN KEY (`baseline_id`) REFERENCES `public_schedule_baselines` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `public_schedule_baseline_entries_sessions_public_schedule_baseline_entry` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "public_schedule_baseline_entries_session_id_key" to table: "public_schedule_baseline_entries"
CREATE UNIQUE INDEX `public_schedule_baseline_entries_session_id_key` ON `public_schedule_baseline_entries` (`session_id`);
-- Create "competition_result_standings" table
CREATE TABLE `competition_result_standings` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `standing` text NOT NULL,
  `placement` integer NULL,
  `display_order` integer NOT NULL,
  `decimal_score` text NULL,
  `duration_score_nanos` integer NULL,
  `entry_id` integer NOT NULL,
  `results_draft_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_result_standings_competition_entries_result_standings` FOREIGN KEY (`entry_id`) REFERENCES `competition_entries` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_competition_results_drafts_standings` FOREIGN KEY (`results_draft_id`) REFERENCES `competition_results_drafts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_events_competition_result_standings` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_result_standings_sessions_competition_result_standings` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "competitionresultstanding_results_draft_id_entry_id" to table: "competition_result_standings"
CREATE UNIQUE INDEX `competitionresultstanding_results_draft_id_entry_id` ON `competition_result_standings` (`results_draft_id`, `entry_id`);
-- Create index "competitionresultstanding_results_draft_id_display_order" to table: "competition_result_standings"
CREATE UNIQUE INDEX `competitionresultstanding_results_draft_id_display_order` ON `competition_result_standings` (`results_draft_id`, `display_order`);
-- Create index "competitionresultstanding_competition_session_id_results_draft_id" to table: "competition_result_standings"
CREATE INDEX `competitionresultstanding_competition_session_id_results_draft_id` ON `competition_result_standings` (`competition_session_id`, `results_draft_id`);
-- Create "event_awards_drafts" table
CREATE TABLE `event_awards_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `awards` json NULL,
  `path_states` json NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `event_awards_drafts_events_event_awards_drafts` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "eventawardsdraft_event_id_revision" to table: "event_awards_drafts"
CREATE UNIQUE INDEX `eventawardsdraft_event_id_revision` ON `event_awards_drafts` (`event_id`, `revision`);
-- Create "prizegivings" table
CREATE TABLE `prizegivings` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `ceremony_session_id` integer NOT NULL,
  `revision` integer NOT NULL DEFAULT 0,
  `competition_session_ids` json NULL,
  `sequence` json NULL,
  `publication_order` json NULL,
  `results_text_template` json NULL,
  `locked` bool NOT NULL DEFAULT false,
  `preflight_lock` json NULL,
  `locked_by_account_id` integer NULL,
  `locked_at` datetime NULL,
  `operation_revision` integer NOT NULL DEFAULT 0,
  `item_states` json NULL,
  `release_policy` text NOT NULL DEFAULT 'ProgressiveOnReveal',
  CONSTRAINT `prizegivings_events_prizegivings` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegivings_sessions_prizegiving` FOREIGN KEY (`ceremony_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "prizegivings_ceremony_session_id_key" to table: "prizegivings"
CREATE UNIQUE INDEX `prizegivings_ceremony_session_id_key` ON `prizegivings` (`ceremony_session_id`);
-- Create index "prizegiving_event_id_ceremony_session_id" to table: "prizegivings"
CREATE UNIQUE INDEX `prizegiving_event_id_ceremony_session_id` ON `prizegivings` (`event_id`, `ceremony_session_id`);
-- Create "prizegiving_competitions" table
CREATE TABLE `prizegiving_competitions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `prizegiving_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `prizegiving_competitions_events_prizegiving_competitions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegiving_competitions_prizegivings_competitions` FOREIGN KEY (`prizegiving_id`) REFERENCES `prizegivings` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `prizegiving_competitions_sessions_prizegiving_assignment` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "prizegiving_competitions_competition_session_id_key" to table: "prizegiving_competitions"
CREATE UNIQUE INDEX `prizegiving_competitions_competition_session_id_key` ON `prizegiving_competitions` (`competition_session_id`);
-- Create index "prizegivingcompetition_event_id_prizegiving_id_competition_session_id" to table: "prizegiving_competitions"
CREATE UNIQUE INDEX `prizegivingcompetition_event_id_prizegiving_id_competition_session_id` ON `prizegiving_competitions` (`event_id`, `prizegiving_id`, `competition_session_id`);
-- Create "results_publications" table
CREATE TABLE `results_publications` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `scope` text NOT NULL,
  `scope_session_id` integer NOT NULL,
  `revision` integer NOT NULL,
  `release_policy` text NOT NULL,
  `status` text NOT NULL,
  `items` json NOT NULL,
  `prizegiving_lock` json NULL,
  `created_by_account_id` integer NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `results_text_template` json NULL,
  `rendered_html` text NULL,
  `rendered_text` text NULL,
  `rendered_json` text NULL,
  `results_correction_revision` integer NULL,
  CONSTRAINT `results_publications_events_results_publications` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "resultspublication_event_id_scope_scope_session_id_revision" to table: "results_publications"
CREATE UNIQUE INDEX `resultspublication_event_id_scope_scope_session_id_revision` ON `results_publications` (`event_id`, `scope`, `scope_session_id`, `revision`);
-- Create index "resultspublication_event_id_scope_scope_session_id" to table: "results_publications"
CREATE INDEX `resultspublication_event_id_scope_scope_session_id` ON `results_publications` (`event_id`, `scope`, `scope_session_id`);
-- Create "results_corrections" table
CREATE TABLE `results_corrections` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `scope` text NOT NULL,
  `scope_session_id` integer NOT NULL,
  `revision` integer NOT NULL,
  `base_publication_revision` integer NOT NULL,
  `status` text NOT NULL,
  `publication_order` json NOT NULL,
  `items_json` text NOT NULL,
  `results_text_template` json NOT NULL,
  `crew_reason` text NOT NULL,
  `public_note` text NULL,
  `published_results_revision` integer NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `results_corrections_events_results_corrections` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "resultscorrection_event_id_scope_scope_session_id_revision" to table: "results_corrections"
CREATE UNIQUE INDEX `resultscorrection_event_id_scope_scope_session_id_revision` ON `results_corrections` (`event_id`, `scope`, `scope_session_id`, `revision`);
-- Create index "resultscorrection_event_id_scope_scope_session_id" to table: "results_corrections"
CREATE INDEX `resultscorrection_event_id_scope_scope_session_id` ON `results_corrections` (`event_id`, `scope`, `scope_session_id`);
-- Create "account_preferences" table
CREATE TABLE `account_preferences` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `reduced_effects` bool NOT NULL DEFAULT false,
  `account_id` integer NOT NULL,
  CONSTRAINT `account_preferences_accounts_preference` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "account_preferences_account_id_key" to table: "account_preferences"
CREATE UNIQUE INDEX `account_preferences_account_id_key` ON `account_preferences` (`account_id`);
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
-- Create "registration_policies" table
CREATE TABLE `registration_policies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `registration_open` bool NOT NULL DEFAULT true
);
INSERT INTO `registration_policies` (`registration_open`) VALUES (true);
-- Create "event_slugs" table
CREATE TABLE `event_slugs` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `slug` text NOT NULL,
  `exposed` bool NOT NULL DEFAULT false,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `event_slugs_events_slugs` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "event_slugs_slug_key" to table: "event_slugs"
CREATE UNIQUE INDEX `event_slugs_slug_key` ON `event_slugs` (`slug`);
-- Create "favorite_sessions" table
CREATE TABLE `favorite_sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `account_id` integer NOT NULL,
  `session_id` integer NOT NULL,
  CONSTRAINT `favorite_sessions_sessions_favorites` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `favorite_sessions_accounts_favorite_sessions` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "favoritesession_account_id_session_id" to table: "favorite_sessions"
CREATE UNIQUE INDEX `favoritesession_account_id_session_id` ON `favorite_sessions` (`account_id`, `session_id`);
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
-- Create "competition_entries" table
CREATE TABLE `competition_entries` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `public_details` text NULL,
  `crew_notes` text NULL,
  `disposition` text NOT NULL,
  `upload_closed_at` datetime NULL,
  `content_revision` integer NOT NULL DEFAULT 1,
  `reviewed_content_revision` integer NULL,
  `reviewed_by_account_id` integer NULL,
  `reviewed_at` datetime NULL,
  `first_presented_at` datetime NULL,
  `presentation_status` text NOT NULL DEFAULT 'Scheduled',
  `deferred_sequence` integer NULL,
  `resolution_required` bool NOT NULL DEFAULT false,
  `result_disposition` text NOT NULL DEFAULT 'Eligible',
  `technical_failure_reason` text NULL,
  `resolution_crew_reason` text NULL,
  `public_disqualification_message` text NULL,
  `release_hold` bool NOT NULL DEFAULT false,
  `revision` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `submitter_account_id` integer NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `competition_entries_accounts_competition_entries` FOREIGN KEY (`submitter_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `competition_entries_events_competition_entries` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_entries_sessions_competition_entries` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "competitionentry_competition_session_id_created_at" to table: "competition_entries"
CREATE INDEX `competitionentry_competition_session_id_created_at` ON `competition_entries` (`competition_session_id`, `created_at`);
-- Create "voting_eligibilities" table
CREATE TABLE `voting_eligibilities` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `account_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `voting_eligibilities_accounts_voting_eligibilities` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `voting_eligibilities_events_voting_eligibilities` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "votingeligibility_event_id_account_id" to table: "voting_eligibilities"
CREATE UNIQUE INDEX `votingeligibility_event_id_account_id` ON `voting_eligibilities` (`event_id`, `account_id`);
-- Create "sessions" table
CREATE TABLE `sessions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `lifecycle` text NOT NULL DEFAULT 'Scheduled',
  `live_state_revision` integer NOT NULL DEFAULT 0,
  `forecast_start` datetime NULL,
  `forecast_end` datetime NULL,
  `communicated_start` datetime NULL,
  `communicated_end` datetime NULL,
  `previous_forecast_start` datetime NULL,
  `forecast_lane_ids` json NULL,
  `forecast_location_ids` json NULL,
  `public_cancellation_message` text NULL,
  `cancellation_crew_notes` text NULL,
  `corrected_title` text NULL,
  `corrected_speaker` text NULL,
  `corrected_public_details` text NULL,
  `presentation_submission_revision` integer NOT NULL DEFAULT 0,
  `require_entry_review` bool NOT NULL DEFAULT false,
  `submission_eligibility_override` text NULL,
  `submission_eligibility_revision` integer NOT NULL DEFAULT 0,
  `file_delivery_required` bool NULL,
  `readiness_revision` integer NOT NULL DEFAULT 0,
  `entry_order_policy` text NOT NULL DEFAULT 'DeterministicShuffle',
  `entry_order_seed` integer NOT NULL DEFAULT 0,
  `entry_order_manual_ids` json NULL,
  `locked_entry_order_ids` json NULL,
  `entry_order_locked_at` datetime NULL,
  `entry_order_revision` integer NOT NULL DEFAULT 0,
  `program_output_kind` text NOT NULL DEFAULT 'Standby',
  `program_output_entry_id` integer NULL,
  `program_output_result` json NULL,
  `program_output_revision` integer NOT NULL DEFAULT 0,
  `program_cursor` integer NOT NULL DEFAULT -1,
  `program_output_taken_at` datetime NULL,
  `attachment_release_policy_override` text NULL,
  `attachment_release_revision` integer NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL,
  `submitter_account_id` integer NULL,
  `event_id` integer NOT NULL,
  `voting_method_override` text NULL,
  `self_vote_policy_override` text NULL,
  `voting_window_state` text NOT NULL DEFAULT 'Unopened',
  `frozen_voting_method` text NULL,
  `frozen_self_vote_policy` text NULL,
  `voting_revision` integer NOT NULL DEFAULT 0,
  `voting_opened_at` datetime NULL,
  `voting_closed_at` datetime NULL,
  CONSTRAINT `sessions_accounts_submitted_presentations` FOREIGN KEY (`submitter_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `sessions_events_sessions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "session_submitter_account_id" to table: "sessions"
CREATE INDEX `session_submitter_account_id` ON `sessions` (`submitter_account_id`);
-- Create "voting_keys" table
CREATE TABLE `voting_keys` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `created_at` datetime NOT NULL,
  `expires_at` datetime NOT NULL,
  `revoked_at` datetime NULL,
  `redeemed_at` datetime NULL,
  `redeemed_by_account_id` integer NULL,
  `event_id` integer NOT NULL,
  CONSTRAINT `voting_keys_accounts_redeemed_voting_keys` FOREIGN KEY (`redeemed_by_account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `voting_keys_events_voting_keys` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "voting_keys_token_hash_key" to table: "voting_keys"
CREATE UNIQUE INDEX `voting_keys_token_hash_key` ON `voting_keys` (`token_hash`);
-- Create index "votingkey_event_id_created_at" to table: "voting_keys"
CREATE INDEX `votingkey_event_id_created_at` ON `voting_keys` (`event_id`, `created_at`);
-- Create "votes" table
CREATE TABLE `votes` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `value` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  `account_id` integer NOT NULL,
  `entry_id` integer NOT NULL,
  CONSTRAINT `votes_competition_entries_votes` FOREIGN KEY (`entry_id`) REFERENCES `competition_entries` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_accounts_votes` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_sessions_votes` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `votes_events_votes` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "vote_competition_session_id_account_id_entry_id" to table: "votes"
CREATE UNIQUE INDEX `vote_competition_session_id_account_id_entry_id` ON `votes` (`competition_session_id`, `account_id`, `entry_id`);
-- Create index "vote_competition_session_id_account_id" to table: "votes"
CREATE INDEX `vote_competition_session_id_account_id` ON `votes` (`competition_session_id`, `account_id`);
-- Create "voting_tallies" table
CREATE TABLE `voting_tallies` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `method` text NOT NULL,
  `self_vote_policy` text NOT NULL,
  `participating` integer NOT NULL,
  `entries` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  CONSTRAINT `voting_tallies_events_voting_tallies` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `voting_tallies_sessions_voting_tallies` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "votingtally_competition_session_id" to table: "voting_tallies"
CREATE UNIQUE INDEX `votingtally_competition_session_id` ON `voting_tallies` (`competition_session_id`);
-- Create index "votingtally_event_id_competition_session_id" to table: "voting_tallies"
CREATE INDEX `votingtally_event_id_competition_session_id` ON `voting_tallies` (`event_id`, `competition_session_id`);
-- Create "competition_results_drafts" table
CREATE TABLE `competition_results_drafts` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `revision` integer NOT NULL,
  `disposition` text NOT NULL,
  `no_public_crew_reason` text NULL,
  `public_explanation` text NULL,
  `score_type` text NOT NULL,
  `score_visibility` text NOT NULL DEFAULT 'Public',
  `score_unit` text NULL,
  `score_precision` integer NOT NULL DEFAULT 0,
  `score_requirement` text NOT NULL DEFAULT 'Optional',
  `score_interpretation` text NOT NULL DEFAULT 'Informational',
  `awards` json NULL,
  `tally_override_crew_reason` text NULL,
  `ready_by_account_id` integer NULL,
  `ready_at` datetime NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `event_id` integer NOT NULL,
  `competition_session_id` integer NOT NULL,
  `voting_tally_id` integer NULL,
  CONSTRAINT `competition_results_drafts_events_competition_results_drafts` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_results_drafts_sessions_competition_results_drafts` FOREIGN KEY (`competition_session_id`) REFERENCES `sessions` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `competition_results_drafts_voting_tallies_results_drafts` FOREIGN KEY (`voting_tally_id`) REFERENCES `voting_tallies` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "competitionresultsdraft_competition_session_id_revision" to table: "competition_results_drafts"
CREATE UNIQUE INDEX `competitionresultsdraft_competition_session_id_revision` ON `competition_results_drafts` (`competition_session_id`, `revision`);
-- Create index "competitionresultsdraft_event_id_competition_session_id_revision" to table: "competition_results_drafts"
CREATE INDEX `competitionresultsdraft_event_id_competition_session_id_revision` ON `competition_results_drafts` (`event_id`, `competition_session_id`, `revision`);
-- Create "installation_theme_revisions" table
CREATE TABLE `installation_theme_revisions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `config` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL
);
-- Create "installations" table
CREATE TABLE `installations` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime NOT NULL,
  `activation_generation` integer NOT NULL DEFAULT 0,
  `active_event_id` integer NULL,
  `active_theme_revision_id` integer NULL,
  CONSTRAINT `installations_events_active_event` FOREIGN KEY (`active_event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `installations_installation_theme_revisions_active_theme_revision` FOREIGN KEY (`active_theme_revision_id`) REFERENCES `installation_theme_revisions` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create "event_theme_revisions" table
CREATE TABLE `event_theme_revisions` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `event_id` integer NOT NULL,
  `config` json NOT NULL,
  `created_by_account_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  CONSTRAINT `event_theme_revisions_events_theme_revisions` FOREIGN KEY (`event_id`) REFERENCES `events` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "eventthemerevision_event_id" to table: "event_theme_revisions"
CREATE INDEX `eventthemerevision_event_id` ON `event_theme_revisions` (`event_id`);
-- Enforce cross-table ownership constraints that Ent cannot express.
CREATE TRIGGER `events_active_theme_revision_owner_insert`
BEFORE INSERT ON `events`
WHEN NEW.`active_theme_revision_id` IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `event_theme_revisions`
    WHERE `id` = NEW.`active_theme_revision_id`
      AND `event_id` = NEW.`id`
  )
BEGIN
  SELECT RAISE(ABORT, 'active Event Theme Revision belongs to another Event');
END;
CREATE TRIGGER `events_active_theme_revision_owner_update`
BEFORE UPDATE OF `active_theme_revision_id` ON `events`
WHEN NEW.`active_theme_revision_id` IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `event_theme_revisions`
    WHERE `id` = NEW.`active_theme_revision_id`
      AND `event_id` = NEW.`id`
  )
BEGIN
  SELECT RAISE(ABORT, 'active Event Theme Revision belongs to another Event');
END;
CREATE TRIGGER `lane_drafts_same_event_insert`
BEFORE INSERT ON `lane_drafts`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;
CREATE TRIGGER `lane_drafts_same_event_update`
BEFORE UPDATE OF `lane_id`, `location_id` ON `lane_drafts`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;
CREATE TRIGGER `lane_published_versions_same_event_insert`
BEFORE INSERT ON `lane_published_versions`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'lane and location must belong to the same Event');
END;
CREATE TRIGGER `session_draft_lanes_same_event_insert`
BEFORE INSERT ON `session_draft_lanes`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_drafts`
        ON `session_drafts`.`session_id` = `sessions`.`id`
      WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;
CREATE TRIGGER `session_draft_locations_same_event_insert`
BEFORE INSERT ON `session_draft_locations`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_drafts`
        ON `session_drafts`.`session_id` = `sessions`.`id`
      WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;
CREATE TRIGGER `session_draft_tracks_same_event_insert`
BEFORE INSERT ON `session_draft_tracks`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_drafts`
        ON `session_drafts`.`session_id` = `sessions`.`id`
      WHERE `session_drafts`.`id` = NEW.`session_draft_id`) !=
     (SELECT `event_id` FROM `tracks` WHERE `id` = NEW.`track_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;
CREATE TRIGGER `session_published_lanes_same_event_insert`
BEFORE INSERT ON `session_published_version_lanes`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_published_versions`
        ON `session_published_versions`.`session_id` = `sessions`.`id`
      WHERE `session_published_versions`.`id` =
        NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `lanes` WHERE `id` = NEW.`lane_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;
CREATE TRIGGER `session_published_locations_same_event_insert`
BEFORE INSERT ON `session_published_version_locations`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_published_versions`
        ON `session_published_versions`.`session_id` = `sessions`.`id`
      WHERE `session_published_versions`.`id` =
        NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `locations` WHERE `id` = NEW.`location_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;
CREATE TRIGGER `session_published_tracks_same_event_insert`
BEFORE INSERT ON `session_published_version_tracks`
FOR EACH ROW
WHEN (SELECT `event_id` FROM `sessions`
      JOIN `session_published_versions`
        ON `session_published_versions`.`session_id` = `sessions`.`id`
      WHERE `session_published_versions`.`id` =
        NEW.`session_published_version_id`) !=
     (SELECT `event_id` FROM `tracks` WHERE `id` = NEW.`track_id`)
BEGIN
  SELECT RAISE(ABORT, 'Session membership must belong to the same Event');
END;

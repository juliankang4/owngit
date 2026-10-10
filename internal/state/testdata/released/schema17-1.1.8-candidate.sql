PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE backup_runs (
			id TEXT PRIMARY KEY CHECK (length(id) = 32),
			kind TEXT NOT NULL CHECK (kind IN ('scheduled','manual')),
			status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','interrupted')),
			destination TEXT NOT NULL CHECK (length(CAST(destination AS BLOB)) BETWEEN 1 AND 4096),
			backup_name TEXT NOT NULL DEFAULT '' CHECK (length(CAST(backup_name AS BLOB)) <= 255),
			verification TEXT NOT NULL DEFAULT 'not_run' CHECK (verification IN ('not_run','passed','failed')),
			message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(message AS BLOB)) <= 500),
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			-- The longest time the backup kept one repository's Git writes
			-- waiting and that repository, once its capture finished.
			longest_hold_ms INTEGER CHECK (longest_hold_ms IS NULL OR longest_hold_ms >= 0),
			longest_hold_repository TEXT CHECK (longest_hold_repository IS NULL OR length(longest_hold_repository) BETWEEN 1 AND 100),
			-- The SHA-256 of the manifest the run wrote, which names its
			-- backup: only the folder that holds this manifest is the run's.
			manifest_sha256 TEXT CHECK (manifest_sha256 IS NULL OR (length(manifest_sha256) = 64 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'))
		);
CREATE TABLE backup_schedule (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
			interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 3600 AND 2592000),
			destination TEXT NOT NULL CHECK (length(CAST(destination AS BLOB)) BETWEEN 1 AND 4096),
			keep INTEGER NOT NULL CHECK (keep BETWEEN 1 AND 1000),
			verify INTEGER NOT NULL DEFAULT 1 CHECK (verify IN (0,1)),
			updated_at INTEGER NOT NULL
		);
CREATE TABLE bootstrap (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			token_hash BLOB NOT NULL,
			expires_at INTEGER NOT NULL
		);
CREATE TABLE check_configurations (
			repository_id TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version > 0),
			config_hash TEXT NOT NULL,
			checks_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, version),
			UNIQUE (repository_id, config_hash),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_configurations VALUES('alpha',1,'07fa9f30ae3862a88fb901922d3ec24f0bbdc5eefd3604104964aa0265363e12','[{"name":"passes","command":"echo synthetic-pass"},{"name":"fails","command":"echo synthetic-fail; exit 3"}]',1790336671);
INSERT INTO check_configurations VALUES('container-fixture',1,'a7c3d299100a7686026fab5923ee4a8cad99bd38684d4577d13ae5c5ec523e2b','[{"name":"synthetic","command":"true"}]',1800000000);
INSERT INTO check_configurations VALUES('workflow-fixture',1,'1bab62a7e75bd7a69559e27590bb7349e95f707615a37a3b78d3ab082698a101','[{"name":"test","command":"echo synthetic"}]',1800000000);
INSERT INTO check_configurations VALUES('workflow-fixture',2,'796f6f15560f4daf249da82839fdc08be40d8b0c078ac01b27f32a14db5804a5','[{"name":"unit","command":"go test ./..."}]',1800000060);
CREATE TABLE check_cycles (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			reserved_at INTEGER NOT NULL,
			reserved_after_sequence INTEGER NOT NULL,
			UNIQUE (task_id, sequence),
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE check_job_runtime_ownership (
			job_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			container_name TEXT NOT NULL CHECK (length(CAST(container_name AS BLOB)) BETWEEN 1 AND 200),
			container_id TEXT NOT NULL CHECK (length(container_id)=0 OR length(container_id) BETWEEN 12 AND 64),
			daemon_id TEXT NOT NULL CHECK (length(CAST(daemon_id AS BLOB)) BETWEEN 1 AND 200),
			created_at INTEGER NOT NULL,
			FOREIGN KEY (job_id) REFERENCES check_jobs(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_job_runtime_ownership VALUES('dd3da90fdb9ea94a96929cb87e835fa0','container-fixture','owngit-synthetic-fixture','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','synthetic-daemon',1800000000000000000);
CREATE TABLE check_observations (
			repository_id TEXT NOT NULL,
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			oid TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id,ref_name),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_observations VALUES('alpha','refs/heads/feature','ff3b89a747a7d6f3bc54451d8b14a64c59c7f532',1790336671849777000);
INSERT INTO check_observations VALUES('alpha','refs/heads/main','0351dee0aee955aafa262b5b90593aa06d02dd97',1790336671850009000);
CREATE TABLE check_policies (
			repository_id TEXT PRIMARY KEY,
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			policy_digest TEXT NOT NULL CHECK (length(policy_digest) = 64),
			executor TEXT NOT NULL CHECK (executor IN ('host','container','external_runner')),
			allowed_events TEXT NOT NULL CHECK (length(CAST(allowed_events AS BLOB)) BETWEEN 2 AND 100),
			max_timeout_ms INTEGER NOT NULL CHECK (max_timeout_ms > 0),
			max_output_limit_bytes INTEGER NOT NULL CHECK (max_output_limit_bytes > 0),
			queue_limit INTEGER NOT NULL CHECK (queue_limit > 0),
			max_active_jobs INTEGER NOT NULL CHECK (max_active_jobs > 0),
			max_lease_ms INTEGER NOT NULL CHECK (max_lease_ms > 0),
			consent_version INTEGER NOT NULL DEFAULT 0 CHECK (consent_version >= 0),
			consent_digest TEXT NOT NULL DEFAULT '' CHECK (consent_digest = '' OR length(consent_digest) = 64),
			consent_active INTEGER NOT NULL DEFAULT 0 CHECK (consent_active IN (0,1)),
			runner_generation INTEGER NOT NULL DEFAULT 0 CHECK (runner_generation >= 0),
			authority_epoch TEXT NOT NULL CHECK (length(authority_epoch) = 32),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL, execution_json TEXT NOT NULL DEFAULT '{"legacy":true}' CHECK (length(CAST(execution_json AS BLOB)) BETWEEN 2 AND 4096), run_workflows INTEGER NOT NULL DEFAULT 0 CHECK (run_workflows IN (0,1)),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_policies VALUES('alpha',1,'3203dccbc95ba6960900062cd8ac6aa54e60f12033a12c177c6483382e1c29de','host','["pull_request","push"]',600000,1048576,32,1,60000,1,'3203dccbc95ba6960900062cd8ac6aa54e60f12033a12c177c6483382e1c29de',1,0,'f7d2bd182b1c903444d46600ffd73b33',1790336671,1790336671,'{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}',0);
INSERT INTO check_policies VALUES('container-fixture',1,'425cd559391eb9f980424192061bb88a1432569796ef29b3ab5e57c2d26fab79','container','["push"]',60000,65536,4,1,60000,1,'425cd559391eb9f980424192061bb88a1432569796ef29b3ab5e57c2d26fab79',1,0,'73c6822b6b89cd83352ad2851e57cb4e',1800000000,1800000000,'{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216},"container_image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","container_runtime":"docker-local","container_network":"none","container_cpu_millis":1000,"container_memory_bytes":536870912,"container_pids":256,"container_scratch_bytes":536870912}',0);
INSERT INTO check_policies VALUES('workflow-fixture',1,'69294fcb4870c4616a669992635d52b1c3711457bbd89f6c9e4289be74a09fc2','external_runner','["pull_request","push","schedule","workflow_dispatch"]',600000,65536,4,1,60000,1,'69294fcb4870c4616a669992635d52b1c3711457bbd89f6c9e4289be74a09fc2',1,0,'96302de8b672811ea48d50d43d699f91',1800000000,1800000000,'{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}',1);
CREATE TABLE check_raw_log_starts (
			created_at INTEGER NOT NULL,
			attempt_id TEXT NOT NULL UNIQUE,
			PRIMARY KEY (created_at, attempt_id),
			FOREIGN KEY (attempt_id) REFERENCES check_raw_logs(attempt_id) ON DELETE CASCADE
		) WITHOUT ROWID;
INSERT INTO check_raw_log_starts VALUES(1790336671,'605bb39b962b2139d4c20ef10407457d');
INSERT INTO check_raw_log_starts VALUES(1800000060,'d58ea5bbbca87911ab6fac895c6b3f14');
CREATE TABLE check_raw_logs (
			attempt_id TEXT PRIMARY KEY,
			content BLOB NOT NULL CHECK (length(content) <= 262144),
			expires_at INTEGER NOT NULL,
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		) WITHOUT ROWID;
INSERT INTO check_raw_logs VALUES('605bb39b962b2139d4c20ef10407457d',X'5b7061737365645d206563686f2073796e7468657469632d706173730a73796e7468657469632d706173730a0a5b6661696c65645d206563686f2073796e7468657469632d6661696c3b206578697420330a73796e7468657469632d6661696c0a0a',1792928671);
INSERT INTO check_raw_logs VALUES('d58ea5bbbca87911ab6fac895c6b3f14',X'73796e746865746963206f757470757420636c6970706564',1802592060);
CREATE TABLE check_runner_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			label TEXT NOT NULL CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 100),
			creation_id TEXT NOT NULL DEFAULT '',
			generation INTEGER NOT NULL CHECK (generation > 0),
			token_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			revoked_at INTEGER,
			last_used_at INTEGER, role TEXT NOT NULL DEFAULT 'external_runner' CHECK (role IN ('server','external_runner')),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE direct_review_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 100),
			value TEXT NOT NULL CHECK (length(CAST(value AS BLOB)) BETWEEN 1 AND 16384),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE direct_review_probes (
			request_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			registration_digest TEXT NOT NULL,
			capability_fingerprint TEXT NOT NULL,
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			connection_version INTEGER NOT NULL CHECK (connection_version >= 0),
			connection_fingerprint TEXT NOT NULL,
			protocol TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			model TEXT NOT NULL,
			authentication_mode TEXT NOT NULL,
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			disclosure_version TEXT NOT NULL,
			disclosure_digest TEXT NOT NULL,
			phase TEXT NOT NULL CHECK (phase IN ('preparing','running','terminal')),
			cancel_requested_at INTEGER,
			created_at INTEGER NOT NULL,
			running_at INTEGER,
			terminal_at INTEGER,
			result_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(result_json AS BLOB)) <= 1048576),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE direct_review_repository_settings (
			repository_id TEXT PRIMARY KEY,
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			protocol TEXT NOT NULL CHECK (protocol IN ('openai_responses','anthropic_messages','compatible_chat_completions')),
			endpoint TEXT NOT NULL CHECK (length(CAST(endpoint AS BLOB)) BETWEEN 1 AND 2048),
			model TEXT NOT NULL CHECK (length(CAST(model AS BLOB)) BETWEEN 1 AND 200),
			authentication_mode TEXT NOT NULL CHECK (authentication_mode IN ('stored_credential','none')),
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			repository_limits_json TEXT NOT NULL CHECK (length(CAST(repository_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			instruction_version TEXT NOT NULL CHECK (length(CAST(instruction_version AS BLOB)) BETWEEN 1 AND 100),
			credential_id TEXT NOT NULL DEFAULT '',
			connection_version INTEGER NOT NULL DEFAULT 0 CHECK (connection_version >= 0),
			authority_epoch TEXT NOT NULL CHECK (length(authority_epoch) = 32),
			probe_fingerprint TEXT NOT NULL DEFAULT '',
			probe_request_id TEXT NOT NULL DEFAULT '',
			probe_updated_at INTEGER NOT NULL DEFAULT 0,
			automatic_pr_enabled INTEGER NOT NULL DEFAULT 0 CHECK (automatic_pr_enabled IN (0,1)),
			automatic_task_enabled INTEGER NOT NULL DEFAULT 0 CHECK (automatic_task_enabled IN (0,1)),
			automatic_consent_version INTEGER NOT NULL DEFAULT 0 CHECK (automatic_consent_version >= 0),
			automatic_consent_digest TEXT NOT NULL DEFAULT '',
			automatic_consent_active INTEGER NOT NULL DEFAULT 0 CHECK (automatic_consent_active IN (0,1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE direct_review_requests (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL UNIQUE,
			repository_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('manual','automatic_pr','automatic_task')),
			source_event_key TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			task_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			observed_source_oid TEXT NOT NULL DEFAULT '',
			observed_target_oid TEXT NOT NULL DEFAULT '',
			effective_base_oid TEXT NOT NULL DEFAULT '',
			effective_head_oid TEXT NOT NULL DEFAULT '',
			diff_mode TEXT NOT NULL DEFAULT '' CHECK (diff_mode IN ('','two_commit')),
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			connection_version INTEGER NOT NULL CHECK (connection_version >= 0),
			connection_fingerprint TEXT NOT NULL,
			protocol TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			model TEXT NOT NULL,
			authentication_mode TEXT NOT NULL,
			provider_limits_json TEXT NOT NULL CHECK (length(CAST(provider_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			repository_limits_json TEXT NOT NULL CHECK (length(CAST(repository_limits_json AS BLOB)) BETWEEN 2 AND 4096),
			instruction_version TEXT NOT NULL,
			consent_version TEXT NOT NULL DEFAULT '',
			consent_digest TEXT NOT NULL DEFAULT '',
			disclosure_version TEXT NOT NULL,
			disclosure_digest TEXT NOT NULL,
			registration_digest TEXT NOT NULL,
			phase TEXT NOT NULL CHECK (phase IN ('observed','preparing','running','terminal')),
			cancel_requested_at INTEGER,
			created_at INTEGER NOT NULL,
			preparing_at INTEGER,
			running_at INTEGER,
			terminal_at INTEGER,
			initial_context_bytes INTEGER NOT NULL DEFAULT 0 CHECK (initial_context_bytes >= 0),
			initial_context_truncated INTEGER NOT NULL DEFAULT 0 CHECK (initial_context_truncated IN (0,1)),
			result_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(result_json AS BLOB)) <= 1048576),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE direct_review_task_contexts (
			context_id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL UNIQUE,
			credential_id TEXT NOT NULL,
			base_oid TEXT NOT NULL,
			head_oid TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			observed_source_oid TEXT NOT NULL DEFAULT '',
			observed_target_oid TEXT NOT NULL DEFAULT '',
			registration_digest TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		);
CREATE TABLE helper_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			label TEXT NOT NULL,
			creation_id TEXT NOT NULL DEFAULT '',
			token_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			revoked_at INTEGER,
			last_used_at INTEGER,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE import_initial_destinations (
			name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 80),
			repository_id TEXT NOT NULL CHECK (length(repository_id) <= 100),
			run_id TEXT NOT NULL CHECK (length(run_id) <= 64),
			root_id TEXT NOT NULL CHECK (length(root_id) = 32),
			token TEXT NOT NULL CHECK (length(token) BETWEEN 16 AND 64),
			display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 100),
			description TEXT NOT NULL DEFAULT '' CHECK (length(CAST(description AS BLOB)) <= 500),
			state TEXT NOT NULL CHECK (state IN ('preparing','ready','published','released','cleanup_failed','unknown')),
			issue TEXT NOT NULL DEFAULT '' CHECK (length(CAST(issue AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
CREATE TABLE IF NOT EXISTS "import_publication_intents" (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			status TEXT NOT NULL CHECK (status IN ('planning','applied','complete','not_applied','abandoned','unresolved','invalidated','owner_resolved')),
			expected_json TEXT NOT NULL CHECK (length(CAST(expected_json AS BLOB)) BETWEEN 2 AND 8388608),
			desired_json TEXT NOT NULL CHECK (length(CAST(desired_json AS BLOB)) BETWEEN 2 AND 8388608),
			observed_json TEXT NOT NULL CHECK (length(CAST(observed_json AS BLOB)) BETWEEN 2 AND 8388608),
			retained_json TEXT NOT NULL CHECK (length(CAST(retained_json AS BLOB)) BETWEEN 2 AND 8388608),
			head_symref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_symref AS BLOB)) <= 500),
			head_detach TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_detach AS BLOB)) <= 64),
			receipt_json TEXT NOT NULL DEFAULT '' CHECK (length(CAST(receipt_json AS BLOB)) <= 8388608),
			receipt_digest TEXT NOT NULL DEFAULT '' CHECK (receipt_digest = '' OR length(receipt_digest) = 64),
			reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(reason AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			head_owned INTEGER NOT NULL DEFAULT 0 CHECK (head_owned IN (0,1))
		);
CREATE TABLE import_ref_observations (
			repository_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			oid TEXT NOT NULL DEFAULT '' CHECK (oid = '' OR length(oid) IN (40,64)),
			symref_target TEXT NOT NULL DEFAULT '' CHECK (length(CAST(symref_target AS BLOB)) <= 500),
			observed_at INTEGER NOT NULL,
			run_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(run_id AS BLOB)) <= 64),
			PRIMARY KEY (repository_id, source_generation, ref_name)
		);
CREATE TABLE import_runs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			kind TEXT NOT NULL CHECK (kind IN ('initial','refresh','scheduled')),
			status TEXT NOT NULL CHECK (status IN ('preparing','fetching','indexing','inspecting','publishing','complete','failed','cancelled','superseded','interrupted','unresolved')),
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			cancel_requested_at INTEGER,
			object_format TEXT NOT NULL DEFAULT '' CHECK (object_format IN ('','sha1','sha256')),
			refs_seen INTEGER NOT NULL DEFAULT 0 CHECK (refs_seen >= 0),
			refs_created INTEGER NOT NULL DEFAULT 0 CHECK (refs_created >= 0),
			refs_updated INTEGER NOT NULL DEFAULT 0 CHECK (refs_updated >= 0),
			refs_unchanged INTEGER NOT NULL DEFAULT 0 CHECK (refs_unchanged >= 0),
			refs_divergent INTEGER NOT NULL DEFAULT 0 CHECK (refs_divergent >= 0),
			refs_deleted_upstream INTEGER NOT NULL DEFAULT 0 CHECK (refs_deleted_upstream >= 0),
			refs_skipped INTEGER NOT NULL DEFAULT 0 CHECK (refs_skipped >= 0),
			pack_bytes INTEGER NOT NULL DEFAULT 0 CHECK (pack_bytes >= 0),
			http_body_bytes INTEGER NOT NULL DEFAULT 0 CHECK (http_body_bytes >= 0),
			head_advertised INTEGER NOT NULL DEFAULT 0 CHECK (head_advertised IN (0,1)),
			head_symref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_symref AS BLOB)) <= 500),
			error_class TEXT NOT NULL DEFAULT '' CHECK (length(CAST(error_class AS BLOB)) <= 100),
			message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(message AS BLOB)) <= 500),
			lfs_detected INTEGER NOT NULL DEFAULT 0 CHECK (lfs_detected >= 0),
			lfs_inspection_complete INTEGER NOT NULL DEFAULT 0 CHECK (lfs_inspection_complete IN (0,1)),
			lfs_scanned_blobs INTEGER NOT NULL DEFAULT 0 CHECK (lfs_scanned_blobs >= 0),
			lfs_scanned_bytes INTEGER NOT NULL DEFAULT 0 CHECK (lfs_scanned_bytes >= 0),
			staging_name TEXT NOT NULL DEFAULT '' CHECK (length(CAST(staging_name AS BLOB)) <= 100),
			cleanup_error TEXT NOT NULL DEFAULT '' CHECK (length(CAST(cleanup_error AS BLOB)) <= 500),
			created_at INTEGER NOT NULL
		);
CREATE TABLE import_schedules (
			repository_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
			interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 60 AND 604800),
			last_started_at INTEGER,
			last_finished_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
CREATE TABLE import_sources (
			repository_id TEXT PRIMARY KEY,
			url TEXT NOT NULL CHECK (length(CAST(url AS BLOB)) BETWEEN 1 AND 8192),
			source_generation INTEGER NOT NULL CHECK (source_generation > 0),
			authority_revision INTEGER NOT NULL CHECK (authority_revision > 0),
			credential_generation TEXT NOT NULL DEFAULT '' CHECK (length(credential_generation) IN (0,32)),
			mode TEXT NOT NULL CHECK (mode IN ('standalone','coexistence')),
			git_only_consent INTEGER NOT NULL DEFAULT 0 CHECK (git_only_consent IN (0,1)),
			allow_private_network INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_network IN (0,1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		, overwrite_diverged INTEGER NOT NULL DEFAULT 0 CHECK (overwrite_diverged IN (0,1)), follow_upstream_deletions INTEGER NOT NULL DEFAULT 0 CHECK (follow_upstream_deletions IN (0,1)), extra_ref_prefixes TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(extra_ref_prefixes) AND length(CAST(extra_ref_prefixes AS BLOB)) <= 4096), allow_plain_http INTEGER NOT NULL DEFAULT 0 CHECK (allow_plain_http IN (0,1)), redirect_policy TEXT NOT NULL DEFAULT 'refuse' CHECK (redirect_policy IN ('refuse','same_origin','approved')), approved_redirect_origin TEXT NOT NULL DEFAULT '' CHECK (length(CAST(approved_redirect_origin AS BLOB)) <= 2048), allow_reserved_addresses INTEGER NOT NULL DEFAULT 0 CHECK (allow_reserved_addresses IN (0,1)), limits_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(limits_json) AND length(CAST(limits_json AS BLOB)) <= 4096), sign_in_revision INTEGER NOT NULL DEFAULT 0 CHECK (sign_in_revision >= 0));
CREATE TABLE import_stagings (
			name TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			token TEXT NOT NULL CHECK (length(token) BETWEEN 16 AND 64),
			state TEXT NOT NULL CHECK (state IN ('active','released','cleanup_failed','unknown')),
			issue TEXT NOT NULL DEFAULT '' CHECK (length(CAST(issue AS BLOB)) <= 500),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
CREATE TABLE login_attempts (
			kind TEXT NOT NULL,
			address TEXT NOT NULL,
			window_started_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL,
			blocked_until INTEGER NOT NULL,
			PRIMARY KEY (kind, address)
		);
CREATE TABLE metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
INSERT INTO metadata VALUES('initialized','true');
INSERT INTO metadata VALUES('access_mode','password');
INSERT INTO metadata VALUES('access_session_version','1');
INSERT INTO metadata VALUES('admin_session_version','1');
INSERT INTO metadata VALUES('insecure_http_accepted','true');
INSERT INTO metadata VALUES('schema_version','17');
INSERT INTO metadata VALUES('repository_root','/home/example/repositories');
CREATE TABLE passwords (
			kind TEXT PRIMARY KEY CHECK (kind IN ('access','admin')),
			encoded TEXT NOT NULL
		);
INSERT INTO passwords VALUES('admin','$argon2id$v=19$m=65536,t=1,p=4$MPNq/hTEdD0pYnpwV3Yqig$Kvg67hSkLA/Oic2h1ivYrKh1xy7a/UtfWKAWH97jxM8');
INSERT INTO passwords VALUES('access','$argon2id$v=19$m=65536,t=1,p=4$JjRbnowjc/VKcxiAoUDA2Q$ENltR9Sw95oPN4cI6G9JKuSeYDUpQmsx7GdGK4rAX6M');
CREATE TABLE pull_request_merge_intents (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			mode TEXT NOT NULL,
			tree_oid TEXT NOT NULL,
			result_oid TEXT NOT NULL,
			receipt_ref TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('preparing','planned','ready','complete')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		);
INSERT INTO pull_request_merge_intents VALUES('beta',1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','fast_forward','','d82cd795c951b62feb478490384ee18a23d75bb4','refs/owngit/pull-requests/1/merge-receipt','complete',1790336670,1790336670);
CREATE TABLE IF NOT EXISTS "pull_request_reviews" (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','changes_requested','skipped','not_requested')),
			reviewer_label TEXT NOT NULL,
			provenance TEXT NOT NULL,
			created_at INTEGER NOT NULL, review_event_id TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '' CHECK (length(CAST(note AS BLOB)) <= 65536), actor TEXT NOT NULL DEFAULT '' CHECK (actor = '' OR (json_valid(actor) AND length(CAST(actor AS BLOB)) <= 1024)),
			PRIMARY KEY (repository_id, pull_request_number, sequence),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		);
INSERT INTO pull_request_reviews VALUES('alpha',1,1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8','not_requested','','default',1790336669,'','','');
INSERT INTO pull_request_reviews VALUES('beta',1,1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','not_requested','','default',1790336669,'','','');
INSERT INTO pull_request_reviews VALUES('gamma',1,1,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5','not_requested','','default',1790336670,'','','');
INSERT INTO pull_request_reviews VALUES('alpha',1,2,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8','pending','','review_request',1790336670,'','','');
INSERT INTO pull_request_reviews VALUES('gamma',1,2,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5','skipped','','explicit_skip',1790336670,'','','');
CREATE TABLE pull_request_revisions (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			recorded_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		);
INSERT INTO pull_request_revisions VALUES('alpha',1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8',1790336669);
INSERT INTO pull_request_revisions VALUES('beta',1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d',1790336669);
INSERT INTO pull_request_revisions VALUES('gamma',1,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5',1790336670);
INSERT INTO pull_request_revisions VALUES('alpha',1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','0351dee0aee955aafa262b5b90593aa06d02dd97',1790336671);
CREATE TABLE IF NOT EXISTS "pull_requests" (
			repository_id TEXT NOT NULL,
			number INTEGER NOT NULL CHECK (number > 0),
			title TEXT NOT NULL,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('creating','open','merged','closed')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			merge_source_oid TEXT NOT NULL DEFAULT '',
			merge_target_oid TEXT NOT NULL DEFAULT '',
			merge_oid TEXT NOT NULL DEFAULT '',
			merge_receipt_ref TEXT NOT NULL DEFAULT '',
			merged_at INTEGER, body TEXT NOT NULL DEFAULT '' CHECK (length(CAST(body AS BLOB)) <= 65536), edit_revision INTEGER NOT NULL DEFAULT 0 CHECK (edit_revision >= 0), edited_at INTEGER, created_by TEXT NOT NULL DEFAULT '' CHECK (created_by = '' OR (json_valid(created_by) AND length(CAST(created_by AS BLOB)) <= 1024)), edited_by TEXT NOT NULL DEFAULT '' CHECK (edited_by = '' OR (json_valid(edited_by) AND length(CAST(edited_by AS BLOB)) <= 1024)), merged_by TEXT NOT NULL DEFAULT '' CHECK (merged_by = '' OR (json_valid(merged_by) AND length(CAST(merged_by AS BLOB)) <= 1024)),
			PRIMARY KEY (repository_id, number),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO pull_requests VALUES('alpha',1,'Synthetic feature for alpha','feature','main','open',1790336669,1790336670,'','','','',NULL,'',0,NULL,'','','');
INSERT INTO pull_requests VALUES('beta',1,'Synthetic feature for beta','feature','main','merged',1790336669,1790336670,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','d82cd795c951b62feb478490384ee18a23d75bb4','refs/owngit/pull-requests/1/merge-receipt',1790336670,'',0,NULL,'','','');
INSERT INTO pull_requests VALUES('gamma',1,'Synthetic feature for gamma','feature','main','open',1790336670,1790336670,'','','','',NULL,'',0,NULL,'','','');
CREATE TABLE push_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			repository_id TEXT NOT NULL,
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			old_oid TEXT NOT NULL CHECK (old_oid = '' OR length(old_oid) IN (40,64)),
			new_oid TEXT NOT NULL CHECK (new_oid = '' OR length(new_oid) IN (40,64)),
			refs_updated INTEGER NOT NULL CHECK (refs_updated >= 1),
			actor TEXT NOT NULL DEFAULT '' CHECK (actor = '' OR (json_valid(actor) AND length(CAST(actor AS BLOB)) <= 1024)),
			pushed_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE repositories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
INSERT INTO repositories VALUES('alpha','alpha','Synthetic alpha repository',1790336667);
INSERT INTO repositories VALUES('beta','beta','Synthetic beta repository',1790336667);
INSERT INTO repositories VALUES('gamma','gamma','Synthetic gamma repository',1790336667);
INSERT INTO repositories VALUES('container-fixture','Container fixture','',1800000000);
INSERT INTO repositories VALUES('workflow-fixture','Workflow fixture','',1800000000);
CREATE TABLE repository_attempt_counters (
			repository_id TEXT PRIMARY KEY,
			attempt_sequence INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO repository_attempt_counters VALUES('alpha',1);
INSERT INTO repository_attempt_counters VALUES('workflow-fixture',1);
CREATE TABLE repository_names (
			name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 100),
			repository_id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('current','alias')),
			created_at INTEGER NOT NULL,
			alias_until INTEGER,
			CHECK ((kind = 'current') = (alias_until IS NULL)),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE repository_policies (
			repository_id TEXT PRIMARY KEY,
			retain_history INTEGER CHECK (retain_history IN (0,1)),
			protect_default_branch INTEGER NOT NULL DEFAULT 0 CHECK (protect_default_branch IN (0,1)),
			extra_ref_prefixes TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(extra_ref_prefixes) AND length(CAST(extra_ref_prefixes AS BLOB)) <= 4096),
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE sessions (
			token_hash BLOB PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('setup','general','admin')),
			csrf TEXT NOT NULL,
			version INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		, verified_at INTEGER);
INSERT INTO sessions VALUES(X'ba8141d9b5bb26f30a84c6715d0fe5ab80f5f5a765d85118a67fd1f639fd6334','general','lR56IYT5ipifxoLjEh1Wioicb61ShsKthFk2Bv-tWU4',1,1790379867,NULL);
INSERT INTO sessions VALUES(X'eef6ff338188bc612642b0f4a8358da8c97f277301adb3ebdf986c50a335f9d7','admin','KCuP6iEf8KrOoUZjDG5hviBMBFRcFOACU0njEbzGN9U',1,1790337567,1790335767);
CREATE TABLE share_links (
			id TEXT PRIMARY KEY CHECK (length(id) = 32),
			repository_id TEXT NOT NULL,
			secret_hash BLOB NOT NULL UNIQUE CHECK (length(secret_hash) = 32),
			scope TEXT NOT NULL CHECK (scope IN ('browse','clone')),
			label TEXT NOT NULL DEFAULT '' CHECK (length(CAST(label AS BLOB)) <= 100),
			password_hash TEXT NOT NULL DEFAULT '' CHECK (length(password_hash) <= 512),
			created_by TEXT NOT NULL DEFAULT '' CHECK (created_by = '' OR (json_valid(created_by) AND length(CAST(created_by AS BLOB)) <= 1024)),
			created_at INTEGER NOT NULL,
			expires_at INTEGER,
			revoked_at INTEGER,
			last_used_at INTEGER,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			title TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO tasks VALUES('4a508090c5e8cadb91fc95ec47453622','alpha','Automatic checks',1790336671,1790336671);
INSERT INTO tasks VALUES('6b7fe36313d764918e703a53ca5a6b9d','container-fixture','Automatic checks',1800000000,1800000000);
INSERT INTO tasks VALUES('ce284eff8f56d819fe583a04074b0bc0','workflow-fixture','Workflows',1800000000,1800000000);
INSERT INTO tasks VALUES('f238a1bbf6240bbb830c6222e2032411','workflow-fixture','Synthetic output limit',1800000060,1800000060);
CREATE TABLE trusted_hosts (
			host TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		);
CREATE TABLE IF NOT EXISTS "check_attempts" (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			revision_oid TEXT NOT NULL,
			registration_worktree_state TEXT NOT NULL CHECK (registration_worktree_state IN ('clean','dirty','unknown')),
			submitted_worktree_state TEXT NOT NULL DEFAULT '' CHECK (submitted_worktree_state IN ('','clean','dirty','unknown')),
			configuration_version INTEGER NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','passed','failed','error','cancelled','incomplete','unavailable','skipped')),
			exit_code INTEGER,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL DEFAULT '',
			protection TEXT NOT NULL DEFAULT 'unknown',
			execution_scope TEXT NOT NULL DEFAULT 'inherited',
			credential_id TEXT NOT NULL DEFAULT '',
			timeout_ms INTEGER NOT NULL DEFAULT 0,
			output_limit_bytes INTEGER NOT NULL DEFAULT 0,
			log_id TEXT NOT NULL DEFAULT '',
			log_expires_at INTEGER,
			log_truncated INTEGER NOT NULL DEFAULT 0,
			log_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			sequence INTEGER NOT NULL,
			cycle_id TEXT NOT NULL DEFAULT '',
			registration_digest TEXT NOT NULL,
			completion_digest TEXT NOT NULL DEFAULT '',
			submitted_log_digest TEXT NOT NULL DEFAULT '',
			submitted_truncated INTEGER NOT NULL DEFAULT 0,
			submitted_cancelled INTEGER NOT NULL DEFAULT 0,
			log_digest TEXT NOT NULL DEFAULT '',
			job_id TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_attempts VALUES('605bb39b962b2139d4c20ef10407457d','4a508090c5e8cadb91fc95ec47453622','alpha','0351dee0aee955aafa262b5b90593aa06d02dd97','clean','clean',1,'failed',3,1790336671820352000,1790336671829672000,9,'2 checks: 1 failed, 1 passed','host','inherited','f7d2bd182b1c903444d46600ffd73b33',60000,65536,'605bb39b962b2139d4c20ef10407457d',1792928671,0,'',1790336671,1,'','6169d05b9ed712ac74c8514dd3491661ac16d5d585245aeb3326ee0c53607b29','80804f71f7efb4bc92fdb2117503874b0d35c2ea80390cd7e7942175ced2e099','a07dfd076110ed77ff12fec91843d088b42eb743a40f0de38ec4cac25281a472',0,0,'a07dfd076110ed77ff12fec91843d088b42eb743a40f0de38ec4cac25281a472','fe3423d13cee2347d548dcab7da51823');
INSERT INTO check_attempts VALUES('d58ea5bbbca87911ab6fac895c6b3f14','f238a1bbf6240bbb830c6222e2032411','workflow-fixture','dddddddddddddddddddddddddddddddddddddddd','clean','clean',2,'incomplete',1,1800000060000000000,1800000061000000000,1000,'1 checks: 1 incomplete','unknown','inherited','',0,4096,'d58ea5bbbca87911ab6fac895c6b3f14',1802592060,0,'',1800000060,1,'','345e86a1de23863035c72810634063b48bd27f36d0527ab46fdfffb34b26ead2','7263dc7e07a537fc5cc6187280e8ce314c899e1f8018737abf70d6c91db6614b','a73672968a091140e880e7dccc13b484d0c6086895af55a0ebe374d4c8b49973',0,0,'a73672968a091140e880e7dccc13b484d0c6086895af55a0ebe374d4c8b49973','');
CREATE TABLE IF NOT EXISTS "check_results" (
			attempt_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			command TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('passed','failed','error','cancelled','incomplete','unavailable','skipped','not_run')),
			exit_code INTEGER,
			duration_ms INTEGER NOT NULL,
			output_excerpt TEXT NOT NULL,
			truncated INTEGER NOT NULL DEFAULT 0,
			cleanup_error TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '' CHECK (role IN ('','run','tolerated','builtin')),
			output_limit_exceeded_bytes INTEGER NOT NULL DEFAULT 0 CHECK (output_limit_exceeded_bytes >= 0),
			PRIMARY KEY (attempt_id, position),
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		);
INSERT INTO check_results VALUES('605bb39b962b2139d4c20ef10407457d',0,'passes','echo synthetic-pass','passed',0,3,replace('synthetic-pass\n','\n',char(10)),0,'','',0);
INSERT INTO check_results VALUES('605bb39b962b2139d4c20ef10407457d',1,'fails','echo synthetic-fail; exit 3','failed',3,3,replace('synthetic-fail\n','\n',char(10)),0,'','',0);
INSERT INTO check_results VALUES('d58ea5bbbca87911ab6fac895c6b3f14',0,'unit','go test ./...','incomplete',1,1000,'out',1,'','',4096);
CREATE TABLE IF NOT EXISTS "check_jobs" (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('push','pull_request','workflow_dispatch','schedule')),
			event_key TEXT NOT NULL CHECK (length(CAST(event_key AS BLOB)) BETWEEN 1 AND 200),
			source_oid TEXT NOT NULL,
			base_oid TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			trigger_ref TEXT NOT NULL CHECK (length(CAST(trigger_ref AS BLOB)) BETWEEN 1 AND 200),
			workflow_path TEXT NOT NULL,
			workflow_oid TEXT NOT NULL DEFAULT '',
			workflow_digest TEXT NOT NULL CHECK (length(workflow_digest) = 64),
			configuration_version INTEGER NOT NULL CHECK (configuration_version > 0),
			executor TEXT NOT NULL CHECK (executor IN ('host','container','external_runner')),
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			consent_version INTEGER NOT NULL CHECK (consent_version > 0),
			limits_json TEXT NOT NULL CHECK (length(CAST(limits_json AS BLOB)) BETWEEN 2 AND 4096),
			execution_json TEXT NOT NULL CHECK (length(CAST(execution_json AS BLOB)) BETWEEN 2 AND 4096),
			dedup_digest TEXT NOT NULL CHECK (length(dedup_digest) = 64),
			rerun_root TEXT NOT NULL DEFAULT '',
			rerun_generation INTEGER NOT NULL DEFAULT 0 CHECK (rerun_generation >= 0),
			status TEXT NOT NULL CHECK (status IN ('pending','claimed','started','passed','failed','error','cancelled','incomplete','unavailable','ambiguous','interrupted','waiting','skipped')),
			attempt_id TEXT NOT NULL DEFAULT '',
			lease_id TEXT NOT NULL DEFAULT '',
			lease_expires_at INTEGER,
			credential_id TEXT NOT NULL DEFAULT '',
			credential_generation INTEGER NOT NULL DEFAULT 0 CHECK (credential_generation >= 0),
			credential_role TEXT NOT NULL DEFAULT '' CHECK (credential_role IN ('','server','external_runner')),
			protection TEXT NOT NULL DEFAULT 'unknown' CHECK (protection IN ('unknown','host','container','runner_reported')),
			admitted_at INTEGER NOT NULL,
			claimed_at INTEGER,
			started_at INTEGER,
			finished_at INTEGER,
			lease_lost_at INTEGER,
			cancel_requested_at INTEGER,
			interrupted_at INTEGER,
			summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 500),
			run_id TEXT NOT NULL DEFAULT '',
			job_key TEXT NOT NULL DEFAULT '' CHECK (length(CAST(job_key AS BLOB)) <= 100),
			matrix_index INTEGER NOT NULL DEFAULT 0 CHECK (matrix_index >= 0),
			plan_digest TEXT NOT NULL DEFAULT '' CHECK (length(plan_digest) IN (0,64)),
			tolerated INTEGER NOT NULL DEFAULT 0 CHECK (tolerated IN (0,1)),
			concurrency_group TEXT NOT NULL DEFAULT '' CHECK (length(CAST(concurrency_group AS BLOB)) <= 200),
			max_parallel INTEGER NOT NULL DEFAULT 0 CHECK (max_parallel >= 0),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		);
INSERT INTO check_jobs VALUES('fe3423d13cee2347d548dcab7da51823','alpha','4a508090c5e8cadb91fc95ec47453622','push','refs/heads/main@0351dee0aee955aafa262b5b90593aa06d02dd97','0351dee0aee955aafa262b5b90593aa06d02dd97','',0,'main','.owngit/checks.json','e7dce93327dcae92579d485f1bc1e1d16dded94f','270f982dd082f2bd5042b778e4046f9dacb2fb33b1fd2c3dbda48a5a948cf299',1,'host',1,1,'{"timeout_ms":60000,"output_limit_bytes":65536}','{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}','f72d499deb3cfe2c87848d1b7d932b97d9358f964dc0f493afbc66e8d496d4be','',0,'failed','605bb39b962b2139d4c20ef10407457d','677f02f13f123733576c6641b6bb5d04',1790336731761819000,'f7d2bd182b1c903444d46600ffd73b33',1,'server','host',1790336671659982000,1790336671761819000,1790336671820352000,1790336671829672000,NULL,NULL,NULL,'2 checks: 1 failed, 1 passed','','',0,'',0,'',0);
INSERT INTO check_jobs VALUES('11111111111111111111111111111111','alpha','4a508090c5e8cadb91fc95ec47453622','push','refs/heads/main@dddddddddddddddddddddddddddddddddddddddd','dddddddddddddddddddddddddddddddddddddddd','',0,'main','.owngit/checks.json','e7dce93327dcae92579d485f1bc1e1d16dded94f','270f982dd082f2bd5042b778e4046f9dacb2fb33b1fd2c3dbda48a5a948cf299',1,'host',1,1,'{"timeout_ms":60000,"output_limit_bytes":65536}','{"legacy":true}','80be08f40513cb19a03184a3a00b16992867549e1bb583779a89648fd015e316','',0,'pending','','',NULL,'',0,'','unknown',1790336671659982000,NULL,NULL,NULL,NULL,NULL,NULL,'','','',0,'',0,'',0);
INSERT INTO check_jobs VALUES('dd3da90fdb9ea94a96929cb87e835fa0','container-fixture','6b7fe36313d764918e703a53ca5a6b9d','push','refs/heads/main@dddddddddddddddddddddddddddddddddddddddd','dddddddddddddddddddddddddddddddddddddddd','',0,'main','.owngit/checks.json','','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'container',1,1,'{"timeout_ms":60000,"output_limit_bytes":65536}','{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216},"container_image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","container_runtime":"docker-local","container_network":"none","container_cpu_millis":1000,"container_memory_bytes":536870912,"container_pids":256,"container_scratch_bytes":536870912}','f364cfb8d0684d3c89812dda7549ffce7b7590830858cdc258d22154c75e8b43','',0,'claimed','','14644125e1c0e3925496e68347898bae',1800000060000000000,'73c6822b6b89cd83352ad2851e57cb4e',1,'server','unknown',1800000000000000000,1800000000000000000,NULL,NULL,NULL,NULL,NULL,'','','',0,'',0,'',0);
INSERT INTO check_jobs VALUES('4adeafdab99214fa5a17a6168d2c3687','workflow-fixture','ce284eff8f56d819fe583a04074b0bc0','pull_request','pr/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/cccccccccccccccccccccccccccccccccccccccc','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','cccccccccccccccccccccccccccccccccccccccc',1,'main','.github/workflows/ci.yml','','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'external_runner',1,1,'{"timeout_ms":600000,"output_limit_bytes":65536}','{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}','25172a240ea7948f54de4aa29636e0962351b98a094a962b084b7d878c6b2223','',0,'pending','','',NULL,'',0,'','unknown',1800000000000000000,NULL,NULL,NULL,NULL,NULL,NULL,'','5a96bfd95860787de16d4a36037f8f42','test',0,'4dd1886c92a2f3ec674c7bd0c0db04e2c99ba94c8905835c4ac318e0d29ccab9',0,'',0);
CREATE TABLE actions_runs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			workflow_path TEXT NOT NULL CHECK (length(CAST(workflow_path AS BLOB)) <= 118),
			number INTEGER NOT NULL CHECK (number > 0),
			event TEXT NOT NULL CHECK (event IN ('push','pull_request','workflow_dispatch','schedule')),
			event_key TEXT NOT NULL CHECK (length(CAST(event_key AS BLOB)) BETWEEN 1 AND 200),
			source_oid TEXT NOT NULL,
			base_oid TEXT NOT NULL DEFAULT '',
			pull_request_number INTEGER NOT NULL DEFAULT 0 CHECK (pull_request_number >= 0),
			trigger_ref TEXT NOT NULL CHECK (length(CAST(trigger_ref AS BLOB)) BETWEEN 1 AND 200),
			inputs_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(inputs_json) AND length(CAST(inputs_json AS BLOB)) <= 65536),
			scheduled_for INTEGER,
			rerun_root TEXT NOT NULL DEFAULT '',
			rerun_generation INTEGER NOT NULL DEFAULT 0 CHECK (rerun_generation >= 0),
			outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('','refused','not_run')),
			reason TEXT NOT NULL DEFAULT '',
			concurrency_group TEXT NOT NULL DEFAULT '' CHECK (length(CAST(concurrency_group AS BLOB)) <= 200),
			cancel_in_progress INTEGER NOT NULL DEFAULT 0 CHECK (cancel_in_progress IN (0,1)),
			concurrency_queue TEXT NOT NULL DEFAULT 'single' CHECK (concurrency_queue IN ('single','max')),
			cancel_requested_at INTEGER,
			facts_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(facts_json) AND length(CAST(facts_json AS BLOB)) <= 65536),
			policy_version INTEGER NOT NULL CHECK (policy_version > 0),
			consent_version INTEGER NOT NULL CHECK (consent_version > 0),
			actor TEXT NOT NULL DEFAULT '' CHECK (actor = '' OR (json_valid(actor) AND length(CAST(actor AS BLOB)) <= 1024)),
			created_at INTEGER NOT NULL,
			UNIQUE (repository_id,event,event_key,workflow_path,rerun_generation),
			UNIQUE (repository_id,workflow_path,number),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO actions_runs VALUES('5a96bfd95860787de16d4a36037f8f42','workflow-fixture','.github/workflows/ci.yml',1,'pull_request','pr/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/cccccccccccccccccccccccccccccccccccccccc','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','cccccccccccccccccccccccccccccccccccccccc',1,'main','{}',NULL,'',0,'','','',0,'single',NULL,'{"workflow_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","pull_request_action":"opened","pull_request_head_ref":"feature","needs":{"test":null},"fail_fast":{"test":false}}',1,1,'',1800000000000000000);
CREATE TABLE actions_job_plans (
			job_id TEXT PRIMARY KEY,
			plan_json TEXT NOT NULL CHECK (json_valid(plan_json) AND length(CAST(plan_json AS BLOB)) <= 2097152),
			plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 64),
			FOREIGN KEY (job_id) REFERENCES check_jobs(id) ON DELETE CASCADE
		);
INSERT INTO actions_job_plans VALUES('4adeafdab99214fa5a17a6168d2c3687','{"job_key":"test","matrix_index":0,"concurrency":{},"workflow_defaults":{},"defaults":{},"context":{"github":{"sha":"","ref":"","ref_name":"","ref_type":"","event_name":"","event":{},"repository":"","actor":"","triggering_actor":"","run_id":"","run_number":0,"run_attempt":0,"workflow":"","job":""},"strategy":{"job-index":0,"job-total":0,"fail-fast":false,"max-parallel":0}},"steps":[{"name":"test","run":"echo synthetic"}],"secret_names":["SYNTHETIC_TOKEN"]}','4dd1886c92a2f3ec674c7bd0c0db04e2c99ba94c8905835c4ac318e0d29ccab9');
CREATE TABLE actions_schedules (
			repository_id TEXT NOT NULL,
			workflow_path TEXT NOT NULL,
			cron TEXT NOT NULL,
			source_oid TEXT NOT NULL,
			next_due_at INTEGER NOT NULL,
			last_slot_at INTEGER,
			last_admitted_at INTEGER,
			last_run_id TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id,workflow_path,cron),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
CREATE TABLE actions_accepted_pushes (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			repository_id TEXT NOT NULL,
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) <= 4096),
			old_oid TEXT NOT NULL,
			new_oid TEXT NOT NULL,
			consumed INTEGER NOT NULL DEFAULT 0 CHECK (consumed IN (0,1)),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
DELETE FROM sqlite_sequence;
CREATE UNIQUE INDEX helper_credentials_creation ON helper_credentials(repository_id, creation_id) WHERE creation_id != '';
CREATE INDEX check_cycles_task ON check_cycles(task_id, sequence);
CREATE INDEX check_raw_logs_expiry ON check_raw_logs(expires_at,attempt_id);
CREATE UNIQUE INDEX pull_request_reviews_event ON pull_request_reviews(repository_id,review_event_id) WHERE review_event_id != '';
CREATE INDEX direct_review_probes_repository ON direct_review_probes(repository_id,created_at,request_id);
CREATE INDEX direct_review_task_contexts_task ON direct_review_task_contexts(repository_id,task_id,created_at,context_id);
CREATE UNIQUE INDEX direct_review_requests_source_event ON direct_review_requests(repository_id,trigger_kind,source_event_key) WHERE source_event_key != '';
CREATE INDEX direct_review_requests_pull_request ON direct_review_requests(repository_id,pull_request_number,sequence) WHERE pull_request_number > 0;
CREATE INDEX direct_review_requests_task ON direct_review_requests(repository_id,task_id,sequence) WHERE task_id != '';
CREATE UNIQUE INDEX check_runner_credentials_creation ON check_runner_credentials(repository_id,creation_id) WHERE creation_id != '';
CREATE UNIQUE INDEX check_runner_credentials_hash ON check_runner_credentials(repository_id,token_hash) WHERE revoked_at IS NULL;
CREATE INDEX check_observations_recent ON check_observations(repository_id,observed_at,ref_name);
CREATE INDEX import_runs_repository ON import_runs(repository_id,started_at,id);
CREATE INDEX import_ref_observations_recent ON import_ref_observations(repository_id,observed_at,ref_name);
CREATE INDEX import_stagings_run ON import_stagings(run_id);
CREATE INDEX import_schedules_due ON import_schedules(enabled,last_started_at,repository_id);
CREATE INDEX import_initial_destinations_run ON import_initial_destinations(run_id);
CREATE INDEX import_publication_intents_repository ON import_publication_intents(repository_id,created_at,id);
CREATE UNIQUE INDEX repository_names_current ON repository_names(repository_id) WHERE kind = 'current';
CREATE INDEX repository_names_repository ON repository_names(repository_id,kind,name);
CREATE INDEX share_links_repository ON share_links(repository_id,created_at,id);
CREATE UNIQUE INDEX backup_runs_running ON backup_runs(status) WHERE status = 'running';
CREATE INDEX backup_runs_recent ON backup_runs(started_at,id);
CREATE INDEX push_events_repository ON push_events(repository_id,sequence);
CREATE INDEX check_attempts_revision ON check_attempts(repository_id,revision_oid,sequence);
CREATE INDEX check_attempts_task ON check_attempts(task_id,sequence);
CREATE INDEX check_attempts_pending ON check_attempts(task_id,status,sequence);
CREATE INDEX check_attempts_refusal_event ON check_attempts(repository_id,task_id,log_error,credential_id);
CREATE INDEX check_attempts_job ON check_attempts(job_id) WHERE job_id != '';
CREATE UNIQUE INDEX check_jobs_dedup ON check_jobs(repository_id,dedup_digest);
CREATE INDEX check_jobs_queue ON check_jobs(repository_id,status,admitted_at,id);
CREATE INDEX check_jobs_attempt ON check_jobs(attempt_id) WHERE attempt_id != '';
CREATE INDEX check_jobs_run ON check_jobs(run_id) WHERE run_id != '';
CREATE INDEX check_jobs_waiting ON check_jobs(repository_id) WHERE status='waiting';
CREATE INDEX check_jobs_group ON check_jobs(repository_id,concurrency_group) WHERE concurrency_group != '' AND status IN ('claimed','started');
CREATE INDEX check_jobs_live_leases ON check_jobs(lease_expires_at) WHERE status IN ('claimed','started');
CREATE INDEX check_jobs_pending ON check_jobs(repository_id,admitted_at,id) WHERE status='pending';
CREATE INDEX check_jobs_recent ON check_jobs(repository_id,admitted_at DESC,id DESC);
CREATE INDEX import_runs_finished ON import_runs(finished_at,id);
CREATE INDEX actions_runs_revision ON actions_runs(repository_id,source_oid);
CREATE INDEX actions_runs_group ON actions_runs(repository_id,concurrency_group) WHERE concurrency_group != '';
CREATE INDEX actions_schedules_due ON actions_schedules(next_due_at);
CREATE INDEX actions_accepted_pushes_pending ON actions_accepted_pushes(repository_id,sequence) WHERE consumed=0;
CREATE INDEX actions_accepted_pushes_source ON actions_accepted_pushes(repository_id,new_oid);
CREATE INDEX actions_accepted_pushes_consumed ON actions_accepted_pushes(repository_id,sequence) WHERE consumed=1;
COMMIT;

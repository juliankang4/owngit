PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
INSERT INTO metadata VALUES('initialized','true');
INSERT INTO metadata VALUES('access_mode','password');
INSERT INTO metadata VALUES('access_session_version','1');
INSERT INTO metadata VALUES('admin_session_version','1');
INSERT INTO metadata VALUES('insecure_http_accepted','true');
INSERT INTO metadata VALUES('schema_version','14');
INSERT INTO metadata VALUES('repository_root','/home/example/repositories');
CREATE TABLE passwords (
			kind TEXT PRIMARY KEY CHECK (kind IN ('access','admin')),
			encoded TEXT NOT NULL
		);
INSERT INTO passwords VALUES('admin','$argon2id$v=19$m=65536,t=1,p=4$+op4peC4BaQ3Jc4etMuCxQ$0I5rkQnMl7rfCKp8ESp3SuxTWjq53VvgurkGoIMd6Qw');
INSERT INTO passwords VALUES('access','$argon2id$v=19$m=65536,t=1,p=4$yXC+YvUhSj06zTrE/ZBZzg$jLHmyaMwptZKq41YFa2Sd0zMyePih1sEJ1eM+U7HPvI');
CREATE TABLE sessions (
			token_hash BLOB PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('setup','general','admin')),
			csrf TEXT NOT NULL,
			version INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);
INSERT INTO sessions VALUES(x'e997159d1d6ef6c575600b0ef15aaf694b1957b4cbeaec8c8b599661731bd4fc','general','zgCP8unznwjBYjgrh9cIHkXe4MYy_cPGuDVvs0lsO8s',1,1790379857);
INSERT INTO sessions VALUES(x'5fcd2067996fa120e5de0a2703e97f575f3d1507425bf6d37e3aaaffdccf491e','admin','lQxAoWUxRMTEBS8LglWIm0IaEn3rt28at4bOnv3hQdA',1,1790337557);
CREATE TABLE bootstrap (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			token_hash BLOB NOT NULL,
			expires_at INTEGER NOT NULL
		);
CREATE TABLE login_attempts (
			kind TEXT NOT NULL,
			address TEXT NOT NULL,
			window_started_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL,
			blocked_until INTEGER NOT NULL,
			PRIMARY KEY (kind, address)
		);
CREATE TABLE repositories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
INSERT INTO repositories VALUES('alpha','alpha','Synthetic alpha repository',1790336657);
INSERT INTO repositories VALUES('beta','beta','Synthetic beta repository',1790336657);
INSERT INTO repositories VALUES('gamma','gamma','Synthetic gamma repository',1790336658);
CREATE TABLE pull_requests (
			repository_id TEXT NOT NULL,
			number INTEGER NOT NULL CHECK (number > 0),
			title TEXT NOT NULL,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('creating','open','merged')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			merge_source_oid TEXT NOT NULL DEFAULT '',
			merge_target_oid TEXT NOT NULL DEFAULT '',
			merge_oid TEXT NOT NULL DEFAULT '',
			merge_receipt_ref TEXT NOT NULL DEFAULT '',
			merged_at INTEGER,
			PRIMARY KEY (repository_id, number),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO pull_requests VALUES('alpha',1,'Synthetic feature for alpha','feature','main','open',1790336659,1790336661,'','','','',NULL);
INSERT INTO pull_requests VALUES('beta',1,'Synthetic feature for beta','feature','main','merged',1790336660,1790336660,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','d82cd795c951b62feb478490384ee18a23d75bb4','refs/owngit/pull-requests/1/merge-receipt',1790336660);
INSERT INTO pull_requests VALUES('gamma',1,'Synthetic feature for gamma','feature','main','open',1790336660,1790336661,'','','','',NULL);
CREATE TABLE pull_request_revisions (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			recorded_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		);
INSERT INTO pull_request_revisions VALUES('alpha',1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8',1790336659);
INSERT INTO pull_request_revisions VALUES('beta',1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d',1790336660);
INSERT INTO pull_request_revisions VALUES('gamma',1,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5',1790336660);
INSERT INTO pull_request_revisions VALUES('alpha',1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','0351dee0aee955aafa262b5b90593aa06d02dd97',1790336662);
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
INSERT INTO pull_request_merge_intents VALUES('beta',1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','fast_forward','','d82cd795c951b62feb478490384ee18a23d75bb4','refs/owngit/pull-requests/1/merge-receipt','complete',1790336660,1790336660);
CREATE TABLE trusted_hosts (
			host TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		);
CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			title TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO tasks VALUES('4a508090c5e8cadb91fc95ec47453622','alpha','Automatic checks',1790336662,1790336662);
CREATE TABLE repository_attempt_counters (
			repository_id TEXT PRIMARY KEY,
			attempt_sequence INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO repository_attempt_counters VALUES('alpha',1);
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
INSERT INTO check_configurations VALUES('alpha',1,'07fa9f30ae3862a88fb901922d3ec24f0bbdc5eefd3604104964aa0265363e12','[{"name":"passes","command":"echo synthetic-pass"},{"name":"fails","command":"echo synthetic-fail; exit 3"}]',1790336662);
CREATE TABLE check_attempts (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			revision_oid TEXT NOT NULL,
			registration_worktree_state TEXT NOT NULL CHECK (registration_worktree_state IN ('clean','dirty','unknown')),
			submitted_worktree_state TEXT NOT NULL DEFAULT '' CHECK (submitted_worktree_state IN ('','clean','dirty','unknown')),
			configuration_version INTEGER NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','passed','failed','error','cancelled','incomplete','unavailable')),
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
			log_digest TEXT NOT NULL DEFAULT '', job_id TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_attempts VALUES('d5373625a3610e1ae6fb36bf35640556','4a508090c5e8cadb91fc95ec47453622','alpha','0351dee0aee955aafa262b5b90593aa06d02dd97','clean','clean',1,'failed',3,1790336662418174000,1790336662427716000,9,'2 checks: 1 failed, 1 passed','host','inherited','ee6b4b90750b567fc321c3433771abe2',60000,65536,'d5373625a3610e1ae6fb36bf35640556',1792928662,0,'',1790336662,1,'','20ab005e2ee4cf3aa86c4901150c08eb04460dbc762c7fdaec27357b36956761','3338941c586237f6fa6219f9c630ea753db2a6f797b8387f4a01f5646650110b','a07dfd076110ed77ff12fec91843d088b42eb743a40f0de38ec4cac25281a472',0,0,'a07dfd076110ed77ff12fec91843d088b42eb743a40f0de38ec4cac25281a472','040b7aea0e70869a8656a877092d67e6');
CREATE TABLE check_results (
			attempt_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			command TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('passed','failed','error','cancelled','incomplete','unavailable')),
			exit_code INTEGER,
			duration_ms INTEGER NOT NULL,
			output_excerpt TEXT NOT NULL,
			truncated INTEGER NOT NULL DEFAULT 0,
			cleanup_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (attempt_id, position),
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		);
INSERT INTO check_results VALUES('d5373625a3610e1ae6fb36bf35640556',0,'passes','echo synthetic-pass','passed',0,3,unistr('synthetic-pass\u000a'),0,'');
INSERT INTO check_results VALUES('d5373625a3610e1ae6fb36bf35640556',1,'fails','echo synthetic-fail; exit 3','failed',3,3,unistr('synthetic-fail\u000a'),0,'');
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
CREATE TABLE IF NOT EXISTS "pull_request_reviews" (
			repository_id TEXT NOT NULL,
			pull_request_number INTEGER NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			source_oid TEXT NOT NULL,
			target_oid TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','changes_requested','skipped','not_requested')),
			reviewer_label TEXT NOT NULL,
			provenance TEXT NOT NULL,
			created_at INTEGER NOT NULL, review_event_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (repository_id, pull_request_number, sequence),
			FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
		);
INSERT INTO pull_request_reviews VALUES('alpha',1,1,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8','not_requested','','default',1790336659,'');
INSERT INTO pull_request_reviews VALUES('beta',1,1,'d82cd795c951b62feb478490384ee18a23d75bb4','f0bc8df7c0db4ef670b68b553725f921d317fc6d','not_requested','','default',1790336660,'');
INSERT INTO pull_request_reviews VALUES('gamma',1,1,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5','not_requested','','default',1790336660,'');
INSERT INTO pull_request_reviews VALUES('alpha',1,2,'ff3b89a747a7d6f3bc54451d8b14a64c59c7f532','9125f163fc4e8925cf4d2002d4aae7ce351048a8','pending','','review_request',1790336661,'');
INSERT INTO pull_request_reviews VALUES('gamma',1,2,'6f8fc60fba165f813b9a4234c1b3bf481da958c6','1c186762873df05433818452355281625c0419a5','skipped','','explicit_skip',1790336661,'');
CREATE TABLE check_raw_logs (
			attempt_id TEXT PRIMARY KEY,
			content BLOB NOT NULL CHECK (length(content) <= 262144),
			expires_at INTEGER NOT NULL,
			FOREIGN KEY (attempt_id) REFERENCES check_attempts(id) ON DELETE CASCADE
		) WITHOUT ROWID;
INSERT INTO check_raw_logs VALUES('d5373625a3610e1ae6fb36bf35640556',x'5b7061737365645d206563686f2073796e7468657469632d706173730a73796e7468657469632d706173730a0a5b6661696c65645d206563686f2073796e7468657469632d6661696c3b206578697420330a73796e7468657469632d6661696c0a0a',1792928662);
CREATE TABLE direct_review_credentials (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL UNIQUE,
			label TEXT NOT NULL CHECK (length(CAST(label AS BLOB)) BETWEEN 1 AND 100),
			value TEXT NOT NULL CHECK (length(CAST(value AS BLOB)) BETWEEN 1 AND 16384),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
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
			updated_at INTEGER NOT NULL, execution_json TEXT NOT NULL DEFAULT '{"legacy":true}' CHECK (length(CAST(execution_json AS BLOB)) BETWEEN 2 AND 4096),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_policies VALUES('alpha',1,'3203dccbc95ba6960900062cd8ac6aa54e60f12033a12c177c6483382e1c29de','host','["pull_request","push"]',600000,1048576,32,1,60000,1,'3203dccbc95ba6960900062cd8ac6aa54e60f12033a12c177c6483382e1c29de',1,0,'ee6b4b90750b567fc321c3433771abe2',1790336661,1790336661,'{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}');
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
CREATE TABLE check_observations (
			repository_id TEXT NOT NULL,
			ref_name TEXT NOT NULL CHECK (length(CAST(ref_name AS BLOB)) BETWEEN 1 AND 500),
			oid TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			PRIMARY KEY (repository_id,ref_name),
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
		);
INSERT INTO check_observations VALUES('alpha','refs/heads/feature','ff3b89a747a7d6f3bc54451d8b14a64c59c7f532',1790336662449344000);
INSERT INTO check_observations VALUES('alpha','refs/heads/main','0351dee0aee955aafa262b5b90593aa06d02dd97',1790336662449619000);
CREATE TABLE check_jobs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('push','pull_request')),
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
			status TEXT NOT NULL CHECK (status IN ('pending','claimed','started','passed','failed','error','cancelled','incomplete','unavailable','ambiguous','interrupted')),
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
			FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		);
INSERT INTO check_jobs VALUES('040b7aea0e70869a8656a877092d67e6','alpha','4a508090c5e8cadb91fc95ec47453622','push','refs/heads/main@0351dee0aee955aafa262b5b90593aa06d02dd97','0351dee0aee955aafa262b5b90593aa06d02dd97','',0,'main','.owngit/checks.json','e7dce93327dcae92579d485f1bc1e1d16dded94f','270f982dd082f2bd5042b778e4046f9dacb2fb33b1fd2c3dbda48a5a948cf299',1,'host',1,1,'{"timeout_ms":60000,"output_limit_bytes":65536}','{"source":{"max_entries":20000,"max_file_bytes":67108864,"max_total_bytes":268435456,"max_path_depth":64,"max_path_bytes":1024,"max_name_bytes":255,"metadata_limit_bytes":16777216}}','f72d499deb3cfe2c87848d1b7d932b97d9358f964dc0f493afbc66e8d496d4be','',0,'failed','d5373625a3610e1ae6fb36bf35640556','645fec37c090873fc1f75cc98c4aad25',1790336722352495000,'ee6b4b90750b567fc321c3433771abe2',1,'server','host',1790336662222918000,1790336662352495000,1790336662418174000,1790336662427716000,NULL,NULL,NULL,'2 checks: 1 failed, 1 passed');
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
CREATE TABLE import_schedules (
			repository_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
			interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 60 AND 604800),
			last_started_at INTEGER,
			last_finished_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
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
CREATE UNIQUE INDEX helper_credentials_creation ON helper_credentials(repository_id, creation_id) WHERE creation_id != '';
CREATE INDEX check_attempts_revision ON check_attempts(repository_id, revision_oid, sequence);
CREATE INDEX check_attempts_task ON check_attempts(task_id, sequence);
CREATE INDEX check_attempts_pending ON check_attempts(task_id, status, sequence);
CREATE INDEX check_cycles_task ON check_cycles(task_id, sequence);
CREATE INDEX check_raw_logs_expiry ON check_raw_logs(expires_at,attempt_id);
CREATE UNIQUE INDEX pull_request_reviews_event ON pull_request_reviews(repository_id,review_event_id) WHERE review_event_id != '';
CREATE INDEX direct_review_probes_repository ON direct_review_probes(repository_id,created_at,request_id);
CREATE INDEX direct_review_task_contexts_task ON direct_review_task_contexts(repository_id,task_id,created_at,context_id);
CREATE UNIQUE INDEX direct_review_requests_source_event ON direct_review_requests(repository_id,trigger_kind,source_event_key) WHERE source_event_key != '';
CREATE INDEX direct_review_requests_pull_request ON direct_review_requests(repository_id,pull_request_number,sequence) WHERE pull_request_number > 0;
CREATE INDEX direct_review_requests_task ON direct_review_requests(repository_id,task_id,sequence) WHERE task_id != '';
CREATE INDEX check_attempts_job ON check_attempts(job_id) WHERE job_id != '';
CREATE UNIQUE INDEX check_runner_credentials_creation ON check_runner_credentials(repository_id,creation_id) WHERE creation_id != '';
CREATE UNIQUE INDEX check_runner_credentials_hash ON check_runner_credentials(repository_id,token_hash) WHERE revoked_at IS NULL;
CREATE INDEX check_observations_recent ON check_observations(repository_id,observed_at,ref_name);
CREATE UNIQUE INDEX check_jobs_dedup ON check_jobs(repository_id,dedup_digest);
CREATE INDEX check_jobs_queue ON check_jobs(repository_id,status,admitted_at,id);
CREATE INDEX check_jobs_attempt ON check_jobs(attempt_id) WHERE attempt_id != '';
CREATE INDEX import_runs_repository ON import_runs(repository_id,started_at,id);
CREATE INDEX import_ref_observations_recent ON import_ref_observations(repository_id,observed_at,ref_name);
CREATE INDEX import_stagings_run ON import_stagings(run_id);
CREATE INDEX import_schedules_due ON import_schedules(enabled,last_started_at,repository_id);
CREATE INDEX import_initial_destinations_run ON import_initial_destinations(run_id);
CREATE INDEX import_publication_intents_repository ON import_publication_intents(repository_id,created_at,id);
COMMIT;

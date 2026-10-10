# Released and candidate state databases

SQL dumps (`sqlite3 owngit.sqlite .dump`) of stopped states, used by `TestReleasedDatabasesUpgradeOnce`. The schema-14, schema-15 and schema-16 dumps come from released OwnGit builds; the schema-17 dump comes from a 1.1.8 preparation candidate, not a published release artifact. Each dump preserves the catalog text its originating build stored, and all data is synthetic.

The schema-14 and schema-15 dumps use the 1.0.1 and 1.0.2 captures of the shared OwnGit test data set or the committed-baseline test fixture. Each released dump's `repository_root` was replaced with `/home/example/repositories` before dumping.

| File | Schema | Made by |
| --- | --- | --- |
| `schema14-1.0.1.sql` | 14 | the 1.0.1 capture's stopped state, written by OwnGit 1.0.1 |
| `schema14-1.0.2.sql` | 14 | the 1.0.2 capture's stopped state, written by OwnGit 1.0.2 |
| `schema15-1.0.3.sql` | 15 | OwnGit 1.0.3 `restore` of the 1.0.2 capture's offline backup (a database 1.0.3 created) |
| `schema15-1.0.3-upgraded-from-1.0.2.sql` | 15 | OwnGit 1.0.3 `backup` of the 1.0.2 stopped state, which upgraded it from schema 14 |
| `schema15-baseline-upgraded-by-1.0.3.sql` | 15 | OwnGit 1.0.3 `backup` of a committed-baseline state (`testfixture.CreateCommittedBaselineState`), which upgraded it from no schema version |
| `schema15-1.1.2.sql` | 15 | OwnGit 1.1.2 `restore` of the 1.0.2 capture's offline backup (a database 1.1.2 created) |
| `schema15-1.1.2-upgraded-from-1.0.2.sql` | 15 | OwnGit 1.1.2 `backup` of the 1.0.2 stopped state, which upgraded it from schema 14 |
| `schema16-1.1.6-populated.sql` | 16 | OwnGit 1.1.6 upgraded a synthetic schema-15 catalog; released state APIs populated and validated check evidence |
| `schema17-1.1.8-candidate.sql` | 17 | A Go test binary built from committed 1.1.8 preparation source upgraded the released synthetic schema-16 fixture; state APIs populated workflow and output-limit evidence before the store was closed and dumped |

For `schema16-1.1.6-populated.sql`, the released OwnGit 1.1.6 executable upgraded the catalog from schema 15. State APIs built from the same released source wrote and validated synthetic JSON jobs with v1 and v2 digests, active consent, completed attempts and results, raw logs, and runtime ownership. The stopped database was then dumped with `sqlite3`. No container was run, and no private repository data is included.

For `schema17-1.1.8-candidate.sql`, the candidate opened `schema16-1.1.6-populated.sql` and upgraded its catalog to schema 17. State APIs added an enabled external-runner policy with workflow consent, a pull-request workflow run linked to a plan-backed job, and a separate incomplete result with `output_limit_exceeded_bytes=4096` and `truncated=true`. The capture checked the run, job, result and recovery records, closed the store, then dumped the stopped database with SQLite 3.46.1. No SQL catalog or data was edited after the dump. The example repository root was inherited from the sanitized schema-16 fixture; no path replacement was performed on the candidate dump.

The test matrix checks that this candidate opens twice without migration, preserves row counts and the expected catalog fingerprint, passes recovery validation, retains the workflow and output-limit records, and has no foreign-key violations. This is committed-source candidate coverage, not evidence from a published 1.1.8 executable.

A new schema step does not change these files. Add a dump when a release writes a new schema, and label any preparation candidate separately.

# Released state databases

SQL dumps (`sqlite3 owngit.sqlite .dump`) of stopped states that released OwnGit builds wrote, used by `TestReleasedDatabasesUpgradeOnce`. The catalog text in each dump is the text those releases stored. All data is synthetic. The schema-14 and schema-15 dumps use the 1.0.1 and 1.0.2 captures of the shared OwnGit test data set or the committed-baseline test fixture. Each dump's `repository_root` was replaced with `/home/example/repositories` before dumping.

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

For `schema16-1.1.6-populated.sql`, the released OwnGit 1.1.6 executable upgraded the catalog from schema 15. State APIs built from the same released source wrote and validated synthetic JSON jobs with v1 and v2 digests, active consent, completed attempts and results, raw logs, and runtime ownership. The stopped database was then dumped with `sqlite3`. No container was run, and no private repository data is included.

A new schema step does not change these files. Add a dump when a release writes a new schema.

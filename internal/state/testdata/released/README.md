# Released state databases

SQL dumps (`sqlite3 owngit.sqlite .dump`) of stopped states that released OwnGit builds wrote, used by `TestReleasedDatabasesUpgradeOnce`. The catalog text in each dump is the text those releases stored. All data is synthetic, from the 1.0.1 and 1.0.2 captures of the shared OwnGit test data set or the committed-baseline test fixture; each dump's `repository_root` was replaced with `/home/example/repositories` before dumping.

| File | Schema | Made by |
| --- | --- | --- |
| `schema14-1.0.1.sql` | 14 | the 1.0.1 capture's stopped state, written by OwnGit 1.0.1 |
| `schema14-1.0.2.sql` | 14 | the 1.0.2 capture's stopped state, written by OwnGit 1.0.2 |
| `schema15-1.0.3.sql` | 15 | OwnGit 1.0.3 `restore` of the 1.0.2 capture's offline backup (a database 1.0.3 created) |
| `schema15-1.0.3-upgraded-from-1.0.2.sql` | 15 | OwnGit 1.0.3 `backup` of the 1.0.2 stopped state, which upgraded it from schema 14 |
| `schema15-baseline-upgraded-by-1.0.3.sql` | 15 | OwnGit 1.0.3 `backup` of a committed-baseline state (`testfixture.CreateCommittedBaselineState`), which upgraded it from no schema version |
| `schema15-1.1.2.sql` | 15 | OwnGit 1.1.2 `restore` of the 1.0.2 capture's offline backup (a database 1.1.2 created) |
| `schema15-1.1.2-upgraded-from-1.0.2.sql` | 15 | OwnGit 1.1.2 `backup` of the 1.0.2 stopped state, which upgraded it from schema 14 |

A new schema step does not change these files. Add a dump when a release writes a new schema.

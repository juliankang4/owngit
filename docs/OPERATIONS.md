# Operations

## First-time setup

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

On first run, OwnGit writes an owner-readable setup file inside the state directory. Open that file in the installation owner's browser. The setup secret is not printed or passed in a browser command argument.

The default address is `http://127.0.0.1:7654`. Setup configures repository storage, optional shared-password protection for general access, and a separate administrator password. Every later security-setting change asks for the current administrator password. Setup finishes at an empty dashboard, where New repository creates a repository. Its clone address has the form `http://HOST:7654/git/PROJECT.git`.

## Reaching the server from another device

OwnGit serves plain HTTP, so the connection is not encrypted, and it has no built-in TLS. Use Tailscale or your own VPN to reach its private-network address. A Tailscale-related name alone does not prove that the whole path is protected. Ordinary LAN HTTP also works: OwnGit shows a one-time warning before it accepts passwords, and the interface keeps the connection status visible. Do not expose OwnGit to the public Internet.

To use a LAN name:

```sh
./bin/owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

The server accepts only requests whose Host is `localhost`, `127.0.0.1`, `::1`, or an approved name. `--allowed-host` is repeatable. To approve another name permanently, run this on the installation host and restart the server:

```sh
./bin/owngit approve-host gitbox.internal
```

## Host-owner recovery

Before setup is complete, issue a replacement setup link with:

```sh
./bin/owngit setup-link --base-url http://127.0.0.1:7654 --no-open
```

To reset a forgotten administrator password, put the new password in an owner-readable file:

```sh
./bin/owngit reset-admin --password-file /path/to/owner-only-password-file
```

The password file must be a regular file. On Unix-like systems, it must not be readable by group or other users. OwnGit never accepts a password as a command-line value. Resetting the administrator password signs out administrator sessions and leaves repositories unchanged.

OwnGit has no email or account recovery. Both procedures require access to the installation host.

## Restoring repository files

Open Restore from a repository, commit, or file page. Choose a source commit and target branch, then preview the complete list of additions, changes, and deletions. OwnGit applies the reviewed tree only if the target branch still has the previewed tip.

An existing branch receives a new commit whose parent is its previous tip. A deleted branch is recreated at the selected commit. Selected-file restore keeps unselected files, file modes, binary files, and symbolic links as they are, and never follows links on the host. OwnGit refuses to restore a selected submodule, or a path whose replacement would remove unselected files beneath it.

Restore changes Git-tracked content in OwnGit only. It does not touch another computer's working tree or its uncommitted files.

## Changing the default branch

The default branch is the branch that OwnGit and `git clone` open first (the repository's `HEAD`). An imported repository whose only branch is `master` shows no default branch until you choose one. An administrator picks any existing branch in the repository's Settings tab. Changing it creates no branch and leaves every ref and retained history as it was.

## Deleting a repository

Deleting a repository removes it from OwnGit together with its pull requests, reviews, tasks, check settings, jobs and results, runner and helper credentials, import settings, run history and stored import credentials. Queued check jobs are dropped. An administrator deletes a repository with Delete repository, at the end of the repository's tabs, by typing its name and the administrator password. When the deletion finishes, the name is free for a new repository. You choose what happens to the files:

- Keep files moves the bare repository, unchanged, to `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git` inside the repository folder. `ID` is the repository name in lowercase, as in its Git URL, and the time is UTC; a number is added if that name is taken. Its branches, tags and retained history stay in that folder until you remove it yourself.
- Delete files deletes the bare repository, including its retained history.

OwnGit refuses to delete a repository while an import is running, while a check job is claimed or running, while a check container still waits for OwnGit to confirm its removal, or while another Git operation (a push, clone, restore or merge) still holds the repository after a short wait. Try again once it finishes. A container cleanup that failed is retried when OwnGit starts, so restart OwnGit after Docker is available again. If the server log says the job belongs to another Docker daemon (for example after Docker was reset or reinstalled), OwnGit cannot confirm the cleanup: remove any leftover `owngit-check-*` container yourself, and expect the repository to stay undeletable until the original Docker daemon is back, because this version has no command to release that record.

OwnGit records the deletion in its state database before it moves or deletes the files. If OwnGit stops partway, or the files cannot be moved or deleted, the deletion reports that its files are not finished. The repository is already gone from the dashboard and from Git URLs, and creating or importing a repository with the same name reports that the name is in use. The files stay at `ID.git`, at their kept-folder path, or under a temporary `.owngit-delete-*` name. The next start of OwnGit finishes the move or deletion and frees the name; if it cannot, the reason is in the server log and the files stay where they are.

While a deletion is unfinished, the repository folder also holds a small `.owngit-deletion-ID` file with a random token for that deletion. It shows OwnGit that the folder is the storage the deletion began on. If the file is missing or holds another token at startup, for example because the storage is not mounted or an older copy of it is mounted, OwnGit keeps the deletion recorded, logs that the storage may be unavailable, and tries again at the next start. Do not remove this file while a deletion is unfinished. If you removed it by hand while the correct storage was mounted, recreate it in the repository folder with the `token ...` line quoted in the server log, then restart OwnGit. A leftover file after a finished deletion is harmless. OwnGit never follows a symbolic link while deleting and never removes anything outside that repository's directory. A Git request that was already waiting when the deletion started fails afterwards.

Earlier backups still contain a deleted repository, and the database space its records used is freed but not securely erased. Folders under `.owngit-removed` are never listed as repositories and are not included in backups.

After a Keep files deletion, the dashboard shows the kept folder and this command once. To bring back a kept repository, create a new empty repository in the dashboard, then push the branches and tags from the kept folder:

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

Retained history is not transferred. Commits that only retained history holds stay in the kept folder, and the new repository starts its own retained history. Pull requests, checks and other records do not come back either. If the kept repository's main branch is not `main`, change the default branch afterwards.

## Moving an existing repository into OwnGit

Create an empty repository in the dashboard. From a clone of the existing repository, add OwnGit as a remote and push branches and tags:

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

OwnGit accepts pushes only to branches (`refs/heads/*`) and tags (`refs/tags/*`). A `git push --mirror` from a mirror clone of another host therefore fails for other refs, such as `refs/pull/*`.

Compare the branch and tag refs before you treat the move as complete:

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

Pushing between two OwnGit installations does not carry retained history or repository records. Use an offline backup when those must move too. To keep pulling changes from a host that stays in use, see [Importing from another Git host](#importing-from-another-git-host).

## Keeping a copy on another host

OwnGit does not push to other hosts itself, but ordinary Git can keep a copy elsewhere.

To copy every branch and tag from OwnGit to another host, work from a mirror clone:

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

To update the copy later, run `git fetch --prune` and `git push --mirror` again in the same directory. `--mirror` makes the other host match the copy exactly: it overwrites refs there and deletes refs that the copy does not have. OwnGit does not share its retained history, so that history stays in OwnGit.

To update both hosts with every push from a working clone, give its remote two push URLs:

```sh
git remote set-url --add --push origin http://HOST:7654/git/PROJECT.git
git remote set-url --add --push origin https://git.example.test/team/project.git
```

Once a remote has a push URL, Git pushes only to its push URLs, so list OwnGit as well. Fetches still use the original URL. Git pushes to each URL in turn, and a rejection by one host does not undo the push to the other.

## Importing from another Git host

An import copies a repository from another Git host over HTTPS into a new OwnGit repository and can refresh it later. Imports are inbound only: OwnGit never writes to the source. Git LFS objects are not fetched or hosted.

Each import has a mode. In `standalone` mode, OwnGit becomes the primary copy. In `coexistence` mode, the other host stays authoritative and OwnGit keeps a refreshed copy.

In the browser, the administrator uses Import a repository on the dashboard to start an import, and the repository's Import tab to change its source and credentials, refresh, cancel, view history, and set a schedule. Every change asks for the current administrator password. Saving the credential form never clears a stored credential; only Clear credentials removes it. The browser form is limited to 1 MiB in total, so store a CA bundle close to that size with the command line.

The same operations are available from the command line. Import commands read the administrator password from a file with the same checks as `reset-admin`, and read a source token or Basic credential from a private file or an interactive prompt. They never accept a secret as an argument or environment variable.

```sh
./bin/owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

Every import command takes the same `--server`, `--accept-insecure-http`, and `--password-file` flags; they are omitted below:

```sh
./bin/owngit import refresh PROJECT
./bin/owngit import status PROJECT
./bin/owngit import history PROJECT --limit 20
./bin/owngit import cancel PROJECT
./bin/owngit import schedule PROJECT --enable --interval 6h
./bin/owngit import schedule PROJECT --disable
./bin/owngit import credentials PROJECT --token-file /path/to/owner-only-token
./bin/owngit import credentials PROJECT --ca-file /path/to/source-ca.pem
./bin/owngit import credentials PROJECT --clear
./bin/owngit import resolve PROJECT
```

- `--basic-file` replaces `--token-file` for a Basic credential; the file holds the username and password on separate lines. `--ca-file` alone stores only a source certificate authority, up to 1 MiB. `--clear` removes the stored credential and CA.
- `--allow-private-network` permits a source on a private LAN, CGNAT or Tailnet, or loopback address.
- `--git-only-consent` accepts a repository with Git LFS pointers; see [Git LFS](#git-lfs).
- `--accept-insecure-http` consents to reaching OwnGit over plain HTTP for that command only. The import source itself must use HTTPS.
- Output shows the credential type and whether one is stored, never the token, password, or CA.
- `import add` and `import refresh` wait for the whole run, up to about 62 minutes by default.
- A schedule interval is between 60 seconds and 7 days. Scheduled refreshes run only while `owngit serve` is running.

### Source connections

The source URL must use HTTPS with TLS 1.2 or newer and must not contain a username, password, query, or fragment. Hostnames must be ASCII, and IPv6 zone identifiers are not supported. OwnGit does not follow redirects and ignores proxy environment variables, cookies, and Git credential helpers.

OwnGit resolves the hostname once and checks every returned address before it connects. Public addresses are allowed. Private LAN, CGNAT, Tailnet, and loopback addresses need `--allow-private-network`, including when a DNS answer mixes public and private addresses. Other special-purpose addresses are always refused. A custom CA adds to the system roots and never disables certificate or hostname checks.

### What an import publishes

Each run fetches a full copy into a private staging area and checks it before anything reaches the repository: every advertised branch, tag, and HEAD must be present with the advertised object and a complete object graph. A new repository appears only when it is complete.

OwnGit publishes only branches and tags. Other refs, such as notes, replace refs, and pull request refs, are skipped. A source whose HEAD points outside `refs/heads/` is refused.

A refresh never overwrites local work. For each ref:

- a missing ref is created, and an identical ref is left alone;
- a branch follows the source only when it still holds the value OwnGit last saw from this source URL, or when it only moved forward from that value and the new source value includes it;
- a tag changes only when it is still the exact tag OwnGit last saw;
- anything else is divergent: the local ref is kept and the run reports it.

A source branch or tag whose name differs only by case from an existing local ref is not created and is reported as divergent; rename or remove one of the two if you want the source ref imported.

A branch or tag deleted at the source is never removed locally. Every replaced value is kept in retained history. After you change the source URL, OwnGit has not yet seen the new source's refs, so refs that differ are reported as divergent instead of being replaced.

A refresh changes the repository's HEAD only when OwnGit set that HEAD on an earlier import from the same source and nothing changed it since. Otherwise HEAD stays as it is and is reported as divergent.

Repository hooks and configuration are not copied. A source with a different object format (SHA-1 or SHA-256) than the repository fails, and ref names that differ only by case are refused. A source whose branch, tag, or HEAD target name is longer than 417 bytes fails with `unsupported_refs` before anything is published; shorten that name at the source to import it.

### Git LFS

OwnGit scans the fetched objects for Git LFS pointer files, up to 200,000 objects, 100,000 candidate files, and 32 MiB of candidate content. If it finds a pointer, or cannot finish the scan within those limits, the run stops with `git_lfs_required`. With Git-only consent, the import proceeds, the pointer files are kept as they are, and the status says the content is incomplete. LFS objects themselves are never downloaded. OwnGit does not read `.gitattributes`, so a clean scan does not prove that a repository does not use LFS.

### Failures and cancellation

Only one run per repository is active at a time; another request returns `busy`. A run is limited to 60 minutes by default. A cancelled run is recorded as `cancelled`, and a run that reaches its time limit as `limit`. Other conflicts return `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved`, or `nothing_to_resolve`.

When `owngit serve` stops, it cancels running imports and waits up to 45 seconds for each to record its outcome. At the next start, OwnGit marks interrupted runs, checks any publication that was in progress against the repository, and records what it finds. It never repeats or rolls back a write. If the import service cannot start, the Import page and `import status` say so, and ordinary Git service continues.

### Unresolved publications

A publication is unresolved when OwnGit cannot prove how it ended, for example when refs were written and HEAD was not. Refreshes are refused until the owner accepts the repository as it is:

1. Check the repository's branches, tags, and HEAD, and the reason on the last run. Fix anything you do not want to keep with ordinary Git operations.
2. Make sure no import is running. Resolution is refused with `busy` while a run or another Git operation holds the repository, and with `nothing_to_resolve` when nothing is unresolved.
3. Resolve with `owngit import resolve PROJECT` or the button on the Import tab. OwnGit records the current refs and HEAD as the accepted state. It writes nothing to Git and does not change the earlier run's history.
4. Refresh. Refs that match the source stay, refs that still hold the last confirmed source value follow the source, and anything else stays divergent.

If an initial import is unresolved and its repository does not exist yet, `import resolve` refuses it. Restart OwnGit. If the problem remains, move that import's `.owngit-create-*` directory out of the repository folder and restart again.

## Command-line pull requests

A normal push does not create a pull request. After pushing distinct source and target branches, create one. `--review` is optional:

```sh
./bin/owngit pr create \
  --server http://HOST:7654 \
  --accept-insecure-http \
  --repository PROJECT \
  --source feature-branch \
  --target main \
  --title "Describe the change" \
  --review request \
  --password-file /path/to/owner-only-shared-password-file
```

Use `--review skip` when you intentionally omit review. A skip is recorded as skipped, not approved. Without `--review`, no review is requested, and `pr review request` can still be run later. Omit `--password-file` when general access is open. The file contains the shared general-access password, never the administrator password, and has the same owner-only checks as `reset-admin`.

Plain HTTP exposes the password and pull request details to the network. `--accept-insecure-http` records your consent for that command only; omit it for HTTPS. The CLI rejects credentials embedded in the URL and does not follow redirects.

The other `pr` commands take the same `--server`, `--accept-insecure-http`, `--repository`, and `--password-file` flags; they are omitted below. `pr show` reports the current source and target object IDs, and every review decision and merge must supply both:

```sh
./bin/owngit pr list
./bin/owngit pr show --number 1
./bin/owngit pr review request --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
./bin/owngit pr review submit --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID \
  --decision approved --reviewer "existing-tool: reviewer label"
./bin/owngit pr review skip --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
./bin/owngit pr merge --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
```

A submitted review is `approved` or `changes_requested`. The reviewer label records who supplied the review; it does not claim independence or that checks ran. A pending or changes-requested review does not hold a merge. When the source or target moves, earlier review and skip decisions no longer apply, so inspect the pull request again and decide for the new object IDs.

Every command writes a JSON result. Failures include a stable `error.code` and a nonzero exit status. `checks` in a pull request result reports the evidence recorded for the current source revision, or `absent`. A failed, stale, dirty, or incomplete check is advisory and never blocks a merge.

Merge makes a fast-forward or a new merge commit with the old target as first parent and the source as second parent, authored as `OwnGit <owngit@localhost>` with the pull request number and title in the message. It does not squash, rebase, force-update, delete the source branch, or change anyone's working tree. Merge requires Git 2.38 or newer on the OwnGit host; with an older Git it returns `unsupported_git`, and other Git use keeps working. A retried or interrupted merge never creates a second merge commit.

## Project checks

OwnGit records manual helper checks and runs owner-enabled automatic checks.

A manual helper runs in your working environment and uploads evidence tied to a revision. It inherits your environment and permissions, so it is not a sandbox: a check can read files and credentials your account can reach. OwnGit records the worktree state with every attempt and never reports a dirty or unknown worktree as a tested commit.

Automatic checks run as the OwnGit account, in a restricted local Docker container, or on a separately connected runner. They need a committed `.owngit/checks.json`, an owner policy, and current consent. See [Automatic checks](AUTOMATIC_CHECKS.md).

Create a repository-scoped helper credential with the administrator password. The token is written only to the owner-readable `--output` file and stored on the server only as a hash:

```sh
./bin/owngit helper-credential create \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --label laptop \
  --password-file /path/to/admin-password-file \
  --output ~/.owngit-helper-token
```

An existing file or symbolic link at `--output` is reported, not replaced. If creation or delivery fails, OwnGit leaves the output file in place instead of risking the removal of someone else's file; inspect and remove it before you retry. If the response is lost, the command revokes the new credential; if it cannot confirm the revoke, it prints the creation identity (never the token) so you can revoke it. `helper-credential list` and `helper-credential revoke --id ID` manage credentials, and a revoked token stops working immediately. An administrator can also issue and revoke helper credentials from the Helper credentials link on the repository's Checks tab. Issuing and revoking always ask for the administrator password, even in a signed-in browser.

Create a stable task, then run checks:

```sh
./bin/owngit check task new \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --title "Fix the failing build"

./bin/owngit check run \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --task TASK_ID --check "unit=go test ./..." --check "lint=go vet ./..."
```

[Coding tools](CODING_TOOLS.md) is the reference for these commands, correction rounds, result fields, and exit codes. On Windows, `cmd.exe` returns exit code 1 for an unknown command, so OwnGit records that result as `failed`.

Raw check logs are stored in `owngit.sqlite`, limited to 256 KiB each, and kept for 30 days by default. Task and attempt records stay after a log expires. Reading an expired log returns `log_expired`, and a log that is missing earlier returns `log_missing`. A log that fails its integrity check is refused, and a truncated log is reported as truncated. If the database is full or reports an I/O error while storing a result, OwnGit stores the result without its raw log and records a log error on the attempt.

## Storage

- The state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available. It holds `owngit.sqlite` and, while the database is in use, its `-wal` and `-shm` files. Keep it on local storage, never on a network share used by other computers. Windows network (UNC) paths are refused.
- Choose the repository folder during setup. It can be on a separate disk or a mounted SMB or NFS share, with one OwnGit writer at a time. OwnGit leaves existing files in the folder alone and creates repositories there as bare repositories ending in `.git`.
- The activity graph and recent activity are counted in the background when the server starts, and again when a page is opened after a branch changes. Counts are reused until the branches change. On a slow share the dashboard can appear before counting finishes; it then says that some repositories are still being counted, and reloading shows the full count.
- A new repository is written under a temporary `.owngit-create-*` name and then renamed into place. On Windows, antivirus or search indexing can briefly lock the new directory. OwnGit retries for about 2 seconds; if the error persists, try again.
- A repository deleted with its files is first renamed to a temporary `.owngit-delete-*` name and then removed. Kept repositories go to the `.owngit-removed` folder. A `.owngit-deletion-*` file marks a deletion that is still unfinished. See [Deleting a repository](#deleting-a-repository).
- Repository names cannot end in `.git` or use Windows device names such as `CON`, `AUX`, `NUL`, `COM1`, or `LPT1`, with or without an extension. `new` and `new-import` are reserved. These rules apply on every platform.
- Expired logs free space inside the database for reuse, but the file does not shrink, the old bytes are not securely erased, and there is no overall size limit. OwnGit does not run `VACUUM`.
- When a `-wal` or `-shm` file is present at startup, OwnGit copies the database and its WAL to a private temporary directory to inspect them. The temporary volume needs about that much free space.
- On start, OwnGit upgrades a database from the earlier committed version in place. It refuses a database from a newer or unknown version, or from an unreleased development build, and leaves its files unchanged. An older build refuses a database that a newer build has upgraded, so back up before you replace the executable.
- OwnGit does not read or remove a `logs/` directory left by older versions. Remove it yourself once no older OwnGit process uses it.
- Removing the `owngit` executable leaves the state directory and repositories in place. Delete them yourself only when you no longer need them.
- A database from an unreleased development build that had built-in AI review may still hold review records and provider tokens. OwnGit does not use or erase them. Backup never copies the tokens, and it checks for review records and refuses to run while any remain.

## Offline backups

Retained history protects against force-pushes and deletions, but it is not a backup. A secret that was ever pushed stays visible in the browser and is included in every later backup, even after a force-push or branch deletion. Only [deleting the repository](#deleting-a-repository) with its files removes that history, and earlier backups still contain it. Rotate any secret you push by mistake. OwnGit does not schedule backups. Stop OwnGit before creating one. The output directory must not exist:

```sh
./bin/owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

A backup holds a manifest and one Git bundle per nonempty repository. It includes:

- every ref, including OwnGit's retained history, and each repository's HEAD and metadata;
- pull requests, reviews, merge records, tasks, check configurations, check results, and automatic-check policies and jobs;
- import sources, run history, and publication records;
- the access mode and password hashes.

Raw logs, credentials and tokens of every kind, import schedules, and consent are not included. Keep backups private, because password hashes are sensitive.

Backup refuses to run when an import publication is still unsettled for a repository that does not exist yet. Start and stop OwnGit once so it can settle the record, then back up again. If the error remains, move the import's `.owngit-create-*` directory out of the repository folder, then start and stop OwnGit again and back up.

The manifest is limited to 64 MiB. A backup that would exceed it fails without writing output and never drops records to fit. Creating a backup holds the whole export in memory.

OwnGit restores backup versions 1, 2, and 9 and refuses others, including the versions 3 through 8 that only unreleased development builds wrote. Older builds refuse a newer backup instead of dropping records they do not know. Restore into new paths that do not exist:

```sh
./bin/owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

Restore checks every bundle, ref, object, and record before it publishes the new state. The SHA-256 hashes detect corruption but cannot detect a backup that someone replaced along with its manifest.

After a restore:

- sign-in sessions, setup links, approved Hosts, credentials, schedules, and every consent are gone;
- create new helper and runner credentials, and store import credentials again before refreshing an import that needs them;
- automatic checks stay off until the owner enables them again, and unfinished check jobs are marked `interrupted` instead of rerunning;
- unsettled import publications are closed without being applied;
- raw logs are absent, so a log reads as missing until its expiry and expired afterward;
- start `owngit serve` with the restored state before you use it in other ways, so that startup can settle interrupted records.

Backup and restore keep Git filenames exactly. A name Git accepts, such as one containing a backslash, may not check out on Windows. Rename it from a compatible working tree, or inspect the repository with a bare clone.

If restore is interrupted, do not start OwnGit from either target, and do not remove a `.owngit-restore-pending` marker. Move both targets and any `TARGET.owngit-restore-...` siblings to a separate quarantine location without merging or overwriting anything, then restore again into new paths. If a backup stops before finishing, its output directory does not exist; once no backup process is running, keep or quarantine its hidden `.OUTPUT.owngit-backup-...` sibling.

# Operations

## First-time setup

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

On first run, OwnGit writes an owner-readable setup file inside the state directory. Open that file in the installation owner's browser. The setup secret is not printed or passed in a browser command argument.

The default address is `http://127.0.0.1:7654`. Setup configures repository storage, optional shared-password protection for general access, and a separate administrator password. Every later security-setting change requires entering the current administrator password. Setup finishes at an empty dashboard.

## Reaching the server from another device

OwnGit serves plain HTTP, so the connection is not encrypted. Use Tailscale or your own VPN to reach its private-network address. A Tailscale-related name alone does not prove that the whole path is protected. Ordinary LAN HTTP is supported after a one-time warning before passwords are accepted, and the interface keeps the connection status visible. OwnGit has no built-in TLS and does not support public Internet hosting.

To use a LAN name:

```sh
./bin/owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

`--allowed-host` is repeatable. The server accepts only requests whose Host matches an approved address. Approve another Host name from the installation host, then restart the server:

```sh
./bin/owngit approve-host gitbox.internal
```

## Host-owner recovery

Before setup is complete, issue a replacement setup link with:

```sh
./bin/owngit setup-link --base-url http://127.0.0.1:7654 --no-open
```

To reset a forgotten administrator password, provide it through an owner-readable file:

```sh
./bin/owngit reset-admin --password-file /path/to/owner-only-password-file
```

The password file must be a regular file. On Unix-like systems, it must not be readable by group or other users. OwnGit never accepts its contents as a command-line value. Resetting the administrator password revokes administrator sessions and leaves repositories unchanged.

OwnGit has no email or account recovery. Both recovery procedures require access to the installation host.

## Restoring repository files

Open Restore from a repository, commit, or file page. Choose a source commit and target branch, then preview the complete list of additions, changes, and deletions. OwnGit applies the reviewed tree only when the target branch still has the previewed tip.

An existing target receives a new commit whose parent is its previous tip. A deleted branch is recreated at the selected source commit. Selected-file restore preserves unselected files, file modes, binary blobs, and symbolic-link blobs without following links on the host. Selected submodules and path replacements that would remove unselected descendants are refused.

Browser restore changes Git-tracked content in OwnGit. It does not modify another computer's working tree or its uncommitted files.

## Importing an existing repository

Add the OwnGit repository as a remote, then send branches and tags as two separate operations:

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

Compare the final branch and tag refs before treating the import as complete:

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

This normal Git import does not carry OwnGit's hidden retention refs or repository metadata. Use an offline OwnGit backup when those records must move too.

## Command-line pull requests

A normal push does not create a pull request. After pushing distinct source and target branches, create one with an explicit review choice:

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

Use `--review skip` when review is intentionally omitted. A skip is recorded as skipped, not approved. Omit `--password-file` when general access is open. This file contains the shared general-access password, never the administrator password. It uses the same owner-only file checks as `reset-admin`. OwnGit does not accept a password in an argument, environment variable, JSON field, or interactive standard input.

Plain HTTP exposes the password and pull request metadata to the network path. `--accept-insecure-http` records informed consent for that command invocation. The CLI validates the server origin before reading the password file, rejects embedded URL credentials, and does not follow redirects. Omit this flag for HTTPS.

List or inspect pull requests with:

```sh
./bin/owngit pr list \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --password-file /path/to/password-file

./bin/owngit pr show \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --number 1 \
  --password-file /path/to/password-file
```

`show` reports the exact current source and target object IDs. Supply both IDs when recording a review decision or merging:

```sh
./bin/owngit pr review request \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --number 1 \
  --source-oid SOURCE_OID --target-oid TARGET_OID \
  --password-file /path/to/password-file

./bin/owngit pr review submit \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --number 1 \
  --source-oid SOURCE_OID --target-oid TARGET_OID \
  --decision approved --reviewer "existing-tool: reviewer label" \
  --password-file /path/to/password-file

./bin/owngit pr review skip \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --number 1 \
  --source-oid SOURCE_OID --target-oid TARGET_OID \
  --password-file /path/to/password-file

./bin/owngit pr merge \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --number 1 \
  --source-oid SOURCE_OID --target-oid TARGET_OID \
  --password-file /path/to/password-file
```

A submitted review accepts `approved` or `changes_requested`. Its reviewer label records supplied provenance. It does not claim reviewer independence or executed checks. A requested review remains pending until a result or explicit skip is recorded. Any source or target movement invalidates review and skip decisions for the older pair. Inspect the pull request again and make a decision for the new object IDs. `changes_requested` blocks merge until a fresh approval or explicit skip is recorded.

Check execution is not configured. JSON results report `checks.status` as `not_configured` and do not treat it as passed. Missing checks alone do not block this optional-review workflow. Every command writes a JSON result. Failures include a stable `error.code` and return a nonzero process status.

Merge supports a fast-forward or a new merge commit with the old target as first parent and the exact source as second parent. OwnGit writes merge commits as `OwnGit <owngit@localhost>` and includes the pull request number and title in the message. It does not squash, rebase, force-update, delete the source branch, or modify a user working tree. Merge requires Git 2.38 or newer. An older Git version returns `unsupported_git` for merge while ordinary Git storage remains available.

OwnGit records a durable merge intent before publication. One Git ref transaction verifies both branch revisions, updates the target from its expected old object ID, and creates a protected receipt. A retry reconciles a matching receipt instead of creating another merge commit.

## Storage and offline backups

- The host-local state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available. It contains `owngit.sqlite` and must not be placed on a network share opened by other computers.
- Setup lets you choose a repository folder, including one on a separate disk. A folder local to the OwnGit host may reside on NAS hardware. Mounted SMB and NFS repository folders have passed single-writer use, including retention, restart, and offline backup. Setup still warns when the folder appears to be on a network share. The state database always remains on host-local storage; the checks also kept credentials and backups local. The SMB and NFS checks did not cover power loss or concurrent writers.
- OwnGit leaves existing files in the chosen repository folder unchanged and creates repositories there as ordinary bare repositories ending in `.git`.
- Repository names cannot end in `.git` or use Windows device basenames such as `CON`, `AUX`, `NUL`, `COM1`, or `LPT1`, including those basenames before an extension. These portable rules apply on every platform.
- Retention refs preserve history replaced by force-push or deletion, but retention alone is not a backup.

Stop OwnGit before creating an offline backup. The output directory must not exist:

```sh
./bin/owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

The backup contains a versioned manifest and one Git bundle for each nonempty repository. Format version 2 records all refs, including hidden retention, pull request revision, provenance, and merge receipt refs. It also records each repository's HEAD and metadata, open and merged pull requests, review and skip decisions, revision bindings, merge intents, access mode, and password hashes. Empty repositories retain their metadata and unborn default branch. Keep the backup private because password hashes are sensitive.

Current OwnGit accepts validated version 1 and version 2 backups. Version 1 has no pull request metadata. A version 1 manifest that contains version 2 fields is rejected. Older OwnGit builds that only understand version 1 reject a version 2 backup instead of restoring repositories while dropping pull request records.

Restore into new paths that do not exist:

```sh
./bin/owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

Restore verifies fixed bundle paths, SHA-256 hashes, refs, objects, and pull request bindings before publishing the new state. The hashes detect corruption but do not authenticate a backup that an attacker has replaced together with its manifest. Restore rebuilds host-specific hooks, binds the database to the new repository root, and reconciles any merge receipt written before an interrupted SQLite update. Sessions, setup links, trusted Hosts, login-attempt state, and consent to insecure HTTP are not restored. The server must be started again with the restored state before use.

Backup and restore preserve Git filenames exactly. A name accepted by Git, such as one containing a literal backslash, may be impossible to materialize in a Windows working tree. Use a bare Git client to inspect such a repository, or rename the path from a compatible working tree before checking it out on Windows. Ordinary portable filenames can be checked out normally.

If restore reports an interruption or terminates without an error message, do not start OwnGit from either requested target and do not remove any `.owngit-restore-pending` marker. Preserve both requested targets and any sibling paths named like `TARGET.owngit-restore-...`. Move every existing artifact to a separate quarantine name without merging or overwriting anything, then retry with new, versioned target paths that do not exist. If backup terminates before publication, its requested output remains absent; after confirming that no backup process is running, preserve or quarantine its hidden `.OUTPUT.owngit-backup-...` sibling.

Automatic backup scheduling is not included.

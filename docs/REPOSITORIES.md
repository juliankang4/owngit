# Repositories and imports

This guide is for OwnGit administrators. It covers moving a repository in, protecting and restoring its history, renaming, sharing and deleting it, and importing a repository from another Git host.

Most tasks have a button in the dashboard and an `owngit` command. Commands that change a repository need the administrator password in an owner-only file, given with `--password-file` (see [Password and token files](OPERATIONS.md)), and the server with `--server`. Inside a clone of the repository, `--server` and `--repository` default to its `origin` remote, except for `owngit repo delete`.

## Repository names

The same rules apply when you create, import or rename a repository:

- 1 to 100 characters: ASCII letters, digits, `.`, `_` and `-`, starting with a letter or digit.
- Not ending in `.git`, and not a Windows device name such as `CON`, `NUL` or `COM1` before the first `.`.
- Not `new` or `new-import`.
- Not used by another repository in any letter case, including an earlier name that still redirects to it.

The address uses the name in lowercase, so `Tools-2` answers at `tools-2`.

## Move an existing repository into OwnGit

Create an empty repository in the dashboard, or with `owngit repo create --name PROJECT`. Then push from a clone of the existing repository and compare both sides:

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

A push may change branches and tags, plus any [other ref namespaces](#other-ref-namespaces) listed for the repository. OwnGit refuses every other ref, so `git push --mirror` from another host's mirror fails on refs such as `refs/pull/*`. It also refuses a branch or tag name that some file systems treat as the same as an existing one, such as `Main` beside `main`; Git shows the reason and the fix.

OwnGit checks every pushed object with Git's own object check. A push that carries a damaged or malformed object is refused, and Git prints the reason on `remote: error:` lines. Old zero-padded file modes and old date formats, which some long-lived projects carry in their history, stay warnings, so those repositories can still be pushed.

Pushing to another OwnGit server does not carry kept history, pull requests or checks. Use a backup for those ([Backups](BACKUPS.md)). To keep following a host that stays in use, [import](#import-from-another-git-host) instead.

## Keep a copy on another host

OwnGit never pushes to other hosts. Mirror the repository yourself:

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

To update the copy, run `git fetch --prune` and `git push --mirror` again in that folder. `--mirror` also deletes from the other host anything deleted here.

## Default branch and its protection

The default branch is the one `git clone` checks out. Change it on the repository's Settings tab, or run:

```sh
owngit repo default-branch --repository NAME --branch BRANCH
```

New repositories start on `main`. To use another name, such as `trunk`, set Initial branch on the Settings Repositories tab, or run `owngit settings set --initial-branch trunk`. Imports keep their source's default branch.

To refuse pushes that rewrite or delete the default branch, turn on "Protect the default branch" on the repository's Settings tab. It is off by default. On the command line:

```sh
owngit repo settings set --repository NAME --protect-default-branch on
```

Pushes that only add commits still work, and so do merges and file restores. An import that would rewrite a protected branch fails with `protected_default_branch` and changes nothing.

## Kept history and restoring files

When a force push, an import or a deletion replaces commits on a branch or tag, OwnGit keeps the old commits as kept history. You can browse them and restore from them. This is on by default.

To turn it off for every repository, use Kept history on the Settings Repositories tab, or `owngit settings set --kept-history off`. For one repository, use its Settings tab, or `owngit repo settings set --repository NAME --kept-history off` (`on`, `off`, or `default` to follow the server). Turning it off deletes nothing already kept, but commits replaced afterwards cannot be restored from kept history.

To bring back files, choose "Start a restore" on the repository's Overview, "Restore files from here" on a branch, tag or kept-history entry, or "Restore this file" on a file page. Pick a source commit and a target branch, check the preview, and apply. OwnGit adds a new commit to the target branch, or recreates a deleted branch. It never rewrites history and never touches anyone's working copy.

On the command line, preview first, then apply what you previewed:

```sh
owngit repo kept-history --repository NAME
owngit repo restore preview --repository NAME --source OID --target main
owngit repo restore apply --repository NAME --source OID --target main --expected-head OID
```

`--source` is a full commit ID. Add `--path FILE` once per file to restore only some files. Set `--expected-head` to the `expected_head` value from the preview. If the branch moved since the preview, apply fails with `stale_revision` and changes nothing; preview again.

## Other ref namespaces

By default a push may change only branches (`refs/heads/`) and tags (`refs/tags/`). To accept other refs in one repository, such as Git notes, list their namespaces under Other ref namespaces on its Settings tab, or run:

```sh
owngit repo settings set --repository NAME --extra-ref-prefixes refs/notes/,refs/meta/
```

Each namespace starts with `refs/` and ends with `/`, and a repository can list up to 32. An empty value removes them all. Refs in these namespaces have no kept history and no protection: a push can overwrite or delete them for good.

## Rename a repository

Rename on the repository's Settings tab, under Name and address, or run:

```sh
owngit repo rename NAME NEW-NAME
```

Everything in the repository stays. For 90 days the old address keeps working. Pages and the API redirect to the new address. Git is answered at the old address itself, so existing clones keep fetching and pushing, and a fetch or push there names the new address:

```text
remote: This repository moved to http://HOST:7654/git/NEW-NAME.git
```

Update each clone within those 90 days:

```sh
git remote set-url origin http://HOST:7654/git/NEW-NAME.git
```

After 90 days the old address answers 404, and another repository may take the old name. A clone still pointing there would then fetch from and push to that other repository.

- `owngit` commands and MCP tools do not follow the redirect. They stop with `repository_moved`; update `origin` or `--repository`.
- Runners and helpers keep working at the old address for the 90 days. Switch them to the new name before then, for example `owngit runner --repository NEW-NAME`.
- A rename is refused while the repository is busy (an import, a check, a push or a backup). Try again later.
- The new name follows the [repository name rules](#repository-names).

## Share links

A share link lets someone without an account read one repository. Create and revoke links under Share links on the repository's Settings tab. Each link has:

- a name that only administrators see;
- an access level: "Browse files and history", or "Browse, and clone with Git";
- an expiry: 1, 7, 30 (default) or 90 days, or "Until revoked";
- an optional extra password (8 to 1024 characters) that visitors must type.

OwnGit shows a new link's address, `https://HOST/share/SECRET`, only once. If you lose it, create a new link and revoke the old one. Revoking stops a link at once.

Visitors see the files, README, commits, branches and tags. They never see other repositories, pull requests, checks, settings or kept history, and they cannot change anything. A clone link gives a Git address `https://HOST/share/ID.git`. Git asks for a user name and password:

- without an extra password, enter any user name, and the part of the link after `/share/` as the password;
- with an extra password, enter the part after `/share/` as the user name, and the extra password as the password.

The Git address offers the repository's HEAD only when HEAD names the tip of a branch or tag (for an annotated tag, the commit it points to). Otherwise a clone may not check out any files; clone with `--branch BRANCH`, or check out a branch afterwards. If OwnGit cannot check HEAD, the address answers HTTP 503; try again.

Treat a link like a password. Revoking a link does not take back a clone someone already made. A reverse proxy in front of OwnGit may log the first request, which carries the secret, so check its access log. Share links are not in backups; create new ones after a restore.

On the command line:

```sh
owngit repo share list   --repository NAME
owngit repo share create --repository NAME --label "Reviewer" --scope clone --days 7
owngit repo share revoke --repository NAME --id ID
```

`create` also takes `--until-revoked` and `--link-password-file PATH` for the extra password. It prints the secret once.

### A public address for share links

Keep OwnGit itself private. You can still give share links a second, public address, for example through Tailscale Funnel. That address serves only share link pages and clones, and answers `404 page not found` to everything else.

It is off by default. Pick a free local port and the address visitors use, then save both on the Settings Network tab, under Public address for share links, or run:

```sh
owngit network set --public-share-listen 127.0.0.1:7655 --public-share-url https://box.tail1234.ts.net:8443
```

Restart OwnGit to apply it. Then point the tunnel at the port yourself:

```sh
tailscale funnel --bg --https=8443 http://127.0.0.1:7655
```

Add the tunnel's address as a trusted proxy (`127.0.0.1` for Funnel on this computer), or every visitor counts as one address for wrong extra passwords. Anyone on the Internet can then reach that address, and anyone with a link can use it until it expires or you revoke it. Turn it off with `owngit network set --public-share-off`.

## Delete a repository

Choose Delete repository, at the end of the repository's tabs, then pick what happens to the files:

- "Remove from OwnGit and keep the files" moves the bare repository to `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git` in the repository folder (`ID` is the lowercase name the repository was first created with). It stays there until you remove it. It is not in backups.
- "Delete the files too" deletes it, with its kept history. Older backups still contain it.

Either way, OwnGit deletes the repository's pull requests, checks, credentials, share links and import settings, and the name becomes free.

On the command line, `--repository` is always required:

```sh
owngit repo delete --repository NAME --files keep --confirm-name NAME
```

The page asks you to type the name. To turn that off, choose "Do not ask" under "Deleting a repository" on the Settings Repositories tab, or run `owngit settings set --delete-requires-name off`.

To bring back a kept repository, create an empty repository with that name and push the kept folder into it on the OwnGit computer:

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

Only branches and tags come back. Kept history, pull requests and checks do not.

### When the repository's files are missing

If a repository's `ID.git` folder has disappeared from the repository folder, you can still delete the repository with either choice. OwnGit removes only its own records, and the name becomes free. The page says "The repository folder was already missing. Only its OwnGit records were removed." Nothing is moved to `.owngit-removed`. Once OwnGit has recorded that the folder is missing, it never moves or deletes a folder that later appears at that path, even one that appears before the deletion finishes or before a restart finishes it. It does not register that folder as a repository either. While the folder is there, OwnGit cannot create a repository with the original name. To recover its branches and tags, create an empty repository with another name and push from the folder as shown above. To keep the original name, first move the `ID.git` folder out of the repository folder to a safe place, then create the repository and push from the moved folder.

In this case `owngit repo delete` prints `"folder_missing": true` and the same sentence in `message`, with no `kept_path`. These two fields appear only in this case.

Before it removes the records, OwnGit must confirm that the repository folder is on the right storage. When a drive or share is not mounted, the mount point folder can look like the repository folder. One of these must be true:

- Another registered repository's folder is present, and OwnGit has checked in this run that it is the same folder as before. A folder with the right name is not enough.
- This is the only registered repository, and the repository folder holds only OwnGit's own entries, such as its lock file, deletion markers and kept folders in `.owngit-removed`. Any other file or folder there prevents this.

Otherwise OwnGit refuses the deletion and keeps the records. The page shows this message, and `owngit repo delete` returns error `delete_failed` with the same text: "The storage folder could not be confirmed. Check that the drive or share is mounted. The repository records were not removed." Check that the drive or share is mounted, then try again. A mount point that is not empty does not prove that the storage is mounted, and there is no option to skip this check. These checks look only at the repository folder, so they cannot catch every way storage can be mounted wrongly.

The deletion is also refused when OwnGit started with an empty mount point, or when a different folder now stands where the repository's folder was. Mount the right storage or put the original folder back, then try again.

### When deletion is refused or stops

- An import, a check or a Git operation is running: try again when it ends.
- A check container is still being cleaned up: start Docker, then restart OwnGit.
- The Automatic checks page lists the container under Leftover check containers: remove that container on its Docker daemon, tick the confirmation and choose Forget container. Without the dashboard, run `owngit forget-check-container --job JOB --confirm-container-removed` on the OwnGit computer.

If OwnGit stops during a deletion, the next start finishes it. Do not remove the `.owngit-deletion-ID` file in the repository folder; it tells OwnGit the right storage is mounted.

## Download an archive

The Code tab offers the selected branch or tag as ZIP or tar.gz, and a commit page offers that commit. Without a browser:

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

`ref` is a branch, tag or full commit ID, and defaults to the default branch. `--user` makes curl ask for the shared password. If the download stops early, curl reports an error and the file is not a valid archive.

Before it creates an archive, OwnGit checks the commit's files. It refuses the archive with HTTP 409 in these cases, and the API error `code` names the case:

- Two files or folders of the commit share one path (`archive_repeated_path`).
- The commit has too many files to check (`archive_too_many_files`).
- On a Linux host, one file needs more memory to rebuild from Git's stored delta than OwnGit can give Git (`archive_memory`). Clone the repository with Git instead, or run OwnGit on a computer with more memory. [Memory on a small Linux host](OPERATIONS.md#memory-on-a-small-linux-host) explains the estimate.
- On a Linux host, one file is stored in a delta chain deeper than Git builds, so OwnGit cannot check the commit's files (`archive_deep_chain`). Pack the repository again on a computer with enough memory, or clone it with Git instead.

If the check itself fails, the answer is HTTP 502 and no archive is created.

## All activity

All activity in the sidebar shows a year of commits across every repository, and the newest 1,000 commits of the year or of one day. `owngit activity` prints the same as JSON; it takes `--year 2025` or `--date 2026-09-29`.

## A repository shows Preparing or Unreadable

When OwnGit starts, or when a repository folder could not be read, it prepares the repository before serving it. Meanwhile Git gets HTTP 503 with `repository is being prepared; try again later`, and the dashboard shows "Preparing". OwnGit retries on its own. If it stays that way, read the server log for the cause, such as an unmounted disk or wrong permissions, fix it, and restart OwnGit. If the log names a `hooks` entry, run [`owngit doctor`](OPERATIONS.md), which prints a command that moves it aside.

Unreadable means OwnGit can read the folder but not its Git data. Git then reports the error itself.

A repository stored before OwnGit checked pushed objects can hold a commit with two entries at one path. OwnGit keeps that data, but a page that reads those entries says the Git data could not be read, the language panel shows "Not counted right now", and an archive of that commit is refused.

## Import from another Git host

An import copies a repository from another Git host into a new OwnGit repository and can refresh it later, by hand or on a schedule. OwnGit only reads from the source; it never writes to it. Git LFS files are not fetched.

### Start an import

In the dashboard, choose "Import a repository". Then use the repository's Import tab to change the source, credentials, choices and schedule, and to see each run. Only administrators see the source address, credentials and choices.

On the command line:

```sh
owngit import add PROJECT https://git.example.test/team/project.git \
  --token-file /path/to/owner-only-token
```

`import add` creates the repository and waits for the run. `--basic-file` takes a user name and password on two lines instead of a token, and `--ca-file` adds a certificate authority for the source. OwnGit reads credentials only from owner-only files or a prompt.

| Command | What it does |
|---|---|
| `owngit import refresh PROJECT` | Fetch the source again and wait |
| `owngit import status PROJECT` | Show runs, choices, limits and refs that differ from the source |
| `owngit import history PROJECT` | List past runs |
| `owngit import cancel PROJECT` | Stop a run before it publishes |
| `owngit import schedule PROJECT --enable --interval 6h` | Refresh every 60s to 168h while OwnGit runs |
| `owngit import credentials PROJECT --token-file FILE` | Replace the credential; `--clear` removes it |
| `owngit import configure PROJECT ...` | Change the source address, mode, consents, connection choices, limits and refresh choices ([details](#change-the-source)) |
| `owngit import resolve PROJECT` | Accept the repository after an [unresolved publication](#unresolved-publications) |

`add` and `refresh` exit 0 on success, 3 when local refs were kept that differ from the source, 130 when cancelled, and 1 otherwise. Every command takes `--json`.

If another Git operation holds the repository, `import add` or a change to the source, credentials or choices waits for it. When the request runs out of time first, it gets HTTP 409, `another Git operation holds the repository; nothing was changed`, and nothing changes; try again. When the first run of `import add` fails, OwnGit removes the source and credential it saved. Anything that cleanup leaves behind is removed the next time OwnGit starts.

### Change the source

`owngit import configure` changes only what you give it. Everything you leave out keeps its saved value:

```sh
owngit import configure PROJECT \
  --url https://git.example.test/new-team/project.git --mode coexistence
```

| Option | What it sets |
|---|---|
| `--url URL` | The source address |
| `--mode standalone` | OwnGit is the main copy from now on |
| `--mode coexistence` | The other host stays the main copy, and you refresh this copy from it |
| `--git-only-consent` | Accept Git-only content ([Git LFS](#git-lfs)) |
| `--allow-private-network` | Allow a private-network source ([Source address and network](#source-address-and-network)) |

Withdraw a consent with `--git-only-consent=false` or `--allow-private-network=false`. OwnGit checks each change as it does in the dashboard, and the command prints its refusal as it is.

To attach a source to an existing repository that has none, give `--url`. The mode is then Standalone and both consents are off unless the same command sets them. `import add` only creates new repositories.

### Source address and network

The source must use HTTPS. OwnGit refuses some addresses unless you allow them for that source:

| Source | What it needs |
|---|---|
| `http://` address | Allow plain HTTP for this source (`--allow-plain-http`). Code and credentials then travel unencrypted. |
| Private LAN, tailnet or loopback address | Allow a private-network source (`--allow-private-network`) |
| Documentation or benchmarking range | Allow this exceptional destination (`--allow-exceptional-destination`) |
| Link-local, such as 169.254.169.254, or multicast | Never allowed |

Redirects are refused by default. Under Redirects, you can follow redirects within the same origin (`--redirects same_origin`), or also to one approved origin (`--redirects approved --approved-origin https://mirror.example`). Credentials are only sent to the source's own origin.

When a run stops on one of these, the Import tab names the setting to turn on. Changing the source address turns plain HTTP, the exceptional destination, redirects and both [refresh choices](#refresh-choices) off again. Choose them again for the new address, or give them in the same `import configure` command. Limits and extra ref namespaces stay. OwnGit stops using a credential saved for the old address, so save one for the new address with `owngit import credentials`.

### Limits

Change limits under Connection and limits, or with `owngit import configure PROJECT --limit run_seconds=2h --limit pack_bytes=32GiB`. Raise one when an import fails on it; higher limits use more disk and time.

| Limit | Default | Range |
|---|---|---|
| `pack_bytes` (largest pack) | 16 GiB | 1 MiB to 1 TiB |
| `run_seconds` (run time) | 1 hour | 1 minute to 24 hours |
| `fetch_seconds` (download, with indexing) | 30 minutes | 1 minute to 24 hours |
| `index_seconds` | 20 minutes | 1 minute to 24 hours |
| `verify_seconds` | 10 minutes | 1 minute to 24 hours |
| `refs` (refs the source lists) | 50,000 | 1 to 200,000 |
| `advertisement_bytes` (ref list size) | 16 MiB | 64 KiB to 64 MiB |
| `tls_handshake_seconds` | 15 seconds | 1 second to 10 minutes |
| `response_header_seconds` | 30 seconds | 1 second to 1 hour |
| `lfs_objects` (objects scanned for LFS) | 200,000 | 1 to 1,000,000 |

The download and verification times must fit in the run time, and the indexing time in the download time. The run time also counts any wait for the repository while another Git operation holds it.

### How a refresh updates refs

An import brings in branches and tags, and the new repository takes the source's default branch. By default a refresh never overwrites work done in OwnGit:

- A branch follows the source when it has not changed here since the last refresh, or when the source's branch already contains every commit added here.
- A tag follows the source only when it is unchanged here.
- Any other difference is kept and reported as diverged.
- A ref deleted at the source stays here and shows as Deleted at source.

Replaced branch and tag commits go to kept history when it is on.

### Refresh choices

Three choices under Connection and limits, in Refs and refresh, change this. They apply from the next refresh.

| Choice | Command line | Effect |
|---|---|---|
| Extra ref namespaces | `--extra-ref-prefixes refs/notes/` | Also import refs in these namespaces |
| Overwrite diverged branches | `--overwrite-diverged` | Replace refs changed in OwnGit with the source's value |
| Follow upstream deletions | `--follow-upstream-deletions` | Delete refs the source deleted |

The last two can delete or replace local work. Before you turn one on, read "Refs these choices would change now" on the Import tab, or the same list in `owngit import status`. Turn a choice off with `--overwrite-diverged=false` or `--follow-upstream-deletions=false`.

Even with both on, a refresh never rewrites a protected default branch, never deletes the branch HEAD points to, and deletes nothing when the source lists none of the refs it imports. Refs in extra namespaces have no kept history.

### Git LFS

If the source holds Git LFS pointer files, the run stops with `git_lfs_required`. Choose Accept Git-only content (`--git-only-consent`) to import the pointer files without the LFS content.

### After a restore

A backup keeps each import's source address and refresh choices. It does not keep credentials, connection choices, limits or schedules. After a restore, enter the credential again and set what each source needs, such as plain HTTP or a schedule. After a restore or a credential change, a refresh deletes a ref only if an earlier refresh with the new credential has seen that ref.

### Unresolved publications

A run is unresolved when OwnGit cannot prove how it ended, for example after a crash while it wrote refs. Refreshes then stop until you accept the repository as it is:

1. Check its branches, tags and HEAD, and the reason on the last run.
2. Use ordinary Git to fix anything you do not want to keep.
3. Make sure no import is running.
4. Choose "Accept the current repository state" on the Import tab, or run `owngit import resolve PROJECT`.

If the reason says a lock file could not be confirmed, stop OwnGit and every Git writer first. Move the named `.lock` file out of the repository instead of deleting it, start OwnGit, and resolve.

If a first import is unresolved and its repository does not exist, restart OwnGit. If that does not help, move the import's `.owngit-create-*` folder out of the repository folder and restart again.

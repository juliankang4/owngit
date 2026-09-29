# Coding tool integration

<p align="center"><b>English</b> | <a href="CODING_TOOLS.ko.md">한국어</a></p>

This guide is for the person who connects a coding tool, such as Codex,
Claude Code, or Pi, to OwnGit, and for the tool itself. A coding tool uses
OwnGit in one of two ways:

- It runs the `owngit` command in the user's own environment and reads its
  versioned JSON result. In this role the command is called the helper. A
  shared Agent Skill tells the tool which commands to run.
- A tool that supports MCP starts `owngit mcp`, a local
  [MCP server](#mcp-server) that offers the same commands as tools.

Either way, the tool can read and review pull requests, list and create
repositories, and record project checks. For checks, OwnGit keeps one task per
unit of work while revisions change, stores each result on the server bound to
the exact revision it tested, and allows three correction rounds per task. The
skill is only an instruction: a tool can ignore it, and the server records only
what the helper actually submits.

## Prerequisites

You need an OwnGit server reachable from the coding machine, a repository
identifier, the `owngit` binary on the coding machine (see
[Reaching the helper binary](#reaching-the-helper-binary)), and a
repository-scoped helper credential in a private file. Inspect the supplied
and project-known facts first; ask the user only for missing choices that
belong to them.

Create the credential with the administrator password:

```sh
owngit helper-credential create \
  --server https://owngit.example.test \
  --repository example-project \
  --label laptop \
  --password-file /path/to/admin-password-file \
  --output /path/to/helper-token
```

The token is written only to the owner-readable `--output` file and stored on
the server as a hash; keep it out of command arguments, documents, and logs.
An existing file or symlink at `--output` is reported instead of replaced. The
file's first line names the server that issued the token (see
[Credential files and the server line](#credential-files-and-the-server-line)),
and the command prints that server as `token_file_server`.

The helper refuses plain HTTP unless the user passes `--accept-insecure-http`
for that request. Do not add that flag on the user's behalf.

## Skill discovery

The shared skill is
[integrations/skills/owngit-checks/SKILL.md](../integrations/skills/owngit-checks/SKILL.md).
The portable archives on GitHub Releases carry it at that path together with
`docs/CODING_TOOLS.md` and `docs/CODING_TOOLS.ko.md`. The Arch Linux package
installs the same files under `/usr/share/doc/owngit-bin/`, for example
`/usr/share/doc/owngit-bin/integrations/skills/owngit-checks/SKILL.md`. The
Homebrew and npm packages install only the `owngit` command, but every binary
carries the skill it shipped with:

```sh
owngit skill --install ~/.agents/skills
```

`--install DIR` writes `DIR/owngit-checks/SKILL.md` and prints a JSON result
whose `status` is `installed`, `already_current`, or `replaced`. A file with
other content may hold your edits, so the command fails with `skill_modified`
and changes nothing; compare with `owngit skill --print`, then add `--replace`
to install the shipped skill and keep the old file beside it as
`SKILL.md.previous-TIMESTAMP` (named in `previous`). A symbolic link or other
non-regular `SKILL.md` is refused with `skill_target_invalid`. From an unpacked
archive you can also copy it:

```sh
mkdir -p ~/.agents/skills
cp -R integrations/skills/owngit-checks ~/.agents/skills/
```

Install or copy the `owngit-checks` directory, rather than linking to it, into
a location the coding tool scans:

- Codex: `.agents/skills/owngit-checks` in the repository, or
  `~/.agents/skills/owngit-checks` for the user. Codex scans `.agents/skills`
  from the current directory up to the repository root.
- Pi: `.agents/skills/owngit-checks` or `.pi/skills/owngit-checks` in the
  project, or `~/.agents/skills/owngit-checks` or
  `~/.pi/agent/skills/owngit-checks` for the user.

Invoke the skill explicitly, because matching by description can be missed:
the Codex app selects a skill with `@`, the Codex CLI and IDE extension list
skills with `/skills` and accept `$` to mention one, and Pi registers
`/skill:owngit-checks`. If the skill is not loaded, run the commands in this
guide directly.

### Reaching the helper binary

Homebrew, npm, and the Arch Linux package put `owngit` on `PATH`. A source
build and a portable archive do not: use `bin/owngit` in a source checkout or
`./owngit` in an unpacked archive. The one-line installer puts the program in
`~/.local/bin/owngit` or `/usr/local/bin/owngit` on Linux and macOS, or in a
release folder under `%LOCALAPPDATA%\Programs\OwnGit` on Windows, and changes
no `PATH` setting. When the binary is not on `PATH`, give the coding tool the
full path instead of editing shell startup files on its behalf.

## Inside a clone

Inside a clone of an OwnGit repository, `owngit pr`, `owngit check`, and
`owngit repo` find the server and the repository by themselves when `--server`
or `--repository` is missing: they read the clone's `origin` remote and accept
only an OwnGit clone address, `http(s)://HOST[:PORT]/git/ID.git`. `check run`
reads the clone that contains `--workdir`; the other commands read the clone
that contains the current directory. Explicit flags always win, `--repository`
alone keeps the inferred server, and `repo list` and `repo create` infer only
the server. The command prints one line on standard error that says what it
inferred, for example
`owngit: using server https://owngit.example.test and repository example-project from the origin remote`;
the JSON on standard output does not change. Plain HTTP still needs
`--accept-insecure-http`.

The command stops before contacting any server with `origin_unavailable` (not
in a clone, or no `origin`), `origin_ambiguous` (`origin` has more than one
URL), `origin_unsupported` (another kind of address, such as a GitHub URL, an
SSH address, or a local path), or `origin_server_mismatch` (`--server` names
another server than `origin`, and `--repository` is missing).

### Credential files and the server line

A clone's `origin` can name any server, so an inferred server does not show
that you trust it. A password or credential file is sent to an inferred server
only when the file names that server on its first line:

```text
owngit-server: https://owngit.example.test
SECRET
```

The first line is the exact text `owngit-server:`, one space, and one HTTP(S)
origin without a path, at the very start of the file; the secret is the last
line. A first line that only resembles it (after a byte order mark or a blank
line, or in another letter case) is refused. A file without the line must
hold the secret on one line and still works with an explicit `--server`; a
file with the line is refused for any other server, including an explicit
`--server`. Administrator password files and runner token files accept the
line too, and their commands always need an explicit `--server`. Refusals are
`credential_origin_required` (the server was inferred and the file names no
server), `credential_origin_mismatch` (the file names another server), and
`invalid_credential_origin` (the first line is malformed); nothing is sent in
any of these cases. Scripts that read a helper or runner token file directly
must take its last line.

`helper-credential create` and `runner-credential issue` write the line for
the server they used. To bind a shared password file you wrote yourself, add
the line at the top with a text editor, which keeps the file's permissions, or
on macOS or Linux write a new file that only you can read:

```sh
(umask 077; { printf 'owngit-server: %s\n' https://owngit.example.test; cat password-file; } > bound-password-file)
```

On Windows, a file made with Notepad or `echo` inherits its folder's access
entries and is refused as not private. In PowerShell, create the file, limit it
to your account, write the password, and then add the line:

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
[IO.File]::WriteAllText($file, "owngit-server: https://owngit.example.test`n" + [IO.File]::ReadAllText($file))
```

The commands work in Windows PowerShell 5.1 and PowerShell 7, in an ordinary
window and in one opened with Run as administrator, where the Administrators
group becomes the file's owner; OwnGit accepts that owner when only your
account has access. `Read-Host -AsSecureString` keeps the password off the
screen and out of the history.

## Workflow

Create one stable task for the unit of work. The task keeps its identity while
revisions change, and a new commit does not reset its correction budget:

```sh
owngit check task new \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --title "Fix the failing build"
```

Run the checks. Without `--check`, the command runs the checks in the
`.owngit/checks.json` committed in the `HEAD` of `--workdir`; the working tree
copy and configurations recorded on the server for other revisions are never
used, and a missing or invalid file stops the command before anything runs.
`--check name=command` runs and records exactly that set instead:

```sh
owngit check run \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID \
  --check "unit=go test ./..." \
  --check "lint=go vet ./..."
```

`check run` registers the attempt before execution, so the server issues a
repository-wide sequence and a retransmitted request stays idempotent. The
helper observes the worktree with Git and your Git configuration before and
after execution: if the initial revision cannot be read, the run stops before
registration; if only the initial status read fails, the state is `unknown`;
and at the final observation an unreadable revision becomes `unknown`, while a
changed revision, dirty status or failed status read is recorded as `dirty`.
Neither proves a clean tested commit.

Before asking an agent to correct, reserve a correction round and pass it to
the verifying run:

```sh
owngit check cycle reserve \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID

owngit check run \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID --cycle CYCLE_ID
```

Read durable state:

```sh
owngit check task list --server URL --repository ID --credential-file PATH
owngit check status --task TASK_ID --server URL --repository ID --credential-file PATH
owngit check log --attempt ATTEMPT_ID --server URL --repository ID --credential-file PATH
owngit check config show --server URL --repository ID --credential-file PATH
owngit check cycle list --task TASK_ID --server URL --repository ID --credential-file PATH
```

## Command reference

Every command takes `--server`, `--repository`, `--credential-file`, and
`--accept-insecure-http` (the remote flags); inside a clone, `--server` and
`--repository` can come from `origin`.

- `check task new` creates a task (`--title`); `check task list` lists the
  repository's tasks with their correction budgets.
- `check run` executes checks and, unless `--no-upload` is set, records the
  attempt. Flags: `--task` (required), `--cycle`, `--workdir` (default `.`),
  `--timeout` (default 10 minutes), `--output-limit` (default 65536 bytes per
  check, both must be positive), `--no-upload` (the remote flags are then
  optional), and repeatable `--check name=command`.
- `check cycle reserve` reserves one correction round (`--task` required);
  `check cycle list` lists the reserved rounds.
- `check status` reads the task and its latest attempt; `check log` reads one
  raw log by `--attempt`; `check config show` reads the configuration recorded
  most recently in the repository, from any branch, which `check run` does not
  use.
- `helper-credential create` issues a credential (`--label`, `--output`
  required, `--password-file` instead of `--credential-file`);
  `helper-credential list` and `helper-credential revoke --id ID` manage
  existing credentials.

## Reading the result

`check run` prints one JSON object and exits `0` when every check passed (also
for a local `--no-upload` run; read `registered` and `uploaded` to see whether
the attempt was recorded), `1` when a check did not pass, `2` when this client
could not confirm that the attempt was recorded, and `130` when the run was
cancelled. If it stops before running any check, for invalid arguments, a
missing or invalid committed configuration (`checks_not_configured`,
`invalid_check_configuration`), or a registration the server refused such as
an unreserved `--cycle`, it prints an error object
`{"ok":false,"error":{"code":...,"message":...}}` and exits 1; nothing ran
and nothing was recorded.

The object carries `ok`, `registered`, `uploaded`, `attempt_id`, `cycle_id`,
`task`, `attempt`, `correction_cycles_remaining`, `results`, and
`upload_error`. `attempt` carries `status`, `revision_oid`, `worktree_state`,
`summary`, `cleanup_failed`, `log_truncated`, and the execution limits; each
entry in `results` carries `name`, `command`, `status`, `exit_code`,
`duration_ms`, `output_excerpt`, `truncated`, and `cleanup_error`.

Per-check statuses are `passed`, `failed`, `error`, `cancelled`, `incomplete`,
and `unavailable`; the server recomputes the attempt status from them and does
not trust a helper-supplied aggregate. A cleanup error makes the result
`error` even when the exit code is visible; output beyond the limit makes it
`incomplete`, while a shortened excerpt or log is only marked truncated; an
empty configured set is `unavailable`, never `passed`; and a registered
attempt that never reports a completion stays visible as `pending`.

`upload_error` means this client could not confirm the registration or
completion. A lost response can leave an accepted registration or completion
on the server, so inspect `check status` before repeating a reservation or a
run, and do not claim the attempt is absent; it is not the same as a recorded
failed check, which has a durable attempt with a `failed` result. When the
server cannot be reached at all, the checks still run, the command exits 2,
and `upload_error` names the cause, such as a refused connection or a TLS
error.

## Correction budget

The task budget is three automatic correction rounds. Reserve a round before
asking an agent to correct, and pass the `cycle.id` from the reserve response
as `--cycle` to the verifying run. A correction already authorized in the
active task may continue within the budget without asking the user again; do
not start an unrequested fix, and reuse the stable task and cycle identifiers
instead of creating new ones. A reserved round is counted once, whether the
following check passes or fails, and retries inside the round reuse it. The
initial check and a manual rerun consume no round, and an unavailable or
cancelled run does not create one by itself. When the budget is exhausted, the
reserve command returns `correction_budget_exhausted`: stop automatic
continuation and report the unresolved task. A manual check can still be
recorded afterwards.

Only `check status`, or a reserve call that returns
`correction_budget_exhausted`, shows that the budget is exhausted. The
top-level `correction_cycles_remaining` in a run output is filled in only from
a server response and has no separate "unknown" value, so when no response
set it, the field still prints `0`. That `0` means the client did not read the
budget, not that the budget ran out. A run carries a measured budget only when
it also carries a `task` object; read `task.correction_cycles_remaining`
there. Two cases print the unmeasured `0`:

- `--no-upload` never contacts the server, so the server task is unchanged.
- A failed registration, where `registered` is false and `upload_error` is set,
  is unconfirmed rather than absent. If the request was accepted, a durable
  attempt exists and the sequence moved; if not, nothing changed. Do not assume
  either.

For an unconfirmed attempt, keep the stable task identifier, preserve the
printed `attempt_id` as diagnostic evidence, and inspect
`check status --task TASK_ID`. Do not rerun the check or reserve a round to
resolve the uncertainty: `check run` generates a new attempt identifier on
every invocation and cannot resubmit an existing one, so a rerun starts a
separate attempt. The client's own retry of an unanswered request sends the
same body and is safe; running the command again from a shell is not. If
`check status` does not resolve that exact attempt, report it as unconfirmed.
Never report exhaustion, and never stop an authorized correction, on the
strength of an unmeasured `0`.

## Repositories

`owngit repo` lists, shows, and creates repositories, restores their files
from earlier commits, and prints one JSON object. It uses general access,
like `owngit pr`: pass the shared general-access password with
`--password-file`, or omit it when access is open. There is no delete or rename.

```sh
owngit repo list --server https://owngit.example.test
owngit repo show --server https://owngit.example.test --repository example-project
owngit repo create --server https://owngit.example.test --name example-project \
  --description "Optional description"
```

Each repository carries `id`, `name`, `description`, `created_at`, and
`clone_url`; `repo show` adds `default_branch` when the branches can be read
at that moment. `repo list` returns at most 1000 repositories, with
`truncated` true when there are more. `repo create` applies the browser form's
rules and fails with `repository_exists`, `invalid_repository_name`,
`reserved_repository_name`, or `invalid_repository_description` (over 500
bytes).

`owngit repo settings show` and `owngit repo settings set` read and change one
repository's [kept history and default branch protection](OPERATIONS.md#kept-history).
Unlike the other `repo` commands, they need the administrator password in
`--password-file`. Inside a clone they take `--server` and `--repository` from
`origin`, and the password file must then name that server
([Credential files and the server line](#credential-files-and-the-server-line)).

`owngit repo kept-history` and `owngit repo restore` bring back files from an
earlier commit, as the dashboard's restore pages do, and use the same general
access ([Restoring repository files](OPERATIONS.md#restoring-repository-files)).
A restore takes two steps: preview it, then apply it with the `expected_head`
that the preview returned.

```sh
owngit repo kept-history
owngit repo restore preview --source OID --target main --path src/app.go
owngit repo restore apply --source OID --target main --path src/app.go --expected-head OID
```

`repo kept-history` lists, newest first, the earlier values of branches and
tags that a force push, an import, or a deletion replaced. Each entry names
the ref it was kept from (`source_ref`), the commit to restore from
(`commit_oid`), and the new branch that the dashboard offers for it
(`restore_target`).

`--source` is the full ID of the commit to restore from. `--target` is a
branch name such as `main`, never a full ref name such as
`refs/heads/main`, the form `source_ref` uses. Repeat `--path` for each file
to restore. Without `--path` the whole tree is restored, which also deletes
files that the source commit does not have. A whole-tree restore onto a branch
that does not exist creates that branch at the source commit.

The preview changes nothing. It lists each changed path with `status`
(`added`, `modified`, or `deleted`), `old_mode` and `new_mode` (`120000` is a
symbolic link), `additions`, `deletions`, and `binary`. `creates_branch` says
whether applying creates the branch; otherwise applying adds one commit with
the tree `result_tree` on top of `expected_head`. `can_apply` is false when
the branch already has these files. A restore never rewrites history.

Apply answers with `commit_oid`. A refused request fails with one of these
codes:

- `stale_revision`: the branch moved after the preview. Nothing changed;
  preview again.
- `restore_no_changes`: the branch already has these files.
- `restore_unsupported`: the selection includes a submodule, would remove
  unselected files beneath a selected path, or selects files for a branch that
  does not exist.
- `invalid_restore`: the request is invalid, for example a full ref name as
  the target, a source that is not a full commit ID of this repository, or a
  target branch whose name some file systems treat as the same as another
  branch's, such as `Main` beside `main`.
- `restore_failed`: the restore could not be completed, and it may have taken
  effect anyway. Read the target branch before trying again.

The API routes are `GET /api/v1/repositories/ID/kept-history`,
`POST /api/v1/repositories/ID/restore/preview` and
`POST /api/v1/repositories/ID/restore`.

## Pull request changes

`owngit pr diff --number N` prints what a pull request changes as one JSON
object: the exact source and target commits it compared, their merge base, the
changed files with line counts, and the patch. It uses general access, like
`owngit pr`, and inside a clone it reads the server and repository from
`origin`.

```sh
owngit pr diff --number 3
owngit pr diff --number 3 --stat
owngit pr diff --number 3 --patch
owngit pr diff --number 3 --source-oid SOURCE_OID --target-oid TARGET_OID
```

The changes are counted from the merge base to the source, as on the pull
request page. By default the command reads the current source and target
commits once and diffs exactly those, so `source.oid` and `target.oid`
describe the patch even if a branch moves during the read; pass the same
object IDs to `pr review submit` to review what you read, and the review fails
with `stale_revision` if a branch moved in the meantime. For a merged pull
request the current pair is the pair it merged. `--source-oid` and
`--target-oid` pin a pair, which must be the current one or one recorded for
the pull request, such as the pair a review was requested for; any other pair
fails with `revision_not_recorded`, and giving only one of the two fails with
`invalid_arguments` (`invalid_revision` in the API). When the branches have
moved away from a pinned pair, the result still shows that pair, sets `moved`
to true, and gives the current pair in `current`.

The result is bounded: `truncated` is true when the patch leaves out some
files, `incomplete` when the file list misses files too, and the patch always
ends at a file boundary. `reason` says why: `output_limit` (the diff reached
its 8 MiB limit), `time_limit` (Git ran out of time; a later try may read
more), or `response_limit` (cut to fit the 4 MiB response). When the branches
share no commit or have more than one merge base, `unavailable` is
`no_merge_base` or `multiple_merge_bases`, and there is no file list or patch.
`--stat` prints the object without `patch`; `--patch` prints only the patch
text and writes the compared commits, and any move or cut, to standard error.
The API route is `GET /api/v1/repositories/ID/pull-requests/N/diff`, with the
optional query parameters `source_oid` and `target_oid`.

## Pull request mergeability

`owngit pr mergeability --number N` answers whether an open pull request can
merge now, for its current source and target commits, and prints one JSON
object. It changes nothing: it creates no ref, no record, and no object in the
repository. It uses general access, like `owngit pr diff`.

```sh
owngit pr mergeability --number 3
owngit pr mergeability --number 3 --source-oid SOURCE_OID --target-oid TARGET_OID
```

`source` and `target` name the commits the answer is about. `status` is one
of these:

- `clean`: the merge would succeed. `method` is `fast_forward`,
  `merge_commit`, or `up_to_date`.
- `conflict`: `conflict_paths` lists up to 100 conflicting paths, and
  `conflict_paths_truncated` is true when there are more. Git can report a
  conflict without naming a file, and `conflict_paths` is then absent. When
  the branches share no history, `reason` is `no_merge_base` instead.
- `unavailable`: OwnGit could not tell. `reason` says why, for example
  `unsupported_git` (Git older than 2.38), `source_branch_missing`, or
  `repository_unavailable`, and `message` explains it.
- `stale`: a branch moved away from the pair given with `--source-oid` and
  `--target-oid`. `source` and `target` then hold the current pair.

Pass `--source-oid` and `--target-oid` from an earlier answer to check that
same pair again. The answer is not stored and does not reserve the merge:
`pr merge` checks again when it runs. The API route is
`GET /api/v1/repositories/ID/pull-requests/N/mergeability`, with the optional
query parameters `source_oid` and `target_oid`.

## MCP server

`owngit mcp` is a [Model Context Protocol](https://modelcontextprotocol.io)
server for coding tools that support MCP. It implements protocol revision
`2025-11-25` over standard input and output (the stdio transport), one
JSON-RPC message per line, and opens no network port. Each tool wraps one
`owngit` command and returns the JSON that command prints. A tool that can run
shell commands can keep using the command line, which usually costs fewer
tokens than a list of tool descriptions.

### Starting the server

The coding tool starts the server as a child process. The flags fix everything
a tool call could use to reach something else:

- `--workdir DIR` (default: the directory the tool starts it in): the clone
  whose `origin` names the server and repository, as in
  [Inside a clone](#inside-a-clone), and where `check_run` runs the checks.
  Give an absolute path, because coding tools differ in the directory they
  start servers in.
- `--server` and `--repository` take precedence over `origin`. When no
  repository is known, the repository and pull request tools take a
  `repository` argument instead.
- `--password-file`: the shared general-access password for the repository and
  pull request tools. Leave it out when access is open.
- `--credential-file`: a helper credential. It adds the check tools and needs a
  known repository.
- `--accept-insecure-http`: required for a plain HTTP server. Add it only for a
  connection whose risk you accepted.
- `--no-run-check`: leaves out `check_run`.
- `--result-limit BYTES` (default 65536, from 4096 to 4194304): the longest
  tool result.

Password and credential files follow the rules in
[Credential files and the server line](#credential-files-and-the-server-line):
when the server comes from `origin`, the file must name it. The files are read
once at startup, and their secrets never appear in a result. When startup
fails, for example with `credential_origin_required` or
`insecure_http_confirmation_required`, the error object goes to standard error
and the process exits with status 1; coding tools show it in their MCP server
log, which also carries the line that says what was read from `origin`.

Tool arguments are values such as pull request numbers, commit IDs, task IDs,
titles, and branch names. An argument outside the tool's input schema, such as
a server, a path, or a command, fails with `invalid_arguments`, and a
`repository` other than the one fixed at startup fails with
`repository_not_allowed`. Text that is not valid UTF-8, including a `\u`
escape of half a surrogate pair such as a lone `\ud800`, also fails with
`invalid_arguments`, because OwnGit never replaces text it cannot read.

### Client configuration

Replace the paths with your own. Credentials stay in the files the flags name,
never in the client configuration or its environment.

Claude Code reads a project's `.mcp.json`, which
`claude mcp add --scope project owngit -- owngit mcp ...` also writes:

```json
{
  "mcpServers": {
    "owngit": {
      "command": "owngit",
      "args": ["mcp", "--workdir", "/path/to/clone", "--credential-file", "/path/to/helper-token"]
    }
  }
}
```

Codex reads `~/.codex/config.toml`. It waits 60 seconds for a tool by default
and then cancels the call, which stops a running `check_run`, so set
`tool_timeout_sec` above the time your checks take:

```toml
[mcp_servers.owngit]
command = "owngit"
args = ["mcp", "--workdir", "/path/to/clone", "--credential-file", "/path/to/helper-token"]
tool_timeout_sec = 1800
```

Any other MCP client: use the stdio transport, the command `owngit` (or its
full path, see [Reaching the helper binary](#reaching-the-helper-binary)), and
the arguments `mcp` followed by the flags above.

### Tools

Read tools change nothing:

| Tool | Command |
|---|---|
| `repository_list`, `repository_show` | `repo list`, `repo show` |
| `repository_kept_history`, `repository_restore_preview` | `repo kept-history`, `repo restore preview`; `source_oid`, `target_branch`, and `paths` stand for `--source`, `--target`, and `--path`, and the preview returns the `expected_head` that applying needs |
| `pull_request_list`, `pull_request_show` | `pr list`, `pr show`; only show includes the description and review notes |
| `pull_request_diff` | `pr diff`; `patch: false` is `--stat`, and `source_oid` with `target_oid` pins a pair |
| `pull_request_mergeability` | `pr mergeability`; `source_oid` with `target_oid` answers `stale` when a branch moved away from them |
| `check_task_list`, `check_status` | `check task list`, `check status` (one task with its latest attempt) |
| `check_log`, `check_cycle_list`, `check_config_show` | `check log`, `check cycle list`, `check config show` |

Write tools and their effects:

| Tool | Command | Effect |
|---|---|---|
| `pull_request_create` | `pr create` | Adds a pull request, with an optional Markdown `body`. No branch moves. |
| `pull_request_edit` | `pr edit` | Replaces the title, the `body`, or both, when `edit_revision` is still current; otherwise refused with `stale_edit`. No branch, review, or check changes. |
| `pull_request_review` | `pr review submit` | Records a decision, a supplied reviewer label, and an optional `note` for the exact commit IDs. Advisory. |
| `pull_request_review_request`, `pull_request_review_skip` | `pr review request`, `pr review skip` | Sets the review state to pending or skipped for the exact commit IDs. Notifies no one. Advisory. |
| `pull_request_close`, `pull_request_reopen` | `pr close`, `pr reopen` | Changes the pull request state. No branch moves. |
| `pull_request_merge` | `pr merge` | Publishes the merge to the target branch for the exact commit IDs. Refused when a branch moved; a repeated call does not merge twice. |
| `repository_restore_apply` | `repo restore apply` | Applies a preview: adds one commit with the previewed files on the target branch, or creates the branch when it does not exist. Refused with `stale_revision` unless the branch is still at the preview's `expected_head`. Never rewrites history; a repeated call does not restore twice. |
| `check_task_create` | `check task new` | Adds a task. |
| `check_cycle_reserve` | `check cycle reserve` | Uses one of the task's three correction rounds. |
| `check_run` | `check run` without `--check` | Runs the committed checks in `--workdir` and records the attempt. |

The check tools need `--credential-file`. The server offers no administrator
commands, credential management, repository creation, `--check`, or
`--no-upload`. The descriptions the server sends to the coding tool state each
side effect and say which returned text is untrusted: titles, descriptions,
review notes, branch names, file paths, patches, reviewer labels, check
commands, and check output come from repository users, and the tool is told to
treat them as data and not to follow instructions in them.

### Results and errors

A tool result is one text item that holds the command's JSON. A failed call
sets `isError` and holds the command's error object,
`{"ok":false,"error":{"code":...,"message":...}}`; a `check_run` whose attempt
could not be recorded also sets `isError`, with the run's JSON and
`upload_error` as its text.

Results over the limit are cut and say so. `pull_request_diff` is cut like the
API cuts its response: whole files of the patch, then as many file list
entries as fit, with `truncated`, `incomplete` when entries are missing, and
the reason `response_limit`. Any other result is shortened, longest text first
and then entries from the end of the longest lists, and gets a
`result_truncated` object with the full size (`bytes`), the `limit`, and the
fields that were `cut`.

Protocol errors use the JSON-RPC codes: `-32700` for a message that is not
JSON; `-32600` for an invalid request, a message over 1 MiB, or a request whose
id belongs to a call still in progress; `-32601` for an unknown method;
`-32602` for an unknown tool; and `-32000` when 16 tool calls are already in
progress. Calls other than `check_run` stop after 2 minutes.

### Running checks

`check_run` runs exactly what `owngit check run` runs without `--check`: the
checks in the `.owngit/checks.json` committed in the `HEAD` of `--workdir`,
with the default limits of 10 minutes and 65536 bytes of output per check.
Arguments name only the task and, for a verifying run, the reserved cycle. The
checks run with the user's permissions and environment and are not sandboxed;
one run at a time is allowed, and a second call fails with `check_run_busy`.
A cancellation from the coding tool, or the end of its input, stops the checks
and their child processes; the attempt is still recorded as cancelled, and the
cancelled call gets no response.

The checks are commands committed in the repository, so anyone who can commit
to the clone can make `check_run` start a program. When an agent may edit
files but must not run commands, start the server with `--no-run-check`:
`check_run` is then left out of the tool list and refused before anything
runs, while the other check tools remain, so the agent can still read
evidence, create tasks, and reserve rounds.

## Limits

- Checks are advisory. They do not block a merge, and a passing check is not
  proof that the code is correct. A project or team can require stricter review
  or check rules, and this integration does not override them.
- The helper inherits the user's environment and permissions. It is not a
  sandbox, and a check can read files and credentials the user account can
  reach.
- A dirty or unknown worktree is not a tested commit. Report the recorded
  worktree state instead of calling the revision tested.
- `--no-upload` runs locally and is not recorded on the server. Do not describe
  it as server-recorded evidence.
- Do not retry a failed check blindly, and do not weaken or replace the
  committed check configuration to make a check pass.
- A reviewer with read-only access cannot run the checks. An authorized
  execution-capable participant runs them and supplies the result with its
  provenance.

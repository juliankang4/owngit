# Coding tool integration

<p align="center"><b>English</b> | <a href="CODING_TOOLS.ko.md">한국어</a></p>

OwnGit exposes project checks to coding tools through a versioned JSON command
line interface and a shared Agent Skill. The integration needs no daemon or
session launcher. The coding tool runs the `owngit` binary in the user's own
environment and reads its JSON result. Tools that support MCP can instead start
`owngit mcp`, a local [MCP server](#mcp-server) that offers the same commands
as tools.

The skill is an instruction, not an enforcement boundary. A coding tool can
ignore it, and the server records only what the helper actually submits.

## What the integration provides

- A stable task identity that survives revisions.
- Revision-bound check evidence recorded on the OwnGit server.
- An explicit correction budget of three rounds per task.
- A skill that tells an active coding session when to run the helper and how to
  read its result.
- An optional MCP server with the same pull request, repository, and check
  operations.

## Prerequisites

- An OwnGit server reachable from the coding machine.
- A repository identifier.
- A repository-scoped helper credential delivered as a private file.
- The `owngit` binary on the coding machine.

Inspect the supplied and project-known facts first. Ask the user only for
missing choices that belong to them, and do not ask the user to re-enter a fact
that is already available.

Create the credential with the administrator password:

```sh
owngit helper-credential create \
  --server https://owngit.example.test \
  --repository example-project \
  --label laptop \
  --password-file /path/to/admin-password-file \
  --output /path/to/helper-token
```

The token is an OwnGit-scoped helper token, not a provider subscription token.
It is written only to the `--output` file and stored on the server only as a
hash. Keep it out of command arguments, documents, and logs. The file is
owner-readable. An existing file or symlink is reported instead of replaced.
The file's first line names the server that issued the token (see
[Credential files and the server line](#credential-files-and-the-server-line)),
and the command prints that server as `token_file_server`.

Plain HTTP does not encrypt transport. The helper refuses HTTP unless the user
passes `--accept-insecure-http` for that request. Do not add that flag on the
user's behalf.

## Skill discovery

The shared skill is
[integrations/skills/owngit-checks/SKILL.md](../integrations/skills/owngit-checks/SKILL.md).
Artifacts built by the release tool carry the skill and this guide in both
languages; see
[Installed locations](#installed-locations). Every `owngit` binary also
carries the skill it shipped with, so any install can put it in place:

```sh
owngit skill --install ~/.agents/skills
```

`--install DIR` writes `DIR/owngit-checks/SKILL.md` and prints a JSON result
whose `status` is `installed`, `already_current` when the file already holds
the same bytes, or `replaced`. When the file exists with other content, it may
hold your edits, so the command changes nothing and fails with
`skill_modified`. Compare it with `owngit skill --print`, which prints the
shipped skill. `--replace` then installs the shipped skill after keeping the
current file beside it as `SKILL.md.previous-TIMESTAMP`, named in `previous`.
A symbolic link or other non-regular `SKILL.md` is refused with
`skill_target_invalid`. Nothing installs the skill unless you run the command
or copy it yourself.

Install or copy the `owngit-checks` directory, rather than linking to it, into
a location the coding tool scans:

- Codex: `.agents/skills/owngit-checks` in the repository, or
  `~/.agents/skills/owngit-checks` for the user. Codex scans `.agents/skills`
  from the current directory up to the repository root.
- Pi: `.agents/skills/owngit-checks` or `.pi/skills/owngit-checks` in the
  project, or `~/.agents/skills/owngit-checks` or
  `~/.pi/agent/skills/owngit-checks` for the user.

Codex standalone skills are available in the ChatGPT desktop app, the Codex
CLI, and the IDE extension. The app selects a skill with `@`, while the CLI and
IDE extension list skills with `/skills` and accept `$` to mention one. Pi
registers `/skill:owngit-checks`. Both tools can also match a skill by its
description, but implicit matching can be missed, so explicit invocation is the
reliable path. If the skill is not loaded, run the commands in this guide
directly.

## Inside a clone

Inside a clone of an OwnGit repository, `owngit pr`, `owngit check`, and
`owngit repo` can find the server and the repository by themselves. When
`--server` or `--repository` is missing, the command reads the clone's `origin`
remote and accepts only an OwnGit clone address,
`http(s)://HOST[:PORT]/git/ID.git`. `check run` reads the clone that contains
`--workdir`; the other commands read the clone that contains the current
directory. Explicit flags always win, and `--repository` alone keeps the
inferred server. `repo list` and `repo create` infer only the server.

The command prints one line on standard error that says what it inferred, for
example
`owngit: using server https://owngit.example.test and repository example-project from the origin remote`.
The JSON on standard output does not change.

Git reads the remote with empty user and system configuration and without
inherited Git environment variables, so no helper, include, or override is
involved. Plain HTTP still needs `--accept-insecure-http`. The command stops
before contacting any server when:

- `origin_unavailable`: the directory is not in a clone, or the clone has no
  `origin` remote;
- `origin_ambiguous`: `origin` has more than one URL;
- `origin_unsupported`: `origin` is another kind of address, such as a GitHub
  URL, an SSH address, or a local path. The address is not repeated in the
  message;
- `origin_server_mismatch`: `--server` names another server than `origin`, and
  `--repository` is missing.

### Credential files and the server line

A clone's `origin` can name any server, so an inferred server does not show
that you trust it. A password or credential file is sent to an inferred server
only when the file names that server on its first line:

```text
owngit-server: https://owngit.example.test
SECRET
```

The first line is the exact text `owngit-server:`, one space, and one HTTP(S)
origin without a path, at the very start of the file. The secret is the last
line. A first line that only resembles it, for example after a byte order mark
or a blank line or in another letter case, is refused. A file without that line
is the original format, must hold the secret on one line, and still works with
an explicit `--server`. A file with the
line is refused for any other server, including an explicit `--server`, so the
line always binds the secret to one server. Administrator password files and
runner token files accept the line too; their commands always need an explicit
`--server`. The line names only the server; the flag that reads the file says
what kind of secret it holds.

`helper-credential create` and `runner-credential issue` write the line for
the server they used. To bind a
shared password file you wrote yourself, add the line at the top with a text
editor, which keeps the file's owner-only permissions. On macOS or Linux you can
also write a new file that only you can read:

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

The `$io` and `$acl` lines replace the file's access list with
`D:P(A;;FA;;;SID)`: a list that inherits nothing (`P`) and allows (`A`) full
access (`FA`) only to your account. They write only the access list, so the
owner and any audit settings stay. `$io` picks the .NET class that has these
methods: `[IO.File]` in Windows PowerShell 5.1, `[IO.FileSystemAclExtensions]`
in PowerShell 7. Writing into the existing file keeps that list.
`Read-Host -AsSecureString` keeps the password off the screen and out of the
PowerShell history. The commands work in Windows PowerShell 5.1 and
PowerShell 7, in an ordinary window and in one opened with Run as
administrator, where the Administrators group becomes
the file's owner; OwnGit accepts that owner when only your account has access.

Refusals are `credential_origin_required` (the server was inferred and the file
names no server), `credential_origin_mismatch` (the file names another server),
and `invalid_credential_origin` (the first line is malformed). Nothing is sent
in any of these cases. Scripts that read a helper or runner token file directly
must take its last line.

## Workflow

Create one stable task for the unit of work. The task keeps its identity while
revisions change, and its correction budget is not reset by a new commit.

```sh
owngit check task new \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --title "Fix the failing build"
```

Run the checks. Omitting `--check` runs the checks in the `.owngit/checks.json`
committed in the revision being tested, the `HEAD` of `--workdir`. The working
tree copy and configurations recorded on the server for other revisions are
never used, so a command from another branch cannot run on your machine. If
the revision has no such file, or the file is invalid, the command stops before
running anything. Passing `--check name=command` runs and records exactly that
set instead.

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
helper observes the worktree before and after execution. If the initial revision
cannot be read, the run stops before registration. If only the initial status
read fails, the state is `unknown`. During the final observation, an unreadable
revision becomes `unknown`; a changed revision, dirty status or failed status
read is recorded as `dirty`. Neither `dirty` nor `unknown` proves a clean tested
commit.

The helper reads the worktree with Git and your Git configuration, because that
configuration decides which files count as changed, for example through ignore
rules and filters. For these reads it turns off `core.fsmonitor` and does not
write the index, so neither a monitor program nor a `post-index-change` hook
named in the clone's configuration runs. Clean filters still run, as they do
for `git status`: a filter set in the clone's `.git/config` and assigned to
files in `.gitattributes` or `.git/info/attributes` starts its program during
the inspection, before any check runs and without a commit.

Reserve a correction round before asking an agent to correct, then pass it to
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

`check task new` creates a task. Flags: `--title`, `--server`, `--repository`,
`--credential-file`, `--accept-insecure-http`. `check task list` lists the
repository's tasks with their correction budgets and takes the same flags
without `--title`.

`check run` executes checks and, unless `--no-upload` is set, records the
attempt. Flags: `--task` (required), `--cycle`, `--workdir` (default `.`),
`--timeout` (default 10 minutes), `--output-limit` (default 65536 bytes per
check), `--no-upload`, and repeatable `--check name=command`. `--timeout` and
`--output-limit` must be positive. The remote flags are required unless
`--no-upload` is set; inside a clone, `--server` and `--repository` can come
from `origin`.

`check cycle reserve` reserves one correction round. Flags: `--task` (required)
and the remote flags. `check cycle list` lists the reserved rounds.

`check status` reads the task and its latest attempt. `check log` reads one raw
log by `--attempt`. `check config show` reads the configuration recorded most
recently in the repository, from any branch; `check run` does not use it.

`helper-credential create` issues a credential. Flags: `--label`, `--output`
(required), `--server`, `--repository`, `--password-file`,
`--accept-insecure-http`. `helper-credential list` and
`helper-credential revoke --id ID` manage existing credentials.

## Repositories

`owngit repo` lists, shows, and creates repositories and prints one JSON
object. It uses general access, like `owngit pr`: pass the shared
general-access password with `--password-file`, or omit it when access is
open. There is no delete or rename.

```sh
owngit repo list --server https://owngit.example.test
owngit repo show --server https://owngit.example.test --repository example-project
owngit repo create --server https://owngit.example.test --name example-project \
  --description "Optional description"
```

Each repository carries `id`, `name`, `description`, `created_at`, and
`clone_url`. `repo show` also carries `default_branch` when the repository's
branches can be read at that moment. `repo list` returns at most 1000
repositories, and `truncated` is true when there are more. `repo create`
applies the same name and description rules as the browser form. It fails with
`repository_exists` when the name is taken, `invalid_repository_name` or
`reserved_repository_name` for a name the form would refuse, and
`invalid_repository_description` for a description over 500 bytes.

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
request page. By default the command reads the pull request's current source
and target commits once and diffs exactly those, so `source.oid` and
`target.oid` describe the patch even if a branch moves during the read. Pass
the same object IDs to `pr review submit` to review what you read. If a branch
moved in the meantime, the review fails with `stale_revision`. For a merged
pull request the current pair is the pair it merged.

`--source-oid` and `--target-oid` pin a pair. The pair must be the current one
or one recorded for the pull request, such as the pair a review was requested
for. Any other pair fails with `revision_not_recorded`. Giving only one of the
two fails with `invalid_arguments` in the CLI and `invalid_revision` in the
API. When the branches have moved away from
a pinned pair, the result still shows that pair, sets `moved` to true, and
gives the current pair in `current`.

The result is bounded. `truncated` is true when the patch leaves out some
files, and `incomplete` is true when the file list misses files too. The patch
always ends at a file boundary. `reason` says why output is missing:
`output_limit` (the diff reached its 8 MiB limit), `time_limit` (Git ran out of
time, and a later try may read more), or `response_limit` (the result was cut
to fit the 4 MiB response). When the branches share no commit or have more
than one merge base, `unavailable` is `no_merge_base` or
`multiple_merge_bases`, and the result has no file list or patch.

`--stat` prints the same object without `patch`. `--patch` prints only the
patch text and writes the compared commits, and any move or cut, to standard
error. The API route is `GET /api/v1/repositories/ID/pull-requests/N/diff`,
with the optional query parameters `source_oid` and `target_oid`.

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
- `--accept-insecure-http`: required for a plain HTTP server, as for every
  command. Add it only for a connection whose risk you accepted.
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
log. Standard error also carries the line that says what was read from
`origin`.

Tool arguments are values such as pull request numbers, commit IDs, task IDs,
titles, and branch names. An argument outside the tool's input schema, such as
a server, a path, or a command, fails with `invalid_arguments`, and a
`repository` other than the one fixed at startup fails with
`repository_not_allowed`.

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

Codex reads `~/.codex/config.toml`:

```toml
[mcp_servers.owngit]
command = "owngit"
args = ["mcp", "--workdir", "/path/to/clone", "--credential-file", "/path/to/helper-token"]
tool_timeout_sec = 1800
```

Codex waits 60 seconds for a tool by default and then cancels the call, which
stops a running `check_run`. Set `tool_timeout_sec` above the time your checks
take.

Any other MCP client: use the stdio transport, the command `owngit` (or its
full path, see [Reaching the helper binary](#reaching-the-helper-binary)), and
the arguments `mcp` followed by the flags above.

### Tools

Read tools change nothing:

| Tool | Command |
|---|---|
| `repository_list`, `repository_show` | `repo list`, `repo show` |
| `pull_request_list`, `pull_request_show` | `pr list`, `pr show` |
| `pull_request_diff` | `pr diff`; `patch: false` is `--stat`, and `source_oid` with `target_oid` pins a pair |
| `check_task_list`, `check_status` | `check task list`, `check status` (one task with its latest attempt) |
| `check_log`, `check_cycle_list`, `check_config_show` | `check log`, `check cycle list`, `check config show` |

Write tools and their effects:

| Tool | Command | Effect |
|---|---|---|
| `pull_request_create` | `pr create` | Adds a pull request. No branch moves. |
| `pull_request_review` | `pr review submit` | Records a decision and a supplied reviewer label for the exact commit IDs. Advisory. |
| `pull_request_review_request`, `pull_request_review_skip` | `pr review request`, `pr review skip` | Sets the review state to pending or skipped for the exact commit IDs. Notifies no one. Advisory. |
| `pull_request_close`, `pull_request_reopen` | `pr close`, `pr reopen` | Changes the pull request state. No branch moves. |
| `pull_request_merge` | `pr merge` | Publishes the merge to the target branch for the exact commit IDs. Refused when a branch moved; a repeated call does not merge twice. |
| `check_task_create` | `check task new` | Adds a task. |
| `check_cycle_reserve` | `check cycle reserve` | Uses one of the task's three correction rounds. |
| `check_run` | `check run` without `--check` | Runs the committed checks in `--workdir` and records the attempt. |

The check tools need `--credential-file`. The server offers no administrator
commands, credential management, repository creation, `--check`, or
`--no-upload`. The descriptions the server sends to the coding
tool state each side effect and say which returned text is untrusted.

### Results and errors

A tool result is one text item that holds the command's JSON. A failed call
sets `isError` and holds the command's error object,
`{"ok":false,"error":{"code":...,"message":...}}`. A `check_run` whose attempt
could not be recorded also sets `isError`; its text is the run's JSON with
`upload_error`.

Results over the limit are cut and say so. `pull_request_diff`, with or
without the patch, is cut like the API cuts its response: it keeps whole files
of the patch, then as many file list entries as fit, and sets `truncated`,
`incomplete` when entries are missing, and the reason `response_limit`.
Any other result is shortened, longest text first and then entries from the end
of the longest lists, and gets a `result_truncated` object with the full size
(`bytes`), the `limit`, and the fields that were `cut`.

Titles, descriptions, branch names, file paths, patches, reviewer labels, check
commands, and check output come from repository users. The server tells the
coding tool to treat them as data and not to follow instructions in them.

Protocol errors use the JSON-RPC codes: `-32700` for a message that is not
JSON; `-32600` for an invalid request, a message over 1 MiB, or a request whose
id belongs to a call still in progress; `-32601` for an unknown method;
`-32602` for an unknown tool; and `-32000` when 16 tool calls are already in
progress. Calls other than `check_run` stop after 2 minutes.

### Running checks

`check_run` is on unless the server was started with `--no-run-check`. It runs
exactly what `owngit check run` runs without `--check`: the checks in the
`.owngit/checks.json` committed in the `HEAD` of `--workdir`, with the default
limits of 10 minutes and 65536 bytes of output per check. Arguments name only
the task and, for a verifying run, the reserved cycle. The checks run with the
user's permissions and environment and are not sandboxed. One run at a time is
allowed; a second call fails with `check_run_busy`.

The checks are commands that come from the repository. Anyone or anything that
can commit to the clone, or edit it (including its `.git/config`, attribute
files, and filters), can therefore make `check_run` start a program. That
program can be one of the committed checks, or a clean filter that runs while
the worktree is inspected. When an agent may edit files but must not run
commands, start the server with `--no-run-check`.

A cancellation from the coding tool, or the end of its input, stops the checks
and their child processes. The attempt is still recorded as cancelled before
the server stops, and a cancelled call gets no response.

With `--no-run-check`, `check_run` is left out of the tool list, and a call to
it is refused before anything runs. The other check tools remain, so an agent
can still read evidence, create tasks, and reserve rounds.

## Reading the result

`check run` prints one JSON object and exits with a code:

- `0`: every check passed. Read `registered` and `uploaded` to see whether the
  attempt was recorded. A local `--no-upload` run also exits 0.
- `1`: at least one check did not pass.
- `2`: this client could not confirm that the attempt was recorded.
- `130`: the run was cancelled.

If `check run` stops before running any check, it prints an error object,
`{"ok":false,"error":{"code":...,"message":...}}`, instead of a result and
exits 1. This happens for invalid arguments, a missing or invalid committed
configuration (`checks_not_configured`, `invalid_check_configuration`), and a
registration the server refused, such as an unreserved `--cycle`. Nothing ran
and nothing was recorded.

The JSON object carries `ok`, `registered`, `uploaded`, `attempt_id`,
`cycle_id`, `task`, `attempt`, `correction_cycles_remaining`, `results`, and
`upload_error`. The `attempt` object carries `status`, `revision_oid`,
`worktree_state`, `summary`, `cleanup_failed`, `log_truncated`, and the
execution limits. Each entry in `results` carries `name`, `command`, `status`,
`exit_code`, `duration_ms`, `output_excerpt`, `truncated`, and `cleanup_error`.

Per-check statuses are `passed`, `failed`, `error`, `cancelled`, `incomplete`,
and `unavailable`. The server recomputes the attempt status from the results
instead of trusting a helper-supplied aggregate. A cleanup error makes the
result `error` even when the command exit code is visible. Output beyond the check's output limit makes the
result `incomplete`; a shortened excerpt or log is marked as truncated and does
not change the status. An empty configured set is `unavailable`, never
`passed`. A registered attempt that never reports a completion stays visible as
`pending`.

`upload_error` means this client could not confirm the registration or
completion. A lost response can leave an accepted registration or completion on
the server, so inspect `check status` before repeating a reservation or a run,
and do not claim the attempt is absent. It is not the same as a recorded failed
check. A recorded failed check has a durable attempt with a `failed` result.
When the server cannot be reached at all, the checks still run, the command
exits 2, and `upload_error` names the cause, such as a refused connection or a
TLS error.

### The top-level correction count is not always a measurement

Top-level `correction_cycles_remaining` is filled in only from a server
response. It has no separate "unknown" value, so when no response set it, the
field still prints `0`. That `0` means the client did not read the budget, not
that the budget ran out. A run carries a measured budget only when it also
carries a `task` object; read `task.correction_cycles_remaining` there.

Two cases print the unmeasured `0`:

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

## Correction budget

The task budget is three automatic correction rounds. Reserve a round before
asking an agent to correct, and pass the `cycle.id` from the reserve response
as `--cycle` to the verifying run. A correction already authorized in the
active task may continue within the budget without asking the user again. Do
not start an unrequested fix. Reuse the stable task and cycle identifiers
instead of creating new ones. A reserved round is counted once, whether the
following check passes or fails, and retries inside the round reuse it. The
initial check and a manual rerun consume no round, and an unavailable or
cancelled run does not create one by itself. When the budget is exhausted, the
reserve command returns `correction_budget_exhausted`. Stop automatic
continuation and report the unresolved task. A manual check can still be
recorded after the budget is exhausted.

Only `check status`, or a reserve call that returns
`correction_budget_exhausted`, shows that the budget is exhausted. A top-level
`correction_cycles_remaining` of `0` in a run output does not, unless that same
run carries a `task` object.

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
  it as server-recorded evidence. Its output has no `task` object, and its
  top-level `correction_cycles_remaining` of `0` is an unread field, not a
  measured budget.
- Do not retry a failed check blindly, and do not weaken or replace the
  committed check configuration to make a check pass.
- Do not launch or resume a coding session, change a model, grant tools to a
  read-only reviewer, reload a team, or inspect authentication files or
  transcripts.
- Skill loading is not a guaranteed callback. Explicit invocation and the
  manual commands in this guide are the reliable paths.
- A reviewer with read-only access cannot run the checks. An authorized
  execution-capable participant runs them and supplies the result with its
  provenance. This does not grant tools, change a role, or state a team policy.

## Installed locations

The portable archives on GitHub Releases keep the resources at their
source-relative paths, so the guide's link to the skill resolves in an unpacked
archive:

- `docs/CODING_TOOLS.md`
- `docs/CODING_TOOLS.ko.md`
- `integrations/skills/owngit-checks/SKILL.md`

The unsigned macOS app prototype holds them under
`OwnGit.app/Contents/Resources/` at the same relative paths, and the Debian
prototype package installs them under `/usr/share/doc/owngit/`.

The Homebrew and npm packages install only the `owngit` command, the license
and the notices, not this guide or the skill file. With those installs, run
`owngit skill --install DIR`; the command carries the skill.

From an unpacked portable archive you can also copy it:

```sh
mkdir -p ~/.agents/skills
cp -R integrations/skills/owngit-checks ~/.agents/skills/
```

### Reaching the helper binary

The commands in this guide call `owngit`. A source build, a portable archive,
and the macOS app do not add it to `PATH`.

- Source build: `bin/owngit` in the checkout, as built in the README.
- Portable archive: run `./owngit` from the unpacked directory, or use its
  full path.
- macOS prototype app: `OwnGit.app/Contents/Resources/bin/owngit` inside the
  app bundle.
- Debian prototype package: `/usr/bin/owngit`, which is normally on `PATH`.

When the binary is not on `PATH`, give the coding tool the full path instead of
editing shell startup files on its behalf.

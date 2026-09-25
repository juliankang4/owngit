# Coding tool integration

<p align="center"><b>English</b> | <a href="CODING_TOOLS.ko.md">한국어</a></p>

OwnGit exposes project checks to coding tools through a versioned JSON command
line interface and a shared Agent Skill. The integration does not require MCP,
a daemon, or a session launcher. The coding tool runs the `owngit` binary in
the user's own environment and reads its JSON result.

The skill is an instruction, not an enforcement boundary. A coding tool can
ignore it, and the server records only what the helper actually submits.

## What the integration provides

- A stable task identity that survives revisions.
- Revision-bound check evidence recorded on the OwnGit server.
- An explicit correction budget of three rounds per task.
- A skill that tells an active coding session when to run the helper and how to
  read its result.

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

Plain HTTP does not encrypt transport. The helper refuses HTTP unless the user
passes `--accept-insecure-http` for that request. Do not add that flag on the
user's behalf.

## Skill discovery

The shared skill is
[integrations/skills/owngit-checks/SKILL.md](../integrations/skills/owngit-checks/SKILL.md).
Artifacts built by the release tool carry the skill and this guide in both
languages; see
[Installed locations](#installed-locations). Nothing installs a skill for you:
copy it yourself, or point the coding tool at its path.

Copy the `owngit-checks` directory, rather than linking to it, into a location
the coding tool scans:

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
owngit check status --task TASK_ID --server URL --repository ID --credential-file PATH
owngit check log --attempt ATTEMPT_ID --server URL --repository ID --credential-file PATH
owngit check config show --server URL --repository ID --credential-file PATH
owngit check cycle list --task TASK_ID --server URL --repository ID --credential-file PATH
```

## Command reference

`check task new` creates a task. Flags: `--title`, `--server`, `--repository`,
`--credential-file`, `--accept-insecure-http`.

`check run` executes checks and, unless `--no-upload` is set, records the
attempt. Flags: `--task` (required), `--cycle`, `--workdir` (default `.`),
`--timeout` (default 10 minutes), `--output-limit` (default 65536 bytes per
check), `--no-upload`, and repeatable `--check name=command`. `--timeout` and
`--output-limit` must be positive. The remote flags are required unless
`--no-upload` is set.

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
and the notices, not this guide or the skill. With those installs, copy the
skill from a source checkout or an unpacked release archive.

Copy the skill from whichever layout you have. For example, from an unpacked
portable archive:

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

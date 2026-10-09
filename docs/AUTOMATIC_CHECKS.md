# Automatic checks

<p align="center"><b>English</b> | <a href="AUTOMATIC_CHECKS.ko.md">한국어</a></p>

Automatic checks let OwnGit run a repository's tests by itself when someone
pushes or updates a pull request. This guide is for the server owner.

To turn them on:

1. Commit a [check file](#check-file), `.owngit/checks.json`, or a GitHub
   Actions workflow file in `.github/workflows/` to the repository. For
   workflow files, see [Workflows](WORKFLOWS.md).
2. Choose [where checks run](#where-checks-run) and save a
   [policy](#policy-and-turning-checks-on).
3. Turn checks on for that policy.

Results show on the repository's Checks tab and on each pull request. They are
advisory and never block a merge. To run checks from a coding tool on your own
computer instead, see [Coding tools](CODING_TOOLS.md).

## Check file

The repository chooses what to run and when. OwnGit reads the file from the
exact commit it checks.

```json
{
  "version": 1,
  "events": {
    "push": {"branches": ["main", "release/*"]},
    "pull_request": {}
  },
  "checks": [{"name": "unit", "command": "go test ./..."}],
  "limits": {"timeout_ms": 600000, "output_limit_bytes": 65536}
}
```

- `events` turns on `push`, `pull_request` or both. An empty event object,
  such as `"pull_request": {}`, matches every branch. A branch pattern is a name, or a name that ends in one `*`.
- `checks` holds 1 to 50 shell commands, each with a name.
- `limits` is optional. A check gets 10 minutes and 64 KiB of output unless
  the file asks for other values. OwnGit lowers a value above the policy's
  maximum.
- The file may be at most 64 KiB. OwnGit refuses unknown fields and invalid
  JSON.

The check file cannot choose where checks run, the container image, the
network or any limit of the policy. Only an administrator sets those.

## Policy and turning checks on

The administrator's policy says where checks run, which events may start them, and
the limits. Edit it in the browser on the repository's **Automatic checks**
screen, linked from its Checks and Settings tabs. The screen shows what to do
next, a check file to copy, and each limit with its range. Changes ask for
the administrator password.

**Save and turn checks on** shows each setting that would change before it
saves anything. Plain **Save** never turns checks on.

On the command line, write the policy to a file:

```json
{
  "executor": "host",
  "allowed_events": ["push", "pull_request"],
  "max_timeout_ms": 600000,
  "max_output_limit_bytes": 1048576,
  "queue_limit": 32,
  "max_active_jobs": 1,
  "max_lease_ms": 60000,
  "execution": {"source": {}}
}
```

`queue_limit` is how many jobs may wait, `max_active_jobs` how many run at
once, and `max_lease_ms` how long a claimed job may go without a sign of life
before its claim ends. `max_timeout_ms` and `max_output_limit_bytes` are the
most a check file may ask for; for workflows they limit each step.

`allowed_events` may also hold `workflow_dispatch` and `schedule`, which only
workflow files use. `run_workflows` turns workflow files on or off. A policy
saved for the first time has it on unless the file says `false`; a policy
saved before OwnGit 1.1.8 keeps it off until you turn it on. See
[Turn workflows on](WORKFLOWS.md#turn-workflows-on).

Then save it and turn checks on in one step:

```sh
owngit check-policy set --enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json
```

`check-policy show`, `enable` and `disable` take the same `--server`,
`--repository` and `--password-file` flags.

- Turning checks on approves exactly the saved policy. Saving a different
  policy turns checks off until you turn them on again.
- Nothing runs without both a matching check file or workflow file and an
  approved policy.
- `check-policy show` also says whether this computer can run checks now. If
  it reports `workspace_unavailable` or `restart_reconciliation_unavailable`,
  fix the cause it names and restart OwnGit. Git and merges keep working.
- The policy's maximums cannot exceed this computer's check ceilings. An
  administrator sets those in Settings; see [Operations](OPERATIONS.md).

## Where checks run

`executor` is one of three:

| Executor | Runs as | Use it for |
| --- | --- | --- |
| `host` | The OwnGit account, through the platform shell | Repositories you fully trust |
| `container` | A restricted container on this computer's Docker | Code you trust less, with resource limits |
| `external_runner` | A runner you start on another computer | Keeping checks off the OwnGit computer |

### Host

Host mode is not a sandbox. A check can reach everything the OwnGit account
can, including OwnGit's own data and secrets. Use it only for repositories
whose every committer you trust. A host check gets only a few environment
variables from the server; see [Environment variables](#environment-variables).

In host mode and with `owngit runner`, OwnGit stops the programs a check
started when the check ends. On Linux this includes programs that started a
new session. Start long-lived services outside OwnGit. For details by system,
see [Processes a check starts](CODING_TOOLS.md#processes-a-check-starts).

### Container

Container mode needs a Docker daemon on this computer that runs Linux
containers. A remote Docker is refused, so do not set `DOCKER_HOST`,
`DOCKER_CONTEXT` or the Docker TLS variables for OwnGit. Set `"executor":
"container"` and add the image and resource limits to `execution`:

```json
"execution": {
  "source": {},
  "container_image": "registry.example/checks@sha256:<64 hex digits>",
  "container_network": "none",
  "container_cpu_millis": 1000,
  "container_memory_bytes": 536870912,
  "container_pids": 256,
  "container_scratch_bytes": 536870912
}
```

The values shown for CPU, memory, processes and scratch space are the
defaults. By default, the image must be pinned by digest and the network is
`none` or `bridge`. The administrator can allow tags or select a Docker
network they created; see [Container options](#container-options).

By default, each check runs:

- on Linux and macOS, with the OwnGit process's UID and GID when it is not
  root; a root service and Windows use the fixed nonroot identity
  `65532:65532`, never the image's user;
- with a read-only root filesystem, all Linux capabilities dropped and
  `no-new-privileges`;
- with CPU, memory and process limits and no swap;
- with the job's copied source mounted at `/workspace` and a size-limited
  `/tmp`. Workflow jobs also mount their own scripts, command files and
  temporary folders, not the server's state or repositories.

There is no privileged mode, host network, Docker socket or arbitrary host
mount. On Windows, Docker must be able to mount the job's local folders and
let `65532:65532` use them. By default, OwnGit refuses images that declare
volumes and Docker setups that cannot enforce the limits. The options below
state the exceptions the administrator can allow.

#### Container options

Each option is off until you turn it on. Turning one on gives checks more
room, so read what it allows.

| Field | What it allows |
| --- | --- |
| `container_allow_tags` | A tag such as `registry.example/checks:1` instead of a digest. A tag can point to different code next time. Each job records the image ID it used. |
| `container_pull_missing` | Download a missing image before the job. Only public images work; no saved registry login is used. |
| `container_network` (any other name) | A Docker network you created. Checks can reach every service on it. `host` is never accepted. |
| `container_image_volumes` | Run an image that declares volumes. Each volume gets temporary in-memory space. |
| `container_writable_root` | Let commands change the container's own files. Changes are discarded with the container. |
| `container_missing_enforcement` | A list of `memory`, `swap`, `cpu` and `pids` limits that may go unenforced on this computer's Docker. |

When a job uses one of these, its output starts with the image ID and any
limits Docker did not enforce.

### External runner

A runner is `owngit runner` started on another computer. It runs the checks
as its own account, without a sandbox, so give it a dedicated account.

1. Save a policy with `"executor": "external_runner"`. Then issue a token
   for the repository into a new private file:

   ```sh
   owngit runner-credential issue \
     --server https://git.example.test \
     --repository project \
     --password-file ./admin-password \
     --label build-host \
     --token-file ./runner-token
   ```

2. Start the runner:

   ```sh
   owngit runner \
     --server https://git.example.test \
     --repository project \
     --token-file ./runner-token
   ```

The runner needs an HTTPS address for OwnGit, for example tailnet sharing or
a reverse proxy (see [Operations](OPERATIONS.md)). `--ca-file` adds a private
certificate authority.

- The runner works in a folder in its account's cache folder. Use
  `--workspace-root` for another empty folder that the account owns.
- It keeps retrying while OwnGit restarts or the network drops. It exits only
  when retrying cannot help, for example after its token was revoked.
- `owngit runner-credential list` and `revoke --credential ID` manage tokens.
  The server keeps only a hash of each token.
- After a repository is renamed, restart the runner with the new
  `--repository` within 90 days.
- A runner from OwnGit 1.1.7 or earlier runs only `.owngit/checks.json`
  jobs. Workflow jobs wait until a current runner takes them; see
  [Old runners](WORKFLOWS.md#old-runners).
- The read limits described under [What a check sees](#what-a-check-sees)
  belong to the computer that runs OwnGit. Before the runner downloads any
  file, OwnGit checks each file's size and the memory needed to rebuild it.
  If a file is over either limit, OwnGit refuses the file list with HTTP 422
  and `check_source_refused`.
- OwnGit checks each file again when the runner downloads it. A runner on a
  larger computer does not raise these limits.

To keep a runner running after a reboot, use the service manager. On Linux
with systemd:

```ini
[Unit]
Description=OwnGit runner for project
Wants=network-online.target
After=network-online.target

[Service]
User=owngit-runner
ExecStart=/usr/local/bin/owngit runner --server https://git.example.test --repository project --token-file /etc/owngit-runner/project-token --workspace-root /srv/owngit-runner/project
Restart=on-failure
RestartSec=60

[Install]
WantedBy=multi-user.target
```

Only the service account may read the token file. After a token is revoked,
issue a new one before you restart the service.

### Environment variables

Host checks and runner checks get a short, fixed list of environment
variables, not the whole environment of the OwnGit server or the runner. If a
command needs anything else, such as a proxy or a tool setting, set it in the
check command. `owngit check` from a coding tool follows the same rules.

A check gets these variables when the server or runner has them:

- on every system: `PATH`, `HOME`, `LANG`, `TZ`, `LC_ALL`, `LC_COLLATE`,
  `LC_CTYPE`, `LC_MESSAGES`, `LC_MONETARY`, `LC_NUMERIC` and `LC_TIME`;
- on Linux and macOS: `USER`, `LOGNAME` and `SHELL`;
- on Windows: the system, command shell, user profile, program folder and
  processor variables, such as `SystemRoot`, `ComSpec`, `PATHEXT`,
  `USERPROFILE`, `APPDATA`, `ProgramFiles` and `NUMBER_OF_PROCESSORS`. Names
  match in any letter case.

OwnGit also sets `CI=true`, and points `TMPDIR`, `TEMP` and `TMP` to a new
private temporary folder. On the server or runner, that folder sits in the
job's own folder, next to the copied files. `owngit check` creates it in the
system's temporary folder. All checks of one run share the folder, and OwnGit
removes it after the last check. If OwnGit cannot create the folder, every
check of the run ends as `error`. If it cannot remove the folder, the last
check ends as `error`.

Other variables are left out. This includes OwnGit's own settings, proxy
settings in upper or lower case (such as `HTTPS_PROXY` and `https_proxy`),
tool settings such as `JAVA_HOME`, and credentials. To give a command a value,
set it in the command itself:

- Linux and macOS: `HTTPS_PROXY=http://proxy.example.test:3128 go test ./...`
- Windows: `set "JAVA_HOME=C:\Tools\jdk" && gradlew test`

In `checks.json`, write each backslash as `\\`. The check file is committed with the repository, so do not put secrets in it.

When the command of one of these checks fails or cannot start, its log starts
with a note. The note lists the names of any variables OwnGit left out and asks
you to set the ones the command needs. It never shows values. It skips any
name that contains `TOKEN`, `SECRET`, `PASSWORD`, `KEY` or `CREDENTIAL` in any
letter case, and the list stops at 8 KiB. A cancelled check gets no note. If
OwnGit cannot create or remove the temporary folder, it reports that as its own
error and does not add the note for it.

These rules only choose the variables a check sees. They are not a sandbox: a
check can still read and change everything its account can.

Workflow jobs on the host get the same list, plus the `GITHUB_*` variables
and the workflow's `env` (see [What the job has](WORKFLOWS.md#what-the-job-has)).

Container checks do not use this list. A command in the container gets the
variables its image defines, plus fixed values from OwnGit: `HOME`, `TMPDIR`,
`TMP`, `TEMP` and `GOTMPDIR` set to `/tmp`, `XDG_CACHE_HOME` set to
`/tmp/.cache` and `GOCACHE` set to `/tmp/go-build`. OwnGit also sets
`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `FTP_PROXY`, `ALL_PROXY` and their
lower-case forms to empty values, so proxy settings from the image or from the
server's Docker client configuration do not reach the command. OwnGit's own
`docker` commands still use the server's environment, which is why the Docker
variables under [Container](#container) matter.

Workflow jobs in a container get the same proxy variables, but `HOME`, the
temporary folders and the caches point to the job's own folder, which keeps
its changes from one step to the next (see
[What the job has](WORKFLOWS.md#what-the-job-has)).

## Results and output limits

A result may record a positive `output_limit_exceeded_bytes`: the applied
execution limit that was exceeded. Zero or absence means not stated;
`truncated` alone can mean excerpt clipping. The dashboard shows verified
result counts and positive-limit notices in English or Korean. API summaries,
raw output and logs remain recorded text. Log expiry on pages uses
server-local time; API instants, Git's recorded offsets and UTC schedules do
not change. Custom helper and runner clients must follow the
[capability and upload rules](CODING_TOOLS.md#output-limits-and-client-compatibility).

## Jobs

OwnGit creates a job for each push or pull request update that matches the
check file. Git never waits for checks.

- When you turn push checks on, OwnGit queues each matching branch head that
  has no job yet.
- At each start, OwnGit queues matching branch heads that never got a job.
- A job goes from `pending` to `claimed` and `started`, and ends as `passed`,
  `failed`, `error`, `cancelled`, `incomplete`, `unavailable`, `ambiguous` or
  `interrupted`. A workflow job can also wait as `waiting` for the jobs it
  needs, and end as `skipped`.
- OwnGit never requeues a job that started, because its commands may already
  have run. Run it again yourself.
- A check that changes a tracked file does not get a clean result.

Some `.owngit/checks.json` admission refusals are kept as an unavailable
attempt with an Admission result and the reason, even though no command ran.
For example, an invalid check file or a full queue can leave this evidence.
It uses no execution or queue slot and is not a passing check. Read it under Checks, Tasks or through the task API, CLI or MCP. Other
workflow files from the same event can still run.

Work with jobs on the Automatic checks screen or on the command line:

```sh
owngit check-job list   --server https://git.example.test --repository project --password-file ./admin-password
owngit check-job show   ... --job JOB_ID
owngit check-job log    ... --job JOB_ID
owngit check-job cancel ... --job JOB_ID
owngit check-job rerun  ... --job JOB_ID
```

`...` stands for the same three flags. `list` shows the newest 100 jobs. A
rerun checks the same commit with the same commands, under the current
policy's maximums. Raw logs are kept as set under
[Raw check logs](#raw-check-logs).

Workflow jobs belong to runs. Cancel or rerun them as a whole run with
`owngit workflow-run`, which needs only general access; `check-job rerun`
refuses a single workflow job. See
[Runs, cancel and rerun](WORKFLOWS.md#runs-cancel-and-rerun).

## What a check sees

Before a job runs, OwnGit copies the committed files of that exact commit into
a new private folder.

- There is no `.git` folder, so commands that need Git history do not work.
- Git attributes, filters, hooks and line-ending conversion are not applied.
- Symbolic links and submodules are refused. A repository that tracks them
  cannot use automatic checks.
- Git LFS files arrive as pointer files.
- On a Linux computer with little memory, OwnGit refuses a file larger than
  one Git process may read: about 16 MiB with 512 MiB of memory and 32 MiB
  with 1 GiB. The limit grows with memory, up to 512 MiB. It also refuses a
  file that needs more memory to rebuild from stored deltas than this
  computer allows (see
  [Memory on a small Linux host](OPERATIONS.md#memory-on-a-small-linux-host)).
  The job then ends without running.

The policy's `execution.source` limits what is copied. Leave it empty for the
defaults:

| Field | Default |
| --- | --- |
| `max_entries` | 20000 tree entries |
| `max_file_bytes` | 64 MiB per file |
| `max_total_bytes` | 256 MiB in total |
| `max_path_depth` | 64 folders deep |
| `max_path_bytes` | 1024 bytes per path |
| `max_name_bytes` | 255 bytes per name |
| `metadata_limit_bytes` | 16 MiB of tree listing |

## Raw check logs

The server keeps the raw log of each check (up to 256 KiB), from jobs and
from coding tool runs alike, for 30 days. `owngit check-job log` shows this log.
If a check prints more than the log can hold, the log keeps the beginning and
the end of the output, with one line between them that says how many bytes
were left out, so the last lines stay visible. The job page shows at most
64 KiB of the log, shortened the same way; its line counts what it leaves out
of the stored log. A check stopped at its output limit has no output after
that point. To change
how long logs are kept, open Settings, Storage & recovery, Raw check logs, or run:

```sh
owngit settings set --check-logs 90d
```

The choices are `7d`, `30d`, `90d`, `365d` and `indefinite`. A shorter time
applies at once to the logs already kept. Results stay after their log
expires.

## Backup and restore

Backups keep policies, jobs, workflow runs and results. They do not keep
runner tokens, workflow secrets, workflow schedules or the records of which
pushes OwnGit accepted. After a restore:

- checks are off until you turn them on again;
- runner tokens must be issued again;
- workflow secrets must be entered again;
- jobs that had not finished are marked `interrupted`;
- schedules start again from the workflow files once checks are on, and they
  wait for the next accepted push to the default branch.

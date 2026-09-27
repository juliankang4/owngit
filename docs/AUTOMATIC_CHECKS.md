# Automatic checks

<p align="center"><b>English</b> | <a href="AUTOMATIC_CHECKS.ko.md">한국어</a></p>

This page is for the owner who wants OwnGit to run a repository's checks by
itself on pushes and pull requests. A repository commits a check file, the
owner saves an execution policy and enables it, and OwnGit runs the checks as
its own account on the host, in a restricted local Docker container, or on a
separately connected runner. Results are advisory and never hold a merge.

## Workflow file

A repository opts in with a committed `.owngit/checks.json`, which OwnGit
reads from the exact commit being checked (a manual helper run without
`--check` runs the same file; see [Coding tools](CODING_TOOLS.md)):

```json
{
  "version": 1,
  "events": {
    "push": {"branches": ["main", "release/*"]},
    "pull_request": {}
  },
  "checks": [{"name": "unit", "command": "go test ./..."}],
  "limits": {"timeout_ms": 60000, "output_limit_bytes": 65536}
}
```

- `events` may enable `push` and `pull_request`. An empty event object
  selects every branch; `branches` accepts literal names or a name ending in
  one `*`, at most 64 patterns of 200 bytes per event.
- `checks` holds 1 to 50 named shell commands, names up to 100 bytes and
  commands up to 24000 bytes.
- `limits` is optional; OwnGit applies defaults and lowers a value that
  exceeds the owner's policy.

Invalid UTF-8, unknown fields, duplicate keys, missing required fields,
malformed branch patterns, and files over 64 KiB are refused. Repository
content cannot choose the executor, container image, network, resource limits,
source limits, runner credential, or consent; those come from the owner's
policy.

## Owner policy and consent

A policy selects the executor (`host`, `container`, or `external_runner`),
allowed events, execution limits, queue and concurrency limits, a lease
duration, and [source limits](#source-limits). A container policy also names
an immutable image, such as `sha256:<64 lowercase hex digits>` or
`registry.example/checks@sha256:<64 lowercase hex digits>`, a `none` or
`bridge` network, and CPU, memory, process, and scratch-space limits. A minimal
host or external-runner policy leaves `execution.source` empty for the default
source limits:

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

Save it and enable it:

```sh
owngit check-policy set \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json

owngit check-policy enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password
```

`check-policy enable` approves the exact policy version that is stored, so
saving a different policy turns execution off until you enable it again.
`check-policy disable` stops new jobs, and a restore turns execution off.
Nothing runs without both a matching workflow file and current consent.
`check-policy show` prints the policy and whether the check runtime is
available: if OwnGit cannot set up its private check workspace or finish its
startup recovery, it keeps Git, setup, and merges working, stops automatic and
runner execution, and reports `workspace_unavailable` or
`restart_reconciliation_unavailable`; fix the cause and restart OwnGit.

## Browser screens

The same operations are on two administrator screens, and every change asks
for the administrator password.

The **Automatic checks** screen, `/repositories/{id}/configured-checks`,
linked from the repository's Checks and Settings tabs, edits the policy, turns
execution on or off, and lists jobs. It opens with a status summary (whether
checks are on, where and when they run, whether the default branch has a valid
`.owngit/checks.json`, whether a policy is stored and the environment is
available, and the next thing to do) and walks through five steps: where
checks run, when they run, the check file (with a minimal example to copy),
saving, and turning checks on. Enabling approves the policy version shown on
screen; if someone saved a different policy in the meantime, the request is
refused with 409.

Limits sit under **Advanced limits** with working values filled in. Times take
seconds, minutes, or hours and sizes bytes, KB, MB, or GB (1 KB is 1024
bytes); a time must come to whole milliseconds, a size to whole bytes, and a
CPU amount to at most three decimal places; each field shows its range and
default. The time and output limits are maximums: a check gets 10 minutes and
keeps 64 KiB of output unless its check file asks for a different value under
`limits`, up to these maximums.

A job page shows the commit, executor, workflow path, configuration and policy
versions, and admission timestamps. It offers Cancel while a job is pending,
claimed, or running (a recorded cancellation is a request and does not prove
that the process stopped) and Run again once it has finished, and says whether
each attempt was a manual helper run or an automatic job.

`/repositories/{id}/runner-tokens` issues and revokes runner tokens, once a
policy is stored. The token value appears once, in the response that issues
it, and never in a URL, log, or browser storage; revoked tokens stay listed as
a record of who had access.

## Jobs

OwnGit notices pushes, pull request updates, and merges, reads the workflow
file from the exact commit, and admits a job for each matching event; Git
writes never wait for checks, and the same event never creates a second job.
When checks are enabled for the first time, each matching branch head that
never had a job is queued once, within the queue limit. Saving or enabling a
new policy version queues nothing that already had a job and marks jobs still
waiting to start `interrupted`; to check an existing head under the new
policy, rerun its job.

A job moves from `pending` to `claimed`, `started`, and a result: `passed`,
`failed`, `error`, `cancelled`, `incomplete`, `unavailable`, `ambiguous`, or
`interrupted`. Once a job has started, OwnGit never queues it again by itself,
even after a restart or a lost lease, because the commands may already have
run. Request a rerun instead:

```sh
owngit check-job list --server https://git.example.test --repository project --password-file ./admin-password
owngit check-job show --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job log --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job cancel --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job rerun --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
```

`check-job list` returns the newest 100 jobs; `check-job log` reads the raw
log, which expires while the result stays; `check-job cancel` on a finished
job changes nothing and fails with `check_job_finished`. A failure before
start is recorded as `unavailable`, `error`, or `interrupted`. A push that
holds the repository while a job copies its source only delays it (up to 10
minutes for a check OwnGit runs, 20 seconds per source request from a runner)
before it is recorded as `unavailable`. After the commands run, OwnGit checks
every tracked file again: generated untracked files are fine, but a changed,
removed, or mode-changed tracked file prevents a clean result, and so does a
container or process that OwnGit cannot confirm was cleaned up.

## Source a check sees

Before a job runs, OwnGit copies the exact committed files into a new private
directory, readable only by the account that runs the check; a runner
receives the same files over its authenticated connection.

- Bytes are exact: Git attributes, filters, hooks, and line-ending conversion
  are not applied, and every file is checked against its Git object ID.
- There is no `.git` directory, index, or empty directory, so commands that
  need Git metadata will not find it.
- Symbolic links and submodules are refused, so a repository that tracks them
  cannot use automatic checks; the job reports this before any command runs.
  Unsafe paths (`..`, absolute paths, `.git` spelled another way), duplicate
  paths, and names that collide on a case-insensitive or Unicode-normalizing
  filesystem are refused too.
- The Git file mode is recorded, but Windows cannot apply the execute bit.
- Git LFS pointer files are copied as they are; large-file content is never
  fetched.

### Source limits

The policy's `execution.source` object bounds what is copied; a job over any
limit is refused before a file is written. Zero or missing values use the
defaults, and `max_total_bytes` cannot be smaller than `max_file_bytes`. The
Automatic checks screen shows each field's range and default.

| Field | Default |
| --- | --- |
| `max_entries` | 20000 tree entries, including refused ones |
| `max_file_bytes` | 64 MiB per file |
| `max_total_bytes` | 256 MiB in total |
| `max_path_depth` | 64 path components |
| `max_path_bytes` | 1024 bytes per path |
| `max_name_bytes` | 255 bytes per name |
| `metadata_limit_bytes` | 16 MiB of tree listing |

## Executors

### Host

Host mode runs commands through the platform shell as the OwnGit account. It
is not a sandbox: a command can reach anything that account can, including
OwnGit's state and credentials. Use it only for fully trusted repositories.

### Restricted local Docker

Container mode uses only a local Linux Docker daemon, selected without
`DOCKER_HOST`, `DOCKER_CONTEXT`, or TLS overrides, and never pulls an image. It
runs the owner's pinned image with:

- a nonroot user and a read-only root filesystem;
- all Linux capabilities dropped and `no-new-privileges`;
- the selected `none` or `bridge` network;
- CPU, memory, and process limits, with no swap;
- a size-limited, executable `/tmp`;
- only the job's source directory mounted, read-write, at `/workspace`, which
  shares the host disk and has no separate size limit;
- Docker logging turned off.

OwnGit refuses an image that declares volumes, and a daemon that does not
enforce the memory, swap, CPU, and process limits.

### External runner

External-runner jobs run only on a runner that you connect. Issue a
repository-scoped token into a new owner-only file, then start the runner:

```sh
owngit runner-credential issue \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --label build-host \
  --token-file ./runner-token

owngit runner \
  --server https://git.example.test \
  --repository project \
  --token-file ./runner-token \
  --workspace-root /srv/owngit-runner/project
```

The token file starts with the line `owngit-server: ORIGIN` for the server that
issued it, and `runner` refuses it for any other `--server` (see
[Credential files and the server line](CODING_TOOLS.md#credential-files-and-the-server-line)).
`owngit runner-credential list` and `owngit runner-credential revoke
--credential ID` manage tokens; the server stores only a hash, and revoking a
token stops its use and interrupts a claimed job that has not started.

`runner` and `runner-credential` require HTTPS, so put a TLS-terminating proxy
in front of OwnGit (`--ca-file /path/to/private-ca.pem` trusts a private
certificate authority in addition to the system roots). The proxy must send a
Host that OwnGit accepts (a name approved with `--allowed-host` or
`approve-host`, or `localhost`, `127.0.0.1` or `::1` when it runs on the same
computer; see
[Reaching the server from another device](OPERATIONS.md#reaching-the-server-from-another-device))
and pass large bodies without a size cap. OwnGit refuses browser changes that
arrive through such a proxy, so use it for runners and open the browser
interface directly. Plain HTTP with `--accept-insecure-http` is accepted only
for a loopback address.

The runner claims jobs for its repository only, downloads the exact source
files, runs the commands as its own account (not sandboxed), cleans its
workspace, and reports the results. `--workspace-root` must be an absolute
path to a folder that is empty or was used by an OwnGit runner before and
belongs to the runner's account; without it, the runner makes one for the
server and repository in the account's cache folder (`~/.cache/owngit` or
`$XDG_CACHE_HOME/owngit` on Linux, `~/Library/Caches/owngit` on macOS,
`%LOCALAPPDATA%\owngit` on Windows), and a workspace that OwnGit 1.1.0 or
earlier made in the temporary folder stays in use while it exists. The runner
refuses a root, and says how to fix it, when the root is nonempty and OwnGit
does not own it, when another runner is using it, when another account owns
it, or, on Linux and macOS, when another account could rename or replace a
folder above it. It warns when it runs as root; run it as a dedicated account,
as in the service example below.

The runner keeps running while OwnGit restarts or the network drops: it
retries with a growing delay of up to one minute (longer only when OwnGit
sends `Retry-After`) and logs the outage once and the recovery once. A job
that was running during the outage may end without a confirmed result; the
runner logs one line with the job ID and the reason, and OwnGit marks the job
`ambiguous` when its lease expires. The runner stops with exit status 1 only
when retrying cannot help: its token is unknown or revoked, belongs to another
repository than `--repository`, or the server refuses the request as invalid.
With `--once` it claims at most one job, makes a single attempt, and exits 1
on any failure, including an unreachable server.

To keep a runner available after a reboot, run it under the system's service
manager. On Linux with systemd, for example:

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

The token file must be readable only by the service account. On macOS use a
launchd agent or daemon, and on Windows a service wrapper, with the same
command. After a revoked token, `Restart=on-failure` only repeats the same
error, so issue a new token first.

## Backup and restore

Offline backups include policies, jobs, and results, but not runner tokens.
After a restore, execution is off until the owner enables it again, runner
tokens must be issued again, and unfinished jobs are marked `interrupted`
instead of running again. Policies and jobs from older OwnGit versions are kept
as history and cannot run until the owner saves and enables a current policy.

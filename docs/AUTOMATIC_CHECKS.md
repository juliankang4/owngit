# Automatic checks

<p align="center"><b>English</b> | <a href="AUTOMATIC_CHECKS.ko.md">한국어</a></p>

OwnGit can run checks that a repository configures, after the owner saves an
execution policy and explicitly enables it. Checks run as the OwnGit account on
the host, in a restricted local Docker container, or on a separately connected
runner. Results are advisory and never hold a merge.

## Workflow file

A repository opts in with a committed `.owngit/checks.json`. OwnGit reads it
from the exact commit being checked. A manual helper run without `--check`
also runs the checks from the file committed in the revision it tests; see
[Coding tools](CODING_TOOLS.md). Invalid UTF-8, unknown fields, duplicate
keys, missing required fields, malformed branch patterns, and files over 64 KiB
are refused.

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
  selects every branch. `branches` accepts literal names or a name ending in
  one `*`, with at most 64 patterns of 200 bytes per event.
- `checks` holds 1 to 50 named shell commands. Names are at most 100 bytes and
  commands at most 24000 bytes.
- `limits` is optional. OwnGit applies defaults and lowers a value that exceeds
  the owner's policy.

Repository content cannot choose the executor, container image, network,
resource limits, source limits, runner credential, or consent. Those come from
the owner's policy.

## Owner policy and consent

A policy selects the executor (`host`, `container`, or `external_runner`),
allowed events, execution limits, queue and concurrency limits, a lease
duration, and [source limits](#source-limits). A container policy also names an
immutable image, such as `sha256:<64 lowercase hex digits>` or
`registry.example/checks@sha256:<64 lowercase hex digits>`, a `none` or
`bridge` network, and CPU, memory, process, and scratch-space limits.

Save a policy from a JSON file and enable it:

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

A minimal host or external-runner policy can leave `execution.source` empty to
use the default source limits:

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

`check-policy enable` approves the exact policy version that is stored.
Saving a different policy turns execution off until you enable it again.
`check-policy disable` stops new jobs, and a restore also turns execution off.
Nothing runs without both a matching workflow file and current consent.

`check-policy show` prints the policy and whether the check runtime is
available. If OwnGit cannot set up its private check workspace or finish its
startup recovery, it keeps Git, setup, and merges working, stops automatic and
runner execution, and reports `workspace_unavailable` or
`restart_reconciliation_unavailable`. Fix the cause and restart OwnGit. There
is no background retry and no fallback to another executor.

## Browser screens

Two administrator screens offer the same operations. Every change asks for the
current administrator password.

`/repositories/{id}/configured-checks` (the **Automatic checks** screen, linked
from the repository's Checks and Settings tabs) edits the policy, turns
execution on or off, and lists jobs. It opens with a status summary: whether
checks are on, where and when they run, whether the default branch has a valid
`.owngit/checks.json`, whether a policy is stored, whether the execution
environment is available, and the next thing to do. The setup follows five
steps: choose where checks run, choose when they run, add a check file (the
screen shows a minimal example you can copy), save the settings, and turn
checks on. Container settings appear only when the container is selected.

Limits sit under **Advanced limits** with working values filled in. Times are
entered in seconds, minutes, or hours and sizes in bytes, KB, MB, or GB, where
1 KB is 1024 bytes. Each field shows its accepted range and, where the backend
has one, its default. A refused value is explained next to its field. An
amount that does not convert to a whole number of milliseconds, bytes, or
thousandths of a core is refused. The time and output limits are maximums: a
check gets 10 minutes and keeps 64 KiB of output unless its check file asks
for a different value under `limits`. In container and runner modes the next
step never promises that checks will run, because the screen cannot see
whether Docker or a runner is ready. Enabling approves the policy version shown
on screen; if someone saved a different policy in the meantime, the request is
refused with 409.

A job page shows the commit, executor, workflow path, configuration and policy
versions, and timestamps captured when the job was admitted, so an older job
shows what applied to it. Cancel is available while a job is pending, claimed,
or running, and a recorded cancellation is a request, not proof that the
process stopped. Run again is available once a job has finished. Each attempt says
whether it was a manual helper run in someone's own environment or an
automatic job, and neither is described as a sandbox. The screen distinguishes
a missing record from one that could not be read.

`/repositories/{id}/runner-tokens` issues and revokes runner tokens. A token
can be issued only after a policy is stored. The token value appears once, in
the response that issues it, and never in a URL, log, or browser storage.
Revoked tokens stay listed as a record of who had access.

## Jobs

OwnGit notices pushes, pull request updates, and merges, reads the workflow
file from the exact commit, and admits a job for each matching event. Git
writes never wait for checks. Seeing the same event again does not create a
second job.

Saving or enabling a new policy version does not queue branch heads or pull
request revisions that already had a job, even if that job ran under an older
version. Only new pushes and new pull request revisions are queued. When checks
are enabled for the first time, each matching branch head that never had a job
is queued once, within the queue limit. Saving a new policy version also marks
jobs that are still waiting to start `interrupted`, and they are not queued
again. To check an existing head under the new policy, rerun its job with
`owngit check-job rerun`.

A job moves from `pending` to `claimed`, `started`, and a result: `passed`,
`failed`, `error`, `cancelled`, `incomplete`, `unavailable`, `ambiguous`, or
`interrupted`. Once a job has started, OwnGit never queues it again
automatically, even after a restart or a lost lease, because the commands may
already have run. Request a rerun instead:

```sh
owngit check-job list --server https://git.example.test --repository project --password-file ./admin-password
owngit check-job show --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job log --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job cancel --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job rerun --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
```

`check-job list` returns the newest 100 jobs. `check-job log` reads the raw
log, which expires; the job's result stays after that. `check-job cancel` on a
job that already finished changes nothing, records no cancellation, and fails
with `check_job_finished`.

A failure before start is recorded as `unavailable`, `error`, or
`interrupted`. A push or another write that holds the repository while a job
copies its source only delays the job: it waits up to 10 minutes for a check
that OwnGit runs, and up to 20 seconds per source request from a runner,
before it is recorded as `unavailable`. After the commands run, OwnGit checks every tracked file again.
Generated untracked files are fine, but a changed, removed, or mode-changed
tracked file prevents a clean result. If OwnGit cannot confirm that the job's
processes or container were cleaned up, the job does not pass.

## Source a check sees

Before a job runs, OwnGit copies the exact committed files into a new private
directory. The runner receives the same files over its authenticated
connection.

- Bytes are exact. Git attributes, filters, hooks, and line-ending conversion
  are not applied, and every file is checked against its Git object ID.
- The directory has no `.git` directory, no index, and no empty directories.
  Commands that need Git metadata will not find it.
- Symbolic links and submodules are refused, so a repository that tracks them
  cannot use automatic checks. The job reports this before any command runs.
- Unsafe paths (such as `..`, absolute paths, or `.git` spelled another way),
  duplicate paths, and names that collide on a case-insensitive or
  Unicode-normalizing filesystem are refused.
- The Git file mode is recorded, but Windows cannot apply the execute bit.
- Git LFS pointer files are copied as they are. Large-file content is never
  fetched, so a check that needs it must get it another way.
- For SHA-1 repositories, the object check detects corruption, not a deliberate
  hash collision.

The directory is readable only by the account that runs the check. It is not a
sandbox: any process running as that account can read or change it.

### Source limits

The policy's `execution.source` object bounds what is copied. A job over any
limit is refused before a file is written. Zero or missing values use the
defaults:

| Field | Default |
| --- | --- |
| `max_entries` | 20000 tree entries, including refused ones |
| `max_file_bytes` | 64 MiB per file |
| `max_total_bytes` | 256 MiB in total |
| `max_path_depth` | 64 path components |
| `max_path_bytes` | 1024 bytes per path |
| `max_name_bytes` | 255 bytes per name |
| `metadata_limit_bytes` | 16 MiB of tree listing |

`max_total_bytes` cannot be smaller than `max_file_bytes`. The Automatic checks
screen shows the accepted range and default of each field.

## Executors

### Host

Host mode runs commands through the platform shell as the OwnGit account. It is
not a sandbox. A command can reach anything that account can, including
OwnGit's state and credentials. Use it only for fully trusted repositories.

### Restricted local Docker

Container mode uses only a local Linux Docker daemon, selected without
`DOCKER_HOST`, `DOCKER_CONTEXT`, or TLS overrides. OwnGit never pulls an image.
It runs the owner's pinned image with:

- a nonroot user and a read-only root filesystem;
- all Linux capabilities dropped and `no-new-privileges`;
- the selected `none` or `bridge` network;
- CPU, memory, and process limits, with no swap;
- a size-limited, executable `/tmp`;
- only the job's source directory mounted, read-write, at `/workspace`, which
  shares the host disk and has no separate size limit;
- Docker logging turned off.

OwnGit refuses an image that declares volumes, and a daemon that does not
enforce the memory, swap, CPU, and process limits. Containment still depends on
the Docker daemon, the kernel, and a trusted image.

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

`runner` and `runner-credential` require HTTPS. OwnGit serves plain HTTP, so
put a TLS-terminating proxy in front of it. `--ca-file /path/to/private-ca.pem`
trusts a private certificate authority in addition to the system roots. The
proxy must:

- send a Host that OwnGit accepts: `localhost`, `127.0.0.1`, `::1`, or a name approved with
  `--allowed-host` or `approve-host` (see
  [Reaching the server from another device](OPERATIONS.md#reaching-the-server-from-another-device));
- pass large request and response bodies without a size cap.

OwnGit refuses browser changes that arrive through an HTTPS proxy, so use the
proxy for runners and open the browser interface directly. Plain HTTP with
`--accept-insecure-http` is accepted only for a loopback address.

The runner claims jobs for its repository only, downloads the exact source
files, runs the commands, cleans its workspace, and reports the results. The
workspace root must be an absolute path that is empty or was used by an OwnGit
runner before. Without `--workspace-root`, the runner picks a temporary
directory. A nonempty root that OwnGit does not own, or one another runner is
using, is refused and left untouched. The runner never receives repository
storage paths. Commands run as the runner's account and are not sandboxed
unless you confine that account or machine yourself.

List or revoke runner tokens with `owngit runner-credential list` and
`owngit runner-credential revoke --credential ID`. The server stores only a
hash of each token. Revoking a token stops its use and interrupts a claimed job
that has not started.

The runner keeps running while OwnGit restarts or the network drops. When
OwnGit does not answer, times out, returns a server error, or asks it to wait,
the runner retries with a growing delay of up to one minute (longer only when
OwnGit sends `Retry-After`). It logs the outage once and the recovery once. A
job that was running during the outage may end without a confirmed result. The
runner then logs one line with the job ID and the reason, and OwnGit marks the
job `ambiguous` when its lease expires; check it with `owngit check-job show`.
The runner stops with exit status 1 and a message only when retrying cannot
help: its token is unknown or revoked, the token belongs to a repository other
than the one given with `--repository`, or the server refuses the request as
invalid. With `--once` the runner claims at
most one job, makes a single attempt, and exits with status 1 on any failure,
including an unreachable server.

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
command. `Restart=on-failure` restarts the runner after a crash; after a
revoked token it only repeats the same error, so issue a new token first.

## Backup and restore

Offline backups include policies, jobs, and results, but not runner tokens.
After a restore, execution is off until the owner enables it again, runner
tokens must be issued again, and unfinished jobs are marked `interrupted`
instead of running again. Policies and jobs from older OwnGit versions are kept
as history and cannot run until the owner saves and enables a current policy.

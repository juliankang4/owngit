# GitHub Actions workflows

<p align="center"><b>English</b> | <a href="WORKFLOWS.ko.md">한국어</a></p>

OwnGit runs the GitHub Actions workflow files in your repositories, the
`.github/workflows/*.yml` and `*.yaml` files, on its own
[automatic checks](AUTOMATIC_CHECKS.md). Nothing is sent to GitHub and no
action is downloaded. This guide is for people who push to an OwnGit
repository and for the administrator who runs the server.

Workflow support requires OwnGit 1.1.8 or later. Workflows that build and test
with supported `run` steps can run without GitHub. Steps
that use third-party actions must become `run` steps; see
[Replacing actions with run steps](#replacing-actions-with-run-steps).

To start:

1. The administrator sets up automatic checks for the repository: where jobs
   run, the limits, and the events that may start them. See
   [Automatic checks](AUTOMATIC_CHECKS.md#policy-and-turning-checks-on).
2. The administrator turns workflows on for the repository. A check policy
   first saved with OwnGit 1.1.8 or later enables workflows by default,
   unless explicitly disabled.
   See [Turn workflows on](#turn-workflows-on).
3. Push a commit that has a workflow file. OwnGit reads the files from that
   commit and starts the runs that match.

Results show on the repository's Checks tab, on each pull request and on the
repository page. Like other checks, they never block a merge.

## Turn workflows on

The check policy decides whether workflows run, as it does for
`.owngit/checks.json`. Workflows need all of these:

- `run_workflows` is on in the policy;
- checks are turned on for that exact policy (the administrator's consent);
- the event is in the policy's `allowed_events`: `push`, `pull_request`,
  `workflow_dispatch` or `schedule`.

The Checks tab has Tasks, Workflows and Runs views. Tasks holds check tasks,
Workflows lists files, and Runs holds workflow execution records. The Workflows
view lists the files found on a branch, starting with the default branch.
For each file it shows its triggers and jobs, which jobs would run, which are
refused or only noted, and runs that could never fit the queue.
If workflows are off, one button turns them on after you check this list. It
uses the administrator confirmation setting under Settings, Access.

On the command line, add `"run_workflows": true` and the events you want to
the policy file, then save it and turn checks on in one step:

```json
{
  "executor": "host",
  "allowed_events": ["push", "pull_request", "workflow_dispatch", "schedule"],
  "run_workflows": true,
  "max_timeout_ms": 600000,
  "max_output_limit_bytes": 1048576,
  "queue_limit": 32,
  "max_active_jobs": 1,
  "max_lease_ms": 60000,
  "execution": {"source": {}}
}
```

```sh
owngit check-policy set --enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json
```

- A policy saved for the first time with 1.1.8 or later has `run_workflows`
  on unless the file says `false`. A policy saved with an earlier version keeps
  it off after the upgrade, so files written for GitHub do not start running
  on their own.
- Changing `run_workflows` changes the policy, so checks stay off until you
  turn them on again. `--enable` does both in one step.
- Turning workflows on starts nothing by itself. The next push, pull
  request update, schedule time or manual start does.
- `owngit workflow list` and `owngit workflow show --path FILE` print the same
  review as the Workflows view.

## What runs

### Events

OwnGit starts a run for one workflow file and one event:

| Event | When |
| --- | --- |
| `push` | A branch push that OwnGit accepted from a Git client. |
| `pull_request` | A new or updated pull request whose source commit came from such a push. |
| `workflow_dispatch` | Someone starts the workflow by hand. See [Run a workflow by hand](#run-a-workflow-by-hand). |
| `schedule` | A cron time on the default branch. See [Schedules](#schedules). |

Each matching file gets its own run, and a file that OwnGit cannot run refuses
only its own run. Other GitHub events, such as `release` or `issues`, are
listed in the Workflows view as not run by OwnGit, and the file's other
triggers still work.

Only pushes that OwnGit authorized and accepted carry workflow authority. An
import refresh, a restore or any other change to a branch never starts a run
by itself and never reads the repository's secrets. A pull request whose
source commit arrived through an import starts no run, and OwnGit notes that
a new push is needed. Manual dispatch and schedules also require a retained
accepted push for that repository, branch ref and current commit. An imported
or restored branch without that record cannot run workflows by manual dispatch.
Push a new commit to OwnGit first.

### Filters

- `branches`, `branches-ignore`, `paths` and `paths-ignore` follow GitHub's
  pattern rules, including `**` and `!` patterns. For `pull_request`, branch
  filters match the target branch.
- `tags` and `tags-ignore` are read, but OwnGit does not run workflows for tag
  pushes. A push trigger with only tag filters never starts a run.
- `pull_request` `types`: OwnGit sends `opened` for the first revision of a
  pull request and `synchronize` for every later one. It never sends
  `reopened`, `closed` or the other types.
- Changed files for `paths` come from a two-dot diff for a push and a
  three-dot diff for a pull request, as on GitHub. A new branch is compared
  with its merge base on the default branch; a first push, or a branch with
  no common commit, counts every file as changed.

When OwnGit cannot list the changed files completely (more than 3,000 files,
more than 1 MiB of file names, more than 10 seconds, or no usable base
commit), it decides only when a listed file already decides. Otherwise the
run is recorded as not run, with the reason. Rerun it to run it without the
filter.

### Jobs

- Each job, and each matrix combination, is one check job. A run has at most
  16 jobs; a larger run is refused with advice on how to split it.
- `needs` makes a job wait, with the status `waiting`, for every combination
  of the jobs it names.
- `if` on jobs and steps follows GitHub: without a status function, it is
  checked as `success() && (...)`. `always()`, `failure()` and `cancelled()`
  work as on GitHub.
- `continue-on-error`, `timeout-minutes` (default 360 minutes for a job),
  `strategy.fail-fast` (default on), `strategy.max-parallel` and
  `concurrency` (with `cancel-in-progress` and `queue`) are applied.
  Concurrency group names are shared by every workflow of the repository and
  compared without regard to letter case.
- A job stops at its first failed step that is not covered by
  `continue-on-error`. Later steps run only when their `if` asks for it, for
  example with `if: failure()` or `if: always()`.
- `runs-on` is shown, but it does not choose a computer. The policy decides
  where every job runs (see [Where checks run](AUTOMATIC_CHECKS.md#where-checks-run)).
  A matrix over operating systems (`os: [ubuntu-latest, windows-latest]`)
  repeats the same work on the same computer, so remove that axis.
- `permissions`, `cache-mode` and job `outputs` are accepted and shown as not
  applied.

The server runs local jobs (host or container) one at a time across all
repositories, so a 16-job matrix runs one job after another. External
runners can run more at once, up to the policy's `max_active_jobs`.

### Steps and shells

A step is a `run` script or one of the built-in actions below. The default
shell is `bash -e {0}` on Linux, macOS and in containers (`sh -e {0}` when
bash is missing), and `pwsh` (or `powershell`) on Windows. On Windows, a job
whose `runs-on` names Linux or macOS uses the Git for Windows bash when it
exists. `shell` accepts `bash`, `sh`, `pwsh`, `powershell`, `cmd`, `python`,
or a command with `{0}` where the script file goes, as on GitHub.

Each step may run up to the policy's `max_timeout_ms` and print up to its
`max_output_limit_bytes`. A step that reaches the policy's time limit says so
and names `max_timeout_ms`; the administrator can raise it under Automatic
checks.

`GITHUB_ENV`, `GITHUB_PATH` and `GITHUB_OUTPUT` work as on GitHub, so a step
can pass variables, `PATH` entries and `steps.<id>.outputs` to later steps of
the same job. `GITHUB_ENV` cannot set `GITHUB_*`, `RUNNER_*` or
`NODE_OPTIONS`; such a line is ignored and the job says so. OwnGit does not
show the job summary written to `GITHUB_STEP_SUMMARY`.

### Built-in actions

| Action | What OwnGit does |
| --- | --- |
| `actions/checkout` | Passes. The commit is already in the workspace, without a `.git` folder. `ref`, `repository` and `path` must name this run's commit, repository and workspace; `submodules` and `lfs` must be off. |
| `actions/setup-go`, `setup-node`, `setup-python`, `setup-java` | Not run. The job uses the tool already on the computer or in the image. The version you asked for is shown, but OwnGit does not install, check or select it. |
| `actions/cache`, `actions/cache/restore`, `actions/cache/save` | Not run. OwnGit keeps no cache; `cache-hit` is `'false'`. |
| `actions/upload-artifact` | Not run. OwnGit keeps no artifacts; the files stay only in the job's workspace. |

Any version is accepted (`@v4`, `@main` or a commit), because nothing is
downloaded. An input that the action does not document refuses the job.

## What OwnGit refuses, and why

OwnGit reads each workflow file strictly. A key or feature it cannot honor
stops the file or the job before anything runs, and the Workflows view and
the run name the line and say what to do. OwnGit does not skip a key it does
not know.

| Refused | Why | What to do |
| --- | --- | --- |
| Any other `uses:` action, `docker://` actions and local `./` actions | OwnGit does not download or build actions. | Replace the step with a `run` step. See the next section. |
| `actions/download-artifact` | OwnGit keeps no artifacts, so the files would be missing. | Build the files in the same job. |
| Reusable workflows (job `uses`, `with`, `secrets`) | They do not run. | Copy the jobs into the file. |
| Job `container` and `services` | The administrator's policy chooses where jobs run and the only container. | Remove the key. Start a needed service inside one `run` script, or use one the policy's network can reach. |
| Job `environment` | OwnGit has no deployment environments or approval gates, and anyone with general access can start a workflow by hand. | Remove it only if running the job without approval is acceptable. |
| Job `snapshot`, and step `background`, `wait`, `wait-all`, `cancel` and `parallel` | OwnGit does not build runner images or run steps in the background. | Start the process and use it inside one `run` script. |
| An event name that GitHub does not define | The file would not mean what it says. | Check the spelling. Other GitHub events, such as `pull_request_target`, are only listed as not run; use `pull_request` instead, which runs the pull request's own commit. |
| `on.schedule[].timezone` | Schedules run in UTC only. | Write the cron time in UTC. |
| `on.workflow_dispatch.inputs.*.type: environment` | There are no environments. | Use `string` or `choice`. |
| `github.token`, `secrets.GITHUB_TOKEN` | OwnGit gives workflows no GitHub token. | Store a token of your own as a secret under another name. |
| `vars`, `needs.<id>.outputs`, `hashFiles()` and `github` fields not listed under [Expressions](#expressions) | Not provided. | Use `env` or a secret, compute values in the job that uses them, and compute hashes in a `run` step. |
| YAML tags, merge keys (`<<`), several documents, duplicate keys, unknown keys | The file must mean exactly one thing. | Write values out in full and check spelling against GitHub's workflow syntax. |

A file that breaks a limit is refused too; see [Limits](#limits).

## Replacing actions with run steps

Replace an action with the command it would run, and install the tool in the
container image, on the host or on the runner:

| Instead of | Use |
| --- | --- |
| `golangci/golangci-lint-action@v6` | `run: golangci-lint run` |
| `pnpm/action-setup@v4` | `run: pnpm install --frozen-lockfile`, with pnpm in the image |
| `actions/setup-dotnet@v4` | `run: dotnet test`, with .NET in the image |
| `ruby/setup-ruby@v1` with `bundler-cache: true` | `run: bundle install && bundle exec rake` |
| `dtolnay/rust-toolchain@stable` | `run: cargo test`, with Rust in the image |
| `docker/build-push-action@v6` | `run: docker build -t app .` on a host or runner that has Docker. Container jobs have no Docker inside. |
| `codecov/codecov-action@v4` | Remove it, or run Codecov's command-line uploader with a token stored as a secret. |
| `actions/github-script@v7` | `run: node scripts/task.js`. OwnGit gives no GitHub token. |

A typical Node.js job runs as it is:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-node@v4
    with:
      node-version: 20
      cache: npm
  - run: npm ci
  - run: npm test
```

### What the job has

OwnGit sets `CI=true` and `GITHUB_ACTIONS=true` so tools behave as in CI, and
the usual `GITHUB_*` and `RUNNER_*` variables: `GITHUB_WORKSPACE`,
`GITHUB_SHA`, `GITHUB_REF`, `GITHUB_REF_NAME`, `GITHUB_REF_TYPE`,
`GITHUB_HEAD_REF`, `GITHUB_BASE_REF`, `GITHUB_EVENT_NAME`,
`GITHUB_EVENT_PATH`, `GITHUB_REPOSITORY`, `GITHUB_RUN_ID`,
`GITHUB_RUN_NUMBER`, `GITHUB_RUN_ATTEMPT`, `GITHUB_JOB`, `GITHUB_WORKFLOW`,
`RUNNER_OS`, `RUNNER_ARCH` and `RUNNER_TEMP`.

What OwnGit does not provide:

- no GitHub token, no GitHub API and none of the tools of GitHub's hosted
  runners;
- no `.git` folder in the workspace, so `git describe` or
  `git diff --exit-code` do not work, and Git LFS files are pointer files;
- a repository that tracks a symbolic link or a submodule cannot be prepared
  for checks at all (see [What a check sees](AUTOMATIC_CHECKS.md#what-a-check-sees)).

On the host, a job gets the short list of server variables that every host
check gets (see [Environment variables](AUTOMATIC_CHECKS.md#environment-variables)),
plus the variables above and the workflow's `env`.

In a container, every step runs in a new container. The workspace
(`/workspace`) and the job's own home, temporary and cache folders keep their
changes for the whole job, so `pip install --user` in one step and `pytest` in
the next works. Changes anywhere else are lost after each step.

## Expressions

`${{ }}` expressions follow GitHub's syntax, operators and type conversion.
OwnGit checks every expression when the run is admitted, so an expression it
cannot evaluate refuses the job before anything runs. It never turns an
unknown value into an empty string.

- Functions: `success()`, `failure()`, `always()`, `cancelled()`,
  `contains()`, `startsWith()`, `endsWith()`, `format()`, `join()`,
  `toJSON()` and `fromJSON()`.
- Contexts: `github`, `inputs`, `matrix`, `strategy`, `needs.<id>.result`,
  `env`, `secrets`, `runner`, `steps` and `job.status`, at the keys where
  GitHub makes them available.
- `github` has `sha`, `ref`, `ref_name`, `ref_type`, `head_ref`, `base_ref`,
  `event_name`, `event.action`, `event.inputs`, `event.number`,
  `event.pull_request` (`number`, `head.ref`, `head.sha`, `base.ref`,
  `base.sha`), `repository`, `actor`, `triggering_actor`, `run_id`,
  `run_number`, `run_attempt`, `workflow`, `job`, and `workspace` inside
  steps.
- For a pull request, OwnGit runs the pull request's source commit, not a
  merge commit. `github.sha` is that commit, `github.ref` is
  `refs/pull/<number>/head`, and `github.ref_name` is `<number>/head`.
  `GITHUB_REF` and `GITHUB_REF_NAME` carry the same values. Branch filters
  still match the target branch.
- `steps.<id>.outcome` keeps the raw result; `conclusion` is `success` for a
  failed step with `continue-on-error`.
- A job with `continue-on-error` that failed gives `success` in
  `needs.<id>.result`, and it does not cancel its matrix siblings.

Expressions have bounds: 4 KiB of text, 32 levels of nesting, 64 KiB for
every value they produce, and 10,000 evaluation steps.

## Secrets

Secrets are values, such as a deploy token, that a workflow reads as
`${{ secrets.NAME }}`. The administrator stores them per repository.

```sh
owngit workflow-secret set \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --name DEPLOY_TOKEN \
  --value-file ./deploy-token
```

- `--value-file` must be a file that only your account can read. Use
  `--value-stdin` to read the value from standard input instead. A value is
  never a command-line argument.
- `owngit workflow-secret list` shows names and update times, never values.
  `owngit workflow-secret remove --name NAME` removes one. In the browser,
  open Workflow secrets from Workflows or Automatic checks to do the same.
- All three need the administrator password. Coding tools connected through
  MCP cannot read or change secrets.
- Names use letters, digits and underscores, do not start with a digit or
  `GITHUB_`, and are compared without regard to letter case. A repository
  holds at most 100 secrets of up to 48 KiB each.

Pass a secret to a command through `env`, so the value is never part of the
script text:

```yaml
  - run: ./deploy.sh
    env:
      DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
```

Name each secret directly as `secrets.NAME`. Secrets cannot be used in `if`;
put the secret in `env` and test the variable, for example
`if: env.DEPLOY_TOKEN != ''`. A job receives only the secrets its workflow
names, on every event. A named secret that is not set is empty, and the job
says so.

### What secrets do not protect

- Anyone who can push to the repository, or start a workflow by hand, can run
  code that reads its secrets. With open access, that is anyone who can
  reach OwnGit.
- OwnGit hides the exact text of each secret in logs and results, and of
  values a step registers with `::add-mask::`. A changed or encoded value is
  not hidden, and neither is a line shorter than 8 bytes of a multi-line
  secret. Masking cannot stop a workflow from sending a secret elsewhere.
- Host jobs are not a sandbox. They can read files the OwnGit account can
  read, including other credentials.
- Secrets are stored without encryption in one file per repository in the
  state directory (`workflow-secrets/`), readable only by the account that
  runs OwnGit. They are never in the database or a backup. After a restore or
  on a new computer, enter them again.
- If the secrets file cannot be read, the job does not start and says so. It
  never runs with empty values instead.

## Run a workflow by hand

Anyone with general access can start a workflow with `on: workflow_dispatch`
when the policy allows that event. Open the file under Checks, Workflows and
choose Run workflow. You can also use the command line or a coding tool
through MCP.

```sh
owngit workflow dispatch \
  --server https://git.example.test \
  --repository project \
  --path .github/workflows/deploy.yml \
  --ref main \
  --input environment_name=staging \
  --expected-oid 0123456789abcdef0123456789abcdef01234567
```

- `--ref` is a branch; it defaults to the default branch. The current commit
  must have a retained accepted push for that repository and branch. Otherwise
  dispatch returns HTTP 409 with `note.push_required`.
- If the branch moves while OwnGit prepares the run, dispatch returns HTTP 409
  with `workflow.moved`. Read the branch again before starting it.
- `--expected-oid` refuses the start if the branch has moved to another
  commit, with `workflow.moved`. The Run workflow form always sends it, so
  you run the commit the form showed.
- `--input name=value` sets a declared input, once per input. OwnGit checks
  `required`, `type` (`string`, `boolean`, `choice`, `number`) and `options`
  against the file at that commit. An undeclared or invalid input is refused
  with `workflow.dispatch_input`.
- Add `--password-file` with the shared password unless access is open.
  With open access, the Run workflow form warns that anyone who can reach
  OwnGit can start the workflow, and the workflow can read the repository's
  secrets.

## Schedules

`on.schedule` runs a workflow at cron times, in UTC. OwnGit reads the
schedules from the workflow files on the default branch, as GitHub does.

- Cron uses five fields: numbers, lists, ranges, steps, month and weekday
  names, and Sunday as `0` or `7`. When both day fields are restricted,
  either may match. A file has at most 10 schedules.
- `timezone` refuses the file. Write the time in UTC instead.
- A schedule fires only when the default branch head came from an accepted
  push. After an import, a restore, or when the push record is no longer
  kept, schedules wait for the next accepted push, and the Workflows view
  says so.
- Starts of one schedule are at least 5 minutes apart. A cron that asks for
  more often skips the times in between, and the run says so.
- Times missed while OwnGit was not running, or while the previous run of
  the schedule was still going, run once, as one run that names the time it
  was due.
- After a scheduled run whose result is uncertain (see
  [Uncertain jobs](#uncertain-jobs)), that schedule pauses until someone
  reruns the run or starts the workflow by hand.
- Turning schedules on, by allowing the event, turning workflows on or
  approving the policy again, starts each schedule from its next time. Times
  while they were off are not made up.
- When the queue is full, the scheduled run is recorded as not run and the
  time is used.
- A cron that can never match a date, such as February 30, is refused. A
  February 29 schedule whose next date is more than five years away runs its
  due time once, then returns when that date comes within five years.

## Runs, cancel and rerun

Open Checks, then Runs to see workflow runs, newest first. A run page lists
its jobs and steps with their results and notes. A job page shows each step's command,
the masked output excerpt (up to 8 KiB per step) and a link to the raw log.

On the command line, with general access:

```sh
owngit workflow-run list   --server https://git.example.test --repository project
owngit workflow-run show   ... --run RUN_ID
owngit workflow-run show   ... --run RUN_ID --job JOB_ID
owngit workflow-run log    ... --run RUN_ID --job JOB_ID
owngit workflow-run cancel ... --run RUN_ID
owngit workflow-run rerun  ... --run RUN_ID
```

`...` stands for the same `--server` and `--repository` flags, plus
`--password-file` when access needs the shared password. `list` shows the
newest 50 runs; `--limit` takes 1 to 999. Add `--json` for JSON.

### Cancel

Anyone with general access can cancel any run, including someone else's
deploy. Jobs that have not started are cancelled at once. A job that is
running is stopped as soon as possible; later steps do not run, including
steps with `if: always()`. Jobs that need a cancelled job do not start.

### Rerun

A rerun runs the whole workflow again at the same commit, with the same event
and the same dispatch inputs. It reads the workflow file again under the
current policy and limits. While a rerun of the same run has not finished,
asking again returns that rerun. Runs that were recorded as not run, for
example because the queue was full or the changed files could not be listed,
can be rerun the same way. Single jobs cannot be rerun.

### Results

A run ends with one result, and the repository page, the pull request and
the task list use the same rule:

| Result | API value | Meaning |
| --- | --- | --- |
| Failed | `failed` | A job failed. A later passing job does not hide it. |
| Incomplete | `incomplete` | A job could not finish: an error, a time or output limit, a missing tool or shell, or an uncertain execution. |
| Cancelled | `cancelled` | A job was cancelled. |
| Partly run | `partial` | Some jobs were refused and the others did not fail. This is not a pass. |
| Passed | `passed` | Every job that ran passed. A failed job with `continue-on-error` counts as passed and is marked. |
| Skipped | `skipped` | Nothing ran: every job was skipped, or had only built-in steps. This is not a pass. |
| Not run | `not_run` | The run was recorded but did not start, with the reason. |
| Not supported by OwnGit | `refused` | Nothing in the file can run here. This result is neutral. |

While jobs wait or run, the result is `queued` or `running`.

A job that changes a tracked file in the workspace ends as incomplete, even
when its steps passed, because its result is evidence for that commit. Its
last `run` step shows the change.

For a revision, OwnGit combines the latest run of each workflow file with the
`.owngit/checks.json` result. A failure anywhere wins. A file that OwnGit
cannot run at all is listed but does not make the revision red or green.

### Uncertain jobs

A job is `ambiguous` when OwnGit stopped, or lost contact with the runner,
after the job started. Its commands may have run, so OwnGit never starts it
again by itself.

- Every job that needs it, directly or through other jobs, is skipped, even
  with `if: always()` or `if: failure()`. Jobs that do not depend on it still
  run.
- The run ends as incomplete.
- A schedule whose run had such a job pauses until someone reruns the run or
  starts the workflow by hand.

Rerun the run when running it again is safe.

## Old runners

An external runner from OwnGit 1.1.7 or earlier cannot run workflow jobs. It
keeps taking `.owngit/checks.json` jobs, and workflow jobs wait with a note
that asks you to update the runner. Update `owngit` on the runner's computer
and restart it.

A current runner receives a job's plan and the secrets it names once, when
the job starts, over the runner's HTTPS connection. If that answer is lost,
the job becomes uncertain instead of running twice.

## How OwnGit differs from GitHub

Most differences are refusals listed above. These change how a workflow that
OwnGit accepts behaves:

- A job that changes tracked files ends incomplete, even when every step
  passed.
- A job whose needed job ended with an uncertain execution is skipped, even
  with `if: always()` or `if: failure()`.
- A rerun of a pull request run keeps its pull request event and action.
- Imports and restores never start runs. Only pushes that OwnGit accepted
  carry workflow authority for `push`, `pull_request`, `workflow_dispatch`
  and `schedule`.
- When the changed files for `paths` cannot be listed completely, the run is
  recorded as not run. GitHub runs it.
- A pull request runs its source commit, not a merge commit.
- After a cancel or a job timeout, no later step runs, including
  `if: always()` steps.
- Outputs do not pass between jobs (`needs.<id>.outputs`), and there is no
  `.git` folder in the workspace.
- `runs-on` does not choose a computer, and `actions/setup-*` do not install
  tools.
- Schedules use UTC only and start at most every 5 minutes.
- Logs are kept per job, not per step. The raw log of a job keeps up to
  256 KiB, the beginning and the end, so output of an early failed step can
  be cut when later steps print a lot. Each step keeps its own 8 KiB excerpt.

## Limits

| Item | Limit |
| --- | --- |
| Workflow files read per commit | 32 files, 128 KiB each, 1 MiB in total, names up to 100 bytes |
| Jobs per run | 16, including matrix combinations |
| Steps per job | 50 |
| `run` script | 24,000 bytes as written |
| Expressions | 4 KiB of text, nesting 32, 64 KiB per value, 10,000 steps |
| Environment per step | 200 variables, 48 KiB per value, 256 KiB in total |
| `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT` per step | 64 KiB each |
| `::add-mask::` values | 256 per job, 8 KiB each |
| Changed files for `paths` | 3,000 files, 1 MiB of names, 10 seconds |
| Dispatch inputs | 25 inputs, 1 KiB per value |
| Schedules | 10 per file |
| Secrets | 100 per repository, 48 KiB each |

A run must also fit the policy's `queue_limit` with all its jobs. A run that
does not fit now is recorded as not run; rerun it later. A workflow that
needs more jobs than `queue_limit` can never start, and the Workflows view
says so before any event.

## Reference: commands, API and MCP

All commands take `--server` and `--repository`, and `--json` for JSON
output. General-access commands take `--password-file` with the shared
password; secret commands take it with the administrator password. Share
links never give access to these commands.

| Task | Command | API (under `/api/v1/repositories/{repository}`) | MCP tool |
| --- | --- | --- | --- |
| List or show workflow files at a branch | `owngit workflow list [--ref]`, `owngit workflow show --path [--ref]` | `GET /workflows?ref=BRANCH[&path=FILE]` | `workflow_list`, `workflow_show` |
| Start a workflow | `owngit workflow dispatch --path [--ref] [--input k=v] [--expected-oid]` | `POST /workflows/dispatch` | `workflow_dispatch` |
| List runs | `owngit workflow-run list [--limit]` | `GET /workflow-runs?limit=N` | `workflow_run_list` |
| Show a run or one job | `owngit workflow-run show --run [--job]` | `GET /workflow-runs/{run}`, `GET /workflow-runs/{run}/jobs/{job}` | `workflow_run_show` |
| Read a job's raw log | `owngit workflow-run log --run --job` | `GET /workflow-runs/{run}/jobs/{job}/log` | `workflow_job_log` |
| Cancel or rerun a run | `owngit workflow-run cancel --run`, `rerun --run` | `POST /workflow-runs/{run}/cancel`, `POST /workflow-runs/{run}/rerun` | `workflow_run_cancel`, `workflow_run_rerun` |
| Manage secrets (administrator) | `owngit workflow-secret list`, `set --name (--value-file \| --value-stdin)`, `remove --name` | `GET /workflow-secrets`, `PUT` and `DELETE /workflow-secrets/{name}` | none |
| Turn workflows on (administrator) | `owngit check-policy set --enable` with `run_workflows` | `POST /check-policy/save-and-enable` | none |

Send API mutation bodies as JSON objects with `Content-Type: application/json`.
Dispatch accepts `path`, optional `ref`, `expected_oid` and an `inputs` object.
Cancel, rerun and secret removal require `{}`. Setting a secret takes
`{"value":"YOUR_SECRET_VALUE"}` over the administrator connection. Arrays,
`null`, scalars and unknown fields are refused. Check-policy `enable` and
`disable` also accept a legacy empty body; use `{}` for new integrations.

Workflow messages carry `code`, `detail` and, when available, `path`, `line`
and structured `args`. API, CLI and MCP keep the recorded English `detail`.
The dashboard translates known messages with complete arguments into English
or Korean. An unknown code or missing required argument falls back to
`detail`; do not parse that text to recover arguments or treat it as success.

A run answer lists jobs and step results without output excerpts; a job
answer adds that job's step commands and excerpts. In task and revision
answers, `workflows_total` counts every workflow file with a run, and
`workflows_truncated` is true when the answer lists only the first 22.

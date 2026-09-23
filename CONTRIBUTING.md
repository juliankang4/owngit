# Contributing to OwnGit

This guide covers building, testing, and changing OwnGit. [AGENTS.md](AGENTS.md) holds the working rules for changes, including task scope, data protection, and verification. User documentation is in [README.md](README.md) and [docs/](docs/).

## Prerequisites

- Go 1.27 or newer.
- Git with an executable `git-http-backend`. Tests run the real system Git. Pull request merge tests need Git 2.38 or newer.
- For the macOS app prototype only: an Apple silicon Mac with Xcode command-line tools.

## Build and run

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

The first run writes an owner-only setup file inside the state directory. The default state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available. Use a separate state directory and repository folder for development so that you never touch real repositories:

```sh
./bin/owngit serve --no-open --state-dir /tmp/owngit-dev-state
```

## Tests and checks

Run these before sending a change:

```sh
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

`gofmt -l .` must print nothing. Integration tests create temporary repositories and cover Smart HTTP, ref retention, pull request revisions and merges, browser restore, offline backup and restore, import publication and recovery, and process cancellation. Tests with POSIX-shell fixtures skip on Windows.

### Real Docker test (opt-in)

One Linux test runs configured checks in a real Docker container. Run it only as a trusted nonroot user with Git, the Docker CLI, and the local default Docker socket available. The test does not pull images, install tools, use `sudo`, or contact an external network.

- `TMPDIR` must be an absolute path that the controller and the Docker daemon see at the same location.
- The image must already be cached, Linux-based, declare no image volumes, and contain `/bin/sh`, `/bin/true`, `cat`, `chmod`, `grep`, `id`, `sleep`, and `touch`.

```sh
CACHED_IMAGE_ID_HEX=replace_with_64_lowercase_hex_digits
TMPDIR=/absolute/shared/test-tmp \
OWNGIT_REAL_DOCKER_TEST=1 \
OWNGIT_DOCKER_IMAGE="sha256:${CACHED_IMAGE_ID_HEX}" \
go test -count=1 -run '^TestRealDockerConfiguredChecks$' ./internal/checkrunner
```

When the test is enabled, a missing prerequisite fails the test instead of skipping it. Cleanup removes only containers recorded in the test's own synthetic state that carry its ownership label.

## Repository layout

- `cmd/owngit`: the `owngit` command, host commands, and the server lifecycle.
- `internal/`: one package per responsibility. The main groups are:
  - storage and Git: `repository`, `githttp`, `gitexec`, `publishdir`;
  - state and security: `state` (SQLite), `auth`, `bootstrap` (setup file), `server` (routing, Host, Origin, and CSRF checks), `webui` (templates, localization, assets);
  - pull requests: `pullrequest`, `apiclient`;
  - checks: `checkapi` (wire contract), `checkworkflow` (`.owngit/checks.json`), `checkexec`, `checksource`, `checkrun`, `checkrunner`;
  - imports: `importgit`, `importfetch`, `importsync`;
  - recovery: `recovery` (offline backup and restore);
  - `version`: the single application version literal.
- `tools/release`: the release tool (see [Releases](#releases)).
- `packaging/`: templates for archives, native prototypes, and package-manager files. See [packaging/README.md](packaging/README.md).
- `integrations/skills/owngit-checks/`: the shared Agent Skill for coding tools.
- `THIRD_PARTY_NOTICES/`: generated notices for linked modules, the Go runtime, and embedded assets.

Runtime dependencies are `modernc.org/sqlite` (SQLite without a C compiler), `golang.org/x/crypto` (Argon2id), and `golang.org/x/sys` (Windows process and file APIs).

## Product principles

A change must keep the following behavior.

### Private Git storage

- Core use must not need published code, an external Git-host account, or an AI subscription.
- Never silently discard work or delete history. Before accepting a force-push or a branch or tag deletion, retain what it replaces. Recovery covers Git-tracked files, not application databases, untracked files, or whole systems.
- Browser restore previews every addition, change, and deletion, then adds a commit without rewriting a branch. It refuses the write if the branch moved after the preview. Repository paths and symbolic links are data, never host filesystem paths.
- A backup keeps every ref and reachable object, repository metadata, access mode, password hashes, and the durable pull request, task, check, and import records. Sessions, setup links, trusted Hosts, consent, schedules, and helper, runner, and source credentials stay machine-local and are not restored.
- Activity comes from commit author dates across working and retained branches, counts each commit once per repository, and is never evidence that checks passed.

### Access and administration

- Support local and private networks, and recommend Tailscale for other devices. Public Internet hosting is out of scope. OwnGit serves plain HTTP; any TLS is provided by the operator.
- General access is password-free or protected by one shared password. There are no individual accounts.
- A separate administrator password protects security settings, and every change asks for it again. Host owners can reset it without deleting repository data.
- Setup starts from an owner-only, one-time link and does not require creating a repository.
- Plain LAN HTTP needs an informed choice, and the connection status stays visible.

### Checks, reviews, and coding tools

- OwnGit stores and shows evidence. It never launches coding sessions or reviewers and never moves heavyweight checks onto the storage host without the owner's explicit consent.
- A helper runs checks in the user's environment with the user's permissions and is not a sandbox. Never claim isolation that was not established.
- Bind check evidence to the tested revision, the check configuration, and the worktree state before and after the run. A dirty or different worktree is not a tested commit. Never reuse old success for changed work or present a summary or AI review as an executed check.
- The server issues attempt order at registration. A late older result never replaces newer state, and a registered attempt without a completion stays visible as pending.
- A task keeps one identity across revisions and a budget of three correction rounds. A round is reserved explicitly and counted once. Manual reruns, retransmits, cancelled runs, and unavailable environments are not corrections. An exhausted budget stops automatic continuation without blocking Git.
- Missing checks are not passing checks. Failed, unavailable, incomplete, cancelled, and stale evidence stays visible. Checks and reviews are advisory and never hold a merge.
- Push works without pull requests. Review decisions and merges bind to exact source and target revisions and become invalid when either moves. A reviewer label is supplied provenance, not independent review.
- Merge publishes a fast-forward or a nonrewriting merge commit, with a protected receipt so a retry never creates a second merge.
- Never send private code to an external service without explicit, informed enablement that states what is sent.

### Imports

- Imports are inbound only. Never write to the source, and never let imported code grant check consent.
- Preserve local work: publish only branches and tags, never remove a local ref because the source deleted it, keep divergent refs and HEAD, and retain replaced history.
- Never report an unconfirmed publication as complete. Refuse refreshes until the owner accepts the repository as found.
- Do not fetch or host Git LFS objects. Require Git-only consent when pointers are found or inspection is incomplete.

### Security and records

- Treat hosted code and check commands as untrusted, and record only the protection actually established.
- Password-free access does not remove administrator confirmation, Host and Origin checks, CSRF protection, or input limits. Private-network membership alone does not prove installation ownership.
- Helper credentials are separate, revocable, and repository-scoped. They cannot change access or security settings or manage other credentials.
- Keep durable records (history, tasks, check outcomes) separate from disposable raw logs. Log cleanup never removes durable records.

## Security rules for code

- Never put setup tokens, passwords, or credentials in command-line values, environment variables, URLs, fixtures, or logs. Secrets come from owner-only files or interactive prompts. Tests use synthetic credentials and disposable repositories.
- Do not weaken Host, exact-Origin, CSRF, access-mode, or administrator-confirmation checks. Mutating JSON endpoints accept JSON only, and the CLI client never follows redirects or retries authentication.
- Git subprocesses use an app-owned HOME and empty global and system config. Do not reintroduce inherited hooks, credential helpers, filters, or client environment variables.
- Treat repository paths, symbolic-link blobs, binary blobs, and modes as data. Pass paths to Git as literal pathspecs and never resolve them on the host filesystem.
- Store only hashes of helper and runner tokens. Issuing or revoking a credential always verifies the current administrator password.
- Deliver a new helper token only through an exclusively created, owner-only file that is never reopened by path. Generate the creation identity before the request, so a lost response can be revoked.
- API responses and CLI output never contain tokens, passwords, CA PEM, or credential file paths. The pull request API never authenticates with browser cookies or the administrator password.
- Once a raw check log is accepted, a later submission never replaces its bytes. Backup files stay owner-readable.
- Smart HTTP serves only discovery and upload and receive paths. Do not expose repository directories through a generic file server.
- Do not describe SHA-256 backup hashes as authentication. They detect corruption only.

## State and backup compatibility

Git refs and objects are the authoritative repository data. SQLite (`owngit.sqlite` in the state directory) holds pull request, review, task, check, and import records. OwnGit's own refs live under `refs/owngit/`. They are hidden from clients, and client pushes are accepted only for `refs/heads/*` and `refs/tags/*`.

- The state schema version is recorded in the database (`currentSchemaVersion` in `internal/state/store.go`). A schema change needs a migration from each accepted earlier catalog. OwnGit refuses unknown, altered, or newer databases without changing their files.
- The offline backup format version is `backupVersion` in `internal/recovery/recovery.go`. When you add durable records, add them to the backup and to restore validation, raise the version, keep released versions readable, and reject newer versions before decoding so records are never dropped silently.
- Machine-local authority (credentials, consent, schedules, sessions) must not be exported or restored.

## Releases

No release has been published. `tools/release` builds and checks release artifacts using the Go toolchain and the standard library. It does not sign or publish anything.

```sh
go run ./tools/release notices -check
go run ./tools/release build -out dist/portable
go run ./tools/release verify -dir dist/portable
```

- `notices -check` compares `THIRD_PARTY_NOTICES/` with the current build inputs. Run it after changing dependencies. To regenerate, write into a new empty directory and compare: `go run ./tools/release notices -out THIRD_PARTY_NOTICES.new`, then `diff -r THIRD_PARTY_NOTICES THIRD_PARTY_NOTICES.new`. Edit `packaging/notices/README.md.tmpl`, not the generated `README.md`.
- `build` compiles each target with `-trimpath -buildvcs=false` and `CGO_ENABLED=0`, writes deterministic archives, `SHA256SUMS`, and `manifest.json`, and then runs `verify`. `-targets` selects a comma-separated subset.
- `verify` re-checks archive digests, contents, embedded build metadata, and the notice set, and rejects private or build files such as `.git/`, `.local/`, `*.sqlite`, and `*.test`. It runs only the binary built for the host.

Build targets are `darwin/arm64` (macOS on Apple silicon), `linux/amd64`, `linux/arm64`, and `windows/amd64`. Each archive holds the `owngit` executable, `LICENSE`, `THIRD_PARTY_NOTICES/`, `README.txt`, `docs/CODING_TOOLS.md`, and `integrations/skills/owngit-checks/SKILL.md`. `tools/release/resources.go` declares the last two by path, so moving them requires changing the release tool and its tests.

The application version is `internal/version.Version`. Every artifact name, manifest, and package file takes its version from that one value. 1.0.0 is the first version and has no changelog entry. Each later release adds a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` that matches `internal/version.Version`, with notable changes grouped under `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, and `Security`, and breaking changes and removals stated explicitly.

Unsigned native prototypes and package-manager files are built from a verified portable output:

```sh
go run ./tools/release native \
  -manifest dist/portable/manifest.json \
  -out dist/native-deb \
  -formats deb \
  -baseline "$REVIEWED_BASELINE"

go run ./tools/release packaging \
  -manifest dist/portable/manifest.json \
  -out dist/packaging
```

`native -formats all` also builds the macOS app and DMG on an Apple silicon Mac. `packaging` renders the Homebrew formula and WinGet manifests. It marks a file `UNREADY` when a download URL or publisher input is missing, or fails with `-strict`. [packaging/README.md](packaging/README.md) describes the templates and prototype contents.

## Documentation

- `README.md`: what OwnGit is and how to start.
- `docs/OPERATIONS.md`: setup, access, recovery, imports, pull requests, checks, storage, and backups.
- `docs/AUTOMATIC_CHECKS.md`: configured checks and runners.
- `docs/CODING_TOOLS.md`: the coding-tool workflow. Its path is fixed by the release tool, and `cmd/owngit/check_budget_test.go` requires certain phrases in it and in the skill. Keep the guide and `integrations/skills/owngit-checks/SKILL.md` consistent.
- `CONTRIBUTING.md`: this guide.
- `CHANGELOG.md`: notable changes per version, in the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Update the documentation in the same change as the behavior it describes. Describe what exists, and do not document planned features or unverified platforms as supported.

# Development reference

These are the maintainers' rules for changing OwnGit. Contributors only need [CONTRIBUTING.md](../CONTRIBUTING.md); the maintainers apply these rules before merging.

## Repository layout

- `cmd/owngit`: the `owngit` command and the server.
- `internal/`: one package per responsibility, such as `repository`, `githttp`, `state` (SQLite), `auth`, `server`, `webui`, `pullrequest`, `checkrun`, `importsync` and `recovery`.
- `tools/release`: the release tool. See [Releases](#releases).
- `packaging/`: archive, container, installer and package-manager files. See [packaging/README.md](../packaging/README.md).
- `integrations/skills/owngit-checks/`: the Agent Skill that `owngit skill` installs.
- `THIRD_PARTY_NOTICES/`: generated license notices.

Runtime dependencies are `modernc.org/sqlite`, `golang.org/x/crypto`, `golang.org/x/sys`, `golang.org/x/text` and `github.com/yuin/goldmark`. Prefer the standard library and these modules. A new dependency needs a stated reason.

## Product principles

Every change keeps these rules.

### Defaults

- Everything works by default. A restriction on what owners do with their own installation is an option, not a default.
- Protections against other people stay on: local-only listening (a computer without a screen listens everywhere but answers only the setup link until setup ends), the administrator password, the shared password when set, the one-time setup link, Host, Origin and CSRF checks, and the plain-HTTP acknowledgement. Make them easier, never remove them.

### Git storage

- Core use needs no external Git-host account or AI subscription.
- Never discard work silently. While kept history is on (the default), keep what a force-push, an import or a ref deletion replaces before accepting it. Turning it off affects only later pushes and imports and never deletes kept history.
- Git names keep their exact bytes. Every Git command on hosted repositories gets `core.precomposeUnicode=false` at command scope. Commands that read the owner's own working copy follow the owner's Git configuration.
- OwnGit owns the hooks of hosted repositories and restores them at every start.
- A new repository folder is made private to the OwnGit account before content is written. Existing folders are never changed; `owngit doctor` reports unsafe ones with a repair command.
- Browser restore previews every change and refuses if the branch moved after the preview. Restoring onto an existing branch adds a commit without rewriting history. Restoring a missing branch points it at the source commit without adding a commit. Repository paths and symbolic links are data, never host paths.
- A backup holds every ref and reachable object, repository metadata, access mode, password hashes and the durable pull request, task, check and import records. Machine-local state is not restored. `recovery.RestoreNotes` is the one list `owngit restore` reports; keep it in step with what restore leaves out.

### Access and administration

- Support local and private networks. Public Internet hosting is out of scope, except an optional address that answers share links only. OwnGit serves plain HTTP; TLS comes from the operator.
- Access is open or protected by one shared password. There are no user accounts.
- A share link opens one repository read-only until it expires or is revoked. It never allows a push or shows owner pages.
- A separate administrator password protects settings. The dashboard asks for it as the owner chooses under Settings, Access. The API and the command line always ask for it, for reads as well.
- Every owner task is available in the dashboard and on the command line, and the command prints JSON (by default or with `--json`). Add an MCP tool when a coding tool would use the task. Exceptions:
  - Command line only: `reset-admin`, `setup-link` and `approve-host` (they recover access), `uninstall`, `restore`, `backup --output`, and `upgrade-backup` (the host-local upgrade backup setting).
  - `serve`, `service`, `runner` and `mcp` control processes.
  - Accepting the plain-HTTP warning is dashboard only.
  - Check tasks, correction rounds and attempts are recorded by coding tools and only shown in the dashboard.

### Checks and coding tools

- OwnGit stores and shows evidence. It never starts coding sessions or reviewers, and runs checks on the storage host only with the owner's explicit consent.
- A helper runs checks with the user's permissions and is not a sandbox. Never claim isolation that was not established.
- Bind check evidence to the tested revision, the check configuration and the worktree state. Never reuse old success for changed work, and never present a summary or an AI review as an executed check.
- The server orders attempts at registration. A late older result never replaces newer state.
- A task keeps one identity and a budget of three correction rounds. Reruns, retransmits, cancelled runs and unavailable environments are not corrections. An exhausted budget never blocks Git.
- Missing checks are not passing checks. Checks and reviews are advisory and never block a merge.
- Review decisions and merges bind to exact source and target revisions. A merge never rewrites history and never runs twice. One pull request can be open per source and target pair.
- Never send private code to an external service without informed consent that states what is sent.

### Imports

- Never write to the source, and never let imported code grant check consent.
- By default, publish branches and tags, preserve refs deleted at the source and leave locally diverged refs alone. The owner may choose extra ref namespaces, overwrite diverged refs or follow upstream deletions. These choices never permit rewriting a protected default branch, deleting the branch HEAD points to or deleting refs when the source lists none of the selected refs. Branches and tags keep replaced history according to the repository's kept-history setting; extra namespaces have no kept history. See [Refresh choices](REPOSITORIES.md#refresh-choices).
- Never report an unconfirmed publication as complete.
- Do not host Git LFS objects. Ask for Git-only consent when pointers are found.

## Security rules for code

- Never put tokens, passwords or credentials in command-line values, environment variables, URLs, fixtures or logs. Secrets come from owner-only files or prompts. The one-time setup link may be printed only to a terminal. Tests use synthetic credentials and disposable repositories.
- Do not weaken Host, Origin, CSRF, access-mode or administrator checks. Every administrator change in the browser goes through the one shared confirmation gate. The administrator API checks the password in the Basic header on every request; a browser session never authorizes it. Mutating JSON endpoints accept JSON only.
- Show a line of user text (titles, branch names, paths, author names) in its own element with `dir="auto"`. Write JSON answers through `bidi.MarshalJSON` or `bidi.EscapeJSON`. Show documents in `.md` or `.textdoc`.
- Check a JSON request with `jsoninput.Valid` before decoding it.
- Git subprocesses on hosted repositories use an app-owned HOME, empty global and system config, and `commandConfig` in `internal/gitexec/runner.go`. Never bring back inherited hooks, credential helpers, filters or client environment variables.
- Pass repository paths to Git as literal pathspecs. Never resolve them on the host.
- Store only hashes of helper and runner tokens. Issuing or revoking one needs administrator authority. The authorized issuance API returns a new token once; the CLI saves it in a new owner-only file created exclusively. Ordinary reads never return token values.
- Ordinary API and command output and logs contain no tokens, passwords or CA PEM. Authorized token issuance and share-link creation are intentional secret-delivery responses, not ordinary reads. CLI token issuance may report the destination file path, but never the token value. Do not echo credentials in errors or logs.
- An accepted raw check log is never replaced. Backup files stay owner-readable.
- Smart HTTP serves only the Git protocol paths.
- SHA-256 backup hashes detect corruption. They are not authentication.

## State and backup compatibility

Git refs and objects are the repository data. SQLite (`owngit.sqlite` in the state directory) holds pull request, review, task, check and import records. OwnGit's own refs live under `refs/owngit/`, hidden from clients and closed to pushes.

- The schema version is `currentSchemaVersion` in `internal/state/store.go`. A schema change needs a migration from each accepted earlier catalog. OwnGit refuses unknown, altered or newer databases without changing them. When a release writes a new schema, add its dump to `internal/state/testdata/released`.
- The backup format version is `backupVersion` in `internal/recovery/recovery.go`. When you add durable records, add them to backup and restore validation, raise the version, keep released versions readable, and reject newer versions before decoding. Select the format from both content and encoded manifest size. Format 12 carries workflow facts or a positive `output_limit_exceeded_bytes`. Without those, format 10 is used only when `format11Content` finds no newer records and the encoded manifest is at most 64 MiB; otherwise use format 11. An upgrade backup is readable by an earlier release only if that release supports its format and records. Backup and restore share `manifestLimit`.
- Credentials, consent, schedules and sessions are never exported or restored.

## Documentation

Before merging, the maintainers update the documentation in English and Korean, the tests and the changelog. Describe what exists; never document planned features or untested platforms as supported.

| File | Content |
| --- | --- |
| `README.md` | what OwnGit is, install and quickstart |
| `SECURITY.md` | reporting a vulnerability, what OwnGit protects |
| `docs/OPERATIONS.md` | setup, service, network access, settings, limits |
| `docs/REPOSITORIES.md` | repositories and imports |
| `docs/BACKUPS.md` | storage, backups and restore |
| `docs/CODING_TOOLS.md` | pull requests and checks from coding tools |
| `docs/AUTOMATIC_CHECKS.md` | configured checks and runners |
| `docs/WORKFLOWS.md` | GitHub Actions workflows, secrets, dispatch and schedules |
| `CHANGELOG.md` | changes per version, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format |

Each of these, except `CHANGELOG.md`, has a Korean `.ko.md` version with the same facts. `CONTRIBUTING.md` and this file are English only.

These tests read documents:

- `cmd/owngit/docs_windows_test.go` runs the PowerShell password-file block in `docs/OPERATIONS*.md` and `docs/CODING_TOOLS*.md` on Windows.
- `tools/release/release_test.go` checks the links in `docs/CODING_TOOLS*.md`, which ship in release archives.
- `tools/release/installer_test.go` checks that the example commands in `packaging/installer/*` appear in `docs/OPERATIONS.md`.

Program messages and other documents link to headings. Search for an anchor before you rename its heading. `go run ./tools/doclinks` checks links and anchors, and the `Document links` workflow runs it on every pull request.

## Continuous integration

The `CI` workflow (`.github/workflows/ci.yml`) runs gofmt, `go vet` and the tests on Linux (with the race detector), Windows and macOS. A pull request tests only the packages its change affects, chosen by `.github/scripts/affected-packages.sh`. A package with a changed file runs in full; a package affected only through a dependency runs with `-short`. A pull request also runs golangci-lint (`.golangci.yml`), which rejects `time.Sleep` and `time.Since` on test lines the pull request adds or changes. The daily run and manual runs test everything, add Linux arm64 and check for known vulnerabilities. Complete a full test run on the release commit before tagging.

When you write a test:

- A slow test may skip itself under `testing.Short()`. It still runs in full runs and when its own package changes.
- A test that reads files from another package by path must be listed in `affected-packages.sh`, and must not skip under `testing.Short()`.

## Real Docker test (opt-in)

One Linux test runs configured checks in a real Docker container. Run it as a trusted nonroot user with Git, the Docker CLI and the default Docker socket. It does not pull images, use `sudo` or reach the network.

- `TMPDIR` must be an absolute path that the test and the Docker daemon see at the same location.
- The image must be cached, Linux-based, declare no volumes, and contain `/bin/sh`, `/bin/true`, `cat`, `chmod`, `grep`, `id`, `sleep` and `touch`.

```sh
CACHED_IMAGE_ID_HEX=replace_with_64_lowercase_hex_digits
TMPDIR=/absolute/shared/test-tmp \
OWNGIT_REAL_DOCKER_TEST=1 \
OWNGIT_DOCKER_IMAGE="sha256:${CACHED_IMAGE_ID_HEX}" \
go test -count=1 -run '^TestRealDockerConfiguredChecks$' ./internal/checkrunner
```

When enabled, a missing prerequisite fails the test instead of skipping it.

## Releases

Releases go to [GitHub Releases](https://github.com/juliankang4/owngit/releases). The Homebrew tap, npm packages and container image use the same archives. `tools/release` builds and checks artifacts and never publishes. It signs macOS artifacts only when given a Developer ID identity and a notarization profile; see [Signed macOS release](../packaging/README.md#signed-macos-release).

```sh
go run ./tools/release notices -check
go run ./tools/release build -out dist/portable
go run ./tools/release verify -dir dist/portable
go run ./tools/release packaging -manifest dist/portable/manifest.json -out dist/packaging
```

- `notices -check` compares `THIRD_PARTY_NOTICES/` with the build inputs. Run it after changing dependencies. To regenerate, run `notices -out THIRD_PARTY_NOTICES.new` and compare with `diff -r`. Edit `packaging/notices/README.md.tmpl`, not the generated `README.md`.
- `build` writes deterministic archives for `darwin/arm64`, `linux/amd64`, `linux/arm64` and `windows/amd64`, plus `SHA256SUMS` and `manifest.json`. It copies `install.sh`, `install.ps1` and `proxmox.sh` beside them; attach all three to the release.
- `verify` re-checks archives, notices and installers.
- `packaging` renders the Homebrew formula and cask, WinGet manifests, npm packages and Arch Linux `PKGBUILD`. It marks the output `UNREADY` when an input is missing, or fails with `-strict`.

Each archive holds `owngit`, `LICENSE`, `THIRD_PARTY_NOTICES/`, `README.txt`, `docs/CODING_TOOLS.md`, `docs/CODING_TOOLS.ko.md` and `integrations/skills/owngit-checks/SKILL.md`. The macOS archive also holds `OwnGit.app`, which needs an Apple silicon Mac with the Xcode command-line tools. The app holds a copy of the archive's `owngit` at `OwnGit.app/Contents/Helpers/owngit`, so it works on its own. `tools/release/resources.go` declares the shipped documents by path.

The version is `internal/version.Version`. Each release adds a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` that matches it.

After the GitHub Release exists, start these workflows by hand, passing the version without the leading `v`:

- `npm publish` (`.github/workflows/npm-publish.yml`) publishes the npm packages with npm trusted publishing. Then run `npm smoke test`.
- `container publish` (`.github/workflows/container-publish.yml`) builds, checks and pushes the image to `ghcr.io/juliankang4/owngit`.

Both publish only from `main`, and both have a `dry_run` option.

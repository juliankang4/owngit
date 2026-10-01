# Development reference

This reference holds the maintainers' rules and procedures for OwnGit: how the product must behave, the security rules for code, state and backup compatibility, the repository layout, and releases. Contributors do not need to read it: [CONTRIBUTING.md](../CONTRIBUTING.md) is the whole path for sending a change, and the maintainers apply these rules before merging.

## Repository layout

- `cmd/owngit`: the `owngit` command, host commands, and the server lifecycle.
- `internal/`: one package per responsibility. The main groups are:
  - storage and Git: `repository`, `githttp`, `gitexec`, `publishdir`;
  - state and security: `state` (SQLite), `auth`, `bootstrap` (setup file), `server` (routing, Host, Origin, and CSRF checks), `requestctx` (a request's effective scheme, Host, and client address), `webui` (templates, localization, assets);
  - text from users: `bidi` (Unicode direction controls), `jsoninput` (JSON request text), `markdown` (Markdown files shown as documents);
  - host integration: `service` (background service), `tailscale` (sharing on a tailnet over HTTPS), `doctor` (installation checkup), `firstrun` (setup in the terminal), `releasecheck` (new-release notice);
  - pull requests: `pullrequest`, `apiclient`;
  - checks: `checkapi` (wire contract), `checkworkflow` (`.owngit/checks.json`), `checkexec`, `checksource`, `checkrun`, `checkrunner`;
  - imports: `importgit`, `importfetch`, `importsync`;
  - recovery: `recovery` (backup and restore), `backups` (scheduled backups and back up now while OwnGit serves);
  - `version`: the single application version literal.
- `tools/release`: the release tool (see [Releases](#releases)).
- `packaging/`: templates for archives, native prototypes, and package-manager files. See [packaging/README.md](../packaging/README.md).
- `integrations/skills/owngit-checks/`: the shared Agent Skill for coding tools, and a small Go package that embeds it so `owngit skill` can install it from any build.
- `THIRD_PARTY_NOTICES/`: generated notices for linked modules, the Go runtime, and embedded assets.

Runtime dependencies are `modernc.org/sqlite` (SQLite without a C compiler), `golang.org/x/crypto` (Argon2id), `golang.org/x/sys` (Windows process and file APIs), and `github.com/yuin/goldmark` (Markdown files shown as documents, with raw HTML turned off). Prefer the standard library and these modules. A new dependency needs a stated reason and an estimate of what it costs to maintain.

## Product principles

A change must keep the following behavior.

### Defaults

- Everything works by default. A restriction on what owners do with their own installation is an option they can turn on, not a default that blocks them.
- Protections against other people stay on. These include local-only listening until the owner chooses another address (a computer without a screen, where setup has to happen on another device, listens on every address from the first start and answers only the one-time setup link until setup is finished), the administrator password, the shared access password when the owner sets one, the one-time setup link, Host, Origin and CSRF checks, and the plain-HTTP acknowledgement. Make these steps easier instead of removing them.
- This rule does not replace the consent rules below. Running checks on the storage host and sending code to an external service still need the owner's explicit choice.

### Private Git storage

- Core use must not need published code, an external Git-host account, or an AI subscription.
- Never silently discard work or delete history. While kept history is on (the default; a repository follows the server choice unless it sets its own), retain what a force-push, an import, or a branch or tag deletion replaces before accepting it. Turning it off is the owner's choice and applies only to pushes and imports that start afterwards. It never deletes, hides, or blocks restoring history already kept. Recovery covers Git-tracked files, not application databases, untracked files, or whole systems.
- Git names in hosted repositories keep their exact bytes. Every Git command OwnGit runs on hosted repositories, including its generated hooks, repository preparation, backup, verification and restore, receives `core.precomposeUnicode=false` at command scope, so Git on macOS does not convert decomposed Unicode names. OwnGit never migrates stored names or repository config for this. Git commands that read the owner's own working copy, such as those in `owngit check` and origin detection, follow the owner's Git configuration instead.
- OwnGit manages the hooks of hosted repositories. Each start restores its `update` hook, which keeps replaced history, and removes `pre-receive` and `reference-transaction`, so local edits to those hooks are not kept.
- Browser restore previews every addition, change, and deletion, then adds a commit without rewriting a branch. It refuses the write if the branch moved after the preview. Repository paths and symbolic links are data, never host filesystem paths.
- A backup keeps every ref and reachable object, repository metadata, access mode, password hashes, and the durable pull request, task, check, and import records. Sessions, setup links, trusted Hosts, network settings, the server-wide choices under Settings (including Git transfer and browsing limits, check ceilings, repository maintenance and unused object cleanup), consent, schedules, each import source's connection choices and limits, share links, backup history, the upgrade backup off switch, raw check logs, recent pushes, and helper, runner, and source credentials stay machine-local and are not restored. `recovery.RestoreNotes` is the one list that `owngit restore` reports; keep it in step with what a restore leaves out. A symbolic branch is the one ref identity a backup does not keep: it is captured as an ordinary branch at the same commit, and its target appears only in the capture-time notice (`recovery.CaptureReport.AliasNotice`), never in the backup itself.
- Activity comes from commit author dates across working and retained branches, counts each commit once per repository, and is never evidence that checks passed.

### Access and administration

- Support local and private networks, and recommend Tailscale for other devices. Public Internet hosting is out of scope; the one exception is an optional public address that answers share links and nothing else. OwnGit serves plain HTTP; any TLS is provided by the operator.
- General access is password-free or protected by one shared password. There are no individual accounts.
- A share link, which only an administrator creates, opens one repository read-only without the shared password until it expires or is revoked. It never allows a push or shows owner pages.
- A separate administrator password protects security settings. The dashboard asks for it as the owner chooses under Settings, Access: for every change, or again after 30 minutes (the default) up to 30 days, counted from when it was last typed in that browser. A remembered confirmation belongs to one browser. Browsing never extends it. It ends with End, sign-out, or a change or reset of the administrator password, and a shorter choice shortens it to the new time counted from when the password was typed. "Do not ask" turns the check off for everyone who can open the dashboard; turning it on asks for the password and an acknowledgement of the warning, and every page says it is off. The API and the command line always ask for the password, for reads as well as changes. Host owners can reset it without deleting repository data.
- Every owner task must be available in both the dashboard and the command line, and its command must print JSON, by default or with `--json`. Add an MCP tool when a coding agent would use the task and MCP authority allows it. Exceptions: `reset-admin`, `setup-link` and `approve-host` are command-line only because they recover access when the dashboard cannot be used; `uninstall` is command-line only because it removes the service that serves the dashboard; `restore` is command-line only because it creates new folders and replacing the serving installation means stopping it first, so the dashboard shows the restore steps instead; `backup --output` is command-line only because it refuses while an OwnGit runs with that state directory, and Back up now covers a running one; `serve`, `service`, `runner` and `mcp` start or control processes rather than perform a task; accepting the plain-HTTP warning is dashboard-only because it concerns the browser's own connection (`network set` records the same acceptance when it saves an address other computers reach); check tasks, correction rounds and attempts are evidence that coding tools record, which the dashboard only shows.
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
- Merge publishes a fast-forward or a nonrewriting merge commit, with a protected receipt so a retry never creates a second merge. A source the target already contains is recorded as merged without a new commit. One pull request can be open per source and target branch pair.
- Never send private code to an external service without explicit, informed enablement that states what is sent.

### Imports

- Imports are inbound only. Never write to the source, and never let imported code grant check consent.
- Preserve local work: publish only branches and tags, never remove a local ref because the source deleted it, keep divergent refs and HEAD, and treat replaced history as a push does, following the repository's kept history choice.
- Never report an unconfirmed publication as complete. Refuse refreshes until the owner accepts the repository as found.
- Do not fetch or host Git LFS objects. Require Git-only consent when pointers are found or inspection is incomplete.

### Security and records

- Treat hosted code and check commands as untrusted, and record only the protection actually established.
- Show a line of text from Git or repository users (titles, reviewer labels, branch names, commit subjects, author names, paths, task and check names) in an element of its own with `dir="auto"`, and write every JSON answer or result through `bidi.MarshalJSON` or `bidi.EscapeJSON`. A Unicode direction control in such text then cannot reorder the states, commit IDs and attribution shown beside it, while right-to-left text keeps its direction. Show a document (a description, a note, a README or Markdown file, a commit body) in `.md` or `.textdoc` instead, never with `dir="auto"` on the whole: the style sheet gives each paragraph or line its own direction and keeps code left to right.
- Check a JSON request with `jsoninput.Valid` before decoding it. `encoding/json` would put U+FFFD in place of bytes that are not UTF-8 and of half a surrogate pair, and so store text other than the one sent.
- Password-free access does not remove administrator confirmation, Host and Origin checks, CSRF protection, or input limits. Only the owner's "Do not ask" choice turns administrator confirmation off, and only in the dashboard. Private-network membership alone does not prove installation ownership.
- Helper credentials are separate, revocable, and repository-scoped. They cannot change access or security settings or manage other credentials.
- Keep durable records (history, tasks, check outcomes) separate from disposable raw logs. Log cleanup never removes durable records.

## Security rules for code

- Never put setup tokens, passwords, or credentials in command-line values, environment variables, URLs, fixtures, or logs. Secrets come from owner-only files or interactive prompts. The one exception is the one-time setup link, which OwnGit may print when standard output is a terminal and nowhere else: never to a pipe, a file, the system journal, or a container log. Tests use synthetic credentials and disposable repositories.
- Do not weaken Host, exact-Origin, CSRF, access-mode, or administrator-confirmation checks. Every administrator page and change in the browser goes through the one shared confirmation gate; do not add a separate password check or exception to one handler. The administrator API authenticates every read and change with the administrator password in the Basic header; a browser session, remembered or not, never authorizes an API request. Mutating JSON endpoints accept JSON only, and the CLI client never follows redirects or retries authentication.
- Git subprocesses on hosted repositories use an app-owned HOME and empty global and system config, plus OwnGit's command-scope settings (`commandConfig` in `internal/gitexec/runner.go`, and the matching `-c` options in generated hooks). Do not reintroduce inherited hooks, credential helpers, filters, or client environment variables.
- Treat repository paths, symbolic-link blobs, binary blobs, and modes as data. Pass paths to Git as literal pathspecs and never resolve them on the host filesystem.
- Store only hashes of helper and runner tokens. Issuing or revoking a credential through the API or the command line always verifies the current administrator password; in the dashboard it follows administrator confirmation like every other administrator change.
- Deliver a new helper token only through an exclusively created, owner-only file that is never reopened by path. Generate the creation identity before the request, so a lost response can be revoked.
- API responses and CLI output never contain tokens, passwords, CA PEM, or credential file paths. The pull request API never authenticates with browser cookies or the administrator password.
- Once a raw check log is accepted, a later submission never replaces its bytes. Backup files stay owner-readable.
- Smart HTTP serves only discovery and upload and receive paths. Do not expose repository directories through a generic file server.
- Do not describe SHA-256 backup hashes as authentication. They detect corruption only.

## State and backup compatibility

Git refs and objects are the authoritative repository data. SQLite (`owngit.sqlite` in the state directory) holds pull request, review, task, check, and import records. OwnGit's own refs live under `refs/owngit/`. They are hidden from clients. Client pushes are accepted for `refs/heads/*`, `refs/tags/*` and the extra ref namespaces a repository lists in its settings, never for `refs/owngit/`.

- The state schema version is recorded in the database (`currentSchemaVersion` in `internal/state/store.go`). A schema change needs a migration from each accepted earlier catalog. OwnGit refuses unknown, altered, or newer databases without changing their files: a database is opened or upgraded only when its whole catalog (every table, index, trigger and view, whatever its name) is exactly the one the schema steps create at the version it records, and a migration commits only the current catalog. `internal/state/testdata/released` holds dumps of databases that releases wrote; add one when a release writes a new schema.
- The offline backup format version is `backupVersion` in `internal/recovery/recovery.go`. When you add durable records, add them to the backup and to restore validation, raise the version, keep released versions readable, and reject newer versions before decoding so records are never dropped silently. A backup is written in the previous release's format when it holds none of the new records (`format11Content`), so that release can still restore it; an older manifest that holds a newer record is refused. Backup and restore share one manifest size limit, `manifestLimit`; restore enforces it and the per-string bound while it reads, before anything is created. The limit counts each record by its size in memory (`manifestBudget`), the same way on both sides.
- Machine-local authority (credentials, consent, schedules, sessions) must not be exported or restored.

## Documentation

Contributors send code only. Before merging, the maintainers update the documentation for the behavior the change adds or alters, in English and Korean, together with any missing tests and the changelog. Describe what exists, and do not document planned features or unverified platforms as supported.

- `README.md`: what OwnGit is and how to start.
- `SECURITY.md`: how to report a vulnerability privately, and what OwnGit protects.
- `docs/OPERATIONS.md`: setup, access, recovery, imports, pull requests, checks, storage, and backups.
- `docs/AUTOMATIC_CHECKS.md`: configured checks and runners.
- `docs/CODING_TOOLS.md`: the coding-tool workflow. Its path and the path of its Korean version `docs/CODING_TOOLS.ko.md` are fixed by the release tool, and `cmd/owngit/check_budget_test.go` requires certain phrases in it and in the skill. Keep the guide and `integrations/skills/owngit-checks/SKILL.md` consistent.
- `README.ko.md`, `SECURITY.ko.md`, `docs/OPERATIONS.ko.md`, `docs/AUTOMATIC_CHECKS.ko.md`, and `docs/CODING_TOOLS.ko.md`: the Korean versions. Each pair keeps the same facts and sections, so the maintainers update both together.
- `CONTRIBUTING.md`: the short path for contributors. This reference: the maintainers' rules. Both are in English only.
- `CHANGELOG.md`: notable changes per version, in the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Some tests read these documents: `cmd/owngit/check_budget_test.go` (`docs/CODING_TOOLS.md`), `cmd/owngit/docs_windows_test.go` (the PowerShell password-file commands in `docs/OPERATIONS*.md` and `docs/CODING_TOOLS*.md`, run on Windows), and `tools/release/release_test.go` (the links in `docs/CODING_TOOLS*.md`). Headings are link targets for other documents, program messages, and tests, so search for a heading's anchor before you rename it.

## Continuous integration

The `CI` workflow ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs gofmt and `go vet` on every package and the tests on Linux (with the race detector), Windows, and macOS. A pull request tests only the packages its change can affect, as chosen by [`.github/scripts/affected-packages.sh`](../.github/scripts/affected-packages.sh). A test that reads files from another package or from the whole repository by path is listed once in that script; when you add such a test, add it there. A daily run and a run started by hand test every package, add Linux arm64, and check for known vulnerabilities. Before tagging a release, start a full run on the release commit and wait for it to pass. The `Document links` workflow checks the links and heading anchors between documents on every pull request.

## Real Docker test (opt-in)

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

## Releases

Releases are published on [GitHub Releases](https://github.com/juliankang4/owngit/releases), with a Homebrew tap and npm packages built from the same archives. `tools/release` builds and checks release artifacts using the Go toolchain and the standard library. It never publishes anything, and it signs the macOS artifacts only when given a Developer ID identity and a notarization profile; see [Signed macOS release](../packaging/README.md#signed-macos-release).

```sh
go run ./tools/release notices -check
go run ./tools/release build -out dist/portable
go run ./tools/release verify -dir dist/portable
```

- `notices -check` compares `THIRD_PARTY_NOTICES/` with the current build inputs. Run it after changing dependencies. To regenerate, write into a new empty directory and compare: `go run ./tools/release notices -out THIRD_PARTY_NOTICES.new`, then `diff -r THIRD_PARTY_NOTICES THIRD_PARTY_NOTICES.new`. Edit `packaging/notices/README.md.tmpl`, not the generated `README.md`.
- `build` compiles each target with `-trimpath -buildvcs=false` and `CGO_ENABLED=0`, writes deterministic archives, `SHA256SUMS`, and `manifest.json`, copies the one-line installers `packaging/installer/install.sh` and `install.ps1` and the Proxmox VE helper `proxmox.sh` unchanged beside them and records their SHA-256 in `manifest.json`, and then runs `verify`. Attach the three scripts to the release with the archives; `SHA256SUMS` lists only the archives. `-targets` selects a comma-separated subset.
- `verify` re-checks archive digests, contents, embedded build metadata, and the notice set. It checks that the installers equal both the manifest and `packaging/installer/` in the source (`-source`, the current module by default), and rejects private or build files such as `.git/`, `.local/`, `*.sqlite`, and `*.test`. It runs only the binary built for the host. Every archive carries the notices for all release targets, so `verify` refuses a notice entry that no target links only when the directory holds every release target.

Build targets are `darwin/arm64` (macOS on Apple silicon), `linux/amd64`, `linux/arm64`, and `windows/amd64`. Each archive holds the `owngit` executable, `LICENSE`, `THIRD_PARTY_NOTICES/`, `README.txt`, `docs/CODING_TOOLS.md`, `docs/CODING_TOOLS.ko.md`, and `integrations/skills/owngit-checks/SKILL.md`. The macOS archive also holds `OwnGit.app`, the menu bar icon, beside the executable. Building the app needs an Apple silicon Mac with the Xcode command-line tools; another host builds the macOS archive without it and says so, and `verify` refuses a signed macOS archive without it. `tools/release/resources.go` declares the last three by path, so moving them requires changing the release tool and its tests.

The application version is `internal/version.Version`. Every artifact name, manifest, and package file takes its version from that one value. 1.0.0 is the first version and has no changelog entry. Each later release adds a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` that matches `internal/version.Version`, with notable changes grouped under `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, and `Security`, and breaking changes and removals stated explicitly.

Native prototypes and package-manager files are built from a verified portable output. The native prototypes (a Debian package, and a macOS app and DMG) are not published; building the macOS app needs an Apple silicon Mac with the Xcode command-line tools.

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

`native -formats all` also builds the macOS app and DMG on an Apple silicon Mac. `packaging` renders the Homebrew formula, the WinGet manifests, the npm packages, and the Arch Linux `PKGBUILD` with its `.SRCINFO`; `-formats` selects `homebrew`, `winget`, `npm`, `aur`, or `all`. It marks the output `UNREADY` when a download URL, homepage, repository URL, publisher, or maintainer input is missing, or fails with `-strict`. The npm packages are written to `<out>/npm/`, which must not exist yet. They are built from a verified snapshot of the portable output, so the npm executables are the ones in the release archives. Check them with `npm pack` in each package directory; `packaging` never publishes. [packaging/README.md](../packaging/README.md) describes the templates and package contents.

The npm packages are published by the `npm publish` workflow ([`.github/workflows/npm-publish.yml`](../.github/workflows/npm-publish.yml)), started by hand from the Actions tab after the GitHub Release exists.

- It uses npm trusted publishing: npm exchanges the workflow's GitHub OIDC token for a short-lived publish token, so no npm token is stored and nobody signs in to npm, and npm adds provenance to each package. Each of the five packages must list this repository and the file name `npm-publish.yml` as a trusted publisher on npmjs.com, with `npm publish` allowed.
- It runs on an Apple silicon macOS runner, because `packaging` runs the executable for its own platform and requires the manifest to record that the release build ran it; release archives are built on an Apple silicon Mac, which records only the `darwin/arm64` run.
- It takes the version without the `v` prefix and checks out tag `v<version>`. It confirms that `internal/version.Version` at that tag matches, downloads the release's archives, `SHA256SUMS`, and `manifest.json`, checks `SHA256SUMS`, and renders the packages with `packaging -strict -formats npm`.
- It publishes the four platform packages and `owngit` last. A package version already on the registry is skipped, so a rerun after a partial failure publishes only the rest. A real publish runs only from `main`.
- With `dry_run`, every step runs, but each package goes through `npm publish --dry-run`, and the log warns when npm could not get a publish token for a package.

After publishing, run the `npm smoke test` workflow.

The container image is published by the `container publish` workflow ([`.github/workflows/container-publish.yml`](../.github/workflows/container-publish.yml)), also started by hand after the GitHub Release exists, with the version without the `v` prefix. It checks out tag `v<version>`, confirms `internal/version.Version`, downloads the release's two Linux archives and `SHA256SUMS`, and prepares the build context with `packaging/container/context.sh`, which checks them. Each architecture is built on a runner of that architecture and checked: the program reports the version, Git runs, the account is `owngit` (ID 10001), and the image records its install route. Each image is pushed by digest to `ghcr.io/juliankang4/owngit` with SLSA provenance and an SBOM, and the tags `X.Y.Z`, `X.Y` (only when the version is the newest release of that minor), and `latest` (only when it is the latest release) then name both. The revision label is the commit of the release tag, and the BuildKit image and the Dockerfile frontend are pinned by digest. A real publish runs only from `main`; `dry_run` builds and checks both images and publishes nothing. The workflow token needs `packages: write`; the package on GitHub must allow this repository to write it.

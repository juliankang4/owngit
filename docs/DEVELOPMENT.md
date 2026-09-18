# Development

## Architecture boundaries

OwnGit builds as one Go executable:

- `cmd/owngit` owns host commands and the HTTP server lifecycle.
- `internal/state` owns host-local SQLite settings, sessions, rate limits, repository records, pull request metadata, review provenance, revision bindings, and merge intents.
- `internal/auth` owns Argon2id password hashing, credential checks, and sessions.
- `internal/gitexec` owns the isolated Git environment, bounded processes, and process-tree cleanup.
- `internal/repository` owns bare repository creation, retained-history hooks, browsing, diffs, activity data, and commit-based tree restoration.
- `internal/pullrequest` owns pull request validation, current-revision eligibility, protected revision refs, review decisions, merge planning, atomic ref publication, and receipt reconciliation.
- `internal/prclient` owns the bounded, redirect-free remote JSON client used by CLI pull request commands.
- `internal/recovery` owns the versioned offline manifest, Git bundles, pull request round trips, validation, and restore staging.
- `internal/githttp` adapts the installed `git-http-backend` for streaming Smart HTTP.
- `internal/server` owns browser and JSON API routing, Host/origin/CSRF enforcement, authorization, and view models.
- `internal/webui` owns presentation models, localization, templates, and embedded assets.

Git refs and objects are the authoritative repository data. SQLite stays outside the repository folder and owns durable pull request titles, review provenance, and merge intent records. The default state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available, and contains `owngit.sqlite`. Retention, pull request revision, provenance, and merge receipt refs use `refs/owngit/`. OwnGit hides them from advertisement and rejects client pushes to that namespace.

Pull request writes share the repository lock used by Smart HTTP receive operations, and passive reads take its shared side. Merge persists its SQLite intent before publication. The publication point is one `git update-ref --stdin` transaction that verifies the source, updates the target from its exact old object ID, and creates the protected receipt. Startup, offline backup, and restore reconcile a matching receipt without issuing another merge.

## Dependencies

- `modernc.org/sqlite` provides SQLite without requiring a C compiler or separate database service.
- `golang.org/x/crypto` provides Argon2id password hashing.
- `golang.org/x/sys` provides Windows Job Object support for containing Git process trees.

The dependency graph is recorded in `go.mod` and `go.sum`.

## Build and checks

```sh
go build ./cmd/owngit
go test ./...
go vet ./...
```

Integration tests use temporary repositories and the real system Git to cover Smart HTTP, destructive ref retention, pull request revision invalidation, conflicts, fast-forward and merge-commit publication, receipt reconciliation, browser restoration, offline backup recovery, and process cancellation.

## Security notes for development

- Never pass setup tokens or passwords in command-line values, environment variables, URLs, fixtures, or logs. Tests must use synthetic credentials and disposable repositories.
- Do not weaken Host, exact-Origin, CSRF, access-mode, or administrator-confirmation checks.
- Pull request API authentication uses open or shared general-access semantics and never browser cookies or the administrator password. Mutating endpoints accept JSON only. Keep redirects and automatic authentication retries disabled in the CLI client.
- Git subprocesses use an app-owned HOME and empty portable global/system config. Do not reintroduce inherited hooks, credential helpers, filters, or client-provided environment variables.
- Selected-file restore uses a private Git index, and patch previews pass each repository path as a literal pathspec. Treat repository paths, symbolic-link blobs, binary blobs, and Git modes as data. Never resolve them as host filesystem paths.
- Offline backup format version 2 includes password hashes, every repository ref, and durable pull request records. Readers validate version 1 without pull request fields and version 2 with exact revision and receipt refs. Keep backup files owner-readable, reject variable bundle paths, and do not describe SHA-256 corruption checks as authentication.
- Do not expose repository directories through a generic file server. Smart HTTP serves only discovery and upload/receive RPC paths. Keep the state directory on host-local storage.

## Platform limits

Runtime verification covers native macOS, Windows 11, and Ubuntu Linux, plus an isolated Debian environment with Git 2.39.5 on NAS hardware. The latest source passed the full Go test suite with the race detector on macOS, and the affected packages plus CLI help on Windows, Linux, and the NAS host; tests with POSIX-shell fixtures skip on Windows. Native Git clients on macOS, Windows, and Linux have exercised a NAS-hosted server, and command-driven pull request journeys have run from macOS, Linux, Windows, and the NAS host, including pull request backup and restore. Cross-device offline restores have preserved refs, HEAD, repository metadata, and password hashes while dropping machine-local authority and rebuilding host-specific hooks. Repositories on a mounted SMB share and on NFS passed fetch, push, force-push retention, deletion retention, restart, offline backup, and independent reconstruction with one writer at a time. State, credentials, and backups stayed on local storage. Pull request merge checks for Git 2.38 or newer when merge is requested; the older-Git refusal was exercised with a Git 2.37 wrapper rather than an actual old Git executable. This does not verify every network route, power loss, concurrent writers, or general network-filesystem behavior. Release packaging, compatibility with future releases, production deployment, and primary-storage migration remain unverified. Explicit Windows UNC state paths are rejected.

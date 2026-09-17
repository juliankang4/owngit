# Development

## Architecture boundaries

OwnGit builds as one Go executable:

- `cmd/owngit` owns host commands and the HTTP server lifecycle.
- `internal/state` owns host-local SQLite settings, sessions, rate limits, and repository records.
- `internal/auth` owns Argon2id password hashing, credential checks, and sessions.
- `internal/gitexec` owns the isolated Git environment, bounded processes, and process-tree cleanup.
- `internal/repository` owns bare repository creation, retained-history hooks, browsing, diffs, and activity data.
- `internal/githttp` adapts the installed `git-http-backend` for streaming Smart HTTP.
- `internal/server` owns routing, Host/origin/CSRF enforcement, authorization, and view models.
- `internal/webui` owns presentation models, localization, templates, and embedded assets.

Git refs and objects are the authoritative repository data. SQLite stays outside the repository folder. The default state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available, and contains `owngit.sqlite`. Retention and provenance refs use `refs/owngit/`; OwnGit hides them from advertisement and rejects client pushes to that namespace.

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

Integration tests use temporary repositories and the real system Git to cover Smart HTTP, destructive ref retention, and process cancellation.

## Security notes for development

- Never pass setup tokens or passwords in command-line values, environment variables, URLs, fixtures, or logs. Tests must use synthetic credentials and disposable repositories.
- Do not weaken Host, exact-Origin, CSRF, access-mode, or administrator-confirmation checks.
- Git subprocesses use an app-owned HOME and empty portable global/system config. Do not reintroduce inherited hooks, credential helpers, filters, or client-provided environment variables.
- Do not expose repository directories through a generic file server. Smart HTTP serves only discovery and upload/receive RPC paths. Keep the state directory on host-local storage.

## Platform limits

Runtime verification currently covers macOS with Apple Git. Windows, Linux, NAS use, upgrades, and deployment remain unverified. Explicit Windows UNC state paths are rejected.

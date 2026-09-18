# OwnGit

OwnGit is Git for HomeLab & Local. It keeps private repositories and their history on your own computer, NAS, or home server, with a browser dashboard for everyday use. Repositories remain ordinary bare Git repositories served by the Git installation on the host. No cloud Git account or subscription is required.

![OwnGit showing an example repository.](docs/images/overview.png)

## What it does

- Supports clone, fetch, and push over Smart HTTP from standard Git clients.
- Creates, inspects, reviews, and merges pull requests through structured CLI commands. Ordinary `git push` remains available without a pull request.
- Shows repositories, branches, tags, files, commits, diffs, and author-date activity in the browser.
- Retains replaced or deleted branch and tag history in hidden refs.
- Restores a whole repository tree or selected files in the browser after showing additions, changes, and deletions.
- Creates and restores offline backups containing repository refs, objects, pull request records, and portable OwnGit settings.
- Runs as one Go executable with a host-local SQLite database. No Node, Python, or database service is required at runtime.
- Provides English and Korean interfaces with Light, Dark, and System appearance modes.

## Quickstart

Prerequisites: Go 1.27 or newer, and Git with an executable `git-http-backend`.

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

On first run, OwnGit writes an owner-readable setup file inside the state directory. Open that file in the installation owner's browser and follow the setup. The secret is never printed or passed as a browser argument. The default address is `http://127.0.0.1:7654`.

See [Operations](docs/OPERATIONS.md) for access from another device, Host approval, recovery, and storage details.

## Access and security

- The server is local-only by default. General repository access may be password-free or protected by one shared password; there are no individual accounts.
- A separate administrator password protects security settings. Every security change requires entering the current administrator password again.
- Plain HTTP does not encrypt transport. Prefer Tailscale when connecting from another device, but do not infer transport protection from a host name alone. OwnGit has no built-in TLS, and public Internet hosting is out of scope.

## Status and limits

OwnGit is an initial implementation with no release download. Build it from source as shown above. Runtime checks have passed on macOS, Windows 11, Ubuntu Linux, and Debian with Git 2.39.5 in an isolated container on NAS hardware. Command-driven pull request journeys, cross-device Git use, and offline recovery have also been exercised. Repositories on a mounted SMB share and on NFS passed fetch, push, retention, restart, and offline backup with one writer at a time. A snapshot-based upgrade check from the first commit to a later build preserved repository records, though compatibility with future releases is untested. Release packaging, production deployment, and primary-storage migration remain unverified.

History retention has no permanent-delete interface and is not a backup by itself. OwnGit provides browser restore and host-owner offline backup commands. Pull request commands use optional review: the external coding tool supplies a labelled result or explicitly skips review. OwnGit does not launch a reviewer or claim that a supplied result is independent. Project checks are not configured in this release and appear as `not_configured`, never as passed. The browser has no pull request interface yet. Pull request merge requires Git 2.38 or newer, while clone, fetch, push, and browsing do not depend on that merge capability.

A dedicated import interface, automatic backup schedules, Git LFS hosting, project check execution, and OwnGit-hosted coding-agent execution are not implemented.

No software license has been selected, so the source must not be treated as licensed yet.

## Documentation

- [Operations](docs/OPERATIONS.md): setup, access, recovery, storage, and backups
- [Development](docs/DEVELOPMENT.md): architecture, dependencies, checks, and security boundaries
- [Product boundaries](docs/PRODUCT_BOUNDARIES.md): durable product design constraints

# OwnGit

OwnGit is Git for HomeLab & Local. It keeps private repositories and their history on your own computer, NAS, or home server, with a browser dashboard for everyday use. Repositories remain ordinary bare Git repositories served by the Git installation on the host. No cloud Git account or subscription is required.

![OwnGit showing an example repository.](docs/images/overview.png)

## What it does

- Supports clone, fetch, and push over Smart HTTP from standard Git clients.
- Shows repositories, branches, tags, files, commits, diffs, and author-date activity in the browser.
- Retains replaced or deleted branch and tag history in hidden refs.
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

OwnGit is an initial implementation with no release download. Build it from source as shown above. Runtime verification currently covers the core macOS paths; Windows, Linux, NAS use, upgrades, and deployment remain unverified.

History retention has no restore or permanent-delete interface and is not a backup. Import, Git LFS hosting, project check execution, and coding-agent execution are not implemented.

No software license has been selected, so the source must not be treated as licensed yet.

## Documentation

- [Operations](docs/OPERATIONS.md): setup, access, recovery, storage, and backups
- [Development](docs/DEVELOPMENT.md): architecture, dependencies, checks, and security boundaries
- [Product boundaries](docs/PRODUCT_BOUNDARIES.md): durable product design constraints

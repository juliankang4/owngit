# OwnGit

OwnGit is Git for HomeLab & Local. It keeps private repositories and their history on your own computer, NAS, or home server, with a browser dashboard for everyday use. Repositories remain ordinary bare Git repositories served by the Git installation on the host. No cloud Git account or subscription is required.

![OwnGit showing an example repository.](docs/images/overview.png)

## What it does

- Supports clone, fetch, and push over Smart HTTP from standard Git clients.
- Shows repositories, branches, tags, files, commits, diffs, author-date activity, pull requests, and revision-bound check evidence in the browser.
- Creates and merges pull requests in the browser at the exact revisions it displays. JSON CLI commands can also create, inspect, review, and merge them. Ordinary `git push` works without a pull request, and review is optional.
- Records checks that a helper runs in your own environment. Configured checks that the owner enables can run on the host, in restricted local Docker, or on a separate runner. Checks and reviews are advisory and never hold a merge.
- Imports a repository from another HTTPS Git host and refreshes it on demand or on a schedule, without writing to the source.
- Keeps replaced or deleted branch and tag history in hidden refs. The browser restores a whole tree or selected files after previewing every change.
- Creates and restores offline backups of repository refs and objects, pull request, check, and import records, and portable settings.
- Runs as one Go executable with a host-local SQLite database. No Node, Python, or database service is required at runtime.
- Provides English and Korean interfaces with Light, Dark, and System appearance modes.

## Quickstart

Prerequisites: Go 1.27 or newer, and Git with an executable `git-http-backend`.

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

On first run, OwnGit writes an owner-readable setup file inside the state directory. Open that file in the installation owner's browser and follow the steps. The setup secret is never printed or passed as a browser argument. The default address is `http://127.0.0.1:7654`.

Create a repository from the dashboard, then use its clone address, for example `http://127.0.0.1:7654/git/project.git`, with any Git client. [Operations](docs/OPERATIONS.md) covers access from other devices, moving existing repositories, recovery, and backups.

## Access and security

- The server is local-only by default. General repository access can be password-free or protected by one shared password. There are no individual accounts.
- A separate administrator password protects security settings, and every security change asks for it again.
- OwnGit serves plain HTTP, which is not encrypted, and has no built-in TLS. Prefer Tailscale or your own VPN for connections from another device. Public Internet hosting is out of scope.

## Status and limits

No release has been published yet, so build from source as shown above.

OwnGit cannot delete retained history, so a force-push or branch deletion does not remove a committed secret from OwnGit or its backups; rotate any secret you push by mistake. Retained history is not a backup, and backups run only when you start them. Host and runner check commands run with their account's permissions and are not sandboxes. OwnGit records review labels and check results that other tools supply, but it never runs reviewers or coding agents. Git LFS objects are not hosted or imported. Pull request merge requires Git 2.38 or newer on the OwnGit host.

## License

OwnGit's source is available under the [MIT License](LICENSE). Notices for the third-party code and assets built into the executable are in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES/README.md).

## Documentation

- [Operations](docs/OPERATIONS.md): setup, access, recovery, moving repositories, imports, pull requests, checks, storage, and backups
- [Automatic checks](docs/AUTOMATIC_CHECKS.md): checks that run on the host, in restricted Docker, or on a separate runner
- [Coding tools](docs/CODING_TOOLS.md): running project checks from a coding tool, and the shared skill
- [Contributing](CONTRIBUTING.md): building, testing, and changing OwnGit
- [Changelog](CHANGELOG.md): notable changes in each version

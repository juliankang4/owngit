<p align="center">
  <img src="internal/webui/assets/logo.svg" width="96" height="96" alt="OwnGit logo">
</p>

<h1 align="center">OwnGit</h1>

<p align="center">
  <a href="CHANGELOG.md"><img src="https://img.shields.io/badge/version-1.1.8-0A62C9?style=flat&colorA=222222" alt="Version 1.1.8"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0-58A6FF?style=flat&colorA=222222" alt="GPL-3.0 License"></a>
  <a href="https://github.com/juliankang4/homebrew-tap"><img src="https://img.shields.io/badge/Homebrew-juliankang4%2Ftap-FBB040?style=flat&colorA=222222&logo=homebrew&logoColor=white" alt="Homebrew tap juliankang4/tap"></a>
  <a href="https://www.npmjs.com/package/owngit"><img src="https://img.shields.io/npm/v/owngit?style=flat&colorA=222222&color=CB3837&logo=npm&logoColor=white&label=npm" alt="npm package owngit"></a>
</p>

<p align="center"><b>English</b> | <a href="README.ko.md">한국어</a></p>

OwnGit is a self-hosted Git server for one person or a small group. It keeps your private repositories on your own computer, NAS or home server, and gives you a browser dashboard for them. You need no cloud account or subscription.

Install it with one command, then open the setup link it prints.

![The OwnGit dashboard with commit activity, repositories and latest activity for sample projects.](docs/images/overview.png)

## Features

- Clone, fetch and push with any Git client over HTTP. Repositories are ordinary bare Git repositories.
- Browse files, commits, diffs, branches and tags, and download any of them as ZIP or tar.gz.
- Open, review and merge pull requests in the browser or from the command line. A plain `git push` needs no pull request.
- Keep the history that a force-push, an import or a deletion would remove, and restore files from the browser.
- Back up and restore repositories and records, on a schedule or on demand.
- Import a repository from another HTTPS Git host and keep it up to date.
- Share one repository read-only through a link you can revoke.
- Run GitHub Actions workflow files from your repositories on this computer, in Docker or on your own runner, with no GitHub account.
- Record and run project checks, and connect coding tools through JSON commands or MCP.
- One Go program with a SQLite file. There is no database service to run.
- English or Korean, with Light, Dark and System appearance.

## Install

On Linux (x64, ARM64) and macOS (Apple silicon):

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

On Windows (x64), in PowerShell:

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

The installer checks the download against the release's `SHA256SUMS`, installs `owngit`, runs it as a service and prints the setup link.

Other ways to install:

| Route | Command |
| --- | --- |
| Homebrew (macOS, Linux) | `brew install juliankang4/tap/owngit` |
| Homebrew menu bar app (macOS, optional) | `brew install --cask juliankang4/tap/owngit`, then `owngit service install` |
| npm (needs Node.js) | `npm install -g owngit` |
| Arch Linux | `makepkg -si` with the `PKGBUILD` from the [latest release](https://github.com/juliankang4/owngit/releases/latest) |
| Docker Compose | `docker compose up -d` with [`compose.yaml`](packaging/container/compose.yaml), then `docker compose exec -it owngit owngit setup-link` |
| Proxmox VE host, as root | `/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh \| /bin/sh` |
| Source (Go 1.27 or newer) | `go build -o bin/owngit ./cmd/owngit` |

Except for the container and Proxmox VE, OwnGit needs Git on PATH with an executable `git-http-backend`. Pull request merging needs Git 2.38 or newer. Homebrew and the Arch package install Git. On Windows, use Command Prompt for npm, because PowerShell's default policy blocks the npm scripts.

[Operations](docs/OPERATIONS.md) has the details for each route.

## Quickstart

1. Run OwnGit as a service that starts by itself (the one-line installer already did this):

   ```sh
   owngit service install
   ```

2. Open the setup link that the command prints. Choose the repository folder and the passwords there. The link works once, within 15 minutes. `owngit setup-link` prints a new one.
3. Create a repository in the dashboard and clone it, for example from `http://127.0.0.1:7654/git/project.git`.
4. Open Settings, Storage & recovery, Backups. Choose a backup folder outside the state and repository folders, preferably on another disk, and turn scheduled backups on. OwnGit makes the first backup when you save. See [Storage and backups](docs/BACKUPS.md#scheduled-backups).

On a computer with a screen, OwnGit accepts connections only from that computer. On a server without a screen, it listens on every address so you can finish setup from another device, and it answers only the setup page until you do.

To run it in a terminal instead, use `owngit serve`.

When something does not work, run `owngit doctor`. It reports what it found and the command that fixes it.

### The OwnGit icon

On macOS 27, a menu bar manager such as Hidden Bar can hide the OwnGit icon when its app is outside `/Applications`. The one-line installer puts the app there when it can. Homebrew users get the same with the optional cask, which also installs the formula if it is missing.

On a desktop, the service also adds an OwnGit icon to the menu bar, notification area or panel. It shows the status, the clone address and the latest pushes, and opens the dashboard. On GNOME it needs the AppIndicator extension.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/tray-panels.dark.png">
  <img src="docs/images/tray-panels.png" alt="The OwnGit panel on macOS, Windows, GNOME and Omarchy, each showing that OwnGit runs, the clone address with a Copy button, the latest pushes to sample repositories, and Open dashboard.">
</picture>

The icon also shows desktop notifications for pushes, new pull requests, failed checks, imports and backups that did not finish successfully, and new releases.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/push-notifications.dark.png">
  <img src="docs/images/push-notifications.png" alt="One OwnGit push notification on macOS, Windows, GNOME and Omarchy: 2 new commits in notes, pushed from another computer.">
</picture>

## Update and uninstall

OwnGit never updates itself. When a new release exists, the dashboard shows the update command for the way you installed it, and `owngit update` prints the same command.

`owngit uninstall` removes the service. Your repositories and settings stay, and the command tells you where they are and how to remove the program.

## Access and security

- Repository access is open or protected by one shared password. There are no user accounts. With open access, anyone who reaches OwnGit, including every device and program on the network, can read, push, delete branches and tags, create repositories, open and merge pull requests and restore files. Choose the shared password when the network has devices or people you do not fully trust.
- A separate administrator password protects settings, repository deletion, rename, imports and check policies, unless you chose on the Access tab of Settings to be asked less often or not at all.
- OwnGit serves plain HTTP. For other devices, use Tailscale (`owngit tailscale on`) or a reverse proxy for HTTPS. Plain HTTP on the LAN needs your explicit consent.
- Do not put OwnGit on the public Internet. The one exception is an optional public address that answers share links only.
- Once a day, OwnGit asks GitHub whether a newer release exists. It sends no repository data. Turn this off in Settings, or start with `--no-update-check`.

Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Limits

- Kept history and backups retain a secret you pushed by mistake. Rotate the secret.
- Kept history is not a backup. Scheduled backups start only after you choose a backup folder.
- Checks that run on the host or a runner use that account's permissions. They do not run in a sandbox.
- GitHub Actions workflows run `run` steps and a few built-in actions. OwnGit does not download other actions.
- Git LFS objects are not hosted or imported.
- Merging a pull request needs Git 2.38 or newer on the server.

## Documentation

- [Operations](docs/OPERATIONS.md): install, setup, service, network access and settings
- [Repositories](docs/REPOSITORIES.md): renaming, sharing, deleting and importing repositories
- [Backups](docs/BACKUPS.md): storage, backups and restore
- [Coding tools](docs/CODING_TOOLS.md): pull requests and checks from the command line or MCP
- [Automatic checks](docs/AUTOMATIC_CHECKS.md): checks on the host, in Docker or on a runner
- [Workflows](docs/WORKFLOWS.md): GitHub Actions workflow files, secrets, manual runs and schedules
- [Contributing](CONTRIBUTING.md) and [Changelog](CHANGELOG.md)

## License

[GPL-3.0-or-later](LICENSE). Versions 1.1.4 and earlier were released under the MIT license. Notices for third-party code are in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES/README.md).

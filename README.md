<p align="center">
  <img src="internal/webui/assets/logo.svg" width="96" height="96" alt="OwnGit logo">
</p>

<h1 align="center">OwnGit</h1>

<p align="center">
  <a href="CHANGELOG.md"><img src="https://img.shields.io/badge/version-1.1.2-0A62C9?style=flat&colorA=222222" alt="Version 1.1.2"></a>
  <a href="CHANGELOG.md"><img src="https://img.shields.io/badge/changelog-keep-E05735?style=flat&colorA=222222" alt="Changelog"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-58A6FF?style=flat&colorA=222222" alt="MIT License"></a>
  <a href="https://github.com/juliankang4/homebrew-tap"><img src="https://img.shields.io/badge/Homebrew-juliankang4%2Ftap-FBB040?style=flat&colorA=222222&logo=homebrew&logoColor=white" alt="Homebrew tap juliankang4/tap"></a>
  <a href="https://www.npmjs.com/package/owngit"><img src="https://img.shields.io/npm/v/owngit?style=flat&colorA=222222&color=CB3837&logo=npm&logoColor=white&label=npm" alt="npm package owngit"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-00ADD8?style=flat&colorA=222222&logo=go&logoColor=white" alt="Go"></a>
  <a href="https://www.sqlite.org"><img src="https://img.shields.io/badge/SQLite-003B57?style=flat&colorA=222222&logo=sqlite&logoColor=white" alt="SQLite"></a>
</p>

<p align="center"><b>English</b> | <a href="README.ko.md">한국어</a></p>

OwnGit is a self-hosted Git server for home labs and local machines. It keeps private repositories and their history on your own computer, NAS, or home server, with a browser dashboard for everyday use. Repositories remain ordinary bare Git repositories served by the Git installation on the host. No cloud Git account or subscription is required.

![The OwnGit dashboard with commit activity, repositories, and latest activity for sample projects.](docs/images/overview.png)

## What it does

- Supports clone, fetch, and push over Smart HTTP from standard Git clients.
- Shares OwnGit over HTTPS on your tailnet through Tailscale Serve, turned on in Settings or with `owngit tailscale on`, or runs behind a reverse proxy such as Caddy, nginx, or Traefik. Once that HTTPS address works, a browser that opens a dashboard page over plain HTTP by its name on OwnGit's own port goes to the same page there. Network settings saved with `owngit network set` apply at every start, also for a background service. See [Share on your tailnet over HTTPS](docs/OPERATIONS.md#share-on-your-tailnet-over-https) and [Behind a reverse proxy](docs/OPERATIONS.md#behind-a-reverse-proxy).
- Shows repositories, branches, tags, files, commits, diffs, author-date activity, pull requests, check evidence, and the languages a repository is written in (by file size on the default branch, honoring Linguist attributes in `.gitattributes` with Git 2.40 or newer) in the browser, and downloads a branch, tag, or commit as a ZIP or tar.gz archive, from the browser or with `curl`.
- Creates, closes, reopens, and merges pull requests in the browser at the exact revisions it displays, and from JSON CLI commands. Ordinary `git push` works without a pull request, and review is optional.
- Lists, shows, and creates repositories from the command line with `owngit repo`, and offers the pull request, repository, and check commands to coding tools that support MCP through `owngit mcp`, a local server on standard input and output. See [Coding tool integration](docs/CODING_TOOLS.md#mcp-server).
- Records checks that a helper runs in your own environment, and runs owner-enabled checks on the host, in restricted local Docker, or on a separate runner. Checks and reviews are advisory and never hold a merge.
- Imports a repository from another HTTPS Git host and refreshes it on demand or on a schedule, without writing to the source.
- Keeps replaced or deleted branch and tag history in hidden refs. The browser restores a whole tree or selected files after previewing every change.
- Creates and restores offline backups of repository refs and objects, pull request, check, and import records, and portable settings.
- Runs as one Go executable with a host-local SQLite database. No Node, Python, or database service is required at runtime.
- Provides English and Korean interfaces with Light, Dark, and System appearance modes.

## Install

Every install route needs Git with an executable `git-http-backend` on the host. Homebrew and the Arch Linux package install Git for you.

The one-line installer downloads the latest release for this computer, checks it against the release's `SHA256SUMS`, installs `owngit` and runs it as a service with `owngit service install`, which prints the setup link at the end when you run it in a terminal. On Linux (x64, ARM64) and macOS (Apple silicon):

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

On Windows (x64), in PowerShell:

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

[One-line installer](docs/OPERATIONS.md#one-line-installer) lists its options, such as a pinned version or no service, and where it puts the program.

With [Homebrew](https://brew.sh) on macOS (Apple silicon) or Linux (x64, ARM64):

```sh
brew install juliankang4/tap/owngit
```

With [npm](https://www.npmjs.com/package/owngit) on macOS (Apple silicon), Linux (x64, ARM64), or Windows (x64). This route needs Node.js to install and to start OwnGit:

```sh
npm install -g owngit
```

On Arch Linux (x64, ARM64) or Omarchy, build the package from the `PKGBUILD` attached to each release from 1.0.3 on. `makepkg` downloads the release archive, checks its SHA-256, and installs `owngit` with `pacman`; it needs the `base-devel` group. An AUR package, `owngit-bin`, is planned.

```sh
mkdir owngit-bin && cd owngit-bin
curl -fLO https://github.com/juliankang4/owngit/releases/latest/download/PKGBUILD
makepkg -si
```

Or download the archive for your platform from [GitHub Releases](https://github.com/juliankang4/owngit/releases) and check it against `SHA256SUMS`. The macOS binary is signed and notarized by Apple; the Linux and Windows binaries are not signed.

To build from source with Go 1.27 or newer:

```sh
git clone https://github.com/juliankang4/owngit.git
cd owngit
go build -o bin/owngit ./cmd/owngit
```

### Update and remove

OwnGit never updates itself. When a newer release exists, the dashboard notice shows a confirmed administrator the one command that updates OwnGit the way it was installed, with a Copy button, and `owngit update` prints the same command:

| Installed with | The command |
| --- | --- |
| Homebrew | `brew upgrade owngit` |
| npm | `npm install -g owngit@X.Y.Z` |
| The Arch Linux `PKGBUILD` (`owngit-bin`) | builds the new release's `PKGBUILD` with `makepkg -si` |
| A release archive or the one-line installer | runs the new release's installer, which checks the archive against `SHA256SUMS` and puts its `owngit` in place of this one; on Windows it unpacks the new release into a folder beside the current one |

When a service runs this OwnGit, the command also runs `owngit service install`, which restarts the service with the new version.

`owngit uninstall` removes what `owngit service install` created: the service and, on Windows, the copy in Program Files. The state and the repositories stay, and the command says where they are. The program files belong to whatever put them there, so the command ends by naming how to remove them: `brew uninstall owngit`, `npm uninstall -g owngit`, `sudo pacman -R owngit-bin`, or the file to delete for an archive. See [Update and uninstall](docs/OPERATIONS.md#update-and-uninstall).

## Quickstart

On Linux, macOS and Windows, install OwnGit as a service that runs in the background and starts again by itself:

```sh
owngit service install
```

It asks nothing, apart from the `sudo` password on a Linux computer you reached over SSH (and one User Account Control approval from a Windows administrator account, which also installs Git with `winget` if it is missing), and prints a one-time setup link at the end. Open the link in a browser to choose the repository folder and the passwords; it works once, within 15 minutes, and `owngit setup-link` prints a new one. On a desktop, OwnGit runs as your user and answers only on this computer, at `http://127.0.0.1:7654`. On a computer without a screen, such as a server or a container you reach over SSH, it listens on every address and the link uses this computer's LAN or tailnet address, so you open it on another device; until setup is finished, that address answers only the setup page, and a server with only a public address gets an SSH tunnel command instead. [Run as a service](docs/OPERATIONS.md#run-as-a-service) explains who runs the service on each system, [On Windows](docs/OPERATIONS.md#on-windows) what the approval does, and both how to update, stop and remove it. When Homebrew installed OwnGit, the service is handed to `brew services`, whose log is `$(brew --prefix)/var/log/owngit.log`.

To run OwnGit in the foreground instead, start it with:

```sh
owngit serve
```

From a source build, run `./bin/owngit serve`; from an unpacked archive, `./owngit serve`. The first time OwnGit starts from a terminal, setup runs there: choose English or 한국어, then "Continue in this terminal" or "Open the web dashboard", where the browser shows a short code that you approve in the terminal. Without a terminal, as under `brew services`, OwnGit writes an owner-readable setup file inside the state directory and opens it in your browser, or logs its path with `--no-open`; the link itself never reaches a log. See [First-time setup](docs/OPERATIONS.md#first-time-setup).

Create a repository from the dashboard, then use its clone address, for example `http://127.0.0.1:7654/git/project.git`, with any Git client. [Operations](docs/OPERATIONS.md) covers access from other devices, moving existing repositories, recovery, and backups. When something does not work, `owngit doctor` names what it found on this computer and the command that fixes it.

## Resource use

OwnGit is one program of about 30 MB (an 18 MB download) plus the Git already on the computer. Measured with OwnGit 1.1.0 after setup, once memory had settled with nobody using it:

| | Linux x64 | macOS (Apple silicon) |
| --- | --- | --- |
| Memory, no repositories | about 45 MB | about 35 MB |
| Memory, 100 small repositories | about 50 MB | about 50 MB |
| CPU | under 0.1% of one core | under 0.1% of one core |

Each password check needs about 70 MB more for a moment, because passwords are hashed with Argon2id. OwnGit checks a password when you set one or sign in, when you confirm a settings change with the administrator password, and on Git and API requests when a shared access password is set. After the shared password is checked once, OwnGit accepts the same password for five minutes without hashing it again, so the several requests of one clone or push need one check. It runs at most four checks at once, and gives the memory back to the system a few minutes later. Each clone or push also runs Git, whose memory depends on the repository. On Linux, memory is the resident set size reported by `/proc` and `ps`; on macOS it is the Memory column of Activity Monitor, where `ps` can show about 120 MB because macOS keeps memory that OwnGit gave back.

## Access and security

- On a computer with a screen, the server is local-only by default. A computer without one, such as a server reached over SSH or a container, listens on every address from the first start so that setup can happen on another device; until setup is finished it answers only the one-time setup link. General repository access can be password-free or protected by one shared password. There are no individual accounts.
- A separate administrator password protects security settings. The dashboard asks for it again after 30 minutes by default; under Settings, Access you can make it ask every time, remember it for up to 30 days in one browser, or turn the check off.
- After setup, OwnGit asks GitHub once a day whether a newer release exists and shows a notice on the dashboard. It sends no repository data and never updates itself. Turn it off in Settings, or start with `--no-update-check` so it never checks. See [New-release notice](docs/OPERATIONS.md#new-release-notice).
- OwnGit serves plain HTTP, which is not encrypted, and has no built-in TLS. TLS comes from Tailscale on this computer (see [Share on your tailnet over HTTPS](docs/OPERATIONS.md#share-on-your-tailnet-over-https)) or a reverse proxy in front of OwnGit, and OwnGit believes forwarded headers only from proxies you configure (see [Behind a reverse proxy](docs/OPERATIONS.md#behind-a-reverse-proxy)). Prefer Tailscale or your own VPN for connections from another device. Public Internet hosting is out of scope.

## Status and limits

A force-push or branch deletion leaves the old commits in kept history, so a committed secret stays in OwnGit and its backups. Deleting the whole repository with its files is the only way to remove that history, and earlier backups still contain it; rotate any secret you push by mistake. Kept history is not a backup, and backups run only when you start them. Host and runner check commands run with their account's permissions and are not sandboxes. OwnGit records review labels and check results that other tools supply, but it never runs reviewers or coding agents. Git LFS objects are not hosted or imported. Pull request merge requires Git 2.38 or newer on the OwnGit host.

## License

OwnGit's source is available under the [MIT License](LICENSE). Notices for the third-party code and assets built into the executable are in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES/README.md).

## Documentation

- [Operations](docs/OPERATIONS.md): setup, access, recovery, moving repositories, imports, pull requests, checks, storage, and backups
- [Automatic checks](docs/AUTOMATIC_CHECKS.md): checks that run on the host, in restricted Docker, or on a separate runner
- [Coding tools](docs/CODING_TOOLS.md): running project checks from a coding tool, and the shared skill
- [Contributing](CONTRIBUTING.md): building, testing, and changing OwnGit
- [Changelog](CHANGELOG.md): notable changes in each version
- [Security policy](SECURITY.md): reporting a vulnerability privately

# Operations

<p align="center"><b>English</b> | <a href="OPERATIONS.ko.md">한국어</a></p>

This guide is for the person who installs and runs OwnGit, the self-hosted Git server. It covers installing, first-time setup, running OwnGit as a service, settings, reaching it from other devices, recovery on the host, and the limits that protect the server.

Commands are written as `owngit`. From an unpacked archive run `./owngit`, and from a source build `./bin/owngit` ([Build and run](../CONTRIBUTING.md#build-and-run)).

Other tasks have their own guides:

- Repositories, renaming, share links, deleting and importing from another Git host: [Repositories](REPOSITORIES.md).
- Storage, backups, verification and restore: [Backups](BACKUPS.md).
- Command-line pull requests and checks run by a coding tool: [Coding tools](CODING_TOOLS.md).
- Automatic checks after a push, and raw check logs: [Automatic checks](AUTOMATIC_CHECKS.md).

## One-line installer

The installer downloads the latest release, checks its SHA-256, installs it and starts it as a service. It asks no questions. On Linux and macOS:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

On Windows, in PowerShell:

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

In a terminal, it ends by printing the setup link. Running it again keeps the state and the repositories.

The program goes to `~/.local/bin/owngit` (`/usr/local/bin/owngit` for root) or, on Windows, to a folder per release under `%LOCALAPPDATA%\Programs\OwnGit`. The installer does not change PATH; when the folder is not on PATH, it says how to run the program.

| Linux and macOS | Windows | What it does |
| --- | --- | --- |
| `--version 1.1.3` | `-Version 1.1.3` | Installs that release instead of the latest. |
| `--no-service` | `-NoService` | Installs the program only. Start it with `owngit serve` or `owngit service install`. |
| `--to PATH` | `-Dir FOLDER` | Installs to another path or folder. |

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh -s -- --version 1.1.3 --no-service
```

```powershell
& ([scriptblock]::Create((irm -MaximumRedirection 0 https://owngit.app/install.ps1))) -Version 1.1.3 -NoService
```

The installer stops before it downloads anything when:

- the program path is a symbolic link, such as npm's `owngit`. Update that install its own way, or choose another path with `--to`;
- another account can change the program's folder, any folder above it, or the temporary folder. Choose a folder only you (or root, or the Windows administrators) can change.

### Check the script before it runs

The one-line command runs the script before anything checks it. `SHA256SUMS` covers only the archives, and it comes from the same release, so it is not an independent signature. To check the script yourself:

1. Download `install.sh`, `install.ps1` or `proxmox.sh` from the release on [GitHub Releases](https://github.com/juliankang4/owngit/releases).
2. Compare its SHA-256 with the release's `manifest.json`, or compare the file with `packaging/installer/` at the release tag.
3. Read it, then run `/bin/sh install.sh`, `& .\install.ps1`, or `/bin/sh proxmox.sh` as root on a Proxmox VE host.

### Other ways to install

Every route needs Git with `git-http-backend` on the host. Homebrew and the Arch Linux package install Git for you.

- Homebrew (macOS on Apple silicon, Linux x64 and ARM64): `brew install juliankang4/tap/owngit`
- npm (macOS on Apple silicon, Linux x64 and ARM64, Windows x64; needs Node.js): `npm install -g owngit`
- Arch Linux or Omarchy (x64, ARM64): build the `PKGBUILD` attached to each release.
- Any of these platforms: the archive from [GitHub Releases](https://github.com/juliankang4/owngit/releases), checked against `SHA256SUMS`.

## First-time setup

Start OwnGit in the foreground, or [as a service](#run-as-a-service):

```sh
owngit serve
```

The default address is `http://127.0.0.1:7654`. Setup asks for:

- the repository folder;
- whether general access is open to anyone who reaches OwnGit, or needs one shared password;
- a separate administrator password, which the dashboard asks for before administrator changes ([how often](#administrator-password-check)).

Nothing is saved until you finish. Setup ends at an empty dashboard, where "New repository" gives a clone address such as `http://HOST:7654/git/PROJECT.git`.

When you open setup from a public Internet address, setup preselects the shared-password option and says why.

### In a terminal

`owngit serve` in a terminal asks for the language, then offers "Continue in this terminal" or "Open the web dashboard".

- In the terminal, OwnGit asks the same questions as the web page. Ctrl-C stops OwnGit without saving anything.
- In the browser, choose "Ask the terminal for approval". The browser and the terminal show the same short code. Answer `y` in the terminal only when your browser shows that code. A request from another device, or through a proxy, is marked with a warning.

If Tailscale runs, the "Other devices" step prints a command such as `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --accept-insecure-http`. Run it after setup and restart OwnGit. For an encrypted address, use [Share on your tailnet over HTTPS](#share-on-your-tailnet-over-https) instead.

### Without a terminal

A service never opens a browser. OwnGit writes an owner-only setup file in the state directory and writes its path, never the link, to the log. To get a link, run this on the installation host:

```sh
owngit setup-link
```

The link works once within 15 minutes, and each run replaces the previous link. To set up from another device before setup is finished, name the address you will use: `owngit setup-link --base-url http://192.168.1.20:7654`.

### A computer without a screen

On a computer without a screen (a container, an SSH session without a display, a Mac where you are not logged in on the screen), the first start listens on every address (`0.0.0.0:7654`), so setup can happen from another device. Until setup is finished, other devices see only the setup page.

- The setup link is printed for private addresses only (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, IPv6 unique local).
- A computer with only public addresses prints an SSH tunnel command instead, such as `ssh -L 7654:127.0.0.1:7654 USER@HOST`. Run it on your own computer and open the link there.
- To accept connections only from this computer, run `owngit network set --listen 127.0.0.1:7654` and restart.

## Run as a service

`owngit service install` runs OwnGit in the background and restarts it when it stops. It waits until OwnGit answers, then prints the setup link.

| Command | What it does |
| --- | --- |
| `owngit service install` | Installs and starts the service. Run it again after you replace the program; it restarts the service with the new version and the same state. |
| `owngit service status` | Whether OwnGit runs and answers, who runs it, the log, the state directory and the addresses. |
| `owngit service start`, `stop`, `restart` | Start, stop or restart the service. |
| `owngit service uninstall` | Stops and removes the service. The state directory and the repositories stay. |

The command refuses an `owngit` program that another account could replace, and names the path to fix. Move the program to a folder only you or root can change, such as `~/.local/bin`, or use the [one-line installer](#one-line-installer).

A Homebrew install hands the service to Homebrew (`brew services restart owngit`); its log is `$(brew --prefix)/var/log/owngit.log`.

### Linux

| Where you run the command | Service | State directory | Log |
| --- | --- | --- | --- |
| On a desktop | systemd user service, started at boot (lingering) | your usual state directory | `journalctl --user -u owngit.service -f` |
| Over SSH, or without a graphical session | system service that runs as your account; asks for `sudo` once | your usual state directory | `sudo journalctl -u owngit.service -f` |
| As root | system service that runs as a new `owngit` account | `/var/lib/owngit/state` | `sudo journalctl -u owngit.service -f` |

When `sudo` is not available, the command prints the script for an administrator to run.

For a root install:

- `--state-dir` must be inside `/var/lib/owngit`, and the program must be where only root can change it, such as `/usr/local/bin/owngit`.
- Other commands find the state through `/etc/owngit/state-dir` and run as the `owngit` account. A file you pass, such as `backup --output`, must be an absolute path that account can write.
- The `owngit` account cannot see `/home`, `/root` or `/run/user`, so its repository folder must belong to it. Setup shows the command, such as `sudo install -d -o owngit -g owngit -m 0700 /srv/git`.

### Windows

`owngit service install` registers a Task Scheduler task named `OwnGit`. The log is `logs\service.log` in the state directory.

- From an administrator account, the task starts at boot. One User Account Control approval copies the program to `%ProgramFiles%\OwnGit`, adds a Windows Firewall rule `OwnGit` for private networks, and installs Git with `winget` when Git is missing. OwnGit itself runs without administrator rights.
- From a standard account, the task starts when you sign in, and no firewall rule is added.

On a new Windows installation, a boot task stays "Queued" until someone signs in at the screen once.

To update, install the new release outside `%ProgramFiles%\OwnGit` and run its `owngit service install`. On a computer with a desktop, a second task, `OwnGit icon`, starts the [OwnGit icon](#owngit-icon-and-notifications).

### macOS

`owngit service install` writes the LaunchAgent `~/Library/LaunchAgents/app.owngit.server.plist`. launchd starts OwnGit at login (right after a restart with automatic login) and restarts it when it stops.

- The state is in `~/Library/Application Support/owngit`.
- The log is in `~/Library/Logs/owngit` ([Log files on macOS](#log-files-on-macos)).
- If you turn OwnGit off under System Settings, General, Login Items & Extensions, the agent does not start.
- After `npm update -g owngit`, run `owngit service install` again.

### Log files on macOS

The log folder is `~/Library/Logs/owngit`, or `$(brew --prefix)/var/log` for a Homebrew install.

- `owngit.log` is the server log. It stays below 10 MB; the older part moves to `owngit.log.1`.
- `owngit.stderr.log` holds only crash output and other text the server log could not record.

OwnGit makes `owngit.log`, `owngit.log.1` and its own LaunchAgent's `owngit.stderr.log` readable by your account only, and removes any access list (ACL) they inherit. If it cannot make `owngit.log` private, it does not start and names the fix, such as `chmod -N PATH`.

OwnGit does not change the `owngit.stderr.log` that Homebrew's service writes. If other accounts can read files in `$(brew --prefix)/var/log`, make that file private yourself:

```sh
chmod -N "$(brew --prefix)/var/log/owngit.stderr.log"
chmod 600 "$(brew --prefix)/var/log/owngit.stderr.log"
```

### What the service account can do

The service, and every check that runs from a pushed commit, can do whatever its account can do. If other people can push, install OwnGit as root on Linux so it runs as the separate `owngit` account.

A Linux system service runs with systemd hardening: nothing it starts can gain privileges (a check cannot use `sudo`), `/usr`, `/boot`, `/efi` and `/etc` are read-only, and new files are private to the service account.

### Health check

`GET /healthz` answers `200 OK` while OwnGit serves HTTP. A monitor on another device must use a host name OwnGit accepts ([Host names](#host-names)). On this computer, `owngit health` exits 0 only when this installation's server answers, and otherwise says what it found.

## Checkup

`owngit doctor` checks the OwnGit installation that uses a state directory on this computer and prints each problem with one command that fixes it. `--json` prints the same as JSON. The General tab of Settings shows the same checkup to a confirmed administrator.

It checks:

- whether the server runs and answers, and whether setup is finished;
- whether other accounts can change the repository folder or the folder of any repository;
- each repository's managed `hooks` entry;
- on Windows, folders that the Administrators group owns (fix: `owngit service install`);
- when OwnGit listens for other devices, the firewall of this computer.

OwnGit never runs a fix itself. Anything it could not check, such as ufw or firewalld rules that need root to read, is listed under "Could not check".

## Update and uninstall

`owngit update` finds out how OwnGit was installed and prints the command that updates it. It asks GitHub for the latest release when you run it; `--json` prints the same as JSON. Neither the command nor the dashboard installs anything.

| Installed with | Update | Remove the program |
| --- | --- | --- |
| Homebrew | `brew upgrade owngit` | `brew uninstall owngit` |
| npm | `npm install -g owngit@X.Y.Z` | `npm uninstall -g owngit` |
| Arch Linux package `owngit-bin` | `makepkg -si` with the new release's `PKGBUILD` | `sudo pacman -R owngit-bin` |
| Archive or one-line installer | run the new release's installer for this program | delete the program |

For a container, see [Run in a container](#run-in-a-container).

When a service under your account runs this program, the printed command also runs `owngit service install`, which restarts the service with the new version. Without a service, restart OwnGit yourself.

`owngit uninstall` removes the service and prints how to remove the program. It never deletes the state directory or the repositories, and it prints where they are. A later install uses them again.

### Backup versions

Check this before you restore a backup with an older OwnGit, for example to go back after an upgrade. The restore steps are in [Backups](BACKUPS.md).

- OwnGit restores backup versions 1, 2, 9, 10 and 11, and refuses any other version.
- A new backup is version 10, which OwnGit 1.0.3 to 1.1.2 can restore. It is version 11, which needs 1.1.3 or later, when its OwnGit records exceed 64 MiB or when it holds records that version 10 cannot hold, such as pull requests and reviews created or edited with OwnGit 1.1.3 or later, repository names after a rename, a repository's own ref settings, import refresh choices or check container options.
- OwnGit 1.1.3 refuses a backup that holds a branch or tag name with a Unicode space character, a record of a refused or stopped merge that differs from the pull request's later merge, or check records in a time order it does not accept (this can happen after the computer's clock was set back). Restore such a backup with 1.1.4 or later.

## Run in a container

On Linux with Docker Engine and Docker Compose, save [`compose.yaml`](../packaging/container/compose.yaml) in a new folder. In that folder:

```sh
docker compose up -d
docker compose exec -it owngit owngit setup-link
```

The image is `ghcr.io/juliankang4/owngit` for x64 and ARM64, tagged `X.Y.Z`, `X.Y` and `latest`. It runs as the account `owngit` (ID 10001) and needs no privileges.

Open the link, `http://localhost:7654/setup#...`, within 15 minutes. With open access (the default), finish setup through the computer's name or address, for example `http://nas.local:7654/setup#...`. After setup, a container with open access refuses `localhost` from outside the container, because it cannot tell this computer from other devices. To allow another name later:

```sh
docker compose exec owngit owngit network set --allowed-host nas.local
docker compose restart
```

To keep OwnGit on this computer only, publish the port as `"127.0.0.1:7654:7654"`; `localhost` then works only with the shared password.

Everything lives in the volume `owngit-data` at `/data`: the state, the repositories and the backups made before an upgrade.

- `docker compose down` keeps the volume. **`docker compose down -v` deletes it, with every repository.**
- Keep `/data` on a local disk. To keep repositories on a network share, mount it at another path, such as `/repositories`, and choose that folder in setup.
- To run as another account, set `user:` in `compose.yaml` and mount a folder that account owns and no other account can change.

To update, run `docker compose pull && docker compose up -d` in the folder of `compose.yaml`. OwnGit backs up its state before it upgrades it. To make your own backup first:

```sh
docker compose stop
docker compose run --rm owngit owngit backup --output /data/backup-before-update
docker compose pull
docker compose up -d
docker compose exec owngit owngit backup verify /data/backup-before-update
```

A backup inside the volume is deleted with the volume. [Backups](BACKUPS.md) explains how to keep one elsewhere.

## Run on Proxmox VE

On a Proxmox VE host, this command creates an unprivileged Debian 13 container (LXC) and installs OwnGit in it as a service. Run it as root in the host's shell:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh
```

The container is named `owngit`, gets the next free ID, 2 cores, 1024 MB of memory, an 8 GB disk on `local-lvm` (or `local-zfs`) and DHCP on `vmbr0`, and starts with the host. The script prints the container's address and the setup link. If a step fails, it removes the container it created. It never changes an existing container.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh -s -- --repositories /tank/owngit
```

| Option | What it does |
| --- | --- |
| `--id N`, `--hostname NAME` | Container ID and name. |
| `--storage NAME`, `--disk GB`, `--cores N`, `--memory MB` | Disk storage and size, CPU cores, memory. |
| `--bridge NAME`, `--ip ADDRESS/PREFIX`, `--gateway ADDRESS` | Network bridge, and a fixed IPv4 address with its gateway. |
| `--repositories FOLDER` | Keeps the repositories in a folder on the host. |
| `--template VOLUME` | Uses a Debian 13 template you already have. |
| `--version X.Y.Z` | Installs that release (1.1.3 or later). |

With `--repositories`, the folder must be new or empty, must not be reached through a link, and it and every folder above it must belong to root with no write access for others (`chown root:root FOLDER && chmod go-w FOLDER`). Proxmox backups (`vzdump`) do not include it, so back it up with [OwnGit backups](BACKUPS.md) or the host's own backups.

Run commands in the container with the full program path, using the ID the script printed:

```sh
pct exec 105 -- /usr/local/bin/owngit service status
```

To update, run `pct exec 105 -- /usr/local/bin/owngit update` and run the command it prints in the container's shell (`pct enter 105`). `pct destroy 105` deletes the container with its state and any repositories inside it; back up first.

## Settings

Settings, in the dashboard sidebar, has five tabs:

| Tab | What it holds |
| --- | --- |
| General (`/settings`) | This browser's language, appearance and list order; the [new-release check](#new-release-notice); the [checkup](#checkup); the [OwnGit icon](#owngit-icon-and-notifications). |
| Access (`/settings/access`) | Open access or the shared password, [sign-in length](#how-long-a-sign-in-lasts), [links from other sites](#links-from-other-sites), [login attempt limits](#login-attempt-limits), the administrator password and [how often it is asked](#administrator-password-check). |
| Network (`/settings/network`) | [Network settings](#network-settings) and [tailnet sharing](#share-on-your-tailnet-over-https). |
| Repositories (`/settings/repositories`) | Default branch, kept history and deletion confirmation ([Repositories](REPOSITORIES.md)); [Git transfer limits](#git-transfer-limits), [browsing limits](#browsing-limits) and [check ceilings](#check-ceilings). |
| Storage & recovery (`/settings/storage`) | Repository folder, backups and maintenance ([Backups](BACKUPS.md)); raw check logs ([Automatic checks](AUTOMATIC_CHECKS.md)). |

Each part has its own Save. Save asks for the administrator password unless this browser is already confirmed. Network settings apply at the next start; everything else applies at once. Turning on or changing the shared password signs out everyone signed in with it.

### Settings on the command line

`owngit settings` changes the General, Access, Repositories and Storage & recovery settings through the administrator API. Each command needs `--server` and a `--password-file` with the administrator password, and prints JSON.

```sh
owngit settings show --server http://127.0.0.1:7654 --accept-insecure-http --password-file /path/to/admin-password
owngit settings set --server http://127.0.0.1:7654 --accept-insecure-http --password-file /path/to/admin-password --update-check off
```

- `settings set` changes only the settings its options name. `owngit settings set --help` lists them.
- `settings access --mode password` or `--mode open` changes general access. `settings admin-password` replaces the administrator password. `settings confirmation --choice CHOICE` sets [how often the dashboard asks](#administrator-password-check).
- A new password is read from an owner-only file (`--access-password-file`, `--new-password-file`) or asked for twice at a hidden prompt. It never goes on the command line.
- `--accept-insecure-http` accepts plain HTTP for that one command. Leave it out for an `https://` address.

The network settings, the tailnet sharing and the icon have their own commands, run on the installation host: `owngit network`, `owngit tailscale` and `owngit tray`.

Settings that `settings set` and `settings confirmation` change belong to this host and are not in backups; a restored installation starts with the defaults. The access mode and both passwords are in backups.

### How long a sign-in lasts

A shared-password sign-in lasts 12 hours by default. Choose 1 hour, 8 hours, 12 hours, 1 day, 7 days or 30 days on the Access tab, or `owngit settings set --session 7d`. A new time applies to later sign-ins. To end every sign-in now, change the shared password.

### Links from other sites

By default, a link to OwnGit opened from a chat, webmail or another site opens without the shared sign-in until you open it again from OwnGit. To keep the sign-in on such links, choose "Keep the sign-in" on the Access tab, or run `owngit settings set --cross-site-links lax` (`strict` is the default). Administrator confirmation is never kept on such a link.

### Login attempt limits

By default, 4 wrong passwords from one address within 10 minutes pause that address for 15 minutes, even for the right password. The pause ends by itself. Shared and administrator passwords are counted separately, in the dashboard, Git and the API.

```sh
owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m
```

Attempts go from 1 to 100; the window and the pause from 1 minute to 24 hours. The limits cannot be turned off. During a pause, Git and the API get HTTP 429 with `Retry-After`.

Behind a proxy that is not trusted, everyone arrives from the proxy's address and is paused together. [Trust the proxy](#behind-a-reverse-proxy) first.

### Administrator password check

"Ask for the administrator password" on the Access tab sets when the dashboard asks for it before administrator changes:

- Every time.
- Again after 30 minutes (the default), 1 hour, 8 hours, 1 day, 7 days or 30 days, counted from when you typed it, for this browser only. The sidebar shows when it expires, with End to stop it now.
- Do not ask: anyone who can open the dashboard can change settings, delete repositories and issue credentials. Every page then shows "Administrator password check off".

On the command line: `owngit settings confirmation --choice every`, `30m`, `1h`, `8h`, `1d`, `7d`, `30d` or `never` (which also needs `--acknowledge-no-ask`). The command line and the API always ask for the password.

To end the confirmation of a browser you can no longer access, change the administrator password, or run `owngit reset-admin` on the installation host.

## OwnGit icon and notifications

On a computer with a desktop, the OwnGit icon shows in the macOS menu bar, the Windows notification area or the Linux desktop's panel. Its panel shows whether OwnGit runs, the clone address, the three latest pushes, and the command to run when OwnGit needs attention. Hiding or quitting the icon never stops OwnGit.

- `owngit tray off` hides the icon until `owngit tray on`. `owngit tray status` says whether it is shown and, if not, why. The General tab of Settings has the same switch.
- macOS: the icon is OwnGit.app, installed beside the program. `owngit service install` opens it and registers it to open at sign-in.
- Windows: the `OwnGit icon` task starts it at sign-in. Click opens the dashboard; right-click opens the panel.
- Linux: run `owngit service install` from a terminal on the desktop. It writes `~/.config/autostart/owngit-icon.desktop`. The desktop must show StatusNotifierItem icons (tested on Omarchy, and GNOME 48 with the AppIndicator extension) and needs `gjs` with GTK 4.

Root installs on Linux and computers without a screen have no icon.

The icon shows desktop notifications for pushes, opened pull requests, failed checks, imports and backups that did not finish successfully, and new OwnGit versions. Each kind can be turned off in the panel or with `owngit tray notifications`:

```sh
owngit tray notifications push off
owngit tray notifications only_others on
```

`only_others` hides pushes, pull requests and imports that came from this computer. Other bars and scripts can read the panel with `owngit tray read --json` and open the dashboard with `owngit tray open --json`.

Notifications are a convenience. The dashboard keeps every push, check and backup result, whether or not a notification appeared. Clicking a notification opens the page it names.

- Windows: notifications that arrive together appear one after another, about 10 seconds apart (5 seconds while the panel is open). When more than three arrive at once, one notification says how many there are, for example "4 new OwnGit notifications", and clicking it opens the dashboard. A click after a notification has left the screen opens nothing.
- Linux: when the desktop has no notification service, the Notifications section of the panel and the icon menu say so. OwnGit keeps trying, waiting longer each time up to five minutes, and writes the reason to its log once until notifications work again. When a service starts, the waiting notifications appear, each one once.

## Reaching the server from another device

OwnGit serves plain HTTP. Choose one way to reach it from other devices:

- **Encrypted**: [share it on your tailnet over HTTPS](#share-on-your-tailnet-over-https), or put it [behind a reverse proxy](#behind-a-reverse-proxy).
- **Plain HTTP** over Tailscale, your own VPN or the LAN: let OwnGit [listen on a network address](#network-settings). You accept plain HTTP once, and the page header always shows whether the connection is encrypted.

**Do not expose OwnGit to the public Internet.** Only share links may have a public address of their own ([Repositories](REPOSITORIES.md)).

### The firewall of this computer

`owngit doctor` tells you what to allow on this computer.

- Windows: an administrator's `owngit service install` adds the rule `OwnGit` for private networks. Mark your home network as Private in Windows Settings.
- macOS: when the application firewall is on, allow OwnGit when macOS asks.
- Linux: with ufw or firewalld on, allow the port from your private network only, for example `sudo ufw allow from 192.168.1.0/24 to any port 7654 proto tcp`.

### Host names

OwnGit answers only requests whose Host is an approved name, or `localhost`, `127.0.0.1` or `::1` from this computer. Other names get "unrecognized host". To approve a name, run this on the installation host and restart OwnGit:

```sh
owngit approve-host gitbox.internal
```

### Network settings

Save the address on the installation host; it applies at the next start:

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal --accept-insecure-http
owngit network show
```

| Option | What it sets |
| --- | --- |
| `--listen HOST:PORT` | Where OwnGit listens. `0.0.0.0` or `::` is every interface. The default is `127.0.0.1:7654`. |
| `--accept-insecure-http` | Accepts plain HTTP for an address other devices reach. Needed once; without it, `set` refuses such an address. |
| `--base-url URL` | The address other devices use, shown in clone addresses. |
| `--allowed-host`, `--remove-allowed-host` | Approved Host names. |
| `--trusted-proxy`, `--remove-trusted-proxy` | Reverse proxies whose forwarded headers OwnGit believes. |

An empty value, such as `--base-url=`, removes a saved value. An option given to `owngit serve` overrides the saved value for that run, so keep `--listen` and `--base-url` out of service definitions. `owngit network show` and the Network tab show the saved values, the running values, and whether a restart is needed.

If a saved address locks you out, reset it on the installation host and restart:

```sh
owngit network reset
```

`reset` removes the listen address, the base URL and the public address for share links. Add `--clear-allowed-hosts` or `--clear-trusted-proxies` to remove those too. Network settings are not in backups.

### Share on your tailnet over HTTPS

When Tailscale runs on the computer that runs OwnGit, OwnGit can have Tailscale answer HTTPS for this computer's Tailscale name. Devices on your tailnet then open `https://NAME.TAILNET.ts.net/` and clone from `https://NAME.TAILNET.ts.net/git/project.git`. Devices outside the tailnet cannot reach it.

You need:

- Tailscale 1.50 or later, signed in on this computer;
- MagicDNS and HTTPS Certificates on, in the DNS page of the Tailscale admin console;
- on Linux, `sudo tailscale set --operator=$USER` once.

Turn on "Share OwnGit on my tailnet" on the Network tab, or on the installation host:

```sh
owngit tailscale on
owngit tailscale status
owngit tailscale off
```

From Settings it applies at once; from the command line, restart OwnGit.

- Tailscale serves HTTPS on 443, or 8443 or 10000 when 443 is taken. To choose a port, select "Custom" under "HTTPS port", or `owngit tailscale on --https-port 8443`. Clones that used the old address need `git remote set-url origin https://NAME.TAILNET.ts.net:8443/git/project.git`.
- OwnGit never changes what another service has on a port unless you review and replace it; `owngit tailscale status` shows what is there and the command that replaces it.
- OwnGit stays off the home network unless you tick "Also allow on the home network (not encrypted)" or use `--home-network`.
- The first HTTPS connection can take about a minute while Tailscale gets the certificate.
- The certificate puts this computer's and your tailnet's names, such as `gitbox.tail0000.ts.net`, in a public Certificate Transparency log. Only the name is published, not your content.
- OwnGit never turns on Tailscale Funnel and refuses every request that comes through it.
- After renaming the computer in Tailscale, turn sharing on again.
- With the Tailscale app for macOS, Tailscale runs only while someone is logged in. Use automatic login, or Homebrew's `tailscaled`.

### Other private networks

Over NetBird, Headscale or WireGuard, let OwnGit listen on this computer's address in that network and restart:

```sh
owngit network set --listen 100.64.0.7:7654 --base-url http://gitbox.netbird.selfhosted:7654 --accept-insecure-http
```

These networks encrypt traffic, but OwnGit cannot see that, so it still reports plain HTTP. For an HTTPS address, run a reverse proxy on this computer as below. Tailnet sharing works only with Tailscale.

### Behind a reverse proxy

A reverse proxy can give OwnGit an HTTPS address at the root of its own host name, such as `https://git.example.internal`. A path below another site is not supported. Save the address and the proxy, then restart OwnGit:

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
```

`--trusted-proxy` is the address the proxy connects from: `127.0.0.1` on this computer, the Docker network range (such as `172.18.0.0/16`) for a proxy in Docker here, or the other computer's address. Until the proxy is trusted, wrong passwords from one device pause every device, and HTTPS forms fail.

- Keep the trusted range small. OwnGit refuses ranges wider than `/8` (IPv4) or `/32` (IPv6). Trusting `127.0.0.1` also trusts every forwarding program on this computer, such as an `ssh -L` tunnel.
- Keep OwnGit on `127.0.0.1:7654` when the proxy runs on this computer, so other devices cannot bypass it.
- The proxy must pass the original Host, set `X-Forwarded-Proto`, and add the client's address to `X-Forwarded-For`. It must not pass a client's own `X-Forwarded-For` through.
- The proxy's body size and timeouts must be at least OwnGit's [Git transfer limits](#git-transfer-limits) (4 GB and 30 minutes by default).

Caddy does all of this with no extra settings. `tls internal` signs the certificate with Caddy's local authority, which each device must trust:

```caddyfile
git.example.internal {
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

nginx:

```nginx
server {
    listen 443 ssl;
    server_name git.example.internal;
    ssl_certificate     /etc/ssl/git.example.internal.crt;
    ssl_certificate_key /etc/ssl/git.example.internal.key;

    client_max_body_size 4g;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_read_timeout 30m;
    proxy_send_timeout 30m;

    location / {
        proxy_pass http://127.0.0.1:7654;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Host "";
    }
}
```

For other proxies:

- Traefik stops reading a request after 60 seconds by default, which cuts a long push. Set `readTimeout: 30m` under the entry point's `transport.respondingTimeouts`.
- Nginx Proxy Manager: leave "Trust Upstream Forwarded Proto Headers" off, and add `client_max_body_size 4g;`, `proxy_request_buffering off;`, `proxy_read_timeout 30m;`, `proxy_send_timeout 30m;` and `set_real_ip_from 127.0.0.1;` under Custom Nginx Configuration. Do not add `proxy_http_version`.

## New-release notice

After setup, OwnGit asks GitHub once a day whether a newer release exists, and the dashboard shows a notice. A confirmed administrator also sees the command that updates this installation. OwnGit never downloads or installs anything itself.

The check is one HTTPS request to `https://api.github.com/repos/juliankang4/owngit/releases/latest`. It sends no repository data; GitHub sees the server's address. Turn it off on the General tab, with `owngit settings set --update-check off`, or for good with:

```sh
owngit serve --no-update-check
```

## Host-owner recovery

OwnGit has no email or account recovery. Both procedures run on the installation host.

Before setup is finished, issue a new setup link:

```sh
owngit setup-link --no-open
```

To reset a forgotten administrator password, put the new password in an owner-only file (see [Password and token files](#password-and-token-files)) and run:

```sh
owngit reset-admin --password-file /path/to/owner-only-password-file
```

This ends every browser's administrator confirmation and leaves the repositories unchanged.

### Password and token files

Every command that reads a password or token file (`reset-admin`, `settings`, `import`, `pr`, `repo` and others) requires a regular file that only your account can read. OwnGit never accepts a password as a command-line value. The file holds the password on one line. When OwnGit refuses a file, it says which accounts can also read it and gives the command that fixes it.

On macOS and Linux, create the file with `umask 077`, or fix it with `chmod 600 FILE`. On macOS, also remove any access list with `chmod -N FILE`.

On Windows, a file made with Notepad or `echo` inherits its folder's permissions. In PowerShell, create the file, limit it to your account, and only then write the password:

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

This works in Windows PowerShell 5.1 and PowerShell 7, and keeps the password off the screen and out of the history.

## Git transfer limits

Each Git request (clone, fetch, push, archive) has these limits. Change them under Git transfers on the Repositories tab, or with `owngit settings set`.

| Limit | Default | Range | Option |
|---|---|---|---|
| Largest transfer, each way | 4 GB | 1 MB to 64 GB | `--transfer-size` |
| Longest transfer | 30 minutes | 1 minute to 24 hours | `--transfer-time` |
| Stop when the client sends nothing for | 1 minute | 10 seconds to 1 hour | `--transfer-idle` |
| Transfers at once per repository | 4 | 1 to 32 | `--transfer-per-repository` |
| Extra slots for repositories with no transfer running | 1 | 0 to 32 | `--transfer-extra-slots` |
| Wait for a free slot | 90 seconds | 5 seconds to 10 minutes | `--transfer-queue` |

What a client sees:

- A push over the size limit gets HTTP 413, which Git may show only as `fatal: the remote end hung up unexpectedly`. A clone or fetch over a limit is cut off.
- A request that finds no free slot within the wait gets HTTP 503, `Git service is busy with other transfers; try again shortly`. Run the command again.
- OwnGit does not host Git LFS. A repository whose history is larger than the size limit cannot be cloned; keep large binary files out of Git history.

### Memory on a small Linux host

On Linux, OwnGit reads how much memory its computer or container allows and fits its work into it. On macOS and Windows it cannot read this, and the saved limits apply as they are.

- OwnGit keeps its own memory near half of the limit. Setting `GOMEMLIMIT` replaces that figure for OwnGit, not for Git.
- With a 512 MiB memory limit, at most about 3 Git transfers run at once, and 6 with 1 GiB, even when the saved limits allow more. Only 1 clone, fetch or archive download builds a pack at a time with 512 MiB, and 2 with 1 GiB; pushes are not held back by this. Requests over these numbers wait up to the transfer queue time for a slot, then get the 503 answer above. Settings still shows the saved values.
- Files above about 16 MiB (with a 512 MiB limit) or 32 MiB (with 1 GiB) are stored without new delta compression, so each new version of such a file takes its full compressed size in storage, backups and clones.
- The dashboard does not show a file above that size. The page says "This file is too large to show here. Clone the repository to get this file.", and the raw download is refused with the same message.

When OwnGit finds a memory limit, its startup log says how much.

## Browsing limits

Browsing limits cap how much one page reads to show a file, a diff or a pull request comparison. Change them under Browsing limits on the Repositories tab, or with `owngit settings set`.

| Limit | Default | Range | Option |
|---|---|---|---|
| Raw file download | 10 MB | 1 MB to 256 MB | `--browse-raw` |
| File view | 2 MB | 64 KB to 64 MB | `--browse-file` |
| Diff of a commit page | 2 MB | 64 KB to 64 MB | `--browse-commit-diff` |
| Diff of one file on its own page | 8 MB | 64 KB to 64 MB | `--browse-file-diff` |
| One file within a commit page | 256 KB | 16 KB to 16 MB | `--browse-commit-file` |
| Pull request comparison | 8 MB | 64 KB to 64 MB | `--browse-compare` |
| Comparison time | 20 seconds | 5 seconds to 1 minute | `--browse-compare-time` |

- A raw file above its limit cannot be downloaded from the browser; clone the repository to get it.
- The file view shows the part that fits. A file whose diff is too large for a commit page links to its own diff page.
- A page shows at most 10,000 lines of a file or diff and 1,000 entries of a folder, with First page and Next page links.
- `owngit pr diff`, the API and the MCP tool keep their own fixed limits, equal to the defaults.

## Check ceilings

Check ceilings are this computer's upper bounds for what a repository's [check policy](AUTOMATIC_CHECKS.md) may ask for. Change them under Check ceilings on the Repositories tab, or with `owngit settings set`.

| Ceiling | Default | Range | Option |
|---|---|---|---|
| Time for one check | 24 hours | 1 second to 168 hours | `--check-time` |
| Output of one check | 64 MB | 1 KB to 1024 MB | `--check-output` |
| Checks waiting per repository | 1000 | 1 to 10000 | `--check-queue` |
| Checks running at once per repository | 100 | 1 to 1000 | `--check-active` |
| Container CPUs | 64 | 0.1 to 1024 | `--check-cpus` |
| Container memory | 64 GB | 64 MB to 1024 GB | `--check-memory` |
| Container processes | 4096 | 16 to 65536 | `--check-processes` |
| Container scratch space | 16 GB | 1 MB to 1024 GB | `--check-scratch` |
| Source copied for a check | 4 GB | up to 1024 GB | `--check-source` |

- A policy above a ceiling cannot be saved. After you lower a ceiling, a saved policy above it queues no new check until you raise the ceiling or lower the policy; "Check ceilings" lists those repositories. Checks already queued keep their limits.
- A check that prints more than its output limit is stopped and ends as `incomplete`.
- Ceilings belong to this computer and are not in backups.

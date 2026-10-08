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

The program goes to `~/.local/bin/owngit` (`/usr/local/bin/owngit` for root) or, on Windows, to a folder per release under `%LOCALAPPDATA%\Programs\OwnGit`. The installer does not change PATH; when the folder is not on PATH, it says how to run the program. On Windows, it also registers OwnGit for [Windows notifications](#windows-notifications), with or without a service.

On macOS, the installer puts the menu bar app, OwnGit.app, in `/Applications` when your account can write that folder. On macOS 27, a menu bar manager such as Hidden Bar can hide the icon of an app outside `/Applications`. When your account cannot write `/Applications`, the app goes beside the program and the installer warns that the icon may be hidden. An older release chosen with `--version` keeps its app beside the program, because that app needs the program next to it. With `--no-service`, the installer still places the app but opens and registers nothing.

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

On macOS, it also stops before it changes anything when another account can change `/Applications`, or when `/Applications/OwnGit.app` is a symbolic link.

### Check the script before it runs

The one-line command runs the script before anything checks it. `SHA256SUMS` covers only the archives, and it comes from the same release, so it is not an independent signature. To check the script yourself:

1. Download `install.sh`, `install.ps1` or `proxmox.sh` from the release on [GitHub Releases](https://github.com/juliankang4/owngit/releases).
2. Compare its SHA-256 with the release's `manifest.json`, or compare the file with `packaging/installer/` at the release tag.
3. Read it, then run `/bin/sh install.sh`, `& .\install.ps1`, or `/bin/sh proxmox.sh` as root on a Proxmox VE host.

### Other ways to install

Every route needs Git with `git-http-backend` on the host. Homebrew and the Arch Linux package install Git for you.

- Homebrew (macOS on Apple silicon, Linux x64 and ARM64): `brew install juliankang4/tap/owngit`
- Homebrew menu bar app (optional; macOS 13 or later on Apple silicon): `brew install --cask juliankang4/tap/owngit`, then `owngit service install`. The cask puts OwnGit.app in `/Applications` and installs the formula above if it is missing; it is not a second copy of the program or the service. With the formula alone, the app stays in Homebrew's own folder and works as before.
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

With open access, anyone who reaches OwnGit, including every device and program on the network, can read, push, delete branches and tags, create repositories, open and merge pull requests and restore files. Settings, repository deletion, rename, imports and check policies need the administrator password, unless you chose to be [asked less often or not at all](#administrator-password-check). Choose the shared password when the network has devices or people you do not fully trust.

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

On macOS and Windows, and with Homebrew, the service log is a file. When it nears 10 MB, its older part moves to a file with `.1` added to the name. If that move fails, OwnGit keeps writing to the current file, reports the failure once, and tries again every minute. When the file reaches twice the limit, later lines go to the service's standard error (`owngit.stderr.log` on macOS). A Windows service discards its standard error, so there those lines are lost, and the failure notice says so.

### How long a stop takes

When OwnGit is asked to stop, it gives its running work up to 45 seconds to finish. An idle server stops right away. Git transfers in progress can continue for up to about 40 of those seconds, and imports, checks, backups and maintenance must end within the same 45 seconds.

If work is still running at that deadline, OwnGit logs what it is, ends the Git, check and helper processes it started, and exits. The next start records what was interrupted.

Each service manager waits 70 seconds before it forces OwnGit to stop:

- the systemd unit (`TimeoutStopSec`);
- the macOS LaunchAgent (`ExitTimeOut`), although launchd waits at most 60 seconds, which still leaves room after the 45 seconds;
- the Windows task, when an `owngit service` command stops or restarts it;
- the Homebrew service (`stop_timeout` in the formula);
- the Compose file (`stop_grace_period`). For Docker without Compose, see [Run in a container](#run-in-a-container).

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
- The service also opens the menu bar icon; [OwnGit icon and notifications](#owngit-icon-and-notifications) says which copy of OwnGit.app it uses.

### Log files on macOS

The log folder is `~/Library/Logs/owngit`, or `$(brew --prefix)/var/log` for a Homebrew install.

- `owngit.log` is the server log. When it nears 10 MB, the older part moves to `owngit.log.1` ([if that fails](#run-as-a-service)).
- `owngit.stderr.log` holds only crash output and other text the server log could not record.
- `.owngit-staging` is a hidden, empty folder where OwnGit makes new log files. Leave it in place.

Only your account can read OwnGit's log files, from the moment each file appears. OwnGit makes each new log file in `.owngit-staging`, which only your account can open, removes any access list (ACL) from it there, and then moves it to its name. If OwnGit cannot make `owngit.log` private, it does not start and names the fix, such as `chmod -N PATH`.

A log file that other accounts can read, or that an older OwnGit wrote, is replaced once by a private copy with the same content. OwnGit does this for `owngit.log` and `owngit.log.1` when it starts, and for its LaunchAgent's `owngit.stderr.log` when you run `owngit service install`. A program that opened the old file sees no new lines after that.

OwnGit removes an inherited access list only from a log folder it owns: `~/Library/Logs/owngit`, or a folder it creates for the log. It leaves a folder shared with other programs, such as `$(brew --prefix)/var/log`, as it is. If that folder's access list lets other accounts read new files, the first lines of the log say so. OwnGit's own log files stay private, but other files in the folder stay readable. To remove the folder's access list, run:

```sh
chmod -N "$(brew --prefix)/var/log"
```

If a second OwnGit writes its log in the same folder while one is running, the second one does not replace the files there, and the first lines of its log say why.

OwnGit does not change the `owngit.stderr.log` that Homebrew's service writes. If other accounts can read files in `$(brew --prefix)/var/log`, make that file private yourself:

```sh
chmod -N "$(brew --prefix)/var/log/owngit.stderr.log"
chmod 600 "$(brew --prefix)/var/log/owngit.stderr.log"
```

### What the service account can do

The service, and every check that runs from a pushed commit, can do whatever its account can do. If other people can push, install OwnGit as root on Linux so it runs as the separate `owngit` account.

A Linux system service runs with systemd hardening: nothing it starts can gain privileges (a check cannot use `sudo`), `/usr`, `/boot`, `/efi` and `/etc` are read-only, and new files are private to the service account.

### Health check

`GET /healthz` answers `200 OK` while OwnGit serves HTTP. A monitor on another device must use a host name OwnGit accepts ([Host names](#host-names)).

On this computer, `owngit health` exits 0 and prints `OwnGit answers at http://ADDRESS` only when the server of this state directory answers and proves who it is. Each time the server starts, it writes a new key to `health-run.json` in the state directory, readable only by its account, and removes the file when it stops. `owngit health` sends a fresh challenge and accepts only an answer made with that key, so another program that listens on the same address cannot pass. `owngit service install`, `start` and `restart` use the same check before they report that the service runs.

`owngit doctor` and `owngit service status` require the same proof before reporting that OwnGit runs and answers. The icon uses proven status answers and the same checkup when no status connection is available. If the proof is missing or invalid, these checks report why they cannot confirm OwnGit. A server started before version 1.1.5 needs one restart to publish its key. Service status still shows what the service manager reports, separately from the health result.

When the running server has published its health file and proves itself, `owngit health` only reads that file, so it also works when the state directory is read-only. Run it as the account that runs OwnGit. The file is not part of backups. When `owngit health` cannot confirm the server, it reads the state to explain why. It works on a private temporary copy of the state database and changes nothing in the state directory, including permissions. So it needs a readable state directory and a writable temporary folder, but not a writable state directory. If the next server start would make part of the state private, `owngit health` lists it on standard error ([State directory permissions](#state-directory-permissions)). If the state was made by an older version of OwnGit, `owngit health` stops and tells you to start or restart OwnGit once, which backs up and upgrades the state, and then to run the command again.

`owngit doctor`, and `owngit health` when it cannot confirm the server, make their temporary copy outside the state directory. They take the temporary folder from `TMPDIR` on Linux and macOS, or from `TEMP` and `TMP` on Windows, and follow links to find where it really is. If that folder is the state directory or a folder inside it, they use the system temporary folder instead: `/tmp`, or the `Temp` folder in the account's local application data on Windows. Other accounts must not be able to change the temporary folder they use, so that no one can replace the copy. On Linux and macOS, a shared folder with the sticky bit, such as `/tmp`, is accepted; the folder must also be on a local disk and belong to your account or root. On Windows, no other account may change the folder or delete entries in it. If no temporary folder outside the state passes these checks, they stop before making the copy and change nothing in the state. The message tells you to set `TMPDIR`, or `TEMP` and `TMP` on Windows, to a writable folder outside the state directory. Starting the server is not affected.

When `owngit health` cannot confirm the server, it exits non-zero and says why:

| Message | Meaning |
| --- | --- |
| `it published no health key; restart OwnGit` | The running server started before version 1.1.5, or it could not write its key. Restart OwnGit once. |
| `another program answers at http://ADDRESS, or OwnGit restarted` | The answer did not carry a valid proof. Another program may hold the address, or OwnGit restarted during the check. Run it again; if it repeats, find the program that listens on the address. |
| `OwnGit is still starting` | Try again shortly. |
| `OwnGit is not running` | No server runs for this state directory. |
| `another program holds the state directory` | OwnGit cannot tell which program runs with this state directory. |

### State directory permissions

OwnGit keeps its state directory and its own files there private to the account that runs it. This works the same on Linux, macOS and Windows, and you do not need to run anything.

- When the server starts, it makes the state directory and every file it manages there private. It removes the access that other accounts have, including macOS access lists (ACL) and extra Windows permissions.
- `owngit health` and `owngit doctor` only read the state and change no permissions. If other accounts can change the state directory or a file that OwnGit manages there, they refuse it and give the command that fixes it. For anything else that the next server start would make private, they list the path and its current permissions with the command that starts OwnGit: `owngit serve --state-dir 'STATE_DIRECTORY'`. Starting OwnGit as a service applies the same protection.
- `owngit backup --output` refuses a state directory that other accounts can change. Other commands that open the state refuse it on Linux and macOS, and on Windows they make it private instead. When these commands open the state, they make the state directory and the state database private in the same way as the server, for example by removing read access that other accounts still have.
- The menu bar or panel icon only reads the state directory and changes nothing. If other accounts can change it, the icon shows the problem; starting the server fixes it.

Each change is recorded with the path inside the state directory and the permissions before and after: in the server log for the server, and on standard error for a command. What `owngit health` and `owngit doctor` list is a planned change that has not been made.

The protection holds from that change on. It does not close files that another program opened earlier, and it does not undo changes made before.

When OwnGit cannot make the state private, the server or command stops and the message says what to do:

- If other accounts can change the state directory, the message gives the command that fixes it, usually `chmod g-w,o-w 'STATE_DIRECTORY'` on Linux and macOS. Starting the server also fixes it.
- If a file or folder belongs to another account, the message gives a command that changes its owner, such as `sudo chown ...` on Linux and macOS or a PowerShell command on Windows. Changing the owner needs administrator rights. OwnGit never takes over a file that belongs to another account.
- If a file that OwnGit manages is a link, has a second name (a hard link), or a database file is not a plain file, the message names the file. Replace it with a plain copy of its contents.

Then start OwnGit again.

On Windows, while OwnGit has the state open (the server, or a command while it runs), the state directory and its parent folders cannot be renamed or moved. Files and folders inside them can still be renamed or deleted. Stop OwnGit before you move the state directory.

Administrator accounts such as root, SYSTEM and Administrators are not counted as other accounts. OwnGit still refuses a state directory that belongs to another account or sits in a folder that another account could replace. On Linux, the account's private group is trusted, and on macOS, groups that already have administrator privileges are trusted. The upgrade backup folder beside the state directory is not changed; OwnGit refuses it when other accounts can change it.

## Checkup

`owngit doctor` checks the OwnGit installation that uses a state directory on this computer and prints each problem with one command that fixes it. `--json` prints the same as JSON. The General tab of Settings shows the same checkup to a confirmed administrator.

It checks:

- whether the server runs and answers, and whether setup is finished;
- whether other accounts can change the repository folder or the folder of any repository;
- each repository's managed `hooks` entry;
- on Windows, folders that the Administrators group owns (fix: `owngit service install`);
- when OwnGit listens for other devices, the firewall of this computer.

OwnGit never runs a listed fix itself, and the checkup changes no files or permissions in the state directory. It needs a writable temporary folder outside the state directory ([Health check](#health-check)). If the state was made by an older version of OwnGit, `owngit doctor` stops and tells you to start or restart OwnGit once, which backs up and upgrades the state, and then to run the command again. When the next server start would make part of the state private, `owngit doctor` lists each path with its current permissions and the command that starts OwnGit ([State directory permissions](#state-directory-permissions)). With `--json`, these appear in a `state_protection` list whose entries have `path`, `before` and `repair`; the list is left out when there is nothing to change. Anything it could not check, such as ufw or firewalld rules that need root to read, is listed under "Could not check".

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

With Homebrew, `owngit service install` after `brew upgrade owngit` hands the restart to `brew services restart owngit` without opening the state first, so the new version backs up the state and then upgrades it.

If you also installed the optional Homebrew cask, update the program and the app together, then restart the service and the icon:

```sh
brew upgrade --formula owngit && brew upgrade --cask owngit
owngit service install
```

Until the two have the same version, OwnGit uses the app in Homebrew's own folder instead of the one in `/Applications`. `brew uninstall --cask juliankang4/tap/owngit` removes only the app in `/Applications`; the formula, the state and the repositories stay.

`owngit uninstall` removes the service, quits the menu bar icon and prints how to remove the program. On Windows, it also removes OwnGit's registration for [Windows notifications](#windows-notifications). When OwnGit used the app in `/Applications`, it also prints how to remove that app: `brew uninstall --cask juliankang4/tap/owngit` for a Homebrew install, or moving the app to the Trash otherwise. An app in `/Applications` of another version is not named, because it may belong to another install. `owngit uninstall` never deletes the state directory or the repositories, and it prints where they are. A later install uses them again.

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

`compose.yaml` gives OwnGit 70 seconds to stop ([How long a stop takes](#how-long-a-stop-takes)). Docker's default is 10 seconds, which can end OwnGit in the middle of a stop while a Git transfer runs. If you start the image without Compose, use `docker run --stop-timeout 70`, or stop it with `docker stop -t 70`.

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

A browser can be signed in at the HTTPS address and at a plain HTTP address of the same host at the same time. Each address keeps its own sign-in:

- Signing out at the HTTPS address ends both sign-ins of that browser.
- Signing out at a plain HTTP address ends that address's sign-in only. When the base URL is the HTTPS address, the sign-in page then says that the HTTPS address keeps its own sign-in. Sign out there too to end it.
- Administrator confirmation follows the same rule. Ending it at the HTTPS address ends both confirmations of that browser. Ending it at a plain HTTP address leaves the HTTPS one. When the base URL is the HTTPS address, the dashboard says so, or the sign-in page does when that browser is not signed in at the plain address with the shared password.

Upgrading to OwnGit 1.1.5 ends every sign-in made at the HTTPS address once, because the browser cookies that hold sign-ins were renamed. Sign in again after the upgrade. A sign-in at a plain HTTP address lasts until that browser first opens a page at the HTTPS address. OwnGit then ends it once, on the server as well as in the browser.

### Links from other sites

By default, a link to OwnGit opened from a chat, webmail or another site opens without the shared sign-in until you open it again from OwnGit. To keep the sign-in on such links, choose "Keep the sign-in" on the Access tab, or run `owngit settings set --cross-site-links lax` (`strict` is the default). Administrator confirmation is never kept on such a link.

### Login attempt limits

By default, 4 wrong passwords from one address within 10 minutes pause that address for 15 minutes, even for the right password. The pause ends by itself. Shared and administrator passwords are counted separately, in the dashboard, Git and the API.

```sh
owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m
```

Attempts go from 1 to 100; the window and the pause from 1 minute to 24 hours. The limits cannot be turned off. During a pause, Git and the API get HTTP 429 with `Retry-After`.

Wrong administrator passwords from all addresses together also have a limit: 5 times the attempts within the same window (20 within 10 minutes with the defaults). It covers the dashboard, the API and the command line; Git asks only for the shared password and is not affected. Changing the shared password on the Access tab without typing the administrator password counts as one wrong administrator password, because OwnGit checks that the new shared password is not the administrator password. Reaching it pauses every administrator password check for the pause time, even the right password from any address. Browsers already confirmed as administrator keep working. The pause ends by itself. If you cannot wait, run [`owngit reset-admin`](#host-owner-recovery) on the installation host; it ends the pause at once, and also every browser's administrator confirmation. This limit follows the settings above and has no setting of its own. The shared password is not affected.

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
- macOS: the icon is OwnGit.app. `owngit service install` opens it and registers it to open at sign-in. Where the app is depends on how you installed OwnGit:
  - The one-line installer puts it in `/Applications` when your account can write that folder, and otherwise beside the program.
  - The Homebrew formula keeps it in Homebrew's own folder, beside the formula's `bin` folder.
  - The optional Homebrew cask puts it in `/Applications`.

  OwnGit uses the app in `/Applications` only when it is OwnGit's own app, has the same version as the program, and holds its launcher and built-in program. Otherwise it uses the app that came with the program. Before `owngit service install` opens the chosen app, it quits your account's running OwnGit icon from `/Applications` or from beside the program, whatever its version, so only one icon shows. It knows these apps by OwnGit's bundle identifier and leaves other apps alone. If an earlier icon does not quit, the command does not open another one and says so. On macOS 27, a menu bar manager such as Hidden Bar can hide the icon of an app outside `/Applications`.

  When the one-line installer moves the app to `/Applications`, the OwnGit.app that an earlier version put beside the program stays there. `owngit service install` quits its icon but does not delete the app. Once the new icon works, you can move the old app to the Trash. If an old icon still opens when you sign in, turn off its entry under Open at Login in System Settings, General, Login Items & Extensions. Do not remove the app in Homebrew's folder by hand; Homebrew manages it.
- Windows: the `OwnGit icon` task starts it at sign-in. Click opens the dashboard; right-click opens the panel.
- Linux: run `owngit service install` from a terminal on the desktop. It writes `~/.config/autostart/owngit-icon.desktop`. The desktop must show StatusNotifierItem icons (tested on Omarchy, and GNOME 48 with the AppIndicator extension) and needs `gjs` with GTK 4. On a small screen, the panel keeps its title, status line and the Open dashboard, Hide and Quit buttons in view, and the part between them scrolls. If the desktop's panel program does not answer within 30 seconds, `owngit tray icon` gives up and says why. An icon stopped while it is still starting ends normally.

Root installs on Linux and computers without a screen have no icon.

The icon shows desktop notifications for pushes, opened pull requests, failed checks, imports and backups that did not finish successfully, and new OwnGit versions. Each kind can be turned off in the panel or with `owngit tray notifications`:

```sh
owngit tray notifications push off
owngit tray notifications only_others on
```

`only_others` hides pushes, pull requests and imports that came from this computer. Other bars and scripts can read the panel with `owngit tray read --json` and open the dashboard with `owngit tray open --json`.

Notifications are a convenience. The dashboard keeps every push, check and backup result, whether or not a notification appeared. Clicking a notification opens the page it names. A notification that groups several pushes to one repository opens the commit list of the latest pushed branch or tag, or the repository when that latest push deleted it.

- Windows: notifications that arrive together appear one after another, about 10 seconds apart (5 seconds while the panel is open). When more than three arrive at once, one notification says how many there are, for example "4 new OwnGit notifications", and clicking it opens the dashboard. How Windows shows them is in [Windows notifications](#windows-notifications).
- Linux: when the desktop has no notification service, the Notifications section of the panel and the icon menu say so. OwnGit keeps trying, waiting longer each time up to five minutes, and writes the reason to its log once until notifications work again. When a service starts, the waiting notifications appear, each one once.

### Windows notifications

On Windows 10 version 1809 or later, including Windows 11, OwnGit's notifications are regular Windows notifications from an app named OwnGit (tested on Windows 11). Each one appears as a banner and then stays in the notification center. Clicking it, on the banner or in the notification center, opens its page, even after the icon has quit. The click needs OwnGit itself to be running; when it is not, no page opens.

Windows settings decide how notifications appear. With Do not disturb on, no banner appears, and the notification goes to the notification center. If you turn off OwnGit's notifications in Windows settings, OwnGit does not show them in any other way.

Windows needs OwnGit registered for your account: the app name, and the program that Windows starts when you click a notification.

- The one-line installer registers it, also with `-NoService`.
- The icon registers it again each time it starts. A program you unpacked yourself, or one that is now in another folder, works the same way.
- If registration fails, the installer prints a warning and finishes the installation anyway. To try again, run `owngit tray icon --register-notifications`; the warning shows this command with the program's full path.
- `owngit uninstall` removes the registration. If part of the registration cannot be removed, it removes the rest, warns with the registry keys left, still prints how to remove the program, and exits with an error. Your Windows notification settings for OwnGit stay for a later install.

On Windows 10 versions before 1809, or when registration fails or Windows notifications are unavailable as the icon starts, the icon shows notification area balloons instead. Windows decides how long a balloon stays and whether the notification center keeps it, and this differs between Windows versions. OwnGit opens a balloon's page only when you click the balloon while it is on screen and the icon is running. When Windows refuses one notification after the icon has started, OwnGit does not switch to a balloon; it offers that notification again later.

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

- On the Network tab, turning sharing off stays available while Tailscale is stopped, signed out or starting. OwnGit tries to remove its address. If Tailscale refuses, nothing changes and the page shows Tailscale's answer. Turning sharing on needs Tailscale running.
- One change of sharing runs at a time. A change started while another one runs waits for a limited time. If the other one is still running then, OwnGit says so and changes nothing. Try again in a moment.
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
- A backup does not hold these settings. After a restore, save them again before you start OwnGit, as in [Backups](BACKUPS.md#after-a-restore). Until then, requests through the proxy are refused with 421 or 403.

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

On macOS, Linux and other Unix systems, the file must:

- belong to your account or to root;
- be private to its owner (no access for the group or others);
- be a regular file, not a folder, pipe or device;
- be at most 1 MiB for a token or import credential file.

OwnGit opens the file once, checks the open file and reads from it, so the file cannot be swapped between the check and the read. A symbolic link is followed, and the file it points to must meet the same rules.

Create the file with `umask 077`, or fix it with `chmod 600 FILE`. On macOS, also remove any access list with `chmod -N FILE`.

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

A request may also have to wait while another transfer holds the repository, and that wait counts toward the longest transfer time. A push, clone or fetch waits for the repository no longer than the slot wait (`--transfer-queue`), then gets the 503 answer below. If some of your transfers run longer than that wait, raise `--transfer-queue` so that other pushes and fetches wait for them instead of failing.

What a client sees:

- A push over the size limit gets HTTP 413, which Git may show only as `fatal: the remote end hung up unexpectedly`. A clone or fetch over a limit is cut off.
- A clone or fetch request (the commits the client wants and already has) larger than 10 MiB gets HTTP 413, `request body exceeded the size limit`. This bound is fixed.
- A push whose objects fail Git's object check is refused, and Git prints the reason on `remote: error:` lines (see [Move an existing repository into OwnGit](REPOSITORIES.md#move-an-existing-repository-into-owngit)).
- A request that does not get a free slot or the repository in time gets HTTP 503, `Git service is busy with other transfers; try again shortly`. When this happens before any data moves, Git shows `remote: Git service is busy with other transfers; try again shortly` and `fatal: unable to access '...': The requested URL returned error: 503`. When it happens at the data transfer, Git shows only `error: RPC failed; HTTP 503`. In both cases, run the command again.
- OwnGit does not host Git LFS. A repository whose history is larger than the size limit cannot be cloned; keep large binary files out of Git history.

An import can leave a file named `objects/pack/pack-<hash>.keep` in a repository. This happens when another Git operation still holds the repository after the import finishes, or when OwnGit stops during an import; in the first case the log names the file. While the file exists, maintenance does not repack that pack. Remove the file when no import is running.

### Memory on a small Linux host

On Linux, OwnGit reads how much memory its computer or container allows and fits its work into it, as this section describes. On macOS and Windows it cannot read this, so none of these rules apply there: the saved limits apply as they are, and no file is refused for the memory it needs.

- OwnGit keeps its own memory near half of the limit. Setting `GOMEMLIMIT` replaces that figure for OwnGit, not for Git.
- With a 512 MiB memory limit, at most about 3 Git transfers run at once, and 6 with 1 GiB, even when the saved limits allow more. Only 1 clone, fetch or archive download builds a pack at a time with 512 MiB, and 2 with 1 GiB; pushes are not held back by this. Requests over these numbers wait up to the transfer queue time for a slot, then get the 503 answer above. Settings still shows the saved values.
- A page or raw download that reads file content takes one of the same transfer slots while Git reads. If no slot frees up within 10 seconds, it answers HTTP 503 with `Retry-After` and asks you to try again in a moment.
- Files above about 16 MiB (with a 512 MiB limit) or 32 MiB (with 1 GiB) are stored without new delta compression, so each new version of such a file takes its full compressed size in storage, backups and clones. This rule covers packing that OwnGit does. A push can still bring in a stored delta that needs a lot of memory to rebuild, so an accepted push does not mean every later read of its files fits this host.
- The dashboard does not show a file above that size. The page says "This file is too large to show here. Clone the repository to get this file.", and the raw download is refused with the same message.

Git stores many files as a delta: only the changes against another stored version, its base. A base can be a delta too, so one file can depend on a chain of stored versions. To read such a file, Git rebuilds it in memory, and a small file on a large base can need hundreds of MiB. Before Git rebuilds a file for a file view, a raw download, a commit page, a pull request comparison or an archive, OwnGit estimates that memory. The estimate counts the largest size in the chain twice and adds the next two largest sizes and a margin for Git's buffers, so a longer chain does not raise it. When the estimate is above about three eighths of the memory limit (192 MiB with 512 MiB), OwnGit does not ask Git to rebuild the file. With 512 MiB, this happens whenever the chain holds a version larger than about 73 MiB, even if the file itself is small.

- The file view says "This file needs more memory to rebuild than this computer gives Git, so it is not shown here.", and the raw download is refused with the same message. Files that Git stores whole are still shown up to the size named above.
- A commit page or pull request comparison shows such a file, and any file above the size named above (about 16 MiB with 512 MiB), as "Text comparison unavailable", without its lines or line counts. When a change holds more than 100 such files, OwnGit reads no line counts for that change, and its other files show "Line counts not read".
- The archive of a commit that holds such a file is refused with HTTP 409 (see [Download an archive](REPOSITORIES.md#download-an-archive)). Clone the repository with Git instead, or run OwnGit on a computer with more memory.
- Before an archive, OwnGit lists the commit's files to check their paths. That list may use a sixteenth of the memory limit (32 MiB with 512 MiB) instead of 64 MiB, so a commit with very many files can be refused as having too many files to check.
- A full clone sends the stored deltas as they are. A clone of one branch (`git clone --single-branch`) can still make Git rebuild such a file, and this check does not cover it. The transfer limits above cap how much work runs at once, not the memory one rebuild needs, so a clone of one branch can need much more memory than a full clone of the same repository.
- These refusals do not change the repository. Normal maintenance can keep the stored deltas as they are, so the file can need the same memory afterward. Repacking with forced recompression can need more memory and storage than a small host has. Do it on a computer with enough memory, and keep every branch, tag and other ref with its history.
- OwnGit remembers which files it refused or did not compare, and clears that record when it repacks a repository itself. After a repack done outside OwnGit, the old record can stay until OwnGit drops it from its read cache or restarts.

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
- The file view shows the part that fits.
- A commit or pull request shows at most 400 changed files per page. When a file's changes do not fit on the page, "Show this file's changes" opens that file alone.
- A page shows at most 10,000 lines of a file or diff and 1,000 entries of a folder. These pages, and the pages of changed files, have First page and Next page links.
- All commits shows 100 commits per page, with Older and Newer links.
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

# Operations

<p align="center"><b>English</b> | <a href="OPERATIONS.ko.md">한국어</a></p>

This page is for the person who installs and runs OwnGit, the self-hosted Git server. Each section covers one task: installing, first-time setup, running OwnGit as a service, reaching it from other devices, day-to-day repository tasks, imports, command-line pull requests and checks, and backups and recovery.

The commands are written as `owngit`. From an unpacked archive run `./owngit`, and from a source build run `./bin/owngit` (see [Build and run](../CONTRIBUTING.md#build-and-run)).

## One-line installer

The installer downloads a release, checks it, installs it and starts it as a service, in one command and without questions. On Linux and macOS:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

On Windows, in PowerShell:

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

When it finishes, OwnGit runs in the background and, in a terminal, the installer prints the setup link. Running it again later keeps the state and the repositories.

The installer takes these steps:

1. It picks the archive for this computer (Linux x64 or ARM64, macOS on Apple silicon, Windows x64) from the latest release, or from the release you name.
2. It downloads that release's `SHA256SUMS` and the archive over HTTPS, following redirects only to HTTPS, and checks the archive's SHA-256. When a download fails or the archive does not match, it stops, and nothing on the computer has changed.
3. It puts the program in place:
   - On Linux and macOS, at `~/.local/bin/owngit`, or at `/usr/local/bin/owngit` when root runs the installer. It uses `sudo` only for a folder your account cannot write, so a program that root owns stays root's.
   - On Windows, in a folder of its own for each release, `%LOCALAPPDATA%\Programs\OwnGit\owngit_X.Y.Z_windows_amd64`, because Windows does not let a running program be replaced. It never uses `%ProgramFiles%\OwnGit`, which belongs to `owngit service install`.
4. It runs `owngit service install` from there (see [Run as a service](#run-as-a-service)).

| Linux and macOS | Windows | What it does |
| --- | --- | --- |
| `--version 1.1.3` | `-Version 1.1.3` | Installs that release instead of the latest one. |
| `--no-service` | `-NoService` | Installs the program only and registers and starts nothing. It prints the two next steps: `owngit serve` runs OwnGit now, and `owngit service install` runs it as a service. |
| `--to PATH` | `-Dir FOLDER` | Puts the program at `PATH`, or the release folders in `FOLDER`. |

Options go after `/bin/sh -s --`, or to the script block in PowerShell:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh -s -- --version 1.1.3 --no-service
```

```powershell
& ([scriptblock]::Create((irm -MaximumRedirection 0 https://owngit.app/install.ps1))) -Version 1.1.3 -NoService
```

Running the installer again leaves the program alone when it is already that release. After an upgrade, `owngit service install` restarts the service with the new version, in the same mode and with the same state directory. The installer changes no PATH setting; when the program's folder is not on PATH, it says how to run it. On Windows, the folders of earlier releases stay until you delete them.

### When the installer stops

The installer stops before it downloads anything in these cases, and says why:

- The program's path on Linux or macOS is a symbolic link, such as npm's `owngit` in `/usr/local/bin`. Another install owns that link, so update that install its own way, or choose another path with `--to`.
- Another account can change the program's folder, a folder or link on the way to it (or where that link leads), or the temporary folder (`TMPDIR` or `TEMP`). That account could swap the checked program before it runs. Choose a folder that only you can change; root and, on Windows, the administrators are fine.

### How the download is protected

The curl options keep every request and redirect on HTTPS, and `-MaximumRedirection 0` keeps PowerShell from following a redirect, so the script never arrives over plain HTTP. `curl` and `sh` are named by their system paths, and the installer runs tools from the system folders only, so a folder earlier in your `PATH` cannot stand in for them.

The installer is in `packaging/installer/` of the source, and every release carries the same two files beside its archives. `SHA256SUMS` comes from the same release as the archive. The check therefore finds a damaged, incomplete or wrong download, but it is not an independent signature: whoever can change the release's files can change both. The macOS binary is also signed and notarized by Apple.

The one-line command runs the script that owngit.app serves over HTTPS before anything checks it. `SHA256SUMS` lists only the archives; the release's `manifest.json` records the SHA-256 of both installers. To check the script before it runs:

1. Download `install.sh` or `install.ps1` from the release.
2. Compare its SHA-256 with `manifest.json`, or with the one GitHub shows for that file on the release page. You can also compare the file with `packaging/installer/` at the release's tag.
3. Read it, then run the file with `/bin/sh install.sh` or `& .\install.ps1`.

## First-time setup

Install a release first. Every route needs Git with an executable `git-http-backend` on the host; Homebrew and the Arch Linux package install Git for you.

- Homebrew on macOS (Apple silicon) or Linux (x64, ARM64): `brew install juliankang4/tap/owngit`
- npm on macOS (Apple silicon), Linux (x64, ARM64), or Windows (x64): `npm install -g owngit`. This route needs Node.js to install and to start OwnGit.
- Arch Linux (x64, ARM64) or Omarchy: build the package from the `PKGBUILD` attached to each release from 1.0.3 on.
- Any of these platforms: download the archive from [GitHub Releases](https://github.com/juliankang4/owngit/releases) and check it against `SHA256SUMS`.

[Install](../README.md#install) in the README gives the full steps. To run OwnGit in the background from the start, go to [Run as a service](#run-as-a-service). To run it in the foreground:

```sh
owngit serve
```

The default address is `http://127.0.0.1:7654`. Setup asks for three things:

- the repository folder;
- whether general access is open, or protected by one shared password;
- a separate administrator password, which the dashboard asks for before administrator changes ([how often](#administrator-password-check)).

Setup ends at an empty dashboard. New repository creates a repository with a clone address of the form `http://HOST:7654/git/PROJECT.git`.

The dashboard lists the most recently updated repositories first, by the author date of the latest commit on each default branch. Sort beside the list switches to oldest first or to name order, and this browser remembers the choice.

How setup starts depends on how OwnGit runs. The next four sections cover each case.

### Setup in the terminal

When you start an installation that is not set up yet with `owngit serve` in the foreground of a terminal, setup runs there. It asks for the language first (English or 한국어; Enter keeps your locale's language, and L switches it later). Then it offers "Continue in this terminal" or "Open the web dashboard".

In the terminal, OwnGit asks the same questions as the web page:

1. the repository folder;
2. who can read and write repositories;
3. the administrator password, typed twice with nothing shown;
4. "Other devices";
5. when OwnGit listens on a network address, whether to continue over unencrypted HTTP.

Nothing is saved until you choose "Finish setup" on the review card. Ctrl-C stops the server without saving; run `owngit serve` again to start over. Only Enter, Backspace, Ctrl-U, Ctrl-C and Escape act as keys; any other character becomes part of the answer.

When OwnGit listens only on this computer, setup does not ask about plain HTTP; the Settings page asks later if you reach OwnGit from another device. If you answer no to plain HTTP, OwnGit tells you how to stay on this computer only: start again with `--listen 127.0.0.1:PORT`, or, when the address comes from saved [network settings](#network-settings), run `owngit network set --listen 127.0.0.1:PORT --base-url=` and start again.

"Other devices" checks whether Tailscale runs on this computer. It only reads Tailscale's status.

- If Tailscale cannot be used, it says why.
- If Tailscale runs, it shows this computer's Tailscale address and MagicDNS name and prints a command that saves them as [network settings](#network-settings), such as `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654`. When `owngit` on PATH is not the OwnGit that runs, as for a copy from an archive, the command starts with that copy's path instead.

Setup does not run that command; run it after setup and restart OwnGit. Tailscale encrypts the connection, but OwnGit cannot see that and still reports plain HTTP for this address. For an address that OwnGit reports as encrypted, [share on your tailnet over HTTPS](#share-on-your-tailnet-over-https) instead.

### Setup in the browser, approved in the terminal

"Open the web dashboard" opens `http://127.0.0.1:7654/setup` in your browser; with `--no-open`, the terminal prints the address instead.

1. In the browser, choose "Ask the terminal for approval". The page shows a short code.
2. The terminal shows "A browser wants to set up OwnGit" with the same code and the address the request came from. A request from another device is marked with a warning.
3. Answer `y` only when your browser shows that code.

The approval applies to that one browser, which then continues on the web page. When setup finishes, the terminal lists the saved answers.

Limits on approval requests:

- Only one browser waits for approval at a time.
- A rejected browser waits a minute before asking again, and each address can ask at most five times in ten minutes.
- An unused request or approval expires after ten minutes.
- Press T while waiting to set up in the terminal instead; a browser you already approved then loses its setup session. Approving a second browser also ends the first one's setup session.

### Setup with a setup file

When OwnGit starts without a terminal, it writes an owner-readable setup file inside the state directory and writes the file's path, never the link itself, to the server log. Open that file in a browser on the installation host, or run `owngit setup-link` in a terminal there.

A service never opens a browser. The services that `owngit service install` writes and Homebrew's `brew services` all start OwnGit with `--no-open`, so the path is only in the log; for Homebrew that is `$(brew --prefix)/var/log/owngit.log`. OwnGit opens the file in the installation owner's browser itself only when it runs as a background job in a session with a screen that someone sees, was started without `--no-open`, and can open a browser. [A computer without a screen](#a-computer-without-a-screen) explains when OwnGit counts a session as having no screen. On Windows, a program started in the background outside SSH also uses the setup file, since nobody reads its console.

`owngit setup-link` issues a new link that replaces the one before:

- On a terminal it prints the link, which works once within 15 minutes. When its output is a pipe, a file or the journal, it prints only the path of the setup file.
- Without `--base-url`, the link uses the address the server listens on. When that is every address, it lists this computer's addresses, the most likely first.
- Before setup is finished, the link also works from another device by an address OwnGit was not started with. `owngit setup-link --base-url http://192.168.1.20:7654` makes the link for that address, which then shows only the setup page. After the link is used, only that browser on that address can continue, and the setup form offers to keep accepting the address after setup.

### A computer without a screen

On a computer where nobody can open a browser, setup has to happen from another device. So the first start before setup, with no listen address saved and no `--listen` option, listens on every address (`0.0.0.0:7654`) and saves that as the listen address. Until setup is finished, it answers other devices only with the setup page.

The setup link is printed for private addresses only: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, the tailnet range `100.64.0.0/10` and IPv6 unique local addresses. A computer with only public addresses, such as many cloud servers, prints an SSH command instead (`ssh -L 7654:127.0.0.1:7654 USER@HOST`) with a link on `http://127.0.0.1:7654`. Run the command on your own computer, keep it open, and open the link there.

The setup page asks you to accept plain HTTP and offers to keep accepting the address you used. That box is ticked by default unless you opened setup from a public address. If you untick it, OwnGit refuses that address after setup and, from its next start, listens only on `127.0.0.1:7654`. To stay on this computer only from the start, run `owngit network set --listen 127.0.0.1:7654` and restart. A computer with a screen listens on `127.0.0.1:7654` until you choose another address.

OwnGit counts a computer as headless in these cases:

- root runs it in a container;
- the command runs in an SSH session without a display (`DISPLAY` and `WAYLAND_DISPLAY` unset; on Windows, any SSH session);
- systemd-logind lists no graphical session and neither variable is set;
- on a Mac, the user who runs OwnGit is not logged in on the screen. A Mac where you are logged in on the screen counts as having one, even over SSH.

A service passes `--headless=true` or `false` from its install, so it does not decide again at boot.

## Run as a service

`owngit service install` runs OwnGit in the background and restarts it by itself:

- on Linux, with systemd at every boot;
- on macOS, with launchd at every login;
- on Windows, with Task Scheduler at every boot (administrator account) or sign-in (standard account).

It chooses who runs the service from how you run the command, waits until the service answers, and prints the setup link. If the service stops with an error while it waits, for example because another program uses the port, the command prints that error with the next step.

| Command | What it does |
| --- | --- |
| `owngit service status` | Whether OwnGit runs and answers, who runs it, the unit, agent or task, where the log is, the state directory and the addresses. |
| `owngit service start`, `stop`, `restart` | Start, stop or restart the service. A stopped service starts again at its next boot or login trigger. |
| `owngit service uninstall` | Stop the service and remove its unit, agent or task. The state directory, the repositories and, on Linux, the `owngit` account stay, and the command says where the data is. For a Linux user service it reminds you that lingering stays on (`loginctl disable-linger` turns it off). |
| `owngit uninstall` | The same, and then how to remove the program itself; see [Update and uninstall](#update-and-uninstall). |

After you replace the binary with a new release, run `owngit service install` again. It rewrites the unit and restarts the service in the same mode with the same state directory.

Every unit or agent starts `owngit serve --state-dir DIR --no-open --headless=true` or `--headless=false`. It never passes `--listen` or `--base-url`, so the saved [network settings](#network-settings) apply. The headless choice of the first install is kept; `--headless=true` or `--headless=false` changes it. To go back to this computer only after a headless start, also run `owngit network set --listen 127.0.0.1:7654`.

### Linux

Which kind of service you get depends on where you run the command:

- **On a desktop**, OwnGit becomes a systemd user service of your account (`~/.config/systemd/user/owngit.service`) with the state in your usual state directory. It turns on lingering (`loginctl enable-linger`) so that it starts at boot. Debian 13, Ubuntu 24.04 and 26.04 and Arch Linux allow this without a password; where lingering needs one, OwnGit installs a system service instead.
- **Over SSH, or without a graphical session**, OwnGit becomes a system service that runs as your account (`/etc/systemd/system/owngit.service` with `User=` and `Group=`), with the state in your usual state directory. The command says in one line what root will do and runs one `sudo` for it: write the unit, reload systemd, enable and start. If `sudo` is unavailable or does not finish, it prints the whole script for an administrator to paste into a root shell.
- **As root**, for example in an LXC container or on a cloud server, OwnGit creates a system account `owngit`, keeps the state in `/var/lib/owngit/state` and runs the service as that account. See [Installing as root](#installing-as-root).
- **When Homebrew installed OwnGit**, the command runs `brew services restart owngit`, so that Homebrew keeps managing the service it upgrades.

The log goes to the systemd journal: `journalctl --user -u owngit.service -f` for a user service, `sudo journalctl -u owngit.service -f` for a system service.

#### Installing as root

- A `--state-dir` must lie inside `/var/lib/owngit`.
- The binary must be in a place only root can change, such as `/usr/local/bin/owngit`; a binary in a home folder is refused.
- The state directory path is written to `/etc/owngit/state-dir`, so `owngit setup-link`, `owngit network` and the other state commands find it without `--state-dir`. When root runs them, they run as the `owngit` account. A file you pass, such as `backup --output`, must therefore be an absolute path that account can write, such as `/var/lib/owngit/backup`. `reset-admin --password-file` still reads a file only root can read.

If root already used OwnGit with its own state in `/root/.config/owngit` (for example with 1.1.0), root's `owngit service install` leaves that state where it is. While the new state is not set up, the command prints the commands that serve root's installation instead: stop that OwnGit, back it up, restore it as the `owngit` account, and point the service at the copy.

```sh
sudo owngit backup --state-dir /root/.config/owngit --output /var/lib/owngit-root-backup && \
sudo chown -R owngit: /var/lib/owngit-root-backup && \
sudo runuser -u owngit -- owngit restore --input /var/lib/owngit-root-backup --state-dir /var/lib/owngit/state-from-root --repository-root /var/lib/owngit/repositories && \
sudo owngit service install --state-dir /var/lib/owngit/state-from-root
```

As with every restore, sessions, trusted hosts and network settings are not carried over. Sign in again, and to reach OwnGit from other devices run `sudo owngit network set --listen 0.0.0.0:7654 --allowed-host ADDRESS` and `sudo owngit service restart`. Root's old state, the backup and the unused `/var/lib/owngit/state` stay until you remove them. Later installs keep using the restored state.

### On Windows

`owngit service install` registers a Task Scheduler task named `OwnGit` that runs OwnGit in the background as your account. The account you run it from decides when OwnGit starts:

- **From an administrator account** (the first account on a Windows computer), the task starts at boot, before anyone signs in, without a stored password (the "Do not store password" logon). Registering it needs one User Account Control approval. The command says beforehand what the approval does, and declining it changes nothing. In a terminal opened with "Run as administrator", or over SSH as an administrator, no prompt appears; over SSH without administrator rights, the command says what to do instead.
- **From a standard account**, the task starts when you sign in, and installing it asks nothing. Windows lets a standard account create neither a boot task nor one that runs without a sign-in; to start OwnGit at boot, install it from an administrator account. No firewall rule is added (see [Reaching the server from another device](#reaching-the-server-from-another-device)).

The one approval from an administrator account does the following:

1. Copies the program to `%ProgramFiles%\OwnGit\owngit.exe` and protects that folder so that only Administrators and SYSTEM can change it.
2. Registers the task for that copy.
3. Adds a Windows Firewall rule named `OwnGit` for private networks; public networks stay closed. The rule carries OwnGit's description, and OwnGit changes or removes only a rule with that description for an `owngit.exe`. If a rule named `OwnGit` that OwnGit did not add exists, OwnGit adds and removes no rule of that name: the install stops before it changes anything and says so, and uninstall leaves the rules.
4. Installs Git for Windows with `winget` if Git is missing from the machine and user PATH. If Git is installed but not on that PATH, the command instead asks you to add Git's `cmd` folder, for example `C:\Program Files\Git\cmd`, and run it again.
5. Returns to your account any files in the state directory and the repository folder that an earlier OwnGit with administrator rights left to the Administrators group. Git refuses such repositories as having "dubious ownership". The command says how many files it changed. It changes only what the Administrators group owns, follows no link, and changes nothing in another account's folder, a whole drive, or a Windows or program folder.

From a standard account, `owngit service install` does step 5 alone, with an administrator's approval: Windows asks once for an administrator's password, and the step gives the folders to the account that ran the command. It does so only for the state directory and repository folder of that account's own OwnGit inside its user folder, and only for the install that asked; a folder elsewhere needs an administrator. Over SSH there is no desktop for that prompt, so run the command at the computer.

A state directory or repository folder outside your user folder, such as `C:\OwnGit`, works too.

#### How the Windows service runs

An administrator account's task starts `%ProgramFiles%\OwnGit\owngit.exe serve --state-dir DIR --no-open --log-file DIR\logs\service.log --service --headless=true` (or `false`). A standard account's task starts the `owngit.exe` used to install it.

- The first process only supervises. `--service` makes it start the server as a copy with the rights of an ordinary window of your account, so Git, hooks, checks and new files belong to your account. It restarts the server 5 seconds after a failure.
- Task Scheduler keeps no output, so the log is `logs\service.log` in the state directory, kept below 10 MB with one older file beside it. When the server cannot start, `owngit service install`, `start` and `status` show why, and the log ends with the same error.
- `owngit service stop` asks the server to finish and stop, as Ctrl-C does. It ends the task only when the server has not stopped after 150 seconds.
- On a newly installed Windows, a boot task stays "Queued" until someone signs in at the screen for the first time (a sign-in over SSH does not count). After that it starts at once and at every boot. `owngit service install` and `status` say so when they find the task queued.

#### Updating and removing the Windows service

To update, unpack or install the new release outside `%ProgramFiles%\OwnGit` and run its `owngit service install`. `owngit service status` tells you when its version differs from the protected copy, and `service install` is refused from the protected path.

From an administrator account, the update stops the old service, moves the old folder to `OwnGit.old-TIMESTAMP`, installs the new copy, refreshes the firewall rule and starts the new version. It removes the old folder when that folder holds nothing but `owngit.exe`, `installed-from.txt` and `temp`. A standard account runs the same command with the new `owngit.exe`. The protected copy keeps a note of the `owngit.exe` it was copied from, `installed-from.txt`, so the running service can show how that program is updated.

`owngit service uninstall` removes the task, the firewall rule and the protected copy with one approval; the state directory and the repositories stay. When an install stopped partway and left the protected copy or the rule without the task, the same command from an administrator account removes them too. A folder that also holds files OwnGit did not create stays, and the command says so.

### macOS

`owngit service install` writes a LaunchAgent for your account, `~/Library/LaunchAgents/app.owngit.server.plist`, and starts it without an administrator password. launchd starts OwnGit at every login and restarts it if it stops. That is at login, not at boot; with automatic login on, it is right after a restart. The state is in `~/Library/Application Support/owngit` and the log in `~/Library/Logs/owngit/owngit.log`.

macOS lists the agent as `owngit` under System Settings, General, Login Items & Extensions, Allow in the Background. When it is turned off there, the agent does not start; the command says so and puts back the agent that was there before.

- **Over SSH** while you are logged in on the Mac's screen, the command works as in a Terminal window there. While you are not, OwnGit starts right away and keeps running until the Mac restarts, then starts at your next login; such a Mac counts as [a computer without a screen](#a-computer-without-a-screen). As root, the command refuses.
- **When Homebrew installed OwnGit** and you are logged in on the screen, the command runs `brew services restart owngit`. `status`, `start`, `stop`, `restart` and `uninstall` use `brew services` too, and the log is `$(brew --prefix)/var/log/owngit.log`. A Homebrew install always uses the default state directory, so `--state-dir` and `--headless` are refused there; `owngit network set --listen` changes the address instead. Over SSH while nobody is logged in on the screen, Homebrew's service cannot start, so the command installs the OwnGit LaunchAgent for `$(brew --prefix)/opt/owngit/bin/owngit` instead. A later `owngit service install` or `brew services start owngit` on the desktop hands the service back to Homebrew and removes that agent.
- **When npm installed OwnGit**, the agent starts the executable from the platform package, for example `/opt/homebrew/lib/node_modules/owngit/node_modules/owngit-darwin-arm64/bin/owngit`, not the Node.js launcher, so the [launcher's signal limits](../packaging/README.md#homebrew-winget-npm-and-arch-linux) do not apply. After `npm update -g owngit` or a Node.js change, run `owngit service install` again.

The agent starts OwnGit by the path you ran the command with, for example `/usr/local/bin/owngit`. It refuses a binary that an account other than yours or root could change; group write by macOS's `wheel` and `admin` groups is accepted. If a launchd job that `owngit service` did not create already runs `owngit serve`, the command names it and changes nothing. Unload and remove that job first, for example with `launchctl bootout gui/$(id -u)/LABEL`.

#### Log files on macOS

The service keeps two files in the log folder, `~/Library/Logs/owngit`, or `$(brew --prefix)/var/log` for Homebrew:

- `owngit.log` is the server log. OwnGit writes it itself (`--log-file` with `--service`) and makes it readable only by your account. It keeps the file below 10 MB by moving it to `owngit.log.1`, which replaces the older one, when it reaches that size.
- `owngit.stderr.log` receives what launchd collects from OwnGit's output. That is only what the log cannot hold, such as a crash report or the error that kept OwnGit from opening its log, so it stays small; launchd does not limit its size.

An installation from an earlier version keeps writing everything to one unlimited `owngit.log` until you run `owngit service install` again, or, for Homebrew, upgrade and run `brew services restart owngit`.

### What the service may do

A service that runs as your account can do what your account can do, and so can the checks that run from pushed commits. If other people can push, install OwnGit as root on Linux so that it runs as the separate `owngit` account.

A Linux system service runs with systemd hardening that does not limit Git, hooks, or checks on this computer or in Docker. What you notice:

- Nothing OwnGit starts can gain privileges, so a check cannot use `sudo` (`NoNewPrivileges`, `RestrictSUIDSGID`).
- `/usr`, `/boot`, `/efi` and `/etc` are read-only (`ProtectSystem=full`). Everywhere else the file permissions of the service account decide, so a service that runs as your account can use your home folder and a repository folder your account owns.
- The `owngit` account cannot see `/home`, `/root` or `/run/user` (`ProtectHome=yes`), so its repository folder must belong to it. For a new folder, setup shows the command (`sudo install -d -o owngit -g owngit -m 0700 /srv/git`); for an existing one such as `/opt`, it suggests a new folder inside, such as `/opt/owngit-repos`.
- OwnGit has its own `/tmp` (`PrivateTmp`). Check workspaces live in the state directory, so Docker checks still see them.
- New files are private to the service account (`UMask=0077`).

The unit also sets `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `LockPersonality`, `RestrictRealtime`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, an empty `CapabilityBoundingSet=`, `SystemCallArchitectures=native` and `ProtectProc=invisible`, none of which OwnGit uses. A user service has only `NoNewPrivileges`, `RestrictSUIDSGID`, `LockPersonality`, `RestrictRealtime` and `UMask=0077`. In a container that does not allow a setting, systemd leaves it out and still starts OwnGit.

### Health check

`GET /healthz` answers `200 OK` with an empty body while OwnGit serves HTTP, before and after setup, and reads no state. Like every other path, it answers only a Host name OwnGit accepts, so a monitor on another device must use an approved name or address. `owngit health` checks the server of a state directory on this computer and exits 0 when it answers; `owngit service status` and `install` use the same check.

## Checkup

`owngit doctor` looks at the OwnGit of a state directory on this computer. It prints each problem it finds with one command that repairs it, or says that it found none. It also prints the program, the state directory, the repository folder, the listen address and where the service log is; `--json` prints the same as JSON.

It checks:

- whether the server of this state directory runs and answers (`owngit service start`, or `owngit service install` when there is no service), whether another program answers at its address instead, and whether setup is complete (`owngit setup-link`);
- on Windows, a state directory or repository folder that the Administrators group owns, which OwnGit cannot use because it runs without administrator rights. The repair is `owngit service install` for every account: it gives the folder back with one approval, from an administrator account or, for a standard account, with an administrator's password;
- when OwnGit listens for other devices, the firewall of this computer, as described in [Reaching the server from another device](#reaching-the-server-from-another-device). OwnGit reads the Windows Firewall rules, network type and "block all" setting, and the macOS application firewall. The rules of ufw and firewalld need root to read, so OwnGit lists them under "Could not check" with a command that allows the port only from the private networks it listens on. When it finds no such network, it says what to allow in words.

OwnGit never runs a repair itself; you run the command on this computer. The checkup names only what the configuration of this computer does; it cannot see your router or the other device. A check that could not run, or settings OwnGit cannot read, is listed under "Could not check", never as a clean result.

The General tab of Settings shows the same checkup, only to a confirmed administrator, because it shows this computer's paths.

## Update and uninstall

`owngit update` tells you the command that updates this installation. It works out how OwnGit was installed from facts on this computer, not from guesses:

| Route | How OwnGit knows | Update command | Removing the program |
| --- | --- | --- | --- |
| Homebrew | The program is in Homebrew's `Cellar/owngit` | `brew upgrade owngit` | `brew uninstall owngit` |
| npm | The program is `bin/owngit` of an `owngit-<platform>` package in `node_modules` | `npm install -g owngit@X.Y.Z`, as `sudo npm` when your account cannot write the global `node_modules` folder | `npm uninstall -g owngit`, with `sudo` in the same case |
| Arch Linux package | `/usr/bin/pacman -Qo`, a program only root can change, names the package that holds the program | For `owngit-bin`, builds the new release's `PKGBUILD` with `makepkg -si` in a new temporary folder. Another package gets no command, because the release `PKGBUILD` would replace it; update it the way you installed it | `sudo pacman -R` and the package name |
| Release archive or the [one-line installer](#one-line-installer) | None of the above | Runs the new release's installer for this program: `/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/juliankang4/owngit/vX.Y.Z/packaging/installer/install.sh \| /bin/sh -s -- --version X.Y.Z --to <program>`, or on Windows its `install.ps1` with `-Version X.Y.Z -Dir <folder>`. See [Details of the update command](#details-of-the-update-command) | Delete the file (and the folder you unpacked, if you made one) |

`owngit update` asks GitHub for the latest release when you run it, even when the daily check is off. It prints the release, the route, the program path and the command; `owngit update --json` prints the same as JSON. Neither the command nor the dashboard runs anything.

What the printed command does with the service:

- When a service of your account runs this program, the command also runs `owngit service install`, which rewrites the service for the new version and restarts it. For an archive, the installer runs it. Without such a service, the command passes `--no-service`, or `-NoService` on Windows.
- Homebrew's service is restarted with `brew services restart owngit`.
- When the service runs a different OwnGit, for example a release archive while you update the npm copy, the command updates only this program and leaves the service alone; `owngit update` says so.
- Without a service, restart OwnGit yourself afterwards. After a Windows archive update, start it from the new folder's `owngit.exe`, which the command names.

### Details of the update command

- For a release archive, the installer checks the archive against `SHA256SUMS` before it changes anything. It uses `sudo` only when your account cannot write the program's folder, so a program that root owns stays root's, as a service installed by root requires. On Windows it unpacks the new release into a folder named after it beside the current one.
- On Windows, when your sign-in task runs the npm program itself, the command starts with `owngit service stop`, because Windows does not let npm replace a running program. If npm then fails, `owngit service start` starts the old version again. Each step of an npm command runs only when the one before it succeeded, in Windows PowerShell and PowerShell 7 alike.
- A program built for a platform that has no release archive gets no command; `owngit update` says to build the new release from source.
- Because `makepkg` refuses root, root gets no Arch Linux command either; run `owngit update` as your normal account.

### Uninstall

`owngit uninstall` removes what OwnGit itself created with `owngit service install`:

- the service unit, LaunchAgent or scheduled task;
- for a Homebrew install, `brew services`' registration (with `brew services stop`);
- on Windows, the protected copy in `%ProgramFiles%\OwnGit` with its firewall rule.

It never deletes the state directory or the repositories, and it prints where they are, so a later install uses them again. There is no separate step that deletes data. Files that a package manager installed are its to remove, so OwnGit leaves them and prints its command. A program you placed yourself stays, and OwnGit prints the command that deletes it. Running `owngit service install` again, or reinstalling with the same route, keeps the state and the repositories.

## Run in a container

The OwnGit container image, `ghcr.io/juliankang4/owngit`, runs on Linux on x64 and ARM64 computers with Docker Engine and Docker Compose. Each release has the tags `X.Y.Z`, `X.Y` and `latest`, with build provenance. The image holds the release's `owngit` program and Git on Debian 13, and runs as the account `owngit` (user and group ID 10001), never as root. It needs no privileged mode, no Linux capabilities, no Docker socket and no host network.

Save [`compose.yaml`](../packaging/container/compose.yaml) in a new folder and start OwnGit there:

```sh
docker compose up -d
docker compose exec -it owngit owngit setup-link
```

`setup-link` shows the one-time setup link only on a terminal, which `-it` gives it. The log that `docker compose logs` shows names only the setup file inside the container, never the link. Open the link, `http://localhost:7654/setup#...`, in a browser on the computer that runs the container. From another device, use that computer's name or address in place of `localhost`, for example `http://nas.local:7654/setup#...`; the setup page then offers to keep accepting that name, which other devices use from then on. `docker compose ps` shows the container as `healthy` once `owngit health` inside it gets an answer.

Other commands run the same way, for example `docker compose exec owngit owngit doctor`.

### Where the data lives

Everything lives in the volume `owngit-data`, mounted at `/data`: the state in `/data/owngit`, the repositories in `/data/OwnGit-Repositories` (the folder setup suggests), and the [backups made before an upgrade](#backup-before-an-upgrade) in `/data/owngit-backups`. Restarting, recreating or updating the container keeps the volume. `docker compose down` removes the container and keeps the volume; `docker compose down -v` deletes the volume with all repositories.

The state must stay on a local disk. To keep the repositories on a network share, mount the share at another path in the container, such as `/repositories`, and choose that folder in setup; the state stays in the volume.

### localhost and other addresses

OwnGit listens on every address inside the container, and the `ports` line of `compose.yaml` decides who reaches it. `"7654:7654"` publishes it on every address of the computer that runs the container; `"127.0.0.1:7654:7654"` keeps it on that computer only. To use another port, change the first number, for example `"8080:7654"`, and use that port in the setup link.

Inside a container, OwnGit cannot tell the computer that runs it from other devices by their address: Docker forwards IPv6 connections, and rootless Docker every connection, from an address inside the container network. So OwnGit in the container image accepts `localhost`, `127.0.0.1` and `::1` from outside the container only while access needs the shared password, and every visitor then meets the sign-in page. In open access, the default, it refuses them with a page that says to use the computer's name or address instead, on that computer too. So for open access, finish setup at that name or address and keep it, or allow one later with `docker compose exec owngit owngit network set --allowed-host nas.local` and `docker compose restart`. Before setup only the setup link works, as on every computer. Wrong passwords are counted per address, so under the forwarding above several devices can share one count.

### Update

When a newer release exists, the dashboard notice and `owngit update` show the command for the container:

```sh
docker compose pull && docker compose up -d
```

Run it in the folder of `compose.yaml` on the computer that runs the container. It downloads the new image and recreates the container with it; the volume stays. OwnGit backs up the state before it upgrades it, as described in [Backup before an upgrade](#backup-before-an-upgrade). To keep a backup that you made yourself as well, stop OwnGit and make one first:

```sh
docker compose stop
docker compose run --rm owngit owngit backup --output /data/backup-before-update
docker compose pull
docker compose up -d
docker compose exec owngit owngit backup verify /data/backup-before-update
```

The output folder must not exist yet, so use a new name each time. A backup in the volume goes when the volume goes; to keep one elsewhere, mount a folder that the account `owngit` (ID 10001) owns and that no other account can change, and write the backup there.

### Running as another account

To run OwnGit as another account, for example to match the owner of a folder on the computer, set `user:` in `compose.yaml` and keep `/data` in a folder that this account owns and that no other account can change:

```yaml
services:
  owngit:
    user: "1000:1000"
    volumes:
      - ./owngit-data:/data
```

```sh
sudo install -d -o 1000 -g 1000 -m 700 owngit-data
```

The named volume of the default `compose.yaml` belongs to the account `owngit`, so another account cannot use it. OwnGit refuses a `/data` folder that other accounts can change, and its log names the `chmod` command that fixes it.

## Settings

Settings has five tabs. Each is its own address, so it works as an ordinary link, also without JavaScript:

- **General** (`/settings`): the display choices of this browser (language, appearance and repository list order), which apply at once and never ask for a password, and the new-release [update check](#new-release-notice) for the whole server.
- **Access** (`/settings/access`): who can read and push (anyone who reaches OwnGit, or only people with the shared password), [how long a sign-in lasts](#how-long-a-sign-in-lasts), the administrator password, and [how often it is asked](#administrator-password-check).
- **Network** (`/settings/network`): the connection of this browser, the [network settings](#network-settings) and [sharing on your tailnet](#share-on-your-tailnet-over-https).
- **Repositories** (`/settings/repositories`): the [branch new repositories start on](#changing-the-default-branch), whether overwritten and deleted history is [kept](#kept-history), the [Git transfer limits](#git-transfer-limits) and a link to the settings of each repository.
- **Storage & recovery** (`/settings/storage`): the repository folder, shown only to an administrator, and how long [raw check logs](#raw-check-logs) are kept.

Each part of a tab has its own Save and Cancel, and Save sends only that part. Save asks for the administrator password unless this browser is confirmed as administrator or the check is off. Network settings apply at the next start; everything else applies as soon as you save.

- When OwnGit refuses a save, the part says why and keeps what you entered, except passwords. When the network settings changed after you opened the page, the Network part shows the values saved now instead, so check them before you enter your change again.
- With the shared password already on, leave New shared password empty to keep it.
- Turning the shared password on, or changing it, signs out everyone signed in with the shared password. A browser that is not confirmed as administrator then signs in with the new password.
- Changing the administrator password is a separate form that always asks for the current one.

### Leaving with unsaved changes

When a part holds a change you have not saved and you leave the page, or save another part in a way that reloads the page, Settings first asks what to do. A Save that asks for the password reloads the page; a Save that needs no password keeps the page and your other changes.

The question lists each change, showing a password only as entered, and offers Save and leave, Discard and leave, and Stay; Escape means Stay. Display choices never count as unsaved changes.

- Save and leave saves the parts one after another and leaves only when all of them are saved. If one is refused, you stay on the page: the parts already saved show their saved values, and the others keep your changes.
- Only one part with a password field can be saved on the way out, and only when you go to another OwnGit page by a link or by Back. The question asks for the administrator password when that part needs it. A part it marks Save separately must be saved with its own Save.
- Reloading, closing the tab, or going back to another site shows the browser's own question instead.

### Server settings on the command line

Five settings are shown with their saved values only to a confirmed administrator: [how long a sign-in lasts](#how-long-a-sign-in-lasts), [the branch new repositories start on](#changing-the-default-branch), the server-wide [kept history](#kept-history) choice, the [Git transfer limits](#git-transfer-limits) and how long [raw check logs](#raw-check-logs) are kept. Anyone else who opens Settings sees what each one does and a button to confirm as administrator.

The command line reads and changes the same five through the administrator API. Both commands need `--server` and a `--password-file` holding the administrator password:

- `owngit settings show` prints these settings as JSON.
- `owngit settings set` changes only the ones its options name, such as `--session 7d`.

When a saved setting cannot be read, for example after a hand edit of the state database, `show` fails and names it. `set` still saves the settings it names and lists the unreadable one under `unreadable`; setting it again replaces it. While the server-wide kept history choice cannot be read, pushes and imports to every repository that follows it are refused.

These settings belong to this installation host and are not in backups, so a restored installation starts with the defaults.

### How long a sign-in lasts

"A sign-in lasts" on the Access tab sets how long a browser stays signed in after it signs in with the shared password: 1 hour, 8 hours, 12 hours (the default), 1 day, 7 days or 30 days.

A new time applies to sign-ins after you save it; a browser already signed in keeps the end it has. To end every sign-in now, change the shared password. A longer time lets someone using a signed-in browser use the dashboard without the password for longer. When anyone can reach OwnGit without a password, nobody signs in and the time has no effect.

### Administrator password check

"Ask for the administrator password" on the Access tab decides when the dashboard asks for it. It applies to Settings, repository settings, deletion, imports, checks, and helper and runner credentials:

- **Every time**: each change asks. Signing in as administrator opens the administrator pages for a short time only.
- **Again after 30 minutes** (the default), **1 hour**, **8 hours**, **1 day**, **7 days** or **30 days**: after you type the password, on the administrator sign-in or in a form, this browser does not ask again for that long. The time counts from when you typed it; moving between pages does not extend it. Another browser is asked for its own. The sidebar shows until when this browser is confirmed, with End to stop now. Signing out, End, or changing or resetting the administrator password ends it. Choosing a shorter time shortens it to the new time counted from when the password was typed, so it may end at once.
- **Do not ask**: anyone who can open the dashboard can change settings, delete repositories, issue credentials and turn on automatic checks without the administrator password. When anyone can reach OwnGit without a password, nobody has to sign in either. Turning it on asks for the password one last time and for a tick confirming the warning. While it is on, every page shows "Administrator password check off", which leads back here.

To end the confirmation of a browser you no longer have, change the administrator password in Settings, or run `owngit reset-admin`; either ends every browser's confirmation.

The command line and the administrator API always ask for the administrator password, for reads as well as changes, whatever the choice. A browser that is confirmed in the dashboard is not signed in to the API.

The choice belongs to this installation host and is not in backups; a restored installation asks after 30 minutes again. If the saved choice is one this version does not know, for example after going back to an older release, every change asks, and Access says so until you choose again.

### OwnGit icon

This version prepares the OwnGit icon in the macOS menu bar, the Windows notification area and the Linux panel: it keeps the choice to show or hide the icon and serves the status the icon reads. The icon itself is not part of this version yet.

The icon belongs to the computer that runs OwnGit and to the account that runs it. It shows by default on a computer with a desktop and never on one without, such as a server reached over SSH. When root installs OwnGit on Linux, the service runs as its own `owngit` account, which nobody signs in as at a desktop, so that kind of install has no icon; Settings and `owngit tray` say so. `owngit tray off` hides it on this computer until `owngit tray on` shows it again, also after signing in again or restarting. `owngit tray` (or `owngit tray status`) says whether it shows; `--json` prints `available` (false on an install without an icon), `shown` (false only when hidden), `desktop` (whether this computer has a desktop now), `state_dir` and `access_file`. The switch "Show the OwnGit icon in the menu bar, notification area or panel" on the General tab of Settings does the same and asks for the administrator password like the other parts of Settings. It changes the icon of the computer that runs OwnGit, not of the computer your browser is on. Hiding the icon never stops OwnGit: Git and the dashboard keep working. The choice is the file `tray-hidden` in the state directory, so it belongs to this computer and is not in backups.

Each time OwnGit starts, it writes the file `tray-access.json` in the state directory, readable only by the account that runs OwnGit. It holds the address of this server on this computer, such as `http://127.0.0.1:7654`, and a new random token; the file stays after OwnGit stops, but a token from an earlier start no longer works. `GET /tray/status` with the header `Authorization: Bearer <token>` answers a JSON status: the state (`running`, or `attention` when setup is not finished, a newer release is out, or the [checkup](#checkup) found a problem), whether the icon is hidden, the version, the dashboard and clone addresses as the dashboard shows them, the newer release with this install's update command, the checkup findings and the three latest pushes (repository, ref, number of refs, when OwnGit received the push, and how it was authorized, which for Git is always general access). A push is listed only when Git changed a ref; a refused or failed push, or one that asked to set a ref to the value it already had, is not, and a commit's own date never counts as the push time. OwnGit keeps the latest 100 pushes, and they are not in backups. The status answers only a program that connects directly from this computer, never a request a trusted proxy forwards or one that carries forwarding headers, and never without the token. A connection another device makes through a program on this computer, such as Tailscale in userspace networking mode, also looks local, so the token is what keeps the status from other devices and from other accounts on this computer. Programs that run as the account that runs OwnGit can read the token, as they can read the state directory itself. `/healthz` stays empty.

## Reaching the server from another device

OwnGit serves plain HTTP and has no built-in TLS. Choose one of these ways to reach it from another device:

- **An encrypted address**: let Tailscale share OwnGit on your tailnet ([Share on your tailnet over HTTPS](#share-on-your-tailnet-over-https)), or put a reverse proxy in front of it ([Behind a reverse proxy](#behind-a-reverse-proxy)).
- **Plain HTTP** over Tailscale, your own VPN ([Other private networks](#other-private-networks)) or the LAN: let OwnGit listen on a network address ([Network settings](#network-settings)). You accept plain HTTP once, during setup or on the Network tab of Settings when you let other devices in, and the page header always shows whether the connection is encrypted.

Do not expose OwnGit to the public Internet.

A device on your tailnet that opens one of this computer's Tailscale addresses, such as `http://100.64.0.7:7654/`, sees "Encrypted by Tailscale" and is not asked to accept plain HTTP. OwnGit checks that the request came from a Tailscale address to an address that Tailscale on this computer reports as its own. Right after a start, or while Tailscale does not answer, a page can go without the label. Tailscale in userspace networking mode gets none, because it connects from `127.0.0.1`.

### The firewall of this computer

Other devices on your home network reach OwnGit only when it listens on a network address and the firewall of this computer lets them in. `owngit doctor` says which of these applies on this computer and prints the command for it, with the networks it found ([Checkup](#checkup)).

- **Windows**: Windows Firewall blocks other devices unless a rule allows them. `owngit service install` from an administrator account adds the rule `OwnGit` for private networks, so it is there before and after you change the listen address or update OwnGit. A network that Windows treats as public stays closed; mark your home network as Private in Windows Settings under Network & internet. When you start `owngit serve` yourself on the desktop, Windows may ask instead; allow private networks there. A standard account's service gets no rule, and an administrator can add one. Blocking all incoming connections in Windows Security keeps every device out.
- **macOS**: the application firewall is off unless you turned it on. When it is on, macOS may ask whether OwnGit may accept incoming connections; allow it. "Block all incoming connections" keeps every device out.
- **Linux**: when ufw or firewalld is on (Omarchy, for example, turns on ufw), allow OwnGit's port from your private network only, for example `sudo ufw allow from 192.168.1.0/24 to any port 7654 proto tcp`. A rule for the port alone would also let in every other network this computer joins.

### Host names

The server accepts only requests whose Host is an approved name, or `localhost`, `127.0.0.1` or `::1` from this computer; other Hosts get "unrecognized host". To use a LAN name for one run:

```sh
owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

`--allowed-host` is repeatable. To approve a name permanently, run this on the installation host and restart:

```sh
owngit approve-host gitbox.internal
```

### Network settings

To keep an address across restarts, save it. The server uses the saved values whenever it starts without options, as a service does. Run these on the installation host; they work whether or not the server runs, and a change applies at the next start:

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal
owngit network show
```

- `--listen` is `host:port`; an empty host, `0.0.0.0` or `::` listens on every interface.
- `--base-url` is the `http` or `https` origin other devices use, without a path. OwnGit accepts its host name and shows it in clone addresses; without it, clone addresses use the address the browser connected to.
- `--allowed-host` and `--remove-allowed-host` change the list that `owngit approve-host` also adds to.
- `--trusted-proxy` and `--remove-trusted-proxy` change the reverse proxies whose forwarded headers OwnGit believes, each an IP address or CIDR range (see [Behind a reverse proxy](#behind-a-reverse-proxy)).
- All options are repeatable. An empty value, such as `--base-url=`, removes that saved value.

`owngit serve` uses its option if given, then the saved value, then the default (`127.0.0.1:7654`). An option applies to that run only. `set` prints a note when the listen address leaves this computer (other devices then use plain HTTP) or when an `https` base URL has no trusted proxy.

`network show` lists the saved values, what a running server actually uses, and whether a restart is needed; `--json` prints the same as JSON. The Network tab of Settings shows the same, the saved value for the next start next to the value the running server uses. It changes them with the administrator password, and refuses a save when the settings changed after you opened the page.

A service definition that passes `--listen`, `--base-url`, `--allowed-host` or `--trusted-proxy` (the `ProgramArguments` of a LaunchAgent, the `ExecStart` of a unit) overrides the saved values at every start, so leave them out. The units that `owngit service install` writes never pass them. The Homebrew service runs `owngit serve --no-open`, so to reach it from other devices:

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654
brew services restart owngit
```

If a saved value locks you out, for example a listen address that no longer exists, reset it on the installation host and restart:

```sh
owngit network reset
```

`reset` removes the listen address and base URL. It keeps the allowed Hosts and trusted proxies unless you add `--clear-allowed-hosts` or `--clear-trusted-proxies`. No web page can do this.

Network settings belong to this installation host: an offline backup does not carry them, and a restored installation starts with the defaults. When you finish web setup from another device by a name that OwnGit accepts only for the current run, the setup form offers to save it as an allowed Host.

### Share on your tailnet over HTTPS

When Tailscale runs on the computer that runs OwnGit, OwnGit can ask it to answer HTTPS for this computer's Tailscale name and forward to OwnGit. Devices on your tailnet then open `https://NAME.TAILNET.ts.net/` and clone from `https://NAME.TAILNET.ts.net/git/project.git`, and the page header shows "Encrypted by Tailscale on this computer". Devices outside your tailnet cannot reach the address.

You need:

- Tailscale 1.50 or later, installed and signed in on this computer;
- MagicDNS and HTTPS Certificates turned on in the DNS page of the Tailscale admin console;
- on Linux, permission for your user to change Tailscale's settings, given once with `sudo tailscale set --operator=$USER`. OwnGit never runs `sudo`.

Then, on the Network tab of Settings, turn on "Share OwnGit on my tailnet" under "Share on your tailnet over HTTPS" and save with the administrator password. On the installation host you can use the command line instead:

```sh
owngit tailscale on
owngit tailscale status
owngit tailscale off
```

On the Settings page the change applies at once, without a restart. `owngit tailscale on` and `off` save the same change but cannot reach a running server, so they tell you to restart. `owngit tailscale status` says "on and ready", and the Settings page says "On. Encrypted by Tailscale on this computer.", only when the running server accepts the name and trusts `127.0.0.1` and Tailscale still has the address. Otherwise they say what is missing.

The first HTTPS connection after turning sharing on, or after renaming the computer, can take up to about a minute, because Tailscale gets the certificate when the address is first opened. The `owngit` commands, the MCP server and the runner wait up to 75 seconds for it.

When Tailscale issues the certificate, the names of this computer and your tailnet, such as `gitbox.tail0000.ts.net`, are recorded in a public Certificate Transparency log. Only the address is recorded, not your content. The Settings page shows this notice next to the switch, and `owngit tailscale on` prints it (`certificate_log` in `--json`).

#### What turning on does

1. It picks the HTTPS port: 443 if free, otherwise 8443, otherwise 10000, or the one you give with `--https-port PORT`. Whatever is on other ports stays as it is. If every port it would try is taken, or an address already points at OwnGit without OwnGit's record of making it, it changes nothing. It then shows what is on each port with the command that removes it; `owngit tailscale status` always shows these details, and the Settings page shows them only to an administrator. If your tailnet's access controls limit ports, allow the one OwnGit uses.
2. It adds the address to Tailscale's Serve settings, as `tailscale serve --bg --https=HTTPS_PORT http://127.0.0.1:PORT` would, and confirms that it points at OwnGit. Tailscale applies the change only if its Serve settings are still as OwnGit read them. If anything else changed them meanwhile, OwnGit changes nothing and asks you to try again.
3. It saves the HTTPS address as the base URL, the Tailscale name as an allowed Host and `127.0.0.1` as a trusted proxy, each only if not saved yet. Trusting `127.0.0.1` also trusts other programs on this computer that forward requests; see [Behind a reverse proxy](#behind-a-reverse-proxy).
4. It decides the listen address. Tailscale connects through `127.0.0.1`, so a listen address that accepts that, such as the default `127.0.0.1:7654` or `0.0.0.0:7654`, is kept. If OwnGit listens only on its Tailscale address, turning on saves `127.0.0.1:PORT` instead, and from the next start devices reach OwnGit only through the HTTPS address. A new listen address applies at the next start.

OwnGit never opens the home network on its own. Only the "Also allow on the home network (not encrypted)" checkbox, or `owngit tailscale on --home-network`, saves `0.0.0.0:PORT`, which counts as accepting plain HTTP. When the running OwnGit was started with `--listen`, that option decides where it listens, and the page says so.

#### Pages move to the HTTPS address

While sharing is on and ready, a browser that opens a dashboard page at the Tailscale name on OwnGit's own port, such as `http://NAME.TAILNET.ts.net:7654/settings`, goes to the same page at the HTTPS address. Only pages move: Git, the API, `/healthz`, setup, raw files and archives, forms and requests with a password are answered where they were sent. A page opened by an IP address, `localhost` or another name stays on plain HTTP, because that browser may not reach the Tailscale name. Browsers do not keep the redirect, so it stops within seconds once sharing is off or not ready.

#### Turning off

Turning off removes the Tailscale address only if it is still exactly as OwnGit made it, and again only if Tailscale's Serve settings are still as OwnGit read them. Otherwise it changes nothing, and the Settings page and `owngit tailscale status` show the `tailscale serve` steps that put the port back or clear it. It then restores the base URL saved before, removes the allowed Host and trusted proxy it added, and leaves the listen address as it is.

Turning off needs Tailscale to answer, so it is not offered while Tailscale is stopped, signed out or older than 1.50. On a page opened at the HTTPS address, turning off ends with a short page that gives OwnGit's address on this computer instead. To move sharing to another port, turn it off and on again with `--https-port`.

#### Details and troubleshooting

- If OwnGit was started with a `--base-url` option, that option still decides clone addresses; remove it and restart.
- If turning on was interrupted, the switch stays on, and saving it turns sharing on again.
- After a rename (in the admin console or with `tailscale set --hostname NAME`), turn sharing on again for the new name. The old name stays in the certificate log. Tailscale also keeps an address under the old name that answers for nothing, which the Settings page and `owngit tailscale status` show with the `tailscale serve` steps that remove it.
- With the Tailscale app for macOS (as opposed to Homebrew's `tailscaled`), Tailscale runs only while someone is logged in, so after a restart HTTPS works once someone logs in. Turn on automatic login, or use Homebrew's `tailscaled`, which runs without a login. The Settings page says so when it detects the app.
- `owngit serve --tailscale PATH` and `owngit tailscale --tailscale PATH` name a `tailscale` command that OwnGit does not find on its own. OwnGit does not run that command. It reads Tailscale's status and Serve settings where that command reaches Tailscale without options, so a `tailscaled` started with its own `--socket` is not supported.
- OwnGit reaches Tailscale through a Unix socket only when the system reports that the program listening on the socket runs as root, or on Synology DSM 7 as the Tailscale package's `tailscale` account. Linux, macOS and FreeBSD report this. On other systems, and for a socket that another account serves, OwnGit reports `untrusted_socket`.
- `--json` prints the report, or a failure with a code, as JSON.
- OwnGit never resets Tailscale Serve or turns on Funnel, and keeps every other Serve setting as it is. It refuses every request that carries the `Tailscale-Funnel-Request` header, so the address cannot be opened to the Internet through Funnel. It ignores `Tailscale-User-*` headers: passwords still decide who can read, write and administer.
- The sharing record belongs to this installation host, like the network settings, and an offline backup does not carry it.

### Other private networks

Over NetBird, Headscale with the Tailscale client, or plain WireGuard, let OwnGit listen on this computer's address in that network, use the name other devices use as the base URL, and restart:

```sh
owngit network set --listen 100.64.0.7:7654 --base-url http://gitbox.netbird.selfhosted:7654
```

OwnGit accepts the base URL's name and the listen address as Hosts; add other names with `--allowed-host NAME`. NetBird gives each device a name such as `gitbox.netbird.selfhosted`, and Headscale a name under the `base_domain` of its MagicDNS settings. Plain WireGuard gives none, so use the address or your own DNS.

These networks encrypt the traffic, but OwnGit cannot see that. Setup therefore still asks you to accept plain HTTP, and the header says "Not encrypted by OwnGit". Only a Tailscale client gives OwnGit what the "Encrypted by Tailscale" label needs, and the tailnet sharing switch works only with Tailscale. On Headscale the switch stays off and says that the control server offers no HTTPS certificates.

For an HTTPS address, run a reverse proxy on this computer that listens on the network address, and keep OwnGit on `127.0.0.1`. With Caddy:

```caddyfile
gitbox.netbird.selfhosted {
	bind 100.64.0.7
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

```sh
owngit network set --listen 127.0.0.1:7654 --base-url https://gitbox.netbird.selfhosted --trusted-proxy 127.0.0.1
```

Restart OwnGit after saving. `bind` keeps Caddy off the LAN address. `tls internal` signs the certificate with Caddy's own local authority, which each device must trust (see [Caddy](#caddy)); for a single command, pass the certificate instead, for example `curl --cacert root.crt` or `git -c http.sslCAInfo=root.crt clone`. Tested with NetBird 0.79, Headscale 0.29 and WireGuard on Linux.

### Behind a reverse proxy

A reverse proxy such as Caddy, nginx, Traefik or Nginx Proxy Manager can give OwnGit an HTTPS address at the root of its own host name, such as `https://git.example.internal`. A path below another site is not supported. Save the proxy's address and the HTTPS address, then restart:

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
owngit network show
```

Through the proxy, the page header then says "Encrypted by the proxy in front of OwnGit".

Until you tell OwnGit that the proxy is trusted, it treats every client as the proxy. Wrong passwords from one device then lock out every device for 15 minutes, cookies are not marked `Secure`, and forms sent over HTTPS fail the Origin check.

`--trusted-proxy` takes the address the proxy connects from:

- `127.0.0.1` when it runs on this computer;
- the container's Docker network range, such as `172.18.0.0/16`, when it runs in Docker here;
- the other computer's address when it runs there.

OwnGit trusts no proxy by default. It refuses ranges wider than `/8` for IPv4 or `/32` for IPv6, and the unspecified addresses `0.0.0.0` and `::`. A range trusts every computer in it, so keep it small. Trusting `127.0.0.1` also trusts every program on this computer that forwards requests, such as an `ssh -L` tunnel. `owngit serve --trusted-proxy ADDRESS` replaces the saved list for one run, and `--trusted-proxy ""` trusts none.

When the proxy runs on this computer, keep OwnGit on `127.0.0.1:7654` so that other devices reach it only through the proxy. A proxy in a container cannot reach `127.0.0.1` on the host unless it uses the host's network; otherwise let OwnGit listen on an address the proxy can reach, and trust that address. When OwnGit listens on a network address, other devices can also connect directly over plain HTTP. To stop that, let only the proxy reach OwnGit's port, for example with a firewall rule.

By default each Git request can send or receive up to 4 GB and take up to 30 minutes ([Git transfer limits](#git-transfer-limits)), so the proxy's limits must be at least as large as OwnGit's. Pushes and clones were tested through the four proxies below with these settings.

#### Headers OwnGit reads from a trusted proxy

From a trusted proxy, and only from one, OwnGit reads three headers. A repeated header, a list where one value belongs, or any other value is ignored, as is the `Forwarded` header.

- `X-Forwarded-Proto`, exactly `https` or `http`. With `https`, OwnGit marks its cookies `Secure`, checks forms against the `https` address, shows the connection as encrypted, skips the plain-HTTP acknowledgement, and tells Git that the request came over HTTPS.
- `X-Forwarded-For`, when its last entry (the one the proxy added) is an IP address. OwnGit uses it for password lockouts and the setup approval warning, so devices behind the proxy lock out separately. The proxy must add the address itself; a proxy that passes the client's header through lets clients choose their lockout address.
- `X-Forwarded-Host`, when OwnGit accepts both that Host and the request's own Host. The examples below pass the original Host instead.

#### Pages move to the base URL

Once the proxy has passed OwnGit an HTTPS request for the base URL, a browser that opens a dashboard page directly at the base URL's host on OwnGit's port, such as `http://git.example.internal:7654/`, goes to the same page at the base URL. Only pages move, as for [tailnet sharing](#share-on-your-tailnet-over-https).

- When the base URL itself is an IP address or `localhost`, such as `https://192.168.1.5`, a page opened at that address on OwnGit's port moves too. Pages opened by any other name or address stay on plain HTTP.
- OwnGit waits for such a request again after each start and each time tailnet sharing is turned on or off.
- If the proxy stops, pages opened this way keep moving to the base URL until OwnGit restarts or the base URL changes, so open OwnGit by another name or address meanwhile.
- Plain HTTP that the proxy passes on is left to the proxy.

#### Caddy

```caddyfile
git.example.internal {
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

`reverse_proxy` passes the original Host, sets `X-Forwarded-Proto`, sets `X-Forwarded-For` to the client's address, and has no size limit or timeout that would cut a long push. `tls internal` makes Caddy sign the certificate with its own local certificate authority, which each device must trust. Keep the line: without it, older Caddy releases, such as 2.6.2 in Debian 13, do not issue a local certificate for a name such as `git.example.internal`. With Caddy's Debian package, that authority's root certificate is `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`, readable by root and the `caddy` user.

#### nginx

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

- The size and timeouts match OwnGit's limits.
- The two buffering lines pass pushes, clones and archives through as they arrive, instead of storing them on disk first.
- `$proxy_add_x_forwarded_for` adds the client's address.
- The empty `X-Forwarded-Host` stops a client from sending its own.

If clients use a port other than 443, write `proxy_set_header Host $http_host;` so that the Host OwnGit sees matches the browser's address.

#### Traefik

Traefik passes the original Host and sets the forwarded headers itself. Its entry points, however, stop reading a request after 60 seconds by default, which cuts a long push with HTTP 504. Raise `readTimeout` in the static configuration; the router, service and certificate go in a dynamic configuration file:

```yaml
# /etc/traefik/traefik.yml (static configuration)
entryPoints:
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: 30m
providers:
  file:
    filename: /etc/traefik/dynamic.yml
```

```yaml
# /etc/traefik/dynamic.yml
http:
  routers:
    owngit:
      rule: Host(`git.example.internal`)
      entryPoints: [websecure]
      service: owngit
      tls: {}
  services:
    owngit:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:7654
tls:
  certificates:
    - certFile: /etc/ssl/git.example.internal.crt
      keyFile: /etc/ssl/git.example.internal.key
```

Start Traefik with `traefik --configFile=/etc/traefik/traefik.yml`. Tested with Traefik 3.7 from its release binary; in Docker, trust the address it connects from.

#### Nginx Proxy Manager

Create a proxy host with the scheme `http`, OwnGit's address and port, an SSL certificate and Force SSL on, and leave "Trust Upstream Forwarded Proto Headers" off. Then add these lines to Custom Nginx Configuration on the Advanced tab:

```nginx
client_max_body_size 4g;
proxy_request_buffering off;
proxy_read_timeout 30m;
proxy_send_timeout 30m;
set_real_ip_from 127.0.0.1;
```

By default Nginx Proxy Manager limits a request body to 2000 MB, waits 90 seconds for OwnGit, and stores large responses in temporary files. It also accepts an `X-Real-IP` header from any private address as the client's address, which would let a device on your network choose the address OwnGit uses for lockouts; `set_real_ip_from 127.0.0.1;` makes it report the address each device connects from.

The lines work with Websockets Support on or off. You can add `proxy_buffering off;`, but not `proxy_http_version`, which Nginx Proxy Manager already sets and which takes the host offline when doubled with Websockets Support on. Trust the address it connects from, as above. Tested with Nginx Proxy Manager 2.16.0 on Docker Engine on Linux with IPv4 clients; Docker Desktop, rootless Docker and IPv6 clients were not tested.

## New-release notice

After setup, OwnGit asks GitHub once a day whether a newer release exists. When one does, the dashboard shows a notice with links to the release notes and to [Install](../README.md#install). OwnGit never downloads or installs anything itself.

- A confirmed administrator also sees the command that updates this installation (see [Update and uninstall](#update-and-uninstall)) with a Copy button. The command holds this computer's paths, so everyone else is told to run `owngit update` on this computer or to confirm as administrator.
- Dismiss hides the notice for that version in the current browser.
- A failed check shows nothing and writes at most one log line.

The check is one HTTPS request to `https://api.github.com/repos/juliankang4/owngit/releases/latest` with a User-Agent that names OwnGit and its version, about 30 seconds after a start or right after setup. No repository data is sent; GitHub sees the server's address. Drafts and prereleases are ignored. Apart from [imports](#importing-from-another-git-host) and `owngit update` when you run it, this is the only connection OwnGit opens to another host.

Turn the check off on the General tab of Settings, under Update check, and save with the administrator password; the setting belongs to this installation host and is not in backups. For a deployment that must never check, start the server with `--no-update-check`, which wins over the saved setting:

```sh
owngit serve --no-update-check
```

## Host-owner recovery

Both procedures need access to the installation host; OwnGit has no email or account recovery.

Before setup is complete, a terminal on the installation host issues a replacement setup link. Add `--base-url http://127.0.0.1:7654` for a link on that address instead of the one the server listens on:

```sh
owngit setup-link --no-open
```

To reset a forgotten administrator password, put the new password in an owner-only file (see [Password and token files](#password-and-token-files)) and run:

```sh
owngit reset-admin --password-file /path/to/owner-only-password-file
```

Resetting ends every browser's administrator confirmation and leaves repositories unchanged. It keeps the [administrator password check](#administrator-password-check) choice, Do not ask included.

### Password and token files

Every command that reads a password or token file you wrote yourself, including `reset-admin`, `import`, `pr` and `repo`, requires a regular file that only your account can read. OwnGit never accepts a password as a command-line value. When it refuses a file, it says which accounts can also read it and gives the command that fixes it.

A password file holds the password on one line; one line break after it is fine. A file with more lines, or a password that is too short, is refused with a message that says so.

On macOS and Linux, create the file while `umask 077` is in effect, or fix it with `chmod 600 FILE`. On Windows, a file made with Notepad or `echo` inherits its folder's access entries, so in PowerShell create the file, limit it to your account, and only then write the password:

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

The commands work in Windows PowerShell 5.1 and PowerShell 7, in an ordinary window and in one opened with Run as administrator, where the Administrators group becomes the owner; OwnGit accepts that owner when only your account has access. `Read-Host -AsSecureString` keeps the password off the screen and out of the history.

## Repositories

### Restoring repository files

To bring back files from an earlier commit, start a restore from one of these places:

- the repository's Overview (Start a restore, under Restore files);
- a branch, tag or kept-history line (Restore files from here);
- a file page (Restore this file).

Choose a source commit and a target branch, preview the complete list of additions, changes and deletions, and apply. OwnGit adds a new commit on the target branch, or recreates a deleted branch at the selected commit, only if the branch still has the previewed tip.

A selected-file restore keeps unselected files, modes, binary files and symbolic links as they are, and never follows links on the host. It refuses a submodule, or a path whose replacement would remove unselected files beneath it. Restore changes only Git content in OwnGit, never another computer's working tree.

### Kept history

When a force push, an import or a deletion replaces the commits of a branch or tag, OwnGit keeps the previous commits as kept history, which you can browse and [restore](#restoring-repository-files). This is the default, and an administrator can turn it off:

- For every repository that follows the server: Overwritten and deleted history under Kept history on the Settings Repositories tab, or `owngit settings set --kept-history off`.
- For one repository: Kept history on the repository's Settings tab (Follow the server setting, Keep or Do not keep), or `owngit repo settings set --repository NAME --kept-history off` (`default` follows the server). A repository's own choice applies whatever the server's is.

A change applies to pushes and imports that start after you save; a run already going keeps the choices it started with. With Do not keep, the commits replaced from then on are not kept, so they are not listed in kept history and cannot be restored from it. They may still open by their full commit ID. History kept before stays and can still be restored; nothing is deleted. Fast-forward pushes, merges, restores, backups and repository deletion work the same either way.

`owngit repo settings show --repository NAME` prints a repository's choices as JSON, with `kept_history_now` for what it does now. Both `repo settings` commands need a `--password-file` holding the administrator password. Inside a clone of the repository they take `--server` and `--repository` from its `origin` remote, and the password file must then name that server on its first line ([Credential files and the server line](CODING_TOOLS.md#credential-files-and-the-server-line)). If a repository's saved choices cannot be read, pushes and imports to it are refused until you save them again; on the command line, give both `--kept-history` and `--protect-default-branch`. While the server-wide choice cannot be read, any change that leaves a repository following it is refused and saves nothing, including turning protection on or off. Choose Keep or Do not keep for the repository in the same change, or set the server choice again.

### Changing the default branch

The default branch is the one OwnGit and `git clone` open first (the repository's `HEAD`). An administrator picks any existing branch in the repository's Settings tab. An imported repository whose only branch is `master` shows no default branch until you choose one. Changing it creates no branch and leaves every ref and kept history as it was.

To keep the default branch from being rewritten or deleted, turn on Protect the default branch on the repository's Settings tab, or run `owngit repo settings set --repository NAME --protect-default-branch on`. It is off by default. While it is on, OwnGit refuses a push that is not a fast-forward of the default branch and a push that deletes it, and Git shows `remote: OwnGit protects the default branch main and refused ...` with `! [remote rejected]`. Pushes that add commits, every other branch and tag, merging a pull request, restoring files and deleting the repository work as before. An import still follows a fast-forward of the default branch. When the source rewrote it, the refresh fails with `protected_default_branch` and changes nothing; to follow the source, turn the protection off and refresh again. Changing the default branch moves the protection to the new one.

A new repository starts on `main`. To start new repositories on another branch, such as `trunk`, change Initial branch under New repositories on the Settings Repositories tab, or run `owngit settings set --initial-branch trunk`. The name uses up to 100 letters, digits, `-`, `_`, `.` and `/`, and must be one Git accepts. It applies to repositories created afterwards, in the dashboard, on the command line or through the API. Existing repositories keep their branches, and an import takes its source's default branch. The page of an empty repository shows the `git push` command for its branch.

### Deleting a repository

An administrator deletes a repository with Delete repository, at the end of the repository's tabs, by typing its name and, when asked, the administrator password. You choose what happens to the files:

- **Remove from OwnGit and keep the files** moves the bare repository, unchanged, to `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git` inside the repository folder (`ID` is the lowercase name, and the time is UTC). Its branches, tags and kept history stay there until you remove the folder yourself. Folders under `.owngit-removed` are never listed as repositories and are not in backups.
- **Delete the files too** deletes the bare repository, including its kept history. Earlier backups still contain it, and the database space its records used is freed but not securely erased.

Either way, deleting removes the repository's pull requests, reviews, tasks, check settings, jobs and results, helper and runner credentials, and import settings and credentials. Queued check jobs are dropped, and the name is free again.

To bring a kept repository back, create an empty repository with the same name in the dashboard. Then run the push command the dashboard shows for the kept folder, on the computer where OwnGit runs. For another name, use its URL:

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

Only branches and tags come back; kept history, pull requests and checks do not. If the kept repository's main branch is not `main`, change the default branch afterwards.

#### When deletion is refused

The reason decides the next step:

- An import is running, a check job is claimed or running, or another Git operation (push, clone, restore or merge) holds the repository: try again once it finishes.
- A check container still waits for OwnGit to confirm its removal: a cleanup that failed is retried when OwnGit starts, so restart OwnGit after Docker is available again.
- The server log says the container belongs to another Docker daemon, for example after Docker was reset or reinstalled: remove any leftover container labeled `com.owngit.check-job=JOB` on that daemon, or make sure that daemon no longer exists. Then release the record on the OwnGit computer:

```sh
owngit forget-check-container --job JOB --confirm-container-removed
```

`JOB` is the job identifier from the log; add `--state-dir` for a non-default state directory. The command removes no container. It refuses a job without a record, a record of the daemon running now (the next start of OwnGit cleans that up itself), and a job that has not finished.

#### If OwnGit stops during a deletion

The deletion still completes. OwnGit records the deletion before it moves or deletes the files, so the repository is already gone from the dashboard and Git URLs, and the name stays in use until the next start finishes the job. If the next start cannot finish it, the server log says why.

While a deletion is unfinished, a `.owngit-deletion-ID` file in the repository folder tells OwnGit that the right storage is mounted. Do not remove it. If you did, recreate it with the `token ...` line from the server log and restart.

### Moving an existing repository into OwnGit

Create an empty repository in the dashboard, then push branches and tags from a clone of the existing repository:

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

Compare both sides before you treat the move as complete:

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

A push that creates or updates a branch or tag is refused when its name, or any folder in its name, matches another ref apart from letter case, apart from how an accented letter or a Hangul syllable is encoded (as one character or as parts), or through letters that some file systems treat as equal, such as `ß` and `ss` or `ı` and `i`. Those file systems store such names, or such folders, in the same place, so one ref can overwrite or hide another. Examples are `Main` beside `main`, `Release/x` beside `release/main`, and `기본` written as jamo beside `기본` written as syllables. Names that differ in their letters are different names, so `cafe` and `café` can both exist. Git then shows `OwnGit refused changing refs/heads/NAME because another branch or tag, or one of its folders, has a name that some file systems treat as the same, ...` Use a clearly different name.

A repository can already hold two such names, for example after it was copied from a system that tells them apart. Pushes that create or update either one are refused until you delete one of them with `git push origin --delete NAME`. That deletion changes only the ref you name, and its last commit stays in kept history when kept history is on. The default branch cannot be deleted this way under any spelling: delete the other name, or choose another default branch first.

OwnGit accepts pushes only to `refs/heads/*` and `refs/tags/*`, so `git push --mirror` from another host's mirror clone fails for refs such as `refs/pull/*`. Pushing between two OwnGit installations carries neither kept history nor repository records; use an [offline backup](#offline-backups) for those. To keep pulling from a host that stays in use, see [Importing from another Git host](#importing-from-another-git-host).

### Keeping a copy on another host

OwnGit does not push to other hosts itself. To copy every branch and tag elsewhere, work from a mirror clone:

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

To update the copy, run `git fetch --prune` and `git push --mirror` again in the same directory. `--mirror` makes the other host match the copy exactly, including deletions; kept history stays in OwnGit.

To update both hosts with every push from a working clone, give its remote two push URLs:

```sh
git remote set-url --add --push origin http://HOST:7654/git/PROJECT.git
git remote set-url --add --push origin https://git.example.test/team/project.git
```

Git then pushes only to the push URLs, so list OwnGit as well. Fetches still use the original URL, and a rejection by one host does not undo the push to the other.

### Downloading an archive

The Code tab offers the selected branch or tag as a ZIP or tar.gz file, and a commit page offers that commit. Downloading needs the same access as the Code tab.

The archive holds the files of that revision, without history, in one folder named `PROJECT-REF`, such as `project-main`. Like `git archive`, it follows the `export-ignore` and `export-subst` attributes of that revision. Characters other than letters, digits, `.`, `-` and `_` become `-`, so `feature/login` gives `project-feature-login.zip`.

Without a browser, use the API route:

- `ref` is a branch, tag or full commit ID; the default branch is used when it is left out.
- `format` is `zip` or `tar.gz`; an unknown value answers 404.
- With shared-password protection, `--user owngit` makes curl ask for the password.

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

With `--remote-header-name`, curl uses the plain ASCII name that OwnGit sends for clients that cannot read the full name. When the name has other letters, such as Korean, that ASCII name is the repository name and the first 12 characters of the commit ID, for example `project-1a2b3c4d5e6f.zip`. To save under the full name, give it with `--output`, and let `--data-urlencode` encode the ref:

```sh
curl --fail --get --user owngit \
  --data-urlencode 'ref=기능/로그인' --data format=zip \
  --output 'project-기능-로그인.zip' \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive'
```

An archive download counts as a Git transfer with the [limits below](#git-transfer-limits). When Git fails, a limit is reached, or OwnGit stops before the end, OwnGit closes the connection without finishing the response. The download then fails (curl reports `(18) transfer closed with outstanding read data remaining`), and the part received is not a valid archive.

### Repositories being prepared

When `owngit serve` starts, it prepares each repository: safety settings, the retention hook, and unfinished pull request work. It prepares up to 8 at a time and waits at most 10 seconds before it starts serving; the rest are served as soon as they are ready. A repository whose folder cannot be read later, for example because its share is not mounted, is locked and prepared again in the same way.

While a repository is locked:

- Git gets HTTP 503 with `repository is being prepared; try again later`;
- its pages and the API (`repository_preparing`) say so, and the dashboard shows a Preparing label;
- scheduled imports and checks for it wait;
- an administrator can still delete it.

OwnGit retries 30 seconds after a failed attempt, then after twice the previous wait, up to 10 minutes, and every 5 seconds when the folder could not be read. The server log names the cause. Fix it (an unmounted disk, permissions, or a conflicting Git setting the log quotes) and wait for the retry, or restart OwnGit.

A repository whose folder can be read but whose Git data cannot is listed with an Unreadable label instead, and Git reports the error itself. If OwnGit cannot read the list of repositories from its state database, it refuses to start.

## Importing from another Git host

An import copies a repository from another Git host over HTTPS into a new OwnGit repository, and can refresh it later, on demand or on a schedule. Imports are inbound only: OwnGit never writes to the source, and Git LFS objects are not fetched or hosted.

In the browser, an administrator uses Import a repository on the dashboard to start one. The repository's Import tab then changes its source and credentials, refreshes, cancels, and sets a schedule.

- Anyone who can read the repository sees the tab's status, run history and ref states. The source address, credential state and run messages are for administrators only.
- Changes ask for the administrator password as set under [Administrator password check](#administrator-password-check).
- The credential form changes only what you enter. A new token or Basic credential keeps a stored CA, and **No new sign-in (CA only)** keeps the credential; only Clear credentials removes them.
- The form is limited to 1 MiB, so store a CA bundle near that size with the command line.

Each import records a mode: **Standalone** (the copy is the primary one) or **Coexistence** (the other host stays authoritative). The mode is only a label; both follow the same [refresh rules](#what-an-import-publishes).

### Imports on the command line

The command line offers the same operations, except changing the source URL or options of an existing import. It reads the administrator password from a file with the same checks as `reset-admin`, and a source token or Basic credential from a private file or a prompt, never from an argument or environment variable:

```sh
owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

Every import command takes the same `--server`, `--accept-insecure-http` and `--password-file` flags, omitted below. `--accept-insecure-http` is consent to reach OwnGit over plain HTTP for that command; the source itself must use HTTPS.

```sh
owngit import refresh PROJECT
owngit import status PROJECT
owngit import history PROJECT --limit 20
owngit import cancel PROJECT
owngit import schedule PROJECT --enable --interval 6h
owngit import schedule PROJECT --disable
owngit import credentials PROJECT --token-file /path/to/owner-only-token
owngit import credentials PROJECT --ca-file /path/to/source-ca.pem
owngit import credentials PROJECT --clear
owngit import resolve PROJECT
```

- `--basic-file` replaces `--token-file` for a Basic credential (username and password on separate lines). `--ca-file` stores a source certificate authority, up to 1 MiB. `import credentials` changes only what you pass; `--clear` removes credential and CA. Output shows the credential type and whether one is stored, never the secret.
- `--allow-private-network` permits a source on a private LAN, CGNAT, tailnet or loopback address. `--git-only-consent` accepts a repository with Git LFS pointers ([Git LFS](#git-lfs)).
- `import add` creates the repository and refuses an existing name with `repository_taken`; `import refresh` updates from the stored source. Both wait for the whole run (up to about 62 minutes by default). They exit 0 on success, 3 when the run kept local refs that differ from the source (listed in the output), 130 when it was cancelled, and 1 otherwise.
- `import status` lists the last and active runs and every branch or tag that does not match the source.
- `import cancel` can stop a run only until its result is published; for a first import, that is the moment the repository appears. If the first import fails or is cancelled, OwnGit removes the source and credentials stored for that name (at its next start if it crashed), and a retry uses only what you supply.
- A schedule interval is between 60 seconds and 7 days (`invalid_schedule` otherwise), and scheduled refreshes run only while `owngit serve` runs.

### Source connections

The source URL must use HTTPS with TLS 1.2 or newer, an ASCII host name, and no username, password, query or fragment. IPv6 zone identifiers are not supported. OwnGit does not follow redirects, and it ignores proxy environment variables, cookies and Git credential helpers.

OwnGit resolves the host name once and checks every address. Public addresses are allowed; private LAN, CGNAT, tailnet and loopback addresses need `--allow-private-network`; other special-purpose addresses are refused. A custom CA adds to the system roots and never disables certificate or host name checks; a run that fails on the certificate says so.

### What an import publishes

Each run fetches a full copy into a private staging area. Before anything reaches the repository, it checks that every advertised branch, tag and HEAD is present with a complete object graph.

- Only branches and tags are published. Notes, replace refs, pull request refs and a HEAD outside `refs/heads/` are skipped or refused.
- Hooks and configuration are not copied.
- A source with another object format (SHA-1 or SHA-256) fails, and a ref name longer than 417 bytes fails with `unsupported_refs`.
- OwnGit asks the source for Git protocol v2, which lets it list only HEAD, branches and tags, so pull request refs are never fetched or counted. A source that lists more than 50,000 refs fails with `too_many_refs`. A source without protocol v2 lists every ref, and the refs OwnGit skips, such as pull request refs, then count too. For such a source, [move it by hand](#moving-an-existing-repository-into-owngit) with a clone that pushes its branches and tags.

A refresh never overwrites local work:

- A missing ref is created, and an identical one is left alone.
- A branch follows the source only while it still holds the value OwnGit last saw from this source URL, or moved forward from it and the new source value includes it.
- A tag changes only while it is still the exact tag last seen.
- Anything else is divergent and kept, and the run reports it.
- A ref deleted at the source is never removed locally (**Deleted at source** in the Import tab and `import status`).
- A source ref whose name, or any folder in its name, matches a local ref in the way described under [Moving an existing repository into OwnGit](#moving-an-existing-repository-into-owngit) is reported as divergent instead of created, and HEAD does not follow the source to such a name. `cafe` and `café` are different names.
- A replaced value stays in kept history when kept history was on for the repository as the refresh started; with Do not keep it is not kept.
- HEAD follows the source only when OwnGit set it on an earlier import from the same source and nothing changed it since.

After you change the source URL, OwnGit has not yet seen the new source's refs, so refs that differ are reported as divergent instead of being replaced.

### Git LFS

OwnGit scans the fetched objects for LFS pointer files, up to 200,000 objects, 100,000 candidate files and 32 MiB of candidate content. If it finds one, or cannot finish within those limits, the run stops with `git_lfs_required`. With Git-only consent the import proceeds, keeps the pointer files as they are, and marks the content incomplete. OwnGit does not read `.gitattributes`, so a clean scan does not prove that a repository uses no LFS.

### Failures and cancellation

One run per repository is active at a time (`busy` otherwise), and a run is limited to 60 minutes by default (`limit`). Other outcomes are `cancelled`, `protected_default_branch` (see [Changing the default branch](#changing-the-default-branch)), `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved` and `nothing_to_resolve`. A failure OwnGit did not classify is `unclassified`; `unsupported` means the source or destination uses a feature that import does not support.

When `owngit serve` stops, it cancels running imports and waits up to 45 seconds for each to record its outcome. At the next start it marks interrupted runs and checks any publication that was in progress, without repeating or rolling back a write. If the import service cannot start, the Import tab and `import status` say so, and Git keeps working.

### Unresolved publications

A publication is unresolved when OwnGit cannot prove how it ended, for example when refs were written and HEAD was not. Refreshes are refused until you accept the repository as it is:

1. Check its branches, tags and HEAD, and the reason on the last run.
2. Fix what you do not want to keep with ordinary Git.
3. Make sure no import is running.
4. Run `owngit import resolve PROJECT`, or use the button on the Import tab.

OwnGit records the current refs and HEAD as the accepted state without writing to Git. The next refresh then follows the source where the refs still hold the last confirmed value, and keeps the rest as divergent.

If an initial import is unresolved and its repository does not exist yet, restart OwnGit. If the problem remains, move that import's `.owngit-create-*` directory out of the repository folder and restart again.

## Command-line pull requests

A push does not create a pull request. After pushing distinct source and target branches, create one (`--body-file` and `--review` are optional):

```sh
owngit pr create \
  --server http://HOST:7654 \
  --accept-insecure-http \
  --repository PROJECT \
  --source feature-branch \
  --target main \
  --title "Describe the change" \
  --body-file description.md \
  --review request \
  --password-file /path/to/owner-only-shared-password-file
```

- `--body-file` reads the Markdown description from a file, or from standard input when it is `-`.
- `--review skip` records that review was intentionally skipped, not approved. Without `--review`, `pr review request` can still be run later.
- `--password-file` holds the shared general-access password, never the administrator password, with the same owner-only checks as `reset-admin`. Omit it when access is open.
- `--accept-insecure-http` records your consent to plain HTTP for that command only.
- The CLI rejects credentials embedded in the URL and does not follow redirects.
- Inside a clone of an OwnGit repository, `--server` and `--repository` come from the clone's `origin` remote, and a password file is then sent only when its first line names that server ([Inside a clone](CODING_TOOLS.md#inside-a-clone), [Credential files and the server line](CODING_TOOLS.md#credential-files-and-the-server-line)).

The other commands take the same `--server`, `--accept-insecure-http`, `--repository` and `--password-file` flags. `pr show` reports the current source and target object IDs, and every review decision and merge must supply both:

```sh
owngit pr list
owngit pr show --number 1
owngit pr edit --number 1 --edit-revision 0 --title "New title" --body-file description.md
owngit pr diff --number 1
owngit pr review request --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr review submit --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID \
  --decision approved --reviewer "existing-tool: reviewer label" --note-file note.md
owngit pr review skip --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr merge --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr close --number 1
owngit pr reopen --number 1
```

### Pull request rules

- Only one pull request can be open per source and target pair; a second is refused with `pull_request_exists`, and `error.details.number` names the open one.
- Closing (`pr close`, or Close pull request on the page) changes no branch, keeps the history, and frees the pair. `pr reopen` opens it again unless another one is open for the pair. A merged pull request cannot be closed or reopened (`pull_request_merged`). Closing and reopening need the same access as merging.
- A description and a review note are Markdown of at most 64 KiB; the page shows them without HTML or images. `pr show` includes the description (`body`) and the five newest review notes, and `pr list` leaves both out.
- `pr edit` takes the `edit_revision` that `pr show` printed and changes what you pass: `--title`, `--body-file`, or both. If someone edited the pull request since, nothing changes and the command fails with `stale_edit`, with the current revision in `error.details.current_edit_revision`; show it again and reapply your change. Edit title and description on the page works the same way and keeps your text in the form. Editing is possible in every state, including after a merge, and changes no branch, review, check or merge record.
- A review is `approved` or `changes_requested`; the reviewer label records who supplied it and does not claim independence. A pending or changes-requested review does not hold a merge. When a branch moves, earlier decisions no longer apply; inspect again and decide for the new object IDs. A review can carry a note (`--note-file`, or Record a review on the page) that stays with the two commits it reviewed. After either branch moves, the note is still shown, marked as about earlier commits.
- Results name the access that made a change in `created_by`, `edited_by`, `merged_by`, `review.actor` (the current review, request or skip) and each note's `actor`. OwnGit records what authorized the request, which is general access today, and never takes it from a Git commit author. A field is left out when OwnGit recorded nobody: for changes made before OwnGit 1.1.3, and for a merge OwnGit finished on its own after an interruption.
- `pr diff` prints what the pull request changes with the object IDs it compared (`--stat` without the patch, `--patch` only the patch); see [Pull request changes](CODING_TOOLS.md#pull-request-changes). The changes are what the source changed since it branched off the target, from the merge base to the source. When the branches share no commit or have more than one merge base, the page says so and shows no change list.
- Every command writes a JSON result. Failures carry a stable `error.code` and a nonzero exit status, and `connection_failed` names the cause. `checks` in a pull request result reports the evidence for the current source revision, or `absent`. It is `stale` when other checks ran than those in the `.owngit/checks.json` of that revision. Checks are advisory and never block a merge.
- Merge makes a fast-forward, or a merge commit with the old target as first parent, authored as `OwnGit <owngit@localhost>` with the number and title in the message. It never squashes, rebases, force-updates or deletes the source branch, and a retried or interrupted merge never creates a second commit. When the target already contains the source, the pull request is recorded as merged with `merge.mode` `up_to_date` and no new commit. Merge needs Git 2.38 or newer on the OwnGit host (`unsupported_git` otherwise).

## Project checks

OwnGit records checks that a helper runs in your own environment, and runs automatic checks that the owner enabled.

- A manual helper inherits your environment and permissions and is not a sandbox. OwnGit records the worktree state with every attempt and never reports a dirty or unknown worktree as a tested commit.
- Automatic checks run as the OwnGit account, in a restricted local Docker container, or on a separately connected runner. They need a committed `.owngit/checks.json`, an owner policy and current consent ([Automatic checks](AUTOMATIC_CHECKS.md)).

Create a repository-scoped helper credential with the administrator password. The token is written only to the owner-readable `--output` file and stored on the server as a hash:

```sh
owngit helper-credential create \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --label laptop \
  --password-file /path/to/admin-password-file \
  --output ~/.owngit-helper-token
```

- An existing file or link at `--output` is reported, not replaced, and a file left by a failed creation stays for you to inspect.
- If the response is lost, the command revokes the new credential, or prints its creation identity (never the token) so you can.
- `helper-credential list` and `helper-credential revoke --id ID` manage credentials, and the Helper credentials link on the repository's Checks tab does the same in the browser. A revoked token stops working at once.
- The commands always ask for the administrator password; in the browser, issuing and revoking follow the [administrator password check](#administrator-password-check).

Then create a stable task and run checks:

```sh
owngit check task new \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --title "Fix the failing build"

owngit check run \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --task TASK_ID --check "unit=go test ./..." --check "lint=go vet ./..."
```

[Coding tools](CODING_TOOLS.md) is the reference for these commands, correction rounds, result fields and exit codes. On Windows, `cmd.exe` returns exit code 1 for an unknown command, so OwnGit records that as `failed`.

### Raw check logs

Raw check logs are stored in `owngit.sqlite`, limited to 256 KiB each, and kept for 30 days by default, counted from when the check started. To keep them for 7 days, 30 days, 90 days, 1 year or indefinitely, choose Keep raw logs under Raw check logs on the Settings Storage & recovery tab, or run `owngit settings set --check-logs 90d`.

- A new choice applies at once to every raw log kept, and saving it does not rewrite them. A log it no longer keeps cannot be opened, and the next cleanup, when OwnGit starts or a check result arrives, deletes it.
- A shorter time makes older logs unavailable; a longer one uses more space in the state database.
- While logs are kept indefinitely they have no removal date, so the API leaves out `log_expires_at` and `owngit check log` leaves out `expires_at`.
- While the saved choice cannot be read, the API leaves the date out as well, `check log` names the setting to set again, and the dashboard says the log could not be read.
- Task and attempt records, results and excerpts stay after a log expires (`log_expired`). A log missing earlier returns `log_missing`.
- If the database is full while a result is stored, or the saved raw log retention cannot be read, OwnGit keeps the result without its log and records a log error on the attempt that says why.

## Git transfer limits

- Each Git request (clone, fetch, push, archive) can receive at most 4 GB, send at most 4 GB, and must finish within 30 minutes. To change these limits, use Largest transfer and Longest transfer under Git transfers on the Settings Repositories tab, or run `owngit settings set --transfer-size 8GB --transfer-time 2h`. The size goes from 1 MB to 64 GB (1 GB is 1024 MB), and the time from 1 minute to 24 hours. A change applies to transfers that start afterwards. Higher limits let large or slow transfers keep the server busy for longer and use more disk space.
- A push over the size is refused with HTTP 413, which Git may show only as `fatal: the remote end hung up unexpectedly`. A clone or fetch over a limit is cut off. A transfer whose client moves no data for 60 seconds is stopped too. OwnGit does not host Git LFS, so a repository whose history is larger than the size limit cannot be cloned through it; keep large binary files out of Git history.
- At most 5 Git requests run at once, and one repository can use at most 4 of them, so one repository's slow transfers never block the others. A request that finds no free place waits up to 90 seconds, then gets HTTP 503 with `Git service is busy with other transfers; try again shortly`; run the command again.
- With shared-password protection, 4 wrong passwords from one address within 10 minutes block that address for 15 minutes; correct passwords never count. During the block Git gets HTTP 429 (Too Many Requests) instead of the authentication failure a wrong password gets, so Git keeps the password its credential helper saved. The administrator password has the same limit, counted separately.
- When OwnGit is stopped, it waits up to 10 seconds for running requests, then ends the rest and logs how many it ended.

## Storage

OwnGit keeps its own records in the state directory and the Git data in the repository folder. Both stay when you remove the program; delete them yourself when you no longer need them.

### State directory

The state directory is the platform config directory joined with `owngit`, or `~/.owngit` when there is none. It holds:

- `owngit.sqlite`, with `-wal` and `-shm` files while in use;
- the import credentials in `import-credentials/NAME.json`, unencrypted and restricted to the OwnGit account. Protect the directory like the credentials.

Keep the state directory on a local disk, never on a share used by other computers. Whoever serves a shared filesystem could read and change the accounts and credentials in it, or put another state in its place. So OwnGit refuses a state directory that is, or is reached through a folder that is, on any of these:

- a network share, including Windows UNC paths;
- a FUSE or 9P filesystem;
- a virtual machine's shared folder, including a Windows drive under WSL such as `/mnt/c` (a folder in WSL's Linux filesystem works instead) and a Docker Desktop bind mount (a named volume works instead).

More rules for the way to the state directory:

- On Unix, OwnGit refuses a state directory that another account could replace through its owner or a parent folder. Use the printed `chmod` command, or another location.
- OwnGit follows no link that such a filesystem holds on the way to the state directory or a `--log-file` folder, so a link inside a network home folder cannot be on that way; use the real path instead.
- Run as root, or as an elevated administrator on Windows, OwnGit uses no folder on such a filesystem, not even for its log. Another account may keep its log on a share.
- On Linux, OwnGit recognizes these filesystems from a list of known types and treats any other type as local.
- On Windows, it also refuses a link, a junction or a folder where a volume is mounted on the way; reach a mounted disk through its drive letter.
- On macOS, every folder on the way must be readable by the account that runs OwnGit.

When OwnGit refuses the state directory itself, it cannot record why there. `owngit service install` then only says that OwnGit did not answer and points to the log, which shows the reason, as `owngit serve` in a terminal does.

### Repository folder

The repository folder, chosen during setup, can be on another disk or a mounted SMB or NFS share, with one OwnGit writer at a time. OwnGit leaves existing files alone and creates bare repositories ending in `.git` there. While it serves, it keeps a `.owngit-serve.lock` file locked in that folder, so a second server on the same folder, for example from a copy of the state directory, refuses to start. On a network share, this detection depends on the share's file locking.

- Repository names cannot end in `.git` or use Windows device names such as `CON`, `AUX`, `NUL`, `COM1` or `LPT1`, with or without an extension, on any platform. `new` and `new-import` are reserved.
- New repositories are written under a temporary `.owngit-create-*` name and renamed into place. On Windows, antivirus or search indexing can briefly lock the new directory, and OwnGit retries for about 2 seconds.
- Deleted repositories use `.owngit-delete-*`, `.owngit-removed` and `.owngit-deletion-*` names ([Deleting a repository](#deleting-a-repository)).

### Busy repositories and caching

When a Git operation holds a repository, the dashboard waits at most a second, then shows the branch list it read last or lists the repository as In use. The repository's own pages wait until shortly before the request deadline and then answer HTTP 503 with `Retry-After`.

OwnGit remembers each repository's branches and tags between its own writes. It keeps recently read listings, files up to 4 MiB, diffs and pull request comparisons in memory (up to 64 MiB), so a page opened again starts no Git process. Refs changed directly in the repository folder show after OwnGit next changes a ref in that repository or restarts. Activity counts are computed in the background at start and after a branch changes; on a slow share the dashboard says when some repositories are still being counted.

### Maintenance

OwnGit maintains each repository while nobody uses it. After five minutes without a push or request following a change, it packs loose refs and objects and updates the commit-graph. Between 03:00 and 05:00 local time it also combines the packs of a repository that has more than 20. Maintenance never deletes objects or kept history. It holds the repository only while a step runs, so a push or page that arrives then waits for that step, and it logs one line per run. At start, OwnGit removes temporary pack and lock files that an interrupted Git command left behind and names them in the log.

### State database upgrades

On start, OwnGit upgrades a database from any earlier release in place, in one transaction, after [backing it up](#backup-before-an-upgrade). It logs one line such as `state database upgraded from schema 15 to 16`, on standard error for offline commands such as `backup`.

- OwnGit refuses a database from a newer version, and one whose tables, indexes, triggers or views differ from the ones OwnGit creates at the schema version it records. It leaves the files of such a database unchanged.
- An older build refuses a database that a newer build has upgraded. To go back, restore the backup made before the upgrade with the earlier version.
- When the database was changed outside OwnGit, undo that change, or restore a backup of the state with the OwnGit version that made the backup.
- Expired logs and deleted records free space inside the database for reuse, but the file does not shrink and the old bytes are not securely erased. There is no overall size limit, and OwnGit does not run `VACUUM`.
- When a `-wal` or `-shm` file is present at start, OwnGit copies the database to a private temporary directory to inspect it, so the temporary volume needs that much free space.

## Offline backups

OwnGit does not schedule backups; make them with `owngit backup`. Kept history protects against force-pushes and deletions, but it is not a backup.

Stop OwnGit before creating a backup. The output directory must not exist:

```sh
owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

A secret that was ever pushed stays in the repository's Git data, even after a force-push or branch deletion and even with kept history off, and anyone who can read the repository and has its commit ID can still open it in the browser. While the repository keeps history, it also stays in kept history and in every later backup. Only [deleting the repository](#deleting-a-repository) with its files removes it, and earlier backups still contain it. Rotate any secret you push by mistake.

### What a backup holds

A backup holds a manifest and one Git bundle per nonempty repository. Together they carry:

- every ref including kept history, and each repository's HEAD and metadata;
- each repository's own kept history choice and default branch protection;
- pull requests, reviews and merge records;
- tasks, check configurations and results, and automatic-check policies and jobs;
- import sources and history;
- the access mode and password hashes. Keep backups private, because password hashes are sensitive.

A backup does not include raw logs, credentials and tokens of every kind, import schedules, consent, the [update check](#new-release-notice) setting, or the other server-wide settings (the sign-in length, the initial branch, the kept history choice, the Git transfer limits and the raw log retention).

A backup holds up to 1 GiB of OwnGit records, counted by the memory they take and not counting the repositories; backup refuses a larger state. Creating and restoring a backup hold its records in memory, so more records need more memory.

Backup refuses to run while an import publication is unsettled for a repository that does not exist yet. Start and stop OwnGit once; if the error remains, first move that import's `.owngit-create-*` directory out of the repository folder.

### Backup versions

OwnGit restores backup versions 1, 2, 9, 10 and 11 and refuses others; older builds refuse a newer backup instead of dropping records. A backup is written in version 10, which OwnGit 1.0.3 to 1.1.2 restore, unless it holds records that only version 11 can hold, such as a repository's own kept history choice or default branch protection, or its manifest would pass the 64 MiB that version 10 allows. So a backup of an installation that uses nothing new, including every [backup before an upgrade](#backup-before-an-upgrade), can still be restored with the earlier version.

### Restoring a backup

Restore into new paths that do not exist:

```sh
owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

Restore checks every bundle, ref, object and record before it publishes the new state, runs `git fsck` on each repository, and names every repository that fails. The SHA-256 hashes detect corruption, not a backup that someone replaced along with its manifest.

- Restore copies each bundle into the new repository folder as it checks it, and restores from that copy. Before it starts, the disk of the repository folder needs room for all bundles and the largest one once more; otherwise restore stops and says how much it needs.
- Ctrl+C stops a restore, which then removes what it made, says that nothing was restored and exits with status 130. If both folders are already in place, it completes instead.
- Git file names are kept exactly, so a name Git accepts but Windows does not, such as one with a backslash, may not check out there.

After a restore, start `owngit serve` with the restored state before you use it in other ways, so that startup can settle interrupted records. Then set up again what a backup does not carry:

- Sessions, setup links, approved Hosts, network settings, credentials, schedules and every consent are gone. Sign in again, create new helper and runner credentials, store import credentials again, and enable automatic checks again. Unfinished jobs are marked `interrupted`.
- Raw logs are absent, and unsettled import publications are closed without being applied.
- Server-wide settings start at their defaults, as on a new installation, whether the backed-up installation chose stricter or looser ones: a sign-in lasts 12 hours, new repositories start on `main`, repositories that follow the server keep overwritten and deleted history, a Git transfer may move 4 GB and take 30 minutes, raw check logs are kept 30 days, and the administrator password check and the new release check are back at their defaults. `owngit restore` lists them when it finishes; set them again under Settings or with `owngit settings set`.

### When a backup or restore is interrupted

If a restore stops without its own cleanup, for example after a power loss or when its process is killed:

1. Do not start OwnGit from either target, and do not remove a `.owngit-restore-pending` marker.
2. Move both targets and any `TARGET.owngit-restore-...` siblings to a quarantine location.
3. Restore again into new paths.

If a backup stops before finishing, its output directory does not exist. Once no backup process is running, keep or quarantine its hidden `.OUTPUT.owngit-backup-...` sibling.

### Where backups can be written

Backup and restore make each new folder under a temporary name beside its final place and then rename it. The folder that holds it must therefore be one where no other account can rename or remove what OwnGit puts there.

- On macOS and Linux, that folder and every folder on the way to it must be ones that no other account can change, as on the way to the state directory; a sticky folder such as `/tmp` is accepted. Otherwise OwnGit stops before it creates anything, names the folder, and gives the `chmod` command that fixes it when there is one. You can also choose a folder that only this account can change.
- Missing folders on the way are created, private to this account, but only inside a folder where no other account can create names, so not directly in `/tmp`.
- On Windows, OwnGit follows no link, junction or mounted volume on the way, and keeps the folders on the way from being renamed while it works.
- The backup and the restored repository folder may be on a network share, except when OwnGit runs as root or as an elevated administrator on Windows. The restored state directory must be on a local disk, as every state directory must.

### Verifying a backup

`owngit backup verify` shows whether a backup restores, without touching the backup or the state in use. OwnGit can keep running meanwhile:

```sh
owngit backup verify /path/to/backup
```

A backup is verified only when every check passed and the rehearsal folder was removed. The command exits with status 1 unless the backup is verified, and with 130 when it was stopped. `owngit restore --verify` makes the same rehearsal first and restores only a verified backup, so a damaged backup is refused before anything is created at the targets.

The command restores the backup, with every check that `owngit restore` makes, into a new folder private to this account in the system's temporary folder. It removes that folder afterwards, also when you stop it with Ctrl+C.

- For each repository, the bundle must match its SHA-256 hash, Git must restore it with exactly the refs and HEAD that the backup records, and `git fsck` must find every object those refs reach.
- The restored database must have the schema this version creates and pass SQLite's integrity and foreign key checks.
- Every backup version that restore accepts gets the same checks.
- The command reads only the backup, so it needs no access to OwnGit's state and runs as the account that starts it.

The output lists each repository as passed, failed with the reason, or not run because the check stopped before it. Then it gives the database result and what the check cannot show: the SHA-256 hashes detect damage, not a backup that someone replaced along with its manifest.

The rehearsal needs room for the repositories, as a restore does. When the temporary folder's disk has less, the command stops before it starts and says how much it needs; on some Linux systems the temporary folder is kept in memory. `--temp-dir DIR` puts the rehearsal in another folder. Running out of room during the rehearsal is reported as such, never as a damaged backup.

`--json` prints the result as JSON with these fields: `backup`, `verified`, `error`, `version`, `created_at`, `repositories` (each with `id`, `status` as `passed`, `failed` or `not_run`, `refs` and `error`), `database`, `limits`, `cleanup_error` (the rehearsal folder could not be removed) and `leftovers`.

A rehearsal folder stays only when OwnGit could not remove it, for example after a power loss. The next verification in that temporary folder lists such `owngit-verify-...` folders of this account under `leftovers`; remove them once no verification runs.

### Backup before an upgrade

When a newer OwnGit starts on a state whose schema is older than the one it writes, it first makes an offline backup of the state as it is. It upgrades the state only when that backup is complete. `owngit backup` does the same before its own backup. Other commands, which also work beside a running server, leave an older state alone and say to start or restart the newer OwnGit once, or to run `owngit backup`, first.

- The backup is a new folder, such as `pre-1.1.3-20260929T101500Z`, in a folder beside the state directory named after it with `-backups`, for example `~/.config/owngit-backups` beside `~/.config/owngit`.
- It holds every repository, so that disk needs room for them.
- The server log, or standard error for `owngit backup`, says where the backup is and gives the command that restores it. `owngit-upgrade-backup.txt` in the backup says the same.
- A new state, and one whose setup is not complete, need no backup.
- OwnGit 1.0.3 and later restore it.

OwnGit creates the `-backups` folder so that only this account can use it. An existing one must belong to this account, and no other account may be able to change what is in it; otherwise OwnGit refuses the upgrade and says why.

To go back to the earlier version, stop OwnGit, move the state directory aside, and run the printed command with the earlier version, for example:

```sh
owngit restore \
  --input ~/.config/owngit-backups/pre-1.1.3-20260929T101500Z \
  --state-dir ~/.config/owngit \
  --repository-root ~/.config/owngit-backups/pre-1.1.3-20260929T101500Z-repositories
```

Then start the earlier version. The restored repositories, as they were at the upgrade, are in the new folder beside the backup; another new folder in a place this account can create works as well. While the earlier version uses the restored repositories there, keep the `-backups` folder.

#### When the upgrade backup fails

When the backup cannot be made, for example because the disk is full, the folder cannot be created or the repository folder is not available, OwnGit does not upgrade the state and stops with the reason. The earlier version can still use the state. Fix the cause and start OwnGit again.

If OwnGit stops while it makes the backup, the state is not upgraded. Once no OwnGit runs, delete the hidden `.owngit-upgrade-copy-...` and `.pre-...owngit-backup-...` folders it left there.

#### Where upgrade backups go

Once a new backup is complete, OwnGit removes the older backups it made there before an upgrade of the same state directory, which `owngit-upgrade-backup.txt` names. It leaves everything else in the folder alone.

To keep these backups on another local disk, on macOS and Linux make the `-backups` folder a link to a folder there that only this account can change. Windows follows no link on the way, so choose a state directory on that disk instead.

#### Turning the upgrade backup off

To upgrade without a backup, for example when you back up another way, turn it off:

```sh
owngit upgrade-backup off
```

OwnGit then upgrades without a backup and logs a warning when it does. `owngit upgrade-backup on` turns the backup on again, and `owngit upgrade-backup` shows the setting (`--json` for JSON). The command works before a newer version has upgraded the state, so after a failed backup you can turn it off and start again. The setting belongs to this computer's state directory, and backups do not carry it. With the backup off, stop OwnGit and make a backup with the current version's `owngit backup` before you install a newer version.

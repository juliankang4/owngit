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

The installer is in `packaging/installer/` of the source, together with the [Proxmox VE script](#run-on-proxmox-ve) `proxmox.sh`, and every release carries the same three files beside its archives. `SHA256SUMS` comes from the same release as the archive. The check therefore finds a damaged, incomplete or wrong download, but it is not an independent signature: whoever can change the release's files can change both. The macOS binary is also signed and notarized by Apple.

The one-line command runs the script that owngit.app serves over HTTPS before anything checks it. `SHA256SUMS` lists only the archives; the release's `manifest.json` records the SHA-256 of the installers and of `proxmox.sh`. To check the script before it runs:

1. Download `install.sh`, `install.ps1` or `proxmox.sh` from the release.
2. Compare its SHA-256 with `manifest.json`, or with the one GitHub shows for that file on the release page. You can also compare the file with `packaging/installer/` at the release's tag.
3. Read it, then run the file with `/bin/sh install.sh`, `& .\install.ps1`, or `/bin/sh proxmox.sh` as root on the Proxmox VE host.

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

- the repository folder, typed as a path or, in the browser, [chosen from a list](#choose-the-repository-folder-in-the-browser);
- whether general access is open, or protected by one shared password;
- a separate administrator password, which the dashboard asks for before administrator changes ([how often](#administrator-password-check)).

When you open setup from a public Internet address, or through a trusted proxy that did not pass on your original address, the setup page selects the shared password for general access and says why. In the proxy case it shows the warning "Forwarded request, original address unknown".

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
- If Tailscale runs, it shows this computer's Tailscale address and MagicDNS name and prints a command that saves them as [network settings](#network-settings), such as `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --accept-insecure-http`. Its `--accept-insecure-http` option records that you accept plain HTTP for an address other devices reach ([Network settings](#network-settings)). When `owngit` on PATH is not the OwnGit that runs, as for a copy from an archive, the command starts with that copy's path instead.

Setup does not run that command; run it after setup and restart OwnGit. Tailscale encrypts the connection, but OwnGit cannot see that and still reports plain HTTP for this address. For an address that OwnGit reports as encrypted, [share on your tailnet over HTTPS](#share-on-your-tailnet-over-https) instead.

### Setup in the browser, approved in the terminal

"Open the web dashboard" opens `http://127.0.0.1:7654/setup` in your browser; with `--no-open`, the terminal prints the address instead.

1. In the browser, choose "Ask the terminal for approval". The page shows a short code.
2. The terminal shows "A browser wants to set up OwnGit" with the same code and the address the request came from. A request from another device is marked with a warning, and so is every request that came through a trusted proxy. When the proxy did not pass on the original address, the terminal shows "Forwarded request, original address unknown" in place of the address.
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

The setup page asks you to accept plain HTTP and offers to keep accepting the address you used. That box is ticked by default unless you opened setup from a public address, or through a trusted proxy that did not pass on your original address. If you untick it, OwnGit refuses that address after setup and, from its next start, listens only on `127.0.0.1:7654`. To stay on this computer only from the start, run `owngit network set --listen 127.0.0.1:7654` and restart. A computer with a screen listens on `127.0.0.1:7654` until you choose another address.

OwnGit counts a computer as headless in these cases:

- root runs it in a container;
- the command runs in an SSH session without a display (`DISPLAY` and `WAYLAND_DISPLAY` unset; on Windows, any SSH session);
- systemd-logind lists no graphical session and neither variable is set;
- on a Mac, the user who runs OwnGit is not logged in on the screen. A Mac where you are logged in on the screen counts as having one, even over SSH.

A service passes `--headless=true` or `false` from its install, so it does not decide again at boot.

### Choose the repository folder in the browser

On the setup page in a browser, "Choose folder" beside the "Repository folder" field opens a list of folders, so you can pick one instead of typing its path. The folders are on the computer that runs OwnGit, as the account running OwnGit sees them, not on the device with the browser. Typing a path still works. Without JavaScript the button does not appear.

The chooser opens at the path in the field. With the field empty, it opens at the suggested folder, `OwnGit-Repositories` in that account's home folder. If that folder does not exist yet, the chooser opens its nearest existing parent and puts the missing name in "New folder name".

- The list shows folders and symbolic links by name, and no files. "Parent folder" goes up one level. "Show hidden folders" adds hidden ones, such as folders whose names start with a dot.
- Links are listed without being followed. A broken link, a link to a file, or a link to a folder the account cannot read shows its error when you open it.
- "Create folder" makes one folder inside the open folder and opens it. It never replaces something that already exists.
- A missing folder, a path that is not a folder, a folder the account cannot read, or a folder that does not answer within 3 seconds shows an error, never an empty list. Go to the parent folder or type another path.
- In a very large folder, only the folders among the first 2,000 entries are listed, with a notice saying so.
- "Use this folder" only fills in the field. Setup checks the path when you submit the form, as it does for a typed one.

Only the browser that is running setup can use the chooser, and only until setup finishes. OwnGit refuses it to anyone else, and after setup it is gone.

## Run as a service

`owngit service install` runs OwnGit in the background and restarts it by itself:

- on Linux, with systemd at every boot;
- on macOS, with launchd at every login;
- on Windows, with Task Scheduler at every boot (administrator account) or sign-in (standard account).

It chooses who runs the service from how you run the command, waits until the service answers, and prints the setup link. If the service stops with an error while it waits, for example because another program uses the port, the command prints that error with the next step.

Before `owngit service install` registers its own service, it checks that no account other than yours, root or the administrators can replace the `owngit` program, either directly or through a folder or link on its path. When another account could, the command stops before it changes anything and names the path to fix. On Windows, an administrator's install registers a protected copy in Program Files instead. A Homebrew install hands the service to Homebrew.

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

The user service and the system service run the `owngit` you ran the command with. If another account could replace that file, directly or through a folder or link on its path, the command stops and names the path. Move `owngit` to a folder only you or root can change, such as `~/.local/bin`, or use the [one-line installer](#one-line-installer). On a desktop, the program that the icon's sign-in entry starts must pass the same check.

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

As with every restore, what a backup does not carry starts at its default ([After a restore](#after-a-restore)), including sessions, allowed Hosts and network settings. Sign in again, and to reach OwnGit from other devices run `sudo owngit network set --listen 0.0.0.0:7654 --allowed-host ADDRESS --accept-insecure-http` and `sudo owngit service restart`. Root's old state, the backup and the unused `/var/lib/owngit/state` stay until you remove them. Later installs keep using the restored state.

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

On a computer with a desktop, the install also registers a second task, `OwnGit icon`, for the [OwnGit icon](#the-icon-on-windows).

#### How the Windows service runs

An administrator account's task starts `%ProgramFiles%\OwnGit\owngit.exe serve --state-dir DIR --no-open --log-file DIR\logs\service.log --service --headless=true` (or `false`). A standard account's task starts the `owngit.exe` used to install it. That task runs whatever is at that path when you sign in, so `owngit service install` from a standard account refuses an `owngit.exe` that another account on the computer could replace: one in a folder that other accounts can change, such as `D:\tools`, or below one. Move `owngit.exe` into a folder only you can change, such as one in your user profile, or use the [one-line installer](#one-line-installer), then run `owngit service install` again.

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

macOS lists the agent as OwnGit under System Settings, General, Login Items & Extensions, Allow in the Background, when OwnGit.app came with the program, as in every release install; a program without the app is listed as `owngit`. When it is turned off there, the agent does not start; the command says so and puts back the agent that was there before.

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

On a Linux computer with Docker Engine and Docker Compose, OwnGit can run as a container instead of a service. The image is `ghcr.io/juliankang4/owngit`, for x64 and ARM64. Each release is tagged `X.Y.Z`, `X.Y` and `latest` and comes with build provenance.

Save [`compose.yaml`](../packaging/container/compose.yaml) in a new folder, then in that folder start OwnGit and show the setup link:

```sh
docker compose up -d
docker compose exec -it owngit owngit setup-link
```

`-it` gives the command a terminal, which it needs to show the one-time setup link. Without one, the command names only the setup file inside the container. The log that `docker compose logs` shows names that file too, never the link. The link, `http://localhost:7654/setup#...`, works once within 15 minutes. Open it in a browser on the computer that runs the container. From another device, put that computer's name or address in place of `localhost`, for example `http://nas.local:7654/setup#...`. The setup page then offers to keep accepting that name, and other devices use it from then on.

Setup offers open access by default, where anyone who can reach OwnGit gets in without a password. With open access, finish setup at the computer's name or address, even on the computer that runs the container. After setup, OwnGit in a container refuses `localhost` from outside the container, and [localhost and other addresses](#localhost-and-other-addresses) explains why.

`docker compose ps` shows the container as `healthy` once `owngit health` inside it gets an answer. Other commands run the same way, for example `docker compose exec owngit owngit doctor`.

The image holds the release's `owngit` program and Git on Debian 13. It runs as the account `owngit` (user and group ID 10001), never as root. OwnGit needs no privileged mode, Linux capabilities, Docker socket or host network, and `compose.yaml` drops every capability and blocks new privileges.

### Where the data lives

Everything lives in the volume `owngit-data`, mounted at `/data`:

- the state in `/data/owngit`;
- the repositories in `/data/OwnGit-Repositories`, the folder setup suggests;
- the [backups made before an upgrade](#backup-before-an-upgrade) in `/data/owngit-backups`.

Restarting, recreating or updating the container keeps the volume. `docker compose down` removes the container and keeps the volume. `docker compose down -v` deletes the volume, with every repository in it.

Keep `/data` on a local disk, because the [state directory](#state-directory) must be on one. To keep the repositories on a network share, mount the share at another path in the container, such as `/repositories`, and choose that folder in setup.

### localhost and other addresses

OwnGit listens on every address inside the container, and the `ports` line of `compose.yaml` decides who reaches it. `"7654:7654"` publishes it on every address of the computer that runs the container. `"127.0.0.1:7654:7654"` keeps it on that computer only, and there `localhost` works only while access needs the shared password, as the next paragraph explains. To use another port, change the first number, for example `"8080:7654"`, and use that port in the setup link too.

In a container, OwnGit accepts `localhost`, `127.0.0.1` and `::1` from outside the container only while access needs the shared password, when every visitor must sign in anyway. With open access, the default, it refuses them with a page that says to use the computer's name or address instead, also on the computer that runs the container. The reason is that OwnGit cannot tell that computer from other devices by their address: Docker forwards IPv6 connections from an address inside the container network, and rootless Docker forwards every connection that way.

For open access, finish setup at the computer's name or address and keep it. To allow a name later, save it and restart the container:

```sh
docker compose exec owngit owngit network set --allowed-host nas.local
docker compose restart
```

Before setup, only the setup link works, as with any OwnGit.

OwnGit counts wrong passwords per address: by default, four within 10 minutes pause that address for 15 minutes ([Login attempt limits](#login-attempt-limits)). With ordinary Docker, IPv4 connections keep each device's address, so each device is counted on its own. IPv6 connections through Docker's proxy, and every connection under rootless Docker, arrive from one address. Wrong passwords from all those devices then count together, so four wrong tries from anyone on the network pause sign-in for everyone for 15 minutes, including someone with the right password. The pause ends by itself.

### Update

When a newer release exists, the dashboard notice and `owngit update` show the command for the container:

```sh
docker compose pull && docker compose up -d
```

Run it on the computer that runs the container, in the folder of `compose.yaml`. It downloads the new image and recreates the container with it, and the volume stays. OwnGit backs up the state before it upgrades it, as described in [Backup before an upgrade](#backup-before-an-upgrade).

To keep a backup of your own as well, stop OwnGit, back it up, update, and verify the backup:

```sh
docker compose stop
docker compose run --rm owngit owngit backup --output /data/backup-before-update
docker compose pull
docker compose up -d
docker compose exec owngit owngit backup verify /data/backup-before-update
```

The output folder must not exist yet, so use a new name each time. A backup in the volume is deleted with the volume. To keep one elsewhere, mount a folder that the account `owngit` (ID 10001) owns and that no other account can change, and write the backup there.

### Running as another account

To run OwnGit as another account, for example to match the owner of a folder on the computer, set `user:` in `compose.yaml` and keep `/data` in a folder that this account owns and that no other account can change:

```yaml
services:
  owngit:
    user: "1000:1000"
    volumes:
      - ./owngit-data:/data
```

Create that folder first, in the folder of `compose.yaml`:

```sh
sudo install -d -o 1000 -g 1000 -m 700 owngit-data
```

The named volume of the default `compose.yaml` belongs to the account `owngit`, so another account cannot use it. OwnGit refuses a `/data` folder that other accounts can change, and its log names the `chmod` command that fixes it.

## Run on Proxmox VE

On a Proxmox VE host, one command creates an unprivileged Debian 13 container (LXC) for OwnGit and installs OwnGit in it as a service. Run it as root in the host's shell, for example the node's Shell in the Proxmox web interface:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh
```

The script takes these steps:

1. When the host has no `debian-13-standard` template yet, it downloads the newest one with `pveam`, which checks it against Proxmox's signed template list.
2. It creates an unprivileged Debian 13 container named `owngit` with the next free ID: 2 cores, 1024 MB of memory, 512 MB of swap, an 8 GB disk on `local-lvm` (or `local-zfs` when there is no `local-lvm`), and the bridge `vmbr0` with DHCP. The container has the tag `owngit`, starts with the host, and has the `nesting` feature, which systemd in Debian 13 needs in an unprivileged container.
3. In the container, it installs Git and the tools the installer needs, updates the packages, and runs the release's `install.sh`. As described in [One-line installer](#one-line-installer), the installer checks the archive against `SHA256SUMS`, puts the program at `/usr/local/bin/owngit` and runs `owngit service install` as root. So the account `owngit` runs the service, with the state in `/var/lib/owngit/state` (see [Installing as root](#installing-as-root)).
4. With `--repositories`, it gives OwnGit a folder of the host for the repositories (see below).
5. It waits until OwnGit answers and prints the container's address and port 7654. Run from a terminal, it also prints the setup link; otherwise it prints the command that shows the link.

Apart from this script, nothing from the OwnGit release runs on the host: `install.sh` and OwnGit run only inside the unprivileged container. When a step fails before OwnGit answers, the script removes the container it created and puts the repository folder back as it was: it removes a folder it made, or gives an existing one back its owner and mode. A template it downloaded stays.

Options go after `/bin/sh -s --`:

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh -s -- --repositories /tank/owngit
```

| Option | What it does |
| --- | --- |
| `--id N` | Uses this container ID instead of the next free one. |
| `--hostname NAME` | Names the container NAME instead of `owngit`. |
| `--storage NAME` | Puts the container's disk on this storage. |
| `--disk GB`, `--cores N`, `--memory MB` | Sets the disk size, the CPU cores and the memory. |
| `--bridge NAME` | Connects the container to this bridge instead of `vmbr0`. |
| `--ip ADDRESS/PREFIX`, `--gateway ADDRESS` | Gives the container a fixed IPv4 address, such as `192.168.1.50/24`, and its gateway instead of DHCP. Give both. |
| `--repositories FOLDER` | Keeps the repositories in FOLDER on the host. |
| `--template VOLUME` | Uses a Debian 13 template you already have, such as `local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst`. |
| `--version X.Y.Z` | Installs that release (1.1.3 or later) instead of the latest one. |

The script never changes a container that exists. It stops when a container in the cluster already has the name, or a container or virtual machine already has the ID. When that container is OwnGit's own, marked by the tag `owngit`, the script says how to update it and changes nothing.

Run OwnGit's commands in the container with `pct exec` and the container ID that the script printed, 105 in these examples. `pct exec` does not look in `/usr/local/bin`, so give the program's full path:

```sh
pct exec 105 -- /usr/local/bin/owngit service status
```

### Repositories in a folder of the host

With `--repositories /tank/owngit`, the repositories stay in `/tank/owngit` on the host, for example on a ZFS dataset. The container sees that folder as `/var/lib/owngit/OwnGit-Repositories`, the folder that setup suggests, so keep the suggested folder in setup.

- The folder must be new or empty, and the folder above it must exist. Its path must not go through a link.
- Every folder above it, and the folder itself when it exists, must belong to root, and only root may be able to change them. Otherwise another account on the host could redirect the folder, or put files in it before the container gets it. For a folder that you made for OwnGit, `chown root:root FOLDER && chmod go-w FOLDER` sets this.
- In an unprivileged container, the container's accounts have different IDs on the host, usually 100000 higher. The script makes the folder belong to the host ID of the container's `owngit` account, with mode 0700, and changes nothing else on the host.
- To add the folder, the script stops the container once after installing OwnGit and starts it again.
- Proxmox backups (`vzdump`) do not include a folder of the host. Back up the repositories with [`owngit backup`](#backups) or with the host's own backups. A container with a folder of the host cannot be migrated to another node.
- To bring existing repositories in, import them or restore a backup after setup. The script does not take over a folder that already has content.

### Update and delete the container

`pct exec 105 -- /usr/local/bin/owngit update` prints the command that updates OwnGit. Run it in the container's shell, which `pct enter 105` opens; start a stopped container first with `pct start 105`. The command runs the new release's installer as described in [Update and uninstall](#update-and-uninstall), and the service restarts with the new version.

`pct destroy 105` deletes the container with the state and any repositories inside it. A folder of the host given with `--repositories` stays. Make a backup first.

## Settings

Settings has five tabs. Each is its own address, so it works as an ordinary link, also without JavaScript:

- **General** (`/settings`): the display choices of this browser (language, appearance and repository list order), which apply at once and never ask for a password, and the new-release [update check](#new-release-notice) for the whole server.
- **Access** (`/settings/access`): who can read and push (anyone who reaches OwnGit, or only people with the shared password), [how long a sign-in lasts](#how-long-a-sign-in-lasts), [links from other sites](#links-from-other-sites), [login attempt limits](#login-attempt-limits), the administrator password, and [how often it is asked](#administrator-password-check).
- **Network** (`/settings/network`): the connection of this browser, the [network settings](#network-settings) and [sharing on your tailnet](#share-on-your-tailnet-over-https).
- **Repositories** (`/settings/repositories`): the [branch new repositories start on](#changing-the-default-branch), whether overwritten and deleted history is [kept](#kept-history), the [Git transfer limits](#git-transfer-limits), the [browsing limits](#browsing-limits), the [check ceilings](#check-ceilings), whether [deleting a repository](#deleting-a-repository) asks for its name, and a link to the settings of each repository.
- **Storage & recovery** (`/settings/storage`): the repository folder and [backups](#backups-in-the-dashboard), both shown only to an administrator, how long [raw check logs](#raw-check-logs) are kept, [repository maintenance](#maintenance) and [unused object cleanup](#unused-object-cleanup).

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

### Settings on the command line

`owngit settings` makes every change of the General, Access, Repositories and Storage & recovery tabs, except the display choices of your browser and the OwnGit icon. It works through the administrator API, so each command needs `--server` and a `--password-file` holding the administrator password, and it prints its answer as JSON:

- `owngit settings show` prints the saved settings (listed below), the access mode (`access_mode`, `open` or `password`), how often the dashboard asks for the administrator password (`admin_confirmation`) and the new-release check (`update_check`). `update_check_forced_off` is true when the server was started with `--no-update-check`.
- `owngit settings set` changes only the settings its options name, such as `--session 7d` or `--update-check off`.
- `owngit settings access --mode password` turns the shared password on or replaces it. `--mode open` turns it off, so anyone who reaches OwnGit can read and push.
- `owngit settings admin-password` replaces the administrator password.
- `owngit settings confirmation --choice CHOICE` sets how often the dashboard asks for the administrator password: `every`, `30m`, `1h`, `8h`, `1d`, `7d`, `30d` or `never` (Do not ask). `never` also needs `--acknowledge-no-ask`, which accepts the same warning as Settings.

```sh
owngit settings set --server http://127.0.0.1:7654 --accept-insecure-http \
  --password-file /path/to/admin-password --update-check off
```

`--accept-insecure-http` accepts plain HTTP, which is not encrypted, for that one command. Leave it out when the server address starts with `https://`.

A new password never goes on the command line. `settings access` reads it from the file given with `--access-password-file`, and `settings admin-password` from `--new-password-file`; each must be an owner-only file ([Password and token files](#password-and-token-files)). Without that option, the command asks for the password twice at a prompt that does not show what you type, which works only in a terminal. OwnGit refuses the same passwords as Settings, such as one that is too short or one that equals the other password. A new shared password signs out everyone signed in with the old one. A new administrator password ends every browser's administrator confirmation. Commands that read the old one from a password file stop working until you put the new one there.

In the dashboard, the saved values of these settings are shown only to a confirmed administrator: [how long a sign-in lasts](#how-long-a-sign-in-lasts), [links from other sites](#links-from-other-sites), [login attempt limits](#login-attempt-limits), [the branch new repositories start on](#changing-the-default-branch), the server-wide [kept history](#kept-history) choice, the [Git transfer limits](#git-transfer-limits), the [browsing limits](#browsing-limits), the [check ceilings](#check-ceilings), whether [deleting a repository](#deleting-a-repository) asks for its name, how long [raw check logs](#raw-check-logs) are kept, [repository maintenance](#maintenance) and [unused object cleanup](#unused-object-cleanup). Anyone else who opens Settings sees what each one does and a button to confirm as administrator.

In that JSON, and in the administrator API at `/api/v1/settings`, the Git transfer limits are the group `git_transfer`, and the browsing limits, check ceilings, repository maintenance and unused object cleanup are `browse_limits`, `check_ceilings`, `maintenance` and `unused_object_cleanup`. A change that names some fields of a group keeps the others.

When a saved setting cannot be read, for example after a hand edit of the state database, `show` fails and names it. `set` still saves the settings it names and lists the unreadable one under `unreadable`; setting it again replaces it. Meanwhile only what depends on that setting stops, and its message names the setting:

- the server-wide kept history choice: pushes and imports to every repository that follows it are refused;
- the Git transfer limits: Git requests are refused;
- the browsing limits: files, diffs and comparisons are not shown;
- the check ceilings: no check policy is saved and no new check is queued;
- repository maintenance: no repository is maintained;
- unused object cleanup: no object is removed, and maintenance goes on without it.

The settings that `settings set` and `settings confirmation` change belong to this installation host and are not in backups, so a restored installation starts with the defaults. The access mode and both passwords are in backups.

The Network tab and the OwnGit icon have their own commands, which run on the installation host: `owngit network` and `owngit tailscale` ([Network settings](#network-settings)), and `owngit tray on` or `owngit tray off` ([OwnGit icon](#owngit-icon)).

### Dashboard-only and command-line-only tasks

Every owner task in Settings and on a repository's pages also has a command. Most of these commands print JSON, some only with `--json`. These print text only: the host recovery commands, the commands that run or control a process, and `uninstall` (all listed below). A command given `--json` also answers with a JSON error when it cannot start as the account that owns the state: `privilege_drop_failed` when an administrator terminal on Windows cannot start its copy without administrator rights, and `state_unavailable` when it may not act as the service account on Linux.

A few tasks are on one side only, on purpose:

- Command line only: `owngit reset-admin`, `owngit setup-link` and `owngit approve-host` recover access from the installation host when the dashboard cannot be used ([Host-owner recovery](#host-owner-recovery), [Host names](#host-names)). `owngit uninstall` removes the service that serves the dashboard ([Uninstall](#uninstall)). `owngit restore` creates a new state directory and repository folder, so replacing the installation that serves the dashboard means stopping it first; the dashboard shows the [restore steps](#restore-steps-in-the-dashboard) instead of running them. `owngit backup --output` refuses while an OwnGit runs with that state directory; for a running OwnGit, Back up now in the dashboard does the same job.
- Dashboard only: accepting the plain HTTP warning that a browser shows, because it concerns that browser's own connection. A client command such as `settings` or `repo` accepts plain HTTP for itself each time with `--accept-insecure-http`. `owngit network set --accept-insecure-http` records the same acceptance as the dashboard, once, when it saves an address other computers reach ([Network settings](#network-settings)).
- Running programs: `owngit serve` runs OwnGit, `owngit service` installs and controls it as a service, `owngit runner` runs automatic checks, and `owngit mcp` serves a coding tool. They have no dashboard form because they start or control a process.
- Records from coding tools: check tasks, correction rounds and attempts (`owngit check task new`, `check cycle reserve`, `check run`) are evidence that a coding tool records ([Coding tools](CODING_TOOLS.md)). The dashboard shows them but does not create them. `owngit tasks` prints what the dashboard shows of them.

### How long a sign-in lasts

"A sign-in lasts" on the Access tab sets how long a browser stays signed in after it signs in with the shared password: 1 hour, 8 hours, 12 hours (the default), 1 day, 7 days or 30 days.

A new time applies to sign-ins after you save it; a browser already signed in keeps the end it has. To end every sign-in now, change the shared password. A longer time lets someone using a signed-in browser use the dashboard without the password for longer. When anyone can reach OwnGit without a password, nobody signs in and the time has no effect.

### Links from other sites

"Links from other sites" on the Access tab decides what happens to a shared password sign-in when you open an OwnGit link from a chat, webmail or another site:

- **Require fresh navigation** (the default): the link opens the sign-in page, or the page as someone not signed in, until you open it again from OwnGit.
- **Keep the sign-in**: the link opens with this browser's shared sign-in. Choose it when links from your chat or mail should open straight away.

Either way, the administrator confirmation is never kept on a link from another site, and OwnGit refuses a form that another site sends. The choice applies to this browser as soon as you save it and to other browsers at their next sign-in. On the command line, run `owngit settings set --cross-site-links strict` or `--cross-site-links lax`. When anyone can reach OwnGit without a password, nobody signs in and the choice has no effect.

If the saved choice cannot be read, nobody can sign in with the shared password, and the sign-in page names the setting. Set it again under Settings, Access, or with `owngit settings set --cross-site-links`.

### Login attempt limits

OwnGit pauses an address that sends too many wrong passwords. By default, 4 wrong passwords from one address within 10 minutes pause that address for 15 minutes. During the pause the address cannot sign in, even with the right password; the pause ends by itself. Correct passwords never count.

The limits apply in the dashboard, Git and the API. Wrong shared passwords and wrong administrator passwords are counted separately.

To change the limits, open Login attempts on the Access tab and use Change the limits, or run:

```sh
owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m
```

- The number of wrong passwords goes from 1 to 100. The window and the pause each go from 1 minute to 24 hours. The limits cannot be turned off.
- New limits apply to wrong passwords after you save. An address already paused stays paused until its pause ends.
- More attempts, a shorter window or a shorter pause lets people who can reach OwnGit try more passwords. Settings warns about it before you save, and `owngit settings set` lists the warning under `warnings`.
- Until a proxy is trusted, everyone who reaches OwnGit through it arrives from the proxy's address, so they are paused together. Before you loosen the limits, add that proxy under Trusted proxies on the Network tab ([Behind a reverse proxy](#behind-a-reverse-proxy)); OwnGit then counts each person at their own address.

During a pause, Git and the API get HTTP 429 (Too Many Requests) with a `Retry-After` header that gives the seconds left in the pause. Git gets 429 instead of the authentication failure a wrong password gets, so Git keeps the password its credential helper saved.

If the saved limits cannot be read, for example after a hand edit of the state database, a correct password still signs in. A wrong password is refused without being counted, and the message names the command that repairs the limits. Set all three together, under Settings, Access, or with the command above; setting only one or two fails while the saved limits cannot be read.

### Administrator password check

"Ask for the administrator password" on the Access tab decides when the dashboard asks for it. It applies to Settings, repository settings, deletion, imports, checks, and helper and runner credentials:

- **Every time**: each change asks. Signing in as administrator opens the administrator pages for a short time only.
- **Again after 30 minutes** (the default), **1 hour**, **8 hours**, **1 day**, **7 days** or **30 days**: after you type the password, on the administrator sign-in or in a form, this browser does not ask again for that long. The time counts from when you typed it; moving between pages does not extend it. Another browser is asked for its own. The sidebar shows until when this browser is confirmed, with End to stop now. Signing out, End, or changing or resetting the administrator password ends it. Choosing a shorter time shortens it to the new time counted from when the password was typed, so it may end at once.
- **Do not ask**: anyone who can open the dashboard can change settings, delete repositories, issue credentials and turn on automatic checks without the administrator password. When anyone can reach OwnGit without a password, nobody has to sign in either. Turning it on asks for the password one last time and for a tick confirming the warning. While it is on, every page shows "Administrator password check off", which leads back here.

On the command line, `owngit settings confirmation` makes the same choice ([Settings on the command line](#settings-on-the-command-line)).

To end the confirmation of a browser you no longer have, change the administrator password in Settings or with `owngit settings admin-password`, or run `owngit reset-admin` on the installation host. Each ends every browser's confirmation.

The command line and the administrator API always ask for the administrator password, for reads as well as changes, whatever the choice. A browser that is confirmed in the dashboard is not signed in to the API.

The choice belongs to this installation host and is not in backups; a restored installation asks after 30 minutes again. If the saved choice is one this version does not know, for example after going back to an older release, every change asks, and Access says so until you choose again.

### OwnGit icon

The OwnGit icon shows in the menu bar on macOS, in the notification area on Windows and in the desktop's panel on Linux; see [The icon on macOS](#the-icon-on-macos), [The icon on Windows](#the-icon-on-windows) and [The icon on Linux](#the-icon-on-linux). On all three it also shows [desktop notifications](#desktop-notifications) about pushes, pull requests, failed checks, imports and backups that did not finish, and new versions.

The icon belongs to the computer that runs OwnGit and to the account that runs it. It shows by default on a computer with a desktop and never on one without, such as a server reached over SSH. When root installs OwnGit on Linux, the service runs as its own `owngit` account, which nobody signs in as at a desktop, so that kind of install has no icon; Settings and `owngit tray` say so. `owngit tray off` hides it on this computer until `owngit tray on` shows it again, also after signing in again or restarting. `owngit tray` (or `owngit tray status`) says whether it shows; `--json` prints `available` (false on an install without an icon), `shown` (false only when hidden), `desktop` (whether this computer has a desktop now), `state_dir`, `access_file` and `problem`, which says why the icon cannot show on this computer, or is empty. The switch "Show the OwnGit icon in the menu bar, notification area or panel" on the General tab of Settings does the same and asks for the administrator password like the other parts of Settings. It changes the icon of the computer that runs OwnGit, not of the computer your browser is on. Hiding the icon never stops OwnGit: Git and the dashboard keep working. The choice is the file `tray-hidden` in the state directory, so it belongs to this computer and is not in backups.

Each time OwnGit starts, it writes the file `tray-access.json` in the state directory, readable only by the account that runs OwnGit. It holds the address of this server on this computer, such as `http://127.0.0.1:7654`, a new random token and a new random proof secret; the file stays after OwnGit stops, but a token from an earlier start no longer works. `GET /tray/status` with the header `Authorization: Bearer <token>` and a header `X-OwnGit-Tray-Nonce` with 32 new random bytes in unpadded base64url answers a JSON status: the state (`running`, or `attention` when setup is not finished, a newer release is out, or the [checkup](#checkup) found a problem), whether the icon is hidden, the version, the dashboard and clone addresses as the dashboard shows them, the newer release with this install's update command, the checkup findings and the three latest pushes (repository, ref, number of refs, when OwnGit received the push, and how it was authorized, which for Git is always general access). A push is listed only when Git changed a ref; a refused or failed push, or one that asked to set a ref to the value it already had, is not, and a commit's own date never counts as the push time. OwnGit keeps the latest 100 pushes, and they are not in backups. The status answers only a request that carries the token, and the token alone proves the icon. So the icon keeps working when `127.0.0.1` is a trusted proxy, as it is while [tailnet sharing](#share-on-your-tailnet-over-https) is on, and other devices and other accounts on this computer, which cannot read the token, get no status. Programs that run as the account that runs OwnGit can read the token, as they can read the state directory itself. Each status answer carries the header `X-OwnGit-Tray-Proof`: an HMAC-SHA256, keyed with the proof secret, over `owngit tray status`, the nonce and the exact answer body, each followed by a newline, in unpadded base64url. The proof secret is never sent, so a program that takes the address while OwnGit is stopped cannot answer with a valid proof, and a reader uses nothing from an answer without one. `/healthz` stays empty.

#### The icon on macOS

On macOS the icon is OwnGit.app, which comes with every release install. The release archive and the one-line installer put it next to the `owngit` program (`~/.local/bin/OwnGit.app` by default), npm next to the executable of its platform package, and Homebrew at `$(brew --prefix)/opt/owngit/OwnGit.app`. It is only the icon: the service runs the server, and hiding or quitting the icon never stops Git or the dashboard.

On a Mac with a desktop, `owngit service install` opens the icon in your desktop login, and the app then registers itself to open when you sign in. It does not open for a headless service, over SSH while you are not logged in on the Mac's screen, or when the icon is hidden. When an icon of your account already runs, `owngit service install` and `owngit service restart` quit it and open it again, so that after an update it runs the new app; a hidden icon stays hidden. System Settings lists that item as OwnGit under Open at Login in Login Items & Extensions. A Homebrew install uses a LaunchAgent instead, `~/Library/LaunchAgents/app.owngit.icon.plist`, which opens the app from `$(brew --prefix)/opt/owngit`, the folder Homebrew keeps across upgrades, so the icon also opens after an upgrade made while it was not running.

Click the icon to open the panel. It shows whether OwnGit is running, needs attention (setup to finish, a newer version, or a problem the checkup found, with the command that updates this install and a Copy button, or the command that repairs it as text you can select), is not running (with a button that starts it), or cannot report its status just now. While OwnGit runs, it also shows the clone address with a Copy button, the three latest pushes and Open dashboard, which reads Finish setup until setup is done. Tab moves through the panel, Space presses a button, and Esc closes it. The panel follows the macOS appearance (light or dark) and language (Korean when macOS prefers Korean, English otherwise).

The gear button holds this Mac's icon settings. "Open at sign-in" turns the sign-in item on or off. "Hide from the menu bar" is the same choice as `owngit tray off`; opening OwnGit.app, the switch in Settings or `owngit tray on` shows the icon again. "Quit the icon" closes it until you open OwnGit.app or sign in again. `owngit service uninstall` quits your account's icon and turns off its sign-in item, and it says so only after checking that the icon is gone and that macOS reports the item off.

The app refuses to run when it or the `owngit` program it uses is in a folder that another account on the Mac could change: it says where OwnGit is and to move it to a folder only you can change, and it registers and runs nothing. Like the icon on Windows, it uses a status answer only when it carries the server's proof and checks again before it opens the dashboard or setup, and it opens only the address in `tray-access.json`.

#### The icon on Windows

On a computer with a desktop, `owngit service install` also registers a second Task Scheduler task, `OwnGit icon`, that starts the icon when you sign in, as your account and without administrator rights, also when the service itself was installed with an administrator's approval. It runs the same `owngit.exe` as the service. An install without a screen (`--headless`, the default over SSH) has no icon and registers no such task, and `owngit service uninstall` removes it. Without the service, `owngit tray icon` shows the icon until you quit it or close its terminal.

Click the icon to open the dashboard. Right-click it to open the panel. The panel shows whether OwnGit is running, needs attention (with the one command that updates or repairs it and a Copy button), is not running (with the command that starts it), or cannot report its status just now. While OwnGit runs, it also shows the clone address with a Copy button and the three latest pushes. Tab moves through the panel, Enter or Space presses a button, and Esc closes it. The panel follows the Windows color mode (light or dark) and the display language (Korean when Windows shows Korean, English otherwise).

"Hide from the notification area" in the panel is the same choice as `owngit tray off`: the icon stays hidden, also after you sign in again, until you turn it on in Settings or run `owngit tray on`, and then it shows again within a few seconds. "Quit the icon" closes it until you sign in again. OwnGit keeps running either way. For an install that starts when you sign in, `owngit service stop` also closes the icon, because it runs the same `owngit.exe` that an update replaces, and `owngit service start` or `owngit service restart` opens it again.

Before the icon opens the dashboard, it checks again that OwnGit answers at its address, so it never sends your browser to another program that took that address after OwnGit stopped. When that check fails, the panel shows that the status is unavailable.

Windows 11 may put a new icon among the hidden icons (the ^ button). To keep it on the taskbar, turn it on in Settings, Personalization, Taskbar, Other system tray icons, or drag it to the taskbar.

#### The icon on Linux

On a Linux desktop, run `owngit service install` from a terminal on that desktop. It starts the icon in the desktop's panel and writes `~/.config/autostart/owngit-icon.desktop`, so the icon starts again each time you sign in. The icon runs as your account, without `sudo`. For a Homebrew install the entry names Homebrew's `opt/owngit` link, so it keeps working after `brew upgrade`. If that file already exists and OwnGit did not write it, or cannot read it, the install leaves it as it is, does not start the icon and says so. `owngit service uninstall` removes only an entry that OwnGit wrote; another program's file, or one OwnGit cannot read, stays. Without the service, `owngit tray icon` shows the icon until you quit it.

Some installs have no icon: one without a screen (`--headless`, the default over SSH), and a root install, whose service runs as the `owngit` account.

The desktop has to show StatusNotifierItem icons, the standard way for a program to put an icon in the panel. The icon was tested on these desktops:

- Omarchy, whose bar keeps the icon in the drawer behind the arrow.
- GNOME 48 with the AppIndicator extension (package `gnome-shell-extension-appindicator`). GNOME shows such icons only with this extension.

Other desktops that show StatusNotifierItem icons, such as KDE Plasma, should show it too, but they were not tested.

The icon and its panel are drawn by the desktop's `gjs` with GTK 4. Omarchy includes both. Elsewhere, if `owngit tray status` says they are missing, install the distribution's `gjs` package. OwnGit starts `gjs` only when no account other than yours or root can replace it, directly or through a folder or link on its path. Otherwise the icon does not start, and `owngit tray status` names the path. OwnGit itself keeps running without the icon.

On Omarchy, click the icon to open its panel, and right-click it for its menu. On GNOME, a single click opens the menu and a double click opens the panel. The panel shows whether OwnGit is running, needs attention (with the command that updates or repairs it, and a Copy button), is not running (with the command that starts it), or cannot report its status now. It also shows the clone address with a Copy button and the three latest pushes.

Before Open dashboard opens the dashboard in the browser, the icon checks again that OwnGit answers at its address and proves its answer. When that check fails, the panel says "Status unavailable". When the browser cannot start, the panel says why. Tab moves through the panel, Enter or Space presses a button, and Esc closes it. The panel follows the desktop's light or dark style and its language (Korean when the session's language is Korean). On Wayland a program cannot place its window under the icon, so the panel opens where the desktop puts new windows, which on GNOME is the middle of the screen.

"Hide the icon" in the panel or the menu is the same choice as `owngit tray off`. "Quit the icon" closes it until you sign in again. OwnGit keeps running either way.

Other bars and scripts can show the same status with two commands, which read the access file and check every answer's proof themselves:

- `owngit tray read --json` prints `{"shown": ..., "panel": {...}}`. `shown` is false when the icon is hidden. `panel` has `condition` (`running`, `attention`, `stopped` or `unavailable`), `state`, `tooltip`, `subtitle`, `notice` (a list of sentences), `command_intro` and `command` (the command to copy, or empty), `clone_address`, `pushes` (up to three, each with `repository`, `branch` and `when`, a time in words), `no_pushes`, `can_open` and `labels` (the panel's words in the chosen language). It holds no token and no address to open. `--lang en` or `--lang ko` chooses the language; the default follows `LC_ALL`, `LC_MESSAGES` and `LANG`.
- `owngit tray open --json` opens the dashboard after a fresh proof and prints `{"ok": true, "url": ...}`.

Both accept `--state-dir`. When OwnGit does not answer or cannot prove its answer, `tray read` still succeeds and says so in `panel`: `condition` is `unavailable`, or `stopped` when the [checkup](#checkup) finds OwnGit stopped. A command that fails prints `{"ok": false, "error": {"code": ..., "message": ...}}` and exits with an error. Both commands give `tray_unavailable` for an install without an icon. Only `tray open` gives `status_unavailable`, when OwnGit did not prove its answer, and `open_failed`, when the browser could not start or its opener ended with a failure; in both cases no dashboard was opened. The [Omarchy bar widget](../integrations/omarchy/owngit.status/README.md) is built on these two commands.

#### Desktop notifications

The OwnGit icon tells you on this computer's desktop when something happens in OwnGit. Every kind is on by default:

- Pushes, with the branch and the latest commit message. Pushes that arrive within a minute of the first one become one notification, also across repositories: "3 pushes", then "notes 2, household 1" and the latest one.
- Pull requests opened.
- Automatic checks that failed.
- Imports that did not finish.
- Backups that did not finish.
- A new OwnGit version. When this install has an update command, the notification includes it; otherwise it says that the dashboard explains how to update. This one needs the [update check](#new-release-notice) to be on.

A notification about something that happened appears about one to one and a half minutes later. OwnGit waits a minute so that nothing recorded a moment late is missed, and the icon picks the event up at its next status read. A new version does not wait that minute: it shows at the icon's next status read after OwnGit finds the release. When more than three of one other kind arrive at once, they become one notification that counts them, such as "4 imports did not finish".

Clicking a notification opens its page in the dashboard: the branch's commits for a push (the repository for a deleted branch or tag), All activity for pushes to several repositories or a counted notification, the pull request, the repository's Checks tab (or the pull request, for a check that ran on one), the Import tab, Settings for a backup, and the home page for a new version. Before it opens the page, the icon checks again that OwnGit answers at its address, as Open dashboard does.

The notification settings belong to this computer and stay after you sign in again or restart. "Show notifications" turns all of them on or off, and each kind has its own switch, which keeps its choice while all are off. You find them in the icon's panel: on macOS behind the gear button, on Windows under the Notifications button in the This computer part, and on Linux in the Notifications section. On the command line, `owngit tray notifications` prints them (as JSON with `--json`), and a setting followed by `on` or `off` changes one. The settings are `all`, `only_others`, `push`, `pull_request`, `check_failed`, `import_failed`, `backup_failed` and `update`:

```sh
owngit tray notifications push off
owngit tray notifications only_others on
```

"Only what I did not do" (`only_others`) is off by default. When you turn it on, pushes, pull requests and imports that came from this computer are not shown. This computer is the one that runs OwnGit, so, for example, a push from its Git or a pull request opened in its browser counts. A request that reaches OwnGit through a reverse proxy counts as coming from another computer. Pushes and pull requests from other devices then say so: "Pushed from another computer" or "Opened from another computer". This setting does not affect failed checks, backups and new versions. OwnGit remembers where something came from only while it runs, so something you did on this computer just before OwnGit restarted can still show once.

Nothing shows while the icon is hidden, while "Show notifications" is off, or for a kind that is off, and what happened in the meantime is not shown later. When the icon starts for the first time or is shown again, it starts from that moment. A quit icon is different: when it starts again, it shows what happened while it was closed, up to the latest 100 pushes that OwnGit keeps.

The notification settings of the operating system apply as well, such as Do not disturb or notifications turned off for OwnGit:

- **macOS**: notifications appear in Notification Center. macOS asks for permission the first time there is something to show. When OwnGit's notifications are off in System Settings, Notifications, the panel says so and has a button that opens that page.
- **Windows**: the notifications are notification area balloons, which Windows 11 shows as notifications from "owngit.exe". With Do not disturb on, Windows shows nothing and keeps nothing for later, and Notification center does not keep them after they appear. Windows treats every balloon this way; it is not an OwnGit setting. Windows also does not tell the icon which notification you clicked, so a click opens a page that fits all the notifications shown so far: their common page, All activity when they are all pushes, and otherwise the dashboard home page.
- **Linux**: the desktop needs a notification service. GNOME and Omarchy have one.

## Reaching the server from another device

OwnGit serves plain HTTP and has no built-in TLS. Choose one of these ways to reach it from another device:

- **An encrypted address**: let Tailscale share OwnGit on your tailnet ([Share on your tailnet over HTTPS](#share-on-your-tailnet-over-https)), or put a reverse proxy in front of it ([Behind a reverse proxy](#behind-a-reverse-proxy)).
- **Plain HTTP** over Tailscale, your own VPN ([Other private networks](#other-private-networks)) or the LAN: let OwnGit listen on a network address ([Network settings](#network-settings)). You accept plain HTTP once, during setup or on the Network tab of Settings when you let other devices in, and the page header always shows whether the connection is encrypted.

Do not expose OwnGit to the public Internet. Only share links may have a public address of their own ([A public address for share links](#a-public-address-for-share-links)).

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
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal --accept-insecure-http
owngit network show
```

- `--listen` is `host:port`; an empty host, `0.0.0.0` or `::` listens on every interface.
- A listen address that other computers reach, such as `0.0.0.0:7654`, serves them plain HTTP, which is not encrypted. Until plain HTTP is accepted on this installation, `set` refuses such an address and saves nothing. Add `--accept-insecure-http` once to accept it; the acceptance is recorded, as when you accept it during setup or on the Network tab, and is not asked again. An `https` base URL does not change this, because OwnGit itself still serves plain HTTP. A loopback address such as `127.0.0.1:7654` never needs it.
- `--base-url` is the `http` or `https` origin other devices use, without a path. OwnGit accepts its host name and shows it in clone addresses; without it, clone addresses use the address the browser connected to.
- `--allowed-host` and `--remove-allowed-host` change the list that `owngit approve-host` also adds to.
- `--trusted-proxy` and `--remove-trusted-proxy` change the reverse proxies whose forwarded headers OwnGit believes, each an IP address or CIDR range (see [Behind a reverse proxy](#behind-a-reverse-proxy)).
- All options are repeatable. An empty value, such as `--base-url=`, removes that saved value.

`owngit serve` uses its option if given, then the saved value, then the default (`127.0.0.1:7654`). An option applies to that run only. `set` prints a note when the listen address leaves this computer (other devices then use plain HTTP) or when an `https` base URL has no trusted proxy.

`network show` lists the saved values, what a running server actually uses, and whether a restart is needed; `--json` prints the same as JSON. The Network tab of Settings shows the same, the saved value for the next start next to the value the running server uses. It changes them with the administrator password, and refuses a save when the settings changed after you opened the page.

A service definition that passes `--listen`, `--base-url`, `--allowed-host` or `--trusted-proxy` (the `ProgramArguments` of a LaunchAgent, the `ExecStart` of a unit) overrides the saved values at every start, so leave them out. The units that `owngit service install` writes never pass them. The Homebrew service runs `owngit serve --no-open` without any of them, so it uses the saved values. To reach it from other devices:

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --accept-insecure-http
brew services restart owngit
```

If a saved value locks you out, for example a listen address that no longer exists, reset it on the installation host and restart:

```sh
owngit network reset
```

`reset` removes the listen address, the base URL and the public address for share links. It keeps the allowed Hosts and trusted proxies unless you add `--clear-allowed-hosts` or `--clear-trusted-proxies`. No web page can do this.

`network set` and `network reset` also take `--json`. They then print one JSON object: `ok`, the same report that `network show --json` prints, `applies_at_next_start` (true, since changes apply at the next start), `plain_http_accepted` (whether plain HTTP is accepted on this installation) and `warnings` (the notes the text output gives). A refusal is a JSON error with the code `invalid_arguments`, `acknowledgement_required` (an address other computers reach without `--accept-insecure-http`; nothing was saved) or `state_unavailable`.

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

1. It picks the HTTPS port: 443 if free, otherwise 8443, otherwise 10000, or the one you choose ([Changing the HTTPS port](#changing-the-https-port)). Whatever is on other ports stays as it is. If every port it would try is taken, or an address already points at OwnGit without OwnGit's record of making it, it changes nothing. It then shows what is on each port with the command that removes it, and when you no longer need it, you can [replace it](#replacing-what-another-service-has-on-a-port); `owngit tailscale status` always shows these details, and the Settings page shows them only to an administrator. If your tailnet's access controls limit ports, allow the one OwnGit uses.
2. It adds the address to Tailscale's Serve settings, as `tailscale serve --bg --https=HTTPS_PORT http://127.0.0.1:PORT` would, and confirms that it points at OwnGit. Tailscale applies the change only if its Serve settings are still as OwnGit read them. If anything else changed them meanwhile, OwnGit changes nothing and asks you to try again.
3. It saves the HTTPS address as the base URL, the Tailscale name as an allowed Host and `127.0.0.1` as a trusted proxy, each only if not saved yet. Trusting `127.0.0.1` also trusts other programs on this computer that forward requests; see [Behind a reverse proxy](#behind-a-reverse-proxy).
4. It decides the listen address. Tailscale connects through `127.0.0.1`, so a listen address that accepts that, such as the default `127.0.0.1:7654` or `0.0.0.0:7654`, is kept. If OwnGit listens only on its Tailscale address, turning on saves `127.0.0.1:PORT` instead, and from the next start devices reach OwnGit only through the HTTPS address. A new listen address applies at the next start.

OwnGit never opens the home network on its own. Only the "Also allow on the home network (not encrypted)" checkbox, or `owngit tailscale on --home-network`, saves `0.0.0.0:PORT`, which counts as accepting plain HTTP. When the running OwnGit was started with `--listen`, that option decides where it listens, and the page says so.

#### Pages move to the HTTPS address

While sharing is on and ready, a browser that opens a dashboard page at the Tailscale name on OwnGit's own port, such as `http://NAME.TAILNET.ts.net:7654/settings`, goes to the same page at the HTTPS address. Only pages move: Git, the API, `/healthz`, setup, raw files and archives, forms and requests with a password are answered where they were sent. A page opened by an IP address, `localhost` or another name stays on plain HTTP, because that browser may not reach the Tailscale name. Browsers do not keep the redirect, so it stops within seconds once sharing is off or not ready.

#### Turning off

Turning off removes the Tailscale address only if it is still exactly as OwnGit made it, and again only if Tailscale's Serve settings are still as OwnGit read them. Otherwise it changes nothing, and the Settings page and `owngit tailscale status` show the `tailscale serve` steps that put the port back or clear it. It then restores the base URL saved before, removes the allowed Host and trusted proxy it added, and leaves the listen address as it is.

Turning off needs Tailscale to answer, so it is not offered while Tailscale is stopped, signed out or older than 1.50. On a page opened at the HTTPS address, turning off ends with a short page that gives OwnGit's address on this computer instead.

#### Changing the HTTPS port

Choose the port under HTTPS port in "Share on your tailnet over HTTPS" on the Network tab:

- **Automatic** (the default) uses 443, or 8443 or 10000 when something else is on 443. While sharing is on, it keeps the port sharing uses now.
- **Custom** uses the port you enter, from 1 to 65535.

On the command line, run `owngit tailscale on --https-port 8443`. Without `--https-port`, the command keeps the port sharing uses now, or picks one as Automatic does.

While sharing is on, saving another port moves it. OwnGit checks that the new port is free, turns sharing off at the old port and turns it on at the new one. The old address stops working, so each clone that uses it needs the new address:

```sh
git remote set-url origin https://NAME.TAILNET.ts.net:8443/git/project.git
```

If the new port is taken, nothing changes. If sharing was turned off at the old port and Tailscale then refuses it on the new one, sharing stays off and the message says why; save a port again to turn it back on. Some other failures at that point, such as settings that cannot be saved or an answer from Tailscale that never arrives, can leave Tailscale already serving OwnGit on the new port. The last case under [Replacing what another service has on a port](#replacing-what-another-service-has-on-a-port) says how to finish or undo that. If your tailnet's access controls limit ports, allow the new one.

#### Replacing what another service has on a port

Choosing a free port keeps everything else on Tailscale working, so try that first. If you no longer need what Tailscale serves on a port, OwnGit can take its place there:

1. Review what is on the port. When no automatic port is free, a confirmed administrator sees each taken port on the Network tab under Replace what is on a port, with what Tailscale serves there, and `owngit tailscale status` lists the same with the command that replaces each. When an automatic port is still free, for example 443 is taken and 8443 is free, nothing is listed. Then choose the taken port as a Custom port in Settings, or run `owngit tailscale on --https-port PORT`. OwnGit refuses and shows what is on that port, with the way to replace it.
2. Choose Replace this endpoint and enter the administrator password, or run the command shown: `owngit tailscale on --https-port PORT --replace-endpoint DIGEST`. `DIGEST` identifies what you reviewed.

OwnGit replaces only what you reviewed. If anything Tailscale serves changed after your review, on that port or any other, the port is not replaced. When OwnGit finds the change before its first write, it writes nothing. Sharing stays as it was, on or off, and the port is shown again for a new review. Only that port changes; other ports, names and Funnel stay as they are. OwnGit never replaces a port open to Funnel, which would make OwnGit public, or a port held by a `tailscale serve` running in a terminal.

When sharing is on at another port, the replacement also moves it, and OwnGit first clears its old port as in any move. What happens when a step after that fails depends on the cause:

- If Tailscale's configuration changed after OwnGit cleared the old port, or Tailscale refuses the write on the chosen port, that port is not replaced and sharing is off. The message says so ([Changing the HTTPS port](#changing-the-https-port)).
- After any other failure, for example when OwnGit cannot save its settings or does not get Tailscale's answer, Tailscale may already serve OwnGit on the chosen port although OwnGit has not confirmed it. The error says that Tailscale may already have the change, and `owngit tailscale status` says that turning sharing on did not finish. Run `owngit tailscale on` to finish the change, or `owngit tailscale off` to undo it.

#### Details and troubleshooting

- If OwnGit was started with a `--base-url` option, that option still decides clone addresses; remove it and restart.
- If turning on was interrupted, the switch stays on, and saving it turns sharing on again.
- After a rename (in the admin console or with `tailscale set --hostname NAME`), turn sharing on again for the new name. The old name stays in the certificate log. Tailscale also keeps an address under the old name that answers for nothing, which the Settings page and `owngit tailscale status` show with the `tailscale serve` steps that remove it.
- With the Tailscale app for macOS (as opposed to Homebrew's `tailscaled`), Tailscale runs only while someone is logged in, so after a restart HTTPS works once someone logs in. Turn on automatic login, or use Homebrew's `tailscaled`, which runs without a login. The Settings page says so when it detects the app.
- `owngit serve --tailscale PATH` and `owngit tailscale --tailscale PATH` name a `tailscale` command that OwnGit does not find on its own. OwnGit does not run that command. It reads Tailscale's status and Serve settings where that command reaches Tailscale without options, so a `tailscaled` started with its own `--socket` is not supported.
- OwnGit reaches Tailscale through a Unix socket only when the system reports that the program listening on the socket runs as root, or on Synology DSM 7 as the Tailscale package's `tailscale` account. Linux, macOS and FreeBSD report this. On other systems, and for a socket that another account serves, OwnGit reports `untrusted_socket`.
- `--json` prints the report, or a failure with a code, as JSON.
- OwnGit never resets Tailscale Serve or turns on Funnel, and keeps every other Serve setting as it is. It refuses every request that carries the `Tailscale-Funnel-Request` header, so the address cannot be opened to the Internet through Funnel. The public address for share links is a separate listener that you connect to Funnel yourself ([A public address for share links](#a-public-address-for-share-links)). It ignores `Tailscale-User-*` headers: passwords still decide who can read, write and administer.
- The sharing record belongs to this installation host, like the network settings, and an offline backup does not carry it.

### Other private networks

Over NetBird, Headscale with the Tailscale client, or plain WireGuard, let OwnGit listen on this computer's address in that network, use the name other devices use as the base URL, and restart:

```sh
owngit network set --listen 100.64.0.7:7654 --base-url http://gitbox.netbird.selfhosted:7654 --accept-insecure-http
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

Until you tell OwnGit that the proxy is trusted, it treats every client as the proxy. Wrong passwords from one device then pause every device ([Login attempt limits](#login-attempt-limits)), cookies are not marked `Secure`, and forms sent over HTTPS fail the Origin check.

`--trusted-proxy` takes the address the proxy connects from:

- `127.0.0.1` when it runs on this computer;
- the container's Docker network range, such as `172.18.0.0/16`, when it runs in Docker here;
- the other computer's address when it runs there.

OwnGit trusts no proxy by default. It refuses ranges wider than `/8` for IPv4 or `/32` for IPv6, and the unspecified addresses `0.0.0.0` and `::`. A range trusts every computer in it, so keep it small. Trusting `127.0.0.1` also trusts every program on this computer that forwards requests, such as an `ssh -L` tunnel. `owngit serve --trusted-proxy ADDRESS` replaces the saved list for one run, and `--trusted-proxy ""` trusts none.

When the proxy runs on this computer, keep OwnGit on `127.0.0.1:7654` so that other devices reach it only through the proxy. A proxy in a container cannot reach `127.0.0.1` on the host unless it uses the host's network; otherwise let OwnGit listen on an address the proxy can reach, and trust that address. When OwnGit listens on a network address, other devices can also connect directly over plain HTTP. To stop that, let only the proxy reach OwnGit's port, for example with a firewall rule.

By default each Git request can send or receive up to 4 GB and take up to 30 minutes ([Git transfer limits](#git-transfer-limits)), so the proxy's limits must be at least as large as OwnGit's. Pushes and clones were tested through the four proxies below with these settings.

#### Headers OwnGit reads from a trusted proxy

From a trusted proxy, and only from one, OwnGit reads three headers. `X-Forwarded-Proto` and `X-Forwarded-Host` must each arrive once with a single value; OwnGit ignores either one when it is repeated, holds a comma-separated list, or has a value not described below. Several `X-Forwarded-For` lines are joined into one list in their order. The `Forwarded` header is ignored.

- `X-Forwarded-Proto`, exactly `https` or `http`. With `https`, OwnGit marks its cookies `Secure`, checks forms against the `https` address, shows the connection as encrypted, skips the plain-HTTP acknowledgement, and tells Git that the request came over HTTPS. When requests pass through two proxies, the one nearest OwnGit must pass on the `X-Forwarded-Proto` it received, as nginx does with `$http_x_forwarded_proto`, instead of its own `http`.
- `X-Forwarded-For`, read from its last entry backward. OwnGit skips entries that are trusted proxies and takes the first address that is not one as the client's address, so when requests pass through more than one proxy, trust every proxy they cross. OwnGit uses that address for password lockouts and the setup approval warning, so devices behind the proxy lock out separately. Each proxy must add the address it received the request from; a proxy that passes the client's header through lets clients choose their lockout address. When the header is missing, an entry is not an IP address, or every entry is a trusted proxy, the original address is unknown. Lockouts then count against the nearest proxy, and setup shows "Forwarded request, original address unknown".
- `X-Forwarded-Host`, when OwnGit accepts both that Host and the request's own Host. The examples below pass the original Host instead.

#### Pages move to the base URL

Once the proxy has passed OwnGit an HTTPS request for the base URL, a browser that opens a dashboard page directly at the base URL's host on OwnGit's port, such as `http://git.example.internal:7654/`, goes to the same page at the base URL. Only pages move, as for [tailnet sharing](#share-on-your-tailnet-over-https).

- When the base URL itself is an IP address or `localhost`, such as `https://192.168.1.5`, a page opened at that address on OwnGit's port moves too. Pages opened by any other name or address stay on plain HTTP.
- OwnGit waits for such a request again after each start and each time tailnet sharing is turned on or off.
- If the proxy stops, pages opened this way keep moving to the base URL until OwnGit restarts or the base URL changes, so open OwnGit by another name or address meanwhile.
- A request that the proxy passes on with the client's address (`X-Forwarded-For` or `Forwarded`) never moves, because the proxy decides its scheme. A browser on this computer that opens such a page directly still moves, even when this computer's address is a trusted proxy.

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

Turn the check off on the General tab of Settings, under Update check, and save with the administrator password, or run `owngit settings set --update-check off`. The setting belongs to this installation host and is not in backups. For a deployment that must never check, start the server with `--no-update-check`, which wins over the saved setting:

```sh
owngit serve --no-update-check
```

## Host-owner recovery

Both procedures need access to the installation host; OwnGit has no email or account recovery. They are commands only, because they are for when the dashboard cannot be used.

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

Every command that reads a password or token file you wrote yourself, including `reset-admin`, `settings`, `import`, `pr` and `repo`, requires a regular file that only your account can read. OwnGit never accepts a password as a command-line value. A command that sets a new password reads it from such a file or asks for it at a hidden prompt. When it refuses a file, it says which accounts can also read it and gives the command that fixes it.

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

A restore changes only Git content in OwnGit, never another computer's working tree. It never rewrites history, so it also works on a [protected default branch](#changing-the-default-branch) and adds nothing to kept history.

A selected-file restore keeps unselected files, modes, binary files and symbolic links as they are, and never follows links on the host. It refuses a submodule, or a path whose replacement would remove unselected files beneath it.

OwnGit also refuses a target branch whose name some file systems treat as the same as another branch's, such as `Main` beside `main`. Delete or rename one of the two first. On the command line and through MCP, the refusal (`invalid_restore`) names the other branch.

#### Restoring on the command line

The command line and the [MCP server](CODING_TOOLS.md#mcp-server) offer the same restore, with the same general access as the restore pages. Preview first, then apply what you previewed:

```sh
owngit repo kept-history --repository NAME
owngit repo restore preview --repository NAME --source OID --target main
owngit repo restore apply --repository NAME --source OID --target main --expected-head OID
```

- `repo kept-history` lists kept history as JSON. Each entry has the branch or tag it came from, its commit, and in `restore_target` the new branch the dashboard offers to restore it into.
- `repo restore preview` shows what restoring the whole tree of the source commit (`--source`, a full commit ID) onto the target branch would do. `--target` takes a branch name such as `main`, not a full ref name starting with `refs/`. To restore only some files, add `--path FILE` once for each. The preview changes nothing. It lists every path the restore adds, changes or deletes, with its Git modes before and after (`120000` is a symbolic link), and gives the branch tip it was made against as `expected_head`.
- `repo restore apply` takes the same options and `--expected-head` set to that value. If the branch moved after the preview, apply is refused with `stale_revision` and changes nothing; preview again.

### Kept history

When a force push, an import or a deletion replaces the commits of a branch or tag, OwnGit keeps the previous commits as kept history, which you can browse and [restore](#restoring-repository-files). This is the default, and an administrator can turn it off:

- For every repository that follows the server: Overwritten and deleted history under Kept history on the Settings Repositories tab, or `owngit settings set --kept-history off`.
- For one repository: Kept history on the repository's Settings tab (Follow the server setting, Keep or Do not keep), or `owngit repo settings set --repository NAME --kept-history off` (`default` follows the server). A repository's own choice applies whatever the server's is.

A change applies to pushes and imports that start after you save; a run already going keeps the choices it started with. With Do not keep, the commits replaced from then on are not kept, so they are not listed in kept history and cannot be restored from it. They may still open by their full commit ID. History kept before stays and can still be restored; nothing is deleted. Fast-forward pushes, merges, restores, backups and repository deletion work the same either way.

`owngit repo settings show --repository NAME` prints a repository's choices as JSON, with `kept_history_now` for what it does now. Both `repo settings` commands need a `--password-file` holding the administrator password. Inside a clone of the repository they take `--server` and `--repository` from its `origin` remote, and the password file must then name that server on its first line ([Credential files and the server line](CODING_TOOLS.md#credential-files-and-the-server-line)). If a repository's saved choices cannot be read, pushes and imports to it are refused until you save them again; on the command line, give both `--kept-history` and `--protect-default-branch`. While the server-wide choice cannot be read, any change that leaves a repository following it is refused and saves nothing, including turning protection on or off. Choose Keep or Do not keep for the repository in the same change, or set the server choice again.

### Changing the default branch

The default branch is the one OwnGit and `git clone` open first (the repository's `HEAD`). An administrator picks any existing branch in the repository's Settings tab, or runs `owngit repo default-branch --repository NAME --branch BRANCH` with the administrator password in `--password-file`. Inside a clone of the repository, `--server` and `--repository` come from its `origin` remote. An imported repository whose only branch is `master` shows no default branch until you choose one. Changing it creates no branch and leaves every ref and kept history as it was.

To keep the default branch from being rewritten or deleted, turn on Protect the default branch on the repository's Settings tab, or run `owngit repo settings set --repository NAME --protect-default-branch on`. It is off by default. While it is on, OwnGit refuses a push that is not a fast-forward of the default branch and a push that deletes it, and Git shows `remote: OwnGit protects the default branch main and refused ...` with `! [remote rejected]`. Pushes that add commits, every other branch and tag, merging a pull request, restoring files and deleting the repository work as before. An import still follows a fast-forward of the default branch. When the source rewrote it, or when [Overwrite diverged branches](#overwrite-diverged-branches) would replace a local change to it, the refresh fails with `protected_default_branch` and changes nothing; to follow the source, turn the protection off and refresh again. The protected branch is the one HEAD resolves to, also when HEAD reaches it through another symbolic ref. Changing the default branch moves the protection to the new one.

A new repository starts on `main`. To start new repositories on another branch, such as `trunk`, change Initial branch under New repositories on the Settings Repositories tab, or run `owngit settings set --initial-branch trunk`. The name uses up to 100 letters, digits, `-`, `_`, `.` and `/`, and must be one Git accepts. It applies to repositories created afterwards, in the dashboard, on the command line or through the API. Existing repositories keep their branches, and an import takes its source's default branch. The page of an empty repository shows the `git push` command for its branch.

### Other ref namespaces

A push may change branches (`refs/heads/`) and tags (`refs/tags/`). To let one repository accept other refs as well, such as Git notes under `refs/notes/`, an administrator lists their namespaces under Other ref namespaces on the repository's Settings tab, one per line, or runs:

```sh
owngit repo settings set --repository NAME --extra-ref-prefixes refs/notes/,refs/meta/
```

None is listed by default, and an empty value removes them all. A change applies to pushes that start after you save.

- Each namespace starts with `refs/` and ends with `/`, such as `refs/notes/`. It uses only ASCII letters, digits, `-`, `_`, `.` and `/`, and is at most 100 characters long. A repository lists at most 32.
- OwnGit refuses a namespace inside or around `refs/heads/`, `refs/tags/`, its own `refs/owngit/` or another listed namespace, in any letter case. It also refuses two namespaces whose shared folder is spelled in different letter case, such as `refs/notes/a/` and `refs/Notes/b/`.
- Refs in these namespaces have no kept history and no protection. A push can overwrite or delete them, and their earlier value is not kept. OwnGit still refuses a ref whose name some file systems treat as the same as an existing ref's.
- A push to any other ref is refused with `OwnGit accepts branches, tags and the ref namespaces listed in the repository's settings.`
- A push that would update a symbolic ref other than `HEAD` is refused, in any namespace, with a message to push the ref it points to directly.
- If the saved list cannot be read, pushes to the repository are refused until you save the list again.
- Backups carry the list and the refs in these namespaces, and a restore brings both back. The list does not affect imports, which have their own [extra ref namespaces](#extra-ref-namespaces).

`owngit repo settings show` prints the list as `extra_ref_prefixes`, and the repository settings API takes the same field, which replaces the whole list. Anyone with general access can see where a push may go: `owngit repo show` and the MCP tool `repository_show` list the accepted namespaces in `push_ref_namespaces`, or say in `push_ref_namespaces_error` why the list cannot be read.

### Renaming a repository

An administrator can give a repository a new name. Its pages and clone address move to the new name, and everything in the repository stays. For 90 days the old address leads to the new one, so existing clones keep working while you update them.

Rename in one of these places:

- On the dashboard: open the repository's Settings tab, type the new name under Name and address, and choose Rename. The form asks for the administrator password when [Settings require it](#administrator-password-check).
- On the command line: `owngit repo rename NAME NEW-NAME --server URL --password-file ADMIN-PASSWORD-FILE`. The file holds the administrator password, and the command prints the renamed repository as JSON.
- Through the API: `POST /api/v1/repositories/NAME/rename` with the body `{"name":"NEW-NAME"}`, signed in with Basic authentication as `admin` and the administrator password.

Then point every clone at the new clone address, which the repository's page shows:

```sh
git remote set-url origin http://HOST:7654/git/NEW-NAME.git
```

Do the same for [runners and helpers](#runners-and-helpers-after-a-rename) that name the repository.

#### What a rename changes

The addresses of the repository's pages and its clone address use the new name in lowercase. The name shown on the dashboard keeps the letter case you typed, so `Tools-2` is shown as `Tools-2` and answers at `tools-2`. Changing only the letter case changes the shown name and keeps the address.

The files, history, pull requests, checks, imports, kept history and activity stay as they are. So do the storage folder and the repository ID, which is the lowercase name the repository was created with.

#### The old address

For 90 days after a rename, the old address leads to the new one:

- Pages and the API answer with a redirect to the same page or request at the new address.
- Git follows the redirect, so existing clones keep fetching and pushing. Git prints `warning: redirecting to` with the new address each time.
- The `owngit` commands and MCP tools that use general access or the administrator password do not follow it. Until you update `origin` or `--repository`, they stop with `repository_moved` and change nothing; `details.address` gives the new name. Runners and helpers work differently, as [described below](#runners-and-helpers-after-a-rename).

The Settings tab lists each earlier address that still leads to the repository, with the time it stops. After that time the old address answers 404, like a repository that does not exist.

An expired earlier name is free again, and a new repository or a rename can take it. From then on, a clone that still uses the old address reaches the repository that took the name: a fetch gets its content and a push writes to it. Update remotes with `git remote set-url` within the 90 days to avoid this. The first name is an exception. It stays the repository's ID, and no other repository can take it while this one exists, although the address still answers 404 once its 90 days end.

#### Runners and helpers after a rename

[Runner](AUTOMATIC_CHECKS.md) and [helper](CODING_TOOLS.md) credentials keep working after a rename, because they belong to the repository and not to its name. A runner or helper set up with the old address, through `--repository` or a clone's `origin`, keeps working at that address for 90 days. Switch it to the new name before then, for example `owngit runner --repository NEW-NAME`, because it stops at the old address when the 90 days end. From then on the old address refuses it as a credential of another repository, the same answer it gets at a name that does not exist, so a credential never tells whether a name exists.

If another repository later takes the old name, a credential of the renamed repository is refused there. When you issue a new credential, the output names the repository's current address in `repository_address`.

#### When a rename is refused

OwnGit refuses a name that another repository uses in any letter case: as its name, as its ID, or as an earlier name that still leads to it. It also refuses the reserved names `new` and `new-import`, and names that break the [naming rules](#repository-folder).

A rename is also refused while the repository is busy: an import or a check runs, a Git operation such as a push or clone holds it, maintenance runs, or a backup is reading it. Try again once that finishes. A refused rename changes nothing.

#### More about renaming

- You can rename a repository back to its first name at any time. That name becomes its address again.
- With shared-password protection, someone without the password learns nothing about a rename. The old page leads to sign-in, and the API and Git ask for the password before they redirect.
- A Git client set with `http.followRedirects=false` does not follow the old address. It fails with `The requested URL returned error: 307`; update its remote.
- Backups keep each repository's name and earlier addresses, and a restore brings them back with the same end times.
- The Settings tab can rename a repository only when OwnGit can read its Git data. When it cannot, use `owngit repo rename` or the API.

### Share links

A share link lets someone without an account read one repository, such as a recruiter, a contractor or a reviewer. Nothing is shared until an administrator creates a link.

An administrator creates and revokes links under Share links on the repository's Settings tab. Each link has:

- a **name**, which only administrators see, such as the person or company it is for (one line, at most 100 bytes);
- an **access**: *Browse files and history*, or *Browse, and clone with Git*;
- an **expiry**: after 1, 7, 30 (the default) or 90 days, or *Until revoked*. The command line and the API also take any number of days from 1 to 3650;
- an optional **extra password** (8 to 1024 characters) that visitors type before they see anything.

After you create a link, OwnGit shows its address once, such as `https://HOST/share/SECRET`. OwnGit keeps only a SHA-256 fingerprint of the secret, so it cannot show the address again; if it is lost, create a new link and revoke the old one. The list shows each link's name, access, expiry, last use and state (active, expired or revoked), and the start of the address its visitor pages use (`/share/ID`). Revoking stops a link at once. Revoked and expired links stay in the list as a record.

OwnGit warns, without refusing, when a link has no expiry, when it allows cloning (a copy cannot be taken back by revoking the link) and when it has no extra password.

**What a visitor sees.** Opening the address sets a cookie for that link in the browser and moves to `/share/ID`, so the secret leaves the address bar. The visitor sees the repository's overview, files, README, commits of its branches and tags, and raw files, within the browsing limits. A visitor never sees other repositories, the dashboard, activity, pull requests, checks and their logs, kept history, import details, settings or any control that changes something. A commit that only kept history or a ref outside branches and tags holds is not found. An unknown, expired or revoked link answers *not found*. With an extra password, a wrong password says so, and wrong passwords are counted per address under the [login attempt limits](#login-attempt-limits), apart from sign-in, so they never pause your own sign-in.

**Cloning.** A clone link's page shows its Git address, `https://HOST/share/ID.git`. Git asks for a user name and password:

- without an extra password, the password is the part of the share link after `/share/`, and any user name works;
- with an extra password, the user name is the part after `/share/`, and the password is the extra password.

Clone and fetch get branches and tags only, never refs in [other ref namespaces](#other-ref-namespaces) or kept history, whatever the repository's own Git configuration allows. A push is refused. A wrong credential gets the same answer as any failed Git sign-in.

**Renames, deletion and backups.** A link names its repository, not its address, so it keeps working after a rename. Deleting the repository deletes its links. Share links are not in backups: after a restore there are none, so create new ones.

**Where the secret can appear.** Only the first request, `/share/SECRET`, carries the secret in its path. OwnGit does not log it and answers with a redirect to `/share/ID`; no later page, link or `Referer` header carries it (answers under `/share/` send `Referrer-Policy: no-referrer`, also refusals and errors; the extra password form, whose address holds no secret, sends `same-origin`). A reverse proxy in front of OwnGit may still log that first path, so check its access log settings before sending links through it. For Git, the secret is part of the sign-in (the password, or the user name when the link has an extra password), so Git's credential helper may store it with the rest of the clone's credentials.

On the command line, with the administrator password in `--password-file`:

```sh
owngit repo share list   --repository NAME
owngit repo share create --repository NAME --label "Reviewer" [--scope browse|clone] [--days 30 | --until-revoked] [--link-password-file PATH]
owngit repo share revoke --repository NAME --id ID
```

Inside a clone of the repository, `--server` and `--repository` come from its `origin` remote. `create` prints the link once, with its secret, in `url`, and for a clone link `clone_url` and `clone_sign_in`; `list` never prints a secret. The extra password is read from an owner-only file ([Password and token files](#password-and-token-files)). The owner API is `GET` and `POST /api/v1/repositories/NAME/share-links` (`label`, `scope`, `expires_in_days` or `until_revoked`, `password`) and `POST /api/v1/repositories/NAME/share-links/ID/revoke`, with the administrator password. The MCP server does not manage share links.

#### A public address for share links

OwnGit itself stays private, but you can give share links a second, public address, for example with Tailscale Funnel or a reverse proxy on the Internet. That address answers share link pages, share link clones and the few files those pages load (the stylesheet, its font, the script and the logo). Every other path there answers `404 page not found`, the same way for each: the dashboard, sign-in, setup, Settings, the API, `/git/` and every repository without a link. That address accepts requests through Funnel; OwnGit's own address keeps refusing them.

The public address is off by default. To turn it on, choose a free port on this computer and the address visitors will use, then save both:

- In Settings, on the Network tab, open *Public address for share links*, fill in *Listen address* (such as `127.0.0.1:7655`) and *Public URL* (such as `https://box.tail1234.ts.net:8443`), and save.
- Or run `owngit network set --public-share-listen 127.0.0.1:7655 --public-share-url https://box.tail1234.ts.net:8443`.

Both values are needed, and the listen address needs a port other than OwnGit's own. The change applies at the next start of OwnGit, like the other network settings; Settings and `owngit network show` say when a restart is needed, and `network set --json` answers `applies_at_next_start` and `restart_needed`. If the port is taken at start, OwnGit still starts on its own address, logs why, and shows the reason on the Network tab and in `owngit network show`. To turn it off, empty both fields or run `owngit network set --public-share-off`; `owngit network reset` turns it off too.

OwnGit warns when you turn it on: anyone on the Internet reaches that address, and anyone who has a link can use it there until the link expires or you revoke it. A public URL that starts with `http:` needs the same plain HTTP acknowledgement as a listen address other devices reach (the checkbox in Settings, `--accept-insecure-http` on the command line), and OwnGit warns that links, extra passwords and repository content then cross the Internet unencrypted. It also warns when the listen address reaches beyond this computer, because anyone on that network then reaches it directly over plain HTTP, and when no reverse proxy is trusted. Add the address the tunnel or proxy connects from as a trusted proxy (127.0.0.1 for Tailscale Funnel on this computer). Otherwise every visitor counts as that one address for wrong extra passwords, and the share cookie is not limited to HTTPS.

On the public address, a form such as the extra password is accepted only from the public URL or from the listen address itself, whether the proxy passes the visitor's host name on or replaces it. Links are the same on both addresses. A new link shows its public address and, for a clone link, its public Git address next to the usual ones, in the dashboard and in the `public_url` and `public_clone_url` fields of the API and `owngit repo share create`. Revoking a link or its expiry ends it on both addresses at the next request, for pages and for Git.

OwnGit does not change Tailscale for this. With Tailscale Funnel, point Funnel at the listen address yourself, on a port Tailscale Serve does not already use for OwnGit (Funnel offers 443, 8443 and 10000):

```sh
tailscale funnel --bg --https=8443 http://127.0.0.1:7655
```

The first time, Tailscale may ask you to allow Funnel for your tailnet, and it gets the HTTPS certificate when the address is first used. `tailscale funnel --https=8443 off` removes it again. As with any reverse proxy, the tunnel or proxy may log the first request of a link, `/share/SECRET`, which carries the secret.

### Deleting a repository

An administrator deletes a repository with Delete repository, at the end of the repository's tabs. The page asks you to type the repository's name, unless that is turned off (see below), and asks for the administrator password when Settings require it. You choose what happens to the files:

- **Remove from OwnGit and keep the files** moves the bare repository, unchanged, to `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git` inside the repository folder (`ID` is the repository ID, the lowercase name it was created with, and the time is UTC). Its branches, tags and kept history stay there until you remove the folder yourself. Folders under `.owngit-removed` are never listed as repositories and are not in backups.
- **Delete the files too** deletes the bare repository, including its kept history. Earlier backups still contain it, and the database space its records used is freed but not securely erased.

Either way, deleting removes the repository's pull requests, reviews, tasks, check settings, jobs and results, helper and runner credentials, share links, and import settings and credentials. Queued check jobs are dropped, and the name is free again.

Typing the name is on by default. To delete without it, choose Do not ask under Deleting a repository on the Settings Repositories tab, or run `owngit settings set --delete-requires-name off`. The Delete page then shows the repository's name and asks you to check it, and a mistaken deletion no longer needs the name. `--delete-requires-name on` asks for it again. If the saved choice cannot be read, OwnGit deletes nothing and the Delete page says how to set it again.

To bring a kept repository back, create an empty repository with the same name in the dashboard. Then run the push command the dashboard shows for the kept folder, on the computer where OwnGit runs. For another name, use its URL:

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

This command brings back only branches and tags; kept history, pull requests and checks do not come back. To bring back refs such as `refs/notes/` as well, first list their namespace under [Other ref namespaces](#other-ref-namespaces) of the new repository, then add a matching refspec such as `'refs/notes/*:refs/notes/*'`. If the kept repository's main branch is not `main`, change the default branch afterwards.

#### Deleting on the command line

`owngit repo delete` deletes a repository as the Delete page does, with the administrator password:

```sh
owngit repo delete --server http://HOST:7654 --accept-insecure-http --password-file /path/to/admin-password \
  --repository NAME --files keep --confirm-name NAME
```

- `--repository` is always required. Unlike `repo settings` and `repo default-branch`, this command never takes it from the `origin` remote of a clone, so running the command in the wrong folder cannot delete that folder's repository.
- `--files keep` moves the files to `.owngit-removed`, as above, and the answer gives their folder in `kept_path`. `--files delete` deletes them.
- `--confirm-name` repeats the name while Settings ask for it. Without it, the command is refused with `name_mismatch` and deletes nothing.
- The JSON answer names the `repository` and the `mode`. `incomplete` is true when the repository is gone from OwnGit but its files are moved or deleted at the next start ([If OwnGit stops during a deletion](#if-owngit-stops-during-a-deletion)).
- A refusal has the same reasons as on the Delete page, such as `repository_busy` ([When deletion is refused](#when-deletion-is-refused)), and `setting_unreadable` when the name setting cannot be read.

#### When deletion is refused

The reason decides the next step:

- An import is running, a check job is claimed or running, or another Git operation (push, clone, restore or merge) holds the repository: try again once it finishes.
- A check container still waits for OwnGit to confirm its removal: a cleanup that failed is retried when OwnGit starts, so restart OwnGit after Docker is available again.
- The repository's Automatic checks page lists the container under Leftover check containers, for example after Docker was reset or reinstalled: forget its record as described next.

The Leftover check containers list shows the containers of finished jobs that OwnGit could not remove, because they were created on another Docker daemon or Docker was unavailable. Each entry gives the job, the container's name and ID, its Docker daemon and its label `com.owngit.check-job=JOB`. To release one:

1. Remove the container on the Docker daemon that ran it, or make sure that daemon no longer exists.
2. Tick "I removed this container, or its Docker daemon no longer exists" and choose Forget container.
3. Restart OwnGit to clean up the job's check workspace.

OwnGit removes no container; it only forgets the record. It refuses a record of the Docker daemon it uses now, because the next start of OwnGit removes that container itself, and a job that has not finished.

When the dashboard cannot be opened, or OwnGit is stopped, release the record on the OwnGit computer instead:

```sh
owngit forget-check-container --job JOB --confirm-container-removed
```

`JOB` is the job identifier from the list or the server log; add `--state-dir` for a non-default state directory. `--json` prints the forgotten record as JSON: `job`, `repository`, `container_name`, `container_id` when Docker assigned one, `daemon_id`, and `docker_unavailable` when Docker could not be checked. The command refuses the same records as the page, and a job without a record.

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

A push may change branches (`refs/heads/*`) and tags (`refs/tags/*`), plus the namespaces an administrator lists for that repository under [Other ref namespaces](#other-ref-namespaces), such as `refs/notes/`. Every other ref, and always `refs/owngit/`, is refused, so `git push --mirror` from another host's mirror clone fails for refs such as `refs/pull/*`. Pushing between two OwnGit installations carries neither kept history nor repository records; use a [backup](#backups) for those. To keep pulling from a host that stays in use, see [Importing from another Git host](#importing-from-another-git-host).

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

### All activity

All activity in the sidebar shows, for one year, how many commits each day has across every repository, and lists the commits of the year, or of one day when you pick it in the graph. The list shows at most the newest 1,000 commits and says so above the list when there are more; pick a day to see others. The graph still counts every commit it read. A commit's author name and date are what the commit records, not a verified identity. The count leaves out repositories that are still being prepared or whose Git data could not be read, and names the unreadable ones.

`owngit activity` prints the same as JSON, with general access like `owngit repo list`:

```sh
owngit activity --server https://owngit.example.test
owngit activity --server https://owngit.example.test --year 2025
owngit activity --server https://owngit.example.test --date 2026-09-29
```

The result has `year`, `date` (when given), `repository_count`, `total` (commits in the year), `complete`, `incomplete_reason` when `complete` is false (`counting`, `preparing`, `unreadable`, `preparing_or_unreadable` or `limit`), `unreadable` (repository names), `days` (each day with commits and its `count`), `entries` (newest first, with `repository` (the repository ID), `repository_name`, `repository_address` (where the repository answers now), `ref`, `ref_retained`, `oid`, `subject`, `author_name` and `author_date`) and `truncated`, which is true when more than 1,000 commits matched. A year outside 1970 to 9999, or a date that is not a day of the given year, fails with `invalid_request`. When no repository could be read the command fails with `activity_unavailable` instead of reporting zero commits. The API route is `GET /api/v1/activity`, with the optional query parameters `year` and `date`.

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

An import copies a repository from another Git host over HTTPS (or plain HTTP, when you allow it for that source) into a new OwnGit repository, and can refresh it later, on demand or on a schedule. Imports are inbound only: OwnGit never writes to the source, and Git LFS objects are not fetched or hosted.

In the browser, an administrator uses Import a repository on the dashboard to start one. The repository's Import tab then changes its source, credentials, [connection choices and limits](#connection-choices-and-limits) and [refs and refresh choices](#refs-and-refresh-choices), refreshes, cancels, and sets a schedule.

- Anyone who can read the repository sees the tab's status, run history and ref states. The source address, credential state, run messages and refresh choices are for administrators only.
- Changes ask for the administrator password as set under [Administrator password check](#administrator-password-check).
- The credential form changes only what you enter. A new token or Basic credential keeps a stored CA, and **No new sign-in (CA only)** keeps the credential; only Clear credentials removes them.
- The form is limited to 1 MiB, so store a CA bundle near that size with the command line.

Each import records a mode: **Standalone** (the copy is the primary one) or **Coexistence** (the other host stays authoritative). The mode is only a label; both follow the same [refresh rules](#what-an-import-publishes).

### Imports on the command line

The command line offers the same operations, except changing the source URL, mode or consents of an existing import. It reads the administrator password from a file with the same checks as `reset-admin`, and a source token or Basic credential from a private file or a prompt, never from an argument or environment variable:

```sh
owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

Every import command takes the same `--server`, `--accept-insecure-http` and `--password-file` flags, omitted below. `--accept-insecure-http` is consent to reach OwnGit over plain HTTP for that command. It does not apply to the source, which has its own `--allow-plain-http` choice.

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
owngit import configure PROJECT --redirects same_origin
owngit import configure PROJECT --limit run_seconds=2h --limit pack_bytes=32GiB
owngit import configure PROJECT --extra-ref-prefixes refs/notes/
owngit import configure PROJECT --overwrite-diverged --follow-upstream-deletions
owngit import resolve PROJECT
```

- `--basic-file` replaces `--token-file` for a Basic credential (username and password on separate lines). `--ca-file` stores a source certificate authority, up to 1 MiB. `import credentials` changes only what you pass; `--clear` removes credential and CA. Output shows the credential type and whether one is stored, never the secret.
- `--allow-private-network` permits a source on a private LAN, CGNAT, tailnet or loopback address. `--git-only-consent` accepts a repository with Git LFS pointers ([Git LFS](#git-lfs)).
- `import add` and `import configure` take the source's [connection choices and limits](#connection-choices-and-limits) and its [refs and refresh choices](#refs-and-refresh-choices). `import configure` changes only the options you pass and keeps the address, mode and consents.
- `import add` creates the repository and refuses an existing name with `repository_taken`; `import refresh` updates from the stored source. Both wait for the whole run. The server stops a run at the source's run time (1 hour by default) and answers within about a minute after that. The command's own request limit is fixed at 24 hours and 2 minutes, the longest allowed run time plus margins, whatever the source's settings. They exit 0 on success, 3 when the run kept local refs that differ from the source (listed in the output), 130 when it was cancelled, and 1 otherwise.
- `import status` lists the last and active runs, the source's connection choices, changed limits and refresh choices, every imported ref that does not match the source, and the refs the refresh choices [would change now](#checking-what-the-choices-would-change). With `--json` it prints the status as JSON, including every limit in force.
- `import cancel` can stop a run only until its result is published; for a first import, that is the moment the repository appears. If the first import fails or is cancelled, OwnGit removes the source and credentials stored for that name (at its next start if it crashed), and a retry uses only what you supply.
- A schedule interval is between 60 seconds and 7 days (`invalid_schedule` otherwise), and scheduled refreshes run only while `owngit serve` runs.
- Every import command takes `--json` and then prints the server's answer as JSON instead of text, with the same exit status. `import status --json` prints the status itself; the other commands print the whole answer, such as `run` and `status` for `import add` and `import refresh`, or `enabled` and `interval_seconds` for `import schedule`. When the status after a saved change cannot be read, the answer has `status_error` instead of the warning the text prints. With `--json` an error is printed as JSON with `"ok": false` and a `code`.

### Source connections

The source URL must use HTTPS with TLS 1.2 or newer, an ASCII host name, and no username, password, query or fragment. An `http://` URL works only when the source [allows plain HTTP](#plain-http). IPv6 zone identifiers are not supported. By default OwnGit does not follow redirects, and it always ignores proxy environment variables, cookies and Git credential helpers.

OwnGit resolves the host name once and checks every address it gets back. If any address is not allowed, the run stops.

- Public addresses are allowed.
- Private LAN, CGNAT, tailnet and loopback addresses need private network consent (`--allow-private-network`, or Allow a private-network source in the browser).
- Other special-purpose addresses, such as the documentation and benchmarking ranges, need the source's [exceptional destination](#exceptional-destinations) choice.
- Link-local addresses (including the cloud metadata address 169.254.169.254), multicast and unspecified addresses, the local-use NAT64 range 64:ff9b:1::/48 and a few other reserved ranges are never reached, whatever the settings.
- An address in the well-known NAT64 range 64:ff9b::/96 needs the exceptional destination choice, and the IPv4 address it contains must be allowed as well.

The host of every redirect target is checked the same way. A custom CA adds to the system roots and never disables certificate or host name checks; a run that fails on the certificate says so.

### Connection choices and limits

Each import source has its own connection choices and limits. The defaults suit most sources, and every choice that loosens a protection is off until you turn it on for that one source. Change them in the collapsed **Connection and limits** group of the new-import form or the Import tab, with `owngit import configure`, or through the API. A change applies from the next run. Changing a connection choice (not a limit) also cancels a run in progress that has not published yet, which ends as `superseded`.

| Choice in the browser | Default | Command line | API field |
|---|---|---|---|
| Allow plain HTTP for this source | Off | `--allow-plain-http` | `allow_plain_http` |
| Redirects | Refuse redirects | `--redirects` with `refuse`, `same_origin` or `approved` | `redirects` |
| Approved origin | None | `--approved-origin` | `approved_redirect_origin` |
| Allow this exceptional destination | Off | `--allow-exceptional-destination` | `allow_reserved_addresses` |

The API takes these fields, and a `limits` object, in `PUT /api/v1/repositories/{id}/import` and in the request that starts a new import (`POST /api/v1/repositories/{id}/import/run`). `PATCH /api/v1/repositories/{id}/import` changes only the fields it names. A source's JSON includes an `options` object with the choices, every limit in force (`limits`), the names of the limits you changed (`changed_limits`), and a `problem` when a saved setting cannot be used.

#### Plain HTTP

An `http://` source address is refused until you allow plain HTTP for that source, and the refusal names the choice. With plain HTTP on, the source's code and credentials travel unencrypted and can be read or changed on the way, so use it only on a network you trust. The same choice lets a redirect go from HTTPS to plain HTTP and lets the approved origin use `http://`.

#### Redirects

With the default, Refuse redirects, a run that meets a redirect stops and names the origin the source pointed to. Follow redirects within the same origin accepts a redirect to the source's own scheme, host and port. Also follow redirects to one approved origin accepts those and redirects to one other origin, which you enter under Approved origin as a scheme and host without a path, such as `https://mirror.example`. OwnGit saves that origin with a lowercase host and without a default port, and refuses a malformed one even while the redirect choice does not use it.

OwnGit follows redirects only on its first request, the one that lists the source's refs, and at most 5 in a row. The new address must still end in `/info/refs?service=git-upload-pack`, and every later request of the run goes there. A loop, a sixth redirect, or a redirect on any later request stops the run.

The source's credentials and custom CA go only to the source's own origin. Another origin gets no sign-in and is checked against the system certificate authorities alone. If that origin needs a sign-in, change the source address to it instead.

#### Exceptional destinations

Allow this exceptional destination lets the source connect to a special-purpose address that is normally blocked, such as one in a documentation range (192.0.2.0/24, 2001:db8::/32) or the benchmarking range 198.18.0.0/15. It does not cover private addresses, which still need private network consent. The addresses that [Source connections](#source-connections) lists as never reached stay blocked.

#### Limits

Limits bound how much a run may download and how long each stage may take. Raise one when an import fails on it; higher limits let an import use more disk and keep the server busy longer. The browser shows each limit's default and range beside its field, and an empty field uses the default. OwnGit stores only the limits you change, and setting a limit to its default value returns it to the default.

| Limit | In the browser | Default | Range |
|---|---|---|---|
| `pack_bytes` | Largest pack | 16 GiB | 1 MiB to 1 TiB |
| `run_seconds` | Run time | 1 hour | 1 minute to 24 hours |
| `fetch_seconds` | Download time, including indexing | 30 minutes | 1 minute to 24 hours |
| `index_seconds` | Indexing time | 20 minutes | 1 minute to 24 hours |
| `verify_seconds` | Verification time | 10 minutes | 1 minute to 24 hours |
| `refs` | Most refs listed | 50,000 | 1 to 200,000 |
| `advertisement_bytes` | Largest ref list | 16 MiB | 64 KiB to 64 MiB |
| `tls_handshake_seconds` | TLS handshake time | 15 seconds | 1 second to 10 minutes |
| `response_header_seconds` | Wait for response headers | 30 seconds | 1 second to 1 hour |
| `lfs_objects` | Objects checked for Git LFS | 200,000 | 1 to 1,000,000 |

The last four sit in the nested **Transfer and scan limits** group. The stage times must fit together: the download time and the verification time may not exceed the run time, and the indexing time may not exceed the download time, which includes indexing. A change that breaks this, or a value outside its range, is refused and names the limit; in the browser the message appears on that field.

The API takes sizes in bytes, times in seconds and counts as numbers. `owngit import configure --limit NAME=VALUE` takes the same numbers, and also sizes such as `32GiB` (KiB, MiB, GiB or TiB) and times such as `90m` or `2h`. Repeat `--limit` for more than one limit.

#### When the source address changes

Connection choices belong to one address. When a source's address changes, OwnGit turns plain HTTP, the exceptional destination, [Overwrite diverged branches](#overwrite-diverged-branches) and [Follow upstream deletions](#follow-upstream-deletions) off, sets Redirects back to Refuse redirects, and drops the approved origin. Limits, private network consent and extra ref namespaces stay.

On the Import tab, the page clears these choices as soon as you edit the address and says so. Enter the new address first, then choose again whatever it needs.

In a browser without JavaScript the page cannot clear them, so OwnGit sorts them out when you save. A choice you changed in the same save as the new address counts as consent for that address. A choice left as it was for the old address is dropped. If the save is refused, for example because a new `http://` address needs plain HTTP, the form comes back for the new address with those carried choices unchecked. Check the ones it needs and save again.

Through the API, a new URL resets these choices unless the same request sets them.

#### When a setting stops a run

When a source setting stops a run, the last-run message on the Import tab says the refusal was deliberate and names the setting to turn on. Administrators also find the refused address and its range, or the origin the redirect leads to, in the technical details; other readers see only the explanation. In the API and on the command line, these refusals have their own classes:

- `address_needs_private_network`: turn on private network consent.
- `address_needs_exceptional_destination`: turn on the exceptional destination.
- `address_refused`: no setting allows this address, for example a link-local or multicast one, so use another source address.
- `redirect_not_allowed`: choose a redirect option, and approve the other origin when the redirect leaves the source's origin.
- `redirect_needs_plain_http`: the redirect goes from HTTPS to plain HTTP, which needs plain HTTP for this source.

A redirect loop, too many redirects, or a redirect to an address that is not a Git repository is reported as `protocol`.

If a saved choice or limit can no longer be used, for example an approved origin with a path or a run time shorter than the download time, the Import tab and `import status` name the setting, and runs from that source stop before they connect. The same applies to a saved list of [extra ref namespaces](#extra-ref-namespaces). Save that setting again on the Import tab, or with `owngit import configure --redirects`, `--limit` or `--extra-ref-prefixes`, and runs resume. For the namespaces, saving an empty list works too.

#### Choices and limits stay on this machine

Backups do not include a source's connection choices and limits. After a [restore](#restoring-a-backup), every source starts from the defaults, so turn on again whatever a source needs, such as plain HTTP for an `http://` source. The [refs and refresh choices](#refs-and-refresh-choices) are in backups and come back with a restore.

### What an import publishes

Each run fetches a full copy into a private staging area. Before anything reaches the repository, it checks that every advertised ref it imports, and HEAD, is present with a complete object graph.

- Branches and tags are published, and so are the refs of any [extra ref namespaces](#extra-ref-namespaces) you list, such as `refs/notes/`. Refs in namespaces you did not list are skipped. Pull request refs, for example, are skipped unless you list `refs/pull/`. A HEAD outside `refs/heads/` is refused.
- Hooks and configuration are not copied.
- A source with another object format (SHA-1 or SHA-256) fails, and a ref name longer than 417 bytes fails with `unsupported_refs`.
- OwnGit asks the source for Git protocol v2, which lets it ask only for HEAD, branches, tags and the extra namespaces, so refs in other namespaces, such as pull request refs, are not fetched. A source that lists more refs than the source's ref limit (50,000 by default) fails with `too_many_refs`. Every ref the source lists counts, including refs OwnGit then skips: a source without protocol v2 lists every ref, and a protocol v2 source may send refs OwnGit did not ask for. For such a source, raise the [ref limit](#limits) or [move it by hand](#moving-an-existing-repository-into-owngit) with a clone that pushes its branches and tags.

By default, a refresh never overwrites local work. Two [refresh choices](#refs-and-refresh-choices) change that, and the rules below say where.

- A missing ref is created, and an identical one is left alone.
- A branch follows the source only while it still holds the value OwnGit last saw from this source URL, or moved forward from it and the new source value includes it.
- A tag, or a ref in an extra namespace, changes only while it still holds the exact value last seen.
- Anything else is divergent and kept, and the run reports it. [Overwrite diverged branches](#overwrite-diverged-branches) replaces it instead.
- A ref deleted at the source stays locally (**Deleted at source** in the Import tab and `import status`). [Follow upstream deletions](#follow-upstream-deletions) deletes it instead.
- A source ref whose name, or any folder in its name, matches a local ref in the way described under [Moving an existing repository into OwnGit](#moving-an-existing-repository-into-owngit) is reported as divergent instead of created, and HEAD does not follow the source to such a name. `cafe` and `café` are different names.
- A replaced branch or tag value stays in kept history when kept history was on for the repository as the refresh started; with Do not keep it is not kept. A replaced value in an extra namespace is never kept.
- HEAD follows the source only when OwnGit set it on an earlier import from the same source and nothing changed it since.

After you change the source URL, OwnGit has not yet seen the new source's refs, so refs that differ are reported as divergent instead of being replaced, and no ref is deleted.

### Refs and refresh choices

Each source has three choices about what a refresh brings in and how closely it follows the source. By default a refresh brings in branches and tags, keeps what you changed in OwnGit, and only reports refs the source deleted. The choices sit in the **Refs and refresh** part of the collapsed **Connection and limits** group, on the new-import form and on the Import tab. They apply from the next refresh, and only administrators see them.

| Choice in the browser | Default | Command line | API field |
|---|---|---|---|
| [Extra ref namespaces](#extra-ref-namespaces) | None | `--extra-ref-prefixes refs/notes/,refs/changes/` | `extra_ref_prefixes` |
| [Overwrite diverged branches](#overwrite-diverged-branches) | Off | `--overwrite-diverged` | `overwrite_diverged` |
| [Follow upstream deletions](#follow-upstream-deletions) | Off | `--follow-upstream-deletions` | `follow_upstream_deletions` |

On the command line, `--extra-ref-prefixes=` clears the list, and `--overwrite-diverged=false` or `--follow-upstream-deletions=false` turns a choice off. The API takes these fields in the same requests as the [connection choices](#connection-choices-and-limits). There, `extra_ref_prefixes` is an array, `[]` clears it, and a field left out keeps its saved value. The source's `options` object reports all three, and the Import tab lists the ones you changed under Refresh.

The last two choices can change or delete local work, so the form warns "Local work may be replaced; upstream deletions will remove these local refs." `owngit import configure` prints the same warning in its text output when either is on after the change. Before you turn one on, [check what it would change](#checking-what-the-choices-would-change).

Changing any of the three stops a refresh in progress that has not published yet, which ends as `superseded`. A new source address turns both Overwrite diverged branches and Follow upstream deletions off and keeps the namespaces ([When the source address changes](#when-the-source-address-changes)).

#### Extra ref namespaces

An extra ref namespace makes an import bring in refs outside branches and tags, such as Git notes under `refs/notes/`. In the browser, enter one namespace per line; on the command line, separate them with commas.

- Each namespace starts with `refs/`, has a name after it and ends with a slash, such as `refs/notes/` or `refs/changes/`.
- A source can list up to 32, each once.
- A namespace inside, or containing, `refs/heads/`, `refs/tags/` or `refs/owngit/` is refused.

A refused list is not saved; the form shows the error on the field and keeps what you typed.

Refs in these namespaces are fetched, published and restored from backups like branches, and count against the same limits and name rules. Like a tag, such a ref follows the source only while it still holds the value last seen; the two choices below widen that. Kept history does not cover these namespaces, so the previous value of an overwritten or deleted ref there is not kept.

Removing a namespace from the list leaves the refs it brought in as they are. Later refreshes no longer change or delete them, or count them as deleted at the source. If the source still lists them, they still count toward the [ref limit](#limits). The Import tab and `import status` show them as **Namespace not imported** (`not_imported` in JSON).

The list affects imports only. Which refs a push may change is set separately under [Other ref namespaces](#other-ref-namespaces).

#### Overwrite diverged branches

With this choice on, a refresh replaces a branch, tag or extra ref that was changed in OwnGit since the source was last seen, instead of keeping it as divergent. It covers only refs that this source address has seen, so a ref you created only in OwnGit is never touched. The replaced branch or tag commit stays in kept history when kept history is on.

These stay as they are even with the choice on:

- The protected default branch. When overwriting would rewrite it, the refresh stops with `protected_default_branch` and changes nothing ([Changing the default branch](#changing-the-default-branch)).
- Symbolic refs, and a ref whose name clashes with another ref's spelling as described under [Moving an existing repository into OwnGit](#moving-an-existing-repository-into-owngit).
- A HEAD you changed in OwnGit.

#### Follow upstream deletions

With this choice on, a refresh deletes a ref that the source deleted. It does so only when all of these hold:

- this source address saw the ref, and so did a refresh made with the current sign-in ([After a sign-in change or a restore](#after-a-sign-in-change-or-a-restore));
- the ref still holds the value last seen, or Overwrite diverged branches is on too;
- the ref is in a namespace the import still brings in.

A deleted branch or tag commit stays in kept history when kept history is on; a deleted ref in an extra namespace is not kept. Once a ref is deleted, its name is free, and a ref you later create with that name is treated as local work.

A refresh never deletes:

- a symbolic ref;
- the branch HEAD points to, directly or through other symbolic refs, or the branch the source's HEAD points to in that refresh;
- a ref whose name matches another ref's apart from letter case or spelling, such as `refs/Notes/commits` when the import brings in `refs/notes/`;
- anything, when the source lists none of the refs the import brings in. An empty listing does not empty the repository.

#### After a sign-in change or a restore

A new sign-in may see fewer refs than the old one, so a ref it does not list may only be hidden from it. After a sign-in change, a refresh therefore deletes a ref only once a refresh with the new sign-in has seen it.

- Saving a different token or Basic credential, or clearing a stored one, is a sign-in change. Changing only the CA, or saving the same credential again, is not.
- A [restore](#restoring-a-backup) counts as a sign-in change for every source.

A ref the source deleted after the last refresh with the old sign-in, and before the first one with the new sign-in, is never deleted automatically. It stays as Deleted at source until you delete it yourself. Everything else continues as before: branches keep following the source, and Overwrite diverged branches still applies.

#### Checking what the choices would change

The Import tab lists, under **Refs these choices would change now**, every local ref that Overwrite diverged branches or Follow upstream deletions would change, whether the choice is on yet or not. `owngit import status` prints the same list. For each ref it says:

- whether it would be replaced with the source's value or deleted;
- which choice that needs (a ref the source deleted and you changed in OwnGit needs both);
- whether kept history keeps its current commit.

When default branch protection would stop overwriting, the list names that branch with **Refresh stops**. With Overwrite diverged branches on, a refresh stops there and changes nothing until you turn the protection off. The rest of the list then shows what following deletions alone would delete.

The list applies the refresh rules to the source as the last complete refresh saw it, and changes nothing. The next refresh decides against the source as it is then, so its result can differ. Before the first complete refresh the list is empty.

When the list cannot be worked out, the page and `import status` say so instead of showing an empty list. This happens while the repository is being written, when its storage cannot be read, or when a saved setting cannot be used. Reload the page or run the command again later.

In JSON (`import status --json` and the API), the list is `refresh_effects`. Each entry has `name`, `effect` (`replace`, `delete` or `refused`), `local_oid`, `local_changed` and `history` (`kept` or `not_kept`). `refresh_effects_unknown` is `true` when the list could not be worked out.

### When a refresh writes in two steps

A refresh usually writes all of its ref changes in one Git transaction, so other clients see all of them or none. A refresh that deletes refs, or rewrites a branch other than by a fast-forward while default branch protection is on, writes in two steps when it also updates the branch HEAD points to. It first writes every other change, then that branch.

While a refresh writes to the repository, clones and fetches through OwnGit wait, so they do not see the state between the two steps. A program that reads the repository folder directly can: it may find the other changes written while HEAD's branch still has its old value. If the second step fails, that state remains and clones and fetches see it too. The run then ends as [unresolved](#unresolved-publications) instead of complete.

While a refresh deletes refs, or rewrites a branch under default branch protection, OwnGit locks HEAD and every symbolic ref HEAD passes through. Another Git command that tries to change HEAD then fails with `cannot lock ref 'HEAD'`; run it again after the refresh. If HEAD was moved onto a ref the refresh deletes or rewrites just before those locks, the refresh stops before it writes anything, and the run is reported as unresolved.

### Git LFS

OwnGit scans the fetched objects for LFS pointer files, up to the source's LFS object limit (200,000 objects by default), 100,000 candidate files and 32 MiB of candidate content. If it finds one, or cannot finish within those limits, the run stops with `git_lfs_required`. With Git-only consent the import proceeds, keeps the pointer files as they are, and marks the content incomplete. OwnGit does not read `.gitattributes`, so a clean scan does not prove that a repository uses no LFS.

### Failures and cancellation

One run per repository is active at a time (`busy` otherwise), and a run is limited by the source's run time, 60 minutes by default (`limit`). Other outcomes are `cancelled`, `protected_default_branch` (see [Changing the default branch](#changing-the-default-branch)), `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved` and `nothing_to_resolve`. A failure OwnGit did not classify is `unclassified`; `unsupported` means the source or destination uses a feature that import does not support.

When `owngit serve` stops, it cancels running imports and waits up to 45 seconds for each to record its outcome. At the next start it marks interrupted runs and checks any publication that was in progress, without repeating or rolling back a write. If the import service cannot start, the Import tab and `import status` say so, and Git keeps working.

### Unresolved publications

A publication is unresolved when OwnGit cannot prove how it ended, for example when refs were written and HEAD was not, or when the second step of a [two-step refresh](#when-a-refresh-writes-in-two-steps) failed. Refreshes are refused until you accept the repository as it is:

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
owngit pr mergeability --number 1
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
- `pr mergeability`, or Check mergeability on the pull request page, tells whether the pull request can merge now, for its current source and target commits. It changes nothing, and OwnGit works it out only when asked: opening or listing pull requests never calculates a merge. `status` is one of these:
  - `clean`, with the `method` a merge would use: `fast_forward`, `merge_commit` or `up_to_date`;
  - `conflict`, with up to 100 `conflict_paths` (and `conflict_paths_truncated` when there are more), or with `reason` `no_merge_base` when the branches share no history;
  - `unavailable`, with a `reason` when OwnGit could not tell, such as `unsupported_git` or `source_branch_missing`;
  - `stale`, when the commits you passed are no longer current; `source` and `target` then hold the current ones.

  The answer is not stored and stops applying once either branch moves. To check whether an earlier answer still holds, pass its object IDs as `--source-oid` and `--target-oid`. A merge checks again by itself. On the page, a conflict answer turns off Merge until you open the page again.
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
- A push over the size is refused with HTTP 413, which Git may show only as `fatal: the remote end hung up unexpectedly`. A clone or fetch over a limit is cut off. A transfer whose client moves no data for the idle limit (1 minute by default) is stopped too. OwnGit does not host Git LFS, so a repository whose history is larger than the size limit cannot be cloned through it; keep large binary files out of Git history.
- One repository runs at most 4 Git requests at once. The server keeps 1 extra slot that only a repository with no transfer running may take, so one busy repository never makes the others wait, and at most 5 requests run in all. A request that finds no free slot waits up to 90 seconds, then gets HTTP 503 with `Git service is busy with other transfers; try again shortly`; run the command again.
- To change the transfers per repository, the extra slots, the idle limit or the wait for a slot, open Transfers at once and waiting under Git transfers on the Settings Repositories tab, or run `owngit settings set` with `--transfer-per-repository` (1 to 32), `--transfer-extra-slots` (0 to 32), `--transfer-idle` (10 seconds to 1 hour, such as `1m`) and `--transfer-queue` (5 seconds to 10 minutes, such as `90s`). The settings API fields are `per_repository`, `extra_slots`, `idle_seconds` and `queue_seconds` in `git_transfer`. A change applies to requests that ask for a slot after you save. Lowering a number never stops a transfer already running; new ones wait until enough have ended. Raising any of them above its default warns that transfers can hold slots longer and make other clients wait.
- Too many wrong passwords pause the address that sent them, and Git then gets HTTP 429 (Too Many Requests) with `Retry-After`. See [Login attempt limits](#login-attempt-limits).
- When OwnGit is stopped, it waits up to 10 seconds for running requests, then ends the rest and logs how many it ended.

## Browsing limits

Browsing limits cap how much one page reads to show a file, a diff or a pull request comparison, so a very large file or change cannot tie up the server. To change them, open Browsing limits on the Settings Repositories tab, or run `owngit settings set` with the options below. Sizes are written like `4MB` or `256KB` (1 MB is 1024 KB).

| Limit | Default | Range | Option |
|---|---|---|---|
| Raw file download | 10 MB | 1 MB to 256 MB | `--browse-raw` |
| File view | 2 MB | 64 KB to 64 MB | `--browse-file` |
| Commit diff, for all files of a commit page | 2 MB | 64 KB to 64 MB | `--browse-commit-diff` |
| One file's diff alone | 8 MB | 64 KB to 64 MB | `--browse-file-diff` |
| One file within a commit page | 256 KB | 16 KB to 16 MB | `--browse-commit-file` |
| Pull request comparison | 8 MB | 64 KB to 64 MB | `--browse-compare` |
| Comparison time | 20 seconds | 5 seconds to 1 minute | `--browse-compare-time` |

- A raw file over its limit is not downloaded from the browser; clone the repository to get it. The file view shows the part that fits and offers the rest as a download. A file whose diff is larger than the limit within a commit page is listed there with a link to its diff alone.
- New limits apply to pages opened after you save. A page still stops at its own time limit, so a comparison time longer than that ends with the page's error instead of a partial comparison.
- The pull request diff of the API, `owngit pr diff` and the MCP tool `pull_request_diff` keep their own fixed budget, equal to the defaults.
- Raising any limit above its default warns that larger views use more memory and may delay other pages.
- In the settings API, the group `browse_limits` has `raw_bytes`, `file_bytes`, `commit_patch_bytes`, `file_patch_bytes`, `commit_file_bytes`, `compare_bytes` and `compare_seconds`.

## Check ceilings

Check ceilings are this computer's upper bounds for what a repository's [check policy](AUTOMATIC_CHECKS.md) may choose. To change them, open Check ceilings on the Settings Repositories tab, or run `owngit settings set` with the options below. The defaults are the bounds earlier releases enforced.

| Ceiling | Default | Range | Option |
|---|---|---|---|
| Time for one check | 24 hours | 1 second to 168 hours (7 days) | `--check-time` |
| Output of one check | 64 MB | 1 KB to 1024 MB | `--check-output` |
| Checks waiting per repository | 1000 | 1 to 10000 | `--check-queue` |
| Checks running at once per repository | 100 | 1 to 1000 | `--check-active` |
| Container CPUs | 64 | 0.1 to 1024 | `--check-cpus` |
| Container memory | 64 GB | 64 MB to 1024 GB | `--check-memory` |
| Container processes | 4096 | 16 to 65536 | `--check-processes` |
| Container scratch space | 16 GB | 1 MB to 1024 GB | `--check-scratch` |
| Source copied for a check | 4 GB | 1 byte to 1024 GB | `--check-source` |

- A ceiling is checked when a check policy is saved and when a check is queued. The checks page states each field's range under the ceilings, and a value above a ceiling is refused with a note that an administrator can raise it.
- Changing a ceiling never changes a saved policy or its consent. After a ceiling is lowered, a policy above it queues no new check until you raise the ceiling or lower the policy; Check ceilings lists those repositories, and their checks page says so. Checks already queued run with the limits they were queued with.
- The output ceiling limits how much one check may print; OwnGit stops a check that prints more, and it ends as `incomplete`. OwnGit counts every byte but keeps only the first 256 KiB of each check's output, the most a raw log stores, so a larger output ceiling does not make a check use more memory.
- Raising any ceiling above its default warns that repositories you enable can request more resources.
- In the settings API, the group `check_ceilings` has `timeout_seconds`, `output_bytes`, `queue_limit`, `active_jobs`, `container_cpu_millis`, `container_memory_bytes`, `container_pids`, `container_scratch_bytes` and `source_total_bytes`. A `PATCH` that lowers a ceiling below a saved policy answers with a warning naming the repositories.
- The ceilings belong to this computer and are not in backups. A policy saved under higher ceilings is backed up in format 11 and restores anywhere; on a computer with lower ceilings it queues nothing until they are raised there.

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

OwnGit maintains each repository while nobody uses it, so Git stays fast and uses less space. After five minutes without a push or request following a change, it packs loose refs and objects and updates the commit-graph. In a daily window, 03:00 to 05:00 local time, it also combines the packs of a repository that has more than 20. Maintenance never deletes a ref or kept history, and it deletes no object unless [unused object cleanup](#unused-object-cleanup) is on. It holds the repository only while a step runs, so a push or page that arrives then waits for that step, and it logs one line per run. At start, OwnGit removes temporary pack and lock files that an interrupted Git command left behind and names them in the log.

To change maintenance, open Repository maintenance on the Settings Storage & recovery tab, or run `owngit settings set` with these options:

- `--maintenance on` or `--maintenance off`: whether maintenance runs at all. It is on by default. While it is off, no repository is maintained, cleanup included, and the log says so once.
- `--maintenance-window 3-5`: the daily window in whole hours of the host's local time. Start and end must differ. A window may pass midnight, such as `22-6`, and then belongs to the date it started.
- `--maintenance-idle 5m`: how long a repository must go unused first, from 1 minute to 24 hours.
- `--maintenance-step-time 30m`: how long each ordinary step may take, from 1 minute to 24 hours.
- `--maintenance-consolidation-time 2h`: how long combining all of a repository's packs may take, from 1 minute to 24 hours.
- `--maintenance-packs 20`: the daily window combines the packs of a repository that has more than this many, from 2 to 1000.

The values shown are the defaults. The settings API fields in `maintenance` are `enabled`, `window_start_hour`, `window_end_hour`, `idle_seconds`, `command_seconds`, `full_repack_seconds` and `pack_threshold`. A change applies from the next maintenance, and one already running finishes with the choices it started with. Turning maintenance off, or making the window longer than 2 hours, shows a warning: Git can slow down and use more space, and a wider window can keep the computer busy while you work. If the saved choices cannot be read, no repository is maintained until you set them again, and the log names the setting.

### Unused object cleanup

Unused object cleanup removes old Git objects that no ref reaches, such as what a force push left behind when its history was not kept. It is off by default, and while it is off nothing is removed. To turn it on, open Unused object cleanup on the Settings Storage & recovery tab, or run `owngit settings set --unused-object-cleanup on`.

- It removes an object only when no ref reaches it and it is older than the grace period: 14 days by default, from 2 to 365 days (`--cleanup-grace-days`). The 2 day minimum keeps a new object safe while a transfer, import or check may still use it.
- It never removes anything a ref reaches, including branches, tags, [other ref namespaces](#other-ref-namespaces), kept history and pull request refs. Packs marked with a `.keep` file stay as they are, and cleanup does not run while a backup holds the repository.
- It runs once a night for each repository, in the maintenance window and only while maintenance is on. It takes the place of that night's pack combining and rewrites all of the repository's packs, so a large repository takes longer. When someone uses the repository, it stops after the current step and continues later.
- While a repository has an unfinished check (queued, claimed or running) or an import in progress, its cleanup waits for the next maintenance window. If cleanup has already removed the commit of a check job, running that job again is refused with `check_source_missing`.
- It needs Git 2.37 or newer on the host. With an older Git the cleanup fails, the log says why, and nothing is removed.
- Turning it on warns that unreachable objects older than the grace period may be removed for good.

Cleanup is not a way to remove history or a pushed secret, because whatever kept history or another ref still reaches stays. See [Backups](#backups) for what removes a secret. The settings API fields in `unused_object_cleanup` are `enabled` and `grace_days`. If the saved choice cannot be read, no object is removed and maintenance goes on without cleanup; the log names the setting.

### State database upgrades

On start, OwnGit upgrades a database from any earlier release in place, in one transaction, after [backing it up](#backup-before-an-upgrade). It logs one line such as `state database upgraded from schema 15 to 16`, on standard error for offline commands such as `backup`.

- OwnGit refuses a database from a newer version, and one whose tables, indexes, triggers or views differ from the ones OwnGit creates at the schema version it records. It leaves the files of such a database unchanged.
- An older build refuses a database that a newer build has upgraded. To go back, restore the backup made before the upgrade with the earlier version.
- When the database was changed outside OwnGit, undo that change, or restore a backup of the state with the OwnGit version that made the backup.
- Expired logs and deleted records free space inside the database for reuse, but the file does not shrink and the old bytes are not securely erased. There is no overall size limit, and OwnGit does not run `VACUUM`.
- When a `-wal` or `-shm` file is present at start, OwnGit copies the database to a private temporary directory to inspect it, so the temporary volume needs that much free space.

## Backups

While OwnGit runs, it can make backups itself: on a schedule that you set, or at once when you ask. The administrator manages them [in the dashboard](#backups-in-the-dashboard) or with the `owngit backup` commands. To back up a stopped OwnGit, use [`owngit backup --output`](#backing-up-a-stopped-owngit). Every backup is a folder that [`owngit restore`](#restoring-a-backup) restores and [`owngit backup verify`](#verifying-a-backup) checks, and OwnGit can hand one over as a single `.tar` file for [download](#downloading-a-backup). The [backup before an upgrade](#backup-before-an-upgrade) is separate and has its own switch.

Kept history protects against force-pushes and deletions, but it is not a backup.

A secret that was ever pushed stays in the repository's Git data, even after a force-push or branch deletion and even with kept history off, and anyone who can read the repository and has its commit ID can still open it in the browser. While the repository keeps history, it also stays in kept history and in every later backup. Only [deleting the repository](#deleting-a-repository) with its files removes it for certain, and earlier backups still contain it. [Unused object cleanup](#unused-object-cleanup) keeps anything that kept history or another ref still reaches, so do not rely on it. Rotate any secret you push by mistake.

### Backups in the dashboard

The administrator manages backups in Settings, on the Storage & recovery tab (`/settings/storage`). Only a browser confirmed as administrator sees the backup parts; anyone else sees what backups do and a button to confirm as administrator.

- **Backups** holds the schedule and has its own Save: the backup folder, Scheduled backups on or off, Back up every (12 hours, Day or 7 days), Backups to keep (1 to 1000) and Verify each new backup on or off. They mean the same as the options of [`owngit backup schedule set`](#scheduled-backups). Turning scheduled backups or verification off shows a warning at once that says what you give up, and the warning shows again after you save. Until a folder is saved, the part says that OwnGit makes no backups.
- **Current state** shows whether scheduled backups are on, off or not configured, the backup running now, the last backup with its result and time, the last verified backup and the next scheduled backup. It also shows the result of the last [verification you asked for](#verifying-a-kept-backup-again), and a warning when a restore cannot write to the backup folder's disk (see [Where backups can be written](#where-backups-can-be-written)). Back up now is here and works as [`owngit backup now`](#backing-up-now) does.
- **Recorded backups** lists every recorded backup, newest first, with its result, start time, kind (Scheduled or Back up now), verification (Passed, Failed or Not verified), longest Git write wait, message and folder. A backup that OwnGit still keeps has three actions: Verify again, Download and Restore this backup. For a run whose backup is gone, the list says that OwnGit keeps no backup of it.
- **Restore from a backup file** takes back a backup file that OwnGit downloaded; see [Restoring from a backup file](#restoring-from-a-backup-file).

Each button asks for the administrator password when Settings would ask for it; see [Administrator password check](#administrator-password-check). Back up now, Verify again and an upload return at once; reload the page to see when they end.

### Scheduled backups

Choose a folder for the backups and turn scheduled backups on, in the Backups part of the dashboard or with this command:

```sh
owngit backup schedule set \
  --destination /absolute/path/to/backups \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

The first backup starts as soon as the schedule is saved. The commands `owngit backup schedule`, `now`, `status`, `runs`, `check`, `download` and `upload` talk to the running server. They take the same `--server`, `--accept-insecure-http` and `--password-file` flags as the [import commands](#imports-on-the-command-line), need the administrator password, and print JSON. The flags are omitted below. [`owngit backup --output`](#backing-up-a-stopped-owngit) and [`owngit backup verify`](#verifying-a-backup) work on this computer instead, without the server or its password.

- `--destination` is a folder on the computer that runs OwnGit, given as an absolute path. It must not be inside OwnGit's state directory or repository folder, and it follows the rules in [Where backups can be written](#where-backups-can-be-written). When it is missing, OwnGit creates it so that only the account that runs OwnGit can use it.
- `--interval` is `12h`, `1d` (the default) or `7d`.
- `--keep` is how many of its own backups OwnGit keeps in the folder, from 1 to 1000 (default 7).
- `--verify on` (the default) checks each new backup, as described in [Verification of new backups](#verification-of-new-backups). `--verify off` skips that check.

`set` turns scheduled backups on and changes only the options you give; the first `set` needs `--destination`. `owngit backup schedule show` prints the schedule. `owngit backup schedule off` stops scheduled backups. The backups already made stay, and `owngit backup now` still works. When you turn scheduled backups or verification off, the answer carries a warning that says what you give up.

The next scheduled backup is due one interval after the last scheduled one started. Scheduled backups run only while OwnGit runs:

- If OwnGit was stopped when a backup was due, one backup starts when OwnGit starts again, not one for each missed interval.
- A scheduled backup that failed or was interrupted is not retried before the next interval. Run `owngit backup now` to try again at once.

### Backing up now

Choose Back up now in the dashboard, or run:

```sh
owngit backup now
```

The command starts a backup into the folder of the schedule, also while scheduled backups are off, and returns without waiting for it. Follow it with `owngit backup status`. The backup is verified and older backups are removed as for a scheduled one.

- Before a folder is set, the command answers `Choose a backup folder first.`
- OwnGit runs one backup, verification or upload at a time. While a backup runs, starting another backup, a verification or an upload answers `A backup is already running. Wait for it to finish.` While a verification or an upload runs, starting any of them answers `A backup, a verification or an upload is running. Wait for it to finish.` A scheduled backup that falls due during a verification or an upload starts as soon as it ends.

### Checking backups

`owngit backup status` shows the state of backups, as Current state in the dashboard does:

- `schedule`: its `state` is `not_configured` before a folder is set, then `on` or `off`, with the folder, interval, keep count and verification choice.
- `running`: the backup that runs now, or null.
- `last_run`: the last backup that ended.
- `last_verified`: the newest backup that passed verification and that OwnGit still keeps.
- `next_run`: when the next scheduled backup is due. A time already past means as soon as the running backup ends.
- `check`: the last [verification you asked for](#verifying-a-kept-backup-again), or null.
- `upload`: the [uploaded backup](#restoring-from-a-backup-file), or null. Once it passes verification, `upload_restore_command` holds the command that restores it.
- `restore_limit`: present when a restore cannot write to the backup folder's disk, with the `destination` and the `file_system`.

`owngit backup runs` lists every recorded backup, the newest first. Each one shows:

- `kind`: `scheduled`, or `manual` for back up now.
- `status`: `running`, `succeeded`, `failed` or `interrupted`. `interrupted` means that OwnGit stopped, or its process was killed, before the backup finished. Such a run is never a finished backup, even when a complete folder was left behind; see [When a backup or restore is interrupted](#when-a-backup-or-restore-is-interrupted).
- `verification`: `passed`, `failed` or `not_run`. Only `passed` makes a backup verified.
- `backup_name` and `path`: the backup's folder. Both are empty when the run wrote no backup, or when OwnGit removed the backup or found it gone or replaced.
- `started_at`, `finished_at`, and a `message` that says why the backup failed or what OwnGit left in place.
- `longest_hold_ms` and `longest_hold_repository`: see [Backups while OwnGit runs](#backups-while-owngit-runs).

Both commands show what OwnGit's records hold and read nothing in the backup folder, so a slow or missing folder never holds them up. OwnGit looks at the folder when it makes the next backup. A backup that you remove by hand is still listed until then.

The server log has one line for each backup that ends, with its folder and result. OwnGit keeps the records of the latest 100 backups, of every backup it still keeps, and of the latest scheduled backup, whose start sets when the next one is due.

### Verification of new backups

With verification on, OwnGit checks each new backup as [`owngit backup verify`](#verifying-a-backup) does. It rehearses a restore in the system's temporary folder, whose disk needs room for the repositories. A verification that has not finished within 2 hours fails with that reason. A backup that fails verification is recorded as `failed`, and OwnGit removes no older backup after it.

### Verifying a kept backup again

A backup that OwnGit keeps can be verified again at any time, for example one made while verification was off. Choose Verify again on it under Recorded backups, or run:

```sh
owngit backup check --run ID
```

`ID` is the 32 character `id` that `owngit backup runs` lists. The check is the same as for a new backup, with the same 2 hour limit. It starts in the background, and the command prints it with `status` `running`. Current state in the dashboard and `check` in `owngit backup status` then show it as `running`, `passed` or `failed`, with a message when it failed, until OwnGit stops.

OwnGit records the result with the backup, so `verification` in `owngit backup runs` changes too. A backup that fails no longer counts as verified, and OwnGit no longer keeps it as the newest verified backup.

When the backup is already gone from its folder, or its folder holds something else, the check does not start and says that the backup is gone. When the backup's folder is moved, removed or replaced by another folder while it is being checked, or its manifest changes or can no longer be read, the check fails with a message that says so, records nothing, and the backup keeps its earlier `verification`. A bundle that is damaged while the folder and manifest stay the same is checked as usual, so the check fails and records the backup as failed.

### Which backups OwnGit keeps

Each backup is a new folder in the destination named `owngit-backup-YYYYMMDD-HHMMSS-XXXXXXXX`, where the time is the start in UTC. After a backup succeeds, and passes verification when verification is on, OwnGit keeps its newest backups in that folder, as many as `--keep` says, and also the newest verified one. Newest means the latest to start, also within one second. OwnGit removes its older backups there.

OwnGit records the SHA-256 hash of each backup's manifest with the backup. A folder counts as its backup only when it holds exactly that manifest; the name alone never counts. OwnGit never touches other files or folders in the destination, and it leaves the following in place and names them in the new backup's message:

- a backup folder that holds anything besides the manifest and the bundles it names, such as a file you added;
- a folder that is a link, or that holds another backup than the one OwnGit wrote there, for example one copied or swapped in. OwnGit stops counting such a folder as its own;
- a backup whose manifest cannot be read.

A backup whose end OwnGit could not record, for example because its process was killed, is never counted or removed. After a failed or interrupted backup, nothing is removed. When you change the destination, the backups in the earlier folder stay as they are.

### Free space in the backup folder

A new backup is written before an older one is removed, so the folder needs room for one more backup. Before a backup starts, OwnGit compares the free space in the folder with the size of the last backup it made there, counting only repositories that still exist. When there is less room, the backup fails at once with a message such as `not enough free space in DIR: a new backup needs about N MiB, the size of the last backup here, and M MiB is free`. When the last backup cannot be read, the backup goes ahead, and its message says that the free space was not checked.

Before the first backup in a folder there is nothing to compare with. A disk that fills up during a backup ends it with `not enough free space in DIR`, and no incomplete backup takes its name.

### Backups while OwnGit runs

A backup describes one moment. When it starts, OwnGit waits for pushes and other Git writes already running. It then holds new ones only while it reads every repository's refs, and lets them continue while it writes the backup. On Linux this pause is usually well under a second; on Windows it took about 8 seconds with 1,000 repositories. `longest_hold_ms` in `owngit backup runs` says how long Git writes to one repository waited at most, and `longest_hold_repository` names it.

- Pushes, merges and imports after that moment are not in the backup.
- While a repository is being written into the backup, it cannot be deleted and its maintenance waits.

**A backup holds only the commits that a ref or HEAD reaches.** A ref is a name that points to a commit: a branch, a tag, or one of OwnGit's own refs, such as those for pull requests, imports and kept history. A backup carries every ref and the HEAD of each repository with the commits they reach; a commit that none of them reaches is not in it. With kept history on, an overwritten branch tip stays reachable from kept history and is backed up. With kept history off, an overwritten commit is not in a later backup unless another ref still reaches it. Check, pull request and import records keep the commit IDs they name; after a restore, a record whose commit is not in the repository shows the ID without its content.

A backup is refused, and the refusal names the repository, when:

- the repository's HEAD cannot be read;
- the repository borrows objects from another repository (`objects/info/alternates`) or is a partial clone;
- the Git process with which an import writes the repository's refs could not be stopped. Restart OwnGit first.

### Downloading a backup

Download, on a backup under Recorded backups, gives a backup that OwnGit keeps as one `.tar` file, named after the backup, such as `owngit-backup-YYYYMMDD-HHMMSS-XXXXXXXX.tar`. The page stays as it is. On the command line:

```sh
owngit backup download --run ID --output /path/to/new-file.tar
```

The file holds the backup's folder with its `manifest.json` and the repository bundles the manifest names, and nothing else.

**The file holds every repository, OwnGit's records and the password hashes of the administrator and shared passwords.** Keep it as private as the backup folder.

- To check the file on another computer, unpack it with `tar -xf FILE` and run [`owngit backup verify`](#verifying-a-backup) on the folder it makes. To restore it, see [Restoring from a backup file](#restoring-from-a-backup-file).
- There is no size limit. A download stops when the receiving side takes nothing for 1 minute.
- When the backup's manifest changes while it is being downloaded, the download fails.
- When a download fails after it started, OwnGit breaks off the connection, so the browser or the command reports the download as failed and a shortened file never passes for a whole one.
- The command refuses an output file that already exists, removes its partial file when the download fails, and prints JSON with the `path` and size in `bytes` of the file.
- OwnGit does not remove a backup while it is being downloaded. When that backup is due for removal, it stays until the next backup, and the new backup's message says so.

### Who can see backup status

The backup parts of the dashboard show only to a browser confirmed as administrator. `owngit backup schedule`, `now`, `status`, `runs`, `check`, `download` and `upload` need the administrator password, because their answers name folders and repositories and carry error messages, and a downloaded backup holds every repository and the password hashes. `owngit backup --output` and `owngit backup verify` need no server password; they run on this computer and need only access to the state directory or to the backup. Coding tools get a summary instead, through the [`backup_status` MCP tool](CODING_TOOLS.md#tools), with general access (the shared password, or none when access is open). The summary says whether scheduled backups are `not_configured`, `off` or `on`, how and when the last backup ended, when the newest verified backup finished and when the next one is due. It names no folder, repository or error.

The backup commands use the administrator API, which takes the administrator password as HTTP Basic authentication with the user name `admin` and refuses the shared password and helper and runner credentials:

- `GET /api/v1/backups`: the status, as `owngit backup status` prints it.
- `GET /api/v1/backups/runs` lists the recorded backups, and `POST` backs up now.
- `GET /api/v1/backups/schedule` shows the schedule, and `PATCH` changes it.
- `POST /api/v1/backups/runs/ID/verify` verifies a kept backup again.
- `GET /api/v1/backups/runs/ID/archive` answers the backup as a `.tar` file.
- `PUT /api/v1/backups/upload` takes a `.tar` file as the body, with a `Content-Length` header.

A refusal answers JSON with a code such as `backup_running`, `backup_busy`, `backup_not_found`, `backup_gone`, `invalid_backup_archive` or `length_required`.

### Backing up a stopped OwnGit

`owngit backup --output` backs up an OwnGit that is not running. It refuses while an OwnGit runs with that state directory; use `owngit backup now` then. The output directory must not exist:

```sh
owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

With `--json` it prints `ok`, the `backup` folder and a `note` about what the SHA-256 hashes show, and a refusal is a JSON error with a `code`, such as `offline_required` while an OwnGit runs with that state directory, `state_missing`, `setup_incomplete` or `backup_failed`.

### What a backup holds

A backup holds a manifest and one Git bundle per nonempty repository. Together they carry:

- every ref including kept history, and each repository's HEAD and metadata;
- each repository's own kept history choice, default branch protection and [other ref namespaces](#other-ref-namespaces);
- pull requests, reviews and merge records;
- tasks, check configurations and results, and automatic-check policies and jobs;
- import sources and history, including each source's extra ref namespaces and refresh choices;
- the access mode and password hashes. Keep backups private, because password hashes are sensitive.

A backup does not include raw logs, credentials and tokens of every kind, import schedules, each import source's [connection choices and limits](#connection-choices-and-limits), the backup schedule and the records of earlier backups, consent, the [update check](#new-release-notice) setting, or the other server-wide settings (the sign-in length, the initial branch, the kept history choice, the Git transfer limits, the browsing limits, the check ceilings, repository maintenance, unused object cleanup and the raw log retention). Nor does it include sessions, network settings, share links, the recent pushes list or the switch for the backup before an upgrade. [After a restore](#after-a-restore) lists everything a restore leaves out and where to set it up again.

A backup holds up to 1 GiB of OwnGit records, counted by the memory they take and not counting the repositories; backup refuses a larger state. Creating and restoring a backup hold its records in memory, so more records need more memory.

### Backup versions

OwnGit restores backup versions 1, 2, 9, 10 and 11 and refuses others; older builds refuse a newer backup instead of dropping records. A backup is written in version 10, which OwnGit 1.0.3 to 1.1.2 restore, unless it holds records that only version 11 can hold, or its manifest would pass the 64 MiB that version 10 allows. Records that need version 11 include a repository's own kept history choice, default branch protection or other ref namespaces, the names of a [renamed](#renaming-a-repository) repository, an import source's extra ref namespaces or refresh choices, and a check policy's or check job's [container options](AUTOMATIC_CHECKS.md#container-options), including a named Docker network. Import history needs version 11 too when it records refs outside branches and tags, or a deletion that followed the source, even after those choices were turned off. So a backup of an installation that uses nothing new, including every [backup before an upgrade](#backup-before-an-upgrade), can still be restored with the earlier version. The exception is a backup whose automatic check records hold a time order that earlier builds refuse, such as a check job that appears to be picked up before it was queued. This can happen after the computer's clock was set back. Only OwnGit 1.1.4 or later restores such a backup.

### Restoring a backup

Restore into new paths that do not exist, on a disk whose file system can rename without replacing (see [Where backups can be written](#where-backups-can-be-written)). Restore refuses a state directory or repository folder that already exists, so it never writes into a running OwnGit's folders; it works whether or not an OwnGit is running. To replace the installation you use, stop OwnGit and rename its folders first, as the [restore steps](#restore-steps-in-the-dashboard) show:

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
- With `--json` a finished restore prints `ok`, `state_dir`, `repository_root`, the `verification` result when `--verify` was given, and `notes`, one sentence for each thing it did not restore and one for the server-wide settings that start at their defaults, each saying how to set it up again. Without `--json` the same notes follow the result, one per line. A refusal is a JSON error with a `code` and the same exit status: `backup_not_verified` (with the verification as `details`), `restore_failed`, or `interrupted` after Ctrl+C (status 130).

### Restore steps in the dashboard

OwnGit never restores from the browser. Restore this backup, on each backup that OwnGit keeps under Recorded backups, shows the steps with this installation's folders filled in:

1. Stop OwnGit: `owngit service stop` when it runs as a service, or otherwise end the `owngit serve` process, for example with Ctrl+C in its terminal.
2. Rename the current state folder and repository folder by adding `.before-restore` to their names. A restore never writes into a folder that exists.
3. Run the command shown, which has a Copy button. It has the form `owngit restore --input BACKUP --state-dir STATE --repository-root REPOSITORIES --verify`, so it verifies the backup first and restores only one that passes. When OwnGit runs as a Linux system service, the command starts with `sudo -u owngit`. On Windows the command is written for PowerShell and labelled so; run it in PowerShell, not in Command Prompt (`cmd.exe`).
4. Start OwnGit again: `owngit service start`, or the way you started it before.

When the restore finishes, it lists what the backup did not bring back, as described [after a restore](#after-a-restore). The renamed folders stay until you remove them.

### Restoring from a backup file

To restore a `.tar` file that OwnGit [downloaded](#downloading-a-backup), upload it under Restore from a backup file on the Storage & recovery tab, or run:

```sh
owngit backup upload --input /path/to/backup.tar
```

OwnGit verifies the uploaded backup as it verifies a new one. Once it passes, the page shows the same [restore steps](#restore-steps-in-the-dashboard), and `owngit backup status` gives the command as `upload_restore_command`. In that command, `--input` already points to where the backup will be after you rename the state folder in step 2. Nothing is replaced until you run the command.

OwnGit keeps one uploaded backup, in the folder `backup-uploads` inside its state folder:

- A new upload replaces it once OwnGit has checked the free space for it. A refusal before that point, such as too little free space or another backup, verification or upload running, keeps the earlier upload. From that point on, OwnGit removes the earlier upload first, so an upload that is then refused or fails verification leaves no uploaded backup.
- OwnGit removes it 24 hours after it arrived, whenever OwnGit starts, and at once when its verification fails. So run the restore before you start OwnGit again. The page shows when it will be removed.
- `owngit backup upload` prints the upload with `status` `verifying`. `upload` in `owngit backup status` then shows `status` (`verifying`, `passed` or `failed`), a `message` when it failed, and `removes_at`.

OwnGit refuses an upload in these cases, and then keeps nothing of it:

- The disk of the state folder has less free space than the file's size plus 1 GiB. OwnGit checks this before it reads the file.
- The upload does not declare its size. Browsers and `owngit backup upload` always declare it.
- No data arrives for 1 minute.
- The file holds anything besides one top folder with `manifest.json` and `repositories/*.bundle`, such as an absolute path, `..`, a link or another file.
- The top folder's name holds anything besides ASCII letters, digits, `.`, `_` and `-`. The files OwnGit downloads always meet this rule.

When OwnGit refuses an upload before the browser has sent the whole file, for example for lack of free space, some browsers show a connection error instead of the reason. `owngit backup upload` prints the reason.

### After a restore

Start `owngit serve` with the restored state before you use it in other ways, so that startup can settle interrupted records. Then set up again what a backup does not carry. `owngit restore` lists each of these when it finishes, with where to set it again:

- Sessions and setup links: sign in again.
- Network settings (listen address, base URL, allowed Hosts, trusted proxies, the public share address, Tailscale Serve and the acknowledgement of plain HTTP): set them again under Settings or with `owngit network set` and `owngit tailscale on`.
- Helper credentials and runner tokens: the old tokens are refused. Create new ones on each repository's Helper credentials and Runner tokens pages, or with `owngit helper-credential create` and `owngit runner-credential issue`.
- Consent to run automatic checks: turn checks on again on each repository's Automatic checks tab or with `owngit check-policy enable`. Unfinished jobs are marked `interrupted`.
- Import credentials, each source's connection choices and limits, and import schedules: store the credentials again and turn on what a source needs on its Import tab, or with `owngit import credentials`, `owngit import configure` and `owngit import schedule`.
- Share links: create new ones under Share links on each repository's Settings tab, or with `owngit repo share create`.
- Scheduled backups and the backup history: set the schedule up again under Settings or with `owngit backup schedule set`. Earlier manual and scheduled backups are no longer listed under Recorded backups, but their folders stay where they were written, and `owngit backup verify` and `owngit restore` still take them.
- The [backup before an upgrade](#backup-before-an-upgrade) is on, as on a new installation: if you turned it off, run `owngit upgrade-backup off` again.
- Raw check logs and the recent pushes list are absent, and unsettled import publications are closed without being applied.
- An import that follows upstream deletions deletes a ref only after a refresh has seen it again ([After a sign-in change or a restore](#after-a-sign-in-change-or-a-restore)).
- Server-wide settings start at their defaults, as on a new installation, whether the backed-up installation chose stricter or looser ones: a sign-in lasts 12 hours, new repositories start on `main`, repositories that follow the server keep overwritten and deleted history, a Git transfer may move 4 GB and take 30 minutes, raw check logs are kept 30 days, deleting a repository asks for its name, 4 wrong passwords within 10 minutes pause an address for 15 minutes, and a link from another site opens without the shared sign-in. Transfer slots and waits, browsing limits, check ceilings, repository maintenance, the administrator password check and the new release check are back at their defaults, and unused object cleanup is off. Each repository's own choices under its Settings tab come back with it. Set the server-wide ones again under Settings or with `owngit settings set`.

### When a backup or restore is interrupted

If a restore stops without its own cleanup, for example after a power loss or when its process is killed:

1. Do not start OwnGit from either target, and do not remove a `.owngit-restore-pending` marker.
2. Move both targets and any `TARGET.owngit-restore-...` siblings to a quarantine location.
3. Restore again into new paths.

A backup that stops before it finishes is not a finished backup, whatever it left on disk. Once no backup process is running, look beside its output, where `OUTPUT` is the output's name (for a scheduled backup or back up now, the backup's folder name in the destination):

- A hidden `.OUTPUT.owngit-backup-...` sibling holds the unfinished backup. Keep or quarantine it.
- On a file system that [cannot rename without replacing](#where-backups-can-be-written), a folder at the final name without a manifest may remain. It is not a backup; remove or quarantine it.
- A backup that stopped after its folder was complete, for example during verification, leaves a complete folder. Its run stays `interrupted` and the backup is not verified, so check it with `owngit backup verify` before you rely on it.

When OwnGit stops during a scheduled backup or back up now, the backup is recorded as `interrupted`. When its process was killed instead, OwnGit records the backup as `interrupted` the next time it starts, and says so in the server log.

### Where backups can be written

Backup and restore make each new folder under a temporary name beside its final place and then rename it, except that on a file system that cannot rename without replacing, described at the end of this section, a backup is moved into a new folder instead. The folder that holds it must therefore be one where no other account can rename or remove what OwnGit puts there.

- On macOS and Linux, that folder and every folder on the way to it must be ones that no other account can change, as on the way to the state directory; a sticky folder such as `/tmp` is accepted. Otherwise OwnGit stops before it creates anything, names the folder, and gives the `chmod` command that fixes it when there is one. You can also choose a folder that only this account can change.
- Missing folders on the way are created, private to this account, but only inside a folder where no other account can create names, so not directly in `/tmp`.
- On Windows, OwnGit follows no link, junction or mounted volume on the way, and keeps the folders on the way from being renamed while it works.
- The backup and the restored repository folder may be on a network share, except when OwnGit runs as root or as an elevated administrator on Windows. A restore also needs what the file system can do, as described below. The restored state directory must be on a local disk, as every state directory must.

Two kinds of file system need care:

- Some cannot rename a folder without replacing what is at the new name, such as exFAT on macOS, and NFS and some other network shares on Linux.
- On some, macOS keeps each file's attributes in a separate file named `._` and the file's name, such as FAT16 and FAT32 volumes on macOS.

Backups work on both. On a file system that cannot rename without replacing, OwnGit creates the backup's folder, which fails when anything is at that name, and moves the finished backup into it with the manifest last, writing each part to disk on the way. A folder left without a manifest by a stop in the middle is not a backup, and OwnGit neither counts nor removes it. When OwnGit removes an old backup, it also removes the `._` files that macOS keeps beside the backup's files. Any other file in the backup's folder, including one only named like a `._` file, still keeps the backup in place. `owngit backup verify` rehearses in the system's temporary folder, so it can check a backup stored on either kind of disk.

Restoring repositories works on neither. On macOS and Linux, `owngit restore` checks the target folders before any work. It stops, names the file system (for example `exfat` or `msdos`) and changes nothing when the repository folder's file system cannot rename without replacing, or when macOS keeps attributes there in `._` files, which Git would read as part of the restored repositories. Restore the repositories to a folder on another disk.

When the backup folder is on a disk where a restore of repositories would stop like this, Current state in the dashboard says so with the file system's name, and `owngit backup status` shows it as `restore_limit`.

The restored state directory needs only the rename without replacing. It can be on a FAT16 or FAT32 volume on macOS while the repositories go to another disk, but not on exFAT.

Backing up to exFAT, FAT16 and FAT32 volumes, verifying and removing those backups, and the restore refusals, were tested on macOS with disk images; NFS was not tested.

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

When a newer OwnGit starts on a state whose schema is older than the one it writes, it first makes an offline backup of the state as it is. It upgrades the state only when that backup is complete. `owngit backup --output` does the same before its own backup. Other commands, which also work beside a running server, leave an older state alone and say to start or restart the newer OwnGit once, or to run `owngit backup --output`, first.

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

When the earlier version is 1.0.3, fix the helper credential and runner token files that OwnGit 1.1.0 or later wrote. They start with an `owngit-server:` line, and 1.0.3 reads that line as part of the token, so the connection fails. Delete the first line of each file, or create the file again with 1.0.3.

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

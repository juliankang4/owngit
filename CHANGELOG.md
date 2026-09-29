# Changelog

All notable changes to OwnGit are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

**Upgrading:** the first start of this version upgrades the state database from schema 15 to 16. Before it does, OwnGit makes an offline backup of the state in a folder beside the state directory, such as `~/.config/owngit-backups`, and the log names the command that restores it; `owngit upgrade-backup off` turns this backup off. An earlier version refuses the upgraded database, so to go back, restore that backup with the earlier version. Backups are still written in version 10, which 1.0.3 to 1.1.2 restore, unless they hold records that only the new version 11 can hold, such as a repository's own kept history choice. Every folder on the way to the state directory, to backups and to a restore target must now be on a local disk for every account, so a state directory reached through a network share is refused.

Pushes and imports now refuse branches and tags whose name, or any folder in it, matches another ref apart from letter case, Unicode encoding or letters that some file systems treat as equal, so a repository that already holds two such names (for example `main` and `Main`) refuses pushes that create or update either one. Delete one of them with `git push origin --delete NAME`; the other ref keeps its value, the default branch cannot be deleted this way under any spelling, and the deleted commit stays in kept history when kept history is on.

### Added

- One-line installers for Linux, macOS and Windows download the latest or a named release, check it against `SHA256SUMS`, install `owngit` and run it as a service.
- `owngit update` prints the command that updates OwnGit the way it was installed, and the new-release notice shows it to a confirmed administrator with a Copy button. `owngit uninstall` removes the service and names the command that removes the program.
- A container image for Linux on x64 and ARM64, `ghcr.io/juliankang4/owngit`, with a Compose file. It runs as a non-root account with its data in one volume, shows the setup link through `docker compose exec -it owngit owngit setup-link`, and updates with `docker compose pull && docker compose up -d`.
- A helper script for Proxmox VE hosts, `https://owngit.app/proxmox.sh`, creates an unprivileged Debian 13 container, installs OwnGit in it with the one-line installer, and can keep the repositories in a folder of the host. Nothing from the OwnGit release runs on the host, and the script never changes an existing container.
- `owngit doctor` and a Checkup card in Settings list problems such as a stopped server, Windows folders that the Administrators group owns, or a firewall that keeps other devices out, each with one command that repairs it.
- Kept history can be turned off for the whole server or for one repository, and a repository's default branch can be protected from rewrites and deletion, in Settings, the API, `owngit settings set` and `owngit repo settings`.
- `owngit repo kept-history`, `owngit repo restore preview` and `apply`, the matching API routes and MCP tools list kept history and restore files, with the same preview and the same check that the branch has not moved as the restore pages.
- Check mergeability on the pull request page, `owngit pr mergeability`, `GET /api/v1/repositories/{id}/pull-requests/{number}/mergeability` and the MCP tool `pull_request_mergeability` answer whether a pull request can merge now: clean with the merge method, conflict with the conflicting paths, unavailable with the reason, or stale when a branch moved. OwnGit checks only when asked and writes nothing.
- A pull request can have a Markdown description, its title and description can be edited, and a review can carry a note. An edit based on an outdated version is refused with `stale_edit`. `owngit pr edit`, `--body-file`, `--note-file` and the MCP tool `pull_request_edit` do the same from the command line, and results name who recorded the current review.
- `owngit backup verify` rehearses a restore in a temporary folder and reports whether the backup restores, without changing it or the running OwnGit. `owngit restore --verify` restores only a verified backup.
- Settings has five tabs, General, Access, Network, Repositories, and Storage & recovery, with a Save for each part, and asks before you leave it with unsaved changes.
- New settings choose how long a sign-in with the shared password lasts, the branch new repositories start on, the Git transfer size and time limits, and how long raw check logs are kept. `owngit settings show` and `owngit settings set` read and change them.
- The Access tab chooses how often the dashboard asks for the administrator password, from every time to once in 30 days, or never. By default it asks again after 30 minutes.
- The repository lists share one row layout, and a Sort control orders them by latest update or by name, remembered in each browser.

### Changed

- Imports ask the source for Git protocol v2, so refs that an import does not use, such as pull request refs, no longer count toward the 50,000-ref limit of such a source.
- Imports accept old commits and tags with a malformed time zone or a missing tag date, and trees with zero-padded folder modes. They refuse commits whose dates OwnGit cannot show.
- A commit whose time zone offset is 24 hours or more is shown in UTC instead of breaking the page, and a repository whose latest commit cannot be read stays readable.
- Merge and restore commits that OwnGit writes carry this computer's UTC offset instead of +0000.
- A dashboard page opened over plain HTTP by the name of a working HTTPS address, from Tailscale sharing or a trusted proxy, moves to that HTTPS address.
- OwnGit reads Tailscale's status from Tailscale's own service instead of running the `tailscale` command, and uses a Unix socket only when Tailscale's service is the one listening on it.
- `owngit serve` opens no browser and asks nothing in a session that nobody watches.
- Backup and restore create missing parent folders of their targets.

### Fixed

- A Git client that is locked out after wrong shared passwords gets HTTP 429 instead of 401, so it keeps the password its credential helper saved. An upload that stops sending gets 408 instead of 502.
- A form larger than 1 MiB gets a page that says nothing was saved, instead of a bare error.
- An import refuses a repository name that Windows reserves, such as `CON`, before it starts.

### Security

- OwnGit opens its state directory, logs, locks and backup folders only through folders that no other account can change, and follows no link that another account could place on the way. The way to the state and to backups must be on a local disk.
- Text from repository users, such as titles, branch names, commit messages and paths, is shown in its own writing direction, so direction controls in it cannot reorder the text beside it. Every JSON result writes those controls as escapes.
- A sign-in and an administrator confirmation belong to the password version that OwnGit actually checked, so a password change made at the same moment ends them. A change of the administrator password in Settings is refused when its confirmation is from before another password change.
- Signing in again replaces the session that the browser held before.
- The administrator API accepts only the administrator password, for reads as well as changes, and no longer a browser's administrator session.

### Changes for scripts

- JSON from the API, the command line and MCP writes Unicode direction controls as `\uXXXX` escapes; the values are unchanged.
- API requests and MCP arguments that are not valid UTF-8, or that escape half of a UTF-16 surrogate pair, are refused with `invalid_json` or `invalid_arguments` instead of being stored with replacement characters.
- The helper credential, check and import owner APIs need the administrator password in the Basic header for reads, as the command line sends it.
- A source with more refs than an import accepts fails with `too_many_refs`.
- An interrupted `owngit restore` says that nothing was restored and exits with 130.

## [1.1.2] - 2026-09-28

This release fixes two low-severity security problems. Upgrading is recommended.

### Added

- The macOS binary in the release archive is signed with a Developer ID and notarized by Apple, so macOS runs it without the quarantine workaround.

### Changed

- The macOS and Homebrew services write their log to the rotating log file, and the same failure repeated within a minute is logged once with a count. Run `owngit service install` again, or restart the Homebrew service, to use the new log settings.
- An import failure that OwnGit cannot classify is reported with the code `unclassified` instead of `unsupported`.

### Fixed

- A password or settings change that fails to save no longer signs the browser out, and the page says what was and was not changed.
- A request that could not read OwnGit's own state answers that OwnGit is unavailable and logs the cause, instead of "not signed in", "not found", "invalid input" or an empty result.
- Tailscale sharing: a turn-on whose settings could not be saved is finished by the next turn-on or turn-off; a port change no longer rewrites or later removes an HTTPS address you made in Tailscale; when Tailscale hangs, the page answers in time and says so.
- A configured check that could not start is recorded as not run with the reason, instead of ending as ambiguous.
- The restore page keeps the file list and the selection when a preview fails, and says why.
- Scheduled imports keep running when other repositories are still being prepared.
- Offline backups keep configured-check histories that the backup check used to refuse.
- Very large check output no longer takes hundreds of megabytes of memory when it is cut for the log.

### Security

- Low: server log files could be read by other local accounts. They are now readable by their owner only. Affects 1.1.1 and earlier on Linux and macOS.
- Low: signing out could report success while the session stayed valid on the server. It now says that you are still signed in. Affects 1.1.1 and earlier.

## [1.1.1] - 2026-09-27

This release fixes security problems. Everyone should upgrade.

### Added

- `owngit service install` runs OwnGit in the background and starts it on its own: at boot on Linux, at sign-in on macOS, and on Windows at boot from an administrator account (one approval) or at sign-in from a standard account. `owngit service status`, `start`, `stop`, `restart` and `uninstall` manage it; `uninstall` keeps your data.
- On a computer without a screen, the first start listens on every address until setup is finished, and the setup link is printed in the terminal.
- When root already used OwnGit with its own state, `owngit service install` prints the commands that move that state into the service.
- `GET /healthz` and `owngit health` report whether the server answers.
- `owngit serve --log-file FILE` also writes the server log to a file.

### Changed

- Sharing on your tailnet uses port 8443 or 10000 when port 443 is already in use. `owngit tailscale on --https-port PORT` chooses the port.
- A page opened over the tailnet at this computer's Tailscale address says "Encrypted by Tailscale".
- `owngit setup-link` makes the link for the address the server listens on.
- `owngit network show` and `owngit tailscale status` work before the first start, and every failure under `--json` is a JSON error.
- On Windows, an SSH session counts as a computer without a screen, and commands that use the state directory drop administrator rights when started from an elevated terminal.

### Fixed

- On Windows, OwnGit started without a visible desktop no longer opens a browser.

### Security

- High: when OwnGit listened on a network address, another device could get past the allowed Host check. Affects 1.1.0 and earlier.
- Medium: `owngit runner` started as root could use a workspace folder that another local account controls. Affects 1.1.0 and earlier on Linux and macOS.
- Medium: a client without a password could hold connections open and make OwnGit unreachable. Affects 1.1.0 and earlier.
- Low: on macOS, private state files could stay readable by other local accounts through inherited access lists. Affects 1.1.0 and earlier.
- Low: a state directory whose parent folders another local account can change is now refused. If yours is refused after upgrading, run the command it prints or move the state directory.

## [1.1.0] - 2026-09-27

This release fixes a security problem in `owngit check run`: it no longer starts a clone's `core.fsmonitor` program or index hooks while it inspects the worktree. See Security below.

**Upgrading:** the state database stays at schema 15 and offline backups at version 10, as in 1.0.3, and OwnGit 1.1.0 upgrades the database of any earlier 1.0.x release directly. You can go back to 1.0.3 without restoring a backup. It ignores the listen address, base URL and trusted proxies saved with `owngit network set`, so start it with the options you used before. Host names allowed with `owngit network set --allowed-host` stay allowed under 1.0.3, because they are stored with the names `owngit approve-host` approves. Helper credential and runner token files written by 1.1.0 start with an `owngit-server:` line that 1.0.3 does not understand, and 1.0.3 then reports a connection failure; delete that first line, or create the file again with 1.0.3. On Windows, 1.0.3 also refuses a password file owned by the Administrators group, which an elevated PowerShell gives new files and 1.1.0 accepts; make the file again in a PowerShell that is not elevated. Upgrading from 1.0.3 and going back were tested on Linux, Windows, macOS and a Raspberry Pi. Before you upgrade, stop OwnGit and make a backup with `owngit backup`.

**Changes for scripts:** helper credential and runner token files written by 1.1.0 start with an `owngit-server:` line, so scripts that read them as a bare token must take the last line. `owngit pr`, `owngit check` and `owngit repo` read a missing `--server` or `--repository` from the clone's `origin` remote and fail with `origin_*` codes instead of `invalid_arguments`. Password and credential files must hold the secret on one line. `setup-link` and `reset-admin` refuse arguments they do not know. A gzip Git request body followed by more data is refused. If you set up Tailscale Serve for OwnGit by hand, `owngit tailscale on` refuses to take over that entry until you remove it. Details are below.

### Added

- Share OwnGit on your tailnet over HTTPS. In Settings or with `owngit tailscale on`, OwnGit sets up Tailscale Serve on this computer, saves the HTTPS address and name, and shows the `https://NAME.TAILNET.ts.net/` clone address. Turning it off removes only what OwnGit made. If HTTPS port 443 already serves something, including an entry for OwnGit that OwnGit did not make, turning on changes nothing and shows the command that clears the port. Before the address is served, OwnGit explains that the certificate puts the computer and tailnet names in a public certificate log. `owngit tailscale status|on|off` do the same from the command line, and changes made there apply at the next start. `owngit serve --tailscale PATH` and `owngit tailscale --tailscale PATH` use a `tailscale` command that OwnGit does not find on its own.
- Reverse proxies: `owngit network set --trusted-proxy ADDRESS` (or `serve --trusted-proxy`) names proxies whose `X-Forwarded-Proto`, `X-Forwarded-For` and `X-Forwarded-Host` OwnGit uses, so cookies are `Secure`, HTTPS forms pass the Origin check and each device behind the proxy has its own password lockout. Nothing is trusted by default. The operations guide has a "Behind a reverse proxy" section with tested settings for Caddy, nginx, Traefik and Nginx Proxy Manager, including the Nginx Proxy Manager line that makes it report each device's own address, and explains when HTTP/2 between Git and the proxy helps.
- The operations guide explains how to reach OwnGit over other private networks, such as NetBird, Headscale or plain WireGuard, including an HTTPS address through a reverse proxy on this computer. The steps were tested with self-hosted setups. Sharing from the Settings switch works only with Tailscale, which serves HTTPS with a certificate for this computer's tailnet name; the other networks named do not offer that on the computer.
- `owngit network show`, `set` and `reset` save the listen address, the address other devices use (base URL) and allowed names, so a server started without options, such as `brew services`, can be reached from other devices. Saved values apply at the next start; a `serve` option still overrides one for that run. `network reset` recovers from a saved value that locks you out.
- Settings has a Network card showing each of these values for the next start and for the running server, with the restart state. Changing them needs the administrator password.
- `owngit pr diff` and `GET /api/v1/repositories/{id}/pull-requests/{number}/diff` show what a pull request changes: the exact commits compared, the changed files with line counts and the patch, cut at file boundaries with a flag when output is too large. `--source-oid` and `--target-oid` read a pair recorded for the pull request even after its branches moved.
- Download a branch, tag or commit as ZIP or tar.gz from the Code tab and commit pages, or with `GET /api/v1/repositories/{id}/archive`. Downloads share the Git transfer limits, and one that fails never leaves a valid-looking archive.
- `owngit repo list`, `show` and `create`, and the matching API routes.
- Inside a clone of an OwnGit repository, `owngit pr`, `owngit check` and `owngit repo` find the server and repository from the `origin` remote.
- `owngit skill --install DIR` installs the coding-tool skill that every build carries, including Homebrew and npm installs.
- `owngit mcp`, a local Model Context Protocol server for coding tools (stdio only). Its tools read repositories, pull requests, diffs and checks, create and review pull requests, and run the checks committed in the working directory; results are the same JSON as the matching commands. The server, repository and credentials are fixed when it starts, and `--no-run-check` removes the tool that runs checks.
- `owngit check task list` lists a repository's check tasks.
- Before setup is finished, the setup link also works from another device by an address OwnGit was not started with. Web setup offers to keep accepting the address you used.

### Changed

- With a shared access password, a correct password is remembered for five minutes, so a clone or push checks it once instead of on every request. On Linux, the extra memory during one clone fell from about 200 MB to about 70 MB. A changed password takes effect at once.
- Clone addresses, runner commands and the recovery command show the configured base URL when one is set.
- The connection status says "Encrypted by Tailscale on this computer" or "Encrypted by the proxy in front of OwnGit" when that is where the encryption happens.
- Terminal setup's "Other devices" step suggests `owngit network set` instead of a one-time `serve --listen`.
- A push or scheduled import that changes no branch or tag no longer makes the next page read every branch and tag again.
- When OwnGit refuses a password or credential file because other accounts can read it, the message says what is wrong (the file mode, or on Windows the owner or the accounts that have access) and prints a command that fixes it.
- The operations guide starts with installing a release instead of building from source, and its examples use `owngit` as installed.

### Fixed

- A clone, fetch, push or archive download whose client stops reading or sending is stopped after 60 seconds without data, instead of holding its transfer slot and the repository for up to 30 minutes. Time that Git spends preparing data does not count, but a client on a link of only a few KB/s can be stopped too.
- Git's keepalive and progress messages reach the client while Git prepares a large pack or runs a slow hook, so a reverse proxy with a read timeout no longer cuts the clone or push as a dead connection. OwnGit starts its answer only after it has read the whole request, which Go-based proxies such as Caddy and Traefik need over HTTP/1.1.
- A push over the request size limit gets HTTP 413 and stops Git at once. Before, it could get a cut response with status 200, and a push with a declared length left Git busy with the repository locked for up to 30 minutes.
- On Windows, a password or token file made by hand in an administrator PowerShell was refused even when only your account could open it. The operations and coding tools guides show PowerShell commands that make such a file.
- `owngit approve-host HOST --state-dir DIR` works as the usage shows.
- A push that OwnGit refused or that was cut off, for example one over the 4 GiB limit, left the data it had received in the repository. OwnGit now removes it when the request ends and logs how much it removed; anything left from earlier is removed at the next start.
- When OwnGit commands opened a new state directory at the same moment, for example the server and a command beside it, one could refuse the state or fail with "database is locked". A crash during the first start could also leave a state directory that refused every later start.
- `approve-host`, `reset-admin`, `setup-link` and `forget-check-container` no longer fail with "state directory changed during inspection" when the running server writes at the same moment.
- An import that ran out of time or was cancelled at certain moments was reported as "state unavailable" or a server error instead of the time limit or the cancellation. A refresh stopped after its branches and tags were already published now completes instead of waiting for the owner to resolve it.
- Restoring a backup no longer lets Git 2.54 or later start its own background maintenance in the repositories being restored. OwnGit turns Git's automatic maintenance off for every Git command it runs.
- Right after startup, a repository that was already prepared could briefly answer as still being prepared.
- On Windows under heavy load, stopping a check or Git process could report a cleanup error when Windows briefly counted no processes in its group.
- When a network failure keeps a runner from reporting a job, the runner log names the job.
- A runner whose token belongs to another repository says so instead of calling the token unknown or revoked.

### Security

- A password or credential file is sent to a server inferred from `origin` only when its first line names that server, and a file that names a server is never sent to another one, so a clone with a hostile `origin` does not receive your secrets.
- Forwarded headers are used only from proxies you name, and `X-Forwarded-Host` is used only when it names a Host OwnGit already accepts.
- Requests forwarded by Tailscale Funnel are refused, and `Tailscale-User-*` headers grant nothing.
- `owngit check run` inspects the worktree without starting the clone's `core.fsmonitor` program or its index hooks.

### Changes for scripts (details)

- New helper credential files written by `helper-credential create` and runner token files written by `runner-credential issue` start with an `owngit-server:` line; scripts that read the file as a bare token must take its last line. Both commands' JSON adds `token_file_server`, and `owngit runner` refuses a token file for another server.
- `owngit pr`, `owngit check` and `owngit repo` no longer fail with `invalid_arguments` when `--server` or `--repository` is missing. They read the clone's `origin` remote and fail with `origin_unavailable`, `origin_ambiguous`, `origin_unsupported` or `origin_server_mismatch` when that is not possible. `owngit check` without `--credential-file` reports "--credential-file is required.".
- A password or credential file whose first line starts with `owngit-server:` must hold exactly `owngit-server: ORIGIN` there, or it is refused with `invalid_credential_origin`. A file without that line must hold the secret on one line; files with more lines are refused (`invalid_password_file`, `invalid_credential_file`).
- `setup-link` and `reset-admin` refuse an argument they do not know instead of ignoring the options after it.
- A gzip Git request body followed by more data, or by a second gzip stream, is refused with HTTP 400 as an invalid body. Before, the extra data was ignored. Git, JGit and libgit2 do not send such bodies.
- `owngit tailscale on` and the Settings switch refuse when HTTPS port 443 already has a Tailscale Serve entry that OwnGit did not make, including one that points at OwnGit. Remove it with the command OwnGit shows, then turn sharing on.

## [1.0.3] - 2026-09-25

**Upgrading:** the state database moves to schema 15 and offline backups to version 10. OwnGit 1.0.3 upgrades a 1.0.x database the first time it starts and still restores backups of versions 1, 2, 9 and 10. Earlier versions refuse the upgraded database and the new backups with a "newer version" message. To go back, restore a backup made before upgrading with the earlier version. Before you upgrade, stop OwnGit and make a backup with your current version's `owngit backup` (see Offline backups in the operations guide). Any 1.0.3 command that opens the state, including `backup`, upgrades the database first, and it logs one line when it does.

**Changes for scripts:** `import add` and `import refresh` exit with status 3 when a finished run left refs that differ from the source, and with status 130 when cancelled. `check run` without `--check` runs only the checks committed in the revision it tests. Commands called without an action, and actions given another action's options, now fail with usage. Details are below.

### Added

- First-run setup in the terminal. When `owngit serve` starts in an interactive terminal on an installation that is not set up, it asks the setup questions there, starting with the language, and hides passwords as you type. You can choose the web dashboard instead: the browser and the terminal show the same short code, and you approve that browser in the terminal. Services, background jobs and redirected output keep the setup link flow. The last step prints a ready-to-copy command for reaching OwnGit over Tailscale when Tailscale is running; it changes nothing.
- Pull requests can be closed without merging and reopened, in the browser, the API and `owngit pr close` and `owngit pr reopen`. Only one open pull request per source and target branch is allowed.
- A sidebar replaces the tab row. Outside a repository it lists Home, All activity, Settings and your repositories by recent activity; inside one it lists the repository's pages. On narrow screens it folds into a menu.
- The repository overview shows the README and the languages the repository is written in. Language colors come from GitHub Linguist.
- Markdown files are rendered, with a Preview and Source switch; folders show their README. Rendering runs in a separate, time- and memory-limited process with raw HTML turned off.
- The Code tab previews PNG, JPEG, GIF and WebP images up to 10 MB and has "Download this file". Long lines do not wrap unless you turn on Wrap lines, which is remembered.
- Commit and pull request pages list the changed files first and show all diffs on one page, with controls to collapse each file or all of them.
- A notice on the dashboard when a new OwnGit release is available, with links to the release notes and install instructions. OwnGit checks GitHub about 30 seconds after it starts and then daily, and sends nothing but its version. Turn it off in Settings or with `owngit serve --no-update-check`. OwnGit does not download or install updates.
- Idle-time repository maintenance. Five minutes after a repository was last changed and used, OwnGit packs its refs and objects and writes a commit graph; between 03:00 and 05:00 it also combines repositories with many packs. It never removes objects, refs or kept history, and it steps aside for pushes, checks and page views.
- Arch Linux and Omarchy: each release includes a `PKGBUILD` that installs the release binary with `makepkg -si`. See the README.
- `owngit <command> --help` prints the command's usage and options.
- Korean versions of the operations, automatic checks, coding tools and security guides.

### Changed

- Pull request changes are compared from the merge base, like `git diff target...source`. Files that only the target branch received are no longer listed. Without a single common commit, the page says so instead of showing a list.
- Repository pages start far fewer Git processes (for example, pull request details 3 instead of 46) and keep recently read folders, files, diffs and comparisons in memory, up to 64 MiB. A page opened again answers even while a push holds the repository. Server memory can peak about 100 MB higher.
- Refs changed directly in the repository folder, without OwnGit, reach code and commit pages after OwnGit next writes to that repository or restarts, as they already did on the dashboard.
- When one repository cannot be prepared at startup, for example because its network share hangs, OwnGit serves the other repositories and keeps retrying that one in the background. Until it is ready, its pages show a notice and Git requests answer 503.
- A busy repository no longer stalls the dashboard. The dashboard waits about a second and then shows the last listing or "In use"; a busy repository page answers 503 with `Retry-After`.
- A missing branch, tag, path or commit answers 404 on every repository tab, with a way back to the repository.
- Check jobs for several branches pushed in quick succession wait for the repository instead of ending as unavailable.
- Saving a new check policy no longer queues again the branches and pull requests that already had a job. Jobs still waiting when a new policy is saved end as `interrupted`.
- `owngit runner` keeps running while OwnGit restarts or the network drops, retrying with backoff. It stops only when its token or requests are refused.
- `owngit import cancel` cancels a first import at any stage before the repository is published and leaves no repository, source or credentials behind.
- On macOS, when Git on the PATH is Apple's `/usr/bin/git` shim, OwnGit runs the Git behind it if the version matches, which halves the cost of each Git process.
- OwnGit holds a lock file in the repository folder while it runs, so a second OwnGit started from a copied state directory stops instead of changing the repositories.
- The Import tab is shown to everyone who can open the repository. The source address, credentials and technical messages are shown only to administrators.
- Password length rules count characters instead of bytes (8 to 1024).
- Light, Dark and System appearance work without JavaScript.
- `import add` refuses a name that already has a repository. `import add` and `import refresh` exit with status 3 when a finished run left refs that differ from the source and list them, and with status 130 when cancelled.
- Commands without an action print usage and exit 1, and actions refuse options that belong to another action.
- When OwnGit upgrades an existing state database, it says so in one line, for example `state database upgraded from schema 14 to 15`: in the server log for `serve`, and on standard error for offline commands such as `backup`.
- A Markdown list or paragraph without blank lines that has more than about 8,000 `*`, `_` or `~` characters (underscores inside words count) shows as source from a smaller size than before, because rendering it can take the whole time limit on a slower machine. Documents made of ordinary paragraphs render as before.

### Fixed

- Clone, fetch and pull failed when Git compressed its request, for example when cloning a repository with 18 or more branches and tags. OwnGit now decompresses these requests with a size limit.
- Parallel Git commands with the correct shared password were refused by the sign-in limiter.
- One repository could take every transfer slot, and a waiting request could wait up to 30 minutes. A repository now uses at most four of the five slots, and a request that waits more than 90 seconds gets 503.
- A stalled or oversized upload held its connection and slot until the 30-minute deadline.
- A repository whose folder was missing or unreadable turned the dashboard into an error page. It is now listed as Preparing or Unreadable.
- Repository names ending with a dot could not be cloned or pushed.
- Merging a pull request whose source the target already contained wrote an empty merge commit. It is now recorded as merged, already up to date.
- A default branch change or repository deletion could fail with 409 while the activity graph was being counted.
- Saving an import token or Basic credential dropped a stored CA certificate, and the reverse. A failed or interrupted first import left its source and credentials behind.
- A ref deleted at the import source was shown as Tracked. It is now shown as Deleted at source.
- After a session expired, submitting a form returns to the page with the form after you sign in again.
- Result notices, such as "Pull request merged", could be shown by opening a crafted address and appeared again on reload. They now appear once, after the action.
- Signing out of shared access was confirmed only after the next sign-in.
- Merging or reviewing after a branch was deleted answered 500 in the API.
- Stopping OwnGit during a Git transfer now ends the transfer and exits normally. Refused pushes are logged.
- On Windows, an automatic check whose command succeeded was sometimes recorded as `error` with a cleanup error such as "owned job membership changed during process capture".
- On Windows, cancelling a Git request, for example when the client disconnects, could leave a Git process that produced no output running until it wrote output or exited on its own. It now stops at once.

### Security

- `owngit check run` without `--check` no longer falls back to a check configuration recorded from another branch. A command that is not committed in the revision being tested never runs on your machine.

## [1.0.2] - 2026-09-24

### Added

- OwnGit can be installed with Homebrew (`brew install juliankang4/tap/owngit`) or npm (`npm install -g owngit`), or downloaded from GitHub Releases. See the README.
- `owngit forget-check-container --job ID --confirm-container-removed` releases the container cleanup record of a finished check job whose Docker daemon OwnGit can no longer reach, for example after Docker was reset. Such a record used to block deleting the repository and cleaning up check workspaces at startup, and nothing could clear it.

### Changed

- Administrator entries are shown to everyone: the repository Settings tab, Delete repository, helper credentials and Automatic checks. Without an administrator session they carry a lock mark, ask for the administrator password when used, and then return to the page you asked for. Administrator data, such as storage paths, stays hidden.
- The storage folder is no longer shown in the top bar. Administrators see it on the Settings page.
- The dashboard and repository pages reuse each repository's branch and tag list until OwnGit next writes to that repository. A dashboard you have opened before no longer starts a Git process per repository, which matters most for repositories on a network share. Refs changed directly in the storage folder without OwnGit appear after OwnGit next writes to that repository or restarts.
- `owngit import credentials` no longer shows the password or token while you type it. Interrupting the prompt with Ctrl+C, `Ctrl+\`, a closed terminal or `kill` (SIGTERM) leaves terminal echo on.

### Fixed

- Choosing the name `new` or `new-import` says that the name is reserved instead of asking for different characters.
- On the Automatic checks page, a number too long for any limit says that it is outside the accepted range instead of "Enter a number".
- During an import, a Git command whose input stopped early because reading the source failed now fails instead of being treated as complete. Smart HTTP push and fetch are unchanged.

### Security

- An import refresh request can no longer make OwnGit read up to 1 MiB of the request before the password is checked. OwnGit now reads at most 4 KiB.
- The page to return to after signing in refuses addresses with control characters. A sign-in link such as `/login?next=/%09/example.com` could otherwise send you to another site after you signed in.

## [1.0.1] - 2026-09-24

### Added

- Repositories have a Settings tab for administrators. It changes the default branch and holds a Delete repository page.
- Administrators can delete a repository. Deletion asks for the exact repository name and the administrator password, and offers two choices:
  - Keep files moves the Git folder to `.owngit-removed/` inside the storage folder. The page then shows a command that pushes its branches and tags into a new repository.
  - Delete files removes the Git folder.
- If OwnGit stops during a deletion, it finishes the deletion at the next start. It does not touch the storage folder while that folder is unavailable.

### Changed

- The repository page shows the activity heatmap first. Below it are a summary of the default branch, branch and tag counts, the last commit, open pull requests and the latest check. Recent commits, branches and tags follow.
- Branch, tag and kept-history lists show the newest entries first and are shortened on repositories with many refs. A Show all link lists every entry.
- The check settings page is renamed Automatic checks. It has a status summary, five numbered setup steps and a copyable example check file. Limits are entered in seconds, minutes, sizes and cores.
- The setup page describes the storage folder accurately: it may be on this computer or on a mounted SMB or NFS share, and settings stay on this computer.
- Pages that cannot read a repository in time show an error page. Requests to the JSON API now stop after 25 seconds instead of 30.

### Fixed

- The dashboard no longer returns an empty reply when repositories are slow to read, for example on a network share with large repositories.
- Startup and the dashboard are much faster with many repositories. Activity counts are cached per branch tip, and the dashboard shows "still counting" instead of waiting.
- The repository sidebar can scroll to its last entry on tall lists.

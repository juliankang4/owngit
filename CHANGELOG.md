# Changelog

All notable changes to OwnGit are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

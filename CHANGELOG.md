# Changelog

All notable changes to OwnGit are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
- The page to return to after signing in refuses control characters.

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

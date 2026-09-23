# Changelog

All notable changes to OwnGit are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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

# Operations

## First-time setup

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve --no-open
```

On first run, OwnGit writes an owner-readable setup file inside the state directory. Open that file in the installation owner's browser. The setup secret is not printed or passed in a browser command argument.

The default address is `http://127.0.0.1:7654`. Setup configures repository storage, optional shared-password protection for general access, and a separate administrator password. Every later security-setting change requires entering the current administrator password. Setup finishes at an empty dashboard.

## Reaching the server from another device

OwnGit serves plain HTTP, so the connection is not encrypted. Use Tailscale or your own VPN to reach its private-network address. A Tailscale-related name alone does not prove that the whole path is protected. Ordinary LAN HTTP is supported after a one-time warning before passwords are accepted, and the interface keeps the connection status visible. OwnGit has no built-in TLS and does not support public Internet hosting.

To use a LAN name:

```sh
./bin/owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

`--allowed-host` is repeatable. The server accepts only requests whose Host matches an approved address. Approve another Host name from the installation host, then restart the server:

```sh
./bin/owngit approve-host gitbox.internal
```

## Host-owner recovery

Before setup is complete, issue a replacement setup link with:

```sh
./bin/owngit setup-link --base-url http://127.0.0.1:7654 --no-open
```

To reset a forgotten administrator password, provide it through an owner-readable file:

```sh
./bin/owngit reset-admin --password-file /path/to/owner-only-password-file
```

The password file must be a regular file. On Unix-like systems, it must not be readable by group or other users. OwnGit never accepts its contents as a command-line value. Resetting the administrator password revokes administrator sessions and leaves repositories unchanged.

OwnGit has no email or account recovery. Both recovery procedures require access to the installation host.

## Storage and backups

- The host-local state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available. It contains `owngit.sqlite` and must not be placed on a network share opened by other computers.
- Setup lets you choose a repository folder, including one on a separate disk. It warns when that folder appears to be on a network share because NAS repository storage has not been runtime-verified. The state database always remains on the host.
- OwnGit leaves existing files in the chosen repository folder unchanged and creates repositories there as ordinary bare repositories ending in `.git`.
- Retention refs preserve history replaced by force-push or deletion, but there is no browser restore or permanent-delete interface. Retention is not a backup. Back up both the repositories and the state database separately.

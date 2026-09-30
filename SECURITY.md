# Security policy

<p align="center"><b>English</b> | <a href="SECURITY.ko.md">한국어</a></p>

Report a security problem in OwnGit privately through GitHub, not in a public issue. This page says how, which versions receive fixes, and what OwnGit is built to protect.

## Reporting a vulnerability

Open the repository's **Security** tab on GitHub and choose **Report a vulnerability**.

Include the OwnGit version (`owngit version`), your operating system, and steps to reproduce. Do not include real passwords, tokens, or private repository contents; use made-up examples.

There is no guaranteed response time. Confirmed problems are fixed in a new release, and the changelog lists them under Security.

## Supported versions

Only the latest release receives fixes.

## Scope

OwnGit is meant for one owner, or a small group that shares one password, on a computer, NAS, or home server reached through a private network or VPN. Public Internet hosting is out of scope (see [Access and security](README.md#access-and-security)), so a report about exposing OwnGit directly to the Internet without a VPN or TLS reverse proxy describes expected behavior. The one exception is the optional public address for share links: it is off by default, and when the owner turns it on it must answer share links and the files their pages load and nothing else. Please report any path there that reaches anything more as a security problem.

What OwnGit does at the network boundary:

- It serves plain HTTP and has no built-in TLS. TLS comes from Tailscale on this computer, when the owner shares OwnGit on the tailnet over HTTPS, or from a reverse proxy in front of OwnGit.
- On its own address it refuses requests that Tailscale Funnel forwards from the Internet, and ignores `Tailscale-User-*` identity headers. The public address for share links is a separate listener, which the owner may connect to Funnel.
- It believes the `X-Forwarded-Proto`, `X-Forwarded-For`, and `X-Forwarded-Host` headers only from proxy addresses the owner configures, and from none by default.

An administrator can create a [share link](docs/OPERATIONS.md#share-links) that lets anyone who holds it, and its extra password when it has one, read one repository without the shared password until the link expires or is revoked. Treat a share link like a password.

Host and runner check commands run with their account's permissions and are not a sandbox.

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

OwnGit is meant for one owner, or a small group that shares one password, on a computer, NAS, or home server reached through a private network or VPN. Public Internet hosting is out of scope (see [Access and security](README.md#access-and-security)), so a report about exposing OwnGit directly to the Internet without a VPN or TLS reverse proxy describes expected behavior.

What OwnGit does at the network boundary:

- It serves plain HTTP and has no built-in TLS. TLS comes from Tailscale on this computer, when the owner shares OwnGit on the tailnet over HTTPS, or from a reverse proxy in front of OwnGit.
- It refuses requests that Tailscale Funnel forwards from the Internet, and ignores `Tailscale-User-*` identity headers.
- It believes the `X-Forwarded-Proto`, `X-Forwarded-For`, and `X-Forwarded-Host` headers only from proxy addresses the owner configures, and from none by default.

Host and runner check commands run with their account's permissions and are not a sandbox.

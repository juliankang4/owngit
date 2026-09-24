# Security policy

## Reporting a vulnerability

Please report security problems privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue for them.

Include the OwnGit version (`owngit version`), your operating system, and steps to reproduce. Do not include real passwords, tokens, or private repository contents; use synthetic examples.

There is no guaranteed response time. Confirmed problems are fixed in a new release, and the changelog lists them under Security.

## Supported versions

Only the latest release receives fixes.

## Scope

OwnGit is meant for a single owner on a computer, NAS, or home server reached through a private network or VPN. It serves plain HTTP without TLS, and public Internet hosting is out of scope (see [Access and security](README.md#access-and-security)). Reports about exposing OwnGit directly to the Internet without a VPN or TLS reverse proxy are expected behavior. Host and runner check commands run with their account's permissions and are not a sandbox.

# Packaging templates

The files here are inputs for `tools/release`. They produce portable archives, unsigned native prototypes, and package-manager files. None of them is a published package. [CONTRIBUTING.md](../CONTRIBUTING.md#releases) shows how to run the release tool.

## Layout

- `archive/README.txt.tmpl` is placed in every portable archive.
- `macos/` contains the AppKit launcher, the prototype `Info.plist`, and the app instructions.
- `linux/` contains the Debian control file, the desktop entry, and the package instructions.
- `homebrew/owngit.rb.tmpl` is the Homebrew formula template.
- `winget/` holds the WinGet portable-package manifest templates.
- `notices/README.md.tmpl` renders the third-party notice index. `release notices` writes it; edit the template rather than a generated copy.

## Versions and inputs

Every artifact takes its version from `internal/version.Version`. Native prototypes are built only from a verified portable output, so each package records the portable manifest, archive, and binary digests it contains. The `-baseline` label for `native` may contain only letters, digits, and `._:=;,+-`, and must not include a local path or secret.

## Native prototypes

`native` writes `native-manifest.json` and `SHA256SUMS`. Every artifact and its packaged provenance record state that it is an unsigned prototype.

The macOS app holds the `darwin/arm64` binary and a small AppKit launcher. The launcher starts `owngit serve --open`, which opens the private setup file before setup and the dashboard afterwards. It shows early process errors, sends `SIGTERM` when you quit, and does not run as a service or update itself. The release tool checks the plist, rejects a launcher that embeds a local source or home path, and verifies the disk image with `hdiutil`. It does not sign the app or DMG with a publisher identity. The ad hoc signature that Apple's linker may add to a Mach-O file is not a publisher signature. Building it requires an Apple silicon Mac with Xcode command-line tools.

Each DEB holds the `linux/amd64` or `linux/arm64` binary, a terminal-backed desktop entry that runs `owngit serve --open`, the license, the notices, the instructions, and provenance. It depends on `git` and has no maintainer scripts, systemd units, or login-start entries.

The bundle identifier and the Debian maintainer address are placeholders, not publisher identities.

## Homebrew and WinGet

`packaging` renders these files from a portable manifest. `-formats` accepts `homebrew`, `winget`, or `all`. The download base URL, the Homebrew tap, and the WinGet package identifier and publisher are explicit inputs. A missing input produces an `UNREADY` marker, or an error with `-strict`.

The Homebrew formula installs the binary, license, and notices, depends on `git`, supports Apple silicon Macs and Linux on x64 and ARM64, and offers a `brew services` entry that runs `owngit serve -no-open`. The WinGet installer manifest describes the zip archive with a nested portable `owngit` command and its SHA-256.

The native prototypes add no login-start entry, and no package removes state or repositories.

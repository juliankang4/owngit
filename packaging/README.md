# Packaging templates

The files here are inputs for `tools/release`. They produce portable archives, unsigned native prototypes, and package-manager files. None of them is a published package. [CONTRIBUTING.md](../CONTRIBUTING.md#releases) shows how to run the release tool.

## Layout

- `archive/README.txt.tmpl` is placed in every portable archive.
- `macos/` contains the AppKit launcher, the prototype `Info.plist`, and the app instructions.
- `linux/` contains the Debian control file, the desktop entry, and the package instructions.
- `homebrew/owngit.rb.tmpl` is the Homebrew formula template.
- `winget/` holds the WinGet portable-package manifest templates.
- `npm/` holds the npm launcher (`owngit.js.tmpl`) and the README templates of the npm packages.
- `notices/README.md.tmpl` renders the third-party notice index. `release notices` writes it; edit the template rather than a generated copy.

## Versions and inputs

Every artifact takes its version from `internal/version.Version`. Native prototypes are built only from a verified portable output, so each package records the portable manifest, archive, and binary digests it contains. The `-baseline` label for `native` may contain only letters, digits, and `._:=;,+-`, and must not include a local path or secret.

## Native prototypes

`native` writes `native-manifest.json` and `SHA256SUMS`. Every artifact and its packaged provenance record state that it is an unsigned prototype.

The macOS app holds the `darwin/arm64` binary and a small AppKit launcher. The launcher starts `owngit serve --open`, which opens the private setup file before setup and the dashboard afterwards. It shows early process errors, sends `SIGTERM` when you quit, and does not run as a service or update itself. The release tool checks the plist, rejects a launcher that embeds a local source or home path, and verifies the disk image with `hdiutil`. It does not sign the app or DMG with a publisher identity. The ad hoc signature that Apple's linker may add to a Mach-O file is not a publisher signature. Building it requires an Apple silicon Mac with Xcode command-line tools.

Each DEB holds the `linux/amd64` or `linux/arm64` binary, a terminal-backed desktop entry that runs `owngit serve --open`, the license, the notices, the instructions, and provenance. It depends on `git` and has no maintainer scripts, systemd units, or login-start entries.

The bundle identifier and the Debian maintainer address are placeholders, not publisher identities.

## Homebrew, WinGet, and npm

`packaging` renders these files from a portable manifest. `-formats` accepts `homebrew`, `winget`, `npm`, or `all`. The download base URL, the project homepage, the npm repository URL, the Homebrew tap, and the WinGet package identifier and publisher are explicit inputs. A missing input produces an `UNREADY` marker, or an error with `-strict`. Every format requires a manifest that holds all four release targets.

The Homebrew formula installs the binary, license, and notices, depends on `git`, supports Apple silicon Macs and Linux on x64 and ARM64, and offers a `brew services` entry that runs `owngit serve -no-open`. The WinGet installer manifest describes the zip archive with a nested portable `owngit` command and its SHA-256.

The npm format writes one directory per package under `<out>/npm/`, ready for `npm pack` or `npm publish`. `<out>/npm` must not exist yet. Because the packages contain the executables, `packaging` first verifies a private snapshot of the portable output, like `native`, and copies each executable only if its digest matches the manifest. `-go` names the Go toolchain used for that verification.

- `owngit` holds `bin/owngit.js`, the README, and the license. Its `optionalDependencies` pin each platform package to the same version, and `engines.node` is `>=18`.
- `owngit-darwin-arm64`, `owngit-linux-x64`, `owngit-linux-arm64`, and `owngit-win32-x64` each hold `bin/owngit` (`bin/owngit.exe` on Windows) from that target's archive, the license, the notices, and a short README. Their npm `os` and `cpu` fields let npm install only the matching one.
- None of the packages has an install script. The launcher runs the executable with the same arguments and standard streams and exits with its exit code, or with its terminating signal. Ctrl+C and Ctrl+\ reach the executable from the terminal, so the launcher only outlives them. On macOS and Linux it passes `SIGTERM` and `SIGHUP` on to the executable. It prints the supported platforms when the platform is unsupported or the platform package is missing, for example after `--omit=optional` in a project install. (npm 12 still installs the platform package for a global install with `--omit=optional`.)
- Launcher limits: Node does not keep an inherited ignored `SIGHUP`, so a server started through the launcher with `nohup owngit serve &` stops when the terminal or SSH session closes. A `SIGINT` or `SIGQUIT` sent only to the launcher, for example Ctrl+C in `docker run` without `-t`, does not reach the server; `SIGTERM` does. For a server that keeps running, use a service manager (launchd, systemd, or `brew services` for the Homebrew install) or run the executable from the platform package or a release archive directly. The npm package README states the same limits.
- An unready package gets a `"//"` note, placeholder URLs, and `"private": true`, which makes `npm publish` refuse it.
- Publish the four platform packages first and `owngit` last, so no install sees version pins that do not resolve yet. A published README cannot be changed for that version.

The npm route needs Node.js to install the packages and to start the launcher. The executable does not use Node, and the other routes need no Node.

The native prototypes add no login-start entry, and no package removes state or repositories.

# Packaging templates

Most files here are inputs for `tools/release`. They produce portable archives, native prototypes, and package-manager files. Every output is unsigned unless a [signed macOS release](#signed-macos-release) is requested. `packaging` output is what gets published to the Homebrew tap and npm and attached to GitHub Releases as the Arch Linux `PKGBUILD`; the native prototypes are not published. [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md#releases) shows how to run the release tool.

## Layout

- `archive/README.txt.tmpl` is placed in every portable archive.
- `container/` holds the container image files, which `tools/release` does not use. The `container publish` workflow builds the image from a published release; see [Releases in docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md#releases). `context.sh` checks the release's two Linux archives against `SHA256SUMS` and unpacks them as the build context, `Dockerfile` builds the image from that context, and `compose.yaml` is the Compose file that users save to run the image.
- `installer/` holds the release scripts: the one-line installers `install.sh` for Linux and macOS and `install.ps1` for Windows, and the Proxmox VE helper `proxmox.sh`, which root runs on a Proxmox VE host to create a container and run `install.sh` in it. `build` copies all three unchanged into its output folder and records their SHA-256 in `manifest.json`, so every release carries the same files. Their tests are `tools/release/installer_test.go` and `tools/release/proxmox_test.go`.
- `macos/` contains OwnGit.app, the menu bar icon (AppKit), its `Info.plist` template, and the app instructions.
- `linux/` contains the Debian control file, the desktop entry, and the package instructions.
- `homebrew/owngit.rb.tmpl` is the Homebrew formula template.
- `winget/` holds the WinGet portable-package manifest templates.
- `aur/` holds the Arch Linux `PKGBUILD` and `.SRCINFO` templates of the `owngit-bin` package.
- `npm/` holds the npm launcher (`owngit.js.tmpl`) and the README templates of the npm packages.
- `notices/README.md.tmpl` renders the third-party notice index. `release notices` writes it; edit the template rather than a generated copy.

## Versions and inputs

Every artifact takes its version from `internal/version.Version`. Native prototypes are built only from a verified portable output, so each package records the portable manifest, archive, and binary digests it contains. The `-baseline` label for `native` may contain only letters, digits, and `._:=;,+-`, and must not include a local path or secret.

## Native prototypes

`native` writes `native-manifest.json` and `SHA256SUMS`. Every artifact and its packaged provenance record state that it is a prototype, and whether it is signed and notarized.

OwnGit.app is the menu bar icon: a small AppKit launcher (`packaging/macos`) with the bundle identifier `app.owngit.OwnGit`. It never runs the server; it reads the tray status of the OwnGit service and runs the `owngit` program for service and tray commands. The app in the disk image holds the `darwin/arm64` binary at `OwnGit.app/Contents/Helpers/owngit`, where Apple places helper tools. The macOS release archive holds the app beside the `owngit` program instead, and the app then runs the program beside it (or in the `bin` folder beside it, as Homebrew lays it out). The release tool checks the plist, rejects a launcher that embeds a local source or home path, and verifies the disk image with `hdiutil`. Unless signing is requested, it does not sign the app or DMG with a publisher identity. The ad hoc signature that Apple's linker may add to a Mach-O file is not a publisher signature. Building it requires an Apple silicon Mac with Xcode command-line tools.

Earlier prototypes kept the binary at `OwnGit.app/Contents/Resources/bin/owngit`. A service installed with `owngit service install` from that path records it in `~/Library/LaunchAgents/app.owngit.server.plist`, so the service no longer starts once the app is replaced. Run `OwnGit.app/Contents/Helpers/owngit service install` again from the new app to point the service at the new path.

Each DEB holds the `linux/amd64` or `linux/arm64` binary, a terminal-backed desktop entry that runs `owngit serve --open`, the license, the notices, the instructions, and provenance. It depends on `git` and has no maintainer scripts, systemd units, or login-start entries.

The Debian maintainer address is a placeholder, not a publisher identity.

## Signed macOS release

A release built on the maintainer's Mac can sign every macOS artifact with a Developer ID, notarize it with Apple, and staple the ticket where Apple allows it. A downloaded OwnGit then opens without Gatekeeper's warning. Without the two signing options below, nothing is signed, and CI and development builds need no Apple account.

### One-time setup

These steps run in the maintainer's own terminal and Apple account. No password, key, or app-specific password is given to the release tool, written to a file in the repository, or passed as an argument to it.

1. Create a **Developer ID Application** certificate. The Apple Developer Program Account Holder can do it in Xcode (Settings, Accounts, select the team, Manage Certificates, add "Developer ID Application") or on the developer website (Certificates, Identifiers & Profiles, Certificates, add "Developer ID Application", upload a certificate signing request made with Keychain Access, then open the downloaded certificate). The private key stays in the login keychain.
2. Check that the identity is usable and copy its exact name:

   ```sh
   security find-identity -v -p codesigning
   ```

   The name looks like `Developer ID Application: Your Name (TEAMID1234)`. The ten characters in parentheses are the Team ID, also shown under Membership details on the developer website. `-sign-identity` takes this name and nothing else, so it must match exactly one identity. A renewed certificate has the same name as the old one, and `codesign` then stops because the name is ambiguous. After renewing, delete the certificate you no longer use (with its private key) from the login keychain in Keychain Access. Code signed earlier stays valid, because its signature carries a secure timestamp. If Keychain Access shows the certificate but this list does not, install the "Developer ID - G2" intermediate certificate from [Apple PKI](https://www.apple.com/certificateauthority/).
3. Store notarization credentials in the keychain as the profile `owngit-notary`. With an app-specific password made at [account.apple.com](https://account.apple.com) (Sign-In and Security, App-Specific Passwords):

   ```sh
   xcrun notarytool store-credentials owngit-notary --apple-id YOUR_APPLE_ID --team-id TEAMID1234
   ```

   `notarytool` asks for the password at a prompt and checks it with Apple before saving. An App Store Connect API key works too: `--key AuthKey_XXXX.p8 --key-id XXXX --issuer ISSUER_ID` instead of the Apple ID options; the key file can be deleted afterwards because the profile holds it.

### Release commands

```sh
IDENTITY="Developer ID Application: Your Name (TEAMID1234)"

go run ./tools/release build -out dist/portable \
  -sign-identity "$IDENTITY" -notary-profile owngit-notary

go run ./tools/release native -formats macos \
  -manifest dist/portable/manifest.json -out dist/native-macos \
  -baseline "$REVIEWED_BASELINE" \
  -sign-identity "$IDENTITY" -notary-profile owngit-notary

go run ./tools/release native -formats deb \
  -manifest dist/portable/manifest.json -out dist/native-deb \
  -baseline "$REVIEWED_BASELINE"
```

The two options go together, need a Mac, and take only names. `-sign-identity` must be the full certificate name, from which the tool takes the Team ID. A signed `native` run builds only the `macos` format, so the Debian packages are built in their own unsigned run. `packaging` needs no options: the Homebrew formula and the npm `owngit-darwin-arm64` package take the signed binary from the portable archive.

### What the tool does

- `build` signs the `darwin/arm64` `owngit` binary right after compiling it, with the hardened runtime, a secure timestamp, and the identifier `app.owngit.cli`, and checks the signature with `codesign --verify --strict` against the Developer ID requirement of the team. It signs OwnGit.app for the archive the same way and checks it with `codesign --verify --deep --strict`. It submits the binary and the app to Apple in one zip made with `ditto`, prints the submission id, and waits for the result. It then staples the ticket to the app and asks Gatekeeper (`spctl --assess`) to accept it. Only then does it run the binary, archive it, and write `SHA256SUMS` and `manifest.json`. A bare binary cannot hold a stapled ticket, so Gatekeeper looks the ticket up online.
- `native` puts that binary into `OwnGit.app` unchanged and requires it to be signed by the same team. It signs the app (bundle identifier `app.owngit.OwnGit`, hardened runtime, timestamp), checks it with `codesign --verify --deep --strict`, creates the DMG, signs it as `app.owngit.dmg`, checks it and runs `hdiutil verify`, notarizes the DMG, staples the ticket to it with `stapler`, and asks Gatekeeper (`spctl --assess`) to accept both the DMG and the app. Apple notarizes the app and both executables as part of the DMG.
- Neither executable needs entitlements.

### Records

The manifests describe the signed bytes that ship: archive and DMG digests, `SHA256SUMS`, and per-file digests. The signed `darwin/arm64` artifact in `manifest.json` also carries `apple_signature` with `team_id`, `identifier`, `notary_submission`, and `unsigned_sha256`, the digest of the Go linker output before signing. The Go build is reproducible and the signature is not, so `unsigned_sha256` is the value to compare when rebuilding the tagged source with the same Go toolchain. The DMG in `native-manifest.json` carries `apple_signature` without `unsigned_sha256`, `publisher_signed` and `notarized` are true, and its status says it is a signed and notarized prototype. An unsigned build has no `apple_signature`.

`verify` checks a recorded signature on macOS with `codesign` against the recorded team and identifier, and on another host says that it could not check it. It does not contact Apple.

### Failures

Every failure stops the command before `SHA256SUMS` and the manifest are written, with one exception: `build` verifies its output after writing them, so when that final verification fails or is interrupted, they remain and the error says to check them again with `release verify -dir <out>`. The binary and the DMG are made in private folders outside the output folder (the DMG's folder is a hidden folder beside it) and removed afterwards, so a binary or DMG that failed signing, notarization, stapling, or Gatekeeper's check is never left in it. Archives of other targets written earlier in the same `build` run can remain; they carry no signature claim. An interruption (Ctrl-C or `SIGTERM`) takes the same path: the running tool is stopped, the temporary folders are removed, and a submission already made is named. Only a forced kill (`SIGKILL`) or a second interrupt, which ends the command at once, can leave a temporary folder behind (a hidden `.<out>-stage-*` folder beside the output folder, or `owngit-*` folders in the temporary directory), which can be deleted.

- A missing or unusable identity stops at `codesign` with its message; check `security find-identity -v -p codesigning`.
- When Apple does not accept a submission, the error names the submission id, Apple's status and message, and the issues from `notarytool log`. `xcrun notarytool log <id> --keychain-profile owngit-notary` prints the full log.
- When waiting ends without a result (interrupted, timed out, or offline), the error names the submission id; Apple keeps processing it. `xcrun notarytool info <id> --keychain-profile owngit-notary` shows the result. Run the command again for a release; each run makes a new submission. Apple asks for no more than 75 submissions a day.
- An error after Apple accepted a submission, from stapling or from Gatekeeper's check, names the submission id as well.

## Homebrew, WinGet, npm, and Arch Linux

`packaging` renders these files from a portable manifest. `-formats` accepts `homebrew`, `winget`, `npm`, `aur`, or `all`. The download base URL, the project homepage, the npm repository URL, the Homebrew tap, the WinGet package identifier and publisher, and the `PKGBUILD` maintainer line are explicit inputs. A missing input produces an `UNREADY` marker, or an error with `-strict`. Every format requires a manifest that holds all four release targets, with archive names that match the version and SHA-256 values of 64 lowercase hexadecimal characters; `packaging` refuses any other manifest before it writes a file. The `aur` format also refuses a version that `makepkg` does not accept as `pkgver` with `-strict`, and marks it `UNREADY` otherwise.

The Homebrew formula installs the binary, license, and notices, and on macOS OwnGit.app from the archive into the formula's prefix, beside its `bin` folder, depends on `git`, supports Apple silicon Macs and Linux on x64 and ARM64, and offers a `brew services` entry that runs `owngit serve -no-open`. The WinGet installer manifest describes the zip archive with a nested portable `owngit` command and its SHA-256.

The `aur` format writes `<out>/aur/PKGBUILD` and `<out>/aur/.SRCINFO` for the `owngit-bin` package. The `.SRCINFO` holds the same fields that `makepkg --printsrcinfo` prints for the `PKGBUILD`. The package takes the `linux/amd64` archive on `x86_64` and the `linux/arm64` archive on `aarch64`, each with the SHA-256 from the manifest, and installs the unchanged binary as `/usr/bin/owngit` (`!strip` and `!debug`), the license in `/usr/share/licenses/owngit-bin/`, and the notices and coding-tool documents in `/usr/share/doc/owngit-bin/`. It depends on `git`, provides and conflicts with `owngit`, and has no install scripts or systemd units. `-aur-maintainer` sets the `# Maintainer:` comment; the AUR convention is `Name <address at domain dot tld>`.

The npm format writes one directory per package under `<out>/npm/`, ready for `npm pack` or `npm publish`. `<out>/npm` must not exist yet. Because the packages contain the executables, `packaging` first verifies a private snapshot of the portable output, like `native`, and copies each executable only if its digest matches the manifest. `-go` names the Go toolchain used for that verification.

- `owngit` holds `bin/owngit.js`, the README, and the license. Its `optionalDependencies` pin each platform package to the same version, and `engines.node` is `>=18`.
- `owngit-darwin-arm64`, `owngit-linux-x64`, `owngit-linux-arm64`, and `owngit-win32-x64` each hold `bin/owngit` (`bin/owngit.exe` on Windows) from that target's archive, the license, the notices, and a short README. `owngit-darwin-arm64` also holds `bin/OwnGit.app` when the macOS archive has it. Their npm `os` and `cpu` fields let npm install only the matching one.
- None of the packages has an install script. The launcher runs the executable with the same arguments and standard streams and exits with its exit code, or with its terminating signal. Ctrl+C and Ctrl+\ reach the executable from the terminal, so the launcher only outlives them. On macOS and Linux it passes `SIGTERM` and `SIGHUP` on to the executable. It prints the supported platforms when the platform is unsupported or the platform package is missing, for example after `--omit=optional` in a project install. (npm 12 still installs the platform package for a global install with `--omit=optional`.)
- Launcher limits: Node does not keep an inherited ignored `SIGHUP`, so a server started through the launcher with `nohup owngit serve &` stops when the terminal or SSH session closes. A `SIGINT` or `SIGQUIT` sent only to the launcher, for example Ctrl+C in `docker run` without `-t`, does not reach the server; `SIGTERM` does. For a server that keeps running, use a service manager (launchd, systemd, or `brew services` for the Homebrew install) or run the executable from the platform package or a release archive directly. The npm package README states the same limits.
- An unready package gets a `"//"` note, placeholder URLs, and `"private": true`, which makes `npm publish` refuse it.
- Publish the four platform packages first and `owngit` last, so no install sees version pins that do not resolve yet. The `npm publish` workflow does this with npm trusted publishing; see [Releases in docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md#releases). A published README cannot be changed for that version.

The npm route needs Node.js to install the packages and to start the launcher. The executable does not use Node, and the other routes need no Node.

OwnGit.app, from the disk image or beside the program, registers itself to open at sign-in the first time it opens; the owner can turn that off in its settings. No package removes state or repositories.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// macLauncherSources are the Swift files of OwnGit.app's launcher, the menu
// bar icon, in packaging/macos.
var macLauncherSources = []string{"Launcher.swift", "Notifications.swift", "Panel.swift", "ProtectedPath.swift", "TrayStatus.swift"}

const macMinimumSystemVersion = "13.0"

var appleBundleVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,2}$`)

func buildMacPrototype(inputs nativeInputs, outDir string) (nativeArtifact, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nativeArtifact{}, errors.New("the macOS prototype requires a native darwin/arm64 build host")
	}
	if !appleBundleVersionPattern.MatchString(inputs.manifest.Version) {
		return nativeArtifact{}, fmt.Errorf("version %q is not valid for CFBundleVersion", inputs.manifest.Version)
	}
	payload, ok := inputs.payloads["darwin/arm64"]
	if !ok {
		return nativeArtifact{}, errors.New("no darwin/arm64 portable payload")
	}
	signer := inputs.signer
	signed := signer != nil
	// The helper is signed and notarized once, by release build, and ships
	// in the app unchanged, so a signed app needs a portable binary signed by
	// the same team. Verifying the portable snapshot has already checked
	// that signature against the binary on this Mac.
	if signed {
		if recorded := payload.built.AppleSignature; recorded == nil || recorded.TeamID != signer.team {
			return nativeArtifact{}, fmt.Errorf("the portable darwin/arm64 binary is not signed by team %s; build the portable output with the same -sign-identity and -notary-profile", signer.team)
		}
	}
	shared, err := explicitSharedFiles(payload)
	if err != nil {
		return nativeArtifact{}, err
	}
	provenance, err := provenanceBytes(inputs, payload, "macos-dmg", signed)
	if err != nil {
		return nativeArtifact{}, err
	}
	readme, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "macos", "README.txt.tmpl"), struct {
		Version, Helper string
		Signed          bool
	}{inputs.manifest.Version, "OwnGit.app/" + appHelperPath, signed})
	if err != nil {
		return nativeArtifact{}, err
	}

	// The app and the DMG are made in a private folder and removed on every
	// path. The DMG moves into outDir only after every check passed, because
	// a signed DMG seals the claim that it is notarized, and the output
	// folder must only receive artifacts whose claims hold. The folder sits
	// beside outDir so that the final move is a rename on the same volume.
	stage, err := os.MkdirTemp(filepath.Dir(outDir), "."+filepath.Base(outDir)+"-stage-")
	if err != nil {
		return nativeArtifact{}, err
	}
	defer os.RemoveAll(stage)
	volume := filepath.Join(stage, "volume")
	app := filepath.Join(volume, "OwnGit.app")
	if err := buildIconApp(inputs.run, inputs.xcrun, inputs.root, app, inputs.manifest.Version); err != nil {
		return nativeArtifact{}, err
	}

	helper := "OwnGit.app/" + appHelperPath
	files := []nativePackageFile{
		{path: helper, mode: 0o755, data: payload.binary.data},
		{path: "OwnGit.app/Contents/Resources/package-provenance.json", mode: 0o644, data: provenance},
		{path: "README.txt", mode: 0o644, data: readme},
	}
	for _, file := range shared {
		name := filepath.ToSlash(filepath.Join("OwnGit.app", "Contents", "Resources", file.path))
		files = append(files, nativePackageFile{path: name, mode: file.mode, data: file.data})
	}
	// The coding-tool resources, taken from the verified portable payload so
	// the app ships the same bytes that artifact was verified with.
	resources, err := resourceFiles(payload)
	if err != nil {
		return nativeArtifact{}, err
	}
	for _, file := range resources {
		name := filepath.ToSlash(filepath.Join("OwnGit.app", "Contents", "Resources", file.path))
		files = append(files, nativePackageFile{path: name, mode: file.mode, data: file.data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for _, file := range files {
		if err := validateNativePath(file.path); err != nil {
			return nativeArtifact{}, err
		}
		path := filepath.Join(volume, filepath.FromSlash(file.path))
		if _, err := writeFile(path, file.data, os.FileMode(file.mode)); err != nil {
			return nativeArtifact{}, err
		}
	}
	packagedBinaryDigest, err := sha256File(filepath.Join(volume, filepath.FromSlash(helper)))
	if err != nil {
		return nativeArtifact{}, err
	}
	if packagedBinaryDigest != payload.binary.sha {
		return nativeArtifact{}, errors.New("the app binary does not match the verified portable binary")
	}
	// Signing the app signs its launcher as the main executable and seals
	// the helper, which is already signed, together with the resources.
	if signed {
		if err := signer.sign(app, "", true); err != nil {
			return nativeArtifact{}, err
		}
		if err := checkDeveloperIDSignature(inputs.run, app, signer.team, appleBundleID, true); err != nil {
			return nativeArtifact{}, err
		}
	}
	// The manifest lists what the volume holds after signing, which
	// includes the signed launcher and the app's signature files.
	shipped, err := volumeFiles(volume)
	if err != nil {
		return nativeArtifact{}, err
	}

	name := fmt.Sprintf("owngit_%s_darwin_arm64_prototype.dmg", inputs.manifest.Version)
	dmgPath := filepath.Join(stage, name)
	volumeName := "OwnGit Prototype " + inputs.manifest.Version
	if err := createDiskImage(inputs.run, time.Sleep, inputs.hdiutil, diskImageCreateArguments(volumeName, volume, dmgPath)); err != nil {
		return nativeArtifact{}, err
	}
	if signed {
		if err := signer.sign(dmgPath, appleDiskImageIdentifier, false); err != nil {
			return nativeArtifact{}, err
		}
		if err := checkDeveloperIDSignature(inputs.run, dmgPath, signer.team, appleDiskImageIdentifier, false); err != nil {
			return nativeArtifact{}, err
		}
	}
	if _, err := inputs.run(inputs.hdiutil, []string{"verify", dmgPath}, nil); err != nil {
		return nativeArtifact{}, err
	}
	// The DMG is the outermost container, so it alone is notarized; Apple
	// issues tickets for the app and the executables inside it as well. The
	// ticket is stapled to the DMG for offline checks, and Gatekeeper then
	// has to accept both the DMG and the app.
	var signature *appleSignature
	if signed {
		submission, err := signer.notarize(dmgPath)
		if err != nil {
			return nativeArtifact{}, err
		}
		if err := stapleAndAssess(signer, dmgPath, app); err != nil {
			return nativeArtifact{}, fmt.Errorf("Apple accepted %s (submission %s), but %w", name, submission, err)
		}
		signature = &appleSignature{TeamID: signer.team, Identifier: appleDiskImageIdentifier, NotarySubmission: submission}
	}
	shippedPath := filepath.Join(outDir, name)
	if err := os.Rename(dmgPath, shippedPath); err != nil {
		return nativeArtifact{}, err
	}
	digest, err := sha256File(shippedPath)
	if err != nil {
		return nativeArtifact{}, err
	}
	info, err := os.Stat(shippedPath)
	if err != nil {
		return nativeArtifact{}, err
	}
	return nativeArtifact{
		Format: "macos-dmg", Target: "darwin/arm64", Name: name,
		SHA256: digest, Size: info.Size(), Prototype: true, PublisherSigned: signed, Notarized: signed,
		StructureVerified: true, NativeInstallVerified: false, PublicReady: false,
		Toolchain:        inputs.macToolchain,
		PortableArtifact: payload.built.Name, PortableArtifactSHA256: payload.built.SHA256,
		ApplicationBinarySHA256:  payload.binary.sha,
		PackagedProvenanceSHA256: sha256Bytes(provenance), AppleSignature: signature, Files: shipped,
	}, nil
}

func buildIconApp(run commandRunner, xcrun, root, app, version string) error {
	if !appleBundleVersionPattern.MatchString(version) {
		return fmt.Errorf("version %q is not valid for CFBundleVersion", version)
	}
	contents := filepath.Join(app, "Contents")
	launcherPath := filepath.Join(contents, "MacOS", "OwnGitLauncher")
	if err := os.MkdirAll(filepath.Dir(launcherPath), 0o755); err != nil {
		return err
	}
	infoPlist, err := renderNativeTemplate(filepath.Join(root, "packaging", "macos", "Info.plist.tmpl"), struct{ Version, BundleID, MinimumSystemVersion string }{version, appleBundleID, macMinimumSystemVersion})
	if err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(contents, "Info.plist"), infoPlist, 0o644); err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644); err != nil {
		return err
	}
	if err := buildAppIcon(run, root, filepath.Join(contents, "Resources", "AppIcon.icns")); err != nil {
		return fmt.Errorf("build the app icon: %w", err)
	}
	arguments := []string{"swiftc", "-target", "arm64-apple-macosx" + macMinimumSystemVersion, "-O", "-gnone", "-framework", "AppKit", "-framework", "ServiceManagement", "-o", launcherPath}
	for _, source := range macLauncherSources {
		arguments = append(arguments, filepath.Join(root, "packaging", "macos", source))
	}
	if _, err := run(xcrun, arguments, nil); err != nil {
		return err
	}
	if err := os.Chmod(launcherPath, 0o755); err != nil {
		return err
	}
	if err := rejectEmbeddedHostPaths(launcherPath, root); err != nil {
		return err
	}
	_, err = run("plutil", []string{"-lint", filepath.Join(contents, "Info.plist")}, nil)
	return err
}

func copyAppHelper(binary, app string) error {
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	_, err = writeFile(filepath.Join(app, appHelperPath), data, 0o755)
	return err
}

// buildAppIcon writes the app icon, which Finder, System Settings and
// notifications show: OwnGit's logo as opaque art that fills the square,
// which macOS 26 and later round to their own icon shape (art with a
// transparent edge would sit on a gray tile there), made into an icon set
// with sips, which reads SVG, and iconutil.
func buildAppIcon(run commandRunner, root, icns string) error {
	logo, err := readRegularInput(filepath.Join(root, "internal", "webui", "assets", "logo.svg"))
	if err != nil {
		return err
	}
	canvas := string(logo)
	for _, change := range [][2]string{{`width="40" height="40"`, `width="1024" height="1024"`}, {` rx="10"`, ""}} {
		if !strings.Contains(canvas, change[0]) {
			return fmt.Errorf("logo.svg does not hold %s", change[0])
		}
		canvas = strings.Replace(canvas, change[0], change[1], 1)
	}
	dir, err := os.MkdirTemp("", "owngit-app-icon-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	svg := filepath.Join(dir, "icon.svg")
	if err := os.WriteFile(svg, []byte(canvas), 0o600); err != nil {
		return err
	}
	set := filepath.Join(dir, "AppIcon.iconset")
	if err := os.Mkdir(set, 0o700); err != nil {
		return err
	}
	for _, points := range []int{16, 32, 128, 256, 512} {
		// Each size also comes at twice the pixels, named with "@2x".
		for scale, suffix := range []string{"", "@2x"} {
			name := fmt.Sprintf("icon_%dx%d%s.png", points, points, suffix)
			pixels := strconv.Itoa(points * (scale + 1))
			if _, err := run("sips", []string{"-s", "format", "png", "-z", pixels, pixels, svg, "--out", filepath.Join(set, name)}, nil); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(icns), 0o755); err != nil {
		return err
	}
	_, err = run("iconutil", []string{"-c", "icns", "-o", icns, set}, nil)
	return err
}

// stapleAndAssess finishes an accepted DMG: it staples the ticket for
// offline checks, then Gatekeeper has to accept the DMG and the app in it.
func stapleAndAssess(signer *appleSigner, diskImage, app string) error {
	if err := signer.staple(diskImage); err != nil {
		return err
	}
	if err := signer.assess(diskImage, "open"); err != nil {
		return err
	}
	return signer.assess(app, "execute")
}

// volumeFiles lists every file under the disk image folder, with the path
// relative to it. Only regular files and directories may appear there.
func volumeFiles(volume string) ([]fileEntry, error) {
	var entries []fileEntry
	err := filepath.WalkDir(volume, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(volume, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !info.Mode().IsRegular() {
			return fmt.Errorf("the disk image folder holds %s, which is not a regular file", relative)
		}
		if err := validateNativePath(relative); err != nil {
			return err
		}
		digest, err := sha256File(path)
		if err != nil {
			return err
		}
		entries = append(entries, fileEntry{
			Path: relative, Mode: fmt.Sprintf("%04o", info.Mode().Perm()), Size: info.Size(), SHA256: digest,
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

// hdiutil create sometimes fails with "Resource busy" on hosted CI runners,
// and a later attempt succeeds, so that failure is retried a few times after
// a pause. -ov lets a retry replace an image a failed attempt left in the
// fresh output folder.
const (
	diskImageCreateAttempts = 5
	diskImageBusyPause      = 3 * time.Second
)

// diskImageCreateArguments has no -quiet, which would also hide why hdiutil
// failed; its output is shown only on failure.
func diskImageCreateArguments(volumeName, source, image string) []string {
	return []string{"create", "-ov", "-fs", "HFS+", "-format", "UDZO", "-volname", volumeName, "-srcfolder", source, image}
}

// createDiskImage runs hdiutil create and retries it only when hdiutil
// reports that a resource is busy.
func createDiskImage(run commandRunner, pause func(time.Duration), hdiutil string, arguments []string) error {
	for attempt := 1; ; attempt++ {
		_, err := run(hdiutil, arguments, nil)
		var failure *nativeCommandError
		busy := errors.As(err, &failure) && strings.Contains(failure.message, "Resource busy")
		switch {
		case err == nil:
			return nil
		case !busy:
			return err
		case attempt == diskImageCreateAttempts:
			return fmt.Errorf("%w (after %d attempts)", err, attempt)
		}
		pause(diskImageBusyPause)
	}
}

// nativeCommandError reports a failed command with its error output, or
// with the exit status when it wrote none.
type nativeCommandError struct {
	command string
	message string
}

func (e *nativeCommandError) Error() string { return e.command + ": " + e.message }

func runNativeCommand(name string, arguments []string, extraEnvironment []string) (string, error) {
	command := externalCommand(name, arguments...)
	if len(extraEnvironment) > 0 {
		command.Env = append(os.Environ(), extraEnvironment...)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), &nativeCommandError{command: name + " " + strings.Join(arguments, " "), message: failureMessage(err, stderr.String())}
	}
	return stdout.String(), nil
}

func rejectEmbeddedHostPaths(binaryPath, sourceRoot string) error {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return err
	}
	candidates := []string{sourceRoot}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}
	for _, candidate := range candidates {
		if bytes.Contains(data, []byte(candidate)) {
			return fmt.Errorf("launcher embeds local path %s", candidate)
		}
	}
	return nil
}

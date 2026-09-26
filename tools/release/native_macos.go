package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

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
	shared, err := explicitSharedFiles(payload)
	if err != nil {
		return nativeArtifact{}, err
	}
	provenance, err := provenanceBytes(inputs, payload, "macos-dmg")
	if err != nil {
		return nativeArtifact{}, err
	}
	infoPlist, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "macos", "Info.plist.tmpl"), struct{ Version string }{inputs.manifest.Version})
	if err != nil {
		return nativeArtifact{}, err
	}
	readme, err := renderNativeTemplate(filepath.Join(inputs.root, "packaging", "macos", "README.txt.tmpl"), struct{ Version string }{inputs.manifest.Version})
	if err != nil {
		return nativeArtifact{}, err
	}

	stage, err := os.MkdirTemp("", "owngit-macos-prototype-")
	if err != nil {
		return nativeArtifact{}, err
	}
	defer os.RemoveAll(stage)
	volume := filepath.Join(stage, "volume")
	contents := filepath.Join(volume, "OwnGit.app", "Contents")
	launcherPath := filepath.Join(contents, "MacOS", "OwnGitLauncher")
	if err := os.MkdirAll(filepath.Dir(launcherPath), 0o755); err != nil {
		return nativeArtifact{}, err
	}
	launcherSource := filepath.Join(inputs.root, "packaging", "macos", "Launcher.swift")
	lifecycleSource := filepath.Join(inputs.root, "packaging", "macos", "Lifecycle.swift")
	if _, err := runNativeCommand(inputs.xcrun, []string{
		"swiftc", "-O", "-gnone", "-framework", "AppKit", "-o", launcherPath,
		launcherSource, lifecycleSource,
	}, nil); err != nil {
		return nativeArtifact{}, err
	}
	if err := os.Chmod(launcherPath, 0o755); err != nil {
		return nativeArtifact{}, err
	}
	if err := rejectEmbeddedHostPaths(launcherPath, inputs.root); err != nil {
		return nativeArtifact{}, err
	}

	files := []nativePackageFile{
		{path: "OwnGit.app/Contents/Info.plist", mode: 0o644, data: infoPlist},
		{path: "OwnGit.app/Contents/PkgInfo", mode: 0o644, data: []byte("APPL????")},
		{path: "OwnGit.app/Contents/Resources/bin/owngit", mode: 0o755, data: payload.binary.data},
		{path: "OwnGit.app/Contents/Resources/package-provenance.json", mode: 0o644, data: provenance},
		{path: "README.txt", mode: 0o644, data: readme},
	}
	launcherData, err := os.ReadFile(launcherPath)
	if err != nil {
		return nativeArtifact{}, err
	}
	files = append(files, nativePackageFile{path: "OwnGit.app/Contents/MacOS/OwnGitLauncher", mode: 0o755, data: launcherData})
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
	if _, err := runNativeCommand("plutil", []string{"-lint", filepath.Join(contents, "Info.plist")}, nil); err != nil {
		return nativeArtifact{}, err
	}
	packagedBinaryDigest, err := sha256File(filepath.Join(contents, "Resources", "bin", "owngit"))
	if err != nil {
		return nativeArtifact{}, err
	}
	if packagedBinaryDigest != payload.binary.sha {
		return nativeArtifact{}, errors.New("the app binary does not match the verified portable binary")
	}

	name := fmt.Sprintf("owngit_%s_darwin_arm64_prototype.dmg", inputs.manifest.Version)
	dmgPath := filepath.Join(outDir, name)
	volumeName := "OwnGit Prototype " + inputs.manifest.Version
	if err := createDiskImage(runNativeCommand, time.Sleep, inputs.hdiutil, diskImageCreateArguments(volumeName, volume, dmgPath)); err != nil {
		return nativeArtifact{}, err
	}
	if _, err := runNativeCommand(inputs.hdiutil, []string{"verify", dmgPath}, nil); err != nil {
		return nativeArtifact{}, err
	}
	digest, err := sha256File(dmgPath)
	if err != nil {
		return nativeArtifact{}, err
	}
	info, err := os.Stat(dmgPath)
	if err != nil {
		return nativeArtifact{}, err
	}
	return nativeArtifact{
		Format: "macos-dmg", Target: "darwin/arm64", Name: name,
		SHA256: digest, Size: info.Size(), Prototype: true, PublisherSigned: false, Notarized: false,
		StructureVerified: true, NativeInstallVerified: false, PublicReady: false,
		Toolchain:        inputs.macToolchain,
		PortableArtifact: payload.built.Name, PortableArtifactSHA256: payload.built.SHA256,
		ApplicationBinarySHA256:  payload.binary.sha,
		PackagedProvenanceSHA256: sha256Bytes(provenance), Files: fileEntries(files),
	}, nil
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
func createDiskImage(run func(string, []string, []string) (string, error), pause func(time.Duration), hdiutil string, arguments []string) error {
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
	command := exec.Command(name, arguments...)
	if len(extraEnvironment) > 0 {
		command.Env = append(os.Environ(), extraEnvironment...)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), &nativeCommandError{command: name + " " + strings.Join(arguments, " "), message: message}
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

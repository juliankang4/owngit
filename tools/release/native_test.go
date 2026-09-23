package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const testNativeBaseline = "accepted-core-sha256=abc123;release-sha256=def456"

func TestSelectNativeFormats(t *testing.T) {
	selected, err := selectNativeFormats("macos,deb")
	noErr(t, err)
	if !selected["macos"] || !selected["deb"] || len(selected) != 2 {
		t.Fatalf("selected formats = %#v", selected)
	}
	for _, value := range []string{"", "npm", "macos,unknown"} {
		if _, err := selectNativeFormats(value); err == nil {
			t.Errorf("selectNativeFormats(%q) succeeded", value)
		}
	}
}

func TestNativeDebPrototypes(t *testing.T) {
	root := repoRoot(t)
	portable := sharedDist(t)
	first := filepath.Join(t.TempDir(), "native-first")
	if err := nativeCommand([]string{
		"-source", root, "-manifest", filepath.Join(portable, "manifest.json"),
		"-out", first, "-formats", "deb", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatalf("first native build: %v", err)
	}
	document := readNativeManifest(t, first)
	if document.Status != nativePrototypeStatus || document.Baseline != testNativeBaseline {
		t.Fatalf("unexpected native manifest status: %#v", document)
	}
	if len(document.Artifacts) != 2 {
		t.Fatalf("native manifest has %d artifacts, want 2", len(document.Artifacts))
	}

	seenTargets := map[string]bool{}
	for _, built := range document.Artifacts {
		seenTargets[built.Target] = true
		if built.Format != "deb" || !built.Prototype || built.PublisherSigned || built.Notarized || built.NativeInstallVerified || built.PublicReady {
			t.Fatalf("invalid prototype claims for %s: %#v", built.Name, built)
		}
		packageData, err := os.ReadFile(filepath.Join(first, built.Name))
		noErr(t, err)
		if sha256Bytes(packageData) != built.SHA256 {
			t.Fatalf("%s digest does not match its manifest", built.Name)
		}
		members, err := readAr(packageData)
		noErr(t, err)
		if got := arMemberNames(members); strings.Join(got, ",") != "debian-binary,control.tar.gz,data.tar.gz" {
			t.Fatalf("%s members = %v", built.Name, got)
		}
		controlFiles := readDebTarFiles(t, members[1].data)
		control := string(controlFiles["control"].data)
		for _, required := range []string{
			"Version: " + document.Version,
			"Depends: git\n",
			"X-OwnGit-Prototype: yes\n",
			"does not install a service or login-start entry",
		} {
			if !strings.Contains(control, required) {
				t.Errorf("%s control does not contain %q", built.Name, required)
			}
		}
		dataFiles := readDebTarFiles(t, members[2].data)
		binary, ok := dataFiles["usr/bin/owngit"]
		if !ok {
			t.Fatalf("%s has no application binary", built.Name)
		}
		if binary.mode != 0o755 || sha256Bytes(binary.data) != built.ApplicationBinarySHA256 {
			t.Fatalf("%s application binary does not match portable provenance", built.Name)
		}
		for _, required := range []string{
			"usr/share/applications/owngit.desktop",
			"usr/share/doc/owngit/LICENSE",
			"usr/share/doc/owngit/copyright",
			"usr/share/doc/owngit/README.Debian",
			"usr/share/doc/owngit/THIRD_PARTY_NOTICES/manifest.json",
			"usr/share/doc/owngit/package-provenance.json",
		} {
			if _, ok := dataFiles[required]; !ok {
				t.Errorf("%s is missing %s", built.Name, required)
			}
		}
		assertDesktopLaunchCommand(t, string(dataFiles["usr/share/applications/owngit.desktop"].data))
		for name := range dataFiles {
			for _, forbidden := range []string{"systemd", "autostart", ".config", "owngit.sqlite", ".local"} {
				if strings.Contains(strings.ToLower(name), forbidden) {
					t.Errorf("%s contains forbidden path %s", built.Name, name)
				}
			}
		}
		var provenance packageProvenance
		noErr(t, json.Unmarshal(dataFiles["usr/share/doc/owngit/package-provenance.json"].data, &provenance))
		if provenance.Baseline != testNativeBaseline || provenance.ApplicationBinarySHA256 != built.ApplicationBinarySHA256 {
			t.Fatalf("%s provenance does not bind the baseline and binary", built.Name)
		}
		if provenance.PublisherSigned || provenance.NativeInstallationVerified || provenance.PublicDistributionReady {
			t.Fatalf("%s provenance overclaims readiness", built.Name)
		}
	}
	if !seenTargets["linux/amd64"] || !seenTargets["linux/arm64"] {
		t.Fatalf("native targets = %#v", seenTargets)
	}

	second := filepath.Join(t.TempDir(), "native-second")
	if err := nativeCommand([]string{
		"-source", root, "-manifest", filepath.Join(portable, "manifest.json"),
		"-out", second, "-formats", "deb", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatalf("second native build: %v", err)
	}
	secondDocument := readNativeManifest(t, second)
	for index := range document.Artifacts {
		if document.Artifacts[index].SHA256 != secondDocument.Artifacts[index].SHA256 {
			t.Errorf("%s is not reproducible", document.Artifacts[index].Name)
		}
	}
	firstManifest, err := os.ReadFile(filepath.Join(first, "native-manifest.json"))
	noErr(t, err)
	secondManifest, err := os.ReadFile(filepath.Join(second, "native-manifest.json"))
	noErr(t, err)
	if !bytes.Equal(firstManifest, secondManifest) {
		t.Fatal("native manifest is not reproducible")
	}
}

func TestNativeRefusesInvalidInputBeforeOutput(t *testing.T) {
	root := repoRoot(t)
	portable := sharedDist(t)

	t.Run("missing baseline", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "native")
		err := nativeCommand([]string{
			"-source", root, "-manifest", filepath.Join(portable, "manifest.json"),
			"-out", out, "-formats", "deb",
		})
		if err == nil || !strings.Contains(err.Error(), "-baseline") {
			t.Fatalf("nativeCommand returned %v", err)
		}
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatalf("invalid request created output: %v", err)
		}
	})

	t.Run("existing destination", func(t *testing.T) {
		out := t.TempDir()
		sentinel := filepath.Join(out, "keep.txt")
		noErr(t, os.WriteFile(sentinel, []byte("keep\n"), 0o644))
		before := snapshotTree(t, out)
		err := nativeCommand([]string{
			"-source", root, "-manifest", filepath.Join(portable, "manifest.json"),
			"-out", out, "-formats", "deb", "-baseline", testNativeBaseline,
		})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("nativeCommand returned %v", err)
		}
		after := snapshotTree(t, out)
		if strings.Join(sortedMapPairs(before), "\n") != strings.Join(sortedMapPairs(after), "\n") {
			t.Fatal("refused native destination changed")
		}
	})

	t.Run("corrupt portable archive", func(t *testing.T) {
		corrupt := copyDist(t, portable)
		document, err := readManifest(filepath.Join(corrupt, "manifest.json"))
		noErr(t, err)
		path := filepath.Join(corrupt, document.Artifacts[0].Name)
		data, err := os.ReadFile(path)
		noErr(t, err)
		data[len(data)/2] ^= 0xff
		noErr(t, os.WriteFile(path, data, 0o644))
		out := filepath.Join(t.TempDir(), "native")
		err = nativeCommand([]string{
			"-source", root, "-manifest", filepath.Join(corrupt, "manifest.json"),
			"-out", out, "-formats", "deb", "-baseline", testNativeBaseline,
		})
		if err == nil || !strings.Contains(err.Error(), "portable baseline") {
			t.Fatalf("nativeCommand returned %v", err)
		}
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatalf("corrupt baseline created output: %v", err)
		}
	})
}

func TestNativePinsPortableInputsBeforeVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scheduling wrapper is a POSIX shell script")
	}
	root := repoRoot(t)
	portable := copyDist(t, sharedDist(t))
	document, err := readManifest(filepath.Join(portable, "manifest.json"))
	noErr(t, err)
	manifestDigest, err := sha256File(filepath.Join(portable, "manifest.json"))
	noErr(t, err)
	var archivePath string
	for _, built := range document.Artifacts {
		if built.Target == "linux/amd64" {
			archivePath = filepath.Join(portable, built.Name)
			break
		}
	}
	if archivePath == "" {
		t.Fatal("portable manifest has no linux/amd64 fixture")
	}
	marker := []byte("Synthetic replacement license after portable verification.\n")
	replacementPath := filepath.Join(t.TempDir(), "replacement.tar.gz")
	writeReplacementTar(t, archivePath, replacementPath, "LICENSE", marker)

	realGo, err := exec.LookPath("go")
	noErr(t, err)
	wrapperDir := t.TempDir()
	countPath := filepath.Join(wrapperDir, "count")
	noErr(t, os.WriteFile(countPath, []byte("0\n"), 0o600))
	wrapperPath := filepath.Join(wrapperDir, "go-wrapper")
	wrapper := `#!/bin/sh
count=$(cat "$OWNGIT_TEST_COUNT")
count=$((count + 1))
printf '%s\n' "$count" > "$OWNGIT_TEST_COUNT"
"$OWNGIT_TEST_REAL_GO" "$@"
status=$?
if [ "$count" -eq "$OWNGIT_TEST_TRIGGER" ]; then
  cp "$OWNGIT_TEST_REPLACEMENT" "$OWNGIT_TEST_ARCHIVE"
fi
exit "$status"
`
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	t.Setenv("OWNGIT_TEST_COUNT", countPath)
	t.Setenv("OWNGIT_TEST_REAL_GO", realGo)
	t.Setenv("OWNGIT_TEST_TRIGGER", strconv.Itoa(len(document.Artifacts)))
	t.Setenv("OWNGIT_TEST_REPLACEMENT", replacementPath)
	t.Setenv("OWNGIT_TEST_ARCHIVE", archivePath)

	inputs, err := loadNativeInputs(
		root, filepath.Join(portable, "manifest.json"), testNativeBaseline,
		wrapperPath, "", "", map[string]bool{"deb": true},
	)
	noErr(t, err)
	if inputs.manifestSHA256 != manifestDigest {
		t.Fatalf("manifest digest = %s, want %s", inputs.manifestSHA256, manifestDigest)
	}
	sourceEntries, err := readArchive(archivePath, "tar.gz")
	noErr(t, err)
	if !bytes.Equal(archiveEntryData(sourceEntries, "LICENSE"), marker) {
		t.Fatal("replacement boundary was not exercised")
	}
	loaded := inputs.payloads["linux/amd64"].entries["LICENSE"].data
	if bytes.Equal(loaded, marker) {
		t.Fatal("native inputs accepted replacement bytes after portable verification")
	}
}

func TestLoadPortablePayloadRevalidatesSnapshotEntries(t *testing.T) {
	portable := copyDist(t, sharedDist(t))
	snapshot, err := snapshotPortableInputs(filepath.Join(portable, "manifest.json"))
	noErr(t, err)
	t.Cleanup(func() {
		if err := os.RemoveAll(snapshot.dir); err != nil {
			t.Errorf("remove snapshot %s: %v", snapshot.dir, err)
		}
	})

	realGo, err := exec.LookPath("go")
	noErr(t, err)
	noErrf(t, verifyDir(snapshot.dir, realGo), "verify snapshot before replacement")

	var archivePath string
	for _, built := range snapshot.manifest.Artifacts {
		if built.Target == "linux/amd64" {
			archivePath = filepath.Join(snapshot.dir, built.Name)
			break
		}
	}
	if archivePath == "" {
		t.Fatal("portable manifest has no linux/amd64 fixture")
	}
	entries, err := readArchive(archivePath, "tar.gz")
	noErr(t, err)
	license := archiveEntryData(entries, "LICENSE")
	if len(license) == 0 {
		t.Fatal("portable fixture has no LICENSE bytes")
	}
	mutated := bytes.Clone(license)
	mutated[len(mutated)/2] ^= 0xff
	replacementPath := filepath.Join(t.TempDir(), "replacement.tar.gz")
	writeReplacementTar(t, archivePath, replacementPath, "LICENSE", mutated)
	replacement, err := os.ReadFile(replacementPath)
	noErr(t, err)
	noErr(t, os.WriteFile(archivePath, replacement, 0o600))

	_, err = loadPortablePayload(snapshot.dir, snapshot.manifest, "linux/amd64")
	if err == nil || !strings.Contains(err.Error(), "changed after verification") || !strings.Contains(err.Error(), "LICENSE sha256") {
		t.Fatalf("loadPortablePayload returned %v", err)
	}
}

func TestPortableInputSnapshotCleanupErrorsAreReturned(t *testing.T) {
	root := repoRoot(t)
	portable := sharedDist(t)

	t.Run("after successful load", func(t *testing.T) {
		failure := errors.New("synthetic snapshot cleanup failure after success")
		var cleanedPath string
		cleanupCalls := 0
		inputs, err := loadNativeInputsWithCleanup(
			root, filepath.Join(portable, "manifest.json"), testNativeBaseline,
			"go", "", "", map[string]bool{"deb": true},
			failingSnapshotCleanup(&cleanedPath, &cleanupCalls, failure),
		)
		if err == nil {
			t.Fatal("loadNativeInputs discarded the snapshot cleanup failure")
		}
		if len(inputs.payloads) != 0 {
			t.Fatal("loadNativeInputs returned usable inputs after snapshot cleanup failed")
		}
		assertSnapshotCleanupError(t, err, failure, cleanedPath, cleanupCalls)
	})

	t.Run("joined with verification failure", func(t *testing.T) {
		failure := errors.New("synthetic snapshot cleanup failure after verification error")
		var cleanedPath string
		cleanupCalls := 0
		missingGo := filepath.Join(t.TempDir(), "missing-go")
		_, err := loadNativeInputsWithCleanup(
			root, filepath.Join(portable, "manifest.json"), testNativeBaseline,
			missingGo, "", "", map[string]bool{"deb": true},
			failingSnapshotCleanup(&cleanedPath, &cleanupCalls, failure),
		)
		if err == nil || !strings.Contains(err.Error(), "portable baseline") {
			t.Fatalf("loadNativeInputs returned %v", err)
		}
		assertSnapshotCleanupError(t, err, failure, cleanedPath, cleanupCalls)
	})

	t.Run("joined with snapshot construction failure", func(t *testing.T) {
		incomplete := copyDist(t, portable)
		document, err := readManifest(filepath.Join(incomplete, "manifest.json"))
		noErr(t, err)
		noErr(t, os.Remove(filepath.Join(incomplete, document.Artifacts[0].Name)))
		failure := errors.New("synthetic snapshot cleanup failure during construction")
		var cleanedPath string
		cleanupCalls := 0
		snapshot, err := snapshotPortableInputsWithCleanup(
			filepath.Join(incomplete, "manifest.json"),
			failingSnapshotCleanup(&cleanedPath, &cleanupCalls, failure),
		)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("snapshotPortableInputs returned %#v, %v", snapshot, err)
		}
		if snapshot.dir != "" {
			t.Fatalf("snapshotPortableInputs returned failed snapshot path %s", snapshot.dir)
		}
		assertSnapshotCleanupError(t, err, failure, cleanedPath, cleanupCalls)
	})
}

func failingSnapshotCleanup(cleanedPath *string, calls *int, failure error) func(string) error {
	return func(path string) error {
		*cleanedPath = path
		*calls = *calls + 1
		return errors.Join(failure, os.RemoveAll(path))
	}
}

func assertSnapshotCleanupError(t *testing.T, err, failure error, cleanedPath string, cleanupCalls int) {
	t.Helper()
	if !errors.Is(err, failure) {
		t.Fatalf("error %v does not retain cleanup failure %v", err, failure)
	}
	if cleanupCalls != 1 {
		t.Fatalf("snapshot cleanup called %d times, want 1", cleanupCalls)
	}
	if cleanedPath == "" || !strings.Contains(err.Error(), cleanedPath) {
		t.Fatalf("cleanup error %v does not identify owned snapshot path %q", err, cleanedPath)
	}
	if _, statErr := os.Lstat(cleanedPath); !os.IsNotExist(statErr) {
		t.Fatalf("synthetic cleanup left snapshot %s: %v", cleanedPath, statErr)
	}
}

func TestPortableInputSnapshotIsPrivate(t *testing.T) {
	portable := copyDist(t, sharedDist(t))
	manifestPath := filepath.Join(portable, "manifest.json")
	snapshot, err := snapshotPortableInputs(manifestPath)
	noErr(t, err)
	defer os.RemoveAll(snapshot.dir)

	dirInfo, err := os.Stat(snapshot.dir)
	noErr(t, err)
	if runtime.GOOS != "windows" {
		if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("snapshot directory mode = %04o, want no group or other access", got)
		}
	}
	entries, err := os.ReadDir(snapshot.dir)
	noErr(t, err)
	if len(entries) != len(snapshot.manifest.Artifacts)+2 {
		t.Fatalf("snapshot has %d files, want %d", len(entries), len(snapshot.manifest.Artifacts)+2)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		noErr(t, err)
		if !info.Mode().IsRegular() {
			t.Errorf("snapshot entry %s has mode %s", entry.Name(), info.Mode())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Errorf("snapshot entry %s is accessible by group or other: %s", entry.Name(), info.Mode())
		}
	}
	manifestDigest, err := sha256File(manifestPath)
	noErr(t, err)
	if snapshot.manifestSHA256 != manifestDigest {
		t.Fatalf("snapshot manifest digest = %s, want %s", snapshot.manifestSHA256, manifestDigest)
	}
}

func TestDebVerifierRejectsMemberOrder(t *testing.T) {
	files := []nativePackageFile{{path: "control", mode: 0o644, data: []byte("Package: owngit\n")}}
	archive, err := writeDebTarGz(files)
	noErr(t, err)
	members := []arMember{
		{name: "control.tar.gz", data: archive},
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "data.tar.gz", data: archive},
	}
	var encoded bytes.Buffer
	noErr(t, writeAr(&encoded, members))
	path := filepath.Join(t.TempDir(), "bad.deb")
	noErr(t, os.WriteFile(path, encoded.Bytes(), 0o644))
	if err := verifyDebPackage(path, files, files); err == nil || !strings.Contains(err.Error(), "member 0") {
		t.Fatalf("verifyDebPackage returned %v", err)
	}
}

func TestNativeLauncherCommandsOpenOwnerDashboard(t *testing.T) {
	root := repoRoot(t)
	launcher := readText(t, filepath.Join(root, "packaging", "macos", "Launcher.swift"))
	if got := strings.Count(launcher, "process.arguments ="); got != 1 {
		t.Fatalf("macOS launcher assigns process arguments %d times, want 1", got)
	}
	if !strings.Contains(launcher, `process.arguments = ["serve", "--open"]`) {
		t.Fatal("macOS launcher does not use the exact serve --open arguments")
	}
	if strings.Contains(launcher, `process.arguments = ["serve"]`) {
		t.Fatal("macOS launcher still starts serve without --open")
	}
	assertDesktopLaunchCommand(t, readText(t, filepath.Join(root, "packaging", "linux", "owngit.desktop")))
}

func assertDesktopLaunchCommand(t *testing.T, body string) {
	t.Helper()
	var commands []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Exec=") {
			commands = append(commands, line)
		}
	}
	if len(commands) != 1 || commands[0] != "Exec=/usr/bin/owngit serve --open" {
		t.Fatalf("desktop entry Exec lines = %q, want exact serve --open command", commands)
	}
}

func TestMacLauncherTypechecks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Swift AppKit launcher is a macOS input")
	}
	macosDir := filepath.Join(repoRoot(t), "packaging", "macos")
	launcher := filepath.Join(macosDir, "Launcher.swift")
	lifecycle := filepath.Join(macosDir, "Lifecycle.swift")
	command := exec.Command("xcrun", "swiftc", "-typecheck", launcher, lifecycle)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("swiftc typecheck: %v\n%s", err, output)
	}
	body := readText(t, launcher)
	for _, required := range []string{"capturedOutputLimit = 4 * 1024", "process.arguments = [\"serve\", \"--open\"]", "server.terminate()", "terminationReason", "finishQuitAfterServerExit", "Quit OwnGit", "NSApp.activate", "NSAlert"} {
		if !strings.Contains(body, required) {
			t.Errorf("launcher does not contain %q", required)
		}
	}
	for _, forbidden := range []string{"LaunchAgent", "SMAppService", "NSStatusItem", "Settings", "SIGKILL", "kill("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("launcher contains forbidden lifecycle expansion %q", forbidden)
		}
	}
}

func TestMacLifecyclePolicyExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Swift Process termination policy is a macOS launcher input")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "main.swift")
	program := `import Foundation

func require(_ condition: Bool, _ message: String) {
    if !condition { fatalError(message) }
}

require(launcherExitDisposition(shutdownRequested: true, status: 0, reason: .exit) == .finishQuit, "clean requested shutdown must finish Quit")
require(launcherExitDisposition(shutdownRequested: true, status: 1, reason: .exit) == .reportFailure, "nonzero requested shutdown must report")
require(launcherExitDisposition(shutdownRequested: true, status: 15, reason: .uncaughtSignal) == .reportFailure, "signal termination during Quit must report")
require(launcherExitDisposition(shutdownRequested: false, status: 0, reason: .exit) == .reportFailure, "unexpected clean exit must report")
require(launcherExitSummary(status: 7, reason: .exit).contains("status 7"), "exit summary must include status")
require(launcherExitSummary(status: 15, reason: .uncaughtSignal).contains("signal 15"), "signal summary must include signal")
let failure = launcherExitMessage(status: 1, reason: .exit, diagnostics: "cleanup failed")
require(failure.contains("status 1") && failure.contains("cleanup failed"), "diagnostics must retain status and detail")
print("lifecycle fixture passed")
`
	noErr(t, os.WriteFile(fixture, []byte(program), 0o600))
	binary := filepath.Join(dir, "lifecycle-fixture")
	lifecycle := filepath.Join(repoRoot(t), "packaging", "macos", "Lifecycle.swift")
	command := exec.Command("xcrun", "swiftc", lifecycle, fixture, "-o", binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile lifecycle fixture: %v\n%s", err, output)
	}
	output, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("run lifecycle fixture: %v\n%s", err, output)
	}
	if string(output) != "lifecycle fixture passed\n" {
		t.Fatalf("lifecycle fixture output = %q", output)
	}
}

func TestNativeMacPrototypeBuildsUnsignedArtifact(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("macOS prototype requires the native Apple Silicon toolchain")
	}
	out := filepath.Join(t.TempDir(), "native-mac")
	if err := nativeCommand([]string{
		"-source", repoRoot(t), "-manifest", filepath.Join(sharedDist(t), "manifest.json"),
		"-out", out, "-formats", "macos", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatal(err)
	}
	document := readNativeManifest(t, out)
	if len(document.Artifacts) != 1 {
		t.Fatalf("native manifest has %d artifacts", len(document.Artifacts))
	}
	built := document.Artifacts[0]
	if built.Format != "macos-dmg" || built.Target != "darwin/arm64" || !built.Prototype || built.PublisherSigned || built.Notarized || built.NativeInstallVerified || built.PublicReady {
		t.Fatalf("macOS prototype overclaims readiness: %#v", built)
	}
	path := filepath.Join(out, built.Name)
	command := exec.Command("hdiutil", "verify", "-quiet", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hdiutil verify: %v\n%s", err, output)
	}
	if digest, err := sha256File(path); err != nil || digest != built.SHA256 {
		t.Fatalf("DMG digest = %q, %v", digest, err)
	}
	paths := map[string]bool{}
	for _, file := range built.Files {
		paths[file.Path] = true
	}
	for _, required := range []string{
		"OwnGit.app/Contents/Info.plist",
		"OwnGit.app/Contents/MacOS/OwnGitLauncher",
		"OwnGit.app/Contents/Resources/bin/owngit",
		"OwnGit.app/Contents/Resources/LICENSE",
		"OwnGit.app/Contents/Resources/THIRD_PARTY_NOTICES/manifest.json",
		"OwnGit.app/Contents/Resources/package-provenance.json",
		"README.txt",
	} {
		if !paths[required] {
			t.Errorf("DMG manifest is missing %s", required)
		}
	}
}

func writeReplacementTar(t *testing.T, source, destination, replaceName string, replacement []byte) {
	t.Helper()
	entries, err := readArchive(source, "tar.gz")
	noErr(t, err)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	noErr(t, err)
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		data := entry.data
		if entry.name == replaceName {
			data = replacement
		}
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(data)),
			Typeflag: tar.TypeReg, ModTime: fixedModTime, Format: tar.FormatPAX,
		}
		noErr(t, archive.WriteHeader(header))
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	noErr(t, archive.Close())
	noErr(t, compressed.Close())
	noErr(t, file.Close())
}

func archiveEntryData(entries []archiveEntry, name string) []byte {
	for _, entry := range entries {
		if entry.name == name {
			return entry.data
		}
	}
	return nil
}

func readNativeManifest(t *testing.T, dir string) nativeManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "native-manifest.json"))
	noErr(t, err)
	var document nativeManifest
	noErr(t, json.Unmarshal(data, &document))
	return document
}

func arMemberNames(members []arMember) []string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.name)
	}
	return names
}

func readDebTarFiles(t *testing.T, data []byte) map[string]nativePackageFile {
	t.Helper()
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	noErr(t, err)
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := map[string]nativePackageFile{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		noErr(t, err)
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			t.Fatalf("%s is not a regular file", header.Name)
		}
		body, err := io.ReadAll(archive)
		noErr(t, err)
		name := strings.TrimPrefix(header.Name, "./")
		files[name] = nativePackageFile{path: name, mode: header.Mode, data: body}
	}
	return files
}

func sortedMapPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs
}

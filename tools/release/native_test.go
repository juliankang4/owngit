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
	"time"
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
		// A user who installed the package, rather than unpacking an archive,
		// still has to be able to find and copy the coding-tool skill.
		for _, name := range releaseResources {
			installed := "usr/share/doc/owngit/" + name
			file, ok := dataFiles[installed]
			if !ok {
				t.Fatalf("%s does not install %s", built.Name, installed)
			}
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			noErr(t, err)
			if string(file.data) != string(source) {
				t.Errorf("%s in %s does not match the source file", installed, built.Name)
			}
			if file.mode&0o111 != 0 {
				t.Errorf("%s is installed executable (mode %04o)", installed, file.mode)
			}
		}
		// The documented install path is what the README tells a user to look
		// at, so a silent relocation has to fail here.
		if !strings.Contains(string(dataFiles["usr/share/doc/owngit/README.Debian"].data), "/usr/share/doc/owngit/integrations/skills/owngit-checks/SKILL.md") {
			t.Errorf("%s does not tell the user where the skill is installed", built.Name)
		}
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
	// The macOS app is the menu bar icon; the OwnGit service runs the
	// server, so the app never starts one of its own.
	for _, source := range macLauncherSources {
		if body := readText(t, filepath.Join(root, "packaging", "macos", source)); strings.Contains(body, `"serve"`) {
			t.Fatalf("%s starts a server", source)
		}
	}
	// The launcher and the release tool must agree on where the app holds
	// the owngit binary.
	launcher := readText(t, filepath.Join(root, "packaging", "macos", "Launcher.swift"))
	if !strings.Contains(launcher, `private let helperPath = "`+appHelperPath+`"`) {
		t.Fatalf("macOS launcher does not start the binary at %s", appHelperPath)
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
	arguments := []string{"swiftc", "-typecheck"}
	for _, source := range macLauncherSources {
		arguments = append(arguments, filepath.Join(repoRoot(t), "packaging", "macos", source))
	}
	if output, err := exec.Command("xcrun", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("swiftc typecheck: %v\n%s", err, output)
	}
}

// The icon's decisions, compiled from TrayStatus.swift with a fixture that
// feeds it the answers of the server and of owngit doctor.
func TestMacTrayStatusPolicyExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the icon's status policy is a macOS launcher input")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "main.swift")
	program := `import Foundation

func require(_ condition: Bool, _ message: String) {
    if !condition { fatalError(message) }
}
func data(_ text: String) -> Data { Data(text.utf8) }

let running = """
{"ok":true,"state":"running","version":"1.1.3","shown":true,"dashboard_url":"http://127.0.0.1:7654","clone_address":"http://127.0.0.1:7654/git/","setup_required":false,"update":null,
 "findings":[{"code":"doctor.unchecked_firewall","message":"m","repair":"","unchecked":true}],
 "pushes":[{"repository_id":"notes","repository":"notes","ref":"refs/heads/main","branch":"main","refs_updated":1,"pushed_at":"2026-09-29T11:10:15.123456Z","actor":{"kind":"access"},"actor_label":"General access"}]}
"""
guard case .status(let status) = statusAnswer(httpStatus: 200, body: data(running)) else { fatalError("a running answer must decode") }
require(PanelState.status(status).name == .running && status.actionableFindings.isEmpty, "unchecked findings ask for nothing")
require(parsePushTime(status.pushes[0].pushed_at) != nil && parsePushTime("2026-09-29T11:10:15Z") != nil, "push times with and without fractions")
let attention = running.replacingOccurrences(of: "\"state\":\"running\"", with: "\"state\":\"attention\"")
    .replacingOccurrences(of: "\"update\":null", with: "\"update\":{\"version\":\"1.1.4\",\"notes_url\":\"\",\"command\":\"\",\"start\":\"\",\"restart\":false,\"guide_url\":\"https://example.invalid\"}")
guard case .status(let needs) = statusAnswer(httpStatus: 200, body: data(attention)) else { fatalError("an attention answer must decode") }
require(PanelState.status(needs).name == .attention && needs.update?.version == "1.1.4", "attention keeps the update")

require(statusAnswer(httpStatus: nil, body: nil) == .noConnection, "no connection")
require(statusAnswer(httpStatus: 401, body: data("{}")) == .unauthorized, "401")
for code in [403, 405, 421, 503, -1] {
    require(statusAnswer(httpStatus: code, body: data(running)) == .unavailable, "status \(code) is unavailable")
}
require(statusAnswer(httpStatus: 404, body: data("404 page not found")) == .noStatus, "a server without the status route")
require(statusAnswer(httpStatus: 200, body: data("service unavailable")) == .unavailable, "a body that is not the status")
require(statusAnswer(httpStatus: 200, body: data(running.replacingOccurrences(of: "\"running\"", with: "\"stopped\""))) == .unavailable, "an unknown state")

require(dashboardURL(access: "http://127.0.0.1:7654") != nil && dashboardURL(access: "http://[::1]:8123/") != nil, "the loopback address from the access file")
for other in ["https://127.0.0.1:7654", "http://example.invalid:7654", "http://127.0.0.1", "http://127.0.0.1:7654/elsewhere", "http://u@127.0.0.1:7654", "file:///tmp"] {
    require(dashboardURL(access: other) == nil, "not a local dashboard: \(other)")
}
require(webLink("https://github.com/juliankang4/owngit#install") != nil, "an https guide")
for other in ["http://example.invalid", "file:///Applications", "owngit://x", ""] {
    require(webLink(other) == nil, "not a web link: \(other)")
}

func doctor(_ findings: String, running: Bool = false) -> PanelState {
    doctorState(output: data("{\"version\":\"1.1.3\",\"running\":\(running),\"findings\":[\(findings)]}"))
}
require(doctor("{\"code\":\"doctor.not_running\",\"message\":\"m\",\"repair\":\"owngit service start\"}") == .stopped(start: ["service", "start"]), "stopped service")
require(doctor("{\"code\":\"doctor.not_running\",\"message\":\"m\",\"repair\":\"owngit service install\"}") == .stopped(start: ["service", "install"]), "no service")
require(doctor("{\"code\":\"doctor.not_running\",\"message\":\"m\",\"repair\":\"sh -c anything\"}") == .unavailable(why: .noAnswer), "a repair the icon does not know is never run")
require(doctor("{\"code\":\"doctor.silent\",\"message\":\"m\",\"repair\":\"owngit service restart\"}") == .unavailable(why: .silent(restart: ["service", "restart"])), "silent")
require(doctor("{\"code\":\"doctor.silent\",\"message\":\"m\"}") == .unavailable(why: .silent(restart: [])), "silent without a service")
require(doctor("{\"code\":\"doctor.address_taken\",\"message\":\"m\"}") == .unavailable(why: .addressTaken), "address taken")
require(doctor("{\"code\":\"doctor.unchecked_server\",\"message\":\"why\",\"unchecked\":true}") == .unavailable(why: .unchecked(detail: "why")), "unchecked")
require(doctor("", running: true) == .unavailable(why: .noStatus), "a running server that writes no access file is older than the icon")
require(doctor("") == .unavailable(why: .noAnswer), "not running and nothing named")
require(doctorState(output: nil) == .unavailable(why: .noAnswer), "doctor failed")
require(doctorState(output: data("{\"ok\":false}")) == .unavailable(why: .noAnswer), "doctor error JSON")

let helper = "/Applications/OwnGit.app/Contents/Helpers/owngit"
let ours = InstalledAgent(program: helper, stateDir: nil)
let otherApp = InstalledAgent(program: "/opt/other/OwnGit.app/Contents/Helpers/owngit", stateDir: nil)
let brew = InstalledAgent(program: "/opt/homebrew/opt/owngit/bin/owngit", stateDir: nil)
let earlierLayout = InstalledAgent(program: "/opt/other/OwnGit.app/Contents/Resources/bin/owngit", stateDir: nil)
let start = PanelState.stopped(start: ["service", "start"])
require(launchRepair(state: start, agent: nil, helper: helper, version: "1.1.3") == ["service", "start"], "start what doctor names")
require(launchRepair(state: start, agent: brew, helper: helper, version: "1.1.3") == ["service", "start"], "start a Homebrew binary's service as it is")
require(launchRepair(state: start, agent: otherApp, helper: helper, version: "1.1.3") == ["service", "install"], "move the service to the app that was opened")
require(launchRepair(state: .status(status), agent: ours, helper: helper, version: "1.1.3") == nil, "a current service is left alone")
require(launchRepair(state: .status(status), agent: ours, helper: helper, version: "1.1.4") == ["service", "install"], "a replaced app restarts its service")
require(launchRepair(state: .status(status), agent: otherApp, helper: helper, version: "1.1.3") == ["service", "install"], "another app's service")
require(launchRepair(state: .status(status), agent: brew, helper: helper, version: "1.1.4") == nil, "another program's service is the owner's choice")
require(launchRepair(state: .unavailable(why: .noAnswer), agent: otherApp, helper: helper, version: "1.1.4") == nil, "nothing when the state is unknown")
require(launchRepair(state: start, agent: earlierLayout, helper: helper, version: "1.1.3") == ["service", "install"], "a service of an earlier app layout moves to this app")
require(launchRepair(state: .unavailable(why: .noStatus), agent: ours, helper: helper, version: "1.1.3") == ["service", "install"], "an earlier app's server at this path is restarted with this program")
require(launchRepair(state: .unavailable(why: .noStatus), agent: earlierLayout, helper: helper, version: "1.1.3") == ["service", "install"], "an earlier app's server moves to this app")
require(launchRepair(state: .unavailable(why: .noStatus), agent: brew, helper: helper, version: "1.1.3") == nil, "an older Homebrew server is the owner's to update")

let plist = """
<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>ProgramArguments</key><array>
<string>/Applications/OwnGit.app/Contents/Helpers/owngit</string><string>serve</string><string>--state-dir</string><string>/tmp/s &amp; t</string></array></dict></plist>
"""
require(readInstalledAgent(data(plist)) == InstalledAgent(program: helper, stateDir: "/tmp/s & t"), "agent program and state directory")
require(readInstalledAgent(data("nope")) == nil, "not an agent")
require(Words.forLanguages(["ko-KR", "en"]).lang == "ko" && Words.forLanguages(["en-US", "ko"]).lang == "en" && Words.forLanguages([]).lang == "en", "language")
print("tray status fixture passed")
`
	noErr(t, os.WriteFile(fixture, []byte(program), 0o600))
	binary := filepath.Join(dir, "tray-status-fixture")
	source := filepath.Join(repoRoot(t), "packaging", "macos", "TrayStatus.swift")
	if output, err := exec.Command("xcrun", "swiftc", source, fixture, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile tray status fixture: %v\n%s", err, output)
	}
	output, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("run tray status fixture: %v\n%s", err, output)
	}
	if string(output) != "tray status fixture passed\n" {
		t.Fatalf("tray status fixture output = %q", output)
	}
}

// hdiutil create is retried only while it reports a busy resource, with a
// pause before each retry, and the final error keeps hdiutil's message.
func TestDiskImageCreateRetriesOnlyABusyResource(t *testing.T) {
	busy := &nativeCommandError{command: "hdiutil create", message: "hdiutil: create failed - Resource busy"}
	other := &nativeCommandError{command: "hdiutil create", message: "hdiutil: create failed - No space left on device"}
	for _, test := range []struct {
		name     string
		failures []error
		calls    int
		want     string
	}{
		{name: "busy then created", failures: []error{busy, busy}, calls: 3},
		{name: "other failure", failures: []error{other, busy}, calls: 1, want: "No space left on device"},
		{name: "busy on every attempt", failures: []error{busy, busy, busy, busy, busy, busy}, calls: diskImageCreateAttempts,
			want: "Resource busy (after 5 attempts)"},
		{name: "busy then another failure", failures: []error{busy, other}, calls: 2, want: "No space left on device"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, pauses := 0, 0
			run := func(name string, arguments []string, _ []string) (string, error) {
				if name != "hdiutil" || arguments[0] != "create" {
					t.Fatalf("ran %s %v", name, arguments)
				}
				calls++
				if calls <= len(test.failures) {
					return "", test.failures[calls-1]
				}
				return "", nil
			}
			pause := func(duration time.Duration) {
				if duration != diskImageBusyPause {
					t.Fatalf("paused %v", duration)
				}
				pauses++
			}
			err := createDiskImage(run, pause, "hdiutil", []string{"create"})
			if calls != test.calls || pauses != test.calls-1 {
				t.Fatalf("%d calls and %d pauses, want %d calls", calls, pauses, test.calls)
			}
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
}

// A failed hdiutil create reports hdiutil's own message.
func TestDiskImageCreateFailureShowsTheReason(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("hdiutil is a macOS tool")
	}
	directory := t.TempDir()
	arguments := diskImageCreateArguments("Missing", filepath.Join(directory, "missing"), filepath.Join(directory, "missing.dmg"))
	err := createDiskImage(runNativeCommand, func(time.Duration) { t.Fatal("retried a failure that was not busy") }, "hdiutil", arguments)
	if err == nil || !strings.Contains(err.Error(), "create failed") {
		t.Fatalf("error %v, want hdiutil's message", err)
	}
}

func TestNativeMacPrototypeBuildsUnsignedArtifact(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("macOS prototype requires the native Apple Silicon toolchain")
	}
	root := repoRoot(t)
	out := filepath.Join(t.TempDir(), "native-mac")
	if err := nativeCommand([]string{
		"-source", root, "-manifest", filepath.Join(sharedDist(t), "manifest.json"),
		"-out", out, "-formats", "macos", "-baseline", testNativeBaseline,
	}); err != nil {
		t.Fatal(err)
	}
	document := readNativeManifest(t, out)
	if len(document.Artifacts) != 1 {
		t.Fatalf("native manifest has %d artifacts", len(document.Artifacts))
	}
	built := document.Artifacts[0]
	if built.Format != "macos-dmg" || built.Target != "darwin/arm64" || !built.Prototype || built.PublisherSigned || built.Notarized || built.NativeInstallVerified || built.PublicReady || built.AppleSignature != nil {
		t.Fatalf("macOS prototype overclaims readiness: %#v", built)
	}
	if document.Status != nativePrototypeStatus {
		t.Fatalf("unsigned native manifest status = %q", document.Status)
	}
	path := filepath.Join(out, built.Name)
	command := exec.Command("hdiutil", "verify", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hdiutil verify: %v\n%s", err, output)
	}
	if digest, err := sha256File(path); err != nil || digest != built.SHA256 {
		t.Fatalf("DMG digest = %q, %v", digest, err)
	}
	// The manifest file list is what the bundle is built and verified from,
	// so the recorded paths, modes and digests are the layout.
	paths := map[string]fileEntry{}
	for _, file := range built.Files {
		paths[file.Path] = file
	}
	for _, required := range []string{
		"OwnGit.app/Contents/Info.plist",
		"OwnGit.app/Contents/MacOS/OwnGitLauncher",
		"OwnGit.app/" + appHelperPath,
		"OwnGit.app/Contents/Resources/LICENSE",
		"OwnGit.app/Contents/Resources/THIRD_PARTY_NOTICES/manifest.json",
		"OwnGit.app/Contents/Resources/package-provenance.json",
		"README.txt",
	} {
		if _, ok := paths[required]; !ok {
			t.Errorf("DMG manifest is missing %s", required)
		}
	}
	for _, name := range releaseResources {
		bundled := "OwnGit.app/Contents/Resources/" + name
		file, ok := paths[bundled]
		if !ok {
			t.Fatalf("the app does not carry %s", bundled)
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		noErr(t, err)
		if file.SHA256 != sha256Bytes(source) {
			t.Errorf("%s does not match the source file", bundled)
		}
		if file.Mode != "0644" {
			t.Errorf("%s is recorded with mode %s", bundled, file.Mode)
		}
	}
	for path := range paths {
		if strings.Contains(path, "_CodeSignature") || strings.HasPrefix(path, "OwnGit.app/Contents/Resources/bin/") {
			t.Errorf("unsigned DMG manifest lists %s", path)
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

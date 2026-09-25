package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/version"
)

var npmReadyInputs = []string{
	"-formats", "npm",
	"-homepage", "https://example.test/owngit",
	"-repository-url", "https://example.test/owngit.git",
}

// TestNPMPlatformNames pins the npm name, os, cpu, and executable path of
// every release target.
func TestNPMPlatformNames(t *testing.T) {
	want := map[string][4]string{
		"darwin/arm64":  {"owngit-darwin-arm64", "darwin", "arm64", "bin/owngit"},
		"linux/amd64":   {"owngit-linux-x64", "linux", "x64", "bin/owngit"},
		"linux/arm64":   {"owngit-linux-arm64", "linux", "arm64", "bin/owngit"},
		"windows/amd64": {"owngit-win32-x64", "win32", "x64", "bin/owngit.exe"},
	}
	if len(releaseTargets) != len(want) {
		t.Fatalf("%d release targets, but %d npm platforms are pinned", len(releaseTargets), len(want))
	}
	for _, current := range releaseTargets {
		expected, ok := want[current.String()]
		if !ok {
			t.Fatalf("no npm platform is pinned for %s", current)
		}
		got := [4]string{npmPackageName(current), npmOS(current), npmCPU(current), npmBinaryPath(current)}
		if got != expected {
			t.Errorf("%s maps to %v, want %v", current, got, expected)
		}
		if npmPlatformLabels[current.String()] == "" {
			t.Errorf("%s has no README label", current)
		}
	}
}

func TestNPMFormatSelection(t *testing.T) {
	selected, err := selectFormats("all")
	noErr(t, err)
	if !reflect.DeepEqual(selected, map[string]bool{"homebrew": true, "winget": true, "npm": true, "aur": true}) {
		t.Fatalf("-formats all selects %v", selected)
	}
	selected, err = selectFormats("npm")
	noErr(t, err)
	if !reflect.DeepEqual(selected, map[string]bool{"npm": true}) {
		t.Fatalf("-formats npm selects %v", selected)
	}
}

// TestNPMPackages renders the npm packages from a full portable output and
// checks their metadata, contents, modes, and determinism.
func TestNPMPackages(t *testing.T) {
	root := repoRoot(t)
	manifestPath := filepath.Join(sharedDist(t), "manifest.json")
	document, err := readManifest(manifestPath)
	noErr(t, err)
	arguments := append([]string{"-source", root, "-manifest", manifestPath, "-strict"}, npmReadyInputs...)
	out := t.TempDir()
	noErrf(t, packagingCommand(append(append([]string{}, arguments...), "-out", out)), "render npm")
	npmDir := filepath.Join(out, "npm")

	rootEntries, err := os.ReadDir(out)
	noErr(t, err)
	if len(rootEntries) != 1 || rootEntries[0].Name() != "npm" {
		t.Fatalf("-formats npm wrote more than the npm directory: %v", rootEntries)
	}
	names := []string{}
	entries, err := os.ReadDir(npmDir)
	noErr(t, err)
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	wantNames := []string{"owngit", "owngit-darwin-arm64", "owngit-linux-arm64", "owngit-linux-x64", "owngit-win32-x64"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("npm packages are %v, want %v", names, wantNames)
	}

	main := readNPMPackage(t, filepath.Join(npmDir, "owngit"))
	if main.Version != version.Version || document.Version != version.Version {
		t.Fatalf("main package version %s, manifest %s, source %s", main.Version, document.Version, version.Version)
	}
	wantOptional := map[string]string{}
	for _, current := range releaseTargets {
		wantOptional[npmPackageName(current)] = version.Version
	}
	if !reflect.DeepEqual(main.OptionalDependencies, wantOptional) {
		t.Fatalf("optionalDependencies %v, want exact pins %v", main.OptionalDependencies, wantOptional)
	}
	if main.Name != "owngit" || main.License != "MIT" || main.Private || main.Note != "" ||
		main.Homepage != "https://example.test/owngit" ||
		main.Repository != (npmRepository{Type: "git", URL: "git+https://example.test/owngit.git"}) ||
		!reflect.DeepEqual(main.Bin, map[string]string{"owngit": "bin/owngit.js"}) ||
		main.Engines["node"] != npmNodeRange || len(main.OS) != 0 || len(main.CPU) != 0 {
		t.Fatalf("unexpected main package.json: %+v", main)
	}

	launcher := readText(t, filepath.Join(npmDir, "owngit", "bin", "owngit.js"))
	if !strings.HasPrefix(launcher, "#!/usr/bin/env node\n") || strings.Contains(launcher, "{{") {
		t.Fatal("the launcher is not a rendered node script")
	}
	if !strings.Contains(launcher, `const homepage = "https://example.test/owngit";`) {
		t.Fatal("the launcher does not name the homepage")
	}
	// A SIGTERM right after the executable starts must find the listeners.
	listen, start := strings.Index(launcher, "process.on(signal, listener)"), strings.Index(launcher, "spawn(executable")
	if listen < 0 || start < 0 || listen > start {
		t.Fatal("the launcher does not install its signal listeners before starting the executable")
	}

	for _, current := range releaseTargets {
		name := npmPackageName(current)
		dir := filepath.Join(npmDir, name)
		platform := readNPMPackage(t, dir)
		if platform.Name != name || platform.Version != version.Version || platform.Private ||
			!reflect.DeepEqual(platform.OS, []string{npmOS(current)}) ||
			!reflect.DeepEqual(platform.CPU, []string{npmCPU(current)}) ||
			len(platform.Bin) != 0 || len(platform.OptionalDependencies) != 0 {
			t.Fatalf("unexpected %s package.json: %+v", name, platform)
		}
		entry := `"` + npmPlatformKey(current) + `":{"package":"` + name + `","binary":"` + npmBinaryPath(current) + `"}`
		if !strings.Contains(launcher, entry) {
			t.Fatalf("the launcher does not map %s", entry)
		}

		var built artifact
		for _, candidate := range document.Artifacts {
			if candidate.Target == current.String() {
				built = candidate
			}
		}
		recorded := map[string]string{}
		for _, file := range built.Files {
			recorded[file.Path] = file.SHA256
		}
		digest, err := sha256File(filepath.Join(dir, filepath.FromSlash(npmBinaryPath(current))))
		noErr(t, err)
		if digest != recorded[current.binary] {
			t.Fatalf("%s executable sha256 %s, manifest %s", name, digest, recorded[current.binary])
		}
		// Every license and notice file comes from the archive unchanged.
		packaged := snapshotTree(t, dir)
		for path, sha := range recorded {
			if path != "LICENSE" && !strings.HasPrefix(path, "THIRD_PARTY_NOTICES/") {
				continue
			}
			if packaged[path] != sha {
				t.Fatalf("%s %s sha256 %q, archive %s", name, path, packaged[path], sha)
			}
		}
		for path := range packaged {
			if strings.HasPrefix(path, "THIRD_PARTY_NOTICES/") && recorded[path] == "" {
				t.Fatalf("%s carries %s, which the archive does not", name, path)
			}
		}
	}

	if runtime.GOOS != "windows" {
		executables := map[string]bool{"owngit/bin/owngit.js": true}
		for _, current := range releaseTargets {
			executables[npmPackageName(current)+"/"+npmBinaryPath(current)] = true
		}
		err := filepath.WalkDir(npmDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(npmDir, path)
			if err != nil {
				return err
			}
			want := os.FileMode(0o644)
			if entry.IsDir() || executables[filepath.ToSlash(relative)] {
				want = 0o755
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode %04o, want %04o", relative, info.Mode().Perm(), want)
			}
			return nil
		})
		noErr(t, err)
	}

	t.Run("second render is identical", func(t *testing.T) {
		again := t.TempDir()
		noErrf(t, packagingCommand(append(append([]string{}, arguments...), "-out", again)), "render npm again")
		if !reflect.DeepEqual(snapshotTree(t, npmDir), snapshotTree(t, filepath.Join(again, "npm"))) {
			t.Fatal("two renders of the same portable output differ")
		}
	})

	t.Run("existing npm output is refused", func(t *testing.T) {
		before := snapshotTree(t, npmDir)
		err := packagingCommand(append(append([]string{}, arguments...), "-out", out))
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("packaging into an existing npm output returned %v", err)
		}
		if !reflect.DeepEqual(before, snapshotTree(t, npmDir)) {
			t.Fatal("a refused render changed the existing npm output")
		}
	})
}

func TestNPMReadiness(t *testing.T) {
	root := repoRoot(t)
	manifestPath := filepath.Join(sharedDist(t), "manifest.json")

	t.Run("missing inputs make every package private", func(t *testing.T) {
		out := t.TempDir()
		noErrf(t, packagingCommand([]string{"-source", root, "-manifest", manifestPath, "-out", out, "-formats", "npm"}), "render")
		for _, name := range []string{"owngit", "owngit-darwin-arm64", "owngit-linux-x64", "owngit-linux-arm64", "owngit-win32-x64"} {
			document := readNPMPackage(t, filepath.Join(out, "npm", name))
			if !document.Private || !strings.HasPrefix(document.Note, "UNREADY: missing input(s): homepage, repository-url") {
				t.Fatalf("%s is not marked unready: %+v", name, document)
			}
			if document.Homepage != placeholderURL || !strings.Contains(document.Repository.URL, "example.invalid") {
				t.Fatalf("%s does not use the placeholder URLs: %+v", name, document)
			}
		}
	})

	t.Run("strict names only npm inputs", func(t *testing.T) {
		out := t.TempDir()
		err := packagingCommand([]string{
			"-source", root, "-manifest", manifestPath, "-out", out, "-formats", "npm", "-strict",
			"-homepage", "https://example.test/owngit",
		})
		if err == nil || err.Error() != "missing required inputs: repository-url" {
			t.Fatalf("strict npm render returned %v", err)
		}
		if _, err := os.Lstat(filepath.Join(out, "npm")); !os.IsNotExist(err) {
			t.Fatalf("a refused strict render created output: %v", err)
		}
	})
}

// TestNPMRefusesTamperedArchive changes the linux/amd64 executable inside its
// archive and rehashes the archive, leaving the manifest's record of the
// executable. The npm render must refuse before writing a package.
func TestNPMRefusesTamperedArchive(t *testing.T) {
	root := repoRoot(t)
	dir := copyDist(t, sharedDist(t))
	rewriteArtifact(t, dir, "linux/amd64", false, func(files []memFile) []memFile {
		for index := range files {
			if files[index].name == "owngit" {
				data := append([]byte{}, files[index].data...)
				data[len(data)/2] ^= 0xff
				files[index].data = data
			}
		}
		return files
	})
	out := t.TempDir()
	arguments := append([]string{"-source", root, "-manifest", filepath.Join(dir, "manifest.json"), "-out", out}, npmReadyInputs...)
	err := packagingCommand(arguments)
	if err == nil || !strings.Contains(err.Error(), "owngit sha256") || !strings.Contains(err.Error(), "does not match the manifest") {
		t.Fatalf("npm render of a tampered archive returned %v", err)
	}
	if _, err := os.Lstat(filepath.Join(out, "npm")); !os.IsNotExist(err) {
		t.Fatalf("a refused render created output: %v", err)
	}
}

func TestNPMRefusesMissingTarget(t *testing.T) {
	root := repoRoot(t)
	dir := copyDist(t, sharedDist(t))
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	kept := document.Artifacts[:0]
	for _, built := range document.Artifacts {
		if built.Target != "windows/amd64" {
			kept = append(kept, built)
		}
	}
	document.Artifacts = kept
	writeManifest(t, dir, document)
	out := t.TempDir()
	err = packagingCommand(append([]string{"-source", root, "-manifest", filepath.Join(dir, "manifest.json"), "-out", out}, npmReadyInputs...))
	if err == nil || !strings.Contains(err.Error(), "no windows/amd64 artifact") {
		t.Fatalf("npm render without the windows/amd64 target returned %v", err)
	}
}

// TestNPMRecordedBinaryCheck covers the executable digest check directly, apart
// from the verification that normally catches a mismatch first.
func TestNPMRecordedBinaryCheck(t *testing.T) {
	current, err := targetFor("linux/amd64")
	noErr(t, err)
	data := []byte("synthetic executable")
	payload := portablePayload{
		target: current,
		built:  artifact{Name: "synthetic.tar.gz", Files: []fileEntry{{Path: "owngit", SHA256: sha256Bytes(data)}}},
		binary: archiveEntry{name: "owngit", sha: sha256Bytes(data), data: data},
	}
	noErrf(t, checkRecordedBinary(payload), "matching digest")

	changed := payload
	changed.binary.data = []byte("other executable")
	if err := checkRecordedBinary(changed); err == nil || !strings.Contains(err.Error(), "portable manifest records") {
		t.Fatalf("changed executable bytes returned %v", err)
	}
	unrecorded := payload
	unrecorded.built.Files = nil
	if err := checkRecordedBinary(unrecorded); err == nil || !strings.Contains(err.Error(), "records no owngit") {
		t.Fatalf("an unrecorded executable returned %v", err)
	}
}

func readNPMPackage(t *testing.T, dir string) npmPackageJSON {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	noErr(t, err)
	var document npmPackageJSON
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	noErrf(t, decoder.Decode(&document), "decode %s/package.json", dir)
	// Re-encoding reproduces the file only if its keys are in the fixed order.
	canonical, err := npmJSON(document, "  ")
	noErr(t, err)
	if string(data) != string(canonical)+"\n" {
		t.Fatalf("%s/package.json does not have the fixed layout:\n%s", dir, data)
	}
	return document
}

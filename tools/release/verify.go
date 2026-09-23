package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"owngit/internal/version"
)

// forbiddenComponents are path components that must never appear inside a
// portable archive, at any depth. The checkout, its private records, and its
// build output stay outside.
var forbiddenComponents = []struct {
	name   string
	reason string
}{
	{".git", "Git metadata"},
	{".local", "private planning records"},
	{".playwright-mcp", "browser tool state"},
	{"dist", "build output"},
	{"stage", "build output"},
	{"bin", "build output"},
}

// forbiddenNames are base names that must never appear inside an archive.
var forbiddenNames = []struct {
	pattern string
	reason  string
}{
	{"go.mod", "module file"},
	{"go.sum", "module file"},
	{"*.sqlite", "state database"},
	{"*.sqlite-wal", "state database"},
	{"*.sqlite-shm", "state database"},
	{"*.test", "test binary"},
	{"*.log", "log file"},
	{"*.tmp", "temporary file"},
	{"*.bak", "backup file"},
	{".DS_Store", "macOS metadata"},
}

func forbiddenReason(name string) string {
	for _, part := range strings.Split(name, "/") {
		for _, entry := range forbiddenComponents {
			if part == entry.name {
				return entry.reason
			}
		}
	}
	base := name
	if at := strings.LastIndex(name, "/"); at >= 0 {
		base = name[at+1:]
	}
	for _, entry := range forbiddenNames {
		if matched, err := filepath.Match(entry.pattern, base); err == nil && matched {
			return entry.reason
		}
	}
	return ""
}

func verifyCommand(arguments []string) error {
	set := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := set.String("dir", "dist", "directory holding manifest.json and the archives")
	goTool := set.String("go", "go", "Go toolchain command")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	return verifyDir(absolute, *goTool)
}

func verifyDir(dir, goTool string) error {
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	if document.Version != version.Version {
		return fmt.Errorf("manifest version %q does not match the source version %q", document.Version, version.Version)
	}
	if document.Source.Version != document.Version {
		return fmt.Errorf("manifest source version %q does not match the artifact version %q", document.Source.Version, document.Version)
	}
	if len(document.Artifacts) == 0 {
		return fmt.Errorf("manifest lists no artifacts")
	}

	seen := map[string]bool{}
	linked := map[string]bool{}
	notices := map[string]bool{}
	for _, built := range document.Artifacts {
		if seen[built.Target] {
			return fmt.Errorf("manifest lists target %s twice", built.Target)
		}
		seen[built.Target] = true
		coverage, err := verifyArtifact(dir, goTool, document.Version, built)
		if err != nil {
			return fmt.Errorf("%s: %w", built.Target, err)
		}
		for module := range coverage.linked {
			linked[module] = true
		}
		for module := range coverage.notices {
			notices[module] = true
		}
	}
	// The notice set is the union across targets, so an entry only has to be
	// linked by some target. An entry no target links is stale.
	for module := range notices {
		if strings.HasPrefix(module, bundledNoticePrefix) {
			continue
		}
		if !linked[module] {
			return fmt.Errorf("the notice set has an entry for %s that no target links", module)
		}
	}
	if err := verifyChecksums(dir, document.Artifacts); err != nil {
		return err
	}
	fmt.Printf("verified %d artifacts in %s\n", len(document.Artifacts), dir)
	return nil
}

func readManifest(path string) (manifest, error) {
	var document manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return document, err
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return document, fmt.Errorf("%s: %w", path, err)
	}
	return document, nil
}

// artifactCoverage is what one archive proves about the notice set: the
// modules the binary links, and the modules its notice set declares.
type artifactCoverage struct {
	linked  map[string]bool
	notices map[string]bool
}

func verifyArtifact(dir, goTool, appVersion string, built artifact) (artifactCoverage, error) {
	expected, err := targetFor(built.Target)
	if err != nil {
		return artifactCoverage{}, err
	}
	if want := expected.archiveName(appVersion); built.Name != want {
		return artifactCoverage{}, fmt.Errorf("archive name %q does not match the expected %q", built.Name, want)
	}
	archivePath := filepath.Join(dir, built.Name)
	info, err := os.Stat(archivePath)
	if err != nil {
		return artifactCoverage{}, err
	}
	if info.Size() != built.Size {
		return artifactCoverage{}, fmt.Errorf("archive size %d does not match the manifest size %d", info.Size(), built.Size)
	}
	digest, err := sha256File(archivePath)
	if err != nil {
		return artifactCoverage{}, err
	}
	if digest != built.SHA256 {
		return artifactCoverage{}, fmt.Errorf("archive sha256 %s does not match the manifest %s", digest, built.SHA256)
	}

	entries, err := readArchive(archivePath, expected.format)
	if err != nil {
		return artifactCoverage{}, err
	}
	if err := compareEntries(built, entries); err != nil {
		return artifactCoverage{}, err
	}
	if err := verifyRequiredEntries(expected, entries); err != nil {
		return artifactCoverage{}, err
	}
	noticeModules, noticeDocument, err := verifyNoticeSet(entries)
	if err != nil {
		return artifactCoverage{}, err
	}
	if err := verifyResourceSet(entries); err != nil {
		return artifactCoverage{}, err
	}
	if err := verifyReadmeVersion(entries, appVersion); err != nil {
		return artifactCoverage{}, err
	}
	linked, err := verifyBinary(goTool, expected, entries, built, appVersion, noticeModules, noticeDocument)
	return artifactCoverage{linked: linked, notices: noticeModules}, err
}

func targetFor(name string) (target, error) {
	for _, candidate := range releaseTargets {
		if candidate.String() == name {
			return candidate, nil
		}
	}
	return target{}, fmt.Errorf("unknown target %q", name)
}

// archiveEntry is one decoded archive member.
type archiveEntry struct {
	name string
	mode int64
	size int64
	sha  string
	data []byte
}

func readArchive(path, format string) ([]archiveEntry, error) {
	if format == "zip" {
		return readZip(path)
	}
	return readTarGz(path)
}

// readTarGz decodes a tar archive and rejects every nonregular entry, so a
// symlink or directory member cannot be silently skipped.
func readTarGz(path string) ([]archiveEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	var entries []archiveEntry
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("archive entry %s is not a regular file (type %q)", header.Name, header.Typeflag)
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{
			name: header.Name,
			mode: header.Mode,
			size: header.Size,
			sha:  sha256Bytes(data),
			data: data,
		})
	}
	return entries, nil
}

// readZip decodes a zip archive and rejects directory and symlink members.
func readZip(path string) ([]archiveEntry, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	var entries []archiveEntry
	for _, file := range archive.File {
		mode := file.Mode()
		if file.FileInfo().IsDir() {
			return nil, fmt.Errorf("archive entry %s is a directory", file.Name)
		}
		if mode&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive entry %s is a symlink", file.Name)
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("archive entry %s is not a regular file", file.Name)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{
			name: file.Name,
			mode: int64(mode.Perm()),
			size: int64(file.UncompressedSize64),
			sha:  sha256Bytes(data),
			data: data,
		})
	}
	return entries, nil
}

func compareEntries(built artifact, entries []archiveEntry) error {
	actual := map[string]archiveEntry{}
	for _, entry := range entries {
		if reason := forbiddenReason(entry.name); reason != "" {
			return fmt.Errorf("archive contains %s (%s)", entry.name, reason)
		}
		if err := validateRelativePath(entry.name); err != nil {
			return fmt.Errorf("archive entry %s: %w", entry.name, err)
		}
		if _, duplicate := actual[entry.name]; duplicate {
			return fmt.Errorf("archive contains %s twice", entry.name)
		}
		actual[entry.name] = entry
	}
	expected := map[string]fileEntry{}
	for _, file := range built.Files {
		expected[file.Path] = file
	}
	for name, file := range expected {
		entry, ok := actual[name]
		if !ok {
			return fmt.Errorf("archive is missing %s", name)
		}
		if entry.size != file.Size {
			return fmt.Errorf("%s size %d does not match the manifest size %d", name, entry.size, file.Size)
		}
		if entry.sha != file.SHA256 {
			return fmt.Errorf("%s sha256 %s does not match the manifest %s", name, entry.sha, file.SHA256)
		}
		if want := fmt.Sprintf("%04o", entry.mode); want != file.Mode {
			return fmt.Errorf("%s mode %s does not match the manifest mode %s", name, want, file.Mode)
		}
	}
	for name := range actual {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("archive contains unexpected %s", name)
		}
	}
	return nil
}

func verifyRequiredEntries(expected target, entries []archiveEntry) error {
	required := append([]string{expected.binary, "LICENSE", "README.txt"}, releaseResources...)
	for _, name := range required {
		found := false
		for _, entry := range entries {
			if entry.name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("archive is missing the required entry %s", name)
		}
	}
	for _, entry := range entries {
		if entry.name == expected.binary && entry.mode&0o111 == 0 {
			return fmt.Errorf("%s is not executable (mode %04o)", entry.name, entry.mode)
		}
	}
	return nil
}

// verifyNoticeSet checks the whole notice set, not just its index. Every file
// the archived notice manifest declares must be present with the declared
// digest, an embedded-asset entry must exist, and no undeclared notice file
// may ride along. It returns the declared module@version set so the binary's
// own dependency list can be compared with it.
func verifyNoticeSet(entries []archiveEntry) (map[string]bool, noticeManifest, error) {
	var document noticeManifest
	byName := map[string]archiveEntry{}
	for _, entry := range entries {
		byName[entry.name] = entry
	}
	index, ok := byName["THIRD_PARTY_NOTICES/manifest.json"]
	if !ok {
		return nil, document, fmt.Errorf("archive is missing THIRD_PARTY_NOTICES/manifest.json")
	}
	if _, ok := byName["THIRD_PARTY_NOTICES/README.md"]; !ok {
		return nil, document, fmt.Errorf("archive is missing THIRD_PARTY_NOTICES/README.md")
	}
	if err := json.Unmarshal(index.data, &document); err != nil {
		return nil, document, fmt.Errorf("THIRD_PARTY_NOTICES/manifest.json: %w", err)
	}
	if len(document.Entries) == 0 {
		return nil, document, fmt.Errorf("the archived notice manifest lists no entries")
	}
	declared := map[string]bool{
		"THIRD_PARTY_NOTICES/README.md":     true,
		"THIRD_PARTY_NOTICES/manifest.json": true,
	}
	modules := map[string]bool{}
	bundled := false
	for _, entry := range document.Entries {
		if strings.HasPrefix(entry.Module, bundledNoticePrefix) {
			bundled = true
		}
		if len(entry.Files) == 0 {
			return nil, document, fmt.Errorf("notice entry %s lists no files", entry.Module)
		}
		modules[entry.Module+"@"+entry.Version] = true
		for _, file := range entry.Files {
			name := "THIRD_PARTY_NOTICES/" + entry.Module + "/" + file.Path
			declared[name] = true
			archived, ok := byName[name]
			if !ok {
				return nil, document, fmt.Errorf("archive is missing the declared notice %s", name)
			}
			if archived.sha != file.SHA256 {
				return nil, document, fmt.Errorf("%s does not match the digest in the notice manifest", name)
			}
		}
	}
	if !bundled {
		return nil, document, fmt.Errorf("the notice manifest has no %s entry for an embedded asset", bundledNoticePrefix)
	}
	for name := range byName {
		if !strings.HasPrefix(name, "THIRD_PARTY_NOTICES/") {
			continue
		}
		if !declared[name] {
			return nil, document, fmt.Errorf("archive contains the undeclared notice %s", name)
		}
	}
	return modules, document, nil
}

func verifyReadmeVersion(entries []archiveEntry, appVersion string) error {
	for _, entry := range entries {
		if entry.name != "README.txt" {
			continue
		}
		if !strings.Contains(string(entry.data), "OwnGit "+appVersion) {
			return fmt.Errorf("README.txt does not name version %s", appVersion)
		}
	}
	return nil
}

// binaryFacts is the embedded metadata read from an archived binary.
type binaryFacts struct {
	metadata  string
	goVersion string
	mainPath  string
	module    string
	deps      map[string]string
	settings  map[string]string
}

// verifyBinary inspects the embedded metadata of the archived binary instead
// of trusting the manifest, checks that the notice set covers the binary's own
// dependency list, and executes it only when the target matches the host. A
// rehashed archive from another target therefore fails. It returns the module
// set the binary actually links, so the caller can check the reverse direction
// across all targets.
func verifyBinary(goTool string, expected target, entries []archiveEntry, built artifact, appVersion string, noticeModules map[string]bool, noticeDocument noticeManifest) (map[string]bool, error) {
	var binary []byte
	for _, entry := range entries {
		if entry.name == expected.binary {
			binary = entry.data
		}
	}
	if binary == nil {
		return nil, fmt.Errorf("archive has no %s to inspect", expected.binary)
	}
	dir, err := os.MkdirTemp("", "owngit-verify-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, expected.binary)
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		return nil, err
	}

	facts, err := inspectBinary(goTool, path, expected)
	if err != nil {
		return nil, err
	}
	if facts.metadata != built.BuildInfo {
		return nil, fmt.Errorf("embedded build metadata does not match the manifest")
	}
	if noticeDocument.Go != facts.goVersion {
		return nil, fmt.Errorf("the notice manifest records Go %s but the binary embeds %s", noticeDocument.Go, facts.goVersion)
	}

	// The notice set must cover the binary's own dependency list.
	linked := map[string]bool{}
	for module, version := range facts.deps {
		linked[module+"@"+version] = true
		if !noticeModules[module+"@"+version] {
			return nil, fmt.Errorf("the binary links %s@%s but the notice set has no entry", module, version)
		}
	}
	linked["go-runtime@"+facts.goVersion] = true
	if !noticeModules["go-runtime@"+facts.goVersion] {
		return nil, fmt.Errorf("the binary embeds Go %s but the notice set has no go-runtime entry for it", facts.goVersion)
	}

	want := "owngit " + appVersion + "\n"
	if built.Executed && built.ExecutedOutput != want {
		return nil, fmt.Errorf("manifest records execution output %q, want %q", built.ExecutedOutput, want)
	}
	if expected.goos != runtime.GOOS || expected.goarch != runtime.GOARCH {
		return linked, nil
	}
	if !built.Executed {
		return nil, fmt.Errorf("the manifest does not record the execution this host can perform")
	}
	output, err := exec.Command(path, "version").Output()
	if err != nil {
		return nil, fmt.Errorf("running %s version: %w", expected.binary, err)
	}
	if string(output) != want {
		return nil, fmt.Errorf("binary reported %q, want %q", string(output), want)
	}
	return linked, nil
}

// inspectBinary reads the embedded build metadata and checks the target, the
// main package, the module, and the build settings that packaging promises.
func inspectBinary(goTool, binaryPath string, expected target) (binaryFacts, error) {
	goVersion, metadata, err := buildInfo(goTool, binaryPath)
	if err != nil {
		return binaryFacts{}, err
	}
	facts := binaryFacts{
		metadata: metadata, goVersion: goVersion,
		deps: map[string]string{}, settings: map[string]string{},
	}
	for _, line := range strings.Split(metadata, "\n") {
		fields := strings.Split(strings.TrimPrefix(line, "\t"), "\t")
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "path":
			facts.mainPath = fields[1]
		case "mod":
			facts.module = fields[1]
		case "dep":
			if len(fields) >= 3 {
				facts.deps[fields[1]] = fields[2]
			}
		case "build":
			key, value, found := strings.Cut(fields[1], "=")
			if found {
				facts.settings[key] = value
			}
		}
	}
	if facts.mainPath != "owngit/cmd/owngit" {
		return facts, fmt.Errorf("embedded main package is %q, want %q", facts.mainPath, "owngit/cmd/owngit")
	}
	if facts.module != "owngit" {
		return facts, fmt.Errorf("embedded module is %q, want %q", facts.module, "owngit")
	}
	for _, check := range []struct{ key, want string }{
		{"GOOS", expected.goos},
		{"GOARCH", expected.goarch},
		{"-trimpath", "true"},
		{"CGO_ENABLED", "0"},
	} {
		if facts.settings[check.key] != check.want {
			return facts, fmt.Errorf("embedded build setting %s is %q, want %q", check.key, facts.settings[check.key], check.want)
		}
	}
	return facts, nil
}

func verifyChecksums(dir string, artifacts []artifact) error {
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	lines := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	expected := make([]string, 0, len(artifacts))
	for _, built := range artifacts {
		expected = append(expected, fmt.Sprintf("%s  %s", built.SHA256, built.Name))
	}
	sort.Strings(expected)
	if strings.Join(lines, "\n") != strings.Join(expected, "\n") {
		return fmt.Errorf("SHA256SUMS does not match the manifest")
	}
	return nil
}

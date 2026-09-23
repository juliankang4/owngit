package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

const nativePrototypeStatus = "unsigned local prototype; native installation and public readiness are unverified"

var baselinePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:=;,+-]{0,511}$`)

type nativeManifest struct {
	Schema                 int              `json:"schema"`
	Status                 string           `json:"status"`
	Version                string           `json:"version"`
	Baseline               string           `json:"baseline"`
	PortableManifestSHA256 string           `json:"portable_manifest_sha256"`
	PortableSource         sourceInfo       `json:"portable_source"`
	Artifacts              []nativeArtifact `json:"artifacts"`
}

type nativeArtifact struct {
	Format                   string      `json:"format"`
	Target                   string      `json:"target"`
	Name                     string      `json:"name"`
	SHA256                   string      `json:"sha256"`
	Size                     int64       `json:"size"`
	Prototype                bool        `json:"prototype"`
	PublisherSigned          bool        `json:"publisher_signed"`
	Notarized                bool        `json:"notarized"`
	StructureVerified        bool        `json:"structure_verified"`
	NativeInstallVerified    bool        `json:"native_install_verified"`
	PublicReady              bool        `json:"public_ready"`
	Toolchain                string      `json:"toolchain"`
	PortableArtifact         string      `json:"portable_artifact"`
	PortableArtifactSHA256   string      `json:"portable_artifact_sha256"`
	ApplicationBinarySHA256  string      `json:"application_binary_sha256"`
	PackagedProvenanceSHA256 string      `json:"packaged_provenance_sha256"`
	Files                    []fileEntry `json:"files"`
}

type packageProvenance struct {
	Schema                     int    `json:"schema"`
	Status                     string `json:"status"`
	Version                    string `json:"version"`
	Target                     string `json:"target"`
	Format                     string `json:"format"`
	Baseline                   string `json:"baseline"`
	PortableManifestSHA256     string `json:"portable_manifest_sha256"`
	PortableArtifact           string `json:"portable_artifact"`
	PortableArtifactSHA256     string `json:"portable_artifact_sha256"`
	ApplicationBinarySHA256    string `json:"application_binary_sha256"`
	PublisherSigned            bool   `json:"publisher_signed"`
	Notarized                  bool   `json:"notarized"`
	NativeInstallationVerified bool   `json:"native_installation_verified"`
	PublicDistributionReady    bool   `json:"public_distribution_ready"`
}

type portablePayload struct {
	target  target
	built   artifact
	entries map[string]archiveEntry
	binary  archiveEntry
}

type nativeInputs struct {
	root           string
	manifest       manifest
	manifestSHA256 string
	baseline       string
	payloads       map[string]portablePayload
	goTool         string
	xcrun          string
	hdiutil        string
	macToolchain   string
	selected       map[string]bool
}

type nativePackageFile struct {
	path string
	mode int64
	data []byte
}

func nativeCommand(arguments []string) error {
	set := flag.NewFlagSet("native", flag.ContinueOnError)
	source := set.String("source", ".", "module root holding native package inputs")
	manifestPath := set.String("manifest", "dist/manifest.json", "verified portable artifact manifest")
	out := set.String("out", "dist/native", "fresh output directory that must not exist")
	formats := set.String("formats", "deb", "comma-separated formats to build: macos, deb, or all")
	baseline := set.String("baseline", "", "portable/core baseline label recorded in every prototype package")
	goTool := set.String("go", "go", "Go toolchain command")
	xcrun := set.String("xcrun", "xcrun", "Apple toolchain launcher for the macOS format")
	hdiutil := set.String("hdiutil", "hdiutil", "disk image tool for the macOS format")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}
	if !baselinePattern.MatchString(*baseline) {
		return errors.New("-baseline must be 1 to 512 safe label characters and contain no paths or whitespace")
	}
	selected, err := selectNativeFormats(*formats)
	if err != nil {
		return err
	}
	root, err := moduleRoot(*source)
	if err != nil {
		return err
	}
	inputs, err := loadNativeInputs(root, *manifestPath, *baseline, *goTool, *xcrun, *hdiutil, selected)
	if err != nil {
		return err
	}
	outDir, err := createFreshNativeOutput(*out)
	if err != nil {
		return err
	}

	document := nativeManifest{
		Schema:                 1,
		Status:                 nativePrototypeStatus,
		Version:                inputs.manifest.Version,
		Baseline:               inputs.baseline,
		PortableManifestSHA256: inputs.manifestSHA256,
		PortableSource:         inputs.manifest.Source,
	}
	if selected["macos"] {
		built, err := buildMacPrototype(inputs, outDir)
		if err != nil {
			return fmt.Errorf("macos prototype: %w; partial output remains at %s", err, outDir)
		}
		document.Artifacts = append(document.Artifacts, built)
		fmt.Printf("built prototype %s %s\n", built.Target, built.Name)
	}
	if selected["deb"] {
		for _, name := range []string{"linux/amd64", "linux/arm64"} {
			built, err := buildDebPrototype(inputs, outDir, name)
			if err != nil {
				return fmt.Errorf("%s prototype: %w; partial output remains at %s", name, err, outDir)
			}
			document.Artifacts = append(document.Artifacts, built)
			fmt.Printf("built prototype %s %s\n", built.Target, built.Name)
		}
	}
	sort.Slice(document.Artifacts, func(i, j int) bool {
		return document.Artifacts[i].Name < document.Artifacts[j].Name
	})
	if err := writeNativeChecksums(outDir, document.Artifacts); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(outDir, "native-manifest.json"), append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", filepath.Join(outDir, "native-manifest.json"))
	return nil
}

func selectNativeFormats(list string) (map[string]bool, error) {
	selected := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		switch strings.TrimSpace(item) {
		case "":
		case "all":
			selected["macos"] = true
			selected["deb"] = true
		case "macos", "deb":
			selected[strings.TrimSpace(item)] = true
		default:
			return nil, fmt.Errorf("unknown native format %q; use macos, deb, or all", strings.TrimSpace(item))
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no native format selected")
	}
	return selected, nil
}

func loadNativeInputs(root, manifestPath, baseline, goTool, xcrun, hdiutil string, selected map[string]bool) (nativeInputs, error) {
	return loadNativeInputsWithCleanup(root, manifestPath, baseline, goTool, xcrun, hdiutil, selected, os.RemoveAll)
}

func loadNativeInputsWithCleanup(root, manifestPath, baseline, goTool, xcrun, hdiutil string, selected map[string]bool, cleanup func(string) error) (result nativeInputs, resultErr error) {
	absoluteManifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nativeInputs{}, err
	}
	if filepath.Base(absoluteManifest) != "manifest.json" {
		return nativeInputs{}, errors.New("portable manifest must be named manifest.json so its complete output directory can be verified")
	}
	appVersion, err := versionFromSource(root)
	if err != nil {
		return nativeInputs{}, err
	}
	snapshot, err := snapshotPortableInputsWithCleanup(absoluteManifest, cleanup)
	if err != nil {
		return nativeInputs{}, err
	}
	defer func() {
		if cleanupErr := removePortableInputSnapshot(snapshot.dir, cleanup); cleanupErr != nil {
			result = nativeInputs{}
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	if snapshot.manifest.Version != appVersion {
		return nativeInputs{}, fmt.Errorf("portable manifest version %q does not match source version %q", snapshot.manifest.Version, appVersion)
	}
	if err := verifyDir(snapshot.dir, goTool); err != nil {
		return nativeInputs{}, fmt.Errorf("portable baseline: %w", err)
	}
	inputs := nativeInputs{
		root: root, manifest: snapshot.manifest,
		manifestSHA256: snapshot.manifestSHA256, baseline: baseline, payloads: map[string]portablePayload{},
		goTool: goTool, xcrun: xcrun, hdiutil: hdiutil, selected: selected,
	}
	required := []string{}
	if selected["macos"] {
		required = append(required, "darwin/arm64")
		toolchain, err := commandOutput(xcrun, "swiftc", "--version")
		if err != nil {
			return nativeInputs{}, fmt.Errorf("inspect Swift toolchain: %w", err)
		}
		if _, err := commandOutput(hdiutil, "help"); err != nil {
			return nativeInputs{}, fmt.Errorf("inspect hdiutil: %w", err)
		}
		inputs.macToolchain = firstNonemptyLine(toolchain)
		for _, relative := range []string{
			filepath.Join("packaging", "macos", "Launcher.swift"),
			filepath.Join("packaging", "macos", "Lifecycle.swift"),
			filepath.Join("packaging", "macos", "Info.plist.tmpl"),
			filepath.Join("packaging", "macos", "README.txt.tmpl"),
		} {
			if _, err := readRegularInput(filepath.Join(root, relative)); err != nil {
				return nativeInputs{}, err
			}
		}
	}
	if selected["deb"] {
		required = append(required, "linux/amd64", "linux/arm64")
		for _, relative := range []string{
			filepath.Join("packaging", "linux", "control.tmpl"),
			filepath.Join("packaging", "linux", "owngit.desktop"),
			filepath.Join("packaging", "linux", "README.Debian.tmpl"),
		} {
			if _, err := readRegularInput(filepath.Join(root, relative)); err != nil {
				return nativeInputs{}, err
			}
		}
	}
	for _, name := range required {
		if _, exists := inputs.payloads[name]; exists {
			continue
		}
		payload, err := loadPortablePayload(snapshot.dir, snapshot.manifest, name)
		if err != nil {
			return nativeInputs{}, err
		}
		inputs.payloads[name] = payload
	}
	return inputs, nil
}

// portableInputSnapshot owns a private copy of one manifest generation. The
// verifier and payload loader both read this copy, never the mutable source.
type portableInputSnapshot struct {
	dir            string
	manifest       manifest
	manifestSHA256 string
}

func snapshotPortableInputs(manifestPath string) (portableInputSnapshot, error) {
	return snapshotPortableInputsWithCleanup(manifestPath, os.RemoveAll)
}

func snapshotPortableInputsWithCleanup(manifestPath string, cleanup func(string) error) (result portableInputSnapshot, resultErr error) {
	manifestData, err := readPinnedRegularFile(manifestPath)
	if err != nil {
		return portableInputSnapshot{}, err
	}
	var document manifest
	if err := json.Unmarshal(manifestData, &document); err != nil {
		return portableInputSnapshot{}, fmt.Errorf("%s: %w", manifestPath, err)
	}

	sourceDir := filepath.Dir(manifestPath)
	archiveNames := make([]string, 0, len(document.Artifacts))
	seenTargets := map[string]bool{}
	seenNames := map[string]bool{}
	for _, built := range document.Artifacts {
		current, err := targetFor(built.Target)
		if err != nil {
			return portableInputSnapshot{}, err
		}
		if seenTargets[built.Target] {
			return portableInputSnapshot{}, fmt.Errorf("manifest lists target %s twice", built.Target)
		}
		seenTargets[built.Target] = true
		want := current.archiveName(document.Version)
		if built.Name != want {
			return portableInputSnapshot{}, fmt.Errorf("archive name %q does not match the expected %q", built.Name, want)
		}
		if filepath.Base(built.Name) != built.Name || built.Name == "." {
			return portableInputSnapshot{}, fmt.Errorf("portable archive name %q is not a plain file name", built.Name)
		}
		if seenNames[built.Name] {
			return portableInputSnapshot{}, fmt.Errorf("manifest lists archive %s twice", built.Name)
		}
		seenNames[built.Name] = true
		archiveNames = append(archiveNames, built.Name)
	}

	dir, err := os.MkdirTemp("", "owngit-native-input-")
	if err != nil {
		return portableInputSnapshot{}, err
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		if cleanupErr := removePortableInputSnapshot(dir, cleanup); cleanupErr != nil {
			result = portableInputSnapshot{}
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestData, 0o600); err != nil {
		return portableInputSnapshot{}, err
	}
	for _, name := range append([]string{"SHA256SUMS"}, archiveNames...) {
		data, err := readPinnedRegularFile(filepath.Join(sourceDir, name))
		if err != nil {
			return portableInputSnapshot{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return portableInputSnapshot{}, err
		}
	}
	keep = true
	return portableInputSnapshot{
		dir: dir, manifest: document, manifestSHA256: sha256Bytes(manifestData),
	}, nil
}

func removePortableInputSnapshot(dir string, cleanup func(string) error) error {
	if err := cleanup(dir); err != nil {
		return fmt.Errorf("remove native input snapshot %s: %w", dir, err)
	}
	return nil
}

func loadPortablePayload(dir string, document manifest, name string) (portablePayload, error) {
	var built *artifact
	for index := range document.Artifacts {
		if document.Artifacts[index].Target == name {
			built = &document.Artifacts[index]
			break
		}
	}
	if built == nil {
		return portablePayload{}, fmt.Errorf("portable manifest has no %s artifact", name)
	}
	current, err := targetFor(name)
	if err != nil {
		return portablePayload{}, err
	}
	entries, err := readArchive(filepath.Join(dir, built.Name), current.format)
	if err != nil {
		return portablePayload{}, err
	}
	// Bind the retained payload bytes to the verified manifest in case the
	// snapshot archive changed between verification and this final read.
	if err := compareEntries(*built, entries); err != nil {
		return portablePayload{}, fmt.Errorf("portable payload %s changed after verification: %w", built.Name, err)
	}
	byName := make(map[string]archiveEntry, len(entries))
	for _, entry := range entries {
		byName[entry.name] = entry
	}
	binary, ok := byName[current.binary]
	if !ok {
		return portablePayload{}, fmt.Errorf("portable artifact %s has no %s", built.Name, current.binary)
	}
	return portablePayload{target: current, built: *built, entries: byName, binary: binary}, nil
}

func createFreshNativeOutput(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("native output %s already exists; choose a fresh path", absolute)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(absolute, 0o755); err != nil {
		return "", err
	}
	return absolute, nil
}

func renderNativeTemplate(path string, data any) ([]byte, error) {
	body, err := readRegularInput(path)
	if err != nil {
		return nil, err
	}
	parsed, err := template.New(filepath.Base(path)).Parse(string(body))
	if err != nil {
		return nil, err
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, data); err != nil {
		return nil, err
	}
	return []byte(rendered.String()), nil
}

func readRegularInput(path string) ([]byte, error) {
	return readPinnedRegularFile(path)
}

// readPinnedRegularFile reads a regular file only if the opened handle is the
// file inspected before the open and the path still names it after the read.
// Both inspections use lstatIdentity so the comparison holds on Windows.
func readPinnedRegularFile(path string) ([]byte, error) {
	before, err := lstatIdentity(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("package input %s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) {
		return nil, fmt.Errorf("package input %s changed while it was opened", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	after, err := lstatIdentity(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(opened, after) || opened.Size() != int64(len(data)) {
		return nil, fmt.Errorf("package input %s changed while it was read", path)
	}
	return data, nil
}

func provenanceBytes(inputs nativeInputs, payload portablePayload, format string) ([]byte, error) {
	document := packageProvenance{
		Schema: 1, Status: nativePrototypeStatus, Version: inputs.manifest.Version,
		Target: payload.target.String(), Format: format, Baseline: inputs.baseline,
		PortableManifestSHA256: inputs.manifestSHA256,
		PortableArtifact:       payload.built.Name, PortableArtifactSHA256: payload.built.SHA256,
		ApplicationBinarySHA256: payload.binary.sha,
		PublisherSigned:         false, Notarized: false, NativeInstallationVerified: false, PublicDistributionReady: false,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func explicitSharedFiles(payload portablePayload) ([]nativePackageFile, error) {
	names := make([]string, 0, len(payload.entries))
	for name := range payload.entries {
		if name == "LICENSE" || strings.HasPrefix(name, "THIRD_PARTY_NOTICES/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	files := make([]nativePackageFile, 0, len(names))
	for _, name := range names {
		entry := payload.entries[name]
		if err := validateNativePath(name); err != nil {
			return nil, err
		}
		files = append(files, nativePackageFile{path: name, mode: 0o644, data: entry.data})
	}
	if len(files) < 2 {
		return nil, errors.New("portable artifact does not contain the required license and notice set")
	}
	return files, nil
}

func validateNativePath(name string) error {
	if err := validateRelativePath(name); err != nil {
		return err
	}
	for _, part := range strings.Split(name, "/") {
		switch part {
		case ".git", ".local", ".playwright-mcp", "dist", "stage":
			return fmt.Errorf("native package path %s contains forbidden component %s", name, part)
		}
	}
	base := filepath.Base(name)
	for _, pattern := range []string{"*.sqlite", "*.sqlite-wal", "*.sqlite-shm", "*.test", "*.log", "*.tmp", "*.bak", ".DS_Store"} {
		if matched, _ := filepath.Match(pattern, base); matched {
			return fmt.Errorf("native package path %s is forbidden", name)
		}
	}
	return nil
}

func fileEntries(files []nativePackageFile) []fileEntry {
	entries := make([]fileEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, fileEntry{
			Path: file.path, Mode: fmt.Sprintf("%04o", file.mode),
			Size: int64(len(file.data)), SHA256: sha256Bytes(file.data),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func writeNativeChecksums(outDir string, artifacts []nativeArtifact) error {
	lines := make([]string, 0, len(artifacts))
	for _, built := range artifacts {
		lines = append(lines, fmt.Sprintf("%s  %s", built.SHA256, built.Name))
	}
	sort.Strings(lines)
	_, err := writeFile(filepath.Join(outDir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return err
}

func firstNonemptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

func commandOutput(name string, arguments ...string) (string, error) {
	command := exec.Command(name, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s %s: %s", name, strings.Join(arguments, " "), message)
	}
	return stdout.String(), nil
}

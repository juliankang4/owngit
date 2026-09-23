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
	"text/template"
)

// target is one portable release target. macOS is Apple Silicon only, so
// darwin/amd64 is deliberately absent.
type target struct {
	goos   string
	goarch string
	binary string
	format string
}

func (t target) String() string { return t.goos + "/" + t.goarch }

func (t target) key() string { return t.goos + "-" + t.goarch }

func (t target) archiveName(version string) string {
	return fmt.Sprintf("owngit_%s_%s_%s.%s", version, t.goos, t.goarch, t.format)
}

var releaseTargets = []target{
	{"darwin", "arm64", "owngit", "tar.gz"},
	{"linux", "amd64", "owngit", "tar.gz"},
	{"linux", "arm64", "owngit", "tar.gz"},
	{"windows", "amd64", "owngit.exe", "zip"},
}

// selectTargets resolves the -targets flag against the release target table.
func selectTargets(list string) ([]target, error) {
	if strings.TrimSpace(list) == "" {
		return releaseTargets, nil
	}
	var selected []target
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		found := false
		for _, candidate := range releaseTargets {
			if candidate.String() == item {
				selected = append(selected, candidate)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown target %q; known targets are %s", item, knownTargets())
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no target selected")
	}
	return selected, nil
}

func knownTargets() string {
	names := make([]string, 0, len(releaseTargets))
	for _, candidate := range releaseTargets {
		names = append(names, candidate.String())
	}
	return strings.Join(names, ", ")
}

// stagedFile is one archive entry with its content identity.
type stagedFile struct {
	name string
	path string
	mode int64
	size int64
	sha  string
	// data holds the approved bytes of an entry whose identity was checked
	// when it was read. The archive writer uses this snapshot instead of
	// reopening path, so the file that was inspected is the file that is
	// packaged. Entries staged by the build itself leave it nil and are
	// streamed from their staged path.
	data []byte
}

type fileEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type artifact struct {
	Target         string      `json:"target"`
	Name           string      `json:"name"`
	SHA256         string      `json:"sha256"`
	Size           int64       `json:"size"`
	Executed       bool        `json:"executed"`
	ExecutedOutput string      `json:"executed_output,omitempty"`
	BuildInfo      string      `json:"build_info"`
	Files          []fileEntry `json:"files"`
}

type vcsInfo struct {
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

type sourceInfo struct {
	Module          string   `json:"module"`
	Version         string   `json:"version"`
	Go              string   `json:"go"`
	GoModSHA256     string   `json:"go_mod_sha256"`
	GoSumSHA256     string   `json:"go_sum_sha256"`
	VersionGoSHA256 string   `json:"version_go_sha256"`
	VCS             *vcsInfo `json:"vcs,omitempty"`
}

type manifest struct {
	Version   string     `json:"version"`
	Source    sourceInfo `json:"source"`
	Artifacts []artifact `json:"artifacts"`
}

func buildCommand(arguments []string) error {
	set := flag.NewFlagSet("build", flag.ContinueOnError)
	source := set.String("source", ".", "module root to build")
	out := set.String("out", "dist", "output directory")
	targetList := set.String("targets", "", "comma-separated GOOS/GOARCH list (default: every release target)")
	goTool := set.String("go", "go", "Go toolchain command")
	skipVerify := set.Bool("skip-verify", false, "skip the verification pass after building")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}

	root, err := moduleRoot(*source)
	if err != nil {
		return err
	}
	appVersion, err := versionFromSource(root)
	if err != nil {
		return err
	}
	targets, err := selectTargets(*targetList)
	if err != nil {
		return err
	}
	outDir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	noticesDir := filepath.Join(root, "THIRD_PARTY_NOTICES")
	readmeTemplate := filepath.Join(root, "packaging", "notices", "README.md.tmpl")
	// The checked-in notices must cover the current build inputs, not just be
	// internally consistent.
	fresh, _, err := collectNotices(*goTool, root, "./cmd/owngit")
	if err != nil {
		return err
	}
	if err := checkNotices(noticesDir, readmeTemplate, fresh); err != nil {
		return fmt.Errorf("notice tree: %w", err)
	}
	archiveReadmeTemplate := filepath.Join(root, "packaging", "archive", "README.txt.tmpl")
	if _, err := os.Stat(archiveReadmeTemplate); err != nil {
		return err
	}

	goVersion, err := goToolchainVersion(*goTool, root)
	if err != nil {
		return err
	}
	document := manifest{Version: appVersion}
	document.Source = sourceInfo{Module: "owngit", Version: appVersion, Go: goVersion}
	for _, name := range []struct {
		field  *string
		suffix string
	}{
		{&document.Source.GoModSHA256, "go.mod"},
		{&document.Source.GoSumSHA256, "go.sum"},
		{&document.Source.VersionGoSHA256, filepath.Join("internal", "version", "version.go")},
	} {
		digest, err := sha256File(filepath.Join(root, name.suffix))
		if err != nil {
			return err
		}
		*name.field = digest
	}
	if revision, modified, ok := vcsState(root); ok {
		document.Source.VCS = &vcsInfo{Revision: revision, Modified: modified}
	}

	for _, current := range targets {
		built, err := buildTarget(*goTool, root, outDir, archiveReadmeTemplate, current, appVersion)
		if err != nil {
			return fmt.Errorf("%s: %w", current, err)
		}
		document.Artifacts = append(document.Artifacts, built)
		fmt.Printf("built %s %s\n", current, built.Name)
	}

	if err := writeChecksums(outDir, document.Artifacts); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(outDir, "manifest.json"), append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", filepath.Join(outDir, "manifest.json"))

	if *skipVerify {
		return nil
	}
	return verifyDir(outDir, *goTool)
}

// buildTarget compiles one target, stages its files, and writes its archive.
func buildTarget(goTool, root, outDir, readmeTemplate string, current target, appVersion string) (artifact, error) {
	stage := filepath.Join(outDir, "stage", current.key())
	if err := os.RemoveAll(stage); err != nil {
		return artifact{}, err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return artifact{}, err
	}

	binaryPath := filepath.Join(stage, current.binary)
	environment := []string{"GOOS=" + current.goos, "GOARCH=" + current.goarch, "CGO_ENABLED=0"}
	if _, err := runGo(goTool, root, environment, "build", "-trimpath", "-buildvcs=false", "-o", binaryPath, "./cmd/owngit"); err != nil {
		return artifact{}, err
	}

	files := []stagedFile{}
	add := func(name, path string, mode int64) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		digest, err := sha256File(path)
		if err != nil {
			return err
		}
		files = append(files, stagedFile{name: name, path: path, mode: mode, size: info.Size(), sha: digest})
		return nil
	}

	if err := add(current.binary, binaryPath, 0o755); err != nil {
		return artifact{}, err
	}
	if err := add("LICENSE", filepath.Join(root, "LICENSE"), 0o644); err != nil {
		return artifact{}, err
	}
	readme, err := renderArchiveReadme(readmeTemplate, appVersion, current)
	if err != nil {
		return artifact{}, err
	}
	readmePath := filepath.Join(stage, "README.txt")
	if _, err := writeFile(readmePath, readme, 0o644); err != nil {
		return artifact{}, err
	}
	if err := add("README.txt", readmePath, 0o644); err != nil {
		return artifact{}, err
	}
	noticeFiles, err := declaredNoticeFiles(filepath.Join(root, "THIRD_PARTY_NOTICES"))
	if err != nil {
		return artifact{}, err
	}
	for _, relative := range noticeFiles {
		if err := add(filepath.ToSlash(filepath.Join("THIRD_PARTY_NOTICES", relative)), filepath.Join(root, "THIRD_PARTY_NOTICES", filepath.FromSlash(relative)), 0o644); err != nil {
			return artifact{}, err
		}
	}
	// The coding-tool skill and its guide, so an installed user can copy them
	// without cloning the source.
	//
	// These are appended with the snapshot collectResources approved. Passing
	// them back through add() would discard that checked identity and re-stat
	// and re-hash whatever the path resolves to at this later moment, so an
	// atomic replacement made in between would be packaged as if it had been
	// checked.
	resources, err := collectResources(root)
	if err != nil {
		return artifact{}, err
	}
	files = append(files, resources...)
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	archivePath := filepath.Join(outDir, current.archiveName(appVersion))
	if current.format == "zip" {
		err = writeZip(archivePath, files)
	} else {
		err = writeTarGz(archivePath, files)
	}
	if err != nil {
		return artifact{}, err
	}
	digest, err := sha256File(archivePath)
	if err != nil {
		return artifact{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return artifact{}, err
	}
	_, metadata, err := buildInfo(goTool, binaryPath)
	if err != nil {
		return artifact{}, err
	}

	built := artifact{
		Target:    current.String(),
		Name:      filepath.Base(archivePath),
		SHA256:    digest,
		Size:      info.Size(),
		BuildInfo: metadata,
	}
	// Execution is recorded only after the binary actually ran on this host.
	// A cross-compiled binary is never reported as natively executed.
	if current.goos == runtime.GOOS && current.goarch == runtime.GOARCH {
		output, err := exec.Command(binaryPath, "version").Output()
		if err != nil {
			return artifact{}, fmt.Errorf("run %s version: %w", current.binary, err)
		}
		if want := "owngit " + appVersion + "\n"; string(output) != want {
			return artifact{}, fmt.Errorf("binary reported %q, want %q", string(output), want)
		}
		built.Executed = true
		built.ExecutedOutput = string(output)
	}
	for _, file := range files {
		built.Files = append(built.Files, fileEntry{
			Path:   file.name,
			Mode:   fmt.Sprintf("%04o", file.mode),
			Size:   file.size,
			SHA256: file.sha,
		})
	}
	return built, nil
}

// renderArchiveReadme renders the short note placed inside every archive.
func renderArchiveReadme(templatePath, appVersion string, current target) ([]byte, error) {
	parsed, err := template.ParseFiles(templatePath)
	if err != nil {
		return nil, err
	}
	// The archive is not on PATH, so the note uses a relative invocation that
	// matches the target's shell.
	invocation := "./" + current.binary
	if current.goos == "windows" {
		invocation = `.\` + current.binary
	}
	var buffer strings.Builder
	data := struct {
		Version    string
		Binary     string
		Target     string
		Invocation string
	}{appVersion, current.binary, current.String(), invocation}
	if err := parsed.Execute(&buffer, data); err != nil {
		return nil, err
	}
	return []byte(buffer.String()), nil
}

// writeTarGz writes a deterministic gzip-compressed tar archive.
func writeTarGz(path string, files []stagedFile) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for _, entry := range files {
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entry.name,
			Size:     entry.size,
			Mode:     entry.mode,
			ModTime:  fixedModTime,
			Format:   tar.FormatPAX,
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if err := copyEntry(archive, entry); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return compressed.Close()
}

// writeZip writes a deterministic deflate zip archive.
func writeZip(path string, files []stagedFile) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(file)
	for _, entry := range files {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate, Modified: fixedModTime}
		header.SetMode(os.FileMode(entry.mode))
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if err := copyEntry(writer, entry); err != nil {
			return err
		}
	}
	return archive.Close()
}

// copyEntry writes one staged entry's contents.
//
// An entry that carries an approved snapshot is written from those bytes, so
// a change made after the check cannot reach the archive. The snapshot is also
// matched against the recorded size and digest, which keeps the manifest and
// the archive describing the same bytes instead of relying on a later verify
// pass to notice a difference.
func copyEntry(writer io.Writer, entry stagedFile) error {
	if entry.data == nil {
		return copyInto(writer, entry.path)
	}
	if int64(len(entry.data)) != entry.size {
		return fmt.Errorf("%s: the staged snapshot holds %d bytes but the manifest records %d", entry.name, len(entry.data), entry.size)
	}
	if digest := sha256Bytes(entry.data); digest != entry.sha {
		return fmt.Errorf("%s: the staged snapshot hashes to %s but the manifest records %s", entry.name, digest, entry.sha)
	}
	_, err := writer.Write(entry.data)
	return err
}

func copyInto(writer io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(writer, file)
	return err
}

func writeChecksums(outDir string, artifacts []artifact) error {
	lines := make([]string, 0, len(artifacts))
	for _, built := range artifacts {
		lines = append(lines, fmt.Sprintf("%s  %s", built.SHA256, built.Name))
	}
	sort.Strings(lines)
	_, err := writeFile(filepath.Join(outDir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return err
}

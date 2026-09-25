package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// bundledNoticePrefix names the notice directory for non-module material that
// no module graph lists. It holds embedded assets, adapted source, and carried
// upstream credit, which the manifest distinguishes by their recorded source.
const bundledNoticePrefix = "bundled-assets/"

// declaredNoticeInput is one non-module notice input: a notice directory, the
// repository source path it covers, and the notice files to copy. The source
// path is recorded metadata; the collector never reads it.
type declaredNoticeInput struct {
	module  string
	version string
	source  string
	files   []string
}

// bundledAssets declares the embedded assets and bundled data explicitly. Each
// entry names the notice directory, the source file, and the notice files to
// copy.
var bundledAssets = []declaredNoticeInput{
	{
		module:  bundledNoticePrefix + "pretendard",
		version: "unversioned",
		source:  "internal/webui/assets/fonts/PretendardVariable.woff2",
		files:   []string{"internal/webui/assets/fonts/PRETENDARD-LICENSE.txt"},
	},
	{
		// Language names and colors for the repository overview's Languages
		// panel, taken from lib/linguist/languages.yml.
		module:  bundledNoticePrefix + "linguist",
		version: "v9.7.0",
		source:  "internal/repository/languages.go",
		files:   []string{"internal/repository/LINGUIST-LICENSE.txt"},
	},
}

// declaredNoticeInputs returns the non-module notice inputs in a stable order.
//
// Embedded assets and bundled data are the only kinds left. The built-in review removal took the
// pinned third-party authorization flows with it, so no adapted source and no
// credit that such a reference carried is declared any more.
func declaredNoticeInputs() []declaredNoticeInput {
	return bundledAssets
}

// noticeManifest records the notice files collected from the declared build
// inputs. It is regenerated from the module graph, not from a private list.
type noticeManifest struct {
	Package string        `json:"package"`
	Go      string        `json:"go"`
	Entries []noticeEntry `json:"entries"`
}

type noticeEntry struct {
	Module  string      `json:"module"`
	Version string      `json:"version"`
	Source  string      `json:"source,omitempty"`
	Files   []fileEntry `json:"files"`
}

// noticeFilePatterns are the root-level file names that carry license terms.
var noticeFilePatterns = []string{"LICENSE*", "COPYING*", "NOTICE*", "PATENTS*"}

func noticesCommand(arguments []string) error {
	set := flag.NewFlagSet("notices", flag.ContinueOnError)
	source := set.String("source", ".", "module root to inspect")
	out := set.String("out", "", "output directory (default <source>/THIRD_PARTY_NOTICES)")
	check := set.Bool("check", false, "verify the collected notices without writing")
	pkg := set.String("package", "./cmd/owngit", "package whose dependency graph is collected")
	goTool := set.String("go", "go", "Go toolchain command")
	if err := parseFlags(set, arguments); err != nil {
		return err
	}

	root, err := moduleRoot(*source)
	if err != nil {
		return err
	}
	outDir := *out
	if outDir == "" {
		outDir = filepath.Join(root, "THIRD_PARTY_NOTICES")
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		return err
	}
	readmeTemplate := filepath.Join(root, "packaging", "notices", "README.md.tmpl")

	document, contents, err := collectNotices(*goTool, root, *pkg)
	if err != nil {
		return err
	}
	if *check {
		return checkNotices(outDir, readmeTemplate, document)
	}

	// Render the complete output into a staging directory first, so the
	// destination is only touched once the whole generation exists.
	stage, err := os.MkdirTemp("", "owngit-notices-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for relative, data := range contents {
		if _, err := writeFile(filepath.Join(stage, filepath.FromSlash(relative)), data, 0o644); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(stage, "manifest.json"), append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	readme, err := renderNoticesReadme(readmeTemplate, document)
	if err != nil {
		return err
	}
	if _, err := writeFile(filepath.Join(stage, "README.md"), readme, 0o644); err != nil {
		return err
	}

	if err := requireFreshDestination(outDir); err != nil {
		return err
	}
	if err := copyTree(stage, outDir); err != nil {
		return err
	}
	fmt.Printf("collected %d notice sets into %s\n", len(document.Entries), outDir)
	return nil
}

// requireFreshDestination refuses a nonempty destination without changing it.
// Nothing is ever deleted, so a colliding directory or a poisoned manifest in
// the destination cannot be mistaken for something the tool owns.
func requireFreshDestination(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	// The advice must survive a copy and paste, so the paths are quoted when
	// they hold a space or a shell metacharacter, and the suggested destination
	// is derived from the refused one instead of a fixed shared name.
	destination := shellQuote(dir)
	suggestion := shellQuote(dir + ".new")
	return fmt.Errorf(`%s is not empty; generate into a newly chosen empty destination, inspect the difference, then update only the notice files you intend to change:
  go run ./tools/release notices -out %s
  diff -r %s %s`, destination, suggestion, destination, suggestion)
}

// shellQuote renders a path so a printed command can be pasted into a shell. A
// plain path is left alone; anything else is single quoted.
func shellQuote(path string) string {
	const plain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-"
	for _, r := range path {
		if !strings.ContainsRune(plain, r) {
			return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
		}
	}
	return path
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = writeFile(target, data, 0o644)
		return err
	})
}

// noticesReadmeData is the template input for the generated notice index.
type noticesReadmeData struct {
	Package string
	Go      string
	Entries []noticesReadmeEntry
}

type noticesReadmeEntry struct {
	Module  string
	Version string
	Source  string
	Files   string
}

func renderNoticesReadme(templatePath string, document noticeManifest) ([]byte, error) {
	parsed, err := template.ParseFiles(templatePath)
	if err != nil {
		return nil, err
	}
	data := noticesReadmeData{Package: document.Package, Go: document.Go}
	for _, entry := range document.Entries {
		names := make([]string, 0, len(entry.Files))
		for _, file := range entry.Files {
			names = append(names, file.Path)
		}
		data.Entries = append(data.Entries, noticesReadmeEntry{
			Module: entry.Module, Version: entry.Version, Source: entry.Source,
			Files: strings.Join(names, ", "),
		})
	}
	var buffer strings.Builder
	if err := parsed.Execute(&buffer, data); err != nil {
		return nil, err
	}
	return []byte(buffer.String()), nil
}

// collectNotices walks the dependency graph of every release target and reads
// the license files of each linked module, each declared bundled asset, and
// the Go runtime.
func collectNotices(goTool, root, pkg string) (noticeManifest, map[string][]byte, error) {
	goVersion, err := goToolchainVersion(goTool, root)
	if err != nil {
		return noticeManifest{}, nil, err
	}
	modules, err := linkedModules(goTool, root, pkg)
	if err != nil {
		return noticeManifest{}, nil, err
	}

	document := noticeManifest{Package: pkg, Go: goVersion}
	contents := map[string][]byte{}
	for _, module := range modules {
		path, moduleVersion := splitModule(module)
		dir, err := moduleDir(goTool, root, module)
		if err != nil {
			return noticeManifest{}, nil, err
		}
		entry, files, err := readNotices(path, dir)
		if err != nil {
			return noticeManifest{}, nil, err
		}
		entry.Version = moduleVersion
		document.Entries = append(document.Entries, entry)
		for name, data := range files {
			contents[filepath.ToSlash(filepath.Join(path, name))] = data
		}
	}

	for _, input := range declaredNoticeInputs() {
		entry, files, err := readDeclaredNoticeInput(root, input)
		if err != nil {
			return noticeManifest{}, nil, err
		}
		document.Entries = append(document.Entries, entry)
		for name, data := range files {
			contents[filepath.ToSlash(filepath.Join(input.module, name))] = data
		}
	}

	runtimeFiles, err := goRuntimeNotices(goTool, root)
	if err != nil {
		return noticeManifest{}, nil, err
	}
	entry, err := noticeEntryFor("go-runtime", runtimeFiles)
	if err != nil {
		return noticeManifest{}, nil, err
	}
	entry.Version = goVersion
	document.Entries = append(document.Entries, entry)
	for name, data := range runtimeFiles {
		contents[filepath.ToSlash(filepath.Join("go-runtime", name))] = data
	}
	return document, contents, nil
}

// readDeclaredNoticeInput copies one declared non-module notice input from the
// repository root and records its file digests. The declared source path is
// recorded as metadata; it is not read or validated here.
func readDeclaredNoticeInput(root string, input declaredNoticeInput) (noticeEntry, map[string][]byte, error) {
	files := map[string][]byte{}
	for _, path := range input.files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return noticeEntry{}, nil, err
		}
		files[filepath.Base(path)] = data
	}
	entry, err := noticeEntryFor(input.module, files)
	if err != nil {
		return noticeEntry{}, nil, err
	}
	entry.Version = input.version
	entry.Source = input.source
	return entry, files, nil
}

// linkedModules returns the sorted union of module@version pairs compiled into
// the package for every release target.
func linkedModules(goTool, root, pkg string) ([]string, error) {
	seen := map[string]bool{}
	for _, current := range releaseTargets {
		environment := []string{"GOOS=" + current.goos, "GOARCH=" + current.goarch, "CGO_ENABLED=0"}
		output, err := runGo(goTool, root, environment, "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}@{{.Module.Version}}{{end}}", pkg)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasSuffix(line, "@") {
				continue
			}
			seen[line] = true
		}
	}
	modules := make([]string, 0, len(seen))
	for module := range seen {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	return modules, nil
}

func moduleDir(goTool, root, module string) (string, error) {
	output, err := runGo(goTool, root, nil, "list", "-m", "-f", "{{.Dir}}", module)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(output)
	if dir == "" {
		return "", fmt.Errorf("module %s has no source directory in the module cache", module)
	}
	return dir, nil
}

// readNotices reads the root-level license files of one module directory.
func readNotices(module, dir string) (noticeEntry, map[string][]byte, error) {
	names, err := noticeFileNames(dir)
	if err != nil {
		return noticeEntry{}, nil, err
	}
	if len(names) == 0 {
		return noticeEntry{}, nil, fmt.Errorf("module %s has no license file in %s", module, dir)
	}
	files := map[string][]byte{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return noticeEntry{}, nil, err
		}
		files[name] = data
	}
	entry, err := noticeEntryFor(module, files)
	return entry, files, err
}

// noticeEntryFor turns collected file contents into a manifest entry.
func noticeEntryFor(module string, files map[string][]byte) (noticeEntry, error) {
	if len(files) == 0 {
		return noticeEntry{}, fmt.Errorf("module %s has no license file", module)
	}
	entry := noticeEntry{Module: module}
	for name, data := range files {
		entry.Files = append(entry.Files, fileEntry{
			Path:   name,
			Mode:   "0644",
			Size:   int64(len(data)),
			SHA256: sha256Bytes(data),
		})
	}
	sort.Slice(entry.Files, func(i, j int) bool { return entry.Files[i].Path < entry.Files[j].Path })
	return entry, nil
}

// splitModule separates a module@version pair.
func splitModule(module string) (string, string) {
	if at := strings.LastIndex(module, "@"); at >= 0 {
		return module[:at], module[at+1:]
	}
	return module, ""
}

func noticeFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		for _, pattern := range noticeFilePatterns {
			if matched, err := filepath.Match(pattern, entry.Name()); err == nil && matched {
				names = append(names, entry.Name())
				break
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// goRuntimeRequiredNotices are the exact notice files a Go distribution must
// carry.
var goRuntimeRequiredNotices = []string{"LICENSE", "PATENTS"}

func goRuntimeNotices(goTool, root string) (map[string][]byte, error) {
	output, err := runGo(goTool, root, nil, "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	return goRuntimeNoticesFrom(strings.TrimSpace(output))
}

// goRuntimeNoticesFrom reads the Go runtime notices from a GOROOT. Each exact
// file found in GOROOT is authoritative. A missing required file may come from
// the parent only after that directory's exact LICENSE is validated as the Go
// license. Parent directories are never globbed, and every required file must
// be found in an authoritative or validated source.
func goRuntimeNoticesFrom(goroot string) (map[string][]byte, error) {
	files, err := readNoticeFiles(goroot)
	if err != nil {
		return nil, err
	}
	if license, ok := files["LICENSE"]; ok {
		if err := requireGoLicense(goroot, license); err != nil {
			return nil, err
		}
	}

	parent := filepath.Dir(goroot)
	for _, name := range goRuntimeRequiredNotices {
		if _, ok := files[name]; ok {
			continue
		}
		data, err := readNoticeFile(parent, name)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Go runtime %s in %s or %s", name, goroot, parent)
		}
		if err != nil {
			return nil, err
		}

		parentLicense := data
		if name != "LICENSE" {
			parentLicense, err = readNoticeFile(parent, "LICENSE")
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("cannot use %s/%s: the exact parent LICENSE %s/LICENSE is missing", parent, name, parent)
			}
			if err != nil {
				return nil, err
			}
		}
		if err := requireGoLicense(parent, parentLicense); err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

// readNoticeFile reads one exact notice file, so a caller can avoid globbing a
// directory that holds unrelated notice files.
func readNoticeFile(dir, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, name))
}

func readNoticeFiles(dir string) (map[string][]byte, error) {
	names, err := noticeFileNames(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

// requireGoLicense guards the fallback layout, where the parent directory of
// GOROOT could otherwise contribute unrelated host material.
func requireGoLicense(dir string, license []byte) error {
	if !strings.Contains(string(license), "The Go Authors") {
		return fmt.Errorf("%s/LICENSE does not look like the Go license", dir)
	}
	return nil
}

func readNoticeManifest(path string) (noticeManifest, error) {
	var document noticeManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return document, err
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return document, fmt.Errorf("%s: %w", path, err)
	}
	return document, nil
}

// declaredNoticeFiles returns the exact notice file set declared by the
// checked-in manifest and rejects anything else in the tree. The tree must
// hold real directories and regular files, so a symlinked component cannot
// silently change what is packaged.
func declaredNoticeFiles(dir string) ([]string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a real directory", dir)
	}
	document, err := readNoticeManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{"README.md": true, "manifest.json": true}
	for _, entry := range document.Entries {
		if err := validateRelativePath(entry.Module); err != nil {
			return nil, fmt.Errorf("notice module %q: %w", entry.Module, err)
		}
		for _, file := range entry.Files {
			if err := validateRelativePath(file.Path); err != nil {
				return nil, fmt.Errorf("notice file %q: %w", file.Path, err)
			}
			declared[entry.Module+"/"+file.Path] = true
		}
	}

	found := map[string]bool{}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; the notice tree must hold real files", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		if !declared[relative] {
			return fmt.Errorf("%s is not declared in %s", path, filepath.Join(dir, "manifest.json"))
		}
		found[relative] = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	var missing []string
	for name := range declared {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("declared notice files are missing: %s", strings.Join(missing, ", "))
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// validateRelativePath rejects absolute paths, backslashes, and unsafe
// components so an archive entry cannot escape its own root. The rules are
// platform independent, so a Windows zip is judged the same way on macOS.
func validateRelativePath(name string) error {
	if name == "" {
		return fmt.Errorf("empty path")
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("%q must use forward slashes", name)
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("%q is absolute", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		if !isASCIILetter(name[0]) {
			return fmt.Errorf("%q has an invalid drive letter", name)
		}
		return fmt.Errorf("%q carries a drive prefix", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%q has an unsafe path component", name)
		}
	}
	return nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// checkNotices compares the checked-in notices with a fresh collection. The
// whole recorded document, every file digest, and the generated README are
// compared, so a dependency update cannot leave a stale notice behind.
func checkNotices(outDir, readmeTemplate string, document noticeManifest) error {
	if _, err := declaredNoticeFiles(outDir); err != nil {
		return err
	}
	recorded, err := readNoticeManifest(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		return err
	}
	if recorded.Package != document.Package {
		return fmt.Errorf("notice manifest package is %q, want %q", recorded.Package, document.Package)
	}
	if recorded.Go != document.Go {
		return fmt.Errorf("notice manifest Go version is %q, want %q", recorded.Go, document.Go)
	}
	if len(recorded.Entries) != len(document.Entries) {
		return fmt.Errorf("notices list %d entries but the build inputs need %d", len(recorded.Entries), len(document.Entries))
	}
	for index, entry := range document.Entries {
		other := recorded.Entries[index]
		if entry.Module != other.Module || entry.Version != other.Version || entry.Source != other.Source {
			return fmt.Errorf("notice entry %d is %s@%s but the build inputs need %s@%s", index, other.Module, other.Version, entry.Module, entry.Version)
		}
		if len(entry.Files) != len(other.Files) {
			return fmt.Errorf("%s lists %d notice files but %d are needed", entry.Module, len(other.Files), len(entry.Files))
		}
		for fileIndex, file := range entry.Files {
			otherFile := other.Files[fileIndex]
			if file.Path != otherFile.Path {
				return fmt.Errorf("%s notice file %d is %s but %s is needed", entry.Module, fileIndex, otherFile.Path, file.Path)
			}
			if file.Mode != otherFile.Mode || file.Size != otherFile.Size || file.SHA256 != otherFile.SHA256 {
				return fmt.Errorf("%s/%s records mode %s size %d digest %s but the build inputs need mode %s size %d digest %s",
					entry.Module, file.Path, otherFile.Mode, otherFile.Size, otherFile.SHA256, file.Mode, file.Size, file.SHA256)
			}
			path := filepath.Join(outDir, filepath.FromSlash(entry.Module), file.Path)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", path)
			}
			if info.Size() != file.Size {
				return fmt.Errorf("%s is %d bytes but the build inputs need %d", path, info.Size(), file.Size)
			}
			digest, err := sha256File(path)
			if err != nil {
				return err
			}
			if digest != file.SHA256 {
				return fmt.Errorf("%s is stale: %s does not match the module cache", path, file.Path)
			}
		}
	}

	readme, err := renderNoticesReadme(readmeTemplate, document)
	if err != nil {
		return err
	}
	onDisk, err := os.ReadFile(filepath.Join(outDir, "README.md"))
	if err != nil {
		return err
	}
	if string(onDisk) != string(readme) {
		return fmt.Errorf("%s is stale: it does not match %s", filepath.Join(outDir, "README.md"), readmeTemplate)
	}
	return nil
}

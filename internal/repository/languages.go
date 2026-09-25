package repository

import (
	"bytes"
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"owngit/internal/gitexec"
)

// Language is one language the repository overview counts: its display name,
// its color, and the file extensions and exact file names that identify it.
// The names and colors come from GitHub Linguist v9.7.0
// (lib/linguist/languages.yml, MIT License; see THIRD_PARTY_NOTICES under
// bundled-assets/linguist). Only programming and markup languages are listed,
// plus SQL. Data and prose formats (JSON, YAML, TOML, XML, Markdown, plain
// text, CSV) are not languages here. An extension that several languages
// share is counted as its most common one: .h is C and .m is Objective-C.
type Language struct {
	Name       string
	Color      string
	Extensions []string
	FileNames  []string
}

var languageTable = []Language{
	{Name: "Assembly", Color: "#6e4c13", Extensions: []string{".asm", ".nasm", ".s"}},
	{Name: "Astro", Color: "#ff5a03", Extensions: []string{".astro"}},
	{Name: "Batchfile", Color: "#c1f12e", Extensions: []string{".bat", ".cmd"}},
	{Name: "C", Color: "#555555", Extensions: []string{".c", ".h"}},
	{Name: "C#", Color: "#7355dd", Extensions: []string{".cs", ".csx"}},
	{Name: "C++", Color: "#f34b7d", Extensions: []string{".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx"}},
	{Name: "Clojure", Color: "#db5855", Extensions: []string{".clj", ".cljs", ".cljc"}},
	{Name: "CMake", Color: "#da3434", Extensions: []string{".cmake"}, FileNames: []string{"CMakeLists.txt"}},
	{Name: "CSS", Color: "#663399", Extensions: []string{".css"}},
	{Name: "Dart", Color: "#00b4ab", Extensions: []string{".dart"}},
	{Name: "Dockerfile", Color: "#384d54", Extensions: []string{".dockerfile", ".containerfile"}, FileNames: []string{"Dockerfile", "Containerfile"}},
	{Name: "Elixir", Color: "#6e4a7e", Extensions: []string{".ex", ".exs"}},
	{Name: "Elm", Color: "#60b5cc", Extensions: []string{".elm"}},
	{Name: "Erlang", Color: "#b83998", Extensions: []string{".erl", ".hrl"}},
	{Name: "F#", Color: "#b845fc", Extensions: []string{".fs", ".fsi", ".fsx"}},
	{Name: "GLSL", Color: "#5686a5", Extensions: []string{".glsl", ".vert", ".frag"}},
	{Name: "Go", Color: "#00add8", Extensions: []string{".go"}},
	{Name: "Groovy", Color: "#4298b8", Extensions: []string{".groovy"}, FileNames: []string{"Jenkinsfile"}},
	{Name: "Haskell", Color: "#5e5086", Extensions: []string{".hs"}},
	{Name: "HCL", Color: "#844fba", Extensions: []string{".hcl", ".tf", ".tfvars"}},
	{Name: "HTML", Color: "#e34c26", Extensions: []string{".html", ".htm", ".xhtml"}},
	{Name: "Java", Color: "#b07219", Extensions: []string{".java"}},
	{Name: "JavaScript", Color: "#f1e05a", Extensions: []string{".js", ".cjs", ".mjs", ".jsx"}, FileNames: []string{"Jakefile"}},
	{Name: "Julia", Color: "#a270ba", Extensions: []string{".jl"}},
	{Name: "Kotlin", Color: "#a97bff", Extensions: []string{".kt", ".kts"}},
	{Name: "Less", Color: "#1d365d", Extensions: []string{".less"}},
	{Name: "Lua", Color: "#000080", Extensions: []string{".lua"}},
	{Name: "Makefile", Color: "#427819", Extensions: []string{".mk", ".mak"}, FileNames: []string{"Makefile", "GNUmakefile", "BSDmakefile", "makefile"}},
	{Name: "Nix", Color: "#7e7eff", Extensions: []string{".nix"}},
	{Name: "Objective-C", Color: "#438eff", Extensions: []string{".m"}},
	{Name: "Objective-C++", Color: "#6866fb", Extensions: []string{".mm"}},
	{Name: "OCaml", Color: "#ef7a08", Extensions: []string{".ml", ".mli"}},
	{Name: "Perl", Color: "#0298c3", Extensions: []string{".pl", ".pm"}},
	{Name: "PHP", Color: "#4f5d95", Extensions: []string{".php"}},
	{Name: "PowerShell", Color: "#012456", Extensions: []string{".ps1", ".psm1", ".psd1"}},
	{Name: "Python", Color: "#3572a5", Extensions: []string{".py", ".pyi", ".pyw"}, FileNames: []string{"SConstruct", "SConscript"}},
	{Name: "R", Color: "#198ce7", Extensions: []string{".r"}},
	{Name: "Ruby", Color: "#701516", Extensions: []string{".rb", ".rake", ".gemspec"}, FileNames: []string{"Rakefile", "Gemfile", "Podfile", "Brewfile", "Fastfile"}},
	{Name: "Rust", Color: "#dea584", Extensions: []string{".rs"}},
	{Name: "Sass", Color: "#a53b70", Extensions: []string{".sass"}},
	{Name: "Scala", Color: "#c22d40", Extensions: []string{".scala", ".sbt"}},
	{Name: "SCSS", Color: "#c6538c", Extensions: []string{".scss"}},
	{Name: "Shell", Color: "#89e051", Extensions: []string{".sh", ".bash", ".zsh", ".ksh", ".bats"}, FileNames: []string{".bashrc", ".bash_profile", ".zshrc", ".profile"}},
	{Name: "SQL", Color: "#e38c00", Extensions: []string{".sql"}},
	{Name: "Starlark", Color: "#76d275", Extensions: []string{".bzl", ".star"}, FileNames: []string{"BUILD", "BUILD.bazel", "WORKSPACE", "WORKSPACE.bazel", "MODULE.bazel"}},
	{Name: "Svelte", Color: "#ff3e00", Extensions: []string{".svelte"}},
	{Name: "Swift", Color: "#f05138", Extensions: []string{".swift"}},
	{Name: "TSX", Color: "#3178c6", Extensions: []string{".tsx"}},
	{Name: "TypeScript", Color: "#3178c6", Extensions: []string{".ts", ".cts", ".mts"}},
	{Name: "Vue", Color: "#41b883", Extensions: []string{".vue"}},
	{Name: "Zig", Color: "#ec915c", Extensions: []string{".zig"}},
}

var (
	languageByExtension = map[string]*Language{}
	languageByFileName  = map[string]*Language{}
	languageByName      = map[string]*Language{}
)

func init() {
	for index := range languageTable {
		language := &languageTable[index]
		languageByName[strings.ToLower(language.Name)] = language
		for _, extension := range language.Extensions {
			languageByExtension[extension] = language
		}
		for _, name := range language.FileNames {
			languageByFileName[name] = language
		}
	}
}

// LanguageColor returns the color of a counted language, or "" for a name
// the table does not list.
func LanguageColor(name string) string {
	if language, ok := languageByName[strings.ToLower(name)]; ok {
		return language.Color
	}
	return ""
}

// LanguageShare is the size in bytes of one language's files.
type LanguageShare struct {
	Name  string
	Bytes int64
}

// LanguageStats is the language make-up of one commit. Shares are sorted
// by size, largest first. TooLarge means the commit has more files than one
// count reads, and TimedOut means the count ran past its time limit; in both
// cases Shares is empty rather than partial. Attributes says whether the
// Linguist attributes of the commit's .gitattributes files were applied.
type LanguageStats struct {
	Shares     []LanguageShare
	TooLarge   bool
	TimedOut   bool
	Attributes AttributeState
}

// AttributeState says what happened to the Linguist attributes of a count.
type AttributeState string

const (
	// AttributesApplied: applied, or the tree has no .gitattributes file.
	AttributesApplied AttributeState = ""
	// AttributesUnsupported: the host Git cannot read attributes from a
	// commit (it needs Git 2.40 or newer).
	AttributesUnsupported AttributeState = "unsupported"
	// AttributesUnreadable: Git could not read them for another reason.
	AttributesUnreadable AttributeState = "unreadable"
)

// Bounds for one language count. Variables so tests can lower them.
// languageTimeoutRetry is how long a count that ran past languageTimeLimit
// is remembered before it is tried again, so a slow storage folder does not
// pay the time limit on every overview.
var (
	languageEntryLimit         = 200_000
	languageTimeLimit          = 2 * time.Second
	languageOutputLimit  int64 = 64 << 20
	languageTimeoutRetry       = 2 * time.Minute
	languageNow                = time.Now
	// languageLockWait is how long a count waits for an operation that holds
	// the repository before the panel says it will be counted later.
	languageLockWait = 250 * time.Millisecond
)

// languageAttributes are the Linguist attributes Git may report for a path.
var languageAttributes = []string{"linguist-language", "linguist-vendored", "linguist-generated", "linguist-documentation"}

// Languages returns the language make-up of commitOID, counted from blob
// sizes in its tree without a checkout. A commit's contents never change, so
// the result is cached per repository by commit ID; only the newest counted
// commit of each repository is kept. A count that ran past its time limit is
// kept for languageTimeoutRetry. Errors, including a count stopped because
// the request ended, are not cached.
func (m *Manager) Languages(ctx context.Context, id, commitOID string) (LanguageStats, error) {
	if !isOID(commitOID) {
		return LanguageStats{}, errors.New("invalid commit ID")
	}
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return LanguageStats{}, err
	}
	if stats, ok := m.languages.lookup(id, repositoryPath, commitOID); ok {
		return stats, nil
	}
	// The count is a side panel of the overview. It waits for the repository
	// only briefly, and never past the request, so an operation holding the
	// repository cannot hold the page (QA-058). A count that could not start
	// is not cached and is tried on the next visit.
	lock := m.Locks.For(id)
	waitCtx, stopWaiting := context.WithTimeout(ctx, languageLockWait)
	err = readLock(waitCtx, lock)
	stopWaiting()
	if err != nil {
		return LanguageStats{}, err
	}
	defer lock.RUnlock()
	limited, cancel := context.WithTimeout(ctx, languageTimeLimit)
	defer cancel()
	stats, err := m.countLanguages(limited, repositoryPath, commitOID)
	if err != nil {
		// Only the count's own limit is a result worth remembering. The
		// request ending first, or Git failing, is tried again next time.
		if ctx.Err() != nil || !errors.Is(limited.Err(), context.DeadlineExceeded) {
			return LanguageStats{}, err
		}
		stats = LanguageStats{TimedOut: true}
	}
	m.languages.store(id, repositoryPath, commitOID, stats)
	return stats.clone(), nil
}

type languageFile struct {
	path string
	size int64
}

func (m *Manager) countLanguages(ctx context.Context, repositoryPath, commitOID string) (LanguageStats, error) {
	limits := gitexec.CommandLimits{Timeout: languageTimeLimit, OutputLimit: languageOutputLimit}
	listing, err := m.Git.RunWithLimits(ctx, repositoryPath, nil, limits, "--git-dir", ".", "ls-tree", "-r", "-l", "-z", commitOID)
	var limitErr *gitexec.LimitError
	if errors.As(err, &limitErr) {
		return LanguageStats{TooLarge: true}, nil
	}
	if err != nil {
		return LanguageStats{}, err
	}
	var files []languageFile
	hasAttributes := false
	entries := 0
	for _, record := range bytes.Split(listing.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if entries++; entries > languageEntryLimit {
			return LanguageStats{TooLarge: true}, nil
		}
		entry, err := parseTreeEntry(record)
		if err != nil {
			return LanguageStats{}, err
		}
		// Symbolic links (120000) and submodules (commit entries) name no
		// content of this repository.
		if entry.Type != "blob" || entry.Mode == "120000" || entry.Size < 0 {
			continue
		}
		if path.Base(entry.Name) == ".gitattributes" {
			hasAttributes = true
		}
		files = append(files, languageFile{path: entry.Name, size: entry.Size})
	}
	var stats LanguageStats
	var attributes map[string]map[string]string
	if hasAttributes && len(files) > 0 {
		attributes, err = m.readLanguageAttributes(ctx, repositoryPath, commitOID, files)
		switch {
		case ctx.Err() != nil:
			return LanguageStats{}, ctx.Err()
		case errors.As(err, &limitErr):
			return LanguageStats{TooLarge: true}, nil
		case err != nil && unsupportedAttributeSource(err):
			stats.Attributes = AttributesUnsupported
		case err != nil:
			stats.Attributes = AttributesUnreadable
		}
	}
	totals := map[string]int64{}
	for _, file := range files {
		if name, ok := classifyLanguage(file.path, attributes[file.path]); ok {
			totals[name] += file.size
		}
	}
	for name, size := range totals {
		stats.Shares = append(stats.Shares, LanguageShare{Name: name, Bytes: size})
	}
	slices.SortFunc(stats.Shares, func(a, b LanguageShare) int {
		if a.Bytes != b.Bytes {
			if a.Bytes > b.Bytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return stats, nil
}

// readLanguageAttributes asks one Git process for the Linguist attributes
// of every file, as the commit's .gitattributes files set them.
func (m *Manager) readLanguageAttributes(ctx context.Context, repositoryPath, commitOID string, files []languageFile) (map[string]map[string]string, error) {
	var input bytes.Buffer
	for _, file := range files {
		input.WriteString(file.path)
		input.WriteByte(0)
	}
	args := append([]string{"--git-dir", ".", "check-attr", "--source", commitOID, "-z", "--stdin"}, languageAttributes...)
	limits := gitexec.CommandLimits{Timeout: languageTimeLimit, OutputLimit: languageOutputLimit}
	result, err := m.Git.RunWithLimits(ctx, repositoryPath, &input, limits, args...)
	if err != nil {
		return nil, err
	}
	// Each answer is path, attribute and value, each ended by NUL.
	fields := strings.Split(string(result.Stdout), "\x00")
	attributes := map[string]map[string]string{}
	for index := 0; index+2 < len(fields); index += 3 {
		value := fields[index+2]
		if value == "unspecified" {
			continue
		}
		file := fields[index]
		if attributes[file] == nil {
			attributes[file] = map[string]string{}
		}
		attributes[file][fields[index+1]] = value
	}
	return attributes, nil
}

// unsupportedAttributeSource reports whether check-attr failed because the
// host Git does not know --source (added in Git 2.40). Such a Git answers
// with its usage and "unknown option `source'".
func unsupportedAttributeSource(err error) bool {
	message := err.Error()
	return strings.Contains(message, "unknown option") && strings.Contains(message, "source")
}

// Paths that GitHub Linguist treats as not written by the project. The
// Linguist attributes override each group per file.
var (
	vendoredDirectories = map[string]bool{"vendor": true, "node_modules": true, "third_party": true, "bower_components": true, "dist": true}
	lockFiles           = map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "Cargo.lock": true, "Gemfile.lock": true,
		"composer.lock": true, "poetry.lock": true, "go.sum": true, "Package.resolved": true, "Podfile.lock": true,
	}
	generatedSuffixes = []string{".min.js", ".min.css", ".pb.go", "_pb2.py"}
)

// classifyLanguage names the language of one file, or reports false for a
// file that is not counted: vendored, generated, documentation, or of no
// listed language.
func classifyLanguage(filePath string, attributes map[string]string) (string, bool) {
	directories := strings.Split(filePath, "/")
	name := directories[len(directories)-1]
	directories = directories[:len(directories)-1]

	vendored := false
	for _, directory := range directories {
		if vendoredDirectories[directory] {
			vendored = true
		}
	}
	generated := lockFiles[name]
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(name, suffix) {
			generated = true
		}
	}
	documentation := false
	if len(directories) > 0 {
		switch directories[0] {
		case "doc", "Doc", "docs", "Docs":
			documentation = true
		}
	}
	for _, directory := range directories {
		if strings.EqualFold(directory, "documentation") {
			documentation = true
		}
	}
	attributeOverride(attributes["linguist-vendored"], &vendored)
	attributeOverride(attributes["linguist-generated"], &generated)
	attributeOverride(attributes["linguist-documentation"], &documentation)
	if vendored || generated || documentation {
		return "", false
	}
	if chosen := attributes["linguist-language"]; chosen != "" && chosen != "set" && chosen != "unset" {
		// A name the table does not list has no color; the file is not counted.
		language, ok := languageByName[strings.ToLower(strings.ReplaceAll(chosen, "-", " "))]
		if !ok {
			language, ok = languageByName[strings.ToLower(chosen)]
		}
		if !ok {
			return "", false
		}
		return language.Name, true
	}
	if language, ok := languageByFileName[name]; ok {
		return language.Name, true
	}
	if strings.HasPrefix(name, "Dockerfile.") || strings.HasPrefix(name, "Containerfile.") {
		return "Dockerfile", true
	}
	if extension := strings.ToLower(path.Ext(name)); extension != "" && extension != name {
		if language, ok := languageByExtension[extension]; ok {
			return language.Name, true
		}
	}
	return "", false
}

// attributeOverride applies a Linguist boolean attribute: set or "true"
// makes it true, unset or "false" makes it false, and anything else keeps
// the path rule.
func attributeOverride(value string, target *bool) {
	switch value {
	case "set", "true":
		*target = true
	case "unset", "false":
		*target = false
	}
}

func (stats LanguageStats) clone() LanguageStats {
	stats.Shares = slices.Clone(stats.Shares)
	return stats
}

// languageCache keeps the newest language count of each repository, keyed
// by the repository's storage path and the counted commit.
type languageCache struct {
	mu      sync.Mutex
	entries map[string]languageEntry
}

type languageEntry struct {
	path, commit string
	stats        LanguageStats
	// retryAt is set for a count that ran past its time limit; the entry is
	// not used from then on.
	retryAt time.Time
}

func (cache *languageCache) lookup(id, repositoryPath, commit string) (LanguageStats, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.path != repositoryPath || entry.commit != commit {
		return LanguageStats{}, false
	}
	if !entry.retryAt.IsZero() && !languageNow().Before(entry.retryAt) {
		delete(cache.entries, id)
		return LanguageStats{}, false
	}
	return entry.stats.clone(), true
}

func (cache *languageCache) store(id, repositoryPath, commit string, stats LanguageStats) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]languageEntry)
	}
	entry := languageEntry{path: repositoryPath, commit: commit, stats: stats.clone()}
	if stats.TimedOut {
		entry.retryAt = languageNow().Add(languageTimeoutRetry)
	}
	cache.entries[id] = entry
}

func (cache *languageCache) forget(present []string) {
	keep := make(map[string]bool, len(present))
	for _, id := range present {
		keep[id] = true
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for id := range cache.entries {
		if !keep[id] {
			delete(cache.entries, id)
		}
	}
}

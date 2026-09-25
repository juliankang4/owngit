package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeLanguageFixture writes files of the given sizes into the work tree, adds index
// entries for a symbolic link and a submodule, commits, pushes main, and
// returns the pushed commit.
func writeLanguageFixture(t *testing.T, work, remote string, files map[string]int) string {
	t.Helper()
	for name, size := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(full), 0o700))
		noErr(t, os.WriteFile(full, []byte(strings.Repeat("x", size)), 0o600))
	}
	runGit(t, work, "add", "-A")
	// A link named like Go source and a submodule named like Go source: both
	// name no content of this repository and are never counted.
	blob := hashObject(t, work, strings.Repeat("y", 5000))
	runGit(t, work, "update-index", "--add", "--cacheinfo", "120000,"+blob+",link.go")
	runGit(t, work, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",module.go")
	commitFile(t, work, "fixture", "fixture", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	return gitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
}

func hashObject(t *testing.T, work, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blob")
	noErr(t, os.WriteFile(path, []byte(content), 0o600))
	return gitOutput(t, work, "hash-object", "-w", path)
}

var languageFixture = map[string]int{
	"main.go":                    1000,
	"web/index.html":             300,
	"web/site.css":               100,
	"web/app.min.js":             5000,
	"web/theme.min.css":          5000,
	"vendor/lib/x.go":            9000,
	"vendor/keep/k.go":           123,
	"node_modules/a/index.js":    9000,
	"third_party/c/y.c":          9000,
	"dist/bundle.js":             9000,
	"docs/guide.html":            7000,
	"docs/api.ts":                250,
	"api/service.pb.go":          9000,
	"gen/model.go":               4000,
	"lib.inc":                    400,
	"data.json":                  9000,
	"config.yaml":                9000,
	"README.md":                  9000,
	"notes.txt":                  9000,
	"table.csv":                  9000,
	"package-lock.json":          9000,
	"yarn.lock":                  9000,
	"Makefile":                   200,
	"Dockerfile.dev":             50,
	"scripts/run.sh":             150,
	"shapes/unknown.language.zz": 9000,
}

// TestLanguagesCountsListedLanguagesOnly counts blob sizes per language on
// the default branch commit, skipping data and prose formats, symbolic links,
// submodules, and vendored, generated, and documentation paths, and applying
// the commit's Linguist attributes.
func TestLanguagesCountsListedLanguagesOnly(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	files := map[string]int{}
	for name, size := range languageFixture {
		files[name] = size
	}
	attributes := "gen/*.go linguist-generated\n*.inc linguist-language=PHP\nvendor/keep/** linguist-vendored=false\ndocs/api.ts -linguist-documentation\n"
	noErr(t, os.WriteFile(filepath.Join(work, ".gitattributes"), []byte(attributes), 0o600))
	commit := writeLanguageFixture(t, work, remote, files)

	stats, err := manager.Languages(context.Background(), "sample", commit)
	noErr(t, err)
	if stats.TooLarge {
		t.Fatal("a small commit is reported too large")
	}
	want := []LanguageShare{
		{"Go", 1123}, {"PHP", 400}, {"HTML", 300}, {"TypeScript", 250},
		{"Makefile", 200}, {"Shell", 150}, {"CSS", 100}, {"Dockerfile", 50},
	}
	if stats.AttributesIgnored {
		// Git before 2.40 cannot read attributes from a commit, so only the
		// path rules apply.
		want = []LanguageShare{
			{"Go", 5000}, {"HTML", 300}, {"Makefile", 200}, {"Shell", 150}, {"CSS", 100}, {"Dockerfile", 50},
		}
		t.Log("this Git cannot read attributes from a commit; checked the path rules only")
	}
	if !reflect.DeepEqual(stats.Shares, want) {
		t.Fatalf("shares = %v, want %v", stats.Shares, want)
	}
}

func TestClassifyLanguage(t *testing.T) {
	for _, test := range []struct {
		path, want string
	}{
		{"cmd/main.go", "Go"}, {"src/App.TSX", "TSX"}, {"a/b.h", "C"}, {"CMakeLists.txt", "CMake"},
		{"sub/Makefile", "Makefile"}, {"Containerfile.prod", "Dockerfile"}, {".bashrc", "Shell"},
		{"x.json", ""}, {"x.yaml", ""}, {"x.md", ""}, {"x.txt", ""}, {"x.xml", ""}, {"x.toml", ""}, {"x.csv", ""},
		{"lib/vendor/a.go", ""}, {"Documentation/x.c", ""}, {"docs/x.js", ""}, {"src/docs/x.js", "JavaScript"},
		{"a.min.js", ""}, {"Cargo.lock", ""}, {"noextension", ""}, {".go", ""},
	} {
		got, ok := classifyLanguage(test.path, nil)
		if got != test.want || ok != (test.want != "") {
			t.Errorf("%s: got %q %v, want %q", test.path, got, ok, test.want)
		}
	}
	if got, ok := classifyLanguage("x.inc", map[string]string{"linguist-language": "objective-c"}); !ok || got != "Objective-C" {
		t.Errorf("linguist-language=objective-c gave %q %v", got, ok)
	}
	if _, ok := classifyLanguage("x.go", map[string]string{"linguist-language": "NotAListedLanguage"}); ok {
		t.Error("a language the table does not list is counted")
	}
	for _, language := range languageTable {
		if LanguageColor(language.Name) == "" || !strings.HasPrefix(language.Color, "#") || len(language.Color) != 7 {
			t.Errorf("%s has color %q", language.Name, language.Color)
		}
	}
}

// TestLanguagesCacheByCommit proves that a second count of the same commit
// starts no Git process, and that a new commit is counted again.
func TestLanguagesCacheByCommit(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	first := writeLanguageFixture(t, work, remote, map[string]int{"main.go": 10})
	count, _, _ := countGitProcesses(t, manager, "*")
	stats, err := manager.Languages(context.Background(), "sample", first)
	noErr(t, err)
	cold := count()
	if cold == 0 || len(stats.Shares) != 1 {
		t.Fatalf("cold count ran %d Git processes, shares %v", cold, stats.Shares)
	}
	again, err := manager.Languages(context.Background(), "sample", first)
	noErr(t, err)
	if count() != cold || !reflect.DeepEqual(again, stats) {
		t.Fatalf("the same commit ran %d more Git processes", count()-cold)
	}
	again.Shares[0].Name = "changed by the caller"
	if cached, _ := manager.Languages(context.Background(), "sample", first); cached.Shares[0].Name != "Go" {
		t.Fatal("a caller changed the cached count")
	}
	second := writeLanguageFixture(t, work, remote, map[string]int{"main.go": 20, "web/a.css": 5})
	stats, err = manager.Languages(context.Background(), "sample", second)
	noErr(t, err)
	if count() == cold || len(stats.Shares) != 2 {
		t.Fatalf("a new commit was not counted: shares %v", stats.Shares)
	}
	manager.ForgetRefSnapshots(nil)
	before := count()
	if _, err := manager.Languages(context.Background(), "sample", second); err != nil || count() == before {
		t.Fatalf("a forgotten repository was answered from the cache: err=%v", err)
	}
}

// TestLanguagesBounds reports a commit with more entries or output than one
// count reads as too large, never as partial shares, and a count that runs
// past its time limit as an error that is not cached.
func TestLanguagesBounds(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the slow-command wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	commit := writeLanguageFixture(t, work, remote, map[string]int{"a.go": 10, "b.go": 10, "c.go": 10, "d.go": 10})
	restore := func(entries int, output int64, limit time.Duration) func() {
		oldEntries, oldOutput, oldLimit := languageEntryLimit, languageOutputLimit, languageTimeLimit
		languageEntryLimit, languageOutputLimit, languageTimeLimit = entries, output, limit
		manager.ForgetRefSnapshots(nil)
		return func() { languageEntryLimit, languageOutputLimit, languageTimeLimit = oldEntries, oldOutput, oldLimit }
	}

	undo := restore(3, languageOutputLimit, languageTimeLimit)
	stats, err := manager.Languages(context.Background(), "sample", commit)
	undo()
	if err != nil || !stats.TooLarge || len(stats.Shares) != 0 {
		t.Fatalf("entry cap: stats=%+v err=%v", stats, err)
	}

	undo = restore(languageEntryLimit, 64, languageTimeLimit)
	stats, err = manager.Languages(context.Background(), "sample", commit)
	undo()
	if err != nil || !stats.TooLarge || len(stats.Shares) != 0 {
		t.Fatalf("output cap: stats=%+v err=%v", stats, err)
	}

	count, _, slowPath := countGitProcesses(t, manager, "ls-tree")
	noErr(t, os.WriteFile(slowPath, nil, 0o600))
	undo = restore(languageEntryLimit, languageOutputLimit, 200*time.Millisecond)
	started := time.Now()
	_, err = manager.Languages(context.Background(), "sample", commit)
	undo()
	if err == nil || time.Since(started) > 1900*time.Millisecond {
		t.Fatalf("time cap: err=%v after %s", err, time.Since(started))
	}
	noErr(t, os.Remove(slowPath))
	before := count()
	stats, err = manager.Languages(context.Background(), "sample", commit)
	if err != nil || count() == before || len(stats.Shares) != 1 || stats.Shares[0].Bytes != 40 {
		t.Fatalf("after a timed-out count: stats=%+v err=%v", stats, err)
	}

	if _, err := manager.Languages(context.Background(), "sample", "not-a-commit"); err == nil {
		t.Fatal("an invalid commit ID was accepted")
	}
	if _, err := manager.Languages(context.Background(), "missing", commit); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a missing repository: err=%v", err)
	}
}

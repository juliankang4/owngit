package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	noErr(t, err)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s: %v", root, err)
	}
	return root
}

func nativeTarget(t *testing.T) string {
	t.Helper()
	name := runtime.GOOS + "/" + runtime.GOARCH
	if _, err := selectTargets(name); err != nil {
		t.Skipf("no release target for %s", name)
	}
	return name
}

var (
	sharedOnce sync.Once
	sharedPath string
	sharedErr  error
)

// requireGoToolchain skips a test that needs the Go toolchain when go is not on
// PATH.
//
// The release tool runs on a build host, which has Go by design: building,
// verifying build metadata and collecting notices all run go. On a host
// without it such a test can only fail before reaching what it checks, so it
// is skipped with that reason. Where go is present nothing changes; a failing
// go command still fails the test.
func requireGoToolchain(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("needs the Go toolchain, which this host does not provide: %v", err)
	}
}

// sharedDist builds every target once and reuses it across tests.
func sharedDist(t *testing.T) string {
	t.Helper()
	requireGoToolchain(t)
	sharedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "owngit-release-dist-")
		if err != nil {
			sharedErr = err
			return
		}
		sharedPath = dir
		sharedErr = buildCommand([]string{"-source", repoRoot(t), "-out", dir})
	})
	if sharedErr != nil {
		t.Fatalf("shared build: %v", sharedErr)
	}
	return sharedPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedPath != "" {
		os.RemoveAll(sharedPath)
	}
	os.Exit(code)
}

// TestTargetTable pins the agreed target set. macOS is Apple Silicon only.
func TestTargetTable(t *testing.T) {
	names := []string{}
	for _, current := range releaseTargets {
		names = append(names, current.String())
	}
	want := "darwin/arm64,linux/amd64,linux/arm64,windows/amd64"
	if strings.Join(names, ",") != want {
		t.Fatalf("release targets are %s, want %s", strings.Join(names, ","), want)
	}
	if _, err := selectTargets("darwin/amd64"); err == nil {
		t.Fatal("darwin/amd64 is still selectable")
	}
	if _, err := selectTargets("windows/arm64"); err == nil {
		t.Fatal("windows/arm64 is selectable")
	}
}

// TestBuildVerifyAndCounterexamples builds the native target once and then
// proves that verification rejects a mutated output directory.
func TestBuildVerifyAndCounterexamples(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	native := nativeTarget(t)
	dir := t.TempDir()
	noErrf(t, buildCommand([]string{"-source", root, "-out", dir, "-targets", native}), "build")
	noErrf(t, verifyDir(dir, "go"), "verify rejected a fresh build")

	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	if !document.Artifacts[0].Executed {
		t.Fatal("the native artifact was not recorded as executed")
	}
	if document.Artifacts[0].ExecutedOutput != "owngit "+document.Version+"\n" {
		t.Fatalf("recorded execution output is %q", document.Artifacts[0].ExecutedOutput)
	}
	host, err := targetFor(native)
	noErr(t, err)
	entries, err := readArchive(filepath.Join(dir, document.Artifacts[0].Name), host.format)
	noErr(t, err)
	font := false
	for _, entry := range entries {
		if entry.name == "THIRD_PARTY_NOTICES/bundled-assets/pretendard/PRETENDARD-LICENSE.txt" {
			font = true
		}
	}
	if !font {
		t.Fatal("the archive carries no notice for the embedded font")
	}

	t.Run("corrupted archive byte", func(t *testing.T) {
		copied := copyDist(t, dir)
		path := filepath.Join(copied, document.Artifacts[0].Name)
		data, err := os.ReadFile(path)
		noErr(t, err)
		data[len(data)/2] ^= 0xff
		noErr(t, os.WriteFile(path, data, 0o644))
		expectVerifyError(t, copied, "sha256")
	})

	t.Run("git metadata inside the archive", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return append(files, memFile{name: ".git/config", mode: 0o644, data: []byte("[core]\n")})
		})
		expectVerifyError(t, copied, "Git metadata")
	})

	t.Run("state database inside the archive", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return append(files, memFile{name: "owngit.sqlite", mode: 0o644, data: []byte("sqlite")})
		})
		expectVerifyError(t, copied, "state database")
	})

	t.Run("missing required notice index", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return removeFile(files, "THIRD_PARTY_NOTICES/README.md")
		})
		expectVerifyError(t, copied, "missing THIRD_PARTY_NOTICES/README.md")
	})

	t.Run("declared notice omitted from the archive", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return removeFile(files, "THIRD_PARTY_NOTICES/bundled-assets/pretendard/PRETENDARD-LICENSE.txt")
		})
		expectVerifyError(t, copied, "declared notice")
	})

	t.Run("undeclared notice added to the archive", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return append(files, memFile{name: "THIRD_PARTY_NOTICES/extra.txt", mode: 0o644, data: []byte("extra\n")})
		})
		expectVerifyError(t, copied, "undeclared notice")
	})

	t.Run("notice entry removed together with its files", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			return dropNoticeEntry(t, files, "modernc.org/sqlite")
		})
		expectVerifyError(t, copied, "the notice set has no entry")
	})

	t.Run("non executable binary", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, true, func(files []memFile) []memFile {
			for index := range files {
				if !strings.HasSuffix(files[index].name, ".txt") && !strings.Contains(files[index].name, "/") {
					files[index].mode = 0o644
				}
			}
			return files
		})
		expectVerifyError(t, copied, "not executable")
	})

	t.Run("unexpected extra entry", func(t *testing.T) {
		copied := copyDist(t, dir)
		rewriteDist(t, copied, false, func(files []memFile) []memFile {
			return append(files, memFile{name: "notes.txt", mode: 0o644, data: []byte("extra\n")})
		})
		expectVerifyError(t, copied, "unexpected")
	})

	t.Run("version drift", func(t *testing.T) {
		copied := copyDist(t, dir)
		drifted, err := readManifest(filepath.Join(copied, "manifest.json"))
		noErr(t, err)
		drifted.Version = "9.9.9"
		writeManifest(t, copied, drifted)
		expectVerifyError(t, copied, "does not match the source version")
	})

	t.Run("checksum file drift", func(t *testing.T) {
		copied := copyDist(t, dir)
		path := filepath.Join(copied, "SHA256SUMS")
		data, err := os.ReadFile(path)
		noErr(t, err)
		noErr(t, os.WriteFile(path, append([]byte("00"), data...), 0o644))
		expectVerifyError(t, copied, "SHA256SUMS")
	})

	t.Run("symlink archive entry", func(t *testing.T) {
		skipUnlessTar(t, host)
		copied := copyDist(t, dir)
		rewriteTar(t, copied, func(archive *tar.Writer, files []memFile) error {
			return archive.WriteHeader(&tar.Header{
				Typeflag: tar.TypeSymlink, Name: "owngit-link", Linkname: "owngit",
				Mode: 0o777, ModTime: fixedModTime, Format: tar.FormatPAX,
			})
		})
		expectVerifyError(t, copied, "not a regular file")
	})

	t.Run("directory archive entry", func(t *testing.T) {
		skipUnlessTar(t, host)
		copied := copyDist(t, dir)
		rewriteTar(t, copied, func(archive *tar.Writer, files []memFile) error {
			return archive.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir, Name: "THIRD_PARTY_NOTICES/",
				Mode: 0o755, ModTime: fixedModTime, Format: tar.FormatPAX,
			})
		})
		expectVerifyError(t, copied, "not a regular file")
	})
}

// TestRehashedTargetSubstitution puts the linux archive where the macOS
// archive belongs and rehashes the manifest. Only the embedded metadata can
// reveal the swap.
func TestRehashedTargetSubstitution(t *testing.T) {
	dir := copyDist(t, sharedDist(t))
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	var darwin, linux *artifact
	for index := range document.Artifacts {
		switch document.Artifacts[index].Target {
		case "darwin/arm64":
			darwin = &document.Artifacts[index]
		case "linux/amd64":
			linux = &document.Artifacts[index]
		}
	}
	if darwin == nil || linux == nil {
		t.Fatal("the shared build has no darwin/arm64 and linux/amd64 pair")
	}
	data, err := os.ReadFile(filepath.Join(dir, linux.Name))
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(dir, darwin.Name), data, 0o644))
	darwin.SHA256 = linux.SHA256
	darwin.Size = linux.Size
	darwin.Files = linux.Files
	darwin.BuildInfo = linux.BuildInfo
	writeManifest(t, dir, document)
	noErr(t, writeChecksums(dir, document.Artifacts))
	expectVerifyError(t, dir, "embedded build setting GOOS")
}

// TestNoticeStalenessNeedsEveryTarget checks both sides of the stale notice
// rule. Every archive ships the notice set for all release targets, so a build
// of one target carries entries that only other targets link and must still
// verify. A full build has every target's links, so an entry that none of them
// links is stale and is refused.
func TestNoticeStalenessNeedsEveryTarget(t *testing.T) {
	full := sharedDist(t)
	document, err := readManifest(filepath.Join(full, "manifest.json"))
	noErr(t, err)
	for _, built := range document.Artifacts {
		t.Run("only "+built.Target, func(t *testing.T) {
			dir := copyDist(t, full)
			subset := document
			subset.Artifacts = []artifact{built}
			writeManifest(t, dir, subset)
			noErr(t, writeChecksums(dir, subset.Artifacts))
			noErrf(t, verifyDir(dir, "go"), "verify rejected a build of only %s", built.Target)
		})
	}

	t.Run("stale notice entry no target links", func(t *testing.T) {
		dir := copyDist(t, full)
		rewriteArtifact(t, dir, "linux/amd64", true, func(files []memFile) []memFile {
			return addNoticeEntry(t, files, "example.invalid/stale", "v9.9.9")
		})
		expectVerifyError(t, dir, "that no target links")
	})
}

// TestNoticesFreshDestination proves the fresh-destination-only design: a
// nonempty destination is refused unchanged, so nothing is ever deleted and a
// colliding directory or a poisoned manifest cannot be mistaken for something
// the tool owns. The destination is synthetic, never the real checkout.
func TestNoticesFreshDestination(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)

	t.Run("fresh directory is written", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "notices")
		noErr(t, noticesCommand([]string{"-source", root, "-out", out}))
		for _, name := range []string{
			"manifest.json",
			"README.md",
			"bundled-assets/pretendard/PRETENDARD-LICENSE.txt",
		} {
			if _, err := os.Stat(filepath.Join(out, name)); err != nil {
				t.Fatalf("%s was not written: %v", name, err)
			}
		}
	})

	t.Run("empty directory is accepted", func(t *testing.T) {
		noErr(t, noticesCommand([]string{"-source", root, "-out", t.TempDir()}))
	})

	t.Run("nonempty destination is refused unchanged", func(t *testing.T) {
		out := t.TempDir()
		noErr(t, os.MkdirAll(filepath.Join(out, "github.com", "unrelated"), 0o755))
		for _, name := range []string{"github.com/unrelated/keep.txt", "sentinel.txt", "manifest.json"} {
			noErr(t, os.WriteFile(filepath.Join(out, name), []byte("keep\n"), 0o644))
		}
		before := snapshotTree(t, out)
		err := noticesCommand([]string{"-source", root, "-out", out})
		if err == nil || !strings.Contains(err.Error(), "is not empty") {
			t.Fatalf("noticesCommand returned %v", err)
		}
		// The command must surface the advice TestNoticesRefusalAdviceIsSafe
		// pins, unchanged.
		if advice := requireFreshDestination(out); advice == nil || err.Error() != advice.Error() {
			t.Fatalf("noticesCommand refused with %q, want the refusal advice %q", err, advice)
		}
		after := snapshotTree(t, out)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("the refused destination changed:\n%v\n%v", before, after)
		}
	})

	t.Run("check mode is read only", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "notices")
		noErr(t, noticesCommand([]string{"-source", root, "-out", out}))
		before := snapshotTree(t, out)
		noErr(t, noticesCommand([]string{"-source", root, "-out", out, "-check"}))
		after := snapshotTree(t, out)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("check mode changed the destination")
		}
	})
}

// TestNoticesCheckDetectsDrift proves the whole recorded document and the
// generated README are compared, not just the entry names.
func TestNoticesCheckDetectsDrift(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	templatePath := filepath.Join(root, "packaging", "notices", "README.md.tmpl")
	document, _, err := collectNotices("go", root, "./cmd/owngit")
	noErr(t, err)

	cases := []struct {
		name   string
		want   string
		mutate func(*noticeManifest)
	}{
		{"package", "package", func(d *noticeManifest) { d.Package = "./other" }},
		{"go version", "Go version", func(d *noticeManifest) { d.Go = "go0.0.0" }},
		{"entry version", "build inputs need", func(d *noticeManifest) { d.Entries[0].Version = "v0.0.0" }},
		{"file mode", "records mode", func(d *noticeManifest) { d.Entries[0].Files[0].Mode = "0600" }},
		{"file size", "records mode", func(d *noticeManifest) { d.Entries[0].Files[0].Size++ }},
		{"file digest", "records mode", func(d *noticeManifest) { d.Entries[0].Files[0].SHA256 = strings.Repeat("0", 64) }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "notices")
			noErr(t, noticesCommand([]string{"-source", root, "-out", out}))
			drifted := document
			drifted.Entries = append([]noticeEntry{}, document.Entries...)
			drifted.Entries[0].Files = append([]fileEntry{}, document.Entries[0].Files...)
			item.mutate(&drifted)
			encoded, err := json.MarshalIndent(drifted, "", "  ")
			noErr(t, err)
			noErr(t, os.WriteFile(filepath.Join(out, "manifest.json"), append(encoded, '\n'), 0o644))
			err = checkNotices(out, templatePath, document)
			if err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("checkNotices returned %v, want %q", err, item.want)
			}
		})
	}

	t.Run("generated readme", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "notices")
		noErr(t, noticesCommand([]string{"-source", root, "-out", out}))
		noErr(t, os.WriteFile(filepath.Join(out, "README.md"), []byte("stale\n"), 0o644))
		err := checkNotices(out, templatePath, document)
		if err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("checkNotices returned %v", err)
		}
	})
}

// TestArchivePathRules pins the platform-independent archive path rules.
func TestArchivePathRules(t *testing.T) {
	reject := []struct{ name, want string }{
		{"C:/owngit", "drive prefix"},
		{"C:owngit", "drive prefix"},
		{"//server/share/owngit", "absolute"},
		{"/owngit", "absolute"},
		{`a\b`, "forward slashes"},
		{"a//b", "unsafe path component"},
		{"a/../b", "unsafe path component"},
		{"a/./b", "unsafe path component"},
	}
	for _, item := range reject {
		if err := validateRelativePath(item.name); err == nil || !strings.Contains(err.Error(), item.want) {
			t.Errorf("validateRelativePath(%q) = %v, want %q", item.name, err, item.want)
		}
	}
	for _, name := range []string{"owngit", "THIRD_PARTY_NOTICES/README.md", "a/b/c.txt"} {
		if err := validateRelativePath(name); err != nil {
			t.Errorf("validateRelativePath(%q) = %v", name, err)
		}
	}
}

// TestForbiddenComponents proves private and build components are rejected at
// every depth, not only at the archive root.
func TestForbiddenComponents(t *testing.T) {
	cases := []struct{ name, want string }{
		{".git/config", "Git metadata"},
		{"THIRD_PARTY_NOTICES/.git/config", "Git metadata"},
		{"a/.local/b", "private planning records"},
		{"a/b/.playwright-mcp/c", "browser tool state"},
		{"dist/x", "build output"},
		{"a/dist/x", "build output"},
		{"a/owngit.sqlite", "state database"},
		{"a/b/x.test", "test binary"},
	}
	for _, item := range cases {
		if got := forbiddenReason(item.name); got != item.want {
			t.Errorf("forbiddenReason(%q) = %q, want %q", item.name, got, item.want)
		}
	}
	if got := forbiddenReason("THIRD_PARTY_NOTICES/README.md"); got != "" {
		t.Errorf("forbiddenReason(README.md) = %q", got)
	}
}

// TestNoticesRefusalAdviceIsSafe pins the refusal text. The advice must not
// recommend recursive deletion or a fixed shared /tmp name, and it must stay
// copy-pasteable for a checkout-like or spaced destination.
//
// The advice comes from requireFreshDestination, which needs no Go toolchain,
// so it is checked directly; TestNoticesFreshDestination checks that the
// command refuses with exactly this advice. Each destination is relative to a
// temporary working directory, so the host's temporary path does not decide
// the expected text: it can itself lie under /tmp, and on Windows its drive
// and separators always need quoting.
func TestNoticesRefusalAdviceIsSafe(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := []struct {
		name    string
		dirName string
		quoted  bool
	}{
		{"plain", "THIRD_PARTY_NOTICES", false},
		{"checkout like", "owngit-checkout", false},
		{"spaced", "checkout like dir", true},
		{"metacharacter", "odd;dir", true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			out := item.dirName
			noErr(t, os.MkdirAll(out, 0o755))
			noErr(t, os.WriteFile(filepath.Join(out, "sentinel.txt"), []byte("keep\n"), 0o644))
			err := requireFreshDestination(out)
			if err == nil {
				t.Fatal("a nonempty destination was accepted")
			}
			message := err.Error()
			for _, banned := range []string{"rm -rf", "/tmp/", "replace the old"} {
				if strings.Contains(message, banned) {
					t.Errorf("refusal advice still uses %q:\n%s", banned, message)
				}
			}
			destination := out
			suggestion := out + ".new"
			if item.quoted {
				destination = "'" + destination + "'"
				suggestion = "'" + suggestion + "'"
			}
			if !strings.Contains(message, "-out "+suggestion) {
				t.Errorf("refusal advice does not suggest %q:\n%s", suggestion, message)
			}
			if !strings.Contains(message, "diff -r "+destination+" "+suggestion) {
				t.Errorf("refusal advice does not diff %q against %q:\n%s", destination, suggestion, message)
			}
			if !strings.Contains(message, "update only the notice files") {
				t.Errorf("refusal advice does not explain the update step:\n%s", message)
			}
		})
	}
}

// TestGoRuntimeNoticeFallback pins the GOROOT authority rule on a synthetic
// filesystem, so a conflicting parent PATENTS or an unrelated parent notice
// file cannot change the collected bytes. The real Homebrew layout is not
// touched.
func TestGoRuntimeNoticeFallback(t *testing.T) {
	const goLicense = "Copyright 2009 The Go Authors. All rights reserved.\n"
	const parentLicense = "Copyright 2009 The Go Authors. All rights reserved. Homebrew copy.\n"

	// layout builds a synthetic GOROOT with a parent directory that holds a
	// conflicting PATENTS and unrelated NOTICE and COPYING sentinels.
	layout := func(t *testing.T, gorootFiles map[string]string) (string, string) {
		t.Helper()
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		for name, body := range gorootFiles {
			writeSynthetic(t, goroot, name, body)
		}
		writeSynthetic(t, base, "LICENSE", parentLicense)
		writeSynthetic(t, base, "PATENTS", "parent patents\n")
		writeSynthetic(t, base, "NOTICE", "parent notice\n")
		writeSynthetic(t, base, "COPYING", "parent copying\n")
		return goroot, base
	}

	t.Run("goroot license and patents are authoritative", func(t *testing.T) {
		goroot, _ := layout(t, map[string]string{"LICENSE": goLicense, "PATENTS": "goroot patents\n"})
		files, err := goRuntimeNoticesFrom(goroot)
		noErr(t, err)
		if got := string(files["LICENSE"]); got != goLicense {
			t.Errorf("LICENSE = %q, want the GOROOT copy", got)
		}
		if got := string(files["PATENTS"]); got != "goroot patents\n" {
			t.Errorf("PATENTS = %q, want the GOROOT copy", got)
		}
		if len(files) != 2 {
			t.Errorf("collected %d files, want 2", len(files))
		}
	})

	t.Run("parent license supplements a missing goroot license", func(t *testing.T) {
		goroot, _ := layout(t, map[string]string{"PATENTS": "goroot patents\n"})
		files, err := goRuntimeNoticesFrom(goroot)
		noErr(t, err)
		if got := string(files["LICENSE"]); got != parentLicense {
			t.Errorf("LICENSE = %q, want the parent copy", got)
		}
		if got := string(files["PATENTS"]); got != "goroot patents\n" {
			t.Errorf("PATENTS = %q, want the GOROOT copy, not the conflicting parent copy", got)
		}
		if len(files) != 2 {
			t.Errorf("collected %d files, want 2, so no parent NOTICE or COPYING entered", len(files))
		}
	})

	t.Run("parent patents supplements a missing goroot patents", func(t *testing.T) {
		goroot, _ := layout(t, map[string]string{"LICENSE": goLicense})
		files, err := goRuntimeNoticesFrom(goroot)
		noErr(t, err)
		if got := string(files["PATENTS"]); got != "parent patents\n" {
			t.Errorf("PATENTS = %q, want the parent copy", got)
		}
		if len(files) != 2 {
			t.Errorf("collected %d files, want 2", len(files))
		}
	})

	t.Run("parent patents requires a parent license", func(t *testing.T) {
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		writeSynthetic(t, goroot, "LICENSE", goLicense)
		writeSynthetic(t, base, "PATENTS", "parent patents\n")
		_, err := goRuntimeNoticesFrom(goroot)
		if err == nil || !strings.Contains(err.Error(), "parent LICENSE") {
			t.Fatalf("goRuntimeNoticesFrom returned %v", err)
		}
	})

	t.Run("parent patents requires a valid parent license", func(t *testing.T) {
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		writeSynthetic(t, goroot, "LICENSE", goLicense)
		writeSynthetic(t, base, "LICENSE", "some other license\n")
		writeSynthetic(t, base, "PATENTS", "parent patents\n")
		_, err := goRuntimeNoticesFrom(goroot)
		if err == nil || !strings.Contains(err.Error(), "does not look like the Go license") {
			t.Fatalf("goRuntimeNoticesFrom returned %v", err)
		}
	})

	t.Run("missing patents in both sources is reported", func(t *testing.T) {
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		writeSynthetic(t, goroot, "LICENSE", goLicense)
		writeSynthetic(t, base, "LICENSE", parentLicense)
		_, err := goRuntimeNoticesFrom(goroot)
		if err == nil || !strings.Contains(err.Error(), "no Go runtime PATENTS") {
			t.Fatalf("goRuntimeNoticesFrom returned %v", err)
		}
	})

	t.Run("missing parent license is reported", func(t *testing.T) {
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		writeSynthetic(t, goroot, "PATENTS", "goroot patents\n")
		_, err := goRuntimeNoticesFrom(goroot)
		if err == nil || !strings.Contains(err.Error(), "no Go runtime LICENSE") {
			t.Fatalf("goRuntimeNoticesFrom returned %v", err)
		}
	})

	t.Run("a parent license that is not the Go license is rejected", func(t *testing.T) {
		base := t.TempDir()
		goroot := filepath.Join(base, "libexec")
		writeSynthetic(t, goroot, "PATENTS", "goroot patents\n")
		writeSynthetic(t, base, "LICENSE", "some other license\n")
		_, err := goRuntimeNoticesFrom(goroot)
		if err == nil || !strings.Contains(err.Error(), "does not look like the Go license") {
			t.Fatalf("goRuntimeNoticesFrom returned %v", err)
		}
	})
}

func writeSynthetic(t *testing.T, dir, name, body string) {
	t.Helper()
	noErr(t, os.MkdirAll(dir, 0o755))
	noErr(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		digest, err := sha256File(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = digest
		return nil
	})
	noErr(t, err)
	return files
}

// TestNoticesCheckMatchesBuildInputs proves the checked-in notices are current.
func TestNoticesCheckMatchesBuildInputs(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	document, _, err := collectNotices("go", root, "./cmd/owngit")
	noErrf(t, err, "collect")
	noErrf(t, checkNotices(filepath.Join(root, "THIRD_PARTY_NOTICES"), filepath.Join(root, "packaging", "notices", "README.md.tmpl"), document), "checked-in notices are stale")
	// One declared embedded asset plus the module graph. The removal dropped
	// two adopted notice sets, so the bound has one set of slack.
	if len(document.Entries) < 12 {
		t.Fatalf("only %d notice sets were collected", len(document.Entries))
	}
	bundled := false
	for _, entry := range document.Entries {
		if strings.HasPrefix(entry.Module, bundledNoticePrefix) {
			bundled = true
		}
	}
	if !bundled {
		t.Fatal("no bundled asset notice was collected")
	}
}

// TestNoticesDeclaredInputsAreEmbeddedAssets checks the declared non-module
// notice inputs now that no adapted source is left.
//
// The built-in review removal took the pinned third-party authorization flows
// with it, so the only declared kind is an embedded asset. Each declaration
// must name a source file that exists, must be collected under the name it
// declares, and must record that source rather than a package path.
func TestNoticesDeclaredInputsAreEmbeddedAssets(t *testing.T) {
	requireGoToolchain(t)
	root := repoRoot(t)
	document, contents, err := collectNotices("go", root, "./cmd/owngit")
	noErrf(t, err, "collect")
	inputs := declaredNoticeInputs()
	if len(inputs) != len(bundledAssets) || len(inputs) == 0 {
		t.Fatalf("declared inputs = %d, want the declared bundled assets %d", len(inputs), len(bundledAssets))
	}
	for _, input := range inputs {
		if !strings.HasPrefix(input.module, bundledNoticePrefix) {
			t.Errorf("declared input %s is not under %s", input.module, bundledNoticePrefix)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(input.source))); err != nil {
			t.Errorf("the recorded source %s does not exist: %v", input.source, err)
		}
		if input.source == "internal/reviewauth" {
			t.Errorf("the source %s names the removed package", input.source)
		}
		var entry *noticeEntry
		for index := range document.Entries {
			if document.Entries[index].Module == input.module {
				entry = &document.Entries[index]
			}
		}
		if entry == nil {
			t.Fatalf("the declared input %s was not collected", input.module)
		}
		if entry.Source != input.source {
			t.Errorf("%s records source %q, want %q", input.module, entry.Source, input.source)
		}
		for _, file := range entry.Files {
			if _, ok := contents[input.module+"/"+file.Path]; !ok {
				t.Errorf("%s/%s was not collected from its declared file", input.module, file.Path)
			}
		}
	}
}

// TestNoticeTreeRejectsUndeclaredAndSymlink proves the exact declared input
// set and the regular-file rule.
func TestNoticeTreeRejectsUndeclaredAndSymlink(t *testing.T) {
	root := repoRoot(t)
	source := filepath.Join(root, "THIRD_PARTY_NOTICES")

	t.Run("undeclared file", func(t *testing.T) {
		dir := copyTreeToTemp(t, source)
		noErr(t, os.WriteFile(filepath.Join(dir, "sentinel.txt"), []byte("x\n"), 0o644))
		if _, err := declaredNoticeFiles(dir); err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("declaredNoticeFiles returned %v", err)
		}
	})

	t.Run("symlinked notice file", func(t *testing.T) {
		dir := copyTreeToTemp(t, source)
		target := filepath.Join(dir, "modernc.org", "sqlite", "LICENSE")
		noErr(t, os.Remove(target))
		noErr(t, os.Symlink("LICENSE-SQLITE", target))
		if _, err := declaredNoticeFiles(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("declaredNoticeFiles returned %v", err)
		}
	})

	t.Run("missing declared file", func(t *testing.T) {
		dir := copyTreeToTemp(t, source)
		noErr(t, os.Remove(filepath.Join(dir, "go-runtime", "PATENTS")))
		if _, err := declaredNoticeFiles(dir); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("declaredNoticeFiles returned %v", err)
		}
	})
}

// TestPackagingRendering builds every target and exercises the ready, unready,
// per-format, and rejected publication inputs.
func TestPackagingRendering(t *testing.T) {
	root := repoRoot(t)
	dir := sharedDist(t)
	manifestPath := filepath.Join(dir, "manifest.json")

	unready := t.TempDir()
	noErrf(t, packagingCommand([]string{"-source", root, "-manifest", manifestPath, "-out", unready}), "packaging without inputs")
	formula := readText(t, filepath.Join(unready, "owngit.rb"))
	if !strings.Contains(formula, "UNREADY") {
		t.Fatal("a render without publication inputs carries no UNREADY marker")
	}
	if !strings.Contains(formula, "example.invalid") {
		t.Fatal("a render without a base URL does not use the documented placeholder")
	}
	if _, err := os.Stat(filepath.Join(unready, "OwnGit.Owngit.installer.yaml")); err != nil {
		t.Fatalf("default package identifier was not used: %v", err)
	}

	ready := t.TempDir()
	readyArguments := []string{
		"-source", root, "-manifest", manifestPath, "-out", ready,
		"-base-url", "https://example.test/owngit/releases/download/v1.0.0",
		"-homepage", "https://example.test/owngit",
		"-tap", "example/homebrew-owngit",
		"-package-id", "Example.Owngit",
		"-publisher", "Example",
		"-publisher-url", "https://example.test",
	}
	noErrf(t, packagingCommand(readyArguments), "packaging with inputs")
	formula = readText(t, filepath.Join(ready, "owngit.rb"))
	if strings.Contains(formula, "UNREADY") {
		t.Fatal("a fully supplied render still carries an UNREADY marker")
	}
	if !strings.Contains(formula, "on_macos do\n    on_arm do") {
		t.Fatal("the macOS branch does not require arm64")
	}
	installer := readText(t, filepath.Join(ready, "Example.Owngit.installer.yaml"))
	document, err := readManifest(manifestPath)
	noErr(t, err)
	for _, built := range document.Artifacts {
		if built.Target != "windows/amd64" {
			continue
		}
		if !strings.Contains(installer, built.Name) {
			t.Fatalf("installer manifest does not name %s", built.Name)
		}
		if !strings.Contains(installer, strings.ToUpper(built.SHA256)) {
			t.Fatal("installer manifest does not carry the uppercase checksum")
		}
	}
	if !strings.Contains(formula, "sha256") {
		t.Fatal("formula carries no checksum")
	}

	t.Run("homebrew only", func(t *testing.T) {
		out := t.TempDir()
		err := packagingCommand([]string{
			"-source", root, "-manifest", manifestPath, "-out", out, "-formats", "homebrew",
			"-base-url", "https://example.test/owngit/releases/download/v1.0.0",
			"-homepage", "https://example.test/owngit",
			"-tap", "example/homebrew-owngit",
		})
		noErrf(t, err, "homebrew-only render")
		if strings.Contains(readText(t, filepath.Join(out, "owngit.rb")), "UNREADY") {
			t.Fatal("a homebrew-only render with its own inputs is unready")
		}
		if _, err := os.Stat(filepath.Join(out, "OwnGit.Owngit.yaml")); !os.IsNotExist(err) {
			t.Fatal("a homebrew-only render wrote a WinGet manifest")
		}
	})

	t.Run("winget only", func(t *testing.T) {
		out := t.TempDir()
		err := packagingCommand([]string{
			"-source", root, "-manifest", manifestPath, "-out", out, "-formats", "winget",
			"-base-url", "https://example.test/owngit/releases/download/v1.0.0",
			"-homepage", "https://example.test/owngit",
			"-package-id", "Example.Owngit",
			"-publisher", "Example",
			"-publisher-url", "https://example.test",
		})
		noErrf(t, err, "winget-only render")
		if strings.Contains(readText(t, filepath.Join(out, "Example.Owngit.yaml")), "UNREADY") {
			t.Fatal("a winget-only render with its own inputs is unready")
		}
		if _, err := os.Stat(filepath.Join(out, "owngit.rb")); !os.IsNotExist(err) {
			t.Fatal("a winget-only render wrote a formula")
		}
	})

	t.Run("per format readiness", func(t *testing.T) {
		out := t.TempDir()
		err := packagingCommand([]string{
			"-source", root, "-manifest", manifestPath, "-out", out,
			"-base-url", "https://example.test/owngit/releases/download/v1.0.0",
			"-homepage", "https://example.test/owngit",
			"-tap", "example/homebrew-owngit",
		})
		noErrf(t, err, "render")
		if strings.Contains(readText(t, filepath.Join(out, "owngit.rb")), "UNREADY") {
			t.Fatal("the formula is unready although every homebrew input was supplied")
		}
		winget := readText(t, filepath.Join(out, "OwnGit.Owngit.yaml"))
		if !strings.Contains(winget, "UNREADY") {
			t.Fatal("the WinGet manifest is ready although its inputs are missing")
		}
		if strings.Contains(winget, "tap") {
			t.Fatal("the WinGet readiness list mentions a homebrew-only input")
		}
	})

	t.Run("quoted publisher is encoded", func(t *testing.T) {
		out := t.TempDir()
		arguments := append([]string{}, readyArguments...)
		arguments = append(arguments, "-out", out, "-publisher", `Acme "Quoted" #1`)
		noErrf(t, packagingCommand(arguments), "render")
		locale := readText(t, filepath.Join(out, "Example.Owngit.locale.en-US.yaml"))
		if !strings.Contains(locale, `Publisher: "Acme \"Quoted\" #1"`) {
			t.Fatalf("publisher was not encoded: %s", locale)
		}
	})

	rejections := []struct {
		name  string
		extra []string
		want  string
	}{
		{"trailing slash", []string{"-base-url", "https://example.test/x/"}, "must not end with a slash"},
		{"wrong scheme", []string{"-base-url", "ftp://example.test/x"}, "must use http or https"},
		{"query string", []string{"-homepage", "https://example.test/x?y=1"}, "must not carry a query"},
		{"credentials", []string{"-homepage", "https://user:pass@example.test/x"}, "must not carry credentials"},
		{"control character", []string{"-publisher", "Acme\nInc"}, "control character"},
		{"tap without owner", []string{"-tap", "owngit"}, "owner/repository"},
		{"package id without publisher", []string{"-package-id", "Owngit"}, "Publisher.Package"},
		{"unknown format", []string{"-formats", "npm"}, "unknown format"},
	}
	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			arguments := append(append([]string{}, readyArguments...), rejection.extra...)
			arguments = append(arguments, "-out", t.TempDir())
			err := packagingCommand(arguments)
			if err == nil {
				t.Fatalf("packaging accepted %v", rejection.extra)
			}
			if !strings.Contains(err.Error(), rejection.want) {
				t.Fatalf("error %q does not contain %q", err, rejection.want)
			}
		})
	}

	t.Run("strict rejects missing inputs", func(t *testing.T) {
		err := packagingCommand([]string{"-source", root, "-manifest", manifestPath, "-out", t.TempDir(), "-strict"})
		if err == nil || !strings.Contains(err.Error(), "missing required inputs") {
			t.Fatalf("strict mode returned %v", err)
		}
	})
}

// TestScalarEncoding covers the Ruby and YAML encoders directly, including the
// Ruby interpolation and quote cases that a URL cannot reach.
func TestScalarEncoding(t *testing.T) {
	cases := []struct {
		value string
		ruby  string
		yaml  string
	}{
		{"plain", "plain", `"plain"`},
		{`a"b`, `a\"b`, `"a\"b"`},
		{`a\b`, `a\\b`, `"a\\b"`},
		{"a#{b}c", `a\#{b}c`, `"a#{b}c"`},
		{"a#$b", `a\#$b`, `"a#$b"`},
		{"a#@b", `a\#@b`, `"a#@b"`},
		{"a#b", `a\#b`, `"a#b"`},
	}
	for _, item := range cases {
		if got := rubyString(item.value); got != item.ruby {
			t.Errorf("rubyString(%q) = %q, want %q", item.value, got, item.ruby)
		}
		if got := yamlString(item.value); got != item.yaml {
			t.Errorf("yamlString(%q) = %q, want %q", item.value, got, item.yaml)
		}
	}
}

type memFile struct {
	name string
	mode int64
	data []byte
}

// dropNoticeEntry removes one notice entry and its files from the archived
// notice manifest, so the manifest stays self-consistent while losing coverage
// of a dependency the binary still links.
func dropNoticeEntry(t *testing.T, files []memFile, module string) []memFile {
	t.Helper()
	var document noticeManifest
	kept := make([]memFile, 0, len(files))
	for _, file := range files {
		if file.name == "THIRD_PARTY_NOTICES/manifest.json" {
			noErr(t, json.Unmarshal(file.data, &document))
			continue
		}
		kept = append(kept, file)
	}
	dropped := map[string]bool{}
	entries := make([]noticeEntry, 0, len(document.Entries))
	for _, entry := range document.Entries {
		if entry.Module == module {
			for _, file := range entry.Files {
				dropped["THIRD_PARTY_NOTICES/"+entry.Module+"/"+file.Path] = true
			}
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == len(document.Entries) {
		t.Fatalf("no notice entry for %s", module)
	}
	document.Entries = entries
	encoded, err := json.MarshalIndent(document, "", "  ")
	noErr(t, err)
	kept = append(kept, memFile{name: "THIRD_PARTY_NOTICES/manifest.json", mode: 0o644, data: append(encoded, '\n')})
	final := make([]memFile, 0, len(kept))
	for _, file := range kept {
		if !dropped[file.name] {
			final = append(final, file)
		}
	}
	return final
}

// addNoticeEntry appends a self-consistent notice entry that no binary links,
// so the reverse coverage check has to reject it.
func addNoticeEntry(t *testing.T, files []memFile, module, moduleVersion string) []memFile {
	t.Helper()
	var document noticeManifest
	kept := make([]memFile, 0, len(files)+2)
	for _, file := range files {
		if file.name == "THIRD_PARTY_NOTICES/manifest.json" {
			noErr(t, json.Unmarshal(file.data, &document))
			continue
		}
		kept = append(kept, file)
	}
	body := []byte("stale license\n")
	document.Entries = append(document.Entries, noticeEntry{
		Module: module, Version: moduleVersion, Source: "synthetic",
		Files: []fileEntry{{Path: "LICENSE", Mode: "0644", Size: int64(len(body)), SHA256: sha256Bytes(body)}},
	})
	encoded, err := json.MarshalIndent(document, "", "  ")
	noErr(t, err)
	kept = append(kept, memFile{name: "THIRD_PARTY_NOTICES/manifest.json", mode: 0o644, data: append(encoded, '\n')})
	kept = append(kept, memFile{name: "THIRD_PARTY_NOTICES/" + module + "/LICENSE", mode: 0o644, data: body})
	return kept
}

func removeFile(files []memFile, name string) []memFile {
	kept := files[:0]
	for _, file := range files {
		if file.name != name {
			kept = append(kept, file)
		}
	}
	return kept
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	noErr(t, err)
	return string(data)
}

func writeManifest(t *testing.T, dir string, document manifest) {
	t.Helper()
	encoded, err := json.MarshalIndent(document, "", "  ")
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(dir, "manifest.json"), append(encoded, '\n'), 0o644))
}

// copyDist copies the top-level output files, leaving the stage directory out.
func copyDist(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	entries, err := os.ReadDir(source)
	noErr(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		noErr(t, err)
		noErr(t, os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o644))
	}
	return destination
}

func copyTreeToTemp(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	noErr(t, copyTree(source, destination))
	return destination
}

// rewriteDist decodes the single artifact, applies mutate, and writes the
// archive back. When syncFiles is set the manifest file list follows the
// mutation, so a later check can fail for its own reason instead of the digest.
func rewriteDist(t *testing.T, dir string, syncFiles bool, mutate func([]memFile) []memFile) {
	t.Helper()
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	if len(document.Artifacts) != 1 {
		t.Fatalf("rewriteDist expects one artifact, found %d", len(document.Artifacts))
	}
	rewriteArtifact(t, dir, document.Artifacts[0].Target, syncFiles, mutate)
}

// rewriteArtifact is rewriteDist for the artifact of one target in a
// directory that may hold several.
func rewriteArtifact(t *testing.T, dir, name string, syncFiles bool, mutate func([]memFile) []memFile) {
	t.Helper()
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	var built *artifact
	for index := range document.Artifacts {
		if document.Artifacts[index].Target == name {
			built = &document.Artifacts[index]
		}
	}
	if built == nil {
		t.Fatalf("the manifest has no %s artifact", name)
	}
	current, err := targetFor(built.Target)
	noErr(t, err)
	entries, err := readArchive(filepath.Join(dir, built.Name), current.format)
	noErr(t, err)
	files := make([]memFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, memFile{name: entry.name, mode: entry.mode, data: entry.data})
	}
	files = mutate(files)
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	stage := t.TempDir()
	staged := make([]stagedFile, 0, len(files))
	for _, file := range files {
		path := filepath.Join(stage, filepath.FromSlash(file.name))
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o755))
		noErr(t, os.WriteFile(path, file.data, os.FileMode(file.mode)))
		staged = append(staged, stagedFile{
			name: file.name, path: path, mode: file.mode,
			size: int64(len(file.data)), sha: sha256Bytes(file.data),
		})
	}
	archivePath := filepath.Join(dir, built.Name)
	if current.format == "zip" {
		err = writeZip(archivePath, staged)
	} else {
		err = writeTarGz(archivePath, staged)
	}
	noErr(t, err)
	refreshArchiveIdentity(t, dir, document, built)
	if syncFiles {
		built.Files = nil
		for _, file := range staged {
			built.Files = append(built.Files, fileEntry{
				Path: file.name, Mode: fmt.Sprintf("%04o", file.mode),
				Size: file.size, SHA256: file.sha,
			})
		}
	}
	writeManifest(t, dir, document)
	noErr(t, writeChecksums(dir, document.Artifacts))
}

// skipUnlessTar skips a subtest that writes tar-only members when the native
// archive is a zip.
func skipUnlessTar(t *testing.T, native target) {
	t.Helper()
	if native.format != "tar.gz" {
		t.Skipf("tar-only counterexample; the native archive is %s", native.format)
	}
}

// rewriteTar rewrites the single artifact's tar with an extra header written
// after the regular files, so a nonregular member can be exercised.
func rewriteTar(t *testing.T, dir string, extra func(*tar.Writer, []memFile) error) {
	t.Helper()
	document, err := readManifest(filepath.Join(dir, "manifest.json"))
	noErr(t, err)
	built := &document.Artifacts[0]
	entries, err := readArchive(filepath.Join(dir, built.Name), "tar.gz")
	noErr(t, err)
	files := make([]memFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, memFile{name: entry.name, mode: entry.mode, data: entry.data})
	}
	path := filepath.Join(dir, built.Name)
	file, err := os.Create(path)
	noErr(t, err)
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for _, entry := range files {
		header := &tar.Header{
			Typeflag: tar.TypeReg, Name: entry.name, Size: int64(len(entry.data)),
			Mode: entry.mode, ModTime: fixedModTime, Format: tar.FormatPAX,
		}
		noErr(t, archive.WriteHeader(header))
		if _, err := archive.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	noErr(t, extra(archive, files))
	noErr(t, archive.Close())
	noErr(t, compressed.Close())
	noErr(t, file.Close())
	refreshArchiveIdentity(t, dir, document, built)
	writeManifest(t, dir, document)
	noErr(t, writeChecksums(dir, document.Artifacts))
}

func refreshArchiveIdentity(t *testing.T, dir string, document manifest, built *artifact) {
	t.Helper()
	path := filepath.Join(dir, built.Name)
	digest, err := sha256File(path)
	noErr(t, err)
	info, err := os.Stat(path)
	noErr(t, err)
	built.SHA256 = digest
	built.Size = info.Size()
}

func expectVerifyError(t *testing.T, dir, want string) {
	t.Helper()
	err := verifyDir(dir, "go")
	if err == nil {
		t.Fatalf("verify accepted a mutated output directory; want an error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("verify error %q does not contain %q", err, want)
	}
	t.Logf("rejected: %v", err)
}

// noErr stops the test when err is not nil.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// noErrf stops the test when err is not nil, naming the failed step.
func noErrf(t testing.TB, err error, format string, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", fmt.Sprintf(format, args...), err)
	}
}

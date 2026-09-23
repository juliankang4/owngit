package checksource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSource serves a fixed entry list from memory. It lets the tests cover
// tree shapes and failures that are awkward to commit, without a repository.
type fakeSource struct {
	format  string
	commit  string
	entries []Entry
	blobs   map[string][]byte
	// beforeRead runs before each blob read, so a test can change the
	// destination the way a concurrent process would.
	beforeRead func(oid string) error
	// afterRead runs after a blob read returns its bytes.
	afterRead func(oid string)
	// beforeList runs before the listing is returned.
	beforeList func() error
	// listErr replaces a successful listing.
	listErr error
}

func newFakeSource(t *testing.T, files map[string]string, modes map[string]string) *fakeSource {
	t.Helper()
	source := &fakeSource{format: FormatSHA1, commit: strings.Repeat("a", 40), blobs: map[string][]byte{}}
	for path, content := range files {
		mode := ModeRegular
		if modes != nil && modes[path] != "" {
			mode = modes[path]
		}
		source.addBlob(t, path, mode, []byte(content))
	}
	return source
}

func (s *fakeSource) addBlob(t *testing.T, path, mode string, content []byte) {
	t.Helper()
	oid, err := blobObjectID(s.format, content)
	if err != nil {
		t.Fatal(err)
	}
	s.blobs[oid] = content
	s.entries = append(s.entries, Entry{Path: path, OID: oid, Mode: mode, Type: "blob", Size: int64(len(content))})
}

func (s *fakeSource) addEntry(entry Entry) { s.entries = append(s.entries, entry) }

func (s *fakeSource) CommitOID() string    { return s.commit }
func (s *fakeSource) ObjectFormat() string { return s.format }

func (s *fakeSource) ListTree(ctx context.Context, metadataLimit int64) ([]Entry, error) {
	if s.beforeList != nil {
		if err := s.beforeList(); err != nil {
			return nil, err
		}
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]Entry(nil), s.entries...), nil
}

func (s *fakeSource) ReadBlob(ctx context.Context, oid string, size int64) ([]byte, error) {
	if s.beforeRead != nil {
		if err := s.beforeRead(oid); err != nil {
			return nil, err
		}
	}
	content, ok := s.blobs[oid]
	if !ok {
		return nil, errors.New("missing object")
	}
	if s.afterRead != nil {
		s.afterRead(oid)
	}
	return append([]byte(nil), content...), nil
}

func destinationIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "source")
}

func TestMaterializeWritesExactBytesAndExecutableBits(t *testing.T) {
	source := newFakeSource(t, map[string]string{
		"text/lf.txt":   "one\ntwo\n",
		"text/crlf.txt": "one\r\ntwo\r\n",
		"build.sh":      "#!/bin/sh\nexit 0\n",
	}, map[string]string{"build.sh": ModeExecutable})
	source.addBlob(t, "assets/binary.dat", ModeRegular, []byte{0x00, 0xff, 0x0a, 0x00, 0x1b})
	destination := destinationIn(t)

	result, err := Materialize(context.Background(), source, destination, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitOID != source.commit || result.ObjectFormat != FormatSHA1 {
		t.Fatalf("unexpected source identity: %+v", result)
	}
	if len(result.Files) != 4 {
		t.Fatalf("unexpected manifest length: %+v", result.Files)
	}
	for path, want := range map[string]string{
		"text/lf.txt":   "one\ntwo\n",
		"text/crlf.txt": "one\r\ntwo\r\n",
	} {
		content, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("%s bytes changed: got %q want %q", path, content, want)
		}
	}
	binary, err := os.ReadFile(filepath.Join(destination, "assets", "binary.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != string([]byte{0x00, 0xff, 0x0a, 0x00, 0x1b}) {
		t.Fatalf("binary bytes changed: %v", binary)
	}

	var executable FileRecord
	for _, file := range result.Files {
		if file.Path == "build.sh" {
			executable = file
		}
		if file.Mode == "" || file.OID == "" {
			t.Fatalf("manifest lost Git metadata: %+v", file)
		}
	}
	if !executable.Executable || executable.Mode != ModeExecutable {
		t.Fatalf("executable mode was not preserved: %+v", executable)
	}
	info, err := os.Stat(filepath.Join(destination, "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if executable.ExecutableApplied != (info.Mode()&0o100 != 0) {
		t.Fatalf("reported execute bit %v disagrees with filesystem mode %v", executable.ExecutableApplied, info.Mode())
	}
	if executable.ExecutableApplied == result.ExecutableBitsUnsupported {
		t.Fatalf("executable support flags disagree: applied=%v unsupported=%v",
			executable.ExecutableApplied, result.ExecutableBitsUnsupported)
	}
	regular, err := os.Stat(filepath.Join(destination, "text", "lf.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if executable.ExecutableApplied && regular.Mode()&0o111 != 0 {
		t.Fatalf("regular file gained an execute bit: %v", regular.Mode())
	}
}

func TestMaterializeSortsManifestAndCountsBytes(t *testing.T) {
	source := newFakeSource(t, map[string]string{
		"z.txt":     "z",
		"a/b/c.txt": "ccc",
		"a/a.txt":   "aa",
	}, nil)
	result, err := Materialize(context.Background(), source, destinationIn(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range result.Files {
		paths = append(paths, file.Path)
	}
	want := []string{"a/a.txt", "a/b/c.txt", "z.txt"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("manifest order %v, want %v", paths, want)
	}
	if result.TotalBytes != 6 {
		t.Fatalf("total bytes %d, want 6", result.TotalBytes)
	}
}

func TestMaterializeRefusesSymlinksAndGitlinks(t *testing.T) {
	for name, entry := range map[string]Entry{
		"symlink": {Path: "link", OID: strings.Repeat("b", 40), Mode: "120000", Type: "blob", Size: 10},
		"gitlink": {Path: "module", OID: strings.Repeat("c", 40), Mode: "160000", Type: "commit", Size: -1},
		"sticky":  {Path: "odd", OID: strings.Repeat("d", 40), Mode: "100664", Type: "blob", Size: 1},
	} {
		t.Run(name, func(t *testing.T) {
			source := newFakeSource(t, map[string]string{"keep.txt": "keep"}, nil)
			source.addEntry(entry)
			destination := destinationIn(t)
			result, err := Materialize(context.Background(), source, destination, Options{})
			if result != nil || !errors.Is(err, ErrUnsupportedEntry) {
				t.Fatalf("result=%+v err=%v, want unsupported entry", result, err)
			}
			var entryErr *EntryError
			if !errors.As(err, &entryErr) || entryErr.Path != entry.Path {
				t.Fatalf("error did not name the refused entry: %v", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination was created despite a pre-execution refusal: %v", statErr)
			}
		})
	}
}

func TestMaterializeRefusesUnsafePaths(t *testing.T) {
	for name, entryPath := range map[string]string{
		"parent traversal":   "../escape.txt",
		"inner traversal":    "a/../../escape.txt",
		"absolute":           "/etc/hosts",
		"git control":        ".git/config",
		"git control cased":  ".GIT/hooks/pre-commit",
		"git control padded": ".git./config",
		"git short name":     "git~1/config",
		"empty component":    "a//b.txt",
		"dot component":      "a/./b.txt",
		"control character":  "a/\u0007bell.txt",
		"format character":   "a/zero\u200bwidth.txt",
	} {
		t.Run(name, func(t *testing.T) {
			source := newFakeSource(t, map[string]string{"keep.txt": "keep"}, nil)
			source.addBlob(t, entryPath, ModeRegular, []byte("payload"))
			destination := destinationIn(t)
			result, err := Materialize(context.Background(), source, destination, Options{})
			if result != nil || !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("result=%+v err=%v, want unsafe path", result, err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination was created for an unsafe path: %v", statErr)
			}
		})
	}
}

func TestMaterializeRefusesDuplicateAndDirectoryConflicts(t *testing.T) {
	duplicate := newFakeSource(t, nil, nil)
	duplicate.addBlob(t, "same.txt", ModeRegular, []byte("first"))
	duplicate.addBlob(t, "same.txt", ModeRegular, []byte("second"))
	if result, err := Materialize(context.Background(), duplicate, destinationIn(t), Options{}); result != nil || !errors.Is(err, ErrPathConflict) {
		t.Fatalf("duplicate result=%+v err=%v, want path conflict", result, err)
	}

	shadowed := newFakeSource(t, map[string]string{
		"app":         "file that shadows a directory",
		"app/main.go": "package main",
	}, nil)
	if result, err := Materialize(context.Background(), shadowed, destinationIn(t), Options{}); result != nil || !errors.Is(err, ErrPathConflict) {
		t.Fatalf("shadowed result=%+v err=%v, want path conflict", result, err)
	}
}

func TestMaterializeFailsOnFilesystemNameCollision(t *testing.T) {
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, "Case.txt"), []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(probe, "case.txt")); errors.Is(err, os.ErrNotExist) {
		t.Skip("this filesystem distinguishes name case, so no collision is expected")
	}

	source := newFakeSource(t, map[string]string{"Case.txt": "upper", "case.txt": "lower"}, nil)
	destination := destinationIn(t)
	result, err := Materialize(context.Background(), source, destination, Options{})
	if result != nil || !errors.Is(err, ErrPathConflict) {
		t.Fatalf("result=%+v err=%v, want path conflict", result, err)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("collision left %d entries, want the single written file", len(entries))
	}
}

func TestMaterializeEnforcesLimitsBeforeWriting(t *testing.T) {
	cases := map[string]struct {
		limits Limits
		build  func(*testing.T) *fakeSource
	}{
		"entries": {
			limits: Limits{MaxEntries: 1},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"a.txt": "a", "b.txt": "b"}, nil)
			},
		},
		"file bytes": {
			limits: Limits{MaxFileBytes: 4},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"big.txt": "0123456789"}, nil)
			},
		},
		"total bytes": {
			limits: Limits{MaxTotalBytes: 5},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"a.txt": "aaa", "b.txt": "bbb"}, nil)
			},
		},
		"path depth": {
			limits: Limits{MaxPathDepth: 2},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"a/b/c.txt": "deep"}, nil)
			},
		},
		"path bytes": {
			limits: Limits{MaxPathBytes: 4},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"longer-name.txt": "x"}, nil)
			},
		},
		"name bytes": {
			limits: Limits{MaxNameBytes: 3},
			build: func(t *testing.T) *fakeSource {
				return newFakeSource(t, map[string]string{"dir/longer.txt": "x"}, nil)
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			destination := destinationIn(t)
			result, err := Materialize(context.Background(), testCase.build(t), destination, Options{Limits: testCase.limits})
			if result != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("result=%+v err=%v, want limit exceeded", result, err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a bounded refusal created the destination: %v", statErr)
			}
		})
	}
}

func TestMaterializeRecordsEffectiveLimits(t *testing.T) {
	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	result, err := Materialize(context.Background(), source, destinationIn(t), Options{Limits: Limits{MaxEntries: 7}})
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultLimits()
	if result.EffectiveLimits.MaxEntries != 7 || result.EffectiveLimits.MaxFileBytes != defaults.MaxFileBytes {
		t.Fatalf("effective limits were not recorded: %+v", result.EffectiveLimits)
	}
	if _, err := Materialize(context.Background(), source, destinationIn(t), Options{Limits: Limits{MaxEntries: -1}}); err == nil {
		t.Fatal("negative limit unexpectedly accepted")
	}
}

func TestMaterializeRefusesExistingDestinationAndLeavesItIntact(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "existing")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(destination, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	source := newFakeSource(t, map[string]string{"sentinel.txt": "replacement"}, nil)
	result, err := Materialize(context.Background(), source, destination, Options{})
	if result != nil || !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("result=%+v err=%v, want existing destination", result, err)
	}
	for path, want := range map[string]string{sentinelPath: "sentinel", outsidePath: "outside"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != want {
			t.Fatalf("%s changed to %q", path, content)
		}
	}
}

func TestMaterializeRejectsRelativeDestination(t *testing.T) {
	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	if _, err := Materialize(context.Background(), source, "relative/dir", Options{}); !errors.Is(err, ErrInvalidDestination) {
		t.Fatalf("err=%v, want invalid destination", err)
	}
	uncleaned := t.TempDir() + string(filepath.Separator) + "a" + string(filepath.Separator) + ".." +
		string(filepath.Separator) + "b"
	if _, err := Materialize(context.Background(), source, uncleaned, Options{}); !errors.Is(err, ErrInvalidDestination) {
		t.Fatalf("err=%v, want invalid destination", err)
	}
	if _, err := Materialize(context.Background(), source, "", Options{}); !errors.Is(err, ErrInvalidDestination) {
		t.Fatalf("err=%v, want invalid destination", err)
	}
}

func TestMaterializeNeverWritesThroughASymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "source")

	source := newFakeSource(t, nil, nil)
	source.addBlob(t, "a.txt", ModeRegular, []byte("first"))
	source.addBlob(t, "escape/sentinel.txt", ModeRegular, []byte("overwritten"))
	// After the first file is written the destination exists, so a concurrent
	// process can plant a symbolic link the export would otherwise follow.
	planted := false
	source.beforeRead = func(string) error {
		if planted {
			return nil
		}
		if _, err := os.Stat(destination); err != nil {
			return nil
		}
		planted = true
		return os.Symlink(outside, filepath.Join(destination, "escape"))
	}

	result, err := Materialize(context.Background(), source, destination, Options{})
	if result != nil || err == nil {
		t.Fatalf("result=%+v err=%v, want a refusal", result, err)
	}
	content, readErr := os.ReadFile(sentinelPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "sentinel" {
		t.Fatalf("the export wrote through a symlink parent: %q", content)
	}
}

func TestMaterializeStopsOnCancellationWithoutAManifest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := newFakeSource(t, nil, nil)
	source.addBlob(t, "a.txt", ModeRegular, []byte("first"))
	source.addBlob(t, "b.txt", ModeRegular, []byte("second"))
	source.beforeRead = func(string) error {
		cancel()
		return nil
	}
	destination := destinationIn(t)

	result, err := Materialize(ctx, source, destination, Options{})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v err=%v, want cancellation without a manifest", result, err)
	}
	if _, statErr := os.Stat(destination); statErr != nil {
		t.Fatalf("the cancelled destination is not left for the caller to remove: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "b.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancellation still wrote the later entry: %v", statErr)
	}
}

// A source can notice cancellation and still return usable data. These cases
// have no later entry to fail on, so a missed recheck would report success.
func TestMaterializeStopsWhenACancellingSourceStillReturnsValidData(t *testing.T) {
	t.Run("one file", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		source := newFakeSource(t, nil, nil)
		source.addBlob(t, "only.txt", ModeRegular, []byte("only"))
		source.beforeRead = func(string) error {
			cancel()
			return nil
		}
		destination := destinationIn(t)

		result, err := Materialize(ctx, source, destination, Options{})
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%+v err=%v, want cancellation without a manifest", result, err)
		}
		if _, statErr := os.Stat(filepath.Join(destination, "only.txt")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the cancelled single file was still written: %v", statErr)
		}
	})

	t.Run("empty tree", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		source := newFakeSource(t, nil, nil)
		source.beforeList = func() error {
			cancel()
			return nil
		}
		destination := destinationIn(t)

		result, err := Materialize(ctx, source, destination, Options{})
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%+v err=%v, want cancellation without a manifest", result, err)
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a cancelled empty listing still created the destination: %v", statErr)
		}
	})

	// Cancelling as the final bytes are handed back must also withhold the
	// manifest, not just cancellation observed before the read.
	t.Run("while returning the last file", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		source := newFakeSource(t, nil, nil)
		source.addBlob(t, "only.txt", ModeRegular, []byte("only"))
		source.afterRead = func(string) { cancel() }
		result, err := Materialize(ctx, source, destinationIn(t), Options{})
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%+v err=%v, want cancellation without a manifest", result, err)
		}
	})
}

func TestMaterializeDetectsMismatchedObjectBytes(t *testing.T) {
	truncated := newFakeSource(t, nil, nil)
	truncated.addBlob(t, "a.txt", ModeRegular, []byte("exact bytes"))
	for oid := range truncated.blobs {
		truncated.blobs[oid] = []byte("short")
	}
	if result, err := Materialize(context.Background(), truncated, destinationIn(t), Options{}); result != nil || !errors.Is(err, ErrObjectMismatch) {
		t.Fatalf("result=%+v err=%v, want object mismatch", result, err)
	}

	swapped := newFakeSource(t, nil, nil)
	swapped.addBlob(t, "a.txt", ModeRegular, []byte("original"))
	for oid := range swapped.blobs {
		swapped.blobs[oid] = []byte("replaced")
	}
	if result, err := Materialize(context.Background(), swapped, destinationIn(t), Options{}); result != nil || !errors.Is(err, ErrObjectMismatch) {
		t.Fatalf("result=%+v err=%v, want object mismatch", result, err)
	}

	missingSize := newFakeSource(t, nil, nil)
	missingSize.addEntry(Entry{Path: "a.txt", OID: strings.Repeat("e", 40), Mode: ModeRegular, Type: "blob", Size: -1})
	if result, err := Materialize(context.Background(), missingSize, destinationIn(t), Options{}); result != nil || !errors.Is(err, ErrObjectMismatch) {
		t.Fatalf("result=%+v err=%v, want object mismatch", result, err)
	}
}

func TestMaterializeRejectsUnsupportedObjectFormat(t *testing.T) {
	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	source.format = "md5"
	destination := destinationIn(t)
	if _, err := Materialize(context.Background(), source, destination, Options{}); !errors.Is(err, ErrUnsupportedObjectFormat) {
		t.Fatalf("err=%v, want unsupported object format", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an unsupported format created the destination: %v", statErr)
	}
}

func TestMaterializePreservesLFSPointerBytesAndMarksThem(t *testing.T) {
	pointer := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\nsize 12\n"
	source := newFakeSource(t, map[string]string{
		"media/clip.bin": pointer,
		"README.md":      "version https://git-lfs.github.com/spec/v1 mentioned in prose\n",
	}, nil)
	destination := destinationIn(t)

	result, err := Materialize(context.Background(), source, destination, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LFSPointerPaths) != 1 || result.LFSPointerPaths[0] != "media/clip.bin" {
		t.Fatalf("pointer detection reported %v", result.LFSPointerPaths)
	}
	content, err := os.ReadFile(filepath.Join(destination, "media", "clip.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != pointer {
		t.Fatalf("pointer bytes changed: %q", content)
	}
	for _, file := range result.Files {
		switch file.Path {
		case "README.md":
			if file.LFSPointer != nil {
				t.Fatal("prose mentioning the pointer spec was marked as a pointer")
			}
		case "media/clip.bin":
			if file.LFSPointer == nil {
				t.Fatal("a valid pointer was not decoded")
			}
			if file.LFSPointer.Size != 12 ||
				file.LFSPointer.OID != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
				t.Fatalf("decoded pointer fields are wrong: %+v", file.LFSPointer)
			}
		}
	}
}

func TestMaterializeRejectsMalformedLFSPointers(t *testing.T) {
	valid := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\nsize 12\n"
	source := newFakeSource(t, map[string]string{
		"valid.bin":         valid,
		"short-oid.bin":     strings.Replace(valid, "2cf24dba", "2cf24db", 1),
		"upper-oid.bin":     strings.Replace(valid, "2cf24dba", "2CF24DBA", 1),
		"bad-hash.bin":      strings.Replace(valid, "sha256:", "sha1:", 1),
		"bad-size.bin":      strings.Replace(valid, "size 12", "size twelve", 1),
		"negative-size.bin": strings.Replace(valid, "size 12", "size -12", 1),
		"bad-version.bin":   strings.Replace(valid, "spec/v1", "spec/v9", 1),
		"wrong-order.bin": "version https://git-lfs.github.com/spec/v1\nsize 12\n" +
			"oid sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n",
		"extra-key.bin": valid + "trailer git-lfs\n",
		"no-space.bin":  strings.Replace(valid, "size 12", "size=12", 1),
	}, nil)

	result, err := Materialize(context.Background(), source, destinationIn(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LFSPointerPaths) != 1 || result.LFSPointerPaths[0] != "valid.bin" {
		t.Fatalf("malformed pointers were accepted: %v", result.LFSPointerPaths)
	}
}

func TestLFSPointerAcceptsSupportedFormsAndBoundarySizes(t *testing.T) {
	digest := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	body := "oid sha256:" + digest + "\nsize 12\n"
	for name, alias := range map[string]string{
		"current":     "https://git-lfs.github.com/spec/v1",
		"pre-release": "https://hawser.github.com/spec/v1",
		"alpha":       "http://git-media.io/v/2",
	} {
		if ParseLFSPointer([]byte("version "+alias+"\n"+body)) == nil {
			t.Fatalf("%s version alias was rejected", name)
		}
	}
	canonical := "version https://git-lfs.github.com/spec/v1\n" + body
	if ParseLFSPointer([]byte(strings.ReplaceAll(canonical, "\n", "\r\n"))) == nil {
		t.Fatal("CRLF pointer was rejected")
	}
	if parseLFSPointer([]byte(strings.TrimSuffix(canonical, "\n"))) == nil {
		t.Fatal("pointer without a trailing newline was rejected")
	}

	extended := "version https://git-lfs.github.com/spec/v1\next-0-shake " +
		"sha256:" + digest + "\n" + body
	pointer := ParseLFSPointer([]byte(extended))
	if pointer == nil || len(pointer.Extensions) != 1 || pointer.Extensions[0] != "ext-0-shake" {
		t.Fatalf("extension pointer decoded as %+v", pointer)
	}
	duplicate := "version https://git-lfs.github.com/spec/v1\next-0-a sha256:" + digest +
		"\next-0-b sha256:" + digest + "\n" + body
	if parseLFSPointer([]byte(duplicate)) != nil {
		t.Fatal("duplicate extension priority was accepted")
	}

	// The specification requires a pointer to be under 1024 bytes, so exactly
	// 1024 is not a pointer while 1023 still is. Padding uses interior blank
	// lines, which both this parser and the reference decoder skip, so only the
	// size changes between the two cases.
	pad := func(total int) []byte {
		pointer := "version https://git-lfs.github.com/spec/v1\n" + body
		blanks := total - len(pointer)
		if blanks < 0 {
			t.Fatalf("the canonical pointer is already %d bytes", len(pointer))
		}
		return []byte("version https://git-lfs.github.com/spec/v1\n" +
			strings.Repeat("\n", blanks) + body)
	}
	if atLimit := pad(1023); len(atLimit) != 1023 || parseLFSPointer(atLimit) == nil {
		t.Fatalf("a 1023-byte pointer of length %d was rejected", len(atLimit))
	}
	if overLimit := pad(1024); len(overLimit) != 1024 || parseLFSPointer(overLimit) != nil {
		t.Fatalf("a 1024-byte blob of length %d was accepted as a pointer", len(overLimit))
	}
	// EmptyPointer normalization is outside nonempty serialized-pointer detection.
	if parseLFSPointer(nil) != nil || parseLFSPointer([]byte{}) != nil {
		t.Fatal("an ordinary empty blob was classified as a serialized pointer")
	}
}

func TestMaterializeReportsListingFailuresWithoutCreatingTheDestination(t *testing.T) {
	source := newFakeSource(t, map[string]string{"a.txt": "a"}, nil)
	source.listErr = errors.New("object listing exceeded its limit")
	destination := destinationIn(t)
	if _, err := Materialize(context.Background(), source, destination, Options{}); err == nil ||
		!strings.Contains(err.Error(), "list check source tree") {
		t.Fatalf("err=%v, want a listing failure", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a listing failure created the destination: %v", statErr)
	}
}

func TestCleanupWorkspaceRootRemovesOnlyAuthenticatedJobEnvelopes(t *testing.T) {
	root := t.TempDir()
	ownedID := strings.Repeat("a", 32)
	workspace, err := AcquireWorkspaceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	owned, source, err := workspace.PrepareJob(ownedID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "keep-me")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace.Close()

	removed, more, err := CleanupWorkspaceRoot(root, 10)
	if err == nil || more || removed != 1 {
		t.Fatalf("cleanup removed=%d more=%v err=%v, want an observable preserved entry", removed, more, err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned workspace still exists: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated directory was removed: %v", err)
	}
}

func TestCleanupWorkspaceRootPreservesUnownedHexDirectory(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, strings.Repeat("b", 32))
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(unrelated, "unrelated-fixture.txt")
	if err := os.WriteFile(sentinel, []byte("not a check workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, _, err := CleanupWorkspaceRoot(root, 10)
	if removed != 0 || err == nil {
		t.Fatalf("cleanup removed=%d err=%v, want a fail-closed refusal", removed, err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "not a check workspace" {
		t.Fatalf("unowned sentinel content=%q err=%v", content, err)
	}
}

func TestOwnedWorkspaceRootReportsAndPreservesUnknownHexDirectory(t *testing.T) {
	root := t.TempDir()
	workspace, err := AcquireWorkspaceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Close()
	unknown := filepath.Join(root, strings.Repeat("d", 32))
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(unknown, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, _, err := CleanupWorkspaceRoot(root, 10)
	if removed != 0 || err == nil {
		t.Fatalf("cleanup removed=%d err=%v, want an observable refusal", removed, err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve" {
		t.Fatalf("unknown sentinel content=%q err=%v", content, err)
	}
}

func TestWorkspaceRootLockCoversActiveSourceLifecycle(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireWorkspaceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, source, err := first.PrepareJob(strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(context.Background(), newFakeSource(t, map[string]string{
		workspaceJobMarker: "repository content may use the envelope marker name",
	}, nil), source, Options{})
	if err != nil || materialized.Destination != source {
		t.Fatalf("materialize destination=%v err=%v", materialized, err)
	}
	if second, err := AcquireWorkspaceRoot(root); err == nil {
		second.Close()
		t.Fatal("a second lifecycle acquired the active workspace root")
	}
}

func TestVerifyResultDetectsWorkspaceChanges(t *testing.T) {
	source := newFakeSource(t, map[string]string{"nested/a.txt": "a"}, nil)
	destination := destinationIn(t)
	result, err := Materialize(context.Background(), source, destination, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if clean, err := VerifyResult(context.Background(), result); err != nil || !clean {
		t.Fatalf("clean=%v err=%v", clean, err)
	}
	if err := os.Mkdir(filepath.Join(destination, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "build", "generated.txt"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if clean, err := VerifyResult(context.Background(), result); err != nil || !clean {
		t.Fatalf("generated output clean=%v err=%v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(destination, "nested", "a.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if clean, err := VerifyResult(context.Background(), result); err != nil || clean {
		t.Fatalf("modified clean=%v err=%v", clean, err)
	}
}

func TestMaterializeAcceptsAnEmptyTree(t *testing.T) {
	source := newFakeSource(t, nil, nil)
	destination := destinationIn(t)
	result, err := Materialize(context.Background(), source, destination, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 || result.TotalBytes != 0 {
		t.Fatalf("unexpected empty-tree result: %+v", result)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty tree produced %d entries, err=%v", len(entries), err)
	}
}

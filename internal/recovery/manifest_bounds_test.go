package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
)

// A manifest is written in version 10 while its records fit that version
// and its size fits what version 10 readers accept, and in version 11
// otherwise. A version 10 file larger than that is refused like the earlier
// releases refuse it.
func TestManifestFormatFollowsWhatItsReadersAccept(t *testing.T) {
	manifest := Manifest{Format: backupFormat, AccessMode: "open", AdminHash: "synthetic-hash", Repositories: []RepositoryManifest{{ID: "project", Name: "project", Description: strings.Repeat("d", 400)}}}
	for _, test := range []struct {
		format10Limit int64
		want          int
	}{
		{format10Limit: 1 << 20, want: closedPullRequestBackupVersion},
		{format10Limit: 200, want: backupVersion},
	} {
		version, err := backupManifestVersion(manifest, test.format10Limit, manifestLimit)
		if err != nil || version != test.want {
			t.Fatalf("version 10 limit %d chose version %d, want %d, err=%v", test.format10Limit, version, test.want, err)
		}
	}

	// Padding keeps the JSON valid while it passes the version 10 limit.
	path := filepath.Join(t.TempDir(), manifestName)
	manifest.Version = closedPullRequestBackupVersion
	var content bytes.Buffer
	noErr(t, writeManifest(&content, manifest))
	content.Write(bytes.Repeat([]byte(" "), format10ManifestLimit))
	noErr(t, os.WriteFile(path, content.Bytes(), 0o600))
	if _, err := readManifest(path); !errors.Is(err, errManifestTooLarge) {
		t.Fatalf("oversized version 10 manifest error=%v", err)
	}
}

// Writing one record at a time gives the bytes encoding/json gives for the
// whole manifest, so every release reads it as before.
func TestManifestIsWrittenAsEncodingJSONWritesIt(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	for _, manifest := range []Manifest{
		{Format: backupFormat, Version: backupVersion},
		{
			Format: backupFormat, Version: closedPullRequestBackupVersion, CreatedAt: at, AccessMode: "shared", AccessHash: "access", AdminHash: "admin",
			Repositories:         []RepositoryManifest{{ID: "one", Name: "One", Refs: []Ref{{Name: "refs/heads/main", OID: strings.Repeat("a", 40)}}}, {ID: "two", Empty: true}},
			PullRequests:         []PullRequestManifest{{RepositoryID: "one", Number: 1, Body: "<a & b>\u2028"}},
			PullRequestRevisions: []PullRequestRevisionManifest{},
			ImportRunOrderKnown:  true,
		},
	} {
		var written, encoded bytes.Buffer
		noErr(t, writeManifest(&written, manifest))
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		noErr(t, encoder.Encode(manifest))
		if written.String() != encoded.String() {
			t.Fatalf("manifest written as\n%s\nencoding/json writes\n%s", written.Bytes(), encoded.Bytes())
		}
	}
}

func TestManifestIsNotHTMLEscaped(t *testing.T) {
	var content bytes.Buffer
	noErr(t, writeManifest(&content, Manifest{Format: backupFormat, Version: backupVersion, PullRequests: []PullRequestManifest{{Body: "<a & b>"}}}))
	if !bytes.Contains(content.Bytes(), []byte(`"body":"<a & b>"`)) {
		t.Fatalf("manifest escaped HTML characters:\n%s", content.Bytes())
	}
}

// A state whose records do not fit a backup is refused before anything is
// written, with its size and the limit, instead of being cut short.
func TestBackupRefusesAStateLargerThanABackupHolds(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	runner := &recordingRecoveryRunner{delegate: manager.Git}
	output := filepath.Join(root, "backup")
	err := create(ctx, store, manager, runner, output, 100)
	if err == nil || !strings.Contains(err.Error(), "cannot back up this state: its OwnGit records take 1 MiB, and a backup holds at most 0 MiB") {
		t.Fatalf("backup of a state above the limit err=%v", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.arguments, "bundle") {
			t.Fatalf("backup bundled a repository before refusing the state: %v", call.arguments)
		}
	}
	assertNoRecoveryOutputOrStages(t, output, ".owngit-backup-")
}

// Reading a supplied manifest needs memory for its records only: the
// whitespace between tokens costs nothing, and a file past the limit, one
// that grows past it while read, or a string longer than any record holds is
// refused with memory bounded by the limits rather than by the input.
func TestManifestReadingIsBounded(t *testing.T) {
	const prefix = `{"format":"owngit-offline-backup","version":11,"created_at":"2026-09-29T00:00:00Z","access_mode":"open","admin_password_hash":"synthetic","repositories":[]`
	whitespace := func(size int64) io.Reader { return &repeatReader{pattern: []byte(" \n\t\r"), remaining: size} }
	type input struct {
		reader      io.Reader
		size, limit int64
	}
	for _, test := range []struct {
		name     string
		input    func() input
		want     string
		maxAlloc uint64
	}{
		{
			name: "whitespace between and after tokens",
			input: func() input {
				reader := io.MultiReader(strings.NewReader(prefix), whitespace(128<<20), strings.NewReader(`,"pull_requests"`), whitespace(64<<20), strings.NewReader(`:[]}`), whitespace(64<<20))
				return input{reader, int64(len(prefix)) + 256<<20 + 20, manifestLimit}
			},
			maxAlloc: 1 << 20,
		},
		{
			name: "file larger than the limit",
			input: func() input {
				return input{failingReader{t}, manifestLimit + 1, manifestLimit}
			},
			want:     "backup manifest is larger than the 1024 MiB a backup holds",
			maxAlloc: 1 << 20,
		},
		{
			name: "records growing past the limit while read",
			input: func() input {
				record := []byte(`{"repository_id":"project","pull_request_number":1,"sequence":1,"source_oid":"` + strings.Repeat("a", 40) + `","target_oid":"` + strings.Repeat("a", 40) + `","status":"approved","provenance":"supplied_external_tool","created_at":"2026-09-29T00:00:00Z"},`)
				reader := io.MultiReader(strings.NewReader(prefix+`,"pull_request_reviews":[`), &repeatReader{pattern: record, remaining: 1 << 40})
				return input{reader, int64(len(prefix)), 4 << 20}
			},
			want:     "backup manifest is larger than the 4 MiB a backup holds",
			maxAlloc: 32 << 20,
		},
		{
			name: "a string longer than any record holds",
			input: func() input {
				reader := io.MultiReader(strings.NewReader(prefix+`,"pull_requests":[{"body":"`), &repeatReader{pattern: []byte("a"), remaining: manifestLimit})
				return input{reader, manifestLimit, manifestLimit}
			},
			want:     "backup manifest holds a text longer than any OwnGit record",
			maxAlloc: 6 * maxManifestStringBytes,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			given := test.input()
			var err error
			allocated := allocatedBy(func() { _, err = decodeManifest(given.reader, given.size, given.limit) })
			if test.want == "" {
				noErr(t, err)
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			if allocated > test.maxAlloc {
				t.Fatalf("reading allocated %d bytes, want at most %d", allocated, test.maxAlloc)
			}
		})
	}
}

// A backup whose manifest is past the limit or holds an overlong string is
// refused before restore creates any folder.
func TestRestoreRefusesAnOversizedManifestBeforeAnyStage(t *testing.T) {
	hash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	var encoded bytes.Buffer
	noErr(t, writeManifest(&encoded, Manifest{Format: backupFormat, Version: backupVersion, CreatedAt: time.Now().UTC(), AccessMode: "open", AdminHash: hash}))
	valid := encoded.Bytes()
	for _, test := range []struct {
		name  string
		write func(path string)
		want  string
	}{
		{
			name: "sparse file past the limit",
			write: func(path string) {
				noErr(t, os.WriteFile(path, valid, 0o600))
				noErr(t, os.Truncate(path, manifestLimit+1))
			},
			want: "backup manifest is larger than the 1024 MiB a backup holds",
		},
		{
			name: "one overlong string",
			write: func(path string) {
				content := append(bytes.TrimSuffix(bytes.TrimSpace(valid), []byte("}")), `,"pull_requests":[{"body":"`...)
				content = append(content, bytes.Repeat([]byte("a"), maxManifestStringBytes+1)...)
				noErr(t, os.WriteFile(path, append(content, `"}]}`...), 0o600))
			},
			want: "backup manifest holds a text longer than any OwnGit record",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			backup := filepath.Join(root, "backup")
			noErr(t, os.Mkdir(backup, 0o700))
			test.write(filepath.Join(backup, manifestName))
			stateTarget, repositoryTarget := filepath.Join(root, "state"), filepath.Join(root, "repositories")
			if err := Restore(context.Background(), backup, stateTarget, repositoryTarget, ""); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("restore error=%v, want %q", err, test.want)
			}
			for _, target := range []string{stateTarget, repositoryTarget} {
				assertNoRecoveryOutputOrStages(t, target, ".owngit-restore-")
			}
		})
	}
}

func allocatedBy(action func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	action()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// repeatReader yields remaining bytes of pattern repeated, without holding
// them.
type repeatReader struct {
	pattern   []byte
	offset    int
	remaining int64
}

func (reader *repeatReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	for index := range buffer {
		buffer[index] = reader.pattern[reader.offset]
		reader.offset = (reader.offset + 1) % len(reader.pattern)
	}
	reader.remaining -= int64(len(buffer))
	return len(buffer), nil
}

type failingReader struct{ t *testing.T }

func (reader failingReader) Read([]byte) (int, error) {
	reader.t.Error("a manifest past the limit was read")
	return 0, io.EOF
}

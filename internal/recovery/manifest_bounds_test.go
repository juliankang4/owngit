package recovery

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A manifest is written in version 10 while its records fit that version
// and its size fits what version 10 readers accept, and in version 11
// otherwise. A version 10 file larger than that is refused like the earlier
// releases refuse it, and version 11 has no size limit of its own.
func TestManifestFormatFollowsWhatItsReadersAccept(t *testing.T) {
	manifest := Manifest{Format: backupFormat, AccessMode: "open", AdminHash: "synthetic-hash", Repositories: []RepositoryManifest{{ID: "project", Name: "project", Description: strings.Repeat("d", 400)}}}
	for _, test := range []struct {
		limit int64
		want  int
	}{
		{limit: 1 << 20, want: closedPullRequestBackupVersion},
		{limit: 200, want: backupVersion},
	} {
		path := filepath.Join(t.TempDir(), manifestName)
		file, err := os.Create(path)
		noErr(t, err)
		written := manifest
		noErr(t, writeBackupManifest(file, &written, test.limit))
		noErr(t, file.Close())
		content, err := os.ReadFile(path)
		noErr(t, err)
		if written.Version != test.want || !bytes.Contains(content, []byte(`"version": `+strconv.Itoa(test.want)+`,`)) {
			t.Fatalf("limit %d wrote version %d, want %d:\n%.200s", test.limit, written.Version, test.want, content)
		}
		if bytes.Count(content, []byte(`"format"`)) != 1 {
			t.Fatalf("limit %d left the discarded version 10 attempt in the file", test.limit)
		}
	}

	// Padding keeps the JSON valid while it passes the version 10 limit.
	path := filepath.Join(t.TempDir(), manifestName)
	var content bytes.Buffer
	manifest.Version = closedPullRequestBackupVersion
	noErr(t, writeManifest(&content, manifest))
	content.Write(bytes.Repeat([]byte(" "), format10ManifestLimit))
	noErr(t, os.WriteFile(path, content.Bytes(), 0o600))
	if _, err := readManifest(path); !errors.Is(err, errManifestTooLarge) {
		t.Fatalf("oversized version 10 manifest error=%v", err)
	}
}

func TestManifestIsNotHTMLEscaped(t *testing.T) {
	var content bytes.Buffer
	noErr(t, writeManifest(&content, Manifest{Format: backupFormat, Version: backupVersion, PullRequests: []PullRequestManifest{{Body: "<a & b>"}}}))
	if !bytes.Contains(content.Bytes(), []byte(`"body": "<a & b>"`)) {
		t.Fatalf("manifest escaped HTML characters:\n%s", content.Bytes())
	}
}

func TestManifestWritePreservesUnderlyingIOErrors(t *testing.T) {
	sentinel := errors.New("synthetic manifest I/O failure")
	if err := writeManifest(errorManifestWriter{err: sentinel}, Manifest{Format: backupFormat, Version: backupVersion}); !errors.Is(err, sentinel) {
		t.Fatalf("write error=%v", err)
	}
}

type errorManifestWriter struct{ err error }

func (writer errorManifestWriter) Write([]byte) (int, error) { return 0, writer.err }

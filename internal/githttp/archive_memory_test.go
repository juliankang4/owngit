package githttp

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/repository"
)

// Git rebuilds a file it stores as a delta in memory while it writes the
// archive, and the large-file threshold does not bound that memory: measured
// on Git 2.47.3, archiving a commit whose 220 MiB file was stored as a delta
// used 455 MiB of resident memory, and a read in parallel was killed. An
// archive of a commit that lists such a file is therefore refused before Git
// starts, with a message that names the file and says what the owner can do
// instead. A file Git stores on its own is archived as before, and a computer
// with an unknown memory ceiling refuses nothing.
func TestArchiveRefusesAFileThatCannotBeRebuiltInMemory(t *testing.T) {
	handler, _, remote, work := pushFixture(t)
	logs := captureLog(t)
	line := []byte("a line of the large archive fixture\n")
	noErr(t, os.WriteFile(filepath.Join(work, "big.txt"), bytes.Repeat(line, 4096), 0o600))
	runHTTPGit(t, work, "add", "big.txt")
	runHTTPGit(t, work, "commit", "-q", "-m", "large first")
	noErr(t, os.WriteFile(filepath.Join(work, "big.txt"), append(bytes.Repeat(line, 4096), []byte("a change\n")...), 0o600))
	runHTTPGit(t, work, "add", "big.txt")
	runHTTPGit(t, work, "commit", "-q", "-m", "large second")
	runHTTPGit(t, work, "push", "-q", "origin", "main")
	older := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", "main~1")
	newer := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
	runHTTPGit(t, "", "--git-dir", remote, "repack", "-a", "-d", "-f", "--window=10", "--depth=1")
	olderBlob := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", older+":big.txt")
	facts := gitInput(t, remote, []byte(olderBlob+"\n"), "cat-file", "--batch-check=%(objecttype) %(deltabase)")
	if fields := strings.Fields(facts); len(fields) < 2 || strings.Trim(fields[1], "0") == "" {
		t.Fatalf("the fixture no longer stores the older file as a delta: %s", facts)
	}

	restore := archiveRebuildBound
	archiveRebuildBound = func() int64 { return 1 << 17 }
	defer func() { archiveRebuildBound = restore }()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/repositories/sample/archive", nil)
	err := handler.ServeArchive(recorder, request, "sample", older, ArchiveZip, "sample", "sample.zip")
	var failure *ArchiveError
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict {
		t.Fatalf("archive error=%v, want a %d refusal", err, http.StatusConflict)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("the refused archive wrote %d bytes", recorder.Body.Len())
	}
	if failure.Reason != ArchiveRefusalMemory || !strings.Contains(failure.Detail, "big.txt") || failure.Detail == "" {
		t.Fatalf("the refusal does not carry the memory reason and the file: %+v", failure)
	}
	if !strings.Contains(failure.Message, "big.txt") || !strings.Contains(failure.Message, "more memory") {
		t.Fatalf("the English message does not name the file and what to do: %q", failure.Message)
	}
	if !strings.Contains(logs.String(), "needs about") {
		t.Fatalf("the server log does not name the reason:\n%s", logs.String())
	}

	// The same commit archives without the bound, and the file Git stores on
	// its own archives even with it, so the refusal follows the memory the
	// contents need and not the size of the file.
	archiveRebuildBound = func() int64 { return 1 << 40 }
	assertArchiveSucceeds(t, handler, older)
	archiveRebuildBound = func() int64 { return 1 << 17 }
	assertArchiveSucceeds(t, handler, newer)
}

func assertArchiveSucceeds(t *testing.T, handler *Handler, commit string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/repositories/sample/archive", nil)
	if err := handler.ServeArchive(recorder, request, "sample", commit, ArchiveZip, "sample", "sample.zip"); err != nil {
		t.Fatalf("archive error=%v, want an archive", err)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("the archive wrote no bytes")
	}
}

// The number of versions the deep-chain fixture commits. Git's own packing
// keeps chains far shorter than the bound the check applies, so this builds a
// repository that no ordinary pack would produce.
const deepChainArchiveVersions = 340

// A repository whose packing holds a delta chain deeper than the check follows
// cannot have the memory of its files priced. That refusal has its own reason
// and its own English sentence: it names the repository's packing, and does
// not send the owner looking for files that a file-count message would imply.
func TestArchiveRefusesADeepDeltaChainWithItsOwnReason(t *testing.T) {
	logs := captureLog(t)
	failure := archivePrecheckFailure("sample", fmt.Errorf("check the commit's paths: %w", repository.ErrDeltaChainTooDeep))
	if failure == nil || failure.Status != http.StatusConflict {
		t.Fatalf("a chain deeper than the check follows answers %+v, want a %d refusal", failure, http.StatusConflict)
	}
	if failure.Reason != ArchiveRefusalDeepChain {
		t.Fatalf("the refusal names %q, want %q", failure.Reason, ArchiveRefusalDeepChain)
	}
	if !strings.Contains(failure.Message, "chain") {
		t.Fatalf("the English message does not name the packing: %q", failure.Message)
	}
	if !strings.Contains(logs.String(), "deeper than Git builds") {
		t.Fatalf("the server log does not name the shape:\n%s", logs.String())
	}
	// The general tree-check refusal keeps its own reason and message, so the
	// two are not confused with each other.
	missing := archivePrecheckFailure("sample", repository.ErrTreeCheckTooLarge)
	if missing == nil || missing.Reason != ArchiveRefusalManyFiles {
		t.Fatalf("a tree the check could not read answers %+v, want %q", missing, ArchiveRefusalManyFiles)
	}
	if missing.Message == failure.Message {
		t.Error("the deep-chain refusal repeats the file-count message")
	}
}

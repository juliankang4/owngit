package githttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// archiveFixture is a repository with one commit holding a small text file,
// a nested file, and an incompressible file of the given size.
func archiveFixture(t *testing.T, randomBytes int) (*Handler, string) {
	t.Helper()
	manager, runner := newHTTPTestRepository(t)
	repositoryPath, err := manager.Path("sample")
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "-q", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Archive Test")
	runHTTPGit(t, work, "config", "user.email", "archive-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Sample\n"), 0o600))
	noErr(t, os.MkdirAll(filepath.Join(work, "dir"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, "dir", "a.txt"), []byte("nested\n"), 0o600))
	random := make([]byte, randomBytes)
	_, err = rand.Read(random)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "random.bin"), random, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "sample")
	runHTTPGit(t, work, "push", "-q", repositoryPath, "HEAD:refs/heads/main")
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	return handler, httpGitOutput(t, work, "rev-parse", "HEAD")
}

func serveArchiveOf(t *testing.T, handler *Handler, commitOID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		format := request.URL.Query().Get("format")
		err := handler.ServeArchive(writer, request, "sample", commitOID, format, "sample-main", "sample-main."+format)
		var failure *ArchiveError
		if errors.As(err, &failure) {
			http.Error(writer, failure.Message, failure.Status)
		} else if err != nil {
			t.Errorf("ServeArchive returned %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// fetchArchive returns the response and its body, and the error that ended
// reading the body.
func fetchArchive(t *testing.T, target string) (*http.Response, []byte, error) {
	t.Helper()
	response, err := http.Get(target)
	noErr(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response, body, err
}

func zipEntries(content []byte) (map[string]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(opened)
		opened.Close()
		if err != nil {
			return nil, err
		}
		entries[file.Name] = string(data)
	}
	return entries, nil
}

func tarGzEntries(content []byte) (map[string]string, error) {
	decompressed, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(decompressed)
	entries := map[string]string{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		entries[header.Name] = string(data)
	}
	// Reading to the end checks the gzip trailer.
	if _, err := io.Copy(io.Discard, decompressed); err != nil {
		return nil, err
	}
	return entries, nil
}

func entryNames(entries map[string]string) string {
	var names []string
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestArchiveStreamsTheCommitAsZipAndTarGz(t *testing.T) {
	handler, commitOID := archiveFixture(t, 1024)
	server := serveArchiveOf(t, handler, commitOID)
	for format, read := range map[string]func([]byte) (map[string]string, error){ArchiveZip: zipEntries, ArchiveTarGz: tarGzEntries} {
		response, body, err := fetchArchive(t, server.URL+"?format="+format)
		noErr(t, err)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Disposition") != "attachment; filename=sample-main."+format ||
			response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: status=%d header=%v", format, response.StatusCode, response.Header)
		}
		entries, err := read(body)
		noErr(t, err, format)
		if entries["sample-main/README.md"] != "# Sample\n" || entries["sample-main/dir/a.txt"] != "nested\n" || len(entries["sample-main/random.bin"]) != 1024 {
			t.Fatalf("%s entries: %s", format, entryNames(entries))
		}
	}
	if response, _, _ := fetchArchive(t, server.URL+"?format=rar"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown format status=%d", response.StatusCode)
	}
}

// Git failing before it wrote anything still gets an error status, without
// the archive's headers.
func TestArchiveFailureBeforeTheFirstByteIsAnError(t *testing.T) {
	handler, _ := archiveFixture(t, 16)
	logs := captureLog(t)
	server := serveArchiveOf(t, handler, strings.Repeat("0", 40))
	response, body, err := fetchArchive(t, server.URL+"?format=zip")
	noErr(t, err)
	if response.StatusCode != http.StatusBadGateway || response.Header.Get("Content-Disposition") != "" || bytes.HasPrefix(body, []byte("PK")) {
		t.Fatalf("missing commit: status=%d header=%v", response.StatusCode, response.Header)
	}
	if handler.Active() != 0 {
		t.Fatalf("%d archive operations still active", handler.Active())
	}
	if !strings.Contains(logs.String(), "git archive exited with status 128") {
		t.Fatalf("log=%q", logs.String())
	}
}

// An archive that passes the response limit is cut off: the transfer ends
// with an error, and what arrived is not a readable archive.
func TestArchiveStopsAtTheResponseLimitWithoutAValidArchive(t *testing.T) {
	handler, commitOID := archiveFixture(t, 300<<10)
	handler.MaximumResponse = 100 << 10
	previous := archiveHoldback
	archiveHoldback = 1 << 10
	t.Cleanup(func() { archiveHoldback = previous })
	logs := captureLog(t)
	server := serveArchiveOf(t, handler, commitOID)
	for format, read := range map[string]func([]byte) (map[string]string, error){ArchiveZip: zipEntries, ArchiveTarGz: tarGzEntries} {
		response, body, err := fetchArchive(t, server.URL+"?format="+format)
		if response.StatusCode != http.StatusOK || err == nil {
			t.Fatalf("%s: status=%d read error=%v, want a transfer that ends with an error", format, response.StatusCode, err)
		}
		if len(body) == 0 || int64(len(body)) > handler.MaximumResponse {
			t.Fatalf("%s: received %d bytes", format, len(body))
		}
		if entries, err := read(body); err == nil {
			t.Fatalf("%s: the cut archive reads as complete: %s", format, entryNames(entries))
		}
	}
	if !strings.Contains(logs.String(), `Git archive request for repository "sample" failed: response exceeded the size limit`) {
		t.Fatalf("log=%q", logs.String())
	}
}

func TestHoldbackWriterPassesOnlyBytesFollowedByTheHeldTail(t *testing.T) {
	var sent bytes.Buffer
	writer := &holdbackWriter{next: &sent, hold: 4}
	for _, part := range []string{"ab", "cdef", "g", "hijkl"} {
		_, err := writer.Write([]byte(part))
		noErr(t, err)
	}
	if sent.String() != "abcdefgh" {
		t.Fatalf("sent %q before the end", sent.String())
	}
	noErr(t, writer.flush())
	if sent.String() != "abcdefghijkl" {
		t.Fatalf("sent %q after flush", sent.String())
	}
}

// A name that is not plain ASCII also gets an ASCII form, which clients that
// ignore the UTF-8 form keep, such as curl --remote-header-name.
func TestArchiveDispositionHasAnASCIIFallback(t *testing.T) {
	for filename, want := range map[string]string{
		"sample-main.zip":  "attachment; filename=sample-main.zip",
		"demo-기능.zip":      `attachment; filename="demo-.zip"; filename*=UTF-8''demo-%EA%B8%B0%EB%8A%A5.zip`,
		"demo-café.tar.gz": `attachment; filename="demo-caf.tar.gz"; filename*=UTF-8''demo-caf%C3%A9.tar.gz`,
	} {
		got := attachmentDisposition(filename)
		if got != want {
			t.Errorf("%q: %s, want %s", filename, got, want)
		}
		if _, parameters, err := mime.ParseMediaType(got); err != nil || parameters["filename"] != filename {
			t.Errorf("%q: parsed %q %v", filename, parameters["filename"], err)
		}
	}
}

// Waiting for the repository read lock ends with the operation time limit and
// answers busy, without writing anything.
func TestArchiveLockWaitEndsAsBusy(t *testing.T) {
	handler, commitOID := archiveFixture(t, 16)
	handler.OperationTimeout = 300 * time.Millisecond
	lock := handler.Repositories.Locks.For("sample")
	lock.Lock()
	defer lock.Unlock()
	logs := captureLog(t)
	request := httptest.NewRequest(http.MethodGet, "/archive", nil)
	recorder := httptest.NewRecorder()
	started := time.Now()
	err := handler.ServeArchive(recorder, request, "sample", commitOID, ArchiveZip, "sample-main", "sample-main.zip")
	var failure *ArchiveError
	if !errors.As(err, &failure) || failure.Status != http.StatusServiceUnavailable || failure.RetryAfter == 0 || recorder.Body.Len() != 0 || len(recorder.Header()) != 0 {
		t.Fatalf("lock wait: err=%v written=%d header=%v", err, recorder.Body.Len(), recorder.Header())
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("lock wait took %s", elapsed)
	}
	if !strings.Contains(logs.String(), "operation timed out (Git transfer time limit)") {
		t.Fatalf("log=%q", logs.String())
	}
}

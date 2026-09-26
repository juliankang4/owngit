package githttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"owngit/internal/repository"
)

// Real Git clients gzip upload-pack requests larger than 1 KiB. Each case below
// crosses that size in a different way; the server must still answer. Every
// subtest builds its own server and repositories, so any one can run alone.
func TestSmartHTTPServesGzipFetchRequestsFromRealGit(t *testing.T) {
	for _, protocol := range []string{"0", "2"} {
		t.Run("protocol v"+protocol, func(t *testing.T) {
			t.Run("push many branches at once", func(t *testing.T) {
				fixture := newGzipFetchFixture(t, protocol)
				fixture.pushSample(t)
				fixture.pushSame(t)
				for id, want := range map[string]int{"sample": 24, "same": 64} {
					path, err := fixture.manager.Path(id)
					noErr(t, err)
					if got := countRefs(t, fixture.git, path, "refs/heads/"); got != want {
						t.Fatalf("%s has %d branches, want %d", id, got, want)
					}
				}
			})
			t.Run("clone 24 refs at distinct commits", func(t *testing.T) {
				fixture := newGzipFetchFixture(t, protocol)
				fixture.pushSample(t)
				clone := filepath.Join(t.TempDir(), "clone")
				fixture.expectGzip(t, func() { fixture.git(t, "", "clone", "-q", fixture.url+"/git/sample.git", clone) })
				if got := countRefs(t, fixture.git, clone, "refs/remotes/origin/"); got < 24 {
					t.Fatalf("clone has %d remote-tracking refs, want 24 or more", got)
				}
			})
			t.Run("clone 64 refs at one commit", func(t *testing.T) {
				fixture := newGzipFetchFixture(t, protocol)
				fixture.pushSame(t)
				clone := filepath.Join(t.TempDir(), "clone")
				fixture.expectGzip(t, func() { fixture.git(t, "", "clone", "-q", fixture.url+"/git/same.git", clone) })
				if got := countRefs(t, fixture.git, clone, "refs/remotes/origin/"); got < 64 {
					t.Fatalf("clone has %d remote-tracking refs, want 64 or more", got)
				}
			})
			t.Run("fetch with 40 local-only commits after a server change", func(t *testing.T) {
				fixture := newGzipFetchFixture(t, protocol)
				fixture.pushSample(t)
				git, source := fixture.git, fixture.source
				clone := fixture.singleBranchClone(t)
				for index := 0; index < 40; index++ {
					git(t, clone, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("local %d", index))
				}
				git(t, source, "commit", "-q", "--allow-empty", "-m", "server change")
				newMain := git(t, source, "rev-parse", "HEAD")
				git(t, source, "push", "-q", fixture.url+"/git/sample.git", "main")
				fixture.expectGzip(t, func() { git(t, clone, "pull", "-q", "--no-rebase", "--no-edit", "origin", "main") })
				if got := git(t, clone, "rev-parse", "refs/remotes/origin/main"); got != newMain {
					t.Fatalf("fetched main %s, want %s", got, newMain)
				}
				git(t, clone, "push", "-q", "origin", "HEAD:refs/heads/main")
			})
			t.Run("fetch 20 new branches at new commits", func(t *testing.T) {
				fixture := newGzipFetchFixture(t, protocol)
				fixture.pushSample(t)
				git, source := fixture.git, fixture.source
				clone := fixture.singleBranchClone(t)
				var refspecs []string
				for index := 0; index < 20; index++ {
					git(t, source, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("new branch %d", index))
					refspecs = append(refspecs, git(t, source, "rev-parse", "HEAD")+fmt.Sprintf(":refs/heads/new/%02d", index))
				}
				git(t, source, append([]string{"push", "-q", fixture.url + "/git/sample.git"}, refspecs...)...)
				fixture.expectGzip(t, func() { git(t, clone, "fetch", "-q", "origin", "+refs/heads/new/*:refs/remotes/origin/new/*") })
				if got := countRefs(t, git, clone, "refs/remotes/origin/new/"); got != 20 {
					t.Fatalf("fetched %d new branches, want 20", got)
				}
			})
		})
	}
}

// gzipFetchFixture is a real Smart HTTP server with repositories "sample" and
// "same", and a Git client fixed to one protocol version.
type gzipFetchFixture struct {
	url          string
	git          gitClient
	manager      *repository.Manager
	source       string
	gzipRequests *atomic.Int64
}

func newGzipFetchFixture(t *testing.T, protocol string) *gzipFetchFixture {
	t.Helper()
	manager, runner := newHTTPTestRepository(t)
	if _, err := manager.Create(context.Background(), "same", ""); err != nil {
		t.Fatal(err)
	}
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	fixture := &gzipFetchFixture{manager: manager, gzipRequests: &atomic.Int64{},
		git: isolatedGit(t, "-c", "protocol.version="+protocol)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Encoding") == "gzip" {
			fixture.gzipRequests.Add(1)
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	fixture.url = server.URL
	return fixture
}

// pushSample pushes 24 branches at distinct commits to "sample" in one push.
// Git never gzips pushes, so this setup works with or without the fix.
func (fixture *gzipFetchFixture) pushSample(t *testing.T) {
	t.Helper()
	fixture.source = filepath.Join(t.TempDir(), "source")
	commits := linearHistory(t, fixture.git, fixture.source, 120)
	for index := 1; index < 24; index++ {
		fixture.git(t, fixture.source, "branch", fmt.Sprintf("b/%02d", index), commits[index*4])
	}
	fixture.git(t, fixture.source, "push", "-q", fixture.url+"/git/sample.git", "refs/heads/*:refs/heads/*")
}

// pushSame pushes 64 branches that all point at one commit to "same".
func (fixture *gzipFetchFixture) pushSame(t *testing.T) {
	t.Helper()
	same := filepath.Join(t.TempDir(), "same")
	fixture.git(t, "", "init", "-q", "--initial-branch=main", same)
	fixture.git(t, same, "commit", "-q", "--allow-empty", "-m", "one")
	for index := 1; index < 64; index++ {
		fixture.git(t, same, "branch", fmt.Sprintf("same/%02d", index))
	}
	fixture.git(t, same, "push", "-q", fixture.url+"/git/same.git", "refs/heads/*:refs/heads/*")
}

// singleBranchClone keeps its own requests under 1 KiB, so later requests are
// the only ones that exercise gzip.
func (fixture *gzipFetchFixture) singleBranchClone(t *testing.T) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	fixture.git(t, "", "clone", "-q", "--single-branch", "--no-tags", fixture.url+"/git/sample.git", clone)
	return clone
}

func (fixture *gzipFetchFixture) expectGzip(t *testing.T, run func()) {
	t.Helper()
	before := fixture.gzipRequests.Load()
	run()
	if fixture.gzipRequests.Load() == before {
		t.Fatal("Git sent no gzip request; the case no longer covers compressed bodies")
	}
}

func TestSmartHTTPRefusesUnsupportedContentEncoding(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, encoding := range []string{"br", "deflate", "zstd", "gzip, gzip", "gzip, br", "compress"} {
		response := postPack(t, server.URL, "git-upload-pack", encoding, []byte("0000"))
		if response.StatusCode != http.StatusUnsupportedMediaType || response.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Content-Encoding %q: status=%d Accept-Encoding=%q, want 415 and gzip", encoding, response.StatusCode, response.Header.Get("Accept-Encoding"))
		}
	}
	for _, encoding := range []string{"", "identity"} {
		response := postPack(t, server.URL, "git-upload-pack", encoding, []byte("0000"))
		if response.StatusCode != http.StatusOK {
			t.Errorf("Content-Encoding %q: status=%d, want 200", encoding, response.StatusCode)
		}
	}
}

func TestSmartHTTPInflatesGzipBodiesWithinTheRequestLimit(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	handler.MaximumRequest = 1 << 20
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)

	payload := bytes.Repeat([]byte("0032have 0000000000000000000000000000000000000000\n"), 1000)
	for _, service := range []string{"git-upload-pack", "git-receive-pack"} {
		for _, encoding := range []string{"gzip", "x-gzip", "GZIP"} {
			response := postPack(t, server.URL, service, encoding, gzipBytes(t, payload))
			if response.StatusCode != http.StatusOK || response.Header.Get("X-Test-Stdin-Bytes") != strconv.Itoa(len(payload)) ||
				response.Header.Get("X-Test-Content-Length") != "absent" {
				t.Errorf("%s %s: status=%d backend read %s bytes with CONTENT_LENGTH %s, want 200, %d and absent", service, encoding,
					response.StatusCode, response.Header.Get("X-Test-Stdin-Bytes"), response.Header.Get("X-Test-Content-Length"), len(payload))
			}
		}
	}

	// About 64 KiB of gzip that inflates to 64 MiB: the backend must stop at
	// the request limit instead of reading everything. The limit stops the
	// backend before it answers, so the answer is 413 and the backend's own
	// report of what it read never arrives.
	bomb := gzipBytes(t, make([]byte, 64<<20))
	if len(bomb) >= int(handler.MaximumRequest) {
		t.Fatalf("compressed bomb is %d bytes; it must fit under the limit to test inflation", len(bomb))
	}
	response := postPack(t, server.URL, "git-receive-pack", "gzip", bomb)
	if response.StatusCode != http.StatusRequestEntityTooLarge || response.Header.Get("X-Test-Stdin-Bytes") != "" {
		t.Fatalf("gzip bomb: status=%d, backend read %q bytes; want 413 from a backend stopped at the limit", response.StatusCode, response.Header.Get("X-Test-Stdin-Bytes"))
	}
	if !strings.Contains(logs.String(), `Git push request for repository "sample" failed: request body exceeded the size limit`) {
		t.Fatalf("gzip bomb was not logged as over the limit; log:\n%s", logs.String())
	}

	response = postPack(t, server.URL, "git-upload-pack", "gzip", []byte("not gzip at all"))
	if response.StatusCode != http.StatusBadRequest ||
		!strings.Contains(logs.String(), `Git fetch request for repository "sample" failed: request body is not valid gzip`) {
		t.Fatalf("invalid gzip: status=%d, want 400 and a log line; log:\n%s", response.StatusCode, logs.String())
	}
}

// A gzip body that is truncated or fails its checksum on a connection that
// stays open must not reach the backend as a complete request.
func TestSmartHTTPStopsBackendOnCorruptGzipBody(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)

	valid := gzipBytes(t, bytes.Repeat([]byte("0032have 0000000000000000000000000000000000000000\n"), 1000))
	for _, corrupt := range []struct {
		name string
		body []byte
	}{
		{"truncated stream", valid[:len(valid)/2]},
		{"bad checksum", badGzipChecksum(valid)},
	} {
		before := strings.Count(logs.String(), "\n")
		response := postPack(t, server.URL, "git-receive-pack", "gzip", corrupt.body)
		if response.StatusCode != http.StatusBadRequest || response.Header.Get("X-Test-Stdin-Bytes") != "" {
			t.Errorf("%s: status=%d, backend reported reading %q bytes; want 400 from a stopped backend", corrupt.name,
				response.StatusCode, response.Header.Get("X-Test-Stdin-Bytes"))
		}
		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")[before:]
		if len(lines) != 1 || lines[0] != `Git push request for repository "sample" failed: request body is not valid gzip` {
			t.Errorf("%s: log lines %q, want one invalid gzip line", corrupt.name, lines)
		}
	}
}

// A complete push whose gzip checksum is wrong must not update a ref. The same
// body with a correct checksum shows the request itself is a valid push.
func TestSmartHTTPCorruptGzipPushDoesNotUpdateRefs(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)
	git := isolatedGit(t)
	work := filepath.Join(t.TempDir(), "work")
	git(t, "", "init", "-q", "--initial-branch=main", work)
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), bytes.Repeat([]byte("synthetic content\n"), 2000), 0o600))
	git(t, work, "add", "file.txt")
	git(t, work, "commit", "-q", "-m", "one")
	commit := git(t, work, "rev-parse", "HEAD")
	packObjects := exec.Command("git", "pack-objects", "--stdout", "--revs", "-q")
	packObjects.Dir = work
	packObjects.Stdin = strings.NewReader(commit + "\n")
	pack, err := packObjects.Output()
	noErr(t, err)
	pushBody := func(ref string) []byte {
		command := fmt.Sprintf("%s %s %s\x00report-status\n", strings.Repeat("0", 40), commit, ref)
		return append([]byte(fmt.Sprintf("%04x%s0000", len(command)+4, command)), pack...)
	}
	remote, err := manager.Path("sample")
	noErr(t, err)
	refOID := func(ref string) string {
		output, _ := exec.Command("git", "--git-dir", remote, "rev-parse", "--verify", "--quiet", ref).Output()
		return strings.TrimSpace(string(output))
	}

	postPack(t, server.URL, "git-receive-pack", "gzip", gzipBytes(t, pushBody("refs/heads/good")))
	if got := refOID("refs/heads/good"); got != commit {
		t.Fatalf("valid gzip push set refs/heads/good to %q, want %s; log:\n%s", got, commit, logs.String())
	}
	postPack(t, server.URL, "git-receive-pack", "gzip", badGzipChecksum(gzipBytes(t, pushBody("refs/heads/bad"))))
	if got := refOID("refs/heads/bad"); got != "" {
		t.Fatalf("push with a bad gzip checksum created refs/heads/bad at %s", got)
	}
	if !strings.Contains(logs.String(), `Git push request for repository "sample" failed: request body is not valid gzip`) {
		t.Fatalf("bad checksum was not logged; log:\n%s", logs.String())
	}
}

func badGzipChecksum(valid []byte) []byte {
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-8] ^= 0xff // first byte of the CRC-32 trailer
	return corrupt
}

// git-http-backend buffers a whole upload-pack request up to 10 MiB. With the
// body inflated before the backend, that buffer also bounds gzip requests.
func TestSmartHTTPBoundsGzipUploadPackBombAtTheBackendBuffer(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)

	have := []byte("0032have 0000000000000000000000000000000000000000\n")
	bomb := gzipBytes(t, bytes.Repeat(have, (64<<20)/len(have)))
	postPack(t, server.URL, "git-upload-pack", "gzip", bomb)
	if want := `Git fetch request for repository "sample" failed: request exceeded the Git backend request buffer`; !strings.Contains(logs.String(), want) {
		t.Fatalf("a %d-byte gzip body inflating to 64 MiB was not stopped at the backend buffer; log:\n%s", len(bomb), logs.String())
	}
}

func TestSmartHTTPLogsBackendProtocolErrorsWithoutRequestContent(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)

	postPack(t, server.URL, "git-upload-pack", "", []byte("zzzzsecret-request-content"))
	got := logs.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, `Git fetch request for repository "sample" failed: Git protocol error`) {
		t.Fatalf("log = %q, want one protocol error line", got)
	}
	if strings.Contains(got, "zzzz") || strings.Contains(got, "secret") {
		t.Fatalf("log leaked request content: %q", got)
	}
}

func postPack(t *testing.T, serverURL, service, encoding string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/git/sample.git/"+service, bytes.NewReader(body))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-"+service+"-request")
	if encoding != "" {
		request.Header.Set("Content-Encoding", encoding)
	}
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	_, _ = bytes.NewBuffer(nil).ReadFrom(response.Body)
	response.Body.Close()
	return response
}

func gzipBytes(t *testing.T, content []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(content)
	noErr(t, err)
	noErr(t, writer.Close())
	return compressed.Bytes()
}

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buffer := &lockedBuffer{}
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return buffer
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(content)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// gitClient runs Git and fails the given test on error.
type gitClient func(t testing.TB, directory string, arguments ...string) string

// isolatedGit runs the Git client without the developer's global or system
// configuration, so the protocol and identity are fixed by the test.
func isolatedGit(t *testing.T, options ...string) gitClient {
	t.Helper()
	home := t.TempDir()
	emptyConfig := filepath.Join(home, "gitconfig")
	noErr(t, os.WriteFile(emptyConfig, nil, 0o600))
	environment := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+emptyConfig,
		"HOME="+home, "GIT_AUTHOR_NAME=HTTP Test", "GIT_AUTHOR_EMAIL=http@example.invalid",
		"GIT_COMMITTER_NAME=HTTP Test", "GIT_COMMITTER_EMAIL=http@example.invalid")
	return func(t testing.TB, directory string, arguments ...string) string {
		t.Helper()
		command := exec.Command("git", append(append([]string{}, options...), arguments...)...)
		command.Dir = directory
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
}

// linearHistory creates count commits on main and returns their IDs, oldest first.
func linearHistory(t *testing.T, git gitClient, directory string, count int) []string {
	t.Helper()
	git(t, "", "init", "-q", "--initial-branch=main", directory)
	var stream strings.Builder
	for index := 1; index <= count; index++ {
		message := fmt.Sprintf("commit %d", index)
		content := fmt.Sprintf("line %d\n", index)
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\ncommitter HTTP Test <http@example.invalid> %d +0000\ndata %d\n%s\n",
			index, 1700000000+index*60, len(message), message)
		if index > 1 {
			fmt.Fprintf(&stream, "from :%d\n", index-1)
		}
		fmt.Fprintf(&stream, "M 100644 inline file.txt\ndata %d\n%s\n", len(content), content)
	}
	command := exec.Command("git", "fast-import", "--quiet")
	command.Dir = directory
	command.Stdin = strings.NewReader(stream.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, output)
	}
	git(t, directory, "reset", "-q", "--hard", "main")
	return strings.Fields(git(t, directory, "rev-list", "--reverse", "main"))
}

func countRefs(t testing.TB, git gitClient, directory, prefix string) int {
	return len(strings.Fields(git(t, directory, "for-each-ref", "--format=%(refname)", prefix)))
}

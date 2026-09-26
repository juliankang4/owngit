package githttp

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
)

// quarantines lists the push quarantine directories of repository id.
func quarantines(t *testing.T, handler *Handler, id string) []string {
	t.Helper()
	path, err := handler.Repositories.Path(id)
	noErr(t, err)
	names, err := repository.IncomingQuarantines(path)
	noErr(t, err)
	return names
}

// waitForTransfersToEnd waits until the handler has returned from every
// request, which it does only after its cleanup.
func waitForTransfersToEnd(t *testing.T, handler *Handler) {
	t.Helper()
	waitFor(t, 20*time.Second, "the end of every Git request", func() bool { return handler.Active() == 0 })
}

// A push over the request limit leaves no quarantine directory with the
// objects received so far. A quarantine directory that was there before the
// push, such as one left before OwnGit started, stays for the startup cleanup.
func TestAPushOverTheRequestLimitLeavesNoQuarantine(t *testing.T) {
	handler, work, _ := idleFixture(t, 16, time.Minute)
	handler.MaximumRequest = 256 << 10
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	earlier := filepath.Join(repositoryPath, "objects", "tmp_objdir-incoming-Earlie")
	noErr(t, os.Mkdir(earlier, 0o755))
	noErr(t, os.WriteFile(filepath.Join(earlier, "kept"), []byte("kept"), 0o644))

	content := make([]byte, 2<<20)
	_, err = rand.Read(content)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), content, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "large")
	output, err := httpGitCombined(work, "push", server.URL+"/git/sample.git", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("a push of %d bytes over a %d byte limit succeeded:\n%s", len(content), handler.MaximumRequest, output)
	}
	waitForTransfersToEnd(t, handler)
	if got := quarantines(t, handler, "sample"); !slices.Equal(got, []string{"tmp_objdir-incoming-Earlie"}) {
		t.Fatalf("quarantine directories after the refused push: %q, want only the earlier one; log:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), `Git push request for repository "sample": removed the objects of an unfinished push (objects/tmp_objdir-incoming-`) {
		t.Fatalf("the removal was not logged:\n%s", logs.String())
	}
}

// A push body that ends early, or a client that goes away while it sends the
// pack, leaves no quarantine directory, and a later push then works and
// removes nothing.
func TestACutPushLeavesNoQuarantine(t *testing.T) {
	handler, work, head := idleFixture(t, 16, time.Minute)
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	// One command and the start of a pack, so receive-pack has made its
	// quarantine and waits for the rest of the pack.
	command := fmt.Sprintf("%s %s refs/heads/cut\x00report-status\n", strings.Repeat("0", len(head)), head)
	prefix := fmt.Sprintf("%04x%s0000PACK\x00\x00\x00\x02\x00\x00\x00\x01", 4+len(command), command)
	for _, ending := range []string{"body ends early", "client disconnects"} {
		t.Run(ending, func(t *testing.T) {
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			noErr(t, err)
			defer connection.Close()
			_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: example.test\r\n"+
				"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n%s", 1<<20, prefix)
			noErr(t, err)
			waitFor(t, 20*time.Second, "receive-pack's quarantine", func() bool { return len(quarantines(t, handler, "sample")) == 1 })
			if ending == "body ends early" {
				noErr(t, connection.(*net.TCPConn).CloseWrite())
			} else {
				noErr(t, connection.Close())
			}
			waitForTransfersToEnd(t, handler)
			if got := quarantines(t, handler, "sample"); len(got) != 0 {
				t.Fatalf("quarantine directories after the cut push: %q; log:\n%s", got, logs.String())
			}
		})
	}

	removals := strings.Count(logs.String(), "removed the objects of an unfinished push")
	if removals != 2 {
		t.Fatalf("%d removals logged for two cut pushes, want 2:\n%s", removals, logs.String())
	}
	noErr(t, os.WriteFile(filepath.Join(work, "after.txt"), []byte("after\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "after")
	runHTTPGit(t, work, "push", "-q", server.URL+"/git/sample.git", "HEAD:refs/heads/main")
	waitForTransfersToEnd(t, handler)
	if got := strings.Count(logs.String(), "removed the objects of an unfinished push"); got != removals {
		t.Fatalf("a complete push logged a removal:\n%s", logs.String())
	}
}

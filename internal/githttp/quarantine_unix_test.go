//go:build !windows

package githttp

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A refused push removes only its own quarantine: a push to another
// repository that is still checking its objects keeps its quarantine and
// completes. (Pushes to the same repository cannot overlap; they wait for its
// write lock.)
func TestARefusedPushKeepsTheQuarantineOfAnotherPush(t *testing.T) {
	handler, work, _ := idleFixture(t, 16, time.Minute)
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	_, err := handler.Repositories.Create(context.Background(), "other", "")
	noErr(t, err)
	otherPath, err := handler.Repositories.Path("other")
	noErr(t, err)
	// pre-receive runs while receive-pack keeps the pushed objects in its
	// quarantine. It waits for the release file; the bound only keeps a
	// failing run from hanging.
	release := filepath.Join(t.TempDir(), "release")
	noErr(t, os.WriteFile(filepath.Join(otherPath, "hooks", "pre-receive"), []byte("#!/bin/sh\ncat >/dev/null\ni=0\n"+
		"while [ ! -e "+quoteShell(release)+" ]; do i=$((i+1)); if [ $i -gt 600 ]; then exit 1; fi; sleep 0.05; done\n"), 0o700))
	pushed := make(chan string, 1)
	go func() {
		output, err := httpGitCombined(work, "push", server.URL+"/git/other.git", "HEAD:refs/heads/main")
		if err != nil {
			output = "push to other failed: " + err.Error() + "\n" + output
		} else {
			output = ""
		}
		pushed <- output
	}()
	waitFor(t, 20*time.Second, "the other push's quarantine", func() bool { return len(quarantines(t, handler, "other")) == 1 })
	checking := quarantines(t, handler, "other")

	// A push to sample over the request limit of a second handler for the
	// same repositories.
	limited, err := New(handler.Git, handler.Repositories, "", 2)
	noErr(t, err)
	limited.Authorize = handler.Authorize
	limited.MaximumRequest = 64 << 10
	limitedServer := httptest.NewServer(limited)
	defer limitedServer.Close()
	large := filepath.Join(t.TempDir(), "large")
	runHTTPGit(t, "", "clone", "-q", server.URL+"/git/sample.git", large)
	content := make([]byte, 1<<20)
	_, err = rand.Read(content)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(large, "large.bin"), content, 0o600))
	runHTTPGit(t, large, "add", ".")
	runHTTPGit(t, large, "commit", "-q", "-m", "large")
	if output, err := httpGitCombined(large, "push", limitedServer.URL+"/git/sample.git", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("the push over the limit succeeded:\n%s", output)
	}
	waitForTransfersToEnd(t, limited)
	if got := quarantines(t, handler, "sample"); len(got) != 0 {
		t.Fatalf("the refused push left %q", got)
	}
	if got := quarantines(t, handler, "other"); len(got) != 1 || got[0] != checking[0] {
		t.Fatalf("the other push's quarantine is %q after the refused push, want %q", got, checking)
	}

	noErr(t, os.WriteFile(release, nil, 0o600))
	if output := <-pushed; output != "" {
		t.Fatal(output)
	}
	waitForTransfersToEnd(t, handler)
	if got := quarantines(t, handler, "other"); len(got) != 0 {
		t.Fatalf("the completed push left %q", got)
	}
	if strings.Contains(logs.String(), `repository "other": removed`) {
		t.Fatalf("a removal was logged for the completed push:\n%s", logs.String())
	}
}

package githttp

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// OnPush sees each push that updated refs, with exactly the refs Git
// updated, and no push that updated none: one that was up to date, refused
// by the server, or failed.
func TestOnPushReportsTheRefsEachPushUpdated(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	var mu sync.Mutex
	var pushes [][]RefUpdate
	handler.OnPush = func(ctx context.Context, repositoryID string, updates []RefUpdate) {
		if repositoryID != "sample" || ctx.Err() != nil {
			t.Errorf("OnPush for %q with context error %v", repositoryID, ctx.Err())
		}
		mu.Lock()
		pushes = append(pushes, updates)
		mu.Unlock()
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	recorded := func() [][]RefUpdate {
		mu.Lock()
		defer mu.Unlock()
		taken := pushes
		pushes = nil
		return taken
	}
	expect := func(step string, want ...[]RefUpdate) {
		t.Helper()
		got := recorded()
		for _, updates := range got {
			slices.SortFunc(updates, func(a, b RefUpdate) int { return strings.Compare(a.Ref, b.Ref) })
		}
		if len(got) != len(want) {
			t.Fatalf("%s: OnPush ran %d times, want %d: %+v", step, len(got), len(want), got)
		}
		for index := range want {
			if !slices.Equal(got[index], want[index]) {
				t.Fatalf("%s: updates %+v, want %+v", step, got[index], want[index])
			}
		}
	}
	remotePath, err := manager.Path("sample")
	noErr(t, err)

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Push Test")
	runHTTPGit(t, work, "config", "user.email", "push@example.invalid")
	runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/sample.git")
	commit := func(message string) string {
		t.Helper()
		noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte(message+"\n"), 0o600))
		runHTTPGit(t, work, "add", "file.txt")
		runHTTPGit(t, work, "commit", "-m", message)
		return httpGitOutput(t, work, "rev-parse", "HEAD")
	}

	first := commit("first")
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	expect("create main", []RefUpdate{{Ref: "refs/heads/main", New: first}})

	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	expect("up to date")

	second := commit("second")
	runHTTPGit(t, work, "tag", "-a", "v1", "-m", "v1")
	tag := httpGitOutput(t, work, "rev-parse", "refs/tags/v1")
	// A request sent in chunks is read by the backend in many pieces.
	runHTTPGit(t, work, "-c", "http.postBuffer=1", "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/topic", "refs/tags/v1")
	expect("three refs", []RefUpdate{
		{Ref: "refs/heads/main", Old: first, New: second},
		{Ref: "refs/heads/topic", New: second},
		{Ref: "refs/tags/v1", New: tag},
	})

	runHTTPGit(t, "", "--git-dir", remotePath, "config", "receive.denyDeletes", "true")
	runHTTPGit(t, "", "--git-dir", remotePath, "config", "receive.denyNonFastForwards", "true")
	if output, err := httpGitCombined(work, "push", "origin", ":refs/heads/topic"); err == nil {
		t.Fatalf("refused deletion succeeded: %s", output)
	}
	expect("refused deletion")
	// The server refuses main and accepts other; only other was updated.
	if output, err := httpGitCombined(work, "push", "--force", "origin", first+":refs/heads/main", "HEAD:refs/heads/other"); err == nil {
		t.Fatalf("refused rewind succeeded: %s", output)
	}
	expect("partly refused", []RefUpdate{{Ref: "refs/heads/other", New: second}})

	limits := useLimits(t, handler, func(limits *Limits) { limits.MaximumRequest = 64 << 10 })
	large := make([]byte, 1<<20)
	_, _ = rand.Read(large)
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), large, 0o600))
	runHTTPGit(t, work, "add", "large.bin")
	runHTTPGit(t, work, "commit", "-m", "large")
	if output, err := httpGitCombined(work, "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("push over the request limit succeeded: %s", output)
	}
	expect("failed push")
	limits.MaximumRequest = 0

	runHTTPGit(t, "", "--git-dir", remotePath, "config", "receive.denyDeletes", "false")
	runHTTPGit(t, work, "push", "origin", ":refs/heads/other")
	expect("delete", []RefUpdate{{Ref: "refs/heads/other", Old: second}})
}

// The command reader takes each command, however the request is split, and
// stops at the flush packet that ends the commands.
func TestPushCommandsReadsSplitRequests(t *testing.T) {
	oldOID, newOID := strings.Repeat("1", 40), strings.Repeat("2", 64)
	zero := strings.Repeat("0", 40)
	request := packet(zero+" "+newOID+" refs/heads/main\x00report-status side-band-64k\n") +
		packet("shallow "+oldOID+"\n") +
		packet(oldOID+" "+zero+" refs/heads/gone\n") +
		"0000" + packet(oldOID+" "+newOID+" refs/heads/after-flush\n") + "PACK"
	for size := 1; size <= len(request); size += 7 {
		commands := &pushCommands{}
		for start := 0; start < len(request); start += size {
			_, _ = commands.Write([]byte(request[start:min(start+size, len(request))]))
		}
		want := map[string]RefUpdate{
			"refs/heads/main": {Ref: "refs/heads/main", New: newOID},
			"refs/heads/gone": {Ref: "refs/heads/gone", Old: oldOID},
		}
		if len(commands.byRef) != len(want) {
			t.Fatalf("pieces of %d: commands %+v", size, commands.byRef)
		}
		for ref, update := range want {
			if commands.byRef[ref] != update {
				t.Fatalf("pieces of %d: %s = %+v, want %+v", size, ref, commands.byRef[ref], update)
			}
		}
	}
}

func packet(payload string) string {
	const digits = "0123456789abcdef"
	length := len(payload) + 4
	return string([]byte{digits[length>>12&15], digits[length>>8&15], digits[length>>4&15], digits[length&15]}) + payload
}

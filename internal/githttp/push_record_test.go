package githttp

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// OnPush sees each push that updated refs, with exactly the refs Git
// updated, and no push that updated none: one that was up to date, refused
// by the server, or failed.
func TestOnPushReportsTheRefsEachPushUpdated(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	var mu sync.Mutex
	var pushes [][]RefUpdate
	handler.OnPush = func(request *http.Request, repositoryID string, updates []RefUpdate) {
		if repositoryID != "sample" || request.Context().Err() != nil || request.Method != http.MethodPost {
			t.Errorf("OnPush for %q with %s and context error %v", repositoryID, request.Method, request.Context().Err())
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

	// Commands that change nothing are answered "ok" by Git but are not
	// pushes: the same old and new value, and deleting a missing ref. A
	// request without commands is not one either.
	empty := exec.Command("git", "pack-objects", "--stdout")
	empty.Dir, empty.Stdin = work, strings.NewReader("")
	emptyPack, err := empty.Output()
	noErr(t, err)
	zero := strings.Repeat("0", len(first))
	for name, body := range map[string]string{
		"same value":         packet(first+" "+first+" refs/heads/main\x00report-status\n") + "0000" + string(emptyPack),
		"delete missing ref": packet(zero+" "+zero+" refs/heads/missing\x00report-status delete-refs\n") + "0000",
		"no commands":        "0000",
	} {
		response, err := http.Post(server.URL+"/git/sample.git/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(body))
		noErr(t, err)
		report, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if name != "no commands" && !strings.Contains(string(report), "ok refs/heads/") {
			t.Fatalf("%s: Git did not accept the command: %d %q", name, response.StatusCode, report)
		}
		expect(name)
	}
	if refs := httpGitOutput(t, work, "ls-remote", "origin", "refs/*"); refs != first+"\trefs/heads/main" {
		t.Fatalf("refs after commands that change nothing: %q", refs)
	}

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
	// An atomic push with one refused ref changes nothing.
	if output, err := httpGitCombined(work, "push", "--atomic", "--force", "origin", first+":refs/heads/main", "HEAD:refs/heads/atomic"); err == nil {
		t.Fatalf("refused atomic push succeeded: %s", output)
	}
	expect("atomic refused")

	// A push over the request limit fails. It goes to a handler of its own
	// with that limit, so no limit changes while a request is served.
	limited, err := New(runner, manager, "")
	noErr(t, err)
	limited.Authorize, limited.OnPush = handler.Authorize, handler.OnPush
	useLimits(t, limited, func(limits *Limits) { limits.MaximumRequest = 64 << 10 })
	limitedServer := httptest.NewServer(limited)
	defer limitedServer.Close()
	large := make([]byte, 1<<20)
	_, _ = rand.Read(large)
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), large, 0o600))
	runHTTPGit(t, work, "add", "large.bin")
	runHTTPGit(t, work, "commit", "-m", "large")
	if output, err := httpGitCombined(work, "push", limitedServer.URL+"/git/sample.git", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("push over the request limit succeeded: %s", output)
	}
	waitForTransfersToEnd(t, limited)
	expect("failed push")

	runHTTPGit(t, "", "--git-dir", remotePath, "config", "receive.denyDeletes", "false")
	runHTTPGit(t, work, "push", "origin", ":refs/heads/other")
	expect("delete", []RefUpdate{{Ref: "refs/heads/other", Old: second}})
}

// The command reader takes each command, however the request is split, and
// stops at the flush packet that ends the commands.
// A push at an earlier address of a renamed repository records the refs it
// updated, whichever way the client asks for the report. The notice OwnGit
// writes there must stay out of the push report: the report would then look
// multiplexed, and the update would be lost. A report without a side band has
// no room for the notice, so that push is answered without one.
func TestOnPushRecordsPushesAtAnEarlierAddress(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	var mu sync.Mutex
	var pushes [][]RefUpdate
	handler.OnPush = func(_ *http.Request, _ string, updates []RefUpdate) {
		mu.Lock()
		pushes = append(pushes, updates)
		mu.Unlock()
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	if _, err := manager.Rename(context.Background(), "sample", "renamed", time.Now()); err != nil {
		t.Fatal(err)
	}
	expect := func(step string, want ...RefUpdate) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(pushes) != 1 || !slices.Equal(pushes[0], want) {
			t.Fatalf("%s: OnPush saw %+v, want one push of %+v", step, pushes, want)
		}
		pushes = nil
	}
	movedNotice := "remote: This repository moved to " + server.URL + "/git/renamed.git"

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Push Test")
	runHTTPGit(t, work, "config", "user.email", "push@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("first\n"), 0o600))
	runHTTPGit(t, work, "add", "file.txt")
	runHTTPGit(t, work, "commit", "-m", "first")
	commit := httpGitOutput(t, work, "rev-parse", "HEAD")

	// A real push, whose client multiplexes the report: Git prints the notice
	// and the push report still sees the update.
	output, err := httpGitCombined(work, "push", server.URL+"/git/sample.git", "HEAD:refs/heads/real")
	if err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("push at the earlier address err=%v output:\n%s", err, output)
	}
	expect("a multiplexed push", RefUpdate{Ref: "refs/heads/real", New: commit})

	// A hand-built push that requests no side band, as an older client sends
	// it.
	packer := exec.Command("git", "pack-objects", "--stdout", "--revs")
	packer.Dir, packer.Stdin = work, strings.NewReader(commit+"\n")
	pack, err := packer.Output()
	noErr(t, err)
	commands := packet(strings.Repeat("0", len(commit))+" "+commit+" refs/heads/alias\x00report-status\n") + "0000" + string(pack)
	response, err := http.Post(server.URL+"/git/sample.git/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(commands))
	noErr(t, err)
	report, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(report), "ok refs/heads/alias") {
		t.Fatalf("the push was not reported: %d %q", response.StatusCode, report)
	}
	if strings.Contains(string(report), "This repository moved to") {
		t.Fatalf("a report without a side band carries no notice: %q", report)
	}
	if refs := httpGitOutput(t, work, "ls-remote", server.URL+"/git/sample.git", "refs/*"); !strings.Contains(refs, commit+"\trefs/heads/alias") {
		t.Fatalf("refs at the earlier address: %q", refs)
	}
	expect("a push without a side band", RefUpdate{Ref: "refs/heads/alias", New: commit})
}

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

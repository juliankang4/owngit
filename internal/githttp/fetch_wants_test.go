package githttp

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pkt(line string) string { return fmt.Sprintf("%04x%s", len(line)+4, line) }

// fetchRequestBody is what no stock client sends: a fetch that names objects
// by ID, in protocol version 0 or 2.
func fetchRequestBody(v2 bool, wants ...string) string {
	var body strings.Builder
	if v2 {
		body.WriteString(pkt("command=fetch\n") + pkt("object-format=sha1\n") + "0001")
		for _, want := range wants {
			body.WriteString(pkt("want " + want + "\n"))
		}
		return body.String() + pkt("done\n") + "0000"
	}
	for i, want := range wants {
		caps := ""
		if i == 0 {
			caps = " no-progress ofs-delta"
		}
		body.WriteString(pkt("want " + want + caps + "\n"))
	}
	return body.String() + "0000" + pkt("done\n")
}

// A fetch serves the tip of an advertised ref and a commit one reaches, and
// nothing else a request can name by ID: not a tree, a blob, a tag object no
// ref names, or a commit only kept history reaches. The owner's address and a
// share link's address follow the one rule, in both protocol versions.
func TestFetchServesOnlyWhatAdvertisedRefsReach(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	main := httptest.NewServer(handler)
	defer main.Close()
	const sharePrefix = "/s/sample.git/"
	share := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeRead(w, r, "sample", strings.TrimPrefix(r.URL.Path, sharePrefix))
	}))
	defer share.Close()

	remote, err := manager.Path("sample")
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	commit := func(message string) string {
		noErr(t, os.WriteFile(filepath.Join(work, "secret.txt"), []byte(message+"\n"), 0o600))
		runHTTPGit(t, work, "add", ".")
		runHTTPGit(t, work, "-c", "user.name=Fetch Test", "-c", "user.email=fetch@example.invalid", "commit", "-q", "-m", message)
		return httpGitOutput(t, work, "rev-parse", "HEAD")
	}
	older := commit("older")
	for i := 0; i < 2; i++ {
		runHTTPGit(t, work, "-c", "user.name=Fetch Test", "-c", "user.email=fetch@example.invalid", "commit", "-q", "--allow-empty", "-m", fmt.Sprint("bulk ", i))
	}
	tip := commit("tip")
	bulk := strings.Fields(httpGitOutput(t, work, "rev-list", tip+"~1", "^"+older))
	runHTTPGit(t, work, "-c", "user.name=Fetch Test", "-c", "user.email=fetch@example.invalid", "tag", "-a", "-m", "release", "release")
	releaseTag := httpGitOutput(t, work, "rev-parse", "release")
	runHTTPGit(t, work, "-c", "user.name=Fetch Test", "-c", "user.email=fetch@example.invalid", "tag", "-a", "-m", "gone", "gone-tag", older)
	goneTag := httpGitOutput(t, work, "rev-parse", "gone-tag")
	runHTTPGit(t, work, "checkout", "-q", "-b", "replaced", older)
	kept := commit("kept")
	runHTTPGit(t, work, "push", "-q", main.URL+"/git/sample.git", "main", "replaced", "release", "gone-tag")
	// The replaced branch and the tag are gone; only kept history holds the
	// commit, and nothing holds the tag object.
	runHTTPGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/retained/replaced", kept)
	runHTTPGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/replaced")
	runHTTPGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/tags/gone-tag")
	keptTree := httpGitOutput(t, work, "rev-parse", kept+"^{tree}")
	keptBlob := httpGitOutput(t, work, "rev-parse", kept+":secret.txt")

	post := func(address, path, protocol, body string) (int, string) {
		request, err := http.NewRequest(http.MethodPost, address+path, strings.NewReader(body))
		noErr(t, err)
		request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		request.Header.Set("Git-Protocol", protocol)
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		defer response.Body.Close()
		answer, err := io.ReadAll(response.Body)
		noErr(t, err)
		return response.StatusCode, string(answer)
	}
	addresses := []struct{ name, url, path string }{
		{"main", main.URL, "/git/sample.git/git-upload-pack"},
		{"share", share.URL, sharePrefix + "git-upload-pack"},
	}
	for _, address := range addresses {
		for _, protocol := range []string{"version=0", "version=2"} {
			v2 := protocol == "version=2"
			serves := func(object string) bool {
				status, answer := post(address.url, address.path, protocol, fetchRequestBody(v2, object))
				if strings.Contains(answer, "not our ref") {
					return false
				}
				if status != http.StatusOK || !strings.Contains(answer, "PACK") {
					t.Fatalf("%s %s: want %s: status=%d answer=%q", address.name, protocol, object, status, answer)
				}
				return true
			}
			for name, object := range map[string]string{"branch tip": tip, "annotated tag": releaseTag, "reachable commit": older} {
				if !serves(object) {
					t.Errorf("%s %s: %s was refused", address.name, protocol, name)
				}
			}
			if status, answer := post(address.url, address.path, protocol, fetchRequestBody(v2, bulk...)); status != http.StatusOK || !strings.Contains(answer, "PACK") {
				t.Errorf("%s %s: %d reachable commits by ID: status=%d answer=%.80q", address.name, protocol, len(bulk), status, answer)
			}
			for name, object := range map[string]string{"kept commit": kept, "tree": keptTree, "blob": keptBlob, "unreferenced tag": goneTag} {
				if serves(object) {
					t.Errorf("%s %s: %s was served", address.name, protocol, name)
				}
			}
		}
	}

	// A request OwnGit cannot read as a fetch never reaches Git.
	for name, body := range map[string]string{
		"bad packet length": "zzzz",
		"bad object ID":     pkt("want nothex\n") + "0000",
		"want-ref":          pkt("command=fetch\n") + "0001" + pkt("want-ref refs/heads/main\n") + "0000",
		"other command":     pkt("command=object-info\n") + "0001" + pkt("size\n") + "0000",
		"command not first": pkt("agent=x\n") + pkt("command=fetch\n") + "0001" + pkt("want "+tip+"\n") + "0000",
		"two commands":      pkt("command=ls-refs\n") + pkt("command=fetch\n") + "0001" + pkt("want "+tip+"\n") + "0000",
		"no flush":          pkt("command=fetch\n") + "0001" + pkt("want "+tip+"\n"),
	} {
		if status, _ := post(main.URL, "/git/sample.git/git-upload-pack", "version=2", body); status != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", name, status)
		}
	}

	// A refusal still reaches the client when the check takes longer than the
	// delay before Git's headers go out, as it does on a large repository.
	flushDelay := responseFlushDelay
	responseFlushDelay = time.Millisecond
	fetchCheckHook = func() { time.Sleep(50 * time.Millisecond) }
	defer func() { responseFlushDelay, fetchCheckHook = flushDelay, nil }()
	if status, answer := post(main.URL, "/git/sample.git/git-upload-pack", "version=2", fetchRequestBody(true, kept)); status != http.StatusOK || !strings.Contains(answer, "not our ref") {
		t.Errorf("slow check: status=%d answer=%q", status, answer)
	}
	if status, _ := post(main.URL, "/git/sample.git/git-upload-pack", "version=2", pkt("command=fetch\n")+"0001"+pkt("want nothex\n")+"0000"); status != http.StatusBadRequest {
		t.Errorf("slow check, bad request: status=%d", status)
	}
	fetchCheckHook = nil

	// Stock clients keep working, including a fetch of a reachable commit by
	// its ID and a shallow clone. Partial clones are not served.
	for _, address := range []string{main.URL + "/git/sample.git", share.URL + "/s/sample.git"} {
		for _, protocol := range []string{"protocol.version=0", "protocol.version=2"} {
			clone := filepath.Join(t.TempDir(), "clone")
			runHTTPGit(t, "", "-c", protocol, "clone", "-q", address, clone)
			if got := httpGitOutput(t, clone, "rev-parse", "origin/main"); got != tip {
				t.Fatalf("%s %s: clone has %s, want %s", address, protocol, got, tip)
			}
			runHTTPGit(t, clone, "-c", protocol, "fetch", "-q", "origin", "--tags")
			if protocol == "protocol.version=2" {
				runHTTPGit(t, clone, "-c", protocol, "fetch", "-q", "--depth=1", "origin", older)
			}
		}
		runHTTPGit(t, "", "-c", "protocol.version=2", "clone", "-q", "--depth=1", address, filepath.Join(t.TempDir(), "shallow"))
	}
}

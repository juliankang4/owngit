package githttp

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/testfixture"
)

// collisionCommit writes a commit whose tree names one path twice into
// directory, which may be a bare repository, and returns the commit ID. Only a
// repository written before the receive-side object check holds such a tree,
// so an ordinary push cannot store one: the fixture is written directly.
func collisionCommit(t *testing.T, directory string) string {
	t.Helper()
	object := func(input []byte, arguments ...string) string {
		return gitInput(t, directory, input, arguments...)
	}
	blob := object([]byte("collision fixture\n"), "hash-object", "-w", "--stdin")
	inner := object([]byte("100644 blob "+blob+"\tc\n"), "mktree")
	inner = object([]byte("040000 tree "+inner+"\tb\n"), "mktree")
	outer := object([]byte("100644 blob "+blob+"\tc\n"), "mktree")
	var raw bytes.Buffer
	for _, entry := range []struct{ mode, name, oid string }{
		{"40000", "a", inner},
		{"40000", "a/b", outer},
	} {
		raw.WriteString(entry.mode + " " + entry.name)
		raw.WriteByte(0)
		decoded, err := hex.DecodeString(entry.oid)
		noErr(t, err)
		raw.Write(decoded)
	}
	tree := object(raw.Bytes(), "hash-object", "--literally", "-t", "tree", "-w", "--stdin")
	// The literal form writes the record Git warns about instead of refusing.
	return object([]byte("tree names one path twice\n"), "-c", "user.name=Object Check",
		"-c", "user.email=check@example.invalid", "commit-tree", tree)
}

// gitInput runs git in directory with input on standard input.
func gitInput(t *testing.T, directory string, input []byte, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Stdin = bytes.NewReader(input)
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// pushFixture is a repository that answered one ordinary push, with its bare
// path and the pushed tip.
func pushFixture(t *testing.T) (*Handler, *httptest.Server, string, string) {
	t.Helper()
	manager, runner := newHTTPTestRepository(t)
	remote, err := manager.Path("sample")
	noErr(t, err)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "-q", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Object Check")
	runHTTPGit(t, work, "config", "user.email", "check@example.invalid")
	runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/sample.git")
	noErr(t, os.WriteFile(filepath.Join(work, "ok.txt"), []byte("ok\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "ok")
	runHTTPGit(t, work, "push", "-q", "origin", "main")
	return handler, server, remote, work
}

// A push that carries a tree naming one path twice changes no ref: the
// receive-side object check refuses the objects, and the client and the server
// log both name the check. An ordinary push to the same repository succeeds,
// so the check does not refuse what a real push sends.
func TestPushRefusesATreeThatNamesOnePathTwice(t *testing.T) {
	_, _, remote, work := pushFixture(t)
	logs := captureLog(t)
	collision := collisionCommit(t, work)
	output, err := httpGitCombined(work, "push", "origin", collision+":refs/heads/collision")
	if err == nil {
		t.Fatalf("the push was accepted:\n%s", output)
	}
	for _, want := range []string{"fsck error", "remote rejected", "-> collision"} {
		if !strings.Contains(output, want) {
			t.Errorf("the client was not told %q:\n%s", want, output)
		}
	}
	refs := httpGitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, "collision") {
		t.Fatalf("the refused push created a ref: %s", refs)
	}
	if !strings.Contains(logs.String(), "push refused: the pushed objects did not pass Git's object checks") {
		t.Fatalf("the server log does not name the object check:\n%s", logs.String())
	}
}

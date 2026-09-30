package githttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A push follows the kept history and default branch protection saved when
// it starts: the server default, a repository's override of it, and the
// protection of whichever branch is the default now. Turning kept history
// off leaves what was kept before in place.
func TestPushFollowsKeptHistoryAndDefaultBranchProtection(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, runner := newHTTPTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "policy", "", repository.CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("policy")
			noErr(t, err)
			handler, err := New(runner, manager, "")
			noErr(t, err)
			handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
			server := httptest.NewServer(handler)
			defer server.Close()

			work := filepath.Join(t.TempDir(), "work")
			runHTTPGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runHTTPGit(t, work, "config", "user.name", "Policy Test")
			runHTTPGit(t, work, "config", "user.email", "policy@example.invalid")
			runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/policy.git")
			commit := func(name string) string {
				noErr(t, os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o600))
				runHTTPGit(t, work, "add", name)
				runHTTPGit(t, work, "commit", "-m", name)
				return httpGitOutput(t, work, "rev-parse", "HEAD")
			}
			ref := func(name string) string {
				output, err := httpGitCombined("", "--git-dir", remote, "rev-parse", "--verify", "--quiet", name)
				if err != nil {
					return ""
				}
				return strings.TrimSpace(output)
			}
			kept := func(oid string) bool { return ref(repository.RetainedRefName("heads", oid)) == oid }
			// rewrite force-pushes a new root commit to branch and returns
			// the tip it replaced.
			rewrites := 0
			rewrite := func(branch string) (string, string, error) {
				rewrites++
				name := fmt.Sprintf("rewrite-%d", rewrites)
				replaced := ref("refs/heads/" + branch)
				runHTTPGit(t, work, "checkout", "-q", "--orphan", name)
				tip := commit(name)
				output, err := httpGitCombined(work, "push", "--force", "origin", tip+":refs/heads/"+branch)
				return replaced, output, err
			}
			saveServer := func(keep bool) { noErr(t, manager.Store.SavePolicies(ctx, state.PolicyChange{KeptHistory: &keep})) }
			saveRepository := func(change state.RepositoryRefPolicyChange) {
				_, err := manager.Store.SaveRepositoryRefPolicy(ctx, "policy", change)
				noErr(t, err)
			}

			commit("one")
			runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/topic", "HEAD:refs/heads/doomed")

			// The default keeps history.
			replaced, output, err := rewrite("main")
			noErr(t, err, output)
			if !kept(replaced) {
				t.Fatal("the default did not keep a rewritten tip")
			}
			firstKept := replaced

			// Server default off: nothing new is kept, and nothing kept goes.
			saveServer(false)
			replaced, output, err = rewrite("main")
			noErr(t, err, output)
			if kept(replaced) || !kept(firstKept) {
				t.Fatalf("server off: new kept=%v, earlier kept=%v", kept(replaced), kept(firstKept))
			}
			doomed := ref("refs/heads/doomed")
			runHTTPGit(t, work, "push", "origin", ":refs/heads/doomed")
			// The tip is the first commit, which is kept already; only the
			// deleted branch's own record would be new.
			if ref(repository.ProvenanceRefName("heads", "doomed", doomed)) != "" {
				t.Fatal("server off kept a deleted branch")
			}

			// A repository's own choice overrides the server default both ways.
			on, off := state.KeptHistoryOn, state.KeptHistoryOff
			saveRepository(state.RepositoryRefPolicyChange{KeptHistory: &on})
			replaced, output, err = rewrite("topic")
			noErr(t, err, output)
			if !kept(replaced) {
				t.Fatal("repository on did not keep while the server is off")
			}
			saveServer(true)
			saveRepository(state.RepositoryRefPolicyChange{KeptHistory: &off})
			replaced, output, err = rewrite("topic")
			noErr(t, err, output)
			if kept(replaced) {
				t.Fatal("repository off kept while the server is on")
			}

			// Protection refuses rewriting and deleting the default branch
			// only; a fast-forward and every other branch still work.
			protect := true
			saveRepository(state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
			before := ref("refs/heads/main")
			for _, attempt := range []struct {
				push    func() (string, string, error)
				message string
			}{
				{func() (string, string, error) { return rewrite("main") }, "OwnGit protects the default branch main and refused a push that is not a fast-forward."},
				{func() (string, string, error) {
					output, err := httpGitCombined(work, "push", "origin", ":refs/heads/main")
					return "", output, err
				}, "OwnGit protects the default branch main and refused deleting it."},
			} {
				_, output, err := attempt.push()
				if err == nil || !strings.Contains(output, attempt.message) {
					t.Fatalf("protected push: err=%v output=%s", err, output)
				}
				if got := ref("refs/heads/main"); got != before {
					t.Fatalf("main moved from %s to %s under protection", before, got)
				}
			}
			runHTTPGit(t, work, "checkout", "-q", "-B", "main", before)
			forward := commit("forward")
			runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
			if ref("refs/heads/main") != forward {
				t.Fatal("a fast-forward of the protected default branch was refused")
			}
			if _, output, err := rewrite("topic"); err != nil {
				t.Fatalf("another branch was refused: %v %s", err, output)
			}

			// Protection follows the default branch.
			noErr(t, manager.SetDefaultBranch(ctx, "policy", "topic"))
			if _, output, err := rewrite("main"); err != nil {
				t.Fatalf("the former default branch is still protected: %v %s", err, output)
			}
			if _, output, err := rewrite("topic"); err == nil || !strings.Contains(output, "default branch topic") {
				t.Fatalf("the new default branch is not protected: %v %s", err, output)
			}

			// A saved choice that cannot be read stops the push, and the
			// answer says what to set again.
			noErr(t, manager.Store.Exec(ctx, `PRAGMA ignore_check_constraints=ON; UPDATE repository_policies SET retain_history=7 WHERE repository_id='policy'; PRAGMA ignore_check_constraints=OFF`))
			output, err = httpGitCombined(work, "push", "origin", "HEAD:refs/heads/fresh")
			if err == nil || !strings.Contains(output, "remote: This repository's saved kept history and default branch protection cannot be read.") {
				t.Fatalf("push with an unreadable policy: err=%v output=%s", err, output)
			}
			response, err := http.Post(server.URL+"/git/policy.git/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(""))
			noErr(t, err)
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "kept history and default branch protection cannot be read") {
				t.Fatalf("receive-pack with an unreadable policy answered %d %s", response.StatusCode, body)
			}
			if ref("refs/heads/fresh") != "" {
				t.Fatal("a push with an unreadable policy created a branch")
			}
		})
	}
}

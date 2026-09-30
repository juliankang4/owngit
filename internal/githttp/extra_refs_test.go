package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A push changes refs under the repository's extra ref namespaces, such as
// notes, without kept history, while branches keep their history and
// protection. Other namespaces, other spellings of a listed one and an
// unreadable list are refused with the setting to change.
func TestPushFollowsExtraRefNamespaces(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, runner := newHTTPTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "notes", "", repository.CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("notes")
			noErr(t, err)
			handler, err := New(runner, manager, "")
			noErr(t, err)
			handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
			server := httptest.NewServer(handler)
			defer server.Close()

			work := filepath.Join(t.TempDir(), "work")
			runHTTPGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runHTTPGit(t, work, "config", "user.name", "Notes Test")
			runHTTPGit(t, work, "config", "user.email", "notes@example.invalid")
			runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/notes.git")
			runHTTPGit(t, work, "commit", "--allow-empty", "-q", "-m", "one")
			runHTTPGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
			runHTTPGit(t, work, "notes", "add", "-m", "first note")

			output, err := httpGitCombined(work, "push", "origin", "refs/notes/commits")
			if err == nil || !strings.Contains(output, "owngit repo settings set --extra-ref-prefixes") {
				t.Fatalf("notes push without the namespace: err=%v output=%s", err, output)
			}
			protect := true
			_, err = manager.Store.SaveRepositoryRefPolicy(ctx, "notes", state.RepositoryRefPolicyChange{
				ExtraRefPrefixes: &[]string{"refs/notes/"}, ProtectDefaultBranch: &protect,
			})
			noErr(t, err)
			runHTTPGit(t, work, "push", "-q", "origin", "refs/notes/commits")
			first := httpGitOutput(t, work, "rev-parse", "refs/notes/commits")
			runHTTPGit(t, work, "notes", "add", "-f", "-m", "rewritten note")
			runHTTPGit(t, work, "update-ref", "refs/notes/commits", httpGitOutput(t, work, "commit-tree", "-m", "unrelated", httpGitOutput(t, work, "rev-parse", "refs/notes/commits^{tree}")))
			runHTTPGit(t, work, "push", "-q", "--force", "origin", "refs/notes/commits")
			if kept, _ := httpGitCombined("", "--git-dir", remote, "for-each-ref", "refs/owngit/retained/"); strings.Contains(kept, first) {
				t.Fatalf("an overwritten note was kept: %s", kept)
			}
			clone := filepath.Join(t.TempDir(), "clone")
			runHTTPGit(t, "", "clone", "-q", server.URL+"/git/notes.git", clone)
			runHTTPGit(t, clone, "fetch", "-q", "origin", "refs/notes/commits:refs/notes/commits")
			if note := httpGitOutput(t, clone, "notes", "show", "HEAD"); note != "rewritten note" {
				t.Fatalf("fetched note %q", note)
			}
			for _, refused := range []string{"refs/Notes/commits", "refs/other/x"} {
				if output, err := httpGitCombined(work, "push", "origin", "HEAD:"+refused); err == nil {
					t.Fatalf("push to %s was accepted: %s", refused, output)
				}
			}
			runHTTPGit(t, work, "commit", "--allow-empty", "-q", "-m", "two")
			runHTTPGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
			runHTTPGit(t, work, "reset", "-q", "--hard", "HEAD~1")
			runHTTPGit(t, work, "commit", "--allow-empty", "-q", "-m", "rewritten")
			if output, err := httpGitCombined(work, "push", "--force", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(output, "protects the default branch") {
				t.Fatalf("rewriting the protected default branch: err=%v output=%s", err, output)
			}
			runHTTPGit(t, work, "push", "-q", "origin", ":refs/notes/commits")

			noErr(t, manager.Store.Exec(ctx, `UPDATE repository_policies SET extra_ref_prefixes='["refs/heads/"]' WHERE repository_id='notes'`))
			if output, err := httpGitCombined(work, "push", "origin", "HEAD:refs/heads/topic"); err == nil || !strings.Contains(output, "extra ref namespaces cannot be read") {
				t.Fatalf("push with unreadable namespaces: err=%v output=%s", err, output)
			}
		})
	}
}

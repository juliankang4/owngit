package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceivePackRetentionObjectFormats(t *testing.T) {
	for _, format := range []string{ObjectFormatSHA1, ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			manager, _, _ := newTestRepository(t)
			_, err := manager.CreateWithOptions(context.Background(), "formatted", "", CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("formatted")
			noErr(t, err)
			work := filepath.Join(t.TempDir(), "work")
			runGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runGit(t, work, "config", "user.name", "Test Author")
			runGit(t, work, "config", "user.email", "test@example.invalid")
			runGit(t, work, "remote", "add", "origin", remote)
			commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
			first := gitOutput(t, work, "rev-parse", "HEAD")
			// Seed imported history without receive-pack so creation and deletion
			// failures can be observed independently.
			runGit(t, remote, "fetch", work, "HEAD:refs/heads/main", "HEAD:refs/heads/to-delete")
			t.Run("create", func(t *testing.T) {
				runGit(t, work, "push", "origin", "HEAD:refs/heads/created")
				assertRef(t, remote, "refs/heads/created", first)
			})
			commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
			second := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
			assertRef(t, remote, "refs/heads/main", second)
			assertMissingRef(t, remote, RetainedRefName("heads", first))
			t.Run("delete", func(t *testing.T) {
				runGit(t, work, "push", "origin", ":refs/heads/to-delete")
				assertMissingRef(t, remote, "refs/heads/to-delete")
				assertRef(t, remote, RetainedRefName("heads", first), first)
				assertRef(t, remote, ProvenanceRefName("heads", "to-delete", first), first)
			})
			runGit(t, work, "push", "--force", "origin", first+":refs/heads/main")
			assertRef(t, remote, "refs/heads/main", first)
			assertRef(t, remote, RetainedRefName("heads", second), second)
			assertRef(t, remote, ProvenanceRefName("heads", "main", second), second)
			runGit(t, work, "tag", "-a", "annotated", "-m", "first annotation", second)
			tag := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
			runGit(t, work, "push", "origin", "refs/tags/annotated")
			runGit(t, work, "tag", "-f", "-a", "annotated", "-m", "replacement annotation", first)
			replacementTag := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
			runGit(t, work, "push", "--force", "origin", "refs/tags/annotated")
			assertRef(t, remote, RetainedRefName("tags", tag), tag)
			runGit(t, work, "push", "origin", ":refs/tags/annotated")
			assertMissingRef(t, remote, "refs/tags/annotated")
			assertRef(t, remote, RetainedRefName("tags", replacementTag), replacementTag)
			assertRef(t, remote, ProvenanceRefName("tags", "annotated", tag), tag)
			assertRef(t, remote, ProvenanceRefName("tags", "annotated", replacementTag), replacementTag)
			for _, ref := range []string{"refs/owngit/reserved", "refs/meta/rejected"} {
				if output, err := gitCombined(work, "push", "origin", "HEAD:"+ref); err == nil {
					t.Fatalf("unsupported ref push succeeded: %s", output)
				}
				assertMissingRef(t, remote, ref)
			}
			commitFile(t, work, "three", "three", "2024-01-03T00:00:00Z")
			third := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
			for _, refusal := range []struct{ old, new, message string }{
				{third, strings.Repeat("f", len(third)), "could not compare branch history"},
				{second, third, "current ref changed concurrently"},
				{third[:len(third)-1], third, "invalid object ID width"},
			} {
				if output, err := executeUpdateHook(manager.Git, remote, "refs/heads/main", refusal.old, refusal.new); err == nil || !strings.Contains(string(output), refusal.message) {
					t.Fatalf("hook refusal = %v %s, want %s", err, output, refusal.message)
				}
			}
			lock := filepath.Join(remote, filepath.FromSlash(RetainedRefName("heads", third))) + ".lock"
			noErr(t, os.WriteFile(lock, []byte("synthetic retention failure"), 0o600))
			if output, err := gitCombined(work, "push", "--force", "origin", first+":refs/heads/main"); err == nil || !strings.Contains(output, "could not preserve previous history") {
				t.Fatalf("retention failure was not reported: %v %s", err, output)
			}
			assertRef(t, remote, "refs/heads/main", third)
			runGit(t, remote, "reflog", "expire", "--expire=now", "--all")
			runGit(t, remote, "gc", "--prune=now")
			for _, oid := range []string{first, second, tag, replacementTag, third} {
				runGit(t, remote, "cat-file", "-e", oid+"^{object}")
			}
		})
	}
}

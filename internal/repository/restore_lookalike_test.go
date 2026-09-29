package repository

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A restore reads and writes its target branch by its exact name and
// refuses a target that another branch's name makes the same file where
// storage ignores letter case, whether the target exists or would be
// created, and leaves every branch as it was.
func TestRestoreRefusesALookAlikeTargetBranch(t *testing.T) {
	for _, format := range []string{ObjectFormatSHA1, ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			manager, _, _ := newTestRepository(t)
			ctx := context.Background()
			_, err := manager.CreateWithOptions(ctx, "formatted", "", CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("formatted")
			noErr(t, err)
			work := filepath.Join(t.TempDir(), "work")
			runGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runGit(t, work, "config", "user.name", "Test Author")
			runGit(t, work, "config", "user.email", "test@example.invalid")
			commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
			first := gitOutput(t, work, "rev-parse", "HEAD")
			commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
			second := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, remote, "fetch", "-q", work, "HEAD:refs/heads/main", first+":refs/heads/Release/one")
			runGit(t, "", "--git-dir", remote, "pack-refs", "--all")
			branches := func() string {
				return gitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			}
			refused := func(target, want string) {
				t.Helper()
				before := branches()
				request := RestoreRequest{Source: first, Target: target, Mode: RestoreAll}
				_, err := manager.PreviewRestore(ctx, "formatted", request)
				if !errors.Is(err, ErrRestoreInvalid) || !strings.Contains(err.Error(), want) {
					t.Fatalf("preview onto %s: %v, want invalid naming %s", target, err, want)
				}
				for _, expected := range []string{second, first, strings.Repeat("0", len(first))} {
					request.ExpectedHead = expected
					if _, err := manager.ApplyRestore(ctx, "formatted", request); !errors.Is(err, ErrRestoreInvalid) {
						t.Fatalf("apply onto %s at %s: %v, want invalid", target, expected, err)
					}
				}
				if after := branches(); after != before {
					t.Fatalf("a refused restore onto %s changed the branches:\n%s\nwant\n%s", target, after, before)
				}
			}

			// Creating Main beside the packed main, or a branch in a folder
			// spelled otherwise, is refused.
			refused("Main", "existing main")
			refused("release/two", "existing Release")

			// With both spellings present, neither is read or written.
			runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/Main", first)
			if got := branches(); !strings.Contains(got, "refs/heads/Main "+first) || !strings.Contains(got, "refs/heads/main "+second) {
				t.Fatalf("fixture branches:\n%s", got)
			}
			refused("main", "existing Main")
			refused("Main", "existing main")
			runGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/Main", first)

			// Controls: the exact existing branch and a new branch spelled
			// like no other restore as before.
			preview, err := manager.PreviewRestore(ctx, "formatted", RestoreRequest{Source: first, Target: "main", Mode: RestoreAll})
			noErr(t, err)
			if preview.ExpectedHead != second {
				t.Fatalf("main previewed at %s, want %s", preview.ExpectedHead, second)
			}
			restored, err := manager.ApplyRestore(ctx, "formatted", RestoreRequest{Source: first, Target: "main", Mode: RestoreAll, ExpectedHead: second})
			noErr(t, err)
			assertRef(t, remote, "refs/heads/main", restored.CommitOID)
			assertRef(t, remote, "refs/heads/main^", second)
			created, err := manager.ApplyRestore(ctx, "formatted", RestoreRequest{Source: second, Target: "feature", Mode: RestoreAll, ExpectedHead: strings.Repeat("0", len(second))})
			noErr(t, err)
			if !created.Created {
				t.Fatal("the new branch was not created")
			}
			assertRef(t, remote, "refs/heads/feature", second)
		})
	}
}

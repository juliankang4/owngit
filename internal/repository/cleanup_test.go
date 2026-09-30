package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Unused object cleanup removes only unreachable objects older than the
// grace period, loose and packed, in SHA-1 and SHA-256 repositories. It
// keeps every object a ref reaches, kept history and pull request refs
// included, younger unreachable objects and packs with a .keep file. Off,
// full maintenance removes nothing.
func TestCleanupRemovesOnlyOldUnreachableObjects(t *testing.T) {
	for _, format := range []string{ObjectFormatSHA1, ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, _, _ := newTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "cleanup", "", CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("cleanup")
			noErr(t, err)
			work := filepath.Join(t.TempDir(), "work")
			runGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runGit(t, work, "config", "user.name", "Test Author")
			runGit(t, work, "config", "user.email", "test@example.invalid")
			runGit(t, work, "remote", "add", "origin", remote)
			for index := range 3 {
				commitFile(t, work, "content "+string(rune('a'+index))+"\n", "commit", "2024-01-01T00:00:00Z")
				runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
			}
			forced := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "-q", "--force", "origin", "HEAD~1:refs/heads/main")
			pull := gitInputOutput(t, remote, []byte("pull request only\n"), "hash-object", "-w", "--stdin")
			runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/pull-requests/1/source", gitInputOutput(t, remote, nil, "mktree"))
			runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/pull-requests/1/blob", pull)

			old := time.Now().Add(-30 * 24 * time.Hour)
			age := func(paths ...string) {
				for _, path := range paths {
					noErr(t, os.Chtimes(path, old, old))
				}
			}
			loosePath := func(oid string) string { return filepath.Join(remote, "objects", oid[:2], oid[2:]) }
			packPaths := func(id string, extensions ...string) []string {
				var paths []string
				for _, extension := range extensions {
					paths = append(paths, filepath.Join(remote, "objects", "pack", "pack-"+id+extension))
				}
				return paths
			}
			oldLoose := gitInputOutput(t, remote, []byte("old unreachable loose\n"), "hash-object", "-w", "--stdin")
			age(loosePath(oldLoose))
			oldPacked := gitInputOutput(t, remote, []byte("old unreachable packed\n"), "hash-object", "-w", "--stdin")
			packID := gitInputOutput(t, remote, []byte(oldPacked+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
			noErr(t, os.Remove(loosePath(oldPacked)))
			age(packPaths(packID, ".pack", ".idx")...)
			oldKept := gitInputOutput(t, remote, []byte("old unreachable in a kept pack\n"), "hash-object", "-w", "--stdin")
			keptID := gitInputOutput(t, remote, []byte(oldKept+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
			noErr(t, os.Remove(loosePath(oldKept)))
			noErr(t, os.WriteFile(packPaths(keptID, ".keep")[0], []byte("synthetic keep\n"), 0o600))
			age(packPaths(keptID, ".pack", ".idx", ".keep")...)
			// Full maintenance, with cleanup off, removes nothing.
			schedule := MaintenanceSchedule{}.withDefaults()
			full := inventory(t, remote)
			_, err = manager.maintain(ctx, "cleanup", MaintenanceFull, schedule)
			noErr(t, err, "full maintenance with cleanup off")
			assertSameInventory(t, full, inventory(t, remote))
			recent := gitInputOutput(t, remote, []byte("recent unreachable\n"), "hash-object", "-w", "--stdin")
			// Everything else, reachable or not, is old, so only
			// reachability keeps what a ref reaches.
			noErr(t, filepath.WalkDir(filepath.Join(remote, "objects"), func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() && path != loosePath(recent) {
					err = os.Chtimes(path, old, old)
				}
				return err
			}))
			exists := func(oid string) bool {
				_, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", oid)
				return err == nil
			}
			before := inventory(t, remote)

			schedule.cleanupGrace = 14 * 24 * time.Hour
			steps, err := manager.maintain(ctx, "cleanup", MaintenanceCleanup, schedule)
			noErr(t, err, "cleanup")
			if steps != 4 {
				t.Fatalf("cleanup ran %d steps", steps)
			}
			if exists(oldLoose) || exists(oldPacked) {
				t.Fatalf("old unreachable objects stayed: loose=%v packed=%v", exists(oldLoose), exists(oldPacked))
			}
			for name, oid := range map[string]string{"recent": recent, "kept pack": oldKept, "kept history": forced, "pull request": pull} {
				if !exists(oid) {
					t.Fatalf("cleanup removed the %s object %s", name, oid)
				}
			}
			if _, err := os.Stat(packPaths(keptID, ".keep")[0]); err != nil {
				t.Fatalf("the .keep file: %v", err)
			}
			after := inventory(t, remote)
			if before.refs != after.refs || before.head != after.head {
				t.Fatalf("refs changed:\n%s\n%s", before.refs, after.refs)
			}
			runGit(t, "", "--git-dir", remote, "fsck", "--connectivity-only", "--no-dangling")
		})
	}
}

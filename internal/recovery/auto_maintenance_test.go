package recovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

// autoMaintenanceTraces are the files Git's automatic maintenance holds while
// it runs or leaves behind in a repository. OwnGit's restore writes none of
// them itself.
var autoMaintenanceTraces = []string{
	filepath.Join("objects", "maintenance.lock"),
	"gc.pid",
	"gc.log",
	filepath.Join("objects", "info", "commit-graph"),
	filepath.Join("objects", "info", "commit-graphs"),
	filepath.Join("objects", "pack", "multi-pack-index"),
}

// TestRestoreStartsNoGitAutomaticMaintenance restores a repository with more
// commits than Git's automatic commit-graph threshold (100). The fetch that
// fills the staged repository runs before OwnGit writes the repository's
// config, so only the runner's own settings can keep Git from starting its
// automatic maintenance there.
//
// Git normally detaches that maintenance and returns from the fetch before
// it writes anything, so its output would appear at an unknown later time.
// The test therefore keeps it in the foreground: any maintenance the fetch
// starts has finished, with its output in place, when the fetch returns.
func TestRestoreStartsNoGitAutomaticMaintenance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	remote, err := manager.Path("project")
	noErr(t, err)
	if _, err := manager.Git.Run(ctx, remote, strings.NewReader(linearHistory(150)), "--git-dir", ".", "fast-import", "--quiet"); err != nil {
		t.Fatalf("write history: %v", err)
	}
	if count := gitOutput(t, "", "--git-dir", remote, "rev-list", "--count", "refs/heads/main"); count != "151" {
		t.Fatalf("source history has %s commits, want 151", count)
	}
	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))

	stateTarget := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	repositoryTarget := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	var staged []string
	operations := defaultRestoreOperations()
	prepare := operations.prepareExisting
	operations.prepareExisting = func(ctx context.Context, manager *repository.Manager, hookRuntime *gitexec.Runner) error {
		// The staged repository is filled but has no OwnGit config yet.
		staged = autoMaintenanceTracesIn(filepath.Join(manager.Root, "project.git"))
		return prepare(ctx, manager, hookRuntime)
	}
	gitPath := foregroundMaintenanceGit(t, manager.Git.GitPath, filepath.Join(root, "git-wrapper"))
	noErr(t, restore(ctx, backup, stateTarget, repositoryTarget, gitPath, operations))
	restored := filepath.Join(repositoryTarget, "project.git")
	if left := autoMaintenanceTracesIn(restored); len(staged) != 0 || len(left) != 0 {
		t.Fatalf("restore started Git's automatic maintenance: staged repository before config %v, restored repository %v", staged, left)
	}
	if count := gitOutput(t, "", "--git-dir", restored, "rev-list", "--count", "refs/heads/main"); count != "151" {
		t.Fatalf("restored history has %s commits, want 151", count)
	}
}

// foregroundMaintenanceGit returns a Git executable for restore that runs
// automatic maintenance in the foreground. On Unix it is a wrapper that adds
// maintenance.autoDetach=false, which Git passes on to the maintenance it
// starts. Git for Windows cannot detach and already runs maintenance in the
// foreground, so there it is gitPath itself.
func foregroundMaintenanceGit(t *testing.T, gitPath, directory string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return gitPath
	}
	noErr(t, os.Mkdir(directory, 0o700))
	wrapper := filepath.Join(directory, "git")
	script := "#!/bin/sh\nexec " + recoveryShellQuote(gitPath) + " -c maintenance.autoDetach=false \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	return wrapper
}

// linearHistory returns a fast-import stream that adds count commits to main.
func linearHistory(count int) string {
	var stream strings.Builder
	for i := 0; i < count; i++ {
		content := fmt.Sprintf("revision %d\n", i)
		message := fmt.Sprintf("revision %d\n", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter Restore Test <restore@example.invalid> %d +0000\ndata %d\n%s", 1700000000+i, len(message), message)
		if i == 0 {
			stream.WriteString("from refs/heads/main^0\n")
		}
		fmt.Fprintf(&stream, "M 100644 inline file\ndata %d\n%s\n", len(content), content)
	}
	return stream.String()
}

func autoMaintenanceTracesIn(repositoryPath string) []string {
	var found []string
	for _, name := range autoMaintenanceTraces {
		if _, err := os.Lstat(filepath.Join(repositoryPath, name)); err == nil {
			found = append(found, filepath.ToSlash(name))
		}
	}
	return found
}

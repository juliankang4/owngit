package recovery

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

var backupScale = flag.Int("backup-scale", 0, "number of repositories for TestBackupPauseAtScale; 0 skips it")

// TestBackupPauseAtScale measures how long a backup made while OwnGit
// serves holds Git writes, with -backup-scale repositories, while a writer
// moves a ref of the last repository in a loop the way a push does. It
// fails when a repository's writes waited longer than 10 seconds.
func TestBackupPauseAtScale(t *testing.T) {
	count := *backupScale
	if count == 0 {
		t.Skip("run with -args -backup-scale=N to measure a backup of N repositories")
	}
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	project, err := manager.Path("project")
	noErr(t, err)
	tip := gitOutput(t, project, "--git-dir", ".", "rev-parse", "refs/heads/main")
	created := time.Now().UTC().Truncate(time.Second)
	var last string
	for index := range count - 1 {
		id := fmt.Sprintf("scale-%05d", index)
		path := filepath.Join(filepath.Dir(project), id+".git")
		runGit(t, "", "clone", "--bare", "--quiet", project, path)
		noErr(t, store.AddRepository(ctx, state.Repository{ID: id, Name: id, CreatedAt: created}))
		last = id
	}
	lastPath, err := manager.Path(last)
	noErr(t, err)

	// The writer measures how long each ref update waited for the lock.
	stop := make(chan struct{})
	var waits []time.Duration
	var writer sync.WaitGroup
	writer.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			lock := manager.Locks.For(last)
			started := time.Now()
			lock.Lock()
			waits = append(waits, time.Since(started))
			_, err := manager.Git.Run(ctx, lastPath, nil, "--git-dir", ".", "update-ref", "refs/heads/moving", tip)
			lock.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	started := time.Now()
	report, err := CreateWhileServing(ctx, store, manager, filepath.Join(root, "backup"))
	total := time.Since(started)
	close(stop)
	writer.Wait()
	noErr(t, err)
	if len(waits) == 0 {
		t.Fatal("the writer never ran")
	}
	slices.Sort(waits)
	t.Logf("repositories=%d attempts=%d longest_hold=%s (%s) backup=%s writer_updates=%d writer_wait_max=%s writer_wait_median=%s",
		report.Repositories, report.Attempts, report.LongestHold, report.LongestHoldRepository, total,
		len(waits), waits[len(waits)-1], waits[len(waits)/2])
	if report.LongestHold > 10*time.Second {
		t.Fatalf("writes waited %s for the backup", report.LongestHold)
	}
}

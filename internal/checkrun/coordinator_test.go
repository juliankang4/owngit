package checkrun

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestBoundedBranchBatchesMakeFairProgressPastFirstPage(t *testing.T) {
	branches := make([]repository.Ref, 130)
	for index := range branches {
		branches[index] = repository.Ref{Name: fmt.Sprintf("branch-%03d", index)}
	}
	seen := make(map[string]bool)
	after := ""
	for pass := 0; pass < 3; pass++ {
		batch := boundedBranchesAfter(branches, after, maximumObservedRefs)
		if len(batch) != maximumObservedRefs {
			t.Fatalf("pass %d batch size=%d", pass, len(batch))
		}
		for _, branch := range batch {
			seen[branch.Name] = true
		}
		after = batch[len(batch)-1].Name
	}
	if len(seen) != len(branches) {
		t.Fatalf("fair batches reached %d/%d branches", len(seen), len(branches))
	}
}

func TestCoordinatorStartStopAndRestartReturn(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	defer store.Close()
	coordinator := &Coordinator{
		Store: store, Repositories: &repository.Manager{Store: store},
		WorkspaceRoot: filepath.Join(root, "workspaces"), Interval: time.Hour,
	}
	for cycle := 0; cycle < 2; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan error, 1)
		go func() { started <- coordinator.Start(ctx) }()
		select {
		case err := <-started:
			if err != nil {
				cancel()
				t.Fatalf("cycle %d start: %v", cycle, err)
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatalf("cycle %d start did not return", cycle)
		}
		stopContext, stopCancel := context.WithTimeout(context.Background(), time.Second)
		if err := coordinator.Stop(stopContext); err != nil {
			stopCancel()
			cancel()
			t.Fatalf("cycle %d stop: %v", cycle, err)
		}
		stopCancel()
		cancel()
	}
}

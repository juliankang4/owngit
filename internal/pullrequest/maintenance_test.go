package pullrequest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/repository"
)

// Pull request revision and merge refs survive repository maintenance, and
// the recovery that preparation runs at startup still succeeds afterwards.
func TestPullRequestRecoveryAndMergeSucceedAfterMaintenance(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	fixture.commitFile("feature.txt", "one\n", "feature one")
	fixture.push("HEAD:refs/heads/feature")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Maintained", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	noErr(t, err)
	sourceOID := fixture.commitFile("feature.txt", "two\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	targetOID := fixture.commitFile("main.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	before, err := fixture.service.List(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	refsBefore := fixture.gitOutput("--git-dir", fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)")

	var mu sync.Mutex
	var lines []string
	logf := func(format string, arguments ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, arguments...))
	}
	completed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) == 1 && strings.Contains(lines[0], "(small) completed")
	}
	noon := time.Date(2026, 9, 24, 12, 0, 0, 0, time.Local)
	offset := noon.Sub(time.Now())
	maintenance, stopMaintenance := context.WithCancel(fixture.ctx)
	noErr(t, fixture.manager.StartMaintenance(maintenance, repository.MaintenanceSchedule{
		Idle: time.Millisecond, Now: func() time.Time { return time.Now().Add(offset) },
	}, logf))
	fixture.manager.NoteRepositoryWrite(fixture.repositoryID)
	deadline := time.Now().Add(10 * time.Second)
	for !completed() {
		if time.Now().After(deadline) {
			t.Fatalf("maintenance did not complete: %v", lines)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopMaintenance()
	noErr(t, fixture.manager.StopMaintenance(fixture.ctx))
	if refsAfter := fixture.gitOutput("--git-dir", fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)"); refsAfter != refsBefore {
		t.Fatalf("refs changed:\n%s\n%s", refsBefore, refsAfter)
	}

	preparation, stopPreparation := context.WithCancel(fixture.ctx)
	defer func() {
		stopPreparation()
		noErr(t, fixture.manager.StopPreparation(fixture.ctx))
	}()
	noErr(t, fixture.manager.StartPreparation(preparation, fixture.service.RecoverRepositoryLocked, 10*time.Second, nil))
	if fixture.manager.Preparing(fixture.repositoryID) {
		t.Fatal("pull request recovery failed after maintenance")
	}
	after, err := fixture.service.List(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(after) != 1 || len(before) != 1 || after[0].Number != created.Number || after[0].Source != before[0].Source || after[0].Target != before[0].Target {
		t.Fatalf("pull request changed: before %+v after %+v", before, after)
	}
	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	if merged.Merge == nil || fixture.ref("refs/heads/main") != merged.Merge.OID {
		t.Fatalf("merge after maintenance: %+v", merged.Merge)
	}
}

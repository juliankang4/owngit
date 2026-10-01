package backups

import (
	"context"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

func TestServingBackupRecordsAliasNoticeButVerificationDoesNotInventOne(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	if run.Status != state.BackupSucceeded || run.Verification != state.BackupVerifyPassed {
		t.Fatalf("alias prevented backup or verification: %+v", run)
	}
	for _, want := range []string{recovery.AliasBranchNotice, "project: refs/heads/alias -> refs/heads/main", "git symbolic-ref -- 'refs/heads/alias' 'refs/heads/main'"} {
		if !strings.Contains(run.Message, want) {
			t.Fatalf("serving backup lacks %q: %s", want, run.Message)
		}
	}
	_, err = f.service.StartCheck(context.Background(), run.ID)
	noErr(t, err)
	f.waitForTask(t)
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.Check == nil || status.Check.Status != CheckPassed || status.Check.Message != "" {
		t.Fatalf("verification must not invent alias targets from a portable backup: %+v", status.Check)
	}
	if got := f.run(t, run.ID).Message; got != run.Message {
		t.Fatalf("verification changed the original backup-time notice: %s", got)
	}
}

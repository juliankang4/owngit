package backups

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

func TestServingBackupBoundsManyAliasesAndLogsEveryEntry(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	var aliases []recovery.AliasBranch
	for index := range 40 {
		alias := recovery.AliasBranch{Repository: "project", Name: fmt.Sprintf("refs/heads/alias-%02d", index), Target: "refs/heads/main"}
		git(t, remote, "--git-dir", ".", "symbolic-ref", alias.Name, alias.Target)
		aliases = append(aliases, alias)
	}
	var logs []string
	f.service.Logf = func(format string, arguments ...any) { logs = append(logs, fmt.Sprintf(format, arguments...)) }
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	if run.Status != state.BackupSucceeded || run.Verification != state.BackupVerifyPassed || len(run.Message) > state.MaxBackupRunMessage {
		t.Fatalf("many-alias backup was not recorded within the limit: %+v", run)
	}
	displayed := 0
	log := strings.Join(logs, "\n")
	for _, alias := range aliases {
		entry := fmt.Sprintf("%s: %s -> %s. %s", alias.Repository, alias.Name, alias.Target, alias.ReconnectCommand())
		if strings.Contains(run.Message, alias.Name) {
			displayed++
			if !strings.Contains(run.Message, entry) {
				t.Fatalf("run record cut an alias entry: %s", run.Message)
			}
		}
		if !strings.Contains(log, "alias notice: "+entry+"\n") {
			t.Fatalf("full server log omitted %s: %s", alias.Name, log)
		}
	}
	if displayed == 0 || displayed == len(aliases) || !strings.HasSuffix(run.Message, fmt.Sprintf(recovery.OmittedAliasNotice, len(aliases)-displayed)) {
		t.Fatalf("incorrect summary after %d displayed aliases: %s", displayed, run.Message)
	}
}

func TestAliasRunRecordPreservesWarningsAtMarkerBoundary(t *testing.T) {
	f := newFixture(t)
	report := recovery.CaptureReport{AliasBranches: []recovery.AliasBranch{{Repository: "project", Name: "refs/heads/alias", Target: "refs/heads/main"}}}
	marker := fmt.Sprintf(recovery.OmittedAliasNotice, 1)
	for _, remaining := range []int{len(marker), len(marker) - 1, 0} {
		t.Run(fmt.Sprintf("remaining-%d", remaining), func(t *testing.T) {
			warnings := "The backup is complete, but " + strings.Repeat("w", state.MaxBackupRunMessage-remaining-1-len("The backup is complete, but "))
			run := state.BackupRun{ID: fmt.Sprintf("%032x", remaining+1), Kind: state.BackupRunManual, Status: state.BackupRunning,
				Destination: f.destination, BackupName: namePrefix + "boundary", Verification: state.BackupVerifyNotRun, StartedAt: f.clock.Now()}
			noErr(t, f.store.StartBackupRun(context.Background(), run))
			run.Status, run.Message, run.FinishedAt = state.BackupSucceeded, warnings, f.clock.Now()
			var logs []string
			f.service.Logf = func(format string, arguments ...any) { logs = append(logs, fmt.Sprintf(format, arguments...)) }
			f.service.noteAliases(&run, report)
			noErr(t, f.store.FinishBackupRun(context.Background(), run))
			stored := f.run(t, run.ID)
			want := warnings
			if remaining == len(marker) {
				want += "\n" + marker
			}
			if stored.Message != want || run.Message != want {
				t.Fatalf("warnings or marker were cut: got %q, want %q", stored.Message, want)
			}
			log := strings.Join(logs, "\n") + "\n"
			for _, entry := range strings.Split(report.AliasNotice(), "\n") {
				if !strings.Contains(log, "alias notice: "+entry+"\n") {
					t.Fatalf("full notice missing from the log: %s", log)
				}
			}
			if remaining < len(marker) && !strings.Contains(log, "alias notice did not fit in the backup run record") {
				t.Fatalf("log did not explain why the record has no notice: %s", log)
			}
		})
	}
}

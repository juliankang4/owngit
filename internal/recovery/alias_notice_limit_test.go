package recovery

import (
	"fmt"
	"strings"
	"testing"

	"owngit/internal/state"
)

func TestAliasNoticeLimitKeepsWholeEntriesAndReportsOmissions(t *testing.T) {
	var report CaptureReport
	for index := range 1000 {
		report.AliasBranches = append(report.AliasBranches, AliasBranch{Repository: "project", Name: fmt.Sprintf("refs/heads/alias-%04d", index), Target: "refs/heads/main"})
	}
	notice := report.AliasNoticeWithin(state.MaxBackupRunMessage)
	if len(notice) > state.MaxBackupRunMessage || !strings.Contains(notice, "additional aliases: the full list is in the server log.") {
		t.Fatalf("notice is unbounded or has no omission marker: %d bytes", len(notice))
	}
	displayed := 0
	for _, alias := range report.AliasBranches {
		entry := fmt.Sprintf("%s: %s -> %s. %s", alias.Repository, alias.Name, alias.Target, alias.ReconnectCommand())
		if strings.Contains(notice, alias.Name) {
			displayed++
			if !strings.Contains(notice, entry) {
				t.Fatalf("notice cut a ref or recreation command: %s", alias.Name)
			}
		}
	}
	if displayed == 0 || displayed == len(report.AliasBranches) || !strings.HasSuffix(notice, fmt.Sprintf(OmittedAliasNotice, len(report.AliasBranches)-displayed)) {
		t.Fatalf("incorrect omission count after %d displayed entries", displayed)
	}
}

func TestAliasNoticeBudgetEdges(t *testing.T) {
	report := CaptureReport{AliasBranches: []AliasBranch{{Repository: "project", Name: "refs/heads/alias", Target: "refs/heads/main"}}}
	full := report.AliasNotice()
	marker := fmt.Sprintf(OmittedAliasNotice, 1)
	for _, scenario := range []struct {
		name   string
		budget int
		want   string
	}{
		{"entry-exact", len(full), full},
		{"entry-one-byte-over", len(full) - 1, marker},
		{"marker-exact", len(marker), marker},
		{"marker-one-byte-short", len(marker) - 1, ""},
		{"no-room", -1, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := report.AliasNoticeWithin(scenario.budget); got != scenario.want {
				t.Fatalf("budget %d: got %q, want %q", scenario.budget, got, scenario.want)
			}
		})
	}
}

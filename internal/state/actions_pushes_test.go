package state

import (
	"context"
	"strings"
	"testing"

	"owngit/internal/actions"
)

func TestRecordAcceptedActionsPushes(t *testing.T) {
	for _, test := range []struct {
		name       string
		seed       int
		consumed   int
		deletion   bool
		wantRecord bool
	}{
		{"branch update", 0, 0, false, true},
		{"deletion carries no authority", 0, 0, true, false},
		{"consumed rows are evicted before pending rows", MaximumAcceptedActionsPushes, 2, false, true},
		{"full pending queue records a refusal", MaximumAcceptedActionsPushes, 0, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			old, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
			noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "other", []AcceptedActionsPush{{Ref: "refs/heads/main", NewOID: old}}, fixture.now))
			if test.seed != 0 {
				noErr(t, fixture.store.Exec(ctx, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO actions_accepted_pushes(repository_id,ref_name,old_oid,new_oid,consumed) SELECT 'project','refs/heads/old-'||x,'',?,x=? FROM n`, test.seed, old, test.consumed))
			}
			update := AcceptedActionsPush{Ref: "refs/heads/main", OldOID: old, NewOID: head}
			if test.deletion {
				update.NewOID = ""
			}
			noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []AcceptedActionsPush{update}, fixture.now))
			push, exists, err := fixture.store.AcceptedActionsPush(ctx, "project", update.Ref, head)
			if err != nil || exists != test.wantRecord || exists && push.OldOID != old {
				t.Fatalf("accepted=%+v exists=%v err=%v", push, exists, err)
			}
			var count int
			noErr(t, fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions_accepted_pushes WHERE repository_id='project'`).Scan(&count))
			wantCount := test.seed
			if test.wantRecord && test.seed == 0 {
				wantCount++
			}
			if count != wantCount {
				t.Fatalf("records=%d, want %d", count, wantCount)
			}
			other, err := fixture.store.ActionsPullRequestAdmissionNote(ctx, "other", old)
			if err != nil || other != nil {
				t.Fatalf("another repository lost authority: note=%+v err=%v", other, err)
			}
			if test.seed != 0 {
				var kept bool
				noErr(t, fixture.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM actions_accepted_pushes WHERE repository_id='project' AND ref_name='refs/heads/old-1')`).Scan(&kept))
				if !kept {
					t.Fatal("an unconsumed update was evicted")
				}
			}
			if test.seed != 0 && !test.wantRecord {
				runs, err := fixture.store.ActionsRuns(ctx, "project", 4)
				if err != nil || len(runs) != 1 || runs[0].Outcome != actions.StatusRefused || !strings.HasPrefix(runs[0].WorkflowPath, ActionsRefusedWorkflowPrefix) || len(runs[0].Facts.Notes) != 1 || runs[0].Facts.Notes[0].Code != "workflow.limit" {
					t.Fatalf("queue refusal=%+v err=%v", runs, err)
				}
			}
			if exists {
				noErr(t, fixture.store.ConsumeAcceptedActionsPush(ctx, push.Sequence))
				pending, exists, err := fixture.store.AcceptedActionsPushBySequence(ctx, "project", push.Sequence)
				if err != nil || exists {
					t.Fatalf("consumed push still pending: %+v exists=%v err=%v", pending, exists, err)
				}
				note, err := fixture.store.ActionsPullRequestAdmissionNote(ctx, "project", head)
				if err != nil || note != nil {
					t.Fatalf("consuming push lost pull request authority: note=%+v err=%v", note, err)
				}
			}
		})
	}
}

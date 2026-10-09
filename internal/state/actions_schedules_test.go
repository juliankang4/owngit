package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/actions"
)

func TestActionsSchedules(t *testing.T) {
	for _, test := range []struct {
		name, status, followed, outcome, cron, authorityRef string
		late                                                time.Duration
		missing, consumed, stale, rollback, race, spacing   bool
		want                                                bool
		start                                               time.Time
		horizon                                             bool
	}{
		{name: "claim once", want: true},
		{name: "normal leap admission", cron: "0 0 29 FEB *", start: time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC), want: true},
		{name: "century leap admission", cron: "0 0 29 FEB *", start: time.Date(2095, 3, 1, 0, 0, 0, 0, time.UTC), horizon: true, want: true},
		{name: "century leap missed admission", cron: "0 0 29 FEB *", start: time.Date(2095, 3, 1, 0, 0, 0, 0, time.UTC), late: 366 * 24 * time.Hour, horizon: true, want: true},
		{name: "century leap concurrent claims", cron: "0 0 29 FEB *", start: time.Date(2095, 3, 1, 0, 0, 0, 0, time.UTC), horizon: true, race: true, want: true},
		{name: "century leap admission rollback", cron: "0 0 29 FEB *", start: time.Date(2095, 3, 1, 0, 0, 0, 0, time.UTC), horizon: true, rollback: true},
		{name: "consumed accepted push", consumed: true, want: true},
		{name: "missed slots collapse", late: 14 * time.Minute, want: true},
		{name: "late admission spaces from now", late: 59 * time.Second, want: true},
		{name: "five minute cron catch-up", cron: "*/5 * * * *", late: 4*time.Minute + 59*time.Second, want: true},
		{name: "full queue uses slot", outcome: "not_run", want: true},
		{name: "pending pauses", status: CheckJobPending},
		{name: "waiting pauses", status: CheckJobWaiting},
		{name: "claimed pauses", status: CheckJobClaimed},
		{name: "started pauses", status: CheckJobStarted},
		{name: "ambiguous pauses", status: CheckJobAmbiguous},
		{name: "ambiguous other file does not resume", status: CheckJobAmbiguous, followed: "other"},
		{name: "later same file resumes", status: CheckJobAmbiguous, followed: "same", want: true},
		{name: "finished last run resumes", status: CheckJobPassed, want: true},
		{name: "no accepted default head", missing: true},
		{name: "different branch cannot grant authority", authorityRef: "refs/heads/feature"},
		{name: "changed due time loses claim", stale: true},
		{name: "run write failure rolls back slot", rollback: true},
		{name: "two claimants one run", race: true, want: true},
		{name: "spacing cannot be shortened", spacing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.now = fixture.now.Truncate(time.Minute)
			if !test.start.IsZero() {
				fixture.now = test.start
			}
			request := workflowRunRequest(t)
			request.Run.Event = ActionsEventSchedule
			if !test.missing {
				authorityRef := test.authorityRef
				if authorityRef == "" {
					authorityRef = "refs/heads/main"
				}
				noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []AcceptedActionsPush{{Ref: authorityRef, NewOID: request.Run.SourceOID}}, fixture.now))
				if test.consumed {
					pushes, err := fixture.store.PendingAcceptedActionsPushes(ctx, "project", 1)
					noErr(t, err)
					noErr(t, fixture.store.ConsumeAcceptedActionsPush(ctx, pushes[0].Sequence))
				}
			}
			policy, _, err := fixture.store.CheckPolicy(ctx, "project")
			noErr(t, err)
			expected := ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
			cronText := test.cron
			if cronText == "" {
				cronText = "* * * * *"
			}
			notes, err := fixture.store.RebuildActionsSchedules(ctx, "project", "refs/heads/main", request.Run.SourceOID, expected, []ActionsSchedule{{WorkflowPath: request.Run.WorkflowPath, Cron: cronText}}, fixture.now)
			noErr(t, err)
			if len(notes) != 0 {
				t.Fatalf("rebuild notes=%+v", notes)
			}
			rows, err := fixture.store.ActionsSchedules(ctx, "project", "refs/heads/main")
			noErr(t, err)
			parsed, err := actions.ParseCron(cronText)
			noErr(t, err)
			initialDue, err := parsed.Next(fixture.now)
			noErr(t, err)
			if len(rows) != 1 || !rows[0].NextDueAt.Equal(initialDue) {
				t.Fatalf("rows=%+v", rows)
			}
			entry := rows[0]
			now := entry.NextDueAt.Add(test.late)
			if test.status != "" || test.spacing {
				first, err := fixture.store.AdmitScheduledActionsRun(ctx, entry, "refs/heads/main", expected, request, now)
				noErr(t, err)
				jobs, err := fixture.store.ActionsRunJobs(ctx, "project", first.Runs[0].ID)
				noErr(t, err)
				status := test.status
				if test.spacing {
					status = CheckJobPassed
				}
				_, err = fixture.store.db.ExecContext(ctx, `UPDATE check_jobs SET status=? WHERE id=?`, status, jobs[0].ID)
				noErr(t, err)
				if test.followed != "" {
					later := workflowRunRequest(t)
					later.Run.Event = ActionsEventDispatch
					later.Run.EventKey = "dispatch/later"
					if test.followed == "other" {
						later.Run.WorkflowPath = ".github/workflows/other.yml"
					}
					_, _, err := fixture.store.AdmitActionsRun(ctx, later, now.Add(time.Second))
					noErr(t, err)
				}
				rows, err = fixture.store.ActionsSchedules(ctx, "project", "refs/heads/main")
				noErr(t, err)
				entry = rows[0]
				now = entry.NextDueAt
				if test.spacing {
					now = now.Add(-4 * time.Minute)
					_, err = fixture.store.db.ExecContext(ctx, `UPDATE actions_schedules SET next_due_at=?`, now.Unix())
					noErr(t, err)
					entry.NextDueAt = now
				}
			}
			if test.outcome != "" {
				for i := 0; i < policy.QueueLimit; i++ {
					job := pushJobRequest()
					job.EventKey = fmt.Sprintf("queue/%d", i)
					_, _, err := fixture.store.AdmitCheckJob(ctx, job, fixture.now)
					noErr(t, err)
				}
			}
			if test.stale {
				_, err := fixture.store.db.ExecContext(ctx, `UPDATE actions_schedules SET next_due_at=next_due_at+60`)
				noErr(t, err)
			}
			if test.rollback {
				_, err := fixture.store.db.ExecContext(ctx, `CREATE TRIGGER schedule_fail BEFORE INSERT ON actions_runs BEGIN SELECT RAISE(ABORT,'synthetic schedule failure'); END`)
				noErr(t, err)
			}
			before, err := readActionsSchedules(ctx, fixture.store.db, "project")
			noErr(t, err)
			var result CheckEventAdmission
			if test.race {
				var wg sync.WaitGroup
				results := make(chan CheckEventAdmission, 2)
				failures := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Go(func() {
						r, e := fixture.store.AdmitScheduledActionsRun(ctx, entry, "refs/heads/main", expected, request, now)
						results <- r
						failures <- e
					})
				}
				wg.Wait()
				close(results)
				close(failures)
				for e := range failures {
					noErr(t, e)
				}
				for r := range results {
					if r.Admitted {
						if result.Admitted {
							t.Fatal("two claims")
						}
						result = r
					}
				}
			} else {
				result, err = fixture.store.AdmitScheduledActionsRun(ctx, entry, "refs/heads/main", expected, request, now)
			}
			if test.rollback {
				if err == nil || !strings.Contains(err.Error(), "synthetic schedule failure") {
					t.Fatalf("error=%v", err)
				}
			} else {
				noErr(t, err)
			}
			if result.Admitted != test.want {
				t.Fatalf("result=%+v want admitted=%v", result, test.want)
			}
			after, err := fixture.store.ActionsSchedules(ctx, "project", "refs/heads/main")
			noErr(t, err)
			if !test.want {
				if !after[0].NextDueAt.Equal(before[0].NextDueAt) || after[0].LastRunID != before[0].LastRunID {
					t.Fatalf("failed claim consumed slot: %+v", after)
				}
				return
			}
			run := result.Runs[0]
			if test.horizon {
				found := false
				for _, note := range run.Facts.Notes {
					found = found || note.Code == "workflow.cron_never"
				}
				stored, exists, err := fixture.store.ActionsRun(ctx, "project", run.ID)
				noErr(t, err)
				if len(after) != 0 || !found || !exists || stored.ScheduledFor == nil || !stored.ScheduledFor.Equal(entry.NextDueAt) {
					t.Fatalf("horizon run=%+v stored=%+v rows=%+v", run, stored, after)
				}
				replay, err := fixture.store.AdmitScheduledActionsRun(ctx, entry, "refs/heads/main", expected, request, now)
				if err != nil || replay.Admitted {
					t.Fatalf("replay=%+v err=%v", replay, err)
				}
				later := time.Date(2100, 3, 1, 0, 0, 0, 0, time.UTC)
				notes, err := fixture.store.RebuildActionsSchedules(ctx, "project", "refs/heads/main", entry.SourceOID, expected, []ActionsSchedule{entry}, later)
				noErr(t, err)
				resumed, err := fixture.store.ActionsSchedules(ctx, "project", "refs/heads/main")
				noErr(t, err)
				if len(notes) != 0 || len(resumed) != 1 || !resumed[0].NextDueAt.Equal(time.Date(2104, 2, 29, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("future rebuild notes=%+v rows=%+v", notes, resumed)
				}
				return
			}
			expectedNext, err := parsed.Next(now.Add(ActionsScheduleSpacing).Add(-time.Nanosecond))
			noErr(t, err)
			if test.cron == "*/5 * * * *" && expectedNext.Minute()%5 != 0 {
				t.Fatal("catch-up lost cron alignment")
			}
			if run.Outcome != test.outcome || run.ScheduledFor == nil || run.ScheduledFor.After(now) || run.ScheduledFor.Before(entry.NextDueAt) || run.EventKey != ActionsScheduleEventKey(entry, *run.ScheduledFor) || !after[0].NextDueAt.Equal(expectedNext) {
				t.Fatalf("run=%+v schedule=%+v now=%s", run, after[0], now)
			}
			hasMissed := false
			for _, note := range run.Facts.Notes {
				if note.Code == "note.missed" {
					hasMissed = true
				}
			}
			if hasMissed != run.ScheduledFor.After(entry.NextDueAt) {
				t.Fatalf("notes=%+v", run.Facts.Notes)
			}
			replay, err := fixture.store.AdmitScheduledActionsRun(ctx, entry, "refs/heads/main", expected, request, now)
			if err != nil || replay.Admitted {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
		})
	}
}

func TestRebuildActionsSchedules(t *testing.T) {
	for _, name := range []string{"reorder and duplicates", "changed source", "removed cron", "repository limit", "consent reenabled", "policy reenabled", "restore", "wrong branch authority", "normal leap rebuild", "century leap rebuild", "new leap beyond horizon"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.now = fixture.now.Truncate(time.Minute)
			oid := strings.Repeat("a", 40)
			entries := []ActionsSchedule{{WorkflowPath: ".github/workflows/ci.yml", Cron: "*/5 * * * *"}, {WorkflowPath: ".github/workflows/ci.yml", Cron: "0 * * * *"}}
			leap := strings.Contains(name, "leap")
			if leap {
				fixture.now = time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC)
				if name != "normal leap rebuild" {
					fixture.now = time.Date(2095, 3, 1, 0, 0, 0, 0, time.UTC)
				}
				entries = []ActionsSchedule{{WorkflowPath: ".github/workflows/ci.yml", Cron: "0 0 29 FEB *"}}
			}
			ref := "refs/heads/main"
			acceptedRef := ref
			if name == "wrong branch authority" {
				acceptedRef = "refs/heads/feature"
			}
			noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []AcceptedActionsPush{{Ref: acceptedRef, NewOID: oid}}, fixture.now))
			policy, _, err := fixture.store.CheckPolicy(ctx, "project")
			noErr(t, err)
			expected := ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
			_, err = fixture.store.RebuildActionsSchedules(ctx, "project", ref, oid, expected, entries, fixture.now)
			noErr(t, err)
			before, err := fixture.store.ActionsSchedules(ctx, "project", ref)
			noErr(t, err)
			later := fixture.now.Add(2 * time.Hour)
			want := 2
			if leap {
				later = fixture.now.AddDate(2, 0, 0)
				oid = strings.Repeat("c", 40)
				noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []AcceptedActionsPush{{Ref: ref, NewOID: oid}}, later))
				want = 1
				if name == "new leap beyond horizon" {
					entries[0].WorkflowPath = ".github/workflows/new.yml"
					want = 0
				}
			}
			reset := false
			switch name {
			case "reorder and duplicates":
				entries = []ActionsSchedule{entries[1], entries[0], entries[1]}
			case "changed source":
				oid = strings.Repeat("c", 40)
			case "removed cron":
				entries = entries[:1]
				want = 1
			case "repository limit":
				entries = nil
				for i := 0; i < 35; i++ {
					entries = append(entries, ActionsSchedule{WorkflowPath: fmt.Sprintf(".github/workflows/%d.yml", i), Cron: "0 0 * * *"})
				}
				want = 32
			case "consent reenabled":
				_, err = fixture.store.RevokeCheckConsent(ctx, "project", fixture.now)
				noErr(t, err)
				_, err = fixture.store.GrantCheckConsent(ctx, "project", later)
				noErr(t, err)
				reset = true
			case "policy reenabled":
				fixture.setPolicy(t, func(input *CheckPolicyInput) { input.AllowedEvents = []string{"push"} })
				fixture.setPolicy(t, func(input *CheckPolicyInput) { input.AllowedEvents = []string{"push", ActionsEventSchedule} })
				fixture.grantConsent(t)
				reset = true
			case "restore":
				snapshot, err := fixture.store.RecoverySnapshot(ctx)
				noErr(t, err)
				fixture.store = openTestStore(t)
				noErr(t, fixture.store.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
				rows, err := fixture.store.ActionsSchedules(ctx, "project", ref)
				noErr(t, err)
				if len(rows) != 0 {
					t.Fatalf("restored schedules=%+v", rows)
				}
				policy, _, err = fixture.store.CheckPolicy(ctx, "project")
				noErr(t, err)
				if policy.ConsentActive {
					t.Fatal("restore restored consent")
				}
				_, err = fixture.store.GrantCheckConsent(ctx, "project", later)
				noErr(t, err)
				reset = true
			}
			policy, _, err = fixture.store.CheckPolicy(ctx, "project")
			noErr(t, err)
			expected = ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
			notes, err := fixture.store.RebuildActionsSchedules(ctx, "project", ref, oid, expected, entries, later)
			noErr(t, err)
			after, err := fixture.store.ActionsSchedules(ctx, "project", ref)
			noErr(t, err)
			if len(after) != want || (len(notes) > 0) != (name == "repository limit" || name == "new leap beyond horizon") {
				t.Fatalf("after=%+v notes=%+v", after, notes)
			}
			for _, entry := range after {
				if entry.SourceOID != oid {
					t.Fatal("old source")
				}
				if reset {
					if !entry.NextDueAt.After(later) {
						t.Fatal("caught up disabled interval")
					}
				} else if want < 3 {
					found := false
					for _, old := range before {
						if old.Cron == entry.Cron && old.NextDueAt.Equal(entry.NextDueAt) {
							found = true
						}
					}
					if !found {
						t.Fatal("unchanged entry lost time")
					}
				}
			}
			if name == "wrong branch authority" || name == "restore" || name == "changed source" {
				found := false
				for _, note := range after[0].Notes {
					found = found || note.Code == "note.push_required"
				}
				if !found {
					t.Fatal("missing push-required note")
				}
			}
			_, err = fixture.store.RebuildActionsSchedules(ctx, "project", ref, oid, ExpectedCheckPolicy{}, entries, later)
			if !errors.Is(err, ErrCheckPolicyStale) {
				t.Fatalf("stale policy=%v", err)
			}
		})
	}
}

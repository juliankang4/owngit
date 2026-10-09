package checkrun

import (
	"context"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/state"
)

func TestCoordinatorSchedules(t *testing.T) {
	for _, name := range []string{"accepted default head", "consumed receive authority", "imported head is visible but cannot run", "head moves during planning", "new source keeps due time", "unfinished run pauses", "reenabling starts in future", "default branch changes", "empty default head removes rows", "200 byte default branch", "201 byte default branch"} {
		t.Run(name, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			text := "on:\n  schedule:\n    - cron: '* * * * *'\n  workflow_dispatch:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo scheduled\n"
			oid := pushActionsFiles(fixture, map[string]string{".github/workflows/clock.yml": text}, name != "imported head is visible but cannot run")
			now := time.Now().UTC().Truncate(time.Minute)
			coordinator := fixture.coordinator
			ref := "refs/heads/main"
			if name == "200 byte default branch" || name == "201 byte default branch" {
				size := 200
				if name == "201 byte default branch" {
					size++
				}
				branch := strings.Repeat("b", size)
				ref = "refs/heads/" + branch
				fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:"+ref)
				fixture.noteOwnGitWrite()
				noErr(t, coordinator.Repositories.SetDefaultBranch(fixture.ctx, fixture.repositoryID, branch))
				noErr(t, fixture.store.RecordAcceptedActionsPushes(fixture.ctx, fixture.repositoryID, []state.AcceptedActionsPush{{Ref: ref, NewOID: oid}}, now))
			}
			coordinator.admitSchedules(fixture.ctx, now)
			rows, err := fixture.store.ActionsSchedules(fixture.ctx, fixture.repositoryID, ref)
			noErr(t, err)
			if len(rows) != 1 || !rows[0].NextDueAt.Equal(now.Add(time.Minute)) {
				t.Fatalf("rows=%+v", rows)
			}
			original := rows[0]
			if name == "empty default head removes rows" {
				fixture.git("-C", fixture.repoPath, "update-ref", "-d", "refs/heads/main")
				fixture.noteOwnGitWrite()
				coordinator.admitSchedules(fixture.ctx, now.Add(time.Minute))
				rows, err = fixture.store.ActionsSchedules(fixture.ctx, fixture.repositoryID, "refs/heads/main")
				noErr(t, err)
				if len(rows) != 0 {
					t.Fatalf("stale schedules=%+v", rows)
				}
				return
			}
			if name == "consumed receive authority" {
				_, err := coordinator.admitAcceptedPushes(fixture.ctx, fixture.repositoryID)
				noErr(t, err)
			}
			if name == "head moves during planning" {
				policy, _, err := fixture.store.CheckPolicy(fixture.ctx, fixture.repositoryID)
				noErr(t, err)
				event := EventRequest{RepositoryID: fixture.repositoryID, Event: state.ActionsEventSchedule, EventKey: state.ActionsScheduleEventKey(original, original.NextDueAt), SourceOID: oid, TriggerRef: "main", WorkflowPath: original.WorkflowPath, ScheduledFor: &original.NextDueAt, schedule: &original, admissionTime: original.NextDueAt}
				pinned, err := fixture.coordinator.Repositories.PinRepository(fixture.ctx, fixture.repositoryID, oid, oid)
				noErr(t, err)
				planned, err := coordinator.planActionsEvent(fixture.ctx, pinned, policy, event)
				noErr(t, err)
				pushActionsFiles(fixture, map[string]string{"changed.txt": "new default head"})
				result, err := coordinator.admitPlannedSchedule(fixture.ctx, policy, event, planned)
				noErr(t, err)
				if result.Admitted || len(result.Runs) != 0 {
					t.Fatalf("moved head admitted: %+v", result)
				}
				rows, err = fixture.store.ActionsSchedules(fixture.ctx, fixture.repositoryID, "refs/heads/main")
				noErr(t, err)
				if !rows[0].NextDueAt.Equal(original.NextDueAt) || rows[0].LastRunID != "" {
					t.Fatal("moved head consumed slot")
				}
				return
			}
			if name == "new source keeps due time" {
				oid = pushActionsFiles(fixture, map[string]string{"changed.txt": "new scheduled source"})
			}
			if name == "default branch changes" {
				fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/other")
				fixture.noteOwnGitWrite()
				noErr(t, fixture.coordinator.Repositories.SetDefaultBranch(fixture.ctx, fixture.repositoryID, "other"))
			}
			if name == "reenabling starts in future" {
				_, err := fixture.store.RevokeCheckConsent(fixture.ctx, fixture.repositoryID, now)
				noErr(t, err)
				_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Hour))
				noErr(t, err)
			}
			fire := original.NextDueAt.Add(9 * time.Minute)
			due := coordinator.admitSchedules(fixture.ctx, fire)
			runs, err := fixture.store.ActionsRuns(fixture.ctx, fixture.repositoryID, 20)
			noErr(t, err)
			want := 1
			if name == "imported head is visible but cannot run" || name == "reenabling starts in future" || name == "default branch changes" {
				want = 0
			}
			if len(runs) != want {
				t.Fatalf("runs=%+v want=%d", runs, want)
			}
			if name == "default branch changes" {
				ref = "refs/heads/other"
			}
			rows, err = fixture.store.ActionsSchedules(fixture.ctx, fixture.repositoryID, ref)
			noErr(t, err)
			if want == 0 {
				if name == "reenabling starts in future" {
					if !rows[0].NextDueAt.After(fire) {
						t.Fatal("caught up disabled times")
					}
				} else {
					found := false
					for _, note := range rows[0].Notes {
						found = found || note.Code == "note.push_required"
					}
					if !found {
						t.Fatal("missing authority note")
					}
				}
				return
			}
			if runs[0].SourceOID != oid || runs[0].ScheduledFor == nil || !runs[0].ScheduledFor.Equal(fire) || !rows[0].NextDueAt.Equal(fire.Add(5*time.Minute)) || !due.Equal(rows[0].NextDueAt) {
				t.Fatalf("runs=%+v rows=%+v", runs, rows)
			}
			if name == "201 byte default branch" {
				jobs, err := fixture.store.ActionsRunJobs(fixture.ctx, fixture.repositoryID, runs[0].ID)
				noErr(t, err)
				if runs[0].Outcome != actions.StatusRefused || !strings.HasPrefix(runs[0].TriggerRef, state.ActionsRefusedRefPrefix) || len(jobs) != 0 || rows[0].LastRunID != runs[0].ID {
					t.Fatalf("long branch refusal=%+v jobs=%+v row=%+v", runs[0], jobs, rows[0])
				}
			}
			coordinator.admitSchedules(fixture.ctx, fire.Add(time.Minute))
			if name == "unfinished run pauses" {
				coordinator.admitSchedules(fixture.ctx, fire.Add(10*time.Minute))
			}
			again, err := fixture.store.ActionsRuns(fixture.ctx, fixture.repositoryID, 20)
			noErr(t, err)
			if len(again) != 1 {
				t.Fatalf("spacing or unfinished pause failed: %+v", again)
			}
		})
	}
}

func TestScheduleAdmissionTimer(t *testing.T) {
	fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
	pushActionsFiles(fixture, map[string]string{".github/workflows/clock.yml": "on:\n  schedule:\n    - cron: '* * * * *'\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo clock\n"})
	coordinator := fixture.coordinator
	now := time.Now().UTC()
	coordinator.admitSchedules(fixture.ctx, now)
	err := fixture.store.Exec(fixture.ctx, `UPDATE actions_schedules SET next_due_at=?`, now.Add(-time.Minute).Truncate(time.Minute).Unix())
	noErr(t, err)
	coordinator.Interval = 10 * time.Millisecond
	coordinator.wake = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go coordinator.admissionLoop(ctx, make(chan struct{}), done)
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-coordinator.wake:
	case <-time.After(10 * time.Second):
		t.Fatal("admission loop did not fire independently of a local worker")
	}
	runs, err := fixture.store.ActionsRuns(fixture.ctx, fixture.repositoryID, 10)
	noErr(t, err)
	if len(runs) != 1 || runs[0].Event != state.ActionsEventSchedule {
		t.Fatalf("runs=%+v", runs)
	}
	jobs, err := fixture.store.ActionsRunJobs(fixture.ctx, fixture.repositoryID, runs[0].ID)
	noErr(t, err)
	if len(jobs) != 1 || jobs[0].Status != state.CheckJobPending || !strings.HasPrefix(runs[0].EventKey, "schedule/") {
		t.Fatalf("jobs=%+v", jobs)
	}
	pinned, err := fixture.coordinator.Repositories.PinRepository(fixture.ctx, fixture.repositoryID, runs[0].SourceOID, runs[0].SourceOID)
	noErr(t, err)
	files, _, err := ReadActionsWorkflows(fixture.ctx, pinned, 1<<20)
	noErr(t, err)
	if len(actions.ParseFiles(files)) != 1 || pinned.HeadOID() == "" {
		t.Fatal("missing workflow source")
	}
}

package checkrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"owngit/internal/actions"
	"owngit/internal/repository"
	"owngit/internal/state"
)

type scheduleHead struct {
	ref, oid                      string
	policyVersion, consentVersion int64
}

// admitSchedules runs only in the admission goroutine. A local job never holds
// its timer or its default-branch scans.
func (coordinator *Coordinator) admitSchedules(ctx context.Context, now time.Time) time.Time {
	ids, err := coordinator.Store.CheckPolicyRepositories(ctx)
	if err != nil {
		coordinator.log("workflow schedule repositories: %v", err)
		return time.Time{}
	}
	ceilings, err := coordinator.Store.CheckCeilings(ctx)
	if err != nil {
		coordinator.log("workflow schedule ceilings: %v", err)
		return time.Time{}
	}
	var earliest time.Time
	live := map[string]bool{}
	for _, id := range ids {
		live[id] = true
		policy, ready, err := coordinator.admissiblePushPolicy(ctx, id, ceilings)
		if err != nil {
			coordinator.log("workflow schedule policy %s: %v", id, err)
			continue
		}
		if !ready || !policy.RunWorkflows || !contains(policy.AllowedEvents, state.ActionsEventSchedule) {
			delete(coordinator.scheduleHeads, id)
			continue
		}
		due, err := coordinator.admitRepositorySchedules(ctx, policy, now)
		if err != nil {
			if ctx.Err() == nil {
				coordinator.log("workflow schedules %s: %v", id, err)
			}
			continue
		}
		if !due.IsZero() && (earliest.IsZero() || due.Before(earliest)) {
			earliest = due
		}
	}
	for id := range coordinator.scheduleHeads {
		if !live[id] {
			delete(coordinator.scheduleHeads, id)
		}
	}
	return earliest
}

func (coordinator *Coordinator) admitRepositorySchedules(ctx context.Context, policy state.CheckPolicy, now time.Time) (time.Time, error) {
	ref, oid, err := coordinator.Repositories.ResolveRef(ctx, policy.RepositoryID, "")
	if errors.Is(err, repository.ErrNotFound) {
		delete(coordinator.scheduleHeads, policy.RepositoryID)
		_, err = coordinator.Store.RebuildActionsSchedules(ctx, policy.RepositoryID, "", "", state.ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil, now)
		return time.Time{}, err
	}
	if err != nil {
		return time.Time{}, err
	}
	head := scheduleHead{ref, oid, policy.Version, policy.ConsentVersion}
	if previous, exists := coordinator.scheduleHeads[policy.RepositoryID]; !exists || previous != head {
		pinned, err := coordinator.Repositories.PinRepository(ctx, policy.RepositoryID, oid, oid)
		if err != nil {
			return time.Time{}, err
		}
		files, _, err := ReadActionsWorkflows(ctx, pinned, policy.Execution.Source.MetadataLimit)
		if err != nil {
			return time.Time{}, err
		}
		var entries []state.ActionsSchedule
		for _, file := range actions.ParseFiles(files) {
			if file.Refusal != nil || !state.ValidActionsWorkflowPath(file.Path) {
				continue
			}
			for _, cron := range file.Workflow.Events[state.ActionsEventSchedule].Schedules {
				entries = append(entries, state.ActionsSchedule{WorkflowPath: file.Path, Cron: cron})
			}
		}
		currentRef, currentOID, err := coordinator.Repositories.ResolveRef(ctx, policy.RepositoryID, "")
		if err != nil {
			return time.Time{}, err
		}
		if currentRef != ref || currentOID != oid {
			return time.Time{}, nil
		}
		notes, err := coordinator.Store.RebuildActionsSchedules(ctx, policy.RepositoryID, ref, oid, state.ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, entries, now)
		if err != nil {
			return time.Time{}, err
		}
		for _, note := range notes {
			coordinator.log("workflow schedule %s %s: %s", policy.RepositoryID, note.Code, note.Detail)
		}
		if coordinator.scheduleHeads == nil {
			coordinator.scheduleHeads = map[string]scheduleHead{}
		}
		coordinator.scheduleHeads[policy.RepositoryID] = head
	}
	schedules, err := coordinator.Store.ActionsSchedules(ctx, policy.RepositoryID, ref)
	if err != nil {
		return time.Time{}, err
	}
	for _, entry := range schedules {
		if len(entry.Notes) > 0 {
			paused := false
			for _, note := range entry.Notes {
				if note.Code == "note.schedule_paused" || note.Code == "note.push_required" {
					paused = true
				}
			}
			if paused {
				continue
			}
		}
		if !entry.NextDueAt.After(now) {
			result, err := coordinator.admitEvent(ctx, policy, EventRequest{RepositoryID: entry.RepositoryID, Event: state.ActionsEventSchedule, EventKey: state.ActionsScheduleEventKey(entry, entry.NextDueAt), SourceOID: entry.SourceOID, TriggerRef: strings.TrimPrefix(ref, "refs/heads/"), WorkflowPath: entry.WorkflowPath, ScheduledFor: &entry.NextDueAt, schedule: &entry, admissionTime: now})
			if err != nil {
				return time.Time{}, fmt.Errorf("admit %s: %w", entry.WorkflowPath, err)
			}
			if result.Admitted {
				coordinator.Wake(entry.RepositoryID)
			}
		}
	}
	schedules, err = coordinator.Store.ActionsSchedules(ctx, policy.RepositoryID, ref)
	if err != nil {
		return time.Time{}, err
	}
	var earliest time.Time
	for _, entry := range schedules {
		if entry.NextDueAt.After(now) && (earliest.IsZero() || entry.NextDueAt.Before(earliest)) {
			earliest = entry.NextDueAt
		}
	}
	return earliest, nil
}

func (coordinator *Coordinator) admitPlannedSchedule(ctx context.Context, policy state.CheckPolicy, event EventRequest, runs []state.ActionsRunRequest) (state.CheckEventAdmission, error) {
	// A moved head drops this admission without consuming its slot. No Git
	// lock is held across the schedule transaction.
	ref, oid, err := coordinator.Repositories.ResolveRef(ctx, event.RepositoryID, "")
	if err != nil {
		return state.CheckEventAdmission{}, err
	}
	if oid != event.SourceOID || ref != "refs/heads/"+event.TriggerRef {
		return state.CheckEventAdmission{}, nil
	}
	expected := state.ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
	if event.schedule != nil {
		if len(runs) != 1 {
			return state.CheckEventAdmission{}, nil
		}
		return coordinator.Store.AdmitScheduledActionsRun(ctx, *event.schedule, ref, expected, runs[0], event.admissionTime)
	}
	return coordinator.Store.AdmitCheckEvent(ctx, event.RepositoryID, expected, nil, runs, time.Now().UTC())
}

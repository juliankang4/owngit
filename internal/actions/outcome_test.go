package actions

import (
	"reflect"
	"slices"
	"testing"
)

func TestStepConclusion(t *testing.T) {
	for _, test := range []struct {
		name string
		step StepEvidence
		want string
	}{
		{"run passed", StepEvidence{Status: StatusPassed, Role: RoleRun}, ResultSuccess},
		{"run failed", StepEvidence{Status: StatusFailed, Role: RoleRun}, ResultFailure},
		{"run error", StepEvidence{Status: StatusError, Role: RoleRun}, ResultFailure},
		{"run incomplete", StepEvidence{Status: StatusIncomplete, Role: RoleRun}, ResultFailure},
		{"run unavailable", StepEvidence{Status: StatusUnavailable, Role: RoleRun}, ResultFailure},
		{"tolerated failure", StepEvidence{Status: StatusFailed, Role: RoleTolerated}, ResultSuccess},
		{"tolerated error", StepEvidence{Status: StatusError, Role: RoleTolerated}, ResultSuccess},
		{"tolerated timeout", StepEvidence{Status: StatusIncomplete, Role: RoleTolerated}, ResultSuccess},
		{"tolerated unavailable", StepEvidence{Status: StatusUnavailable, Role: RoleTolerated}, ResultSuccess},
		{"cancel not tolerated", StepEvidence{Status: StatusCancelled, Role: RoleTolerated}, ResultCancelled},
		{"skipped", StepEvidence{Status: StatusSkipped, Role: RoleTolerated}, ResultSkipped},
		{"no-op", StepEvidence{Status: StatusNotRun, Role: RoleBuiltin}, ResultSkipped},
		{"checkout", StepEvidence{Status: StatusPassed, Role: RoleBuiltin}, ResultSuccess},
		{"cleanup not tolerated", StepEvidence{Status: StatusFailed, Role: RoleTolerated, CleanupError: "not released"}, ResultFailure},
		{"missing role", StepEvidence{Status: StatusPassed}, ResultFailure},
		{"unknown outcome", StepEvidence{Status: "unknown", Role: RoleTolerated}, ResultFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := StepConclusion(test.step); got != test.want {
				t.Fatalf("conclusion=%q, want %q", got, test.want)
			}
		})
	}
}

func TestAggregateAttemptStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		steps     []StepEvidence
		cancelled bool
		want      string
	}{
		{"empty", nil, false, StatusSkipped},
		{"empty cancelled", nil, true, StatusCancelled},
		{"all skipped", []StepEvidence{{Status: StatusSkipped, Role: RoleRun}, {Status: StatusSkipped, Role: RoleTolerated}}, false, StatusSkipped},
		{"no-op only", []StepEvidence{{Status: StatusPassed, Role: RoleBuiltin}, {Status: StatusNotRun, Role: RoleBuiltin}}, false, StatusSkipped},
		{"not run", []StepEvidence{{Status: StatusNotRun, Role: RoleRun}}, false, StatusSkipped},
		{"passed", []StepEvidence{{Status: StatusPassed, Role: RoleRun}, {Status: StatusNotRun, Role: RoleBuiltin}}, false, StatusPassed},
		{"failed", []StepEvidence{{Status: StatusPassed, Role: RoleRun}, {Status: StatusFailed, Role: RoleRun}}, false, StatusFailed},
		{"error", []StepEvidence{{Status: StatusFailed, Role: RoleRun}, {Status: StatusError, Role: RoleRun}}, false, StatusError},
		{"unavailable outranks incomplete", []StepEvidence{{Status: StatusUnavailable, Role: RoleRun}, {Status: StatusIncomplete, Role: RoleRun}}, false, StatusUnavailable},
		{"failed outranks unavailable", []StepEvidence{{Status: StatusUnavailable, Role: RoleRun}, {Status: StatusFailed, Role: RoleRun}}, false, StatusFailed},
		{"incomplete", []StepEvidence{{Status: StatusPassed, Role: RoleRun}, {Status: StatusIncomplete, Role: RoleRun}}, false, StatusIncomplete},
		{"tolerated failed", []StepEvidence{{Status: StatusFailed, Role: RoleTolerated}}, false, StatusPassed},
		{"tolerated error", []StepEvidence{{Status: StatusError, Role: RoleTolerated}}, false, StatusPassed},
		{"tolerated incomplete", []StepEvidence{{Status: StatusIncomplete, Role: RoleTolerated}}, false, StatusPassed},
		{"tolerated unavailable", []StepEvidence{{Status: StatusUnavailable, Role: RoleTolerated}}, false, StatusPassed},
		{"tolerated skip", []StepEvidence{{Status: StatusSkipped, Role: RoleTolerated}}, false, StatusSkipped},
		{"failure beside tolerated", []StepEvidence{{Status: StatusFailed, Role: RoleTolerated}, {Status: StatusFailed, Role: RoleRun}}, false, StatusFailed},
		{"cancelled after failure", []StepEvidence{{Status: StatusFailed, Role: RoleRun}}, true, StatusCancelled},
		{"error outranks cancel", []StepEvidence{{Status: StatusError, Role: RoleRun}, {Status: StatusCancelled, Role: RoleRun}}, true, StatusError},
		{"error outranks cancel without flag", []StepEvidence{{Status: StatusError, Role: RoleRun}, {Status: StatusCancelled, Role: RoleRun}}, false, StatusError},
		{"cancel result", []StepEvidence{{Status: StatusCancelled, Role: RoleTolerated}}, false, StatusCancelled},
		{"cleanup even in no-op", []StepEvidence{{Status: StatusNotRun, Role: RoleBuiltin, CleanupError: "not released"}}, true, StatusError},
		{"cleanup not tolerated", []StepEvidence{{Status: StatusPassed, Role: RoleTolerated, CleanupError: "not released"}}, false, StatusError},
		{"missing role", []StepEvidence{{Status: StatusPassed}}, false, StatusError},
		{"unknown role", []StepEvidence{{Status: StatusPassed, Role: "unknown"}}, false, StatusError},
		{"unknown status not tolerated", []StepEvidence{{Status: "unknown", Role: RoleTolerated}}, false, StatusError},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				steps := slices.Clone(test.steps)
				if reverse {
					slices.Reverse(steps)
				}
				before := slices.Clone(steps)
				if got := AggregateAttemptStatus(steps, test.cancelled); got != test.want {
					t.Fatalf("status=%q, want %q (reverse=%v)", got, test.want, reverse)
				}
				if !reflect.DeepEqual(before, steps) {
					t.Fatal("raw evidence changed")
				}
			}
		})
	}
}

func TestNeedsResult(t *testing.T) {
	for _, test := range []struct {
		status    string
		tolerated bool
		want      string
	}{
		{StatusPassed, false, ResultSuccess},
		{StatusFailed, false, ResultFailure},
		{StatusFailed, true, ResultSuccess},
		{StatusError, false, ResultFailure},
		{StatusError, true, ResultFailure},
		{StatusIncomplete, false, ResultFailure},
		{StatusIncomplete, true, ResultFailure},
		{StatusUnavailable, false, ResultFailure},
		{StatusUnavailable, true, ResultFailure},
		{StatusInterrupted, true, ResultFailure},
		{StatusAmbiguous, true, ResultFailure},
		{StatusRefused, true, ResultFailure},
		{StatusCancelled, true, ResultCancelled},
		{StatusSkipped, false, ResultSkipped},
		{"unknown", true, ResultFailure},
	} {
		t.Run(test.status+"/"+test.want, func(t *testing.T) {
			if got := NeedsResult(test.status, test.tolerated); got != test.want {
				t.Fatalf("result=%q, want %q (tolerated=%v)", got, test.want, test.tolerated)
			}
		})
	}
}

func TestRunConclusion(t *testing.T) {
	for _, test := range []struct {
		name    string
		jobs    []JobEvidence
		refused bool
		outcome string
		want    string
	}{
		{"empty", nil, false, "", StatusSkipped},
		{"all refused", nil, true, "", StatusRefused},
		{"file refused", nil, false, StatusRefused, StatusRefused},
		{"not admitted", nil, false, StatusNotRun, StatusNotRun},
		{"invalid outcome", nil, false, StatusPassed, StatusIncomplete},
		{"outcome with jobs", []JobEvidence{{Status: StatusPassed}}, false, StatusRefused, StatusIncomplete},
		{"all skipped", []JobEvidence{{Status: StatusSkipped}, {Status: StatusSkipped}}, false, "", StatusSkipped},
		{"passed", []JobEvidence{{Status: StatusPassed}, {Status: StatusSkipped}}, false, "", StatusPassed},
		{"failed beside later passing", []JobEvidence{{Status: StatusFailed}, {Status: StatusPassed}}, false, "", StatusFailed},
		{"refused beside passing", []JobEvidence{{Status: StatusPassed}}, true, "", StatusPartial},
		{"refused beside skipped", []JobEvidence{{Status: StatusSkipped}}, true, "", StatusPartial},
		{"refused beside failed", []JobEvidence{{Status: StatusFailed}}, true, "", StatusFailed},
		{"refused beside cancelled", []JobEvidence{{Status: StatusCancelled}}, true, "", StatusCancelled},
		{"tolerated failed", []JobEvidence{{Status: StatusFailed, Tolerated: true}}, false, "", StatusPassed},
		{"tolerated plus failure", []JobEvidence{{Status: StatusFailed, Tolerated: true}, {Status: StatusFailed}}, false, "", StatusFailed},
		{"error not tolerated", []JobEvidence{{Status: StatusError, Tolerated: true}}, true, "", StatusIncomplete},
		{"incomplete", []JobEvidence{{Status: StatusIncomplete}}, false, "", StatusIncomplete},
		{"unavailable", []JobEvidence{{Status: StatusUnavailable}}, false, "", StatusIncomplete},
		{"ambiguous not tolerated", []JobEvidence{{Status: StatusAmbiguous, Tolerated: true}}, false, "", StatusIncomplete},
		{"interrupted", []JobEvidence{{Status: StatusInterrupted}}, false, "", StatusIncomplete},
		{"unknown", []JobEvidence{{Status: "unknown"}}, false, "", StatusIncomplete},
		{"failed before incomplete", []JobEvidence{{Status: StatusError}, {Status: StatusFailed}}, false, "", StatusFailed},
		{"incomplete before cancelled", []JobEvidence{{Status: StatusError}, {Status: StatusCancelled}}, false, "", StatusIncomplete},
		{"pending before failed", []JobEvidence{{Status: StatusPending}, {Status: StatusFailed}}, false, "", StatusQueued},
		{"waiting", []JobEvidence{{Status: StatusWaiting}}, false, "", StatusQueued},
		{"claimed before queued", []JobEvidence{{Status: StatusClaimed}, {Status: StatusPending}}, false, "", StatusRunning},
		{"started before failed", []JobEvidence{{Status: StatusStarted}, {Status: StatusFailed}}, true, "", StatusRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := RunEvidence{Jobs: test.jobs, Outcome: test.outcome}
			if test.refused {
				run.Facts.RefusedJobs = []RefusedJob{{JobKey: "unsupported", Reason: Message{Code: "workflow.uses"}}}
			}
			before := slices.Clone(run.Jobs)
			if got := RunConclusion(run); got != test.want {
				t.Fatalf("conclusion=%q, want %q", got, test.want)
			}
			if !reflect.DeepEqual(before, run.Jobs) {
				t.Fatal("raw job evidence changed")
			}
		})
	}
}

func TestRevisionConclusion(t *testing.T) {
	for _, test := range []struct {
		name        string
		conclusions []string
		want        string
	}{
		{"no evidence", nil, ""},
		{"all refused", []string{StatusRefused}, StatusSkipped},
		{"all skipped", []string{StatusSkipped, StatusRefused}, StatusSkipped},
		{"unsupported file beside passing CI", []string{StatusRefused, StatusPassed}, StatusPassed},
		{"failed sibling beside passing", []string{StatusPassed, StatusFailed}, StatusFailed},
		{"partial beside passing", []string{StatusPartial, StatusPassed}, StatusPartial},
		{"failure before unfinished", []string{StatusRunning, StatusFailed}, StatusFailed},
		{"incomplete before cancelled", []string{StatusCancelled, StatusIncomplete}, StatusIncomplete},
		{"cancelled before running", []string{StatusRunning, StatusCancelled}, StatusCancelled},
		{"running before queued", []string{StatusQueued, StatusRunning}, StatusRunning},
		{"queued before not run", []string{StatusNotRun, StatusQueued}, StatusQueued},
		{"not run before partial", []string{StatusPartial, StatusNotRun}, StatusNotRun},
		{"passing before skipped", []string{StatusSkipped, StatusPassed}, StatusPassed},
		{"unknown never passes", []string{StatusPassed, "unknown"}, StatusIncomplete},
		{"unknown does not hide failure", []string{"unknown", StatusFailed}, StatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := RevisionConclusion(test.conclusions); got != test.want {
				t.Fatalf("conclusion=%q, want %q", got, test.want)
			}
		})
	}
}

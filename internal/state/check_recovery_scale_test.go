package state

import (
	"fmt"
	"testing"
)

// replicatedCheckRecovery returns a valid portable check state with count
// completed attempts, all copies of one real completed attempt.
func replicatedCheckRecovery(t *testing.T, fixture recoveryInvariantFixture, count int) RecoveryState {
	t.Helper()
	source := fixture.snapshot
	var base CheckAttempt
	for _, attempt := range source.CheckAttempts {
		if attempt.RepositoryID == "secondary" {
			base = attempt
		}
	}
	if base.ID == "" || base.Status == AttemptPending {
		t.Fatal("fixture has no completed secondary attempt")
	}
	var baseResults []CheckResultRecord
	for _, result := range source.CheckResults {
		if result.AttemptID == base.ID {
			baseResults = append(baseResults, result)
		}
	}
	snapshot := RecoveryState{}
	for _, repository := range source.Repositories {
		if repository.ID == base.RepositoryID {
			repository.AttemptSequence = int64(count)
			snapshot.Repositories = append(snapshot.Repositories, repository)
		}
	}
	for _, task := range source.Tasks {
		if task.ID == base.TaskID {
			snapshot.Tasks = append(snapshot.Tasks, task)
		}
	}
	var configuration CheckConfiguration
	for _, candidate := range source.CheckConfigurations {
		if candidate.RepositoryID == base.RepositoryID && candidate.Version == base.ConfigurationVersion {
			configuration = candidate
			snapshot.CheckConfigurations = append(snapshot.CheckConfigurations, candidate)
		}
	}
	for index := 1; index <= count; index++ {
		attempt := base
		attempt.ID = fmt.Sprintf("%032x", index)
		attempt.Sequence = int64(index)
		if attempt.LogID != "" {
			attempt.LogID = attempt.ID
		}
		registration := attempt
		registration.Checks = configuration.Checks
		attempt.RegistrationDigest = registrationDigest(registration)
		results := make([]CheckResult, 0, len(baseResults))
		for _, result := range baseResults {
			result.AttemptID = attempt.ID
			snapshot.CheckResults = append(snapshot.CheckResults, result)
			results = append(results, result.CheckResult)
		}
		attempt.CompletionDigest = completionDigest(attempt, results)
		snapshot.CheckAttempts = append(snapshot.CheckAttempts, attempt)
	}
	if err := ValidateCheckRecovery(snapshot); err != nil {
		t.Fatalf("replicated recovery state is invalid: %v", err)
	}
	return snapshot
}

// Backup and restore validate every check attempt while the snapshot holds the
// store's only connection, so validation must stay linear in the history. It
// reads each result record exactly once; the former per-attempt scan of all
// results read attempts x results records.
func TestCheckRecoveryValidationReadsEachResultOnce(t *testing.T) {
	fixture := newRecoveryInvariantFixture(t)
	for _, attempts := range []int{200, 50, 1} {
		snapshot := replicatedCheckRecovery(t, fixture, attempts)
		visits := 0
		checkRecoveryResultVisited = func() { visits++ }
		err := ValidateCheckRecovery(snapshot)
		checkRecoveryResultVisited = nil
		noErr(t, err)
		if visits != len(snapshot.CheckResults) {
			t.Fatalf("%d attempts with %d results: validation read %d result records", attempts, len(snapshot.CheckResults), visits)
		}
	}
}

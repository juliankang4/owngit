package checkrunner_test

import (
	"path/filepath"
	"testing"

	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestExternalRunnerEnvironment(t *testing.T) {
	environment := testfixture.NewCheckEnvironment(t)
	fixture := newRunnerIntegrationFixture(t, environment.Command)
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	runner := fixture.runner(fixture.client(origin))
	noErr(t, runner.Run(fixture.ctx))
	job := fixture.readJob()
	if job.Status != state.CheckJobFailed {
		t.Fatalf("runner job=%+v", job)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, job.AttemptID)
	noErr(t, err)
	if !exists || len(attempt.Results) != 1 || attempt.EffectiveWorktreeState() != state.WorktreeClean {
		t.Fatalf("runner attempt=%+v exists=%v", attempt, exists)
	}
	environment.Assert(t, attempt.Results[0].OutputExcerpt, filepath.Join(runner.WorkspaceRoot, job.ID))
}

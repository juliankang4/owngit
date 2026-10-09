package checkrun

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/testfixture/containerjob"
)

func TestPrepareActionsContainer(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Coordinator, *state.CheckJob, *string)
		want   string
	}{
		{name: "prepared before grant"},
		{name: "missing engine", want: "Docker", change: func(coordinator *Coordinator, _ *state.CheckJob, _ *string) {
			coordinator.DockerPath = "/owngit-no-such-docker"
		}},
		{name: "missing image", want: "cached image", change: func(_ *Coordinator, job *state.CheckJob, _ *string) {
			job.Execution.ContainerImage = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "host job refused", want: "leased container job", change: func(_ *Coordinator, job *state.CheckJob, _ *string) { job.Executor = state.CheckExecutorHost }},
		{name: "missing workspace", want: "workspace", change: func(_ *Coordinator, _ *state.CheckJob, workspace *string) {
			*workspace = filepath.Join(*workspace, "missing")
		}},
		{name: "revoked consent", want: "not in a state", change: func(coordinator *Coordinator, job *state.CheckJob, _ *string) {
			if _, err := coordinator.Store.RevokeCheckConsent(context.Background(), job.RepositoryID, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := containerjob.NewContainerJob(t)
			coordinator := &Coordinator{Store: fixture.Store, DockerPath: fixture.Docker}
			job, workspace := fixture.Job, fixture.Workspace
			if test.change != nil {
				test.change(coordinator, &job, &workspace)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			prepared, err := coordinator.PrepareActionsContainer(ctx, job, workspace)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("prepared=%v err=%v, want %q", prepared, err, test.want)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				timeout, limit := prepared.Limits()
				if prepared.Architecture != "X64" || prepared.Environment == nil || timeout != 60*time.Second || limit != 64<<10 {
					t.Fatalf("prepared=%+v timeout=%v limit=%v", prepared, timeout, limit)
				}
				stored, found, err := fixture.Store.CheckJob(ctx, job.RepositoryID, job.ID)
				if err != nil || !found || stored.Status != state.CheckJobClaimed {
					t.Fatalf("preflight granted execution: %+v err=%v", stored, err)
				}
			}
			owned, err := fixture.Store.ActiveCheckContainers(ctx, 100)
			if err != nil || len(owned) != 0 {
				t.Fatalf("preflight left ownership: %+v err=%v", owned, err)
			}
		})
	}
}

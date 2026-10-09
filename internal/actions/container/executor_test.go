package container

import (
	"context"
	"testing"
	"time"

	"owngit/internal/actions"
)

type nilExecutor struct{}

func (*nilExecutor) Metadata() ([]string, string, string) { panic("nil executor used") }
func (*nilExecutor) Limits() (time.Duration, int64)       { panic("nil executor used") }
func (*nilExecutor) RunScript(context.Context, actions.Script, ...string) actions.ScriptResult {
	panic("nil executor used")
}
func (*nilExecutor) CheckInterpreter(context.Context, actions.Script, ...string) actions.ScriptResult {
	panic("nil executor used")
}

func TestNilExecutor(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor Executor
	}{{"nil interface", nil}, {"typed nil", (*nilExecutor)(nil)}} {
		for _, entry := range []string{"RunJob", "Runner", "runPayload"} {
			t.Run(test.name+"/"+entry, func(t *testing.T) {
				executor := test.executor
				ctx := context.Background()
				var status, output string
				switch entry {
				case "RunJob":
					result := RunJob(ctx, actions.JobPlan{}, actions.RunOptions{}, executor)
					status, output = result.Status, result.Error
				case "Runner":
					result := Runner(executor)(ctx, actions.Script{})
					status, output = result.Status, result.Output
				case "runPayload":
					result := runPayload(ctx, executor, actions.Script{}, "", false)
					status, output = result.Status, result.Output
				}
				if status != actions.StatusError || output == "" {
					t.Fatalf("status=%s output=%q", status, output)
				}
			})
		}
	}
}

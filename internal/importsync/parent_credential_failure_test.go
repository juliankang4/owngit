package importsync

import (
	"context"
	"testing"

	"owngit/internal/state"
)

func TestParentCredentialDBFailureStopsAlreadyFetchingRun(t *testing.T) {
	for _, operation := range []string{"replace", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.commit("initial", "initial\n")
			f.mustImport(ImportInput{})
			if err := f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-initial-token"}); err != nil {
				t.Fatal(err)
			}
			before := f.destinationRefs()["refs/heads/main"]
			f.commit("new source tip", "changed\n")
			started, done, run, runErr := f.gatedRefresh(t)
			<-started
			released := false
			defer func() {
				if !released {
					f.transport.gate <- struct{}{}
					<-done
				}
			}()
			if err := f.store.Exec(ctx, `CREATE TRIGGER fail_authority_write BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority write failure'); END`); err != nil {
				t.Fatal(err)
			}
			var replacement *Credentials
			if operation == "replace" {
				replacement = &Credentials{BearerToken: "synthetic-replacement-token"}
			}
			if err := f.service.SetCredentials(ctx, "project", replacement); err == nil {
				t.Fatal("credential mutation unexpectedly succeeded")
			}
			f.transport.gate <- struct{}{}
			<-done
			released = true
			if *runErr == nil || run.Status == state.ImportRunComplete {
				t.Errorf("credential DB failure allowed stale run completion: status=%s err=%v", run.Status, *runErr)
			}
			if after := f.destinationRefs()["refs/heads/main"]; after != before {
				t.Errorf("credential DB failure allowed stale ref publication: before=%s after=%s", before, after)
			}
		})
	}
}

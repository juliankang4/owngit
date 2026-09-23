package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Rows of the removed built-in review can exist only in a database an
// unreleased development build wrote. The schema keeps their tables. The rows
// must not stop a store from opening, and a backup must refuse the portable
// ones instead of dropping them.
var directReviewResidueRows = map[string]string{
	"direct_review_credentials": `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at)
		VALUES('credential','project','label','provider-token',1,1)`,
	"direct_review_probes": `INSERT INTO direct_review_probes(request_id,repository_id,registration_digest,capability_fingerprint,configuration_version,
		connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,provider_limits_json,disclosure_version,disclosure_digest,phase,created_at)
		VALUES('probe','project','d','f',1,0,'f','openai_responses','https://provider.invalid','model','none','{}','v','d','running',1)`,
	"direct_review_repository_settings": `INSERT INTO direct_review_repository_settings(repository_id,configuration_version,protocol,endpoint,model,
		authentication_mode,provider_limits_json,repository_limits_json,instruction_version,authority_epoch,created_at,updated_at)
		VALUES('project',1,'openai_responses','https://provider.invalid','model','none','{}','{}','v','` + strings.Repeat("e", 32) + `',1,1)`,
	"direct_review_task_contexts": `INSERT INTO direct_review_task_contexts(context_id,repository_id,task_id,attempt_id,credential_id,base_oid,head_oid,registration_digest,created_at)
		VALUES('context','project','task','attempt','','base','head','d',1)`,
	"direct_review_requests": `INSERT INTO direct_review_requests(request_id,repository_id,trigger_kind,configuration_version,connection_version,connection_fingerprint,
		protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,instruction_version,disclosure_version,disclosure_digest,
		registration_digest,phase,created_at)
		VALUES('request','project','manual',1,0,'f','openai_responses','https://provider.invalid','model','none','{}','{}','v','v','d','d','running',1)`,
}

func insertDirectReviewResidue(t *testing.T, store *Store, table string) {
	t.Helper()
	ctx := context.Background()
	// Task contexts reference a task and an attempt; the residue needs none.
	for _, statement := range []string{`PRAGMA foreign_keys=OFF`, directReviewResidueRows[table], `PRAGMA foreign_keys=ON`} {
		if err := store.Exec(ctx, statement); err != nil {
			t.Fatalf("insert %s residue: %v", table, err)
		}
	}
}

func TestDirectReviewResidueIsInertAndPortableRowsBlockBackup(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "project", CreatedAt: time.Unix(1_800_000_000, 0)}))
	// Machine-local rows were never portable. They stay untouched, and the
	// store still opens and backs up.
	insertDirectReviewResidue(t, store, "direct_review_credentials")
	insertDirectReviewResidue(t, store, "direct_review_probes")
	noErr(t, store.Close())
	reopened, err := Open(ctx, store.Dir())
	if err != nil {
		t.Fatalf("open with direct-review residue: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.RecoverySnapshot(ctx)
	if err != nil || len(snapshot.Repositories) != 1 {
		t.Fatalf("snapshot with machine-local residue repositories=%d err=%v", len(snapshot.Repositories), err)
	}
	var phase string
	if err := reopened.db.QueryRowContext(ctx, `SELECT phase FROM direct_review_probes`).Scan(&phase); err != nil || phase != "running" {
		t.Fatalf("residue probe phase=%q err=%v", phase, err)
	}

	for _, table := range []string{"direct_review_repository_settings", "direct_review_task_contexts", "direct_review_requests"} {
		t.Run(table, func(t *testing.T) {
			insertDirectReviewResidue(t, reopened, table)
			_, err := reopened.RecoverySnapshot(ctx)
			if !errors.Is(err, ErrDirectReviewRecords) || err.Error() != "state contains direct-review records written by an unreleased development build; this build cannot back them up" {
				t.Fatalf("snapshot with %s residue err=%v", table, err)
			}
			if count, err := reopened.TableRowCount(ctx, table); err != nil || count != 1 {
				t.Fatalf("refused snapshot changed %s: rows=%d err=%v", table, count, err)
			}
			noErr(t, reopened.Exec(ctx, "DELETE FROM "+table))
		})
	}

	// Restore into a new store clears every residue row, and the result still
	// passes the schema check on the next open.
	target := openTestStore(t)
	for table := range directReviewResidueRows {
		insertDirectReviewResidue(t, target, table)
	}
	noErr(t, target.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	for table := range directReviewResidueRows {
		if count, err := target.TableRowCount(ctx, table); err != nil || count != 0 {
			t.Fatalf("restore kept %s residue: rows=%d err=%v", table, count, err)
		}
	}
	noErr(t, target.Close())
	restored, err := Open(ctx, target.Dir())
	if err != nil {
		t.Fatalf("open after restore: %v", err)
	}
	noErr(t, restored.Close())
}

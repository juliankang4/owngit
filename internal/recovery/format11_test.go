package recovery

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// newRecordsBackupStore is a backup store with a pull request, an import
// source, and one row of every schema 16 record: portable ones with values
// only format 11 holds, and machine-local ones that no backup carries.
func newRecordsBackupStore(t *testing.T, root string) (*state.Store, *repository.Manager) {
	t.Helper()
	ctx := context.Background()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	remote, err := manager.Path("project")
	noErr(t, err)
	runGit(t, work, "checkout", "-b", "feature")
	noErr(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "feature")
	runGit(t, work, "push", remote, "HEAD:refs/heads/feature")
	service := &pullrequest.Service{Store: store, Repositories: manager}
	_, err = service.Create(ctx, pullrequest.CreateInput{Repository: "project", Title: "Described", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	_, err = store.ConfigureImportSource(ctx, state.ImportSourceInput{RepositoryID: "project", URL: "https://example.invalid/source.git", Mode: state.ImportModeCoexistence, Now: time.Now()})
	noErr(t, err)
	for _, statement := range []string{
		`UPDATE pull_requests SET body='What and why',edit_revision=1,edited_at=created_at+60,created_by='{"kind":"access"}',
			edited_by='{"kind":"helper_credential","id":"0123456789abcdef0123456789abcdef","label":"agent"}'`,
		`UPDATE pull_request_reviews SET note='Checked both revisions',actor='{"kind":"administrator"}'`,
		`INSERT INTO repository_names(name,repository_id,kind,created_at,alias_until) VALUES('renamed','project','current',1800000000,NULL),('project','project','alias',1800000000,1807776000)`,
		`INSERT INTO repository_policies(repository_id,retain_history,protect_default_branch,extra_ref_prefixes,updated_at) VALUES('project',0,1,'["refs/notes/"]',1800000000)`,
		`UPDATE import_sources SET overwrite_diverged=1,extra_ref_prefixes='["refs/changes/"]',allow_private_network=1,allow_plain_http=1,redirect_policy='same_origin'`,
		`INSERT INTO share_links(id,repository_id,secret_hash,scope,created_at) VALUES('00000000000000000000000000000001','project',zeroblob(32),'clone',1800000000)`,
		`INSERT INTO backup_schedule(singleton,enabled,interval_seconds,destination,keep,updated_at) VALUES(1,1,86400,'/home/example/backups',7,1800000000)`,
		`INSERT INTO backup_runs(id,kind,status,destination,started_at) VALUES('00000000000000000000000000000002','manual','succeeded','/home/example/backups',1800000000)`,
		`INSERT INTO push_events(repository_id,ref_name,old_oid,new_oid,refs_updated,pushed_at) VALUES('project','refs/heads/main','','` + strings.Repeat("a", 40) + `',1,1800000000)`,
		`INSERT INTO metadata(key,value) VALUES('admin_confirmation','never')`,
	} {
		noErr(t, store.Exec(ctx, statement))
	}
	return store, manager
}

// A backup with records that only format 11 holds is written in format 11,
// restores them all, and brings back no machine-local item: no share link,
// schedule, recent push, connection choice or remembered confirmation.
func TestFormat11RecordsSurviveBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newRecordsBackupStore(t, root)
	before, err := store.RecoverySnapshot(ctx)
	noErr(t, err)

	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != backupVersion || format11Content(manifest) == "" {
		t.Fatalf("backup version=%d content=%q, want %d", manifest.Version, format11Content(manifest), backupVersion)
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	noErr(t, Restore(ctx, backup, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), ""))
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	after, err := restored.RecoverySnapshot(ctx)
	noErr(t, err)
	for name, pair := range map[string][2]any{
		"pull requests": {before.PullRequests, after.PullRequests},
		"reviews":       {before.PullRequestReviews, after.PullRequestReviews},
		"names":         {before.RepositoryNames, after.RepositoryNames},
		"policies":      {before.RepositoryPolicies, after.RepositoryPolicies},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Fatalf("%s changed through the backup:\n%+v\n%+v", name, pair[0], pair[1])
		}
	}
	if got := after.ImportSources[0]; !got.OverwriteDiverged || got.FollowUpstreamDeletions || !reflect.DeepEqual(got.ExtraRefPrefixes, []string{"refs/changes/"}) || got.AllowPrivateNetwork {
		t.Fatalf("restored import source=%+v", got)
	}
	for _, table := range []string{"share_links", "backup_schedule", "backup_runs", "push_events", "sessions", "helper_credentials"} {
		if count, err := restored.TableRowCount(ctx, table); err != nil || count != 0 {
			t.Fatalf("%s has %d rows after restore, err=%v", table, count, err)
		}
	}
	if count, err := restored.TableRowCount(ctx, "import_sources"); err != nil || count != 1 {
		t.Fatalf("import sources=%d err=%v", count, err)
	}
	for _, condition := range []string{
		`EXISTS(SELECT 1 FROM import_sources WHERE allow_private_network=0 AND allow_plain_http=0 AND redirect_policy='refuse')`,
		`NOT EXISTS(SELECT 1 FROM metadata WHERE key='admin_confirmation')`,
	} {
		if !holds(t, restored, condition) {
			t.Fatalf("machine-local state came back: %s is false", condition)
		}
	}

	// A version 10 manifest that holds any of these records is refused,
	// never restored without them.
	for name, edit := range map[string]func(*Manifest){
		"description": func(m *Manifest) {
			m.Repositories[0].Names, m.Repositories[0].Policy, m.PullRequestReviews = nil, nil, clearReviews(m.PullRequestReviews)
		},
		"name": func(m *Manifest) { m.PullRequests, m.Repositories[0].Policy = clearPullRequests(m.PullRequests), nil },
	} {
		older := manifest
		older.Repositories = append([]RepositoryManifest(nil), manifest.Repositories...)
		older.PullRequests = append([]PullRequestManifest(nil), manifest.PullRequests...)
		older.PullRequestReviews = append([]PullRequestReviewManifest(nil), manifest.PullRequestReviews...)
		older.ImportSources = nil
		older.Version = closedPullRequestBackupVersion
		edit(&older)
		if err := validateManifest(older); err == nil || !strings.Contains(err.Error(), "version 10 backup contains") {
			t.Fatalf("version 10 manifest with a %s: err=%v", name, err)
		}
	}
}

// holds evaluates an SQL condition in store, which has no accessor for the
// machine-local columns a restore resets.
func holds(t *testing.T, store *state.Store, condition string) bool {
	t.Helper()
	ctx := context.Background()
	noErr(t, store.Exec(ctx, "CREATE TEMP TABLE condition_probe AS SELECT 1 AS one WHERE "+condition))
	defer func() { noErr(t, store.Exec(ctx, "DROP TABLE condition_probe")) }()
	count, err := store.TableRowCount(ctx, "condition_probe")
	noErr(t, err)
	return count == 1
}

func clearPullRequests(records []PullRequestManifest) []PullRequestManifest {
	cleared := append([]PullRequestManifest(nil), records...)
	for index := range cleared {
		cleared[index].Body, cleared[index].EditRevision, cleared[index].EditedAt = "", 0, nil
		cleared[index].CreatedBy, cleared[index].EditedBy = state.Actor{}, state.Actor{}
	}
	return cleared
}

func clearReviews(records []PullRequestReviewManifest) []PullRequestReviewManifest {
	cleared := append([]PullRequestReviewManifest(nil), records...)
	for index := range cleared {
		cleared[index].Note, cleared[index].Actor = "", state.Actor{}
	}
	return cleared
}

// A backup whose records format 10 can hold stays format 10, which the
// release before this one restores, even when a repository has a policy
// row that keeps every default.
func TestBackupWithoutFormat11RecordsStaysFormat10(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	noErr(t, store.Exec(ctx, `INSERT INTO repository_policies(repository_id,updated_at) VALUES('project',1800000000)`))
	noErr(t, store.Exec(ctx, `INSERT INTO share_links(id,repository_id,secret_hash,scope,created_at) VALUES('00000000000000000000000000000001','project',zeroblob(32),'browse',1800000000)`))
	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != closedPullRequestBackupVersion {
		t.Fatalf("backup version=%d, want %d", manifest.Version, closedPullRequestBackupVersion)
	}
	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	noErr(t, Restore(ctx, backup, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), ""))
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	for _, table := range []string{"repository_policies", "share_links"} {
		if count, err := restored.TableRowCount(ctx, table); err != nil || count != 0 {
			t.Fatalf("%s has %d rows after restore, err=%v", table, count, err)
		}
	}
}

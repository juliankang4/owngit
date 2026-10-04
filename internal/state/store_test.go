package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapRedemptionIsAtomic(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.PutBootstrap(ctx, "owner-token", now.Add(time.Minute)))

	var redeemed atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 12; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			ok, err := store.RedeemBootstrap(ctx, "owner-token", fmt.Sprintf("session-%d", index), fmt.Sprintf("csrf-%d", index), now, now.Add(time.Minute))
			if err != nil {
				t.Errorf("redeem: %v", err)
				return
			}
			if ok {
				redeemed.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if got := redeemed.Load(); got != 1 {
		t.Fatalf("redeemed %d times, want exactly once", got)
	}
}

func TestBootstrapWriteAtomicallyRejectsCompletedSetup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	if err := store.PutBootstrap(ctx, "late-token", time.Now().Add(time.Minute)); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("PutBootstrap after setup error=%v, want ErrSetupComplete", err)
	}
	snapshot, err := store.BootstrapSnapshot(ctx)
	if err != nil || snapshot.Present {
		t.Fatalf("completed setup regained bootstrap capability: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestBootstrapExpiryAndReissueInvalidateOldCapability(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.PutBootstrap(ctx, "expired", now.Add(-time.Second)))
	if ok, err := store.RedeemBootstrap(ctx, "expired", "session", "csrf", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("expired capability redeemed=%v err=%v", ok, err)
	}
	noErr(t, store.PutBootstrap(ctx, "first", now.Add(time.Minute)))
	noErr(t, store.PutBootstrap(ctx, "replacement", now.Add(time.Minute)))
	if ok, err := store.RedeemBootstrap(ctx, "first", "old-session", "csrf", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("reissued old capability redeemed=%v err=%v", ok, err)
	}
	if ok, err := store.RedeemBootstrap(ctx, "replacement", "new-session", "csrf", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("replacement capability redeemed=%v err=%v", ok, err)
	}
}

func TestSetupAndCredentialModeTransitions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "other", true); err == nil {
		t.Fatal("second setup unexpectedly succeeded")
	}
	settings, err := store.Settings(ctx)
	noErr(t, err)
	if !settings.Initialized || settings.AccessMode != "open" || !settings.InsecureHTTPAccepted {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	noErr(t, store.CreateSession(ctx, "old-general", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	noErr(t, store.SetAccessPassword(ctx, "access-hash"))
	if _, ok, err := store.Session(ctx, "old-general", "general", time.Now()); err != nil || ok {
		t.Fatalf("old session survived mode change: ok=%v err=%v", ok, err)
	}
	settings, _ = store.Settings(ctx)
	if settings.AccessMode != "password" || settings.AccessSessionVersion != 2 {
		t.Fatalf("password mode not recorded: %+v", settings)
	}
	noErr(t, store.DisableAccessPassword(ctx))
	settings, _ = store.Settings(ctx)
	if settings.AccessMode != "open" || settings.AccessSessionVersion != 3 {
		t.Fatalf("open mode not restored: %+v", settings)
	}
}

// Two changes of the administrator password confirmed at the same version:
// the first saved wins, and the later one changes nothing.
func TestAdministratorPasswordChangeNeedsTheVersionItConfirmed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	settings, err := store.Settings(ctx)
	noErr(t, err)
	confirmed := settings.AdminSessionVersion

	noErr(t, store.ChangeAdminPassword(ctx, "owner-hash", confirmed))
	settings, err = store.Settings(ctx)
	noErr(t, err)
	owner := settings.AdminSessionVersion
	if owner == confirmed {
		t.Fatal("changing the administrator password kept its version")
	}
	noErr(t, store.CreateSession(ctx, "owner-admin", "admin", "csrf", owner, time.Now().Add(time.Hour)))

	if err := store.ChangeAdminPassword(ctx, "stale-hash", confirmed); !errors.Is(err, ErrAccessChanged) {
		t.Fatalf("change confirmed at a replaced version: %v", err)
	}
	encoded, err := store.PasswordHash(ctx, "admin")
	noErr(t, err)
	settings, err = store.Settings(ctx)
	noErr(t, err)
	if encoded != "owner-hash" || settings.AdminSessionVersion != owner {
		t.Fatalf("refused change altered the password: hash=%q version=%d, want owner-hash at %d", encoded, settings.AdminSessionVersion, owner)
	}
	if _, ok, err := store.Session(ctx, "owner-admin", "admin", time.Now()); err != nil || !ok {
		t.Fatalf("refused change ended the owner's session: ok=%v err=%v", ok, err)
	}

	// Local recovery replaces the password whatever version was confirmed.
	noErr(t, store.SetAdminPassword(ctx, "reset-hash"))
	encoded, err = store.PasswordHash(ctx, "admin")
	noErr(t, err)
	if encoded != "reset-hash" {
		t.Fatalf("recovery did not replace the password: %q", encoded)
	}
	if _, ok, err := store.Session(ctx, "owner-admin", "admin", time.Now()); err != nil || ok {
		t.Fatalf("recovery kept an administrator session: ok=%v err=%v", ok, err)
	}
}

func TestOnlyOneConcurrentSetupCompletionWins(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	var completed atomic.Int32
	var wait sync.WaitGroup
	base := t.TempDir()
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			root := filepath.Join(base, fmt.Sprintf("repositories-%d", index))
			if err := store.CompleteSetup(ctx, root, "open", "", fmt.Sprintf("admin-hash-%d", index), true); err == nil {
				completed.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if completed.Load() != 1 {
		t.Fatalf("setup completed %d times, want once", completed.Load())
	}
}

func TestSQLiteFileURIKeepsWindowsDriveInThePathAndEscapesReservedBytes(t *testing.T) {
	if got, want := sqliteFileURI("C:/OwnGit state/#?%.sqlite"), "file:///C:/OwnGit%20state/%23%3F%25.sqlite?_txlock=immediate&_pragma=trusted_schema(0)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"; got != want {
		t.Fatalf("Windows SQLite URI = %q, want %q", got, want)
	}
	if got, want := sqliteFileURI("/tmp/OwnGit state/#?%.sqlite"), "file:///tmp/OwnGit%20state/%23%3F%25.sqlite?_txlock=immediate&_pragma=trusted_schema(0)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"; got != want {
		t.Fatalf("Unix SQLite URI = %q, want %q", got, want)
	}
}

func TestStateOpenHandlesEscapedPathCharacters(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "OwnGit state # %")
	store, err := Open(context.Background(), directory)
	noErr(t, err)
	defer store.Close()
	if _, err := store.Settings(context.Background()); err != nil {
		t.Fatalf("read state from escaped path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, databaseName)); err != nil {
		t.Fatalf("state database was not created at the requested path: %v", err)
	}
}

func TestStateFilesAreOwnerOnly(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(context.Background(), directory)
	noErr(t, err)
	defer store.Close()
	if databaseName != "owngit.sqlite" {
		t.Fatalf("database name = %q, want owngit.sqlite", databaseName)
	}
	assertStateStoragePrivate(t, directory)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), copiedStateDirectory(t, filepath.Join(t.TempDir(), "state")))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// emptyStateSchema is the empty current-schema database that ordinary
// fixtures copy, generated once in this test binary. Tests of creation,
// migration, preflight, publication and permissions keep opening real new
// states (see copiedStateDirectory).
var emptyStateSchema struct {
	once  sync.Once
	files map[string][]byte
	err   error
}

// copiedStateDirectory creates directory as a private state directory that
// holds a copy of the current empty state database, for Open. Every fixture
// gets its own writable database: no database is shared, hard-linked or
// reused, and no released dump is used as the current schema. The packages
// whose fixtures open a store repeat this helper, because internal/testfixture
// imports no state and this package's tests import it.
func copiedStateDirectory(t *testing.T, directory string) string {
	t.Helper()
	emptyStateSchema.once.Do(func() { emptyStateSchema.files, emptyStateSchema.err = buildEmptyStateSchema() })
	noErr(t, emptyStateSchema.err)
	held, err := CreateDirectory(directory)
	noErr(t, err)
	noErr(t, held.Close())
	for name, content := range emptyStateSchema.files {
		file, err := CreatePrivateFile(filepath.Join(directory, name))
		noErr(t, err)
		_, err = file.Write(content)
		noErr(t, err)
		noErr(t, file.Close())
	}
	return directory
}

// buildEmptyStateSchema opens a new state, closes it and returns the files
// it left behind.
func buildEmptyStateSchema() (map[string][]byte, error) {
	directory, err := os.MkdirTemp("", "owngit-empty-state-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	store, err := Open(context.Background(), directory)
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = content
	}
	return files, nil
}

// An ordinary fixture copies the current empty database instead of building
// it again, so the copy must be indistinguishable from a state this build
// created, and two fixtures in one binary must stay independent.
func TestCopiedStateFixturesMatchAFreshStateAndStayIndependent(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer fresh.Close()
	copied, err := Open(ctx, copiedStateDirectory(t, filepath.Join(t.TempDir(), "state")))
	noErr(t, err)
	defer copied.Close()

	freshFingerprint, freshObjects, err := schemaFingerprint(ctx, fresh.db)
	noErr(t, err)
	copiedFingerprint, copiedObjects, err := schemaFingerprint(ctx, copied.db)
	noErr(t, err)
	if freshFingerprint != copiedFingerprint || freshObjects != copiedObjects {
		t.Fatalf("the copied state has catalog %s (%d objects), the fresh one %s (%d objects)",
			copiedFingerprint, copiedObjects, freshFingerprint, freshObjects)
	}
	freshMetadata, copiedMetadata := metadataRows(t, fresh), metadataRows(t, copied)
	if !reflect.DeepEqual(copiedMetadata, freshMetadata) {
		t.Fatalf("the copied state holds %v, the fresh one %v", copiedMetadata, freshMetadata)
	}
	freshSettings, err := fresh.Settings(ctx)
	noErr(t, err)
	copiedSettings, err := copied.Settings(ctx)
	noErr(t, err)
	if copiedSettings != freshSettings {
		t.Fatalf("the copied state defaults to %+v, a fresh one to %+v", copiedSettings, freshSettings)
	}
	assertStateStoragePrivate(t, fresh.dir)
	assertStateStoragePrivate(t, copied.dir)
	freshDirectory, err := os.Stat(fresh.dir)
	noErr(t, err)
	copiedDirectory, err := os.Stat(copied.dir)
	noErr(t, err)
	if copiedDirectory.Mode().Perm() != freshDirectory.Mode().Perm() {
		t.Fatalf("the copied directory mode is %v, a fresh one %v", copiedDirectory.Mode(), freshDirectory.Mode())
	}

	// Every fixture is its own database.
	other, err := Open(ctx, copiedStateDirectory(t, filepath.Join(t.TempDir(), "state")))
	noErr(t, err)
	defer other.Close()
	noErr(t, copied.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('fixture-check','one')`))
	var rows int
	noErr(t, other.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key='fixture-check'`).Scan(&rows))
	if rows != 0 {
		t.Fatal("a write in one copied fixture is visible in another")
	}
	noErr(t, copied.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key='fixture-check'`).Scan(&rows))
	if rows != 1 {
		t.Fatalf("the write in the copied fixture left %d rows", rows)
	}
}

// metadataRows returns the state's metadata keys and values.
func metadataRows(t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT key, value FROM metadata`)
	noErr(t, err)
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		noErr(t, rows.Scan(&key, &value))
		values[key] = value
	}
	noErr(t, rows.Err())
	return values
}

// noErr stops the test on an unexpected error. t.Helper keeps the failure
// line at the caller.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newProjectStore opens a store that holds repository "project" and returns
// the context and fixed time its setup used.
func newProjectStore(t *testing.T) (*Store, context.Context, time.Time) {
	t.Helper()
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	return store, ctx, now
}

// newProjectTask creates the task most check tests record attempts for.
func newProjectTask(t *testing.T, store *Store, ctx context.Context, now time.Time) Task {
	t.Helper()
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	noErr(t, err)
	return task
}

// The update check is on unless it was turned off, survives a restart, and a
// state written before the setting existed reads as on.
func TestUpdateCheckSettingDefaultsOnAndPersists(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, directory)
	noErr(t, err)
	settings, err := store.Settings(ctx)
	noErr(t, err)
	if !settings.UpdateCheck {
		t.Fatal("a state without the setting reads as off")
	}
	off, on := false, true
	noErr(t, store.SavePolicies(ctx, PolicyChange{UpdateCheck: &off}))
	noErr(t, store.Close())

	store, err = Open(ctx, directory)
	noErr(t, err)
	defer store.Close()
	if settings, err := store.Settings(ctx); err != nil || settings.UpdateCheck {
		t.Fatalf("after restart settings=%+v err=%v, want the check off", settings, err)
	}
	noErr(t, store.SavePolicies(ctx, PolicyChange{UpdateCheck: &on}))
	if settings, err := store.Settings(ctx); err != nil || !settings.UpdateCheck {
		t.Fatalf("settings=%+v err=%v, want the check on", settings, err)
	}

	// A value this build did not write is refused rather than guessed.
	_, err = store.db.ExecContext(ctx, `UPDATE metadata SET value='maybe' WHERE key=?`, updateCheckKey)
	noErr(t, err)
	if _, err := store.Settings(ctx); err == nil {
		t.Fatal("an unknown update check value was accepted")
	}
}

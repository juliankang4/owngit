package state

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var errInjectedStep = errors.New("injected sqlite step error")

// rowFault makes the next matching query fail after at most one row, the way
// a SQLite step error (I/O error, corruption, interrupt) ends a scan.
// database/sql then closes the rows itself and keeps the error only in Err.
type rowFault struct {
	mu    sync.Mutex
	match func(query string) bool
}

func (f *rowFault) set(match func(string) bool) {
	f.mu.Lock()
	f.match = match
	f.mu.Unlock()
}

func (f *rowFault) pending() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.match != nil
}

func (f *rowFault) take(query string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.match == nil || !f.match(query) {
		return false
	}
	f.match = nil
	return true
}

type faultConnector struct {
	dsn    string
	driver driver.Driver
	fault  *rowFault
}

func (c faultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return faultConn{Conn: conn, fault: c.fault}, nil
}

func (c faultConnector) Driver() driver.Driver { return c.driver }

type faultConn struct {
	driver.Conn
	fault *rowFault
}

func (c faultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !c.fault.take(query) {
		return rows, err
	}
	return &faultRows{Rows: rows}, nil
}

func (c faultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c faultConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (c faultConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c faultConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

type faultRows struct {
	driver.Rows
	delivered int
}

func (r *faultRows) Next(dest []driver.Value) error {
	if r.delivered >= 1 {
		return errInjectedStep
	}
	if err := r.Rows.Next(dest); err != nil {
		// An empty table fails on its first step instead of ending.
		return errInjectedStep
	}
	r.delivered++
	return nil
}

// openFaultStore opens a store whose single connection can inject a
// mid-iteration step error into one chosen query.
func openFaultStore(t *testing.T) (*Store, *rowFault) {
	t.Helper()
	store := openTestStore(t)
	completeTestSetup(t, store)
	ctx := context.Background()
	fault := &rowFault{}
	db := sql.OpenDB(faultConnector{dsn: sqliteFileURI(filepath.Join(store.dir, databaseName)), driver: store.db.Driver(), fault: fault})
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store.db = db
	return store, fault
}

// fullTableRead matches the unfiltered snapshot read of one table.
func fullTableRead(table string) func(string) bool {
	pattern := regexp.MustCompile(`(?s)\bFROM ` + table + `\b`)
	return func(query string) bool {
		return pattern.MatchString(query) && !strings.Contains(query, "WHERE")
	}
}

// A storage error in the middle of a backup read must fail the snapshot. It
// must never produce a snapshot that silently lacks the unread rows.
func TestRecoverySnapshotFailsOnIterationError(t *testing.T) {
	store, fault := openFaultStore(t)
	ctx := context.Background()
	// Two repositories make the repositories case fail after one delivered row.
	for _, id := range []string{"alpha", "beta"} {
		if err := store.AddRepository(ctx, Repository{ID: id, Name: id, CreatedAt: time.Unix(1_800_000_000, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot, err := store.RecoverySnapshot(ctx); err != nil || len(snapshot.Repositories) != 2 {
		t.Fatalf("baseline snapshot repositories=%d err=%v", len(snapshot.Repositories), err)
	}
	cases := map[string]func(string) bool{
		"metadata": func(query string) bool { return strings.Contains(query, "FROM metadata WHERE key IN") },
		// Reconciliation before the snapshot rewrites nonterminal requests and
		// probes; a failed read there must not look like "nothing to settle".
		"direct_review_requests reconcile": func(query string) bool {
			return strings.Contains(query, "FROM direct_review_requests WHERE phase!='terminal'")
		},
		"direct_review_probes reconcile": func(query string) bool {
			return strings.Contains(query, "FROM direct_review_probes WHERE phase!='terminal'")
		},
	}
	for _, table := range []string{
		"passwords", "repositories",
		"pull_requests", "pull_request_revisions", "pull_request_reviews", "pull_request_merge_intents",
		"tasks", "check_configurations", "check_cycles", "check_attempts", "check_results",
		"check_policies", "check_jobs",
		"direct_review_repository_settings", "direct_review_task_contexts", "direct_review_requests",
		"import_sources", "import_runs", "import_ref_observations", "import_publication_intents",
	} {
		cases[table] = fullTableRead(table)
	}
	for name, match := range cases {
		t.Run(name, func(t *testing.T) {
			fault.set(match)
			_, err := store.RecoverySnapshot(ctx)
			if fault.pending() {
				fault.set(nil)
				t.Fatal("the fault matched no query; the test no longer reaches this read")
			}
			if !errors.Is(err, errInjectedStep) {
				t.Fatalf("snapshot with a failing %s read returned err=%v", name, err)
			}
		})
	}
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("snapshot after the faults: %v", err)
	}
}

// The stale-job sweep must not treat a failed read as "no stale jobs".
func TestInterruptStalePendingCheckJobsFailsOnIterationError(t *testing.T) {
	store, fault := openFaultStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	fault.set(func(query string) bool {
		return strings.Contains(query, "FROM check_jobs WHERE repository_id=? AND status IN")
	})
	if _, err := interruptStalePendingCheckJobsTx(ctx, tx, CheckPolicy{RepositoryID: "project"}, time.Now()); !errors.Is(err, errInjectedStep) {
		t.Fatalf("stale job sweep err=%v", err)
	}
}

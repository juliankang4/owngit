package state

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// Nothing saved means each policy's default. A saved value that does not
// parse, is out of bounds or carries a field this build does not know is a
// PolicyError where the policy is read, never the default; saving the
// policy again replaces it.
func TestPoliciesReadTheirDefaultsAndRefuseWhatTheyCannotUse(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if session, err := store.GeneralSession(ctx); err != nil || session != Session12Hours {
		t.Fatalf("session=%q err=%v", session, err)
	}
	if branch, err := store.InitialBranch(ctx); err != nil || branch != "main" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
	if limits, err := store.GitTransferLimits(ctx); err != nil || limits != DefaultGitTransferLimits {
		t.Fatalf("limits=%+v err=%v", limits, err)
	}
	for _, stored := range []struct{ key, value string }{
		{"general_session_seconds", "5"},
		{"general_session_seconds", "9223372036854775807"},
		{"initial_branch", "has space"},
		{"git_transfer_limits", `{"maximum_bytes":0}`},
		{"git_transfer_limits", `{"operation_seconds":9223372036854775807}`},
		{"git_transfer_limits", `{"concurrent":8}`},
		{"git_transfer_limits", `{} {}`},
	} {
		noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stored.key, stored.value))
		var err error
		switch stored.key {
		case "general_session_seconds":
			_, err = store.GeneralSession(ctx)
		case "initial_branch":
			_, err = store.InitialBranch(ctx)
		default:
			_, err = store.GitTransferLimits(ctx)
		}
		var policyErr *PolicyError
		if !errors.As(err, &policyErr) || policyErr.Key != stored.key {
			t.Fatalf("%s=%s read with err=%v, want a PolicyError", stored.key, stored.value, err)
		}
	}
	session, branch := Session30Days, "trunk"
	limits := GitTransferLimits{MaximumBytes: 64 << 30, Operation: 24 * time.Hour}
	noErr(t, store.SavePolicies(ctx, PolicyChange{Session: &session, InitialBranch: &branch, GitTransfer: &limits}))
	if saved, err := store.GeneralSession(ctx); err != nil || saved != session {
		t.Fatalf("session=%q err=%v", saved, err)
	}
	if saved, err := store.InitialBranch(ctx); err != nil || saved != branch {
		t.Fatalf("branch=%q err=%v", saved, err)
	}
	if saved, err := store.GitTransferLimits(ctx); err != nil || saved != limits {
		t.Fatalf("limits=%+v err=%v", saved, err)
	}
}

// A change with one value out of bounds saves none of them.
func TestSavePoliciesSavesAllOrNothing(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	branch := "trunk"
	tooLong := GitTransferLimits{MaximumBytes: 4 << 30, Operation: 25 * time.Hour}
	if err := store.SavePolicies(ctx, PolicyChange{InitialBranch: &branch, GitTransfer: &tooLong}); err == nil {
		t.Fatal("a transfer longer than 24 hours was saved")
	}
	if saved, err := store.InitialBranch(ctx); err != nil || saved != "main" {
		t.Fatalf("branch=%q err=%v after a refused change", saved, err)
	}
}

// A raw log is kept for the chosen time from when its check started. A new
// choice applies at once to every log kept, without rewriting any: a log
// past it can no longer be read and the next cleanup deletes it, while Keep
// indefinitely keeps them all readable through any cleanup. Durable
// attempt records stay.
func TestRawLogRetentionAppliesToTheLogsKept(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	task, err := store.CreateTask(ctx, "project", "Retention", now)
	noErr(t, err)
	_, older := recordAttemptWithLog(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed), "older output")
	_, newer := recordAttemptWithLog(t, store, attemptFor(task, "2222222222222222222222222222222222222222", now.Add(20*24*time.Hour), AttemptFailed), "newer output")
	if want := now.Add(30 * 24 * time.Hour); !older.LogExpiresAt.Equal(want) {
		t.Fatalf("default expiry %v, want %v", older.LogExpiresAt, want)
	}
	// recorded is every expiry the log tables hold; saving a choice leaves
	// them as they are.
	recorded := func() string {
		t.Helper()
		var value string
		noErr(t, store.db.QueryRowContext(ctx, `SELECT group_concat(r.expires_at||':'||a.log_expires_at) FROM check_raw_logs r JOIN check_attempts a ON a.id=r.attempt_id`).Scan(&value))
		return value
	}
	before := recorded()
	save := func(retention CheckLogRetention) {
		t.Helper()
		noErr(t, store.SavePolicies(ctx, PolicyChange{CheckLogs: &retention}))
		if after := recorded(); after != before {
			t.Fatalf("saving %s rewrote the logs' expiries: %s, was %s", retention, after, before)
		}
	}
	logState := func(attempt CheckAttempt, at time.Time) string {
		t.Helper()
		retention, err := store.CheckLogRetention(ctx)
		noErr(t, err)
		_, found, err := store.ReadCheckLog(attempt, retention, at)
		noErr(t, err)
		return found
	}

	save(KeepCheckLogs)
	later := now.Add(3 * 365 * 24 * time.Hour)
	if removed, err := store.PruneCheckLogs(ctx, later); err != nil || removed != 0 {
		t.Fatalf("a cleanup under Keep indefinitely removed %d, err=%v", removed, err)
	}
	if got := logState(older, later); got != CheckLogFound {
		t.Fatalf("a log kept indefinitely reads as %s", got)
	}
	if expires := KeepCheckLogs.LogExpiry(older); expires != nil {
		t.Fatalf("a log kept indefinitely has the expiry %v", expires)
	}

	save(CheckLogs7Days)
	if expires := CheckLogs7Days.LogExpiry(older); expires == nil || !expires.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("expiry under 7 days=%v", expires)
	}
	cleanup := now.Add(10 * 24 * time.Hour)
	if got := logState(older, cleanup); got != CheckLogExpired {
		t.Fatalf("a log past the new time reads as %s", got)
	}
	if removed, err := store.PruneCheckLogs(ctx, cleanup); err != nil || removed != 1 {
		t.Fatalf("the next cleanup removed %d, err=%v", removed, err)
	}
	if got := logState(newer, cleanup); got != CheckLogFound {
		t.Fatalf("a log within the new time reads as %s", got)
	}
	if _, exists, err := store.CheckAttemptByID(ctx, older.RepositoryID, older.ID); err != nil || !exists {
		t.Fatalf("the attempt record went with its log: exists=%v err=%v", exists, err)
	}

	noErr(t, store.Exec(ctx, `UPDATE metadata SET value='45' WHERE key='check_log_retention_days'`))
	var policyErr *PolicyError
	if _, err := store.CheckLogRetention(ctx); !errors.As(err, &policyErr) {
		t.Fatalf("an unknown retention read with err=%v", err)
	}
	if removed, err := store.PruneCheckLogs(ctx, later); !errors.As(err, &policyErr) || removed != 0 {
		t.Fatalf("a cleanup under an unknown retention removed %d, err=%v", removed, err)
	}
	third := attemptFor(task, "3333333333333333333333333333333333333333", now.Add(21*24*time.Hour), AttemptFailed)
	_, _, err = store.RegisterCheckAttempt(ctx, third)
	noErr(t, err)
	// The result is kept without its raw log, and the attempt says which
	// setting to set again.
	_, stored, err := store.CompleteCheckAttempt(ctx, completionFor(third, "third output"), third.CreatedAt)
	noErr(t, err)
	if stored.Status != AttemptFailed || len(stored.Results) != 1 || stored.Results[0].OutputExcerpt != "out" ||
		stored.LogID != "" || stored.LogExpiresAt != nil || !strings.Contains(stored.LogError, "--check-logs") {
		t.Fatalf("attempt stored under an unknown retention: %+v", stored)
	}
	if count, err := store.TableRowCount(ctx, "check_raw_logs"); err != nil || count != 1 {
		t.Fatalf("raw logs=%d err=%v, want only the newer one", count, err)
	}
}

// A cleanup reads the raw logs it removes and stops at the first it keeps:
// its query walks check_raw_log_starts by key, with no scan of the attempts
// and no sort. With many attempts and many logs kept, removing a large
// backlog stays linear, and a cleanup with nothing due returns at once.
func TestPruneReadsOnlyTheLogsItRemoves(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+pruneCheckLogBatch, now.Unix(), checkLogPruneBatch)
	noErr(t, err)
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		noErr(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	noErr(t, rows.Err())
	noErr(t, rows.Close())
	joined := strings.Join(plan, "\n")
	if strings.Contains(joined, "SCAN") || strings.Contains(joined, "TEMP B-TREE") || !strings.Contains(joined, "check_raw_log_starts USING PRIMARY KEY (started_at<?)") {
		t.Fatalf("cleanup plan:\n%s", joined)
	}

	// 20000 older attempts whose logs are gone, 8000 due logs and 8000
	// logs kept, copied from one recorded attempt.
	task := newProjectTask(t, store, ctx, now)
	_, template := recordAttemptWithLog(t, store, attemptFor(task, strings.Repeat("a", 40), now, AttemptFailed), "template")
	old, recent := now.Add(-400*24*time.Hour).Unix(), now.Add(-24*time.Hour).Unix()
	copyAttempts(t, store, template.ID, 1, 28000, old)
	copyAttempts(t, store, template.ID, 28001, 36000, recent-28000)
	addRawLogs(t, store, 20001, 36000)
	week := CheckLogs7Days
	noErr(t, store.SavePolicies(ctx, PolicyChange{CheckLogs: &week}))

	started := time.Now()
	removed, err := store.PruneCheckLogs(ctx, now)
	backlog := time.Since(started)
	if err != nil || removed != 8000 || backlog > 10*time.Second {
		t.Fatalf("removing 8000 due logs: removed=%d err=%v in %v", removed, err, backlog)
	}
	started = time.Now()
	removed, err = store.PruneCheckLogs(ctx, now)
	idle := time.Since(started)
	if err != nil || removed != 0 || idle > time.Second {
		t.Fatalf("a cleanup with nothing due: removed=%d err=%v in %v", removed, err, idle)
	}
	if kept, err := store.TableRowCount(ctx, "check_raw_logs"); err != nil || kept != 8001 {
		t.Fatalf("logs kept=%d err=%v, want 8001", kept, err)
	}
	if starts, err := store.TableRowCount(ctx, "check_raw_log_starts"); err != nil || starts != 8001 {
		t.Fatalf("start rows=%d err=%v, want one per log kept", starts, err)
	}
	t.Logf("removed 8000 due logs in %v; an idle cleanup took %v", backlog, idle)
}

// The longest transfer is checked in whole seconds against both bounds
// before it is converted, so no stored number, however negative or large,
// wraps around into a valid limit.
func TestTransferSecondsAreCheckedBeforeTheyAreConverted(t *testing.T) {
	minimum, maximum := int64(MinimumTransferOperation/time.Second), int64(MaximumTransferOperation/time.Second)
	for _, test := range []struct {
		seconds int64
		valid   bool
	}{
		{math.MinInt64, false}, {-9223372036854689408, false}, {-1, false}, {0, false}, {minimum - 1, false},
		{minimum, true}, {maximum, true}, {maximum + 1, false}, {math.MaxInt64, false},
	} {
		operation, err := TransferOperationSeconds(test.seconds)
		if (err == nil) != test.valid || (test.valid && operation != time.Duration(test.seconds)*time.Second) {
			t.Errorf("%d seconds: operation=%v err=%v, want valid=%v", test.seconds, operation, err, test.valid)
		}
	}
	store := openTestStore(t)
	ctx := context.Background()
	for _, seconds := range []int64{math.MinInt64, -9223372036854689408, minimum - 1, maximum + 1} {
		noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('git_transfer_limits',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			fmt.Sprintf(`{"maximum_bytes":1048576,"operation_seconds":%d}`, seconds)))
		var policyErr *PolicyError
		if limits, err := store.GitTransferLimits(ctx); !errors.As(err, &policyErr) {
			t.Errorf("stored %d seconds read as %+v, err=%v", seconds, limits, err)
		}
	}
}

// copyAttempts copies the attempt templateID as attempts from to to, whose
// IDs are the numbers in hex and whose checks started at base plus the
// number, in seconds.
func copyAttempts(t *testing.T, store *Store, templateID string, from, to int, base int64) {
	t.Helper()
	ctx := context.Background()
	columns, err := store.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('check_attempts')`)
	noErr(t, err)
	var names, values []string
	for columns.Next() {
		var name string
		noErr(t, columns.Scan(&name))
		names = append(names, name)
		switch name {
		case "id", "log_id":
			values = append(values, "printf('%032x',n)")
		case "created_at":
			values = append(values, "?+n")
		default:
			values = append(values, name)
		}
	}
	noErr(t, columns.Err())
	noErr(t, columns.Close())
	noErr(t, store.Exec(ctx, `WITH RECURSIVE seq(n) AS (SELECT ? UNION ALL SELECT n+1 FROM seq WHERE n<?)
		INSERT INTO check_attempts(`+strings.Join(names, ",")+`) SELECT `+strings.Join(values, ",")+` FROM seq, check_attempts WHERE id=?`,
		from, to, base, templateID))
}

// addRawLogs gives the copied attempts from to to a one-byte raw log each,
// as a completion stores it.
func addRawLogs(t *testing.T, store *Store, from, to int) {
	t.Helper()
	ctx := context.Background()
	noErr(t, store.Exec(ctx, `INSERT INTO check_raw_logs(attempt_id,content,expires_at)
		SELECT id,x'2e',created_at+2592000 FROM check_attempts WHERE id BETWEEN printf('%032x',?) AND printf('%032x',?)`, from, to))
	noErr(t, store.Exec(ctx, `INSERT INTO check_raw_log_starts(started_at,attempt_id)
		SELECT created_at,id FROM check_attempts WHERE id BETWEEN printf('%032x',?) AND printf('%032x',?)`, from, to))
}

// A statement interrupted by its context leaves its connection unusable,
// and database/sql opens another. The new connection enforces foreign keys
// and waits for locks like the first, so a cleanup after the interruption
// still removes every due log and its start row.
func TestConnectionSettingsSurviveAnInterruptedStatement(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	task := newProjectTask(t, store, ctx, now)
	_, template := recordAttemptWithLog(t, store, attemptFor(task, strings.Repeat("a", 40), now, AttemptFailed), "template")
	copyAttempts(t, store, template.ID, 1, 40, now.Add(-400*24*time.Hour).Unix())
	addRawLogs(t, store, 1, 40)

	interrupted, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	var sum int64
	err := store.db.QueryRowContext(interrupted, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2000000000) SELECT sum(i) FROM n`).Scan(&sum)
	cancel()
	if err == nil {
		t.Fatal("the long statement was not interrupted")
	}
	var foreignKeys, busyTimeout, trustedSchema int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT (SELECT foreign_keys FROM pragma_foreign_keys),(SELECT timeout FROM pragma_busy_timeout),(SELECT trusted_schema FROM pragma_trusted_schema)`).Scan(&foreignKeys, &busyTimeout, &trustedSchema))
	if foreignKeys != 1 || busyTimeout != 5000 || trustedSchema != 0 {
		t.Fatalf("after an interrupted statement foreign_keys=%d busy_timeout=%d trusted_schema=%d", foreignKeys, busyTimeout, trustedSchema)
	}
	if removed, err := store.PruneCheckLogs(ctx, now); err != nil || removed != 40 {
		t.Fatalf("cleanup after an interrupted statement removed %d, err=%v", removed, err)
	}
	var logs, starts int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM check_raw_logs),(SELECT COUNT(*) FROM check_raw_log_starts)`).Scan(&logs, &starts))
	if logs != 1 || starts != 1 {
		t.Fatalf("after cleanup logs=%d starts=%d, want only the kept one", logs, starts)
	}
}

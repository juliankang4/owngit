package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"modernc.org/sqlite"
)

// The driver's errors carry their SQLite result code.
var _ sqliteCodeError = (*sqlite.Error)(nil)

// sqliteResult is an error with a SQLite result code, as the driver reports.
type sqliteResult int

func (code sqliteResult) Error() string { return fmt.Sprintf("sqlite result (%d)", int(code)) }
func (code sqliteResult) Code() int     { return int(code) }

// The database's answers to work its context stopped are recognized, and
// genuine failures are not.
func TestStoppedByContextRecognizesTheDatabaseStopAnswers(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	noErr(t, err)
	cancel()
	_ = tx.Rollback()
	var one int
	txDone := tx.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one)
	if !errors.Is(txDone, sql.ErrTxDone) {
		t.Fatalf("a read in a transaction its context ended: %v", txDone)
	}
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{"transaction rolled back for the context", fmt.Errorf("read: %w", txDone), true},
		{"statement interrupted", fmt.Errorf("begin: %w", sqliteResult(9)), true},
		{"disk I/O error", sqliteResult(10), false},
		{"database full", sqliteResult(13), false},
		{"other failure", errors.New("no such table"), false},
	} {
		if got := StoppedByContext(testCase.err); got != testCase.want {
			t.Errorf("%s: %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Nothing saved means the default; a value this build does not know reads
// as Every time, the strictest choice, and says it was not known.
func TestAdminConfirmationDefaultsAndReadsAnUnknownValueAsEveryTime(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	choice, known, err := store.AdminConfirmation(ctx)
	if err != nil || !known || choice != Confirm30Minutes {
		t.Fatalf("unsaved choice = %q, %v, %v; want 30m", choice, known, err)
	}
	noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('admin_confirmation','forever')`))
	if choice, known, err := store.AdminConfirmation(ctx); err != nil || known || choice != ConfirmEveryTime {
		t.Fatalf("an unknown saved value read as %q, %v, %v; want every, not known", choice, known, err)
	}
	if err := store.SetAdminConfirmation(ctx, "forever", testAdminLife); err == nil {
		t.Fatal("an unknown value was saved")
	}
	noErr(t, store.SetAdminConfirmation(ctx, Confirm1Hour, testAdminLife))
	if choice, known, err := store.AdminConfirmation(ctx); err != nil || !known || choice != Confirm1Hour {
		t.Fatalf("a saved choice did not replace the unknown value: %q, %v, %v", choice, known, err)
	}
}

// testAdminLife is how long an administrator session started under choice
// lasts, as auth.Manager has it with a 15 minute page session.
func testAdminLife(choice AdminConfirmation) time.Duration {
	if window := choice.Window(); window > 0 {
		return window
	}
	return 15 * time.Minute
}

// A stricter choice shortens held administrator sessions to the new time
// counted from when their password was typed; a looser one extends none,
// and other sessions are left alone.
func TestSetAdminConfirmationShortensOnlyAdministratorSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	for _, test := range []struct {
		name    string
		initial AdminConfirmation
		age     time.Duration
		choices []AdminConfirmation
		left    time.Duration
	}{
		{"fresh", Confirm8Hours, 0, []AdminConfirmation{Confirm1Hour}, time.Hour},
		{"old", Confirm8Hours, 7*time.Hour + 30*time.Minute, []AdminConfirmation{Confirm1Hour}, 0},
		{"short confirmation after a longer choice", Confirm30Minutes, 0, []AdminConfirmation{Confirm30Days, Confirm1Day}, 30 * time.Minute},
		{"repeated changes", Confirm8Hours, 10 * time.Minute, []AdminConfirmation{Confirm1Hour, Confirm7Days, Confirm30Minutes}, 20 * time.Minute},
		{"expired stays expired", Confirm30Minutes, 30 * time.Minute, []AdminConfirmation{Confirm1Day}, 0},
		{"page session stays short", ConfirmEveryTime, 0, []AdminConfirmation{Confirm30Days, Confirm1Day}, 15 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			noErr(t, store.SetAdminConfirmation(ctx, test.initial, testAdminLife))
			_, err := store.StartAdminSession(ctx, "", "admin", "csrf", 1, now.Add(-test.age), testAdminLife)
			noErr(t, err)
			var verified int64
			noErr(t, store.db.QueryRowContext(ctx, `SELECT verified_at FROM sessions WHERE kind='admin'`).Scan(&verified))
			if verified != now.Add(-test.age).Unix() {
				t.Fatalf("verification=%d, want %d", verified, now.Add(-test.age).Unix())
			}
			noErr(t, store.CreateSession(ctx, "general", "general", "csrf", 1, now.Add(12*time.Hour)))
			for _, choice := range test.choices {
				noErr(t, store.SetAdminConfirmation(ctx, choice, testAdminLife))
				if saved, _, err := store.AdminConfirmation(ctx); err != nil || saved != choice {
					t.Fatalf("saved choice=%q err=%v, want %q", saved, err, choice)
				}
			}
			session, ok, err := store.Session(ctx, "admin", "admin", now)
			noErr(t, err)
			if ok != (test.left > 0) || (ok && session.Expires.Sub(now) != test.left) {
				t.Fatalf("confirmation ok=%v expires=%s, want %s left", ok, session.Expires, test.left)
			}
			general, ok, err := store.Session(ctx, "general", "general", now)
			if err != nil || !ok || !general.Expires.Equal(now.Add(12*time.Hour)) {
				t.Fatalf("general session changed: %+v ok=%v err=%v", general, ok, err)
			}
		})
	}
}

func TestAdminConfirmationUpgradeDerivesOnlyKnownWindows(t *testing.T) {
	ctx := context.Background()
	expires := time.Unix(1_800_000_000, 0)
	hash := sha256.Sum256([]byte("upgraded-admin"))
	for _, choice := range []AdminConfirmation{"", Confirm30Minutes, Confirm1Hour, Confirm8Hours, Confirm1Day, Confirm7Days, Confirm30Days, ConfirmEveryTime, ConfirmNever, "forever"} {
		t.Run("saved="+string(choice), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			loadReleasedDump(t, directory, "schema16-1.1.6-populated.sql")
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			_, err := db.ExecContext(ctx, `DELETE FROM metadata WHERE key='admin_confirmation'`)
			noErr(t, err)
			if choice != "" {
				_, err = db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('admin_confirmation',?)`, choice)
				noErr(t, err)
			}
			_, err = db.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,'admin','csrf',1,?)`, hash[:], expires.Unix())
			noErr(t, err)
			noErr(t, db.Close())
			store, err := Open(ctx, directory)
			noErr(t, err)
			t.Cleanup(func() { _ = store.Close() })
			window := choice.Window()
			if choice == "" {
				window = DefaultAdminConfirmation.Window()
			}
			var verified sql.NullInt64
			var end int64
			noErr(t, store.db.QueryRowContext(ctx, `SELECT verified_at,expires_at FROM sessions WHERE token_hash=?`, hash[:]).Scan(&verified, &end))
			if end != expires.Unix() || verified.Valid != (window > 0) || (verified.Valid && verified.Int64 != expires.Add(-window).Unix()) {
				t.Fatalf("upgraded verification=%v expiry=%d, window=%s", verified, end, window)
			}
			want := expires.Unix()
			if window > 0 {
				want = min(want, expires.Add(-window+30*time.Minute).Unix())
			}
			for _, next := range []AdminConfirmation{Confirm30Minutes, Confirm30Days, Confirm1Hour} {
				noErr(t, store.SetAdminConfirmation(ctx, next, testAdminLife))
				noErr(t, store.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE token_hash=?`, hash[:]).Scan(&end))
				if end != want {
					t.Fatalf("after %q expiry=%d, want %d", next, end, want)
				}
			}
			var changed int
			noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE kind!='admin' AND verified_at IS NOT NULL`).Scan(&changed))
			if changed != 0 {
				t.Fatalf("migration changed %d other sessions", changed)
			}
		})
	}
}

// A backup does not carry the choice, so a restored installation asks for
// the administrator password again even when Do not ask was on.
func TestRestoredStateDoesNotReviveDoNotAsk(t *testing.T) {
	source := openTestStore(t)
	ctx := context.Background()
	noErr(t, source.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	noErr(t, source.SetAdminConfirmation(ctx, ConfirmNever, testAdminLife))
	snapshot, err := source.RecoverySnapshot(ctx)
	noErr(t, err)
	destination := openTestStore(t)
	noErr(t, destination.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	if choice, _, err := destination.AdminConfirmation(ctx); err != nil || choice != DefaultAdminConfirmation {
		t.Fatalf("restored choice = %q, %v; want the default", choice, err)
	}
}

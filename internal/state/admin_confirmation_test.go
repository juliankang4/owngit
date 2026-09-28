package state

import (
	"context"
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
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	noErr(t, store.SetAdminConfirmation(ctx, Confirm8Hours, testAdminLife))
	// early typed the password 7 hours 30 minutes ago, fresh just now.
	noErr(t, store.CreateSession(ctx, "early", "admin", "c1", 1, now.Add(30*time.Minute)))
	noErr(t, store.CreateSession(ctx, "fresh", "admin", "c2", 1, now.Add(8*time.Hour)))
	noErr(t, store.CreateSession(ctx, "general", "general", "c3", 1, now.Add(12*time.Hour)))
	ends := func(token string) time.Duration {
		t.Helper()
		session, ok, err := store.Session(ctx, token, "admin", now)
		noErr(t, err)
		if !ok {
			return 0
		}
		return session.Expires.Sub(now)
	}

	noErr(t, store.SetAdminConfirmation(ctx, Confirm1Hour, testAdminLife))
	if choice, _, err := store.AdminConfirmation(ctx); err != nil || choice != Confirm1Hour {
		t.Fatalf("saved choice = %q, %v", choice, err)
	}
	if left := ends("early"); left != 0 {
		t.Fatalf("a session typed 7h30m ago still has %s under 1 hour", left)
	}
	if left := ends("fresh"); left > time.Hour || left < time.Hour-time.Second {
		t.Fatalf("a session typed now ends in %s under 1 hour", left)
	}

	// Looser, then stricter again: the session may end sooner than its
	// password time plus 30 minutes, never later.
	noErr(t, store.SetAdminConfirmation(ctx, Confirm7Days, testAdminLife))
	if left := ends("fresh"); left > time.Hour {
		t.Fatalf("a looser choice extended a session to %s", left)
	}
	noErr(t, store.SetAdminConfirmation(ctx, Confirm30Minutes, testAdminLife))
	if left := ends("fresh"); left > 30*time.Minute {
		t.Fatalf("after 7 days then 30 minutes a session typed now ends in %s", left)
	}

	general, ok, err := store.Session(ctx, "general", "general", now)
	if err != nil || !ok || general.Expires.Sub(now) < 11*time.Hour {
		t.Fatalf("a general session changed: ends in %s ok=%v err=%v", general.Expires.Sub(now), ok, err)
	}
}

// Replacing a saved value this build does not know ends every
// administrator session, since nothing bounds how long they were given.
func TestReplacingAnUnknownChoiceEndsAdministratorSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('admin_confirmation','forever')`))
	noErr(t, store.CreateSession(ctx, "admin", "admin", "c1", 1, now.Add(time.Hour)))
	noErr(t, store.CreateSession(ctx, "general", "general", "c2", 1, now.Add(time.Hour)))
	noErr(t, store.SetAdminConfirmation(ctx, Confirm30Days, testAdminLife))
	if _, ok, err := store.Session(ctx, "admin", "admin", now); err != nil || ok {
		t.Fatalf("an administrator session survived: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Session(ctx, "general", "general", now); err != nil || !ok {
		t.Fatalf("a general session ended: ok=%v err=%v", ok, err)
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

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
	if err := store.SetAdminConfirmation(ctx, "forever", time.Now()); err == nil {
		t.Fatal("an unknown value was saved")
	}
	noErr(t, store.SetAdminConfirmation(ctx, Confirm1Hour, time.Now().Add(time.Hour)))
	if choice, known, err := store.AdminConfirmation(ctx); err != nil || !known || choice != Confirm1Hour {
		t.Fatalf("a saved choice did not replace the unknown value: %q, %v, %v", choice, known, err)
	}
}

// Saving a choice ends administrator sessions by the given time at the
// latest, extends none, and leaves other sessions alone.
func TestSetAdminConfirmationShortensOnlyAdministratorSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	noErr(t, store.CreateSession(ctx, "long-admin", "admin", "c1", 1, now.Add(30*24*time.Hour)))
	noErr(t, store.CreateSession(ctx, "short-admin", "admin", "c2", 1, now.Add(10*time.Minute)))
	noErr(t, store.CreateSession(ctx, "general", "general", "c3", 1, now.Add(12*time.Hour)))

	noErr(t, store.SetAdminConfirmation(ctx, Confirm1Hour, now.Add(time.Hour)))
	if choice, _, err := store.AdminConfirmation(ctx); err != nil || choice != Confirm1Hour {
		t.Fatalf("saved choice = %q, %v", choice, err)
	}
	for token, want := range map[string]time.Duration{"long-admin": time.Hour, "short-admin": 10 * time.Minute} {
		session, ok, err := store.Session(ctx, token, "admin", now)
		if err != nil || !ok || session.Expires.Sub(now) > want || session.Expires.Sub(now) < want-time.Second {
			t.Fatalf("%s ends in %s (ok=%v err=%v), want %s", token, session.Expires.Sub(now), ok, err, want)
		}
	}
	general, ok, err := store.Session(ctx, "general", "general", now)
	if err != nil || !ok || general.Expires.Sub(now) < 11*time.Hour {
		t.Fatalf("a general session changed: ends in %s ok=%v err=%v", general.Expires.Sub(now), ok, err)
	}
}

// A backup does not carry the choice, so a restored installation asks for
// the administrator password again even when Do not ask was on.
func TestRestoredStateDoesNotReviveDoNotAsk(t *testing.T) {
	source := openTestStore(t)
	ctx := context.Background()
	noErr(t, source.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	noErr(t, source.SetAdminConfirmation(ctx, ConfirmNever, time.Now()))
	snapshot, err := source.RecoverySnapshot(ctx)
	noErr(t, err)
	destination := openTestStore(t)
	noErr(t, destination.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	if choice, _, err := destination.AdminConfirmation(ctx); err != nil || choice != DefaultAdminConfirmation {
		t.Fatalf("restored choice = %q, %v; want the default", choice, err)
	}
}

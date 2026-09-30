package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The access policies read their defaults while nothing is saved, and a
// stored value they cannot use is a PolicyError naming their setting.
func TestAccessPoliciesReadTheirDefaultsAndRefuseWhatTheyCannotUse(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if ask, err := store.DeleteRequiresName(ctx); err != nil || !ask {
		t.Fatalf("delete choice=%v err=%v", ask, err)
	}
	if limits, err := store.LoginLimits(ctx); err != nil || limits != (LoginLimits{Attempts: 4, Window: 10 * time.Minute, Pause: 15 * time.Minute}) {
		t.Fatalf("login limits=%+v err=%v", limits, err)
	}
	if links, err := store.CrossSiteLinks(ctx); err != nil || links != CrossSiteStrict {
		t.Fatalf("cross-site=%q err=%v", links, err)
	}
	read := map[string]func() error{
		"delete_requires_name": func() error { _, err := store.DeleteRequiresName(ctx); return err },
		"login_limits":         func() error { _, err := store.LoginLimits(ctx); return err },
		"cross_site_links":     func() error { _, err := store.CrossSiteLinks(ctx); return err },
	}
	for _, stored := range []struct{ key, value string }{
		{"delete_requires_name", "yes"},
		{"login_limits", `{"attempts":0}`},
		{"login_limits", `{"attempts":101}`},
		{"login_limits", `{"window_seconds":59}`},
		{"login_limits", `{"pause_seconds":9223372036854775807}`},
		{"login_limits", `{"pause_seconds":-9223372036854775808}`},
		{"login_limits", `{"disabled":true}`},
		{"login_limits", `{} {}`},
		{"cross_site_links", "none"},
	} {
		noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stored.key, stored.value))
		var policyErr *PolicyError
		if err := read[stored.key](); !errors.As(err, &policyErr) || policyErr.Setting() != stored.key || policyErr.Advice() == "" {
			t.Fatalf("%s=%s read with err=%v, want a PolicyError", stored.key, stored.value, err)
		}
	}
	// A stored object may leave fields out; they keep their defaults.
	noErr(t, store.Exec(ctx, `UPDATE metadata SET value='{"pause_seconds":60}' WHERE key='login_limits'`))
	if limits, err := store.LoginLimits(ctx); err != nil || limits != (LoginLimits{Attempts: 4, Window: 10 * time.Minute, Pause: time.Minute}) {
		t.Fatalf("partial login limits=%+v err=%v", limits, err)
	}
	for _, refused := range []LoginLimits{{Attempts: 0, Window: time.Minute, Pause: time.Minute}, {Attempts: 4, Window: 90*time.Second + time.Millisecond, Pause: time.Minute}, {Attempts: 4, Window: time.Minute, Pause: 25 * time.Hour}} {
		if err := store.SavePolicies(ctx, PolicyChange{LoginLimits: &refused}); err == nil {
			t.Fatalf("saved %+v", refused)
		}
	}
	lax, off := CrossSiteLax, false
	limits := LoginLimits{Attempts: 100, Window: time.Minute, Pause: 24 * time.Hour}
	noErr(t, store.SavePolicies(ctx, PolicyChange{DeleteRequiresName: &off, LoginLimits: &limits, CrossSiteLinks: &lax}))
	if ask, err := store.DeleteRequiresName(ctx); err != nil || ask {
		t.Fatalf("delete choice=%v err=%v", ask, err)
	}
	if saved, err := store.LoginLimits(ctx); err != nil || saved != limits || !saved.Looser() {
		t.Fatalf("login limits=%+v err=%v", saved, err)
	}
	if links, err := store.CrossSiteLinks(ctx); err != nil || links != CrossSiteLax {
		t.Fatalf("cross-site=%q err=%v", links, err)
	}
}

// A wrong password is counted with the limits saved when it arrives, and a
// pause keeps the end it had when it started. Limits that cannot be read
// count nothing.
func TestFailedAttemptsFollowTheSavedLimits(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	two := LoginLimits{Attempts: 2, Window: time.Minute, Pause: 5 * time.Minute}
	noErr(t, store.SavePolicies(ctx, PolicyChange{LoginLimits: &two}))
	noErr(t, store.RecordFailedAttempt(ctx, "general", "192.0.2.1", now))
	if remaining, err := store.AttemptBlocked(ctx, "general", "192.0.2.1", now); err != nil || remaining != 0 {
		t.Fatalf("after one failure remaining=%v err=%v", remaining, err)
	}
	noErr(t, store.RecordFailedAttempt(ctx, "general", "192.0.2.1", now.Add(30*time.Second)))
	longer := LoginLimits{Attempts: 2, Window: time.Minute, Pause: time.Hour}
	noErr(t, store.SavePolicies(ctx, PolicyChange{LoginLimits: &longer}))
	if remaining, err := store.AttemptBlocked(ctx, "general", "192.0.2.1", now.Add(time.Minute)); err != nil || remaining != 4*time.Minute+30*time.Second {
		t.Fatalf("paused remaining=%v err=%v", remaining, err)
	}
	if remaining, err := store.AttemptBlocked(ctx, "admin", "192.0.2.1", now.Add(time.Minute)); err != nil || remaining != 0 {
		t.Fatalf("the administrator kind is counted apart: remaining=%v err=%v", remaining, err)
	}
	noErr(t, store.Exec(ctx, `UPDATE metadata SET value='{"attempts":"4"}' WHERE key='login_limits'`))
	var policyErr *PolicyError
	if err := store.RecordFailedAttempt(ctx, "general", "192.0.2.2", now); !errors.As(err, &policyErr) {
		t.Fatalf("counting under unreadable limits err=%v", err)
	}
	if remaining, err := store.AttemptBlocked(ctx, "general", "192.0.2.2", now); err != nil || remaining != 0 {
		t.Fatalf("an uncounted failure paused: remaining=%v err=%v", remaining, err)
	}
}

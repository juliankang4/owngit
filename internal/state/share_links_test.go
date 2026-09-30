package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// A share link is found by its secret only while it is active: not after
// it expires or is revoked. Revoking keeps the row, and a link belongs to
// its own repository.
func TestShareLinkIsActiveUntilItExpiresOrIsRevoked(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	for _, id := range []string{"project", "other"} {
		noErr(t, store.AddRepository(ctx, Repository{ID: id, Name: id, CreatedAt: now}))
	}
	secret := sha256.Sum256([]byte("synthetic secret"))
	expires := now.Add(time.Hour)
	link, err := store.CreateShareLink(ctx, ShareLink{
		RepositoryID: "project", Scope: ShareClone, Label: "Reviewer", PasswordHash: "encoded",
		CreatedBy: Actor{Kind: ActorAdministrator}, CreatedAt: now, ExpiresAt: &expires,
	}, secret[:])
	noErr(t, err)
	if !validAttemptID(link.ID) {
		t.Fatalf("link ID %q", link.ID)
	}

	found, active, err := store.ActiveShareLink(ctx, secret[:], now.Add(time.Minute))
	if err != nil || !active || found.ID != link.ID || found.RepositoryID != "project" || found.PasswordHash != "encoded" || found.CreatedBy.Kind != ActorAdministrator {
		t.Fatalf("active link=%+v active=%v err=%v", found, active, err)
	}
	if _, active, err := store.ActiveShareLink(ctx, secret[:], expires); err != nil || active {
		t.Fatalf("expired link active=%v err=%v", active, err)
	}
	other := sha256.Sum256([]byte("another secret"))
	if _, active, err := store.ActiveShareLink(ctx, other[:], now); err != nil || active {
		t.Fatalf("unknown secret active=%v err=%v", active, err)
	}

	// Use is written at most once a minute.
	noErr(t, store.NoteShareLinkUse(ctx, link.ID, now.Add(time.Minute)))
	noErr(t, store.NoteShareLinkUse(ctx, link.ID, now.Add(90*time.Second)))
	links, err := store.ShareLinks(ctx, "project")
	noErr(t, err)
	if len(links) != 1 || links[0].LastUsedAt == nil || !links[0].LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("links=%+v", links)
	}

	if _, err := store.RevokeShareLink(ctx, "other", link.ID, now); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("revoke through another repository err=%v", err)
	}
	revoked, err := store.RevokeShareLink(ctx, "project", link.ID, now.Add(2*time.Minute))
	if err != nil || revoked.RevokedAt == nil || revoked.Active(now.Add(3*time.Minute)) {
		t.Fatalf("revoked=%+v err=%v", revoked, err)
	}
	if _, err := store.RevokeShareLink(ctx, "project", link.ID, now); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("second revoke err=%v", err)
	}
	if _, active, err := store.ActiveShareLink(ctx, secret[:], now.Add(3*time.Minute)); err != nil || active {
		t.Fatalf("revoked link active=%v err=%v", active, err)
	}
	if links, err := store.ShareLinks(ctx, "project"); err != nil || len(links) != 1 {
		t.Fatalf("revoked link not listed: %d %v", len(links), err)
	}
}

// A link needs a scope, a one-line label and an expiry after its creation.
func TestShareLinkRefusesInvalidRecords(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "project", CreatedAt: now}))
	secret := sha256.Sum256([]byte("synthetic secret"))
	past := now.Add(-time.Second)
	for name, link := range map[string]ShareLink{
		"scope":  {RepositoryID: "project", Scope: "push", Label: "x", CreatedAt: now},
		"label":  {RepositoryID: "project", Scope: ShareBrowse, Label: "two\nlines", CreatedAt: now},
		"spaces": {RepositoryID: "project", Scope: ShareBrowse, Label: " padded ", CreatedAt: now},
		"expiry": {RepositoryID: "project", Scope: ShareBrowse, Label: "x", CreatedAt: now, ExpiresAt: &past},
	} {
		if _, err := store.CreateShareLink(ctx, link, secret[:]); err == nil {
			t.Errorf("%s: invalid link was stored", name)
		}
	}
	if links, err := store.ShareLinks(ctx, "project"); err != nil || len(links) != 0 {
		t.Fatalf("links=%d err=%v", len(links), err)
	}
}

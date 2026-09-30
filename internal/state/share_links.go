package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ShareLink lets someone without an account read one repository: its code
// pages, and with the clone scope a fetch-only Git clone. Whoever holds the
// link's secret uses it; only the SHA-256 of the secret is stored. Share
// links are machine-local, so a backup never carries them.
type ShareLink struct {
	ID           string
	RepositoryID string
	// Scope is ShareBrowse or ShareClone.
	Scope string
	Label string
	// PasswordHash is the extra password in the password encoding of the
	// auth package, or "" when the link has none.
	PasswordHash string
	CreatedBy    Actor
	CreatedAt    time.Time
	// ExpiresAt is nil for a link that works until it is revoked.
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// The scopes of a share link. A clone link also browses.
const (
	ShareBrowse = "browse"
	ShareClone  = "clone"
)

// ShareLabelBytes is the longest label a share link can have.
const ShareLabelBytes = 100

// ValidShareLabel reports whether label, without surrounding spaces, can
// name a share link: one line of at most ShareLabelBytes bytes.
func ValidShareLabel(label string) bool {
	return label == strings.TrimSpace(label) && validText(label, ShareLabelBytes)
}

// ErrShareLinkNotFound reports a revoke of a link that this repository
// does not have or that was already revoked.
var ErrShareLinkNotFound = errors.New("share link was not found or was already revoked")

// Active reports whether the link opens its repository at now: it is not
// revoked and has not expired. This is the one rule every use of a link
// follows.
func (link ShareLink) Active(now time.Time) bool {
	return link.RevokedAt == nil && (link.ExpiresAt == nil || now.Before(*link.ExpiresAt))
}

// CreateShareLink stores link, whose secret hashes to secretHash, and
// returns it with its new ID. CreatedAt and the other times are the
// caller's; ExpiresAt, when set, must be after CreatedAt.
func (s *Store) CreateShareLink(ctx context.Context, link ShareLink, secretHash []byte) (ShareLink, error) {
	if len(secretHash) != sha256.Size || link.CreatedAt.IsZero() {
		return ShareLink{}, errors.New("a share link needs a secret hash and a creation time")
	}
	if link.Scope != ShareBrowse && link.Scope != ShareClone {
		return ShareLink{}, fmt.Errorf("share link scope %q is not browse or clone", link.Scope)
	}
	if !ValidShareLabel(link.Label) {
		return ShareLink{}, errors.New("a share link needs a label of at most 100 bytes on one line")
	}
	if link.ExpiresAt != nil && !link.ExpiresAt.After(link.CreatedAt) {
		return ShareLink{}, errors.New("a share link must expire after it is created")
	}
	actor, err := encodeActor(link.CreatedBy)
	if err != nil {
		return ShareLink{}, err
	}
	link.ID, err = RandomID()
	if err != nil {
		return ShareLink{}, err
	}
	link.RevokedAt, link.LastUsedAt = nil, nil
	_, err = s.db.ExecContext(ctx, `INSERT INTO share_links(id,repository_id,secret_hash,scope,label,password_hash,created_by,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		link.ID, link.RepositoryID, secretHash, link.Scope, link.Label, link.PasswordHash, actor, link.CreatedAt.Unix(), nullableTime(link.ExpiresAt))
	if err != nil {
		return ShareLink{}, err
	}
	return link, nil
}

// ShareLinks lists the links of a repository, oldest first, revoked and
// expired ones included.
func (s *Store) ShareLinks(ctx context.Context, repositoryID string) ([]ShareLink, error) {
	rows, err := s.db.QueryContext(ctx, shareLinkSelect+` WHERE repository_id=? ORDER BY created_at,id`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []ShareLink
	for rows.Next() {
		link, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// ActiveShareLink returns the link whose secret hashes to secretHash when it
// is active at now. An unknown, revoked or expired link is not found. The
// lookup is by the hash, so how long it takes says nothing about how close
// a wrong secret came.
func (s *Store) ActiveShareLink(ctx context.Context, secretHash []byte, now time.Time) (ShareLink, bool, error) {
	if len(secretHash) != sha256.Size {
		return ShareLink{}, false, nil
	}
	link, err := scanShareLink(s.db.QueryRowContext(ctx, shareLinkSelect+` WHERE secret_hash=?`, secretHash))
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, false, nil
	}
	if err != nil {
		return ShareLink{}, false, err
	}
	return link, link.Active(now), nil
}

// shareUseInterval is how often a link's last use is written: a page and
// its pictures, or a clone's requests, count as one use.
const shareUseInterval = time.Minute

// NoteShareLinkUse records that link id was used at now.
func (s *Store) NoteShareLinkUse(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share_links SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<=?)`,
		now.Unix(), id, now.Add(-shareUseInterval).Unix())
	return err
}

// RevokeShareLink ends link id of a repository at now. The row stays, so
// the list still shows the link as revoked.
func (s *Store) RevokeShareLink(ctx context.Context, repositoryID, id string, now time.Time) (ShareLink, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE share_links SET revoked_at=? WHERE repository_id=? AND id=? AND revoked_at IS NULL`, now.Unix(), repositoryID, id)
	if err != nil {
		return ShareLink{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ShareLink{}, err
	}
	if affected != 1 {
		return ShareLink{}, ErrShareLinkNotFound
	}
	return scanShareLink(s.db.QueryRowContext(ctx, shareLinkSelect+` WHERE id=?`, id))
}

const shareLinkSelect = `SELECT id,repository_id,scope,label,password_hash,created_by,created_at,expires_at,revoked_at,last_used_at FROM share_links`

func scanShareLink(scanner rowScanner) (ShareLink, error) {
	var link ShareLink
	var actor string
	var createdAt int64
	var expiresAt, revokedAt, lastUsedAt sql.NullInt64
	if err := scanner.Scan(&link.ID, &link.RepositoryID, &link.Scope, &link.Label, &link.PasswordHash, &actor, &createdAt, &expiresAt, &revokedAt, &lastUsedAt); err != nil {
		return ShareLink{}, err
	}
	var err error
	if link.CreatedBy, err = decodeActor(actor); err != nil {
		return ShareLink{}, err
	}
	link.CreatedAt = unixTime(createdAt)
	link.ExpiresAt, link.RevokedAt, link.LastUsedAt = nullableTimePointer(expiresAt), nullableTimePointer(revokedAt), nullableTimePointer(lastUsedAt)
	return link, nil
}

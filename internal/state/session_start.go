package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

// ErrAccessChanged means a password changed, or shared access was disabled,
// after the supplied credential version was read.
var ErrAccessChanged = errors.New("password credential changed")

// StartSession stores a new session of kind that ends at expires, in place
// of the session of kind with token replaced, if there is one ("" for
// none). The browser that held replaced holds the new token from then on,
// so the old one ends in the same transaction: a copy of it grants nothing
// once the browser signs in again, and End or sign-out, which end the token
// the browser holds, leave no earlier one behind. Sessions of other
// browsers are not touched.
func (s *Store) StartSession(ctx context.Context, replaced, token, kind, csrf string, version int64, expires time.Time) error {
	if kind != "general" && kind != "admin" {
		return errors.New("invalid session kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := credentialVersionCurrent(ctx, tx, kind, version)
	if err != nil {
		return err
	}
	if !current {
		return ErrAccessChanged
	}
	if err := replaceSession(ctx, tx, replaced, token, kind, csrf, version, expires, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// StartAdminSession stores a new administrator session in place of
// replaced, as StartSession does. It lasts life of the saved choice from
// now, Every time for a value this build does not know. The saved choice
// and the administrator version verified by the caller are checked in the
// same transaction as insertion. It returns when the session ends.
func (s *Store) StartAdminSession(ctx context.Context, replaced, token, csrf string, version int64, now time.Time, life func(AdminConfirmation) time.Duration) (time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	choice, _, err := adminConfirmation(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	current, err := credentialVersionCurrent(ctx, tx, "admin", version)
	if err != nil {
		return time.Time{}, err
	}
	if !current {
		return time.Time{}, ErrAccessChanged
	}
	expires := now.Add(life(choice))
	verifiedAt := now.Unix()
	if err := replaceSession(ctx, tx, replaced, token, "admin", csrf, version, expires, &verifiedAt); err != nil {
		return time.Time{}, err
	}
	return expires, tx.Commit()
}

func replaceSession(ctx context.Context, tx *sql.Tx, replaced, token, kind, csrf string, version int64, expires time.Time, verifiedAt *int64) error {
	if replaced != "" {
		old := sha256.Sum256([]byte(replaced))
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=? AND kind=?`, old[:], kind); err != nil {
			return err
		}
	}
	hash := sha256.Sum256([]byte(token))
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at,verified_at) VALUES(?,?,?,?,?,?)`, hash[:], kind, csrf, version, expires.Unix(), verifiedAt)
	return err
}

package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminConfirmation is how long a browser may make administrator changes
// after the administrator password was typed in it. It is an optional
// metadata row, so it needs no schema change and older builds ignore it. It
// is machine-local: backups do not carry it, so a restored installation
// asks for the password again.
type AdminConfirmation string

const (
	// ConfirmEveryTime asks for the password on every change.
	ConfirmEveryTime AdminConfirmation = "every"
	Confirm30Minutes AdminConfirmation = "30m"
	Confirm1Hour     AdminConfirmation = "1h"
	Confirm8Hours    AdminConfirmation = "8h"
	Confirm1Day      AdminConfirmation = "1d"
	Confirm7Days     AdminConfirmation = "7d"
	Confirm30Days    AdminConfirmation = "30d"
	// ConfirmNever asks no one for the password. It applies to every
	// browser that can open the dashboard, not only to this one.
	ConfirmNever AdminConfirmation = "never"

	// DefaultAdminConfirmation applies while nothing was saved.
	DefaultAdminConfirmation = Confirm30Minutes
)

// AdminConfirmations lists the choices from the strictest to the loosest.
var AdminConfirmations = []AdminConfirmation{
	ConfirmEveryTime, Confirm30Minutes, Confirm1Hour, Confirm8Hours, Confirm1Day, Confirm7Days, Confirm30Days, ConfirmNever,
}

var adminConfirmationWindows = map[AdminConfirmation]time.Duration{
	Confirm30Minutes: 30 * time.Minute,
	Confirm1Hour:     time.Hour,
	Confirm8Hours:    8 * time.Hour,
	Confirm1Day:      24 * time.Hour,
	Confirm7Days:     7 * 24 * time.Hour,
	Confirm30Days:    30 * 24 * time.Hour,
}

// Window is how long a typed password is remembered in the browser it was
// typed in. It is zero for ConfirmEveryTime and ConfirmNever, which
// remember nothing.
func (c AdminConfirmation) Window() time.Duration { return adminConfirmationWindows[c] }

// ParseAdminConfirmation returns the choice value names.
func ParseAdminConfirmation(value string) (AdminConfirmation, bool) {
	for _, choice := range AdminConfirmations {
		if string(choice) == value {
			return choice, true
		}
	}
	return "", false
}

const adminConfirmationKey = "admin_confirmation"

// AdminConfirmation returns the saved choice, or the default when none was
// saved. A value this build does not know is an error, not the default.
func (s *Store) AdminConfirmation(ctx context.Context) (AdminConfirmation, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, adminConfirmationKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultAdminConfirmation, nil
	}
	if err != nil {
		return "", err
	}
	choice, ok := ParseAdminConfirmation(value)
	if !ok {
		return "", fmt.Errorf("invalid administrator confirmation setting %q", value)
	}
	return choice, nil
}

// SetAdminConfirmation saves choice and, in the same transaction, ends every
// administrator session by endBy at the latest. A shorter choice therefore
// also shortens the confirmations browsers hold already; a longer one
// extends none of them.
func (s *Store) SetAdminConfirmation(ctx context.Context, choice AdminConfirmation, endBy time.Time) error {
	if _, ok := ParseAdminConfirmation(string(choice)); !ok {
		return fmt.Errorf("invalid administrator confirmation setting %q", choice)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, adminConfirmationKey, string(choice)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET expires_at=MIN(expires_at,?) WHERE kind='admin'`, endBy.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

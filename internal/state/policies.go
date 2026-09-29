package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Server-wide policies the owner sets in Settings or with "owngit
// settings". Each is one metadata row, read where it applies: a missing row
// means the built-in default, and a stored value that does not parse or is
// out of bounds is an error, never the default. They are machine-local, so
// a backup does not carry them and a restored installation starts with the
// defaults.

// PolicyError reports a saved policy value that cannot be used. Setting the
// policy again replaces it.
type PolicyError struct {
	// Key is the metadata key of the policy.
	Key   string
	Value string
	Cause error
}

func (e *PolicyError) Error() string {
	return fmt.Sprintf("the saved %s %q cannot be used (%v); set it again in Settings or with owngit settings set", e.Key, e.Value, e.Cause)
}

func (e *PolicyError) Unwrap() error { return e.Cause }

// policyValue reads the metadata row key; found is false when there is none.
func (s *Store) policyValue(ctx context.Context, key string) (value string, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// PolicyChange names the policies to save. A nil field keeps its policy
// as it is.
type PolicyChange struct {
	Session       *GeneralSession
	InitialBranch *string
}

// SavePolicies checks every policy change names and saves them all in one
// transaction, or none. Each applies to what starts after it is saved.
func (s *Store) SavePolicies(ctx context.Context, change PolicyChange) error {
	values := map[string]string{}
	if change.Session != nil {
		if change.Session.Length() == 0 {
			return fmt.Errorf("invalid session length %q", *change.Session)
		}
		values[generalSessionKey] = strconv.FormatInt(int64(change.Session.Length()/time.Second), 10)
	}
	if change.InitialBranch != nil {
		if err := ValidateInitialBranch(*change.InitialBranch); err != nil {
			return err
		}
		values[initialBranchKey] = *change.InitialBranch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GeneralSession is how long a sign-in with the shared password lasts, one
// of GeneralSessions. It is stored as a number of seconds.
type GeneralSession string

const (
	Session1Hour   GeneralSession = "1h"
	Session8Hours  GeneralSession = "8h"
	Session12Hours GeneralSession = "12h"
	Session1Day    GeneralSession = "1d"
	Session7Days   GeneralSession = "7d"
	// Session30Days is the longest, as long as the administrator
	// confirmation can be remembered.
	Session30Days GeneralSession = "30d"

	DefaultGeneralSession = Session12Hours

	generalSessionKey = "general_session_seconds"
)

// GeneralSessions lists the choices from the shortest to the longest.
var GeneralSessions = []GeneralSession{Session1Hour, Session8Hours, Session12Hours, Session1Day, Session7Days, Session30Days}

var generalSessionLengths = map[GeneralSession]time.Duration{
	Session1Hour: time.Hour, Session8Hours: 8 * time.Hour, Session12Hours: 12 * time.Hour,
	Session1Day: 24 * time.Hour, Session7Days: 7 * 24 * time.Hour, Session30Days: 30 * 24 * time.Hour,
}

// Length is how long a session started under the choice lasts.
func (c GeneralSession) Length() time.Duration { return generalSessionLengths[c] }

// ParseGeneralSession returns the choice value names.
func ParseGeneralSession(value string) (GeneralSession, bool) {
	choice := GeneralSession(value)
	return choice, choice.Length() > 0
}

// GeneralSession returns how long a new general session lasts. A change
// applies to sessions that start afterwards.
func (s *Store) GeneralSession(ctx context.Context) (GeneralSession, error) {
	raw, found, err := s.policyValue(ctx, generalSessionKey)
	if err != nil || !found {
		return DefaultGeneralSession, err
	}
	for _, choice := range GeneralSessions {
		if raw == strconv.FormatInt(int64(choice.Length()/time.Second), 10) {
			return choice, nil
		}
	}
	return "", &PolicyError{Key: generalSessionKey, Value: raw, Cause: errors.New("not one of the session lengths")}
}

// The initial branch of new repositories: the branch HEAD names until the
// first push. It is stored as the branch name.
const (
	DefaultInitialBranch = "main"

	initialBranchKey = "initial_branch"
)

// ValidateInitialBranch checks a branch name for new repositories: a name
// Git accepts for a branch, written with ASCII letters, digits and "-", "_",
// "." and "/", at most 100 bytes, so it can be typed in a command as it is
// shown.
func ValidateInitialBranch(name string) error {
	valid := len(name) <= 100 && name != "HEAD" && !strings.HasPrefix(name, "-") && validBranchText(name)
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-_./", character)) {
			valid = false
		}
	}
	if !valid {
		return errors.New(`a branch name has at most 100 characters, only letters, digits, "-", "_", "." and "/", and must be one Git accepts`)
	}
	return nil
}

// InitialBranch returns the branch new repositories start on. A change
// applies to repositories created afterwards.
func (s *Store) InitialBranch(ctx context.Context) (string, error) {
	name, found, err := s.policyValue(ctx, initialBranchKey)
	if err != nil || !found {
		return DefaultInitialBranch, err
	}
	if err := ValidateInitialBranch(name); err != nil {
		return "", &PolicyError{Key: initialBranchKey, Value: name, Cause: err}
	}
	return name, nil
}

package state

import (
	"context"
	"database/sql"
	"encoding/json"
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

// policyValue reads the metadata row key with query, the store or a
// transaction; found is false when there is none.
func policyValue(ctx context.Context, query querier, key string) (value string, found bool, err error) {
	err = query.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, key).Scan(&value)
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
	GitTransfer   *GitTransferLimits
	CheckLogs     *CheckLogRetention
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
	if change.GitTransfer != nil {
		if err := change.GitTransfer.Validate(); err != nil {
			return err
		}
		stored, err := change.GitTransfer.stored()
		if err != nil {
			return err
		}
		values[gitTransferLimitsKey] = stored
	}
	if change.CheckLogs != nil {
		if _, valid := ParseCheckLogRetention(string(*change.CheckLogs)); !valid {
			return fmt.Errorf("invalid raw check log retention %q", *change.CheckLogs)
		}
		values[checkLogRetentionKey] = change.CheckLogs.stored()
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
	if change.CheckLogs != nil {
		if err := change.CheckLogs.applyTo(ctx, tx); err != nil {
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
	raw, found, err := policyValue(ctx, s.db, generalSessionKey)
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
	name, found, err := policyValue(ctx, s.db, initialBranchKey)
	if err != nil || !found {
		return DefaultInitialBranch, err
	}
	if err := ValidateInitialBranch(name); err != nil {
		return "", &PolicyError{Key: initialBranchKey, Value: name, Cause: err}
	}
	return name, nil
}

// GitTransferLimits bound each Git transfer: a clone, fetch or push, or an
// archive download. They are stored as a JSON object, in which a missing
// field means its default.
type GitTransferLimits struct {
	// MaximumBytes bounds what a transfer receives and, apart, what it
	// sends.
	MaximumBytes int64
	// Operation bounds how long a transfer takes.
	Operation time.Duration
}

// The bounds of the Git transfer limits. The largest keep one transfer from
// filling a disk or holding one of the few transfer slots for more than a
// day.
const (
	MinimumTransferBytes     = 1 << 20
	MaximumTransferBytes     = 64 << 30
	MinimumTransferOperation = time.Minute
	MaximumTransferOperation = 24 * time.Hour

	gitTransferLimitsKey = "git_transfer_limits"
)

// DefaultGitTransferLimits apply while nothing was saved.
var DefaultGitTransferLimits = GitTransferLimits{MaximumBytes: 4 << 30, Operation: 30 * time.Minute}

// ValidTransferBytes reports whether the largest transfer is within its
// bounds, 1 MiB to 64 GiB.
func ValidTransferBytes(bytes int64) bool {
	return bytes >= MinimumTransferBytes && bytes <= MaximumTransferBytes
}

// ValidTransferOperation reports whether the longest transfer is within its
// bounds, a whole number of seconds from 1 minute to 24 hours.
func ValidTransferOperation(operation time.Duration) bool {
	return operation >= MinimumTransferOperation && operation <= MaximumTransferOperation && operation%time.Second == 0
}

// Validate checks the limits against their bounds.
func (l GitTransferLimits) Validate() error {
	if !ValidTransferBytes(l.MaximumBytes) {
		return errors.New("the largest transfer is from 1 MiB to 64 GiB")
	}
	if !ValidTransferOperation(l.Operation) {
		return errors.New("the longest transfer is a whole number of seconds from 1 minute to 24 hours")
	}
	return nil
}

// gitTransferJSON is the stored form of GitTransferLimits.
type gitTransferJSON struct {
	MaximumBytes     *int64 `json:"maximum_bytes,omitempty"`
	OperationSeconds *int64 `json:"operation_seconds,omitempty"`
}

// GitTransferLimits returns the limits of a transfer that starts now.
func (s *Store) GitTransferLimits(ctx context.Context) (GitTransferLimits, error) {
	raw, found, err := policyValue(ctx, s.db, gitTransferLimitsKey)
	if err != nil || !found {
		return DefaultGitTransferLimits, err
	}
	limits := DefaultGitTransferLimits
	var stored gitTransferJSON
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&stored)
	if err == nil && decoder.More() {
		err = errors.New("more than one JSON value")
	}
	if err == nil {
		if stored.MaximumBytes != nil {
			limits.MaximumBytes = *stored.MaximumBytes
		}
		// A number of seconds past the bound is refused before it is
		// converted, where it could overflow.
		if seconds := stored.OperationSeconds; seconds != nil && *seconds > int64(MaximumTransferOperation/time.Second) {
			err = errors.New("the longest transfer is more than 24 hours")
		} else if seconds != nil {
			limits.Operation = time.Duration(*seconds) * time.Second
		}
	}
	if err == nil {
		err = limits.Validate()
	}
	if err != nil {
		return GitTransferLimits{}, &PolicyError{Key: gitTransferLimitsKey, Value: raw, Cause: err}
	}
	return limits, nil
}

func (l GitTransferLimits) stored() (string, error) {
	seconds := int64(l.Operation / time.Second)
	encoded, err := json.Marshal(gitTransferJSON{MaximumBytes: &l.MaximumBytes, OperationSeconds: &seconds})
	return string(encoded), err
}

// CheckLogRetention is how long raw check logs are kept, counted from when
// their check started: one of CheckLogRetentions. It is stored as a number
// of days, or as "indefinite". It applies to raw logs only; check results,
// summaries and excerpts stay.
type CheckLogRetention string

const (
	CheckLogs7Days   CheckLogRetention = "7d"
	CheckLogs30Days  CheckLogRetention = "30d"
	CheckLogs90Days  CheckLogRetention = "90d"
	CheckLogs365Days CheckLogRetention = "365d"
	// KeepCheckLogs keeps raw logs until another choice is saved.
	KeepCheckLogs CheckLogRetention = "indefinite"

	DefaultCheckLogRetention = CheckLogs30Days

	checkLogRetentionKey = "check_log_retention_days"
)

// CheckLogRetentions lists the choices from the shortest to the longest.
var CheckLogRetentions = []CheckLogRetention{CheckLogs7Days, CheckLogs30Days, CheckLogs90Days, CheckLogs365Days, KeepCheckLogs}

var checkLogRetentionDays = map[CheckLogRetention]int{CheckLogs7Days: 7, CheckLogs30Days: 30, CheckLogs90Days: 90, CheckLogs365Days: 365}

// CheckLogKeptIndefinitely is the expiry of a raw log kept under
// KeepCheckLogs, the last second RFC 3339 can write. The raw log table
// needs an expiry for every log; this one never passes, so no cleanup
// deletes the log and it stays readable.
var CheckLogKeptIndefinitely = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// ParseCheckLogRetention returns the choice value names.
func ParseCheckLogRetention(value string) (CheckLogRetention, bool) {
	choice := CheckLogRetention(value)
	return choice, choice == KeepCheckLogs || checkLogRetentionDays[choice] > 0
}

// Duration is how long a raw log is kept, or zero under KeepCheckLogs.
func (r CheckLogRetention) Duration() time.Duration {
	return time.Duration(checkLogRetentionDays[r]) * 24 * time.Hour
}

func (r CheckLogRetention) stored() string {
	if r == KeepCheckLogs {
		return string(KeepCheckLogs)
	}
	return strconv.Itoa(checkLogRetentionDays[r])
}

// expiry is when a raw log of a check that started at started is removed.
func (r CheckLogRetention) expiry(started time.Time) time.Time {
	if r == KeepCheckLogs {
		return CheckLogKeptIndefinitely
	}
	return started.UTC().Add(r.Duration())
}

// applyTo gives every raw log kept now the expiry of r, in both places the
// expiry is recorded, so the choice applies to them at once: a log past it
// can no longer be read and the next cleanup deletes it.
func (r CheckLogRetention) applyTo(ctx context.Context, tx *sql.Tx) error {
	expiry, arguments := `?`, []any{CheckLogKeptIndefinitely.Unix()}
	if r != KeepCheckLogs {
		expiry, arguments = `(SELECT a.created_at FROM check_attempts a WHERE a.id=check_raw_logs.attempt_id)+?`, []any{int64(r.Duration() / time.Second)}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE check_raw_logs SET expires_at=`+expiry, arguments...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE check_attempts SET log_expires_at=(SELECT r.expires_at FROM check_raw_logs r WHERE r.attempt_id=check_attempts.id)
		WHERE id IN (SELECT attempt_id FROM check_raw_logs)`)
	return err
}

// CheckLogRetention returns how long raw check logs are kept.
func (s *Store) CheckLogRetention(ctx context.Context) (CheckLogRetention, error) {
	return checkLogRetention(ctx, s.db)
}

// checkLogRetention reads the choice with query, the store or a
// transaction.
func checkLogRetention(ctx context.Context, query querier) (CheckLogRetention, error) {
	raw, found, err := policyValue(ctx, query, checkLogRetentionKey)
	if err != nil || !found {
		return DefaultCheckLogRetention, err
	}
	for _, choice := range CheckLogRetentions {
		if raw == choice.stored() {
			return choice, nil
		}
	}
	return "", &PolicyError{Key: checkLogRetentionKey, Value: raw, Cause: errors.New("not one of the raw log retention choices")}
}

// CheckLogExpiry returns when a raw log with the recorded expiry is
// removed, or nil when it is kept indefinitely or there is none.
func CheckLogExpiry(expires *time.Time) *time.Time {
	if expires == nil || expires.Equal(CheckLogKeptIndefinitely) {
		return nil
	}
	return expires
}

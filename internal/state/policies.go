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
// policy again replaces it. Error names the metadata row for the server
// log; Setting and Advice are what an owner is told.
type PolicyError struct {
	// Key is the metadata key of the policy, or repositoryPolicyKey for a
	// repository's own row.
	Key   string
	Value string
	Cause error
}

func (e *PolicyError) Error() string {
	return fmt.Sprintf("the saved %s %q cannot be used (%v)", e.Key, e.Value, e.Cause)
}

func (e *PolicyError) Unwrap() error { return e.Cause }

// Setting is the policy's field in the settings API, such as "session".
func (e *PolicyError) Setting() string { return policyNames[e.Key].field }

// Advice says which setting cannot be read and where to set it again,
// without the metadata key or the stored value.
func (e *PolicyError) Advice() string { return policyNames[e.Key].advice }

// policyNames describes each policy, by metadata key, in the names an owner
// uses to set it.
var policyNames = map[string]struct{ field, advice string }{
	generalSessionKey:        {"session", "The saved sign-in length cannot be read. Set it again under Settings, Access, or with owngit settings set --session."},
	initialBranchKey:         {"initial_branch", "The saved initial branch for new repositories cannot be read. Set it again under Settings, Repositories, or with owngit settings set --initial-branch."},
	gitTransferLimitsKey:     {"git_transfer", "The saved Git transfer limits cannot be read. Set them again under Settings, Repositories, or with owngit settings set --transfer-size, --transfer-time, --transfer-per-repository, --transfer-extra-slots, --transfer-idle and --transfer-queue."},
	maintenanceKey:           {"maintenance", "The saved repository maintenance choices cannot be read, so no repository is maintained. Set them again under Settings, Storage & recovery, or with owngit settings set --maintenance and the other --maintenance- options."},
	browseLimitsKey:          {"browse_limits", "The saved browsing limits cannot be read, so files, diffs and comparisons are not shown. Set them again under Settings, Repositories, or with owngit settings set and the --browse- options."},
	unusedObjectCleanupKey:   {"unused_object_cleanup", "The saved unused object cleanup choice cannot be read, so no object is removed. Set it again under Settings, Storage & recovery, or with owngit settings set --unused-object-cleanup."},
	checkLogRetentionKey:     {"check_logs", "The saved raw check log retention cannot be read. Set it again under Settings, Storage & recovery, or with owngit settings set --check-logs."},
	keptHistoryKey:           {"kept_history", "The saved server-wide kept history choice cannot be read. Set it again under Settings, Repositories, or with owngit settings set --kept-history."},
	deleteRequiresNameKey:    {"delete_requires_name", "The saved choice whether deleting a repository asks for its name cannot be read. Set it again under Settings, Repositories, or with owngit settings set --delete-requires-name."},
	loginLimitsKey:           {"login_limits", "The saved login attempt limits cannot be read, so a wrong password cannot be counted. Set all three again under Settings, Access, or with owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m (the defaults)."},
	crossSiteLinksKey:        {"cross_site_links", "The saved choice for links from other sites cannot be read, so the shared password cannot start a sign-in. Set it again under Settings, Access, or with owngit settings set --cross-site-links."},
	repositoryRefPrefixesKey: {"extra_ref_prefixes", "This repository's saved extra ref namespaces cannot be read, so pushes to it are refused. Set them again in the repository's Settings tab, under Advanced, or with owngit repo settings set --extra-ref-prefixes."},
	repositoryPolicyKey:      {"repository_policy", "This repository's saved kept history and default branch protection cannot be read. Set both again in the repository's Settings tab, or with owngit repo settings set --kept-history and --protect-default-branch."},
}

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
	Maintenance   *Maintenance
	Browse        *BrowseLimits
	Cleanup       *UnusedObjectCleanup
	CheckLogs     *CheckLogRetention
	KeptHistory   *bool
	// DeleteRequiresName, LoginLimits and CrossSiteLinks are the access
	// policies (access_policies.go).
	DeleteRequiresName *bool
	LoginLimits        *LoginLimits
	CrossSiteLinks     *CrossSiteLinks
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
	for _, err := range []error{
		addGroup(values, gitTransferLimitsKey, change.GitTransfer),
		addGroup(values, maintenanceKey, change.Maintenance),
		addGroup(values, browseLimitsKey, change.Browse),
		addGroup(values, unusedObjectCleanupKey, change.Cleanup),
	} {
		if err != nil {
			return err
		}
	}
	if change.CheckLogs != nil {
		if _, valid := ParseCheckLogRetention(string(*change.CheckLogs)); !valid {
			return fmt.Errorf("invalid raw check log retention %q", *change.CheckLogs)
		}
		values[checkLogRetentionKey] = change.CheckLogs.stored()
	}
	if change.KeptHistory != nil {
		values[keptHistoryKey] = onOff(*change.KeptHistory)
	}
	if change.DeleteRequiresName != nil {
		values[deleteRequiresNameKey] = onOff(*change.DeleteRequiresName)
	}
	if change.LoginLimits != nil {
		if err := change.LoginLimits.Validate(); err != nil {
			return err
		}
		stored, err := change.LoginLimits.stored()
		if err != nil {
			return err
		}
		values[loginLimitsKey] = stored
	}
	if change.CrossSiteLinks != nil {
		if _, valid := ParseCrossSiteLinks(string(*change.CrossSiteLinks)); !valid {
			return fmt.Errorf("invalid cross-site link choice %q", *change.CrossSiteLinks)
		}
		values[crossSiteLinksKey] = string(*change.CrossSiteLinks)
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

// CheckLogKeptIndefinitely is the expiry recorded for a raw log stored
// under KeepCheckLogs, the last second RFC 3339 can write. A recorded
// expiry only ties the log to its attempt record; whether the log is kept
// follows the retention saved when it is read or cleaned up (LogExpiry).
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

// expiry is the expiry recorded for a raw log of a check that started at
// started.
func (r CheckLogRetention) expiry(started time.Time) time.Time {
	if r == KeepCheckLogs {
		return CheckLogKeptIndefinitely
	}
	return started.UTC().Add(r.Duration())
}

// LogExpiry is when the raw log of attempt stops being readable under r:
// its check's start plus r. It is nil when r keeps logs indefinitely or the
// attempt stored no log. It follows the choice saved now, not the expiry
// recorded with the log, so a new choice applies to every log kept without
// rewriting them.
func (r CheckLogRetention) LogExpiry(attempt CheckAttempt) *time.Time {
	if r == KeepCheckLogs || attempt.LogID == "" || attempt.LogExpiresAt == nil {
		return nil
	}
	expires := r.expiry(attempt.CreatedAt)
	return &expires
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

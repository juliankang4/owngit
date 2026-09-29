package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Kept history and default branch protection decide what happens when a
// branch or tag is rewritten or deleted.
//
// Kept history: OwnGit keeps the previous tip of every branch or tag that
// is overwritten or deleted as a kept history line, which can be browsed and
// restored. The server default is the metadata row keptHistoryKey ("on" or
// "off", on when absent); a repository follows it unless its own
// repository_policies.retain_history says on (1) or off (0). Turning it off
// affects only what is overwritten or deleted afterwards: lines already
// kept stay.
//
// Default branch protection: repository_policies.protect_default_branch.
// While it is on, rewriting (a non-fast-forward update) or deleting the
// branch HEAD names is refused. It names no branch, so it follows a change of
// the default branch.
//
// Both are read where a ref write happens (RefWrites), and a stored value
// that cannot be used is a PolicyError there, never the default.

const (
	keptHistoryKey = "retain_history"
	// repositoryPolicyKey names a repository_policies row in a PolicyError.
	repositoryPolicyKey = "repository_policies"
)

// KeptHistory reports whether repositories that follow the server default
// keep overwritten and deleted history.
func (s *Store) KeptHistory(ctx context.Context) (bool, error) {
	return keptHistory(ctx, s.db)
}

func keptHistory(ctx context.Context, query querier) (bool, error) {
	raw, found, err := policyValue(ctx, query, keptHistoryKey)
	if err != nil || !found {
		return true, err
	}
	switch raw {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, &PolicyError{Key: keptHistoryKey, Value: raw, Cause: errors.New(`neither "on" nor "off"`)}
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

// KeptHistoryChoice is a repository's own kept history choice.
type KeptHistoryChoice string

const (
	// KeptHistoryDefault follows the server default.
	KeptHistoryDefault KeptHistoryChoice = "default"
	KeptHistoryOn      KeptHistoryChoice = "on"
	KeptHistoryOff     KeptHistoryChoice = "off"
)

// KeptHistoryChoices lists the choices in the order they are offered.
var KeptHistoryChoices = []KeptHistoryChoice{KeptHistoryDefault, KeptHistoryOn, KeptHistoryOff}

// ParseKeptHistoryChoice returns the choice value names.
func ParseKeptHistoryChoice(value string) (KeptHistoryChoice, bool) {
	choice := KeptHistoryChoice(value)
	return choice, choice == KeptHistoryDefault || choice == KeptHistoryOn || choice == KeptHistoryOff
}

// RepositoryRefPolicy is what a repository saved for its ref writes.
type RepositoryRefPolicy struct {
	KeptHistory          KeptHistoryChoice
	ProtectDefaultBranch bool
}

// RepositoryRefPolicyChange names what to change; a nil field keeps its
// saved value.
type RepositoryRefPolicyChange struct {
	KeptHistory          *KeptHistoryChoice
	ProtectDefaultBranch *bool
}

// RepositoryRefPolicy returns what repository id saved. A repository
// without a row has the defaults: follow the server, no protection.
func (s *Store) RepositoryRefPolicy(ctx context.Context, id string) (RepositoryRefPolicy, error) {
	return repositoryRefPolicy(ctx, s.db, id)
}

func repositoryRefPolicy(ctx context.Context, query querier, id string) (RepositoryRefPolicy, error) {
	// Both columns are read as stored, so any value that is not a choice,
	// whatever its type, is a PolicyError.
	var retain, protect any
	err := query.QueryRowContext(ctx, `SELECT retain_history,protect_default_branch FROM repository_policies WHERE repository_id=?`, id).Scan(&retain, &protect)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryRefPolicy{KeptHistory: KeptHistoryDefault}, nil
	}
	if err != nil {
		return RepositoryRefPolicy{}, err
	}
	policy := RepositoryRefPolicy{KeptHistory: KeptHistoryDefault}
	valid := true
	switch retain {
	case nil:
	case int64(1):
		policy.KeptHistory = KeptHistoryOn
	case int64(0):
		policy.KeptHistory = KeptHistoryOff
	default:
		valid = false
	}
	switch protect {
	case int64(1):
		policy.ProtectDefaultBranch = true
	case int64(0):
	default:
		valid = false
	}
	if !valid {
		value := fmt.Sprintf("retain_history=%v protect_default_branch=%v", retain, protect)
		return RepositoryRefPolicy{}, &PolicyError{Key: repositoryPolicyKey, Value: value, Cause: fmt.Errorf("repository %q has a value that is not a choice", id)}
	}
	return policy, nil
}

// RefPolicySave is the result of SaveRepositoryRefPolicy: the saved
// choices, what writes follow now, and which protection the change turned
// off.
type RefPolicySave struct {
	Saved RepositoryRefPolicy
	Now   RefWrites
	// KeptHistoryOff and ProtectionOff are true when the change turned kept
	// history or the protection off, or when it is off now and what it was
	// before cannot be read.
	KeptHistoryOff, ProtectionOff bool
}

// SaveRepositoryRefPolicy applies change to what repository id saved, in
// one transaction that also decides what the change turned off. A saved row
// that cannot be read is replaced only by a change that names both choices;
// otherwise its PolicyError is returned. A change whose result follows a
// server default that cannot be read is refused with that PolicyError and
// saves nothing. The repository's other policies are left as they are.
func (s *Store) SaveRepositoryRefPolicy(ctx context.Context, id string, change RepositoryRefPolicyChange) (RefPolicySave, error) {
	if change.KeptHistory != nil {
		if _, valid := ParseKeptHistoryChoice(string(*change.KeptHistory)); !valid {
			return RefPolicySave{}, fmt.Errorf("invalid kept history choice %q", *change.KeptHistory)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RefPolicySave{}, err
	}
	defer tx.Rollback()
	before, beforeErr := refWrites(ctx, tx, id)
	var policyErr *PolicyError
	if beforeErr != nil && !errors.As(beforeErr, &policyErr) {
		return RefPolicySave{}, beforeErr
	}
	policy, err := repositoryRefPolicy(ctx, tx, id)
	if errors.As(err, &policyErr) && change.KeptHistory != nil && change.ProtectDefaultBranch != nil {
		policy, err = RepositoryRefPolicy{}, nil
	}
	if err != nil {
		return RefPolicySave{}, err
	}
	if change.KeptHistory != nil {
		policy.KeptHistory = *change.KeptHistory
	}
	if change.ProtectDefaultBranch != nil {
		policy.ProtectDefaultBranch = *change.ProtectDefaultBranch
	}
	result := RefPolicySave{Saved: policy, Now: RefWrites{KeepHistory: policy.KeptHistory == KeptHistoryOn, ProtectDefaultBranch: policy.ProtectDefaultBranch}}
	if policy.KeptHistory == KeptHistoryDefault {
		if result.Now.KeepHistory, err = keptHistory(ctx, tx); err != nil {
			return RefPolicySave{}, err
		}
	}
	// What was on before and cannot be read now counts as on.
	result.KeptHistoryOff = !result.Now.KeepHistory && (beforeErr != nil || before.KeepHistory)
	result.ProtectionOff = !result.Now.ProtectDefaultBranch && (beforeErr != nil || before.ProtectDefaultBranch)
	var retain any
	switch policy.KeptHistory {
	case KeptHistoryOn:
		retain = 1
	case KeptHistoryOff:
		retain = 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repository_policies(repository_id,retain_history,protect_default_branch,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(repository_id) DO UPDATE SET retain_history=excluded.retain_history,protect_default_branch=excluded.protect_default_branch,updated_at=excluded.updated_at`,
		id, retain, boolInt(policy.ProtectDefaultBranch), time.Now().Unix()); err != nil {
		return RefPolicySave{}, err
	}
	return result, tx.Commit()
}

// RefWrites is what a write to a repository's branches and tags follows
// now.
type RefWrites struct {
	// KeepHistory keeps the previous tip of a branch or tag that is
	// overwritten or deleted.
	KeepHistory bool
	// ProtectDefaultBranch refuses rewriting or deleting the branch HEAD
	// names.
	ProtectDefaultBranch bool
}

// RefWrites returns what a ref write to repository id follows now: its own
// choices, and the server default where it follows it, read together in
// one transaction. A value that cannot be read is a PolicyError.
func (s *Store) RefWrites(ctx context.Context, id string) (RefWrites, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RefWrites{}, err
	}
	defer tx.Rollback()
	return refWrites(ctx, tx, id)
}

func refWrites(ctx context.Context, query querier, id string) (RefWrites, error) {
	policy, err := repositoryRefPolicy(ctx, query, id)
	if err != nil {
		return RefWrites{}, err
	}
	writes := RefWrites{KeepHistory: policy.KeptHistory == KeptHistoryOn, ProtectDefaultBranch: policy.ProtectDefaultBranch}
	if policy.KeptHistory == KeptHistoryDefault {
		if writes.KeepHistory, err = keptHistory(ctx, query); err != nil {
			return RefWrites{}, err
		}
	}
	return writes, nil
}

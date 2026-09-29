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
	raw, found, err := policyValue(ctx, s.db, keptHistoryKey)
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
	var retain sql.NullInt64
	var protect int64
	err := query.QueryRowContext(ctx, `SELECT retain_history,protect_default_branch FROM repository_policies WHERE repository_id=?`, id).Scan(&retain, &protect)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryRefPolicy{KeptHistory: KeptHistoryDefault}, nil
	}
	if err != nil {
		return RepositoryRefPolicy{}, err
	}
	policy := RepositoryRefPolicy{KeptHistory: KeptHistoryDefault, ProtectDefaultBranch: protect == 1}
	if retain.Valid && retain.Int64 == 1 {
		policy.KeptHistory = KeptHistoryOn
	} else if retain.Valid && retain.Int64 == 0 {
		policy.KeptHistory = KeptHistoryOff
	}
	if (retain.Valid && retain.Int64 != 0 && retain.Int64 != 1) || (protect != 0 && protect != 1) {
		value := fmt.Sprintf("retain_history=NULL protect_default_branch=%d", protect)
		if retain.Valid {
			value = fmt.Sprintf("retain_history=%d protect_default_branch=%d", retain.Int64, protect)
		}
		return RepositoryRefPolicy{}, &PolicyError{Key: repositoryPolicyKey, Value: value, Cause: fmt.Errorf("repository %q has a value that is not a choice", id)}
	}
	return policy, nil
}

// SaveRepositoryRefPolicy applies change to what repository id saved and
// returns the result. A saved row that cannot be read is replaced only by a
// change that names both choices; otherwise its PolicyError is returned.
// The repository's other policies are left as they are.
func (s *Store) SaveRepositoryRefPolicy(ctx context.Context, id string, change RepositoryRefPolicyChange) (RepositoryRefPolicy, error) {
	if change.KeptHistory != nil {
		if _, valid := ParseKeptHistoryChoice(string(*change.KeptHistory)); !valid {
			return RepositoryRefPolicy{}, fmt.Errorf("invalid kept history choice %q", *change.KeptHistory)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RepositoryRefPolicy{}, err
	}
	defer tx.Rollback()
	policy, err := repositoryRefPolicy(ctx, tx, id)
	var policyErr *PolicyError
	if errors.As(err, &policyErr) && change.KeptHistory != nil && change.ProtectDefaultBranch != nil {
		err = nil
	}
	if err != nil {
		return RepositoryRefPolicy{}, err
	}
	if change.KeptHistory != nil {
		policy.KeptHistory = *change.KeptHistory
	}
	if change.ProtectDefaultBranch != nil {
		policy.ProtectDefaultBranch = *change.ProtectDefaultBranch
	}
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
		return RepositoryRefPolicy{}, err
	}
	return policy, tx.Commit()
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
// choices, and the server default where it follows it. A value that cannot
// be read is a PolicyError.
func (s *Store) RefWrites(ctx context.Context, id string) (RefWrites, error) {
	policy, err := s.RepositoryRefPolicy(ctx, id)
	if err != nil {
		return RefWrites{}, err
	}
	writes := RefWrites{KeepHistory: policy.KeptHistory == KeptHistoryOn, ProtectDefaultBranch: policy.ProtectDefaultBranch}
	if policy.KeptHistory == KeptHistoryDefault {
		if writes.KeepHistory, err = s.KeptHistory(ctx); err != nil {
			return RefWrites{}, err
		}
	}
	return writes, nil
}

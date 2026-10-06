package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RepositoryAliasLifetime is how long an earlier address of a renamed
// repository keeps working. Git is answered at that address itself, and the
// answer names the current one (internal/githttp); a page or an API request
// is still redirected to the current name.
const RepositoryAliasLifetime = 90 * 24 * time.Hour

// ErrRepositoryNameTaken reports a name that is another repository's ID,
// current name or unexpired alias.
var ErrRepositoryNameTaken = errors.New("the name is used by another repository")

// RepositoryAddress is what a name in an address reaches.
type RepositoryAddress struct {
	RepositoryID string
	// Current is where the repository answers now: its current name, or its
	// ID when it was never renamed. A name other than Current is an alias.
	Current string
}

// ResolveRepositoryName is the one lookup of a repository address. A name
// reaches a repository when it is the repository's current name, an alias
// that has not expired, or the ID of a repository that was never renamed (or
// was renamed back). An expired alias, and the ID of a renamed repository
// that no alias keeps, reach nothing.
func (s *Store) ResolveRepositoryName(ctx context.Context, name string, now time.Time) (RepositoryAddress, bool, error) {
	var address RepositoryAddress
	var kind sql.NullString
	var until sql.NullInt64
	err := s.db.QueryRowContext(ctx, `WITH named AS (SELECT repository_id,kind,alias_until FROM repository_names WHERE name=?1)
		SELECT r.id, COALESCE(c.name, r.id), n.kind, n.alias_until
		FROM repositories r
		LEFT JOIN repository_names c ON c.repository_id=r.id AND c.kind='current'
		LEFT JOIN named n ON 1
		WHERE r.id=COALESCE(n.repository_id, ?1)`, name).Scan(&address.RepositoryID, &address.Current, &kind, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryAddress{}, false, nil
	}
	if err != nil {
		return RepositoryAddress{}, false, err
	}
	switch {
	case !kind.Valid && address.Current != name:
		// The ID of a renamed repository answers only through its alias row.
		return RepositoryAddress{}, false, nil
	case kind.String == RepositoryNameAlias && until.Int64 <= now.Unix():
		return RepositoryAddress{}, false, nil
	}
	return address, true, nil
}

// RepositoryNameInUse reports whether name is a repository's ID, current
// name or unexpired alias, so a new repository may not take it as its ID.
// AddRepository checks again when it records the repository.
func (s *Store) RepositoryNameInUse(ctx context.Context, name string, now time.Time) (bool, error) {
	owner, err := repositoryNameOwner(ctx, s.db, name, now)
	return owner != "", err
}

// RepositoryNameUnused reports whether name is no repository's ID and no
// recorded name, current or alias, expired or not. Only such a name may
// stand for a repository that does not exist yet: an expired alias still
// names the repository it belonged to until a creation that takes the name
// removes it.
func (s *Store) RepositoryNameUnused(ctx context.Context, name string) (bool, error) {
	var used int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM repositories WHERE id=?1) + (SELECT COUNT(*) FROM repository_names WHERE name=?1)`, name).Scan(&used)
	return used == 0, err
}

// repositoryNameOwner returns the repository that name belongs to, or "".
// A repository's ID always belongs to it, even once no alias keeps it as
// an address; its current name and unexpired aliases belong to it too.
func repositoryNameOwner(ctx context.Context, queryer queryRower, name string, now time.Time) (string, error) {
	var owner string
	err := queryer.QueryRowContext(ctx, `SELECT id FROM repositories WHERE id=?1
		UNION ALL SELECT repository_id FROM repository_names WHERE name=?1 AND (kind='current' OR alias_until>?2)
		LIMIT 1`, name, now.Unix()).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

// takeRepositoryName checks inside tx that name is free for repository id
// ("" for a new repository) and removes an expired alias left for it, or an
// alias of id itself, so the name can be written next.
func takeRepositoryName(ctx context.Context, tx *sql.Tx, id, name string, now time.Time) error {
	owner, err := repositoryNameOwner(ctx, tx, name, now)
	if err != nil {
		return err
	}
	if owner != "" && owner != id {
		return ErrRepositoryNameTaken
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM repository_names WHERE name=?`, name)
	return err
}

// RenameRepository gives repository id the display name name. Its address
// becomes the lowercase name, and the address it had becomes an alias that
// redirects for RepositoryAliasLifetime. Renaming to the ID removes the
// current name, so the repository answers at its ID again. The caller
// checks the name's syntax. The ID and the storage folder never change.
//
// Everything happens in one transaction, which refuses a repository that an
// import or a check is using, and a name another repository has. Expired
// aliases of the repository, and an expired alias of another repository
// that has the wanted name, are removed.
func (s *Store) RenameRepository(ctx context.Context, id, name string, now time.Time) (Repository, error) {
	address := strings.ToLower(name)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Repository{}, err
	}
	defer tx.Rollback()
	repository, exists, err := readRepository(ctx, tx, id)
	if err != nil {
		return Repository{}, err
	}
	if !exists {
		return Repository{}, ErrRepositoryNotFound
	}
	if err := repositoryDeletionBusy(ctx, tx, id); err != nil {
		return Repository{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repository_names WHERE repository_id=? AND kind='alias' AND alias_until<=?`, id, now.Unix()); err != nil {
		return Repository{}, err
	}
	if address != repository.Address {
		if address != id {
			var pending int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key=?`, repositoryDeletionKey(address)).Scan(&pending); err != nil {
				return Repository{}, err
			}
			if pending > 0 {
				return Repository{}, fmt.Errorf("%w: an earlier repository with this name is still being deleted", ErrRepositoryNameTaken)
			}
			if claimed, err := importClaimsRepositoryID(ctx, tx, address); err != nil {
				return Repository{}, err
			} else if claimed {
				return Repository{}, fmt.Errorf("%w: an import for this name is still running or needs recovery", ErrRepositoryNameTaken)
			}
		}
		if err := takeRepositoryName(ctx, tx, id, address, now); err != nil {
			return Repository{}, err
		}
		// The address the repository had, its ID or its current name, now
		// reaches the new one for RepositoryAliasLifetime.
		if _, err := tx.ExecContext(ctx, `DELETE FROM repository_names WHERE name=?`, repository.Address); err != nil {
			return Repository{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repository_names(name,repository_id,kind,created_at,alias_until) VALUES(?,?,'alias',?,?)`,
			repository.Address, id, now.Unix(), now.Add(RepositoryAliasLifetime).Unix()); err != nil {
			return Repository{}, err
		}
		if address != id {
			if _, err := tx.ExecContext(ctx, `INSERT INTO repository_names(name,repository_id,kind,created_at) VALUES(?,?,'current',?)`, address, id, now.Unix()); err != nil {
				return Repository{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repositories SET name=? WHERE id=?`, name, id); err != nil {
		return Repository{}, err
	}
	if err := tx.Commit(); err != nil {
		return Repository{}, err
	}
	repository.Name, repository.Address = name, address
	return repository, nil
}

// RepositoryAliases returns the unexpired aliases of repository id, newest
// first.
func (s *Store) RepositoryAliases(ctx context.Context, id string, now time.Time) ([]RepositoryName, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,created_at,alias_until FROM repository_names
		WHERE repository_id=? AND kind='alias' AND alias_until>? ORDER BY created_at DESC, name`, id, now.Unix())
	if err != nil {
		return nil, err
	}
	var aliases []RepositoryName
	for rows.Next() {
		var created, until int64
		name := RepositoryName{RepositoryID: id, Kind: RepositoryNameAlias}
		if err := rows.Scan(&name.Name, &created, &until); err != nil {
			rows.Close()
			return nil, err
		}
		end := unixTime(until)
		name.CreatedAt, name.AliasUntil = unixTime(created), &end
		aliases = append(aliases, name)
	}
	return aliases, closeRows(rows)
}

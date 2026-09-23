package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

// HelperCredential is a revocable, repository-scoped credential used only for
// evidence transport. It is deliberately weaker than the administrator
// password: it cannot change access or security settings, and it cannot create
// or revoke other credentials.
type HelperCredential struct {
	ID           string
	RepositoryID string
	Label        string
	// CreationID is the client-generated identity of the creation operation.
	// It lets a lost or malformed response be compensated with a scoped
	// revoke instead of leaving unreachable authority behind.
	CreationID string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// ErrCreationConflict reports a reused creation identity with a different
// payload. The existing authority is preserved, so the caller must not revoke
// it.
var ErrCreationConflict = errors.New("helper credential creation identity was reused with different content")

// CreateHelperCredential stores a new credential in one immediate transaction.
// A retransmit with the same creation identity and payload returns the existing
// credential instead of creating duplicate authority, and a changed payload is
// rejected instead of being silently accepted.
func (s *Store) CreateHelperCredential(ctx context.Context, repositoryID, label, creationID string, tokenHash []byte, now time.Time) (HelperCredential, bool, error) {
	if repositoryID == "" || len(tokenHash) != sha256.Size || now.IsZero() {
		return HelperCredential{}, false, errors.New("invalid helper credential")
	}
	if creationID != "" && !validAttemptID(creationID) {
		return HelperCredential{}, false, errors.New("invalid helper credential creation identity")
	}
	label = trimLabel(label)
	if label == "" {
		label = "check helper"
	}
	if !validText(label, 100) {
		return HelperCredential{}, false, errors.New("invalid helper credential label")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HelperCredential{}, false, err
	}
	defer tx.Rollback()
	if creationID != "" {
		existing, found, err := helperCredentialByCreationTx(ctx, tx, repositoryID, creationID)
		if err != nil {
			return HelperCredential{}, false, err
		}
		if found {
			if existing.Label != label {
				return HelperCredential{}, false, ErrCreationConflict
			}
			return existing, false, nil
		}
	}
	id, err := RandomID()
	if err != nil {
		return HelperCredential{}, false, err
	}
	credential := HelperCredential{ID: id, RepositoryID: repositoryID, Label: label, CreationID: creationID, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO helper_credentials(id,repository_id,label,creation_id,token_hash,created_at) VALUES(?,?,?,?,?,?)`,
		credential.ID, credential.RepositoryID, credential.Label, credential.CreationID, tokenHash, credential.CreatedAt.Unix()); err != nil {
		// A concurrent request may have won the unique index before this
		// transaction observed it.
		if creationID != "" {
			existing, found, readErr := helperCredentialByCreationTx(ctx, tx, repositoryID, creationID)
			if readErr == nil && found {
				if existing.Label != label {
					return HelperCredential{}, false, ErrCreationConflict
				}
				return existing, false, nil
			}
		}
		return HelperCredential{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return HelperCredential{}, false, err
	}
	return credential, true, nil
}

func (s *Store) helperCredentialByCreation(ctx context.Context, repositoryID, creationID string) (HelperCredential, bool, error) {
	return helperCredentialByCreationTx(ctx, s.db, repositoryID, creationID)
}

func helperCredentialByCreationTx(ctx context.Context, queryer querier, repositoryID, creationID string) (HelperCredential, bool, error) {
	row := queryer.QueryRowContext(ctx, helperCredentialSelect+` WHERE repository_id=? AND creation_id=?`, repositoryID, creationID)
	credential, err := scanHelperCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperCredential{}, false, nil
	}
	return credential, err == nil, err
}

// RevokeHelperCredentialByCreation revokes the credential created by one
// operation. It is idempotent, so a compensating revoke after a lost response
// is safe even when the server never created the credential.
func (s *Store) RevokeHelperCredentialByCreation(ctx context.Context, repositoryID, creationID string, now time.Time) error {
	if creationID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE helper_credentials SET revoked_at=? WHERE repository_id=? AND creation_id=? AND revoked_at IS NULL`, now.Unix(), repositoryID, creationID)
	return err
}

// HelperCredentialByToken resolves a live credential and records its last use.
// A revoked credential is rejected even though its row remains for audit.
func (s *Store) HelperCredentialByToken(ctx context.Context, tokenHash []byte, now time.Time) (HelperCredential, bool, error) {
	if len(tokenHash) != sha256.Size {
		return HelperCredential{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, helperCredentialSelect+` WHERE token_hash=? AND revoked_at IS NULL`, tokenHash)
	credential, err := scanHelperCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return HelperCredential{}, false, nil
	}
	if err != nil {
		return HelperCredential{}, false, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE helper_credentials SET last_used_at=? WHERE id=?`, now.Unix(), credential.ID); err != nil {
		return HelperCredential{}, false, err
	}
	value := now
	credential.LastUsedAt = &value
	return credential, true, nil
}

func (s *Store) HelperCredentials(ctx context.Context, repositoryID string) ([]HelperCredential, error) {
	rows, err := s.db.QueryContext(ctx, helperCredentialSelect+` WHERE repository_id=? ORDER BY created_at,id`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentials []HelperCredential
	for rows.Next() {
		credential, err := scanHelperCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

func (s *Store) RevokeHelperCredential(ctx context.Context, repositoryID, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE helper_credentials SET revoked_at=? WHERE repository_id=? AND id=? AND revoked_at IS NULL`, now.Unix(), repositoryID, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("helper credential was not found or was already revoked")
	}
	return nil
}

const helperCredentialSelect = `SELECT id,repository_id,label,creation_id,created_at,revoked_at,last_used_at FROM helper_credentials`

func scanHelperCredential(scanner rowScanner) (HelperCredential, error) {
	var credential HelperCredential
	var createdAt int64
	var revokedAt, lastUsedAt sql.NullInt64
	if err := scanner.Scan(&credential.ID, &credential.RepositoryID, &credential.Label, &credential.CreationID, &createdAt, &revokedAt, &lastUsedAt); err != nil {
		return HelperCredential{}, err
	}
	credential.CreatedAt = unixTime(createdAt)
	if revokedAt.Valid {
		value := unixTime(revokedAt.Int64)
		credential.RevokedAt = &value
	}
	if lastUsedAt.Valid {
		value := unixTime(lastUsedAt.Int64)
		credential.LastUsedAt = &value
	}
	return credential, nil
}

func trimLabel(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

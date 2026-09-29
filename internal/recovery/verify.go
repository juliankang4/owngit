package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The outcome of one verification step.
const (
	VerifyPassed = "passed"
	VerifyFailed = "failed"
	// VerifyNotRun is a step that an earlier failure stopped.
	VerifyNotRun = "not_run"
)

// Verification is the result of rehearsing a restore (Verify). Verified is
// set only when every step passed; otherwise Error says why not, and each
// repository says whether its own checks passed, failed or did not run.
type Verification struct {
	Backup   string `json:"backup"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
	// Version and CreatedAt are the manifest's, once it was read.
	Version      int                      `json:"version,omitempty"`
	CreatedAt    *time.Time               `json:"created_at,omitempty"`
	Repositories []RepositoryVerification `json:"repositories"`
	// Database is the check of the restored database and its schema.
	Database string `json:"database"`
	// Limits says what the checks cannot show.
	Limits []string `json:"limits"`
	// CleanupError says that the rehearsal folder could not be removed.
	CleanupError string `json:"cleanup_error,omitempty"`
}

// RepositoryVerification is the result for one repository: its bundle
// matches its digest, restores with the refs and HEAD that the manifest
// records, and git fsck finds every object those reach.
type RepositoryVerification struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Refs   int    `json:"refs"`
	Error  string `json:"error,omitempty"`
}

// Verify rehearses a restore of the backup input: Restore, with all its
// checks, restores it into a new folder private to this account in
// temporary (the system's temporary folder when empty); the restored
// database then passes SQLite's integrity and foreign key checks; and the
// folder is removed. Verify writes nothing beside the backup and never uses
// the live state. The error says why the backup is not verified, or that
// the folder could not be removed; the result says how far each check got.
func Verify(ctx context.Context, input, temporary, gitPath string) (Verification, error) {
	return verify(ctx, input, temporary, gitPath, defaultRestoreOperations())
}

func verify(ctx context.Context, input, temporary, gitPath string, operations restoreOperations) (Verification, error) {
	result := Verification{Backup: input, Repositories: []RepositoryVerification{}, Database: VerifyNotRun, Limits: []string{hashLimit}}
	if absolute, err := filepath.Abs(input); err == nil {
		result.Backup = absolute
	}
	if temporary == "" {
		temporary = os.TempDir()
	}
	scratch, err := os.MkdirTemp(temporary, "owngit-verify-")
	if err != nil {
		err = fmt.Errorf("create the folder for the rehearsal: %w", err)
		result.Error = err.Error()
		return result, err
	}
	err = rehearse(ctx, &result, input, scratch, gitPath, operations)
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Verified = true
	}
	if removeErr := os.RemoveAll(scratch); removeErr != nil {
		removeErr = fmt.Errorf("remove the rehearsal folder %s: %w", scratch, removeErr)
		result.CleanupError = removeErr.Error()
		err = errors.Join(err, removeErr)
	}
	return result, err
}

// rehearse restores input into scratch and checks the restored database.
func rehearse(ctx context.Context, result *Verification, input, scratch, gitPath string, operations restoreOperations) error {
	stateTarget := filepath.Join(scratch, "state")
	operations.rehearsal = result
	if err := restore(ctx, input, stateTarget, filepath.Join(scratch, "repositories"), gitPath, operations); err != nil {
		return err
	}
	// Opening the state checks that its schema is exactly the one this
	// version creates.
	store, err := operations.openState(ctx, stateTarget)
	if err == nil {
		err = errors.Join(store.CheckDatabase(ctx), store.Close())
	}
	if err != nil {
		result.Database = VerifyFailed
		return fmt.Errorf("check the restored database: %w", err)
	}
	result.Database = VerifyPassed
	return nil
}

// begin records what the manifest holds, each repository not checked yet.
func (v *Verification) begin(manifest Manifest) {
	if v == nil {
		return
	}
	created := manifest.CreatedAt
	v.Version, v.CreatedAt = manifest.Version, &created
	for _, item := range manifest.Repositories {
		v.Repositories = append(v.Repositories, RepositoryVerification{ID: item.ID, Status: VerifyNotRun, Refs: len(item.Refs)})
	}
}

// record records how the checks of the manifest's repository at index
// ended.
func (v *Verification) record(index int, err error) {
	if v == nil {
		return
	}
	entry := &v.Repositories[index]
	entry.Status, entry.Error = VerifyPassed, ""
	if err != nil {
		entry.Status, entry.Error = VerifyFailed, err.Error()
	}
}

// hashLimit says what verification cannot show for any backup. Every
// backup version records refs, HEAD and bundle digests in the same way, so
// every version gets every check.
const hashLimit = "The SHA-256 hashes detect damage, not a backup that someone replaced along with its manifest."

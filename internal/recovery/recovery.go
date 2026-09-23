package recovery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

var errManifestTooLarge = errors.New("backup manifest is too large")

// errDirectReviewManifest refuses records of the removed built-in review,
// which only unreleased development builds wrote.
var errDirectReviewManifest = errors.New("backup contains direct-review records written by an unreleased development build; this build cannot restore them")

const (
	manifestName        = "manifest.json"
	backupFormat        = "owngit-offline-backup"
	legacyBackupVersion = 1
	// pullRequestBackupVersion added durable pull request records. Versions 1
	// and 2 were written by the committed baseline.
	pullRequestBackupVersion = 2
	// backupVersion is the current format. Versions 3 through 8 were written
	// only by unreleased development builds and are refused.
	backupVersion      = 9
	maximumManifest    = 64 << 20
	pendingRestoreName = state.IncompleteRestoreMarkerName
)

type Manifest struct {
	Format                     string                        `json:"format"`
	Version                    int                           `json:"version"`
	CreatedAt                  time.Time                     `json:"created_at"`
	AccessMode                 string                        `json:"access_mode"`
	AccessHash                 string                        `json:"access_password_hash,omitempty"`
	AdminHash                  string                        `json:"admin_password_hash"`
	Repositories               []RepositoryManifest          `json:"repositories"`
	PullRequests               []PullRequestManifest         `json:"pull_requests,omitempty"`
	PullRequestRevisions       []PullRequestRevisionManifest `json:"pull_request_revisions,omitempty"`
	PullRequestReviews         []PullRequestReviewManifest   `json:"pull_request_reviews,omitempty"`
	PullRequestMergeIntents    []PullRequestMergeManifest    `json:"pull_request_merge_intents,omitempty"`
	Tasks                      []TaskManifest                `json:"tasks,omitempty"`
	CheckConfigurations        []CheckConfigurationManifest  `json:"check_configurations,omitempty"`
	CheckCycles                []CheckCycleManifest          `json:"check_cycles,omitempty"`
	CheckAttempts              []CheckAttemptManifest        `json:"check_attempts,omitempty"`
	CheckResults               []CheckResultManifest         `json:"check_results,omitempty"`
	CheckPolicies              []CheckPolicyManifest         `json:"check_policies,omitempty"`
	CheckJobs                  []CheckJobManifest            `json:"check_jobs,omitempty"`
	ImportSources              []ImportSourceManifest        `json:"import_sources,omitempty"`
	ImportRuns                 []ImportRunManifest           `json:"import_runs,omitempty"`
	ImportRunOrderKnown        bool                          `json:"import_run_order_known,omitempty"`
	ImportHEADOwnershipVersion int                           `json:"import_head_ownership_version,omitempty"`
	ImportObservations         []ImportObservationManifest   `json:"import_observations,omitempty"`
	ImportIntents              []ImportIntentManifest        `json:"import_intents,omitempty"`
}

type TaskManifest struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CheckCycleManifest struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	RepositoryID string    `json:"repository_id"`
	Sequence     int64     `json:"sequence"`
	ReservedAt   time.Time `json:"reserved_at"`
	// ReservedAfterSequence is the repository attempt counter observed inside
	// the reservation transaction.
	ReservedAfterSequence int64 `json:"reserved_after_sequence"`
}

type CheckConfigurationManifest struct {
	RepositoryID string                    `json:"repository_id"`
	Version      int64                     `json:"version"`
	ConfigHash   string                    `json:"config_hash"`
	Checks       []CheckDefinitionManifest `json:"checks"`
	CreatedAt    time.Time                 `json:"created_at"`
}

type CheckDefinitionManifest struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type CheckAttemptManifest struct {
	ID                   string    `json:"id"`
	TaskID               string    `json:"task_id"`
	RepositoryID         string    `json:"repository_id"`
	RevisionOID          string    `json:"revision_oid"`
	WorktreeState        string    `json:"worktree_state"`
	SubmittedWorktree    string    `json:"submitted_worktree_state,omitempty"`
	ConfigurationVersion int64     `json:"configuration_version"`
	Status               string    `json:"status"`
	ExitCode             *int      `json:"exit_code,omitempty"`
	StartedAt            time.Time `json:"started_at"`
	FinishedAt           time.Time `json:"finished_at"`
	DurationMS           int64     `json:"duration_ms"`
	Summary              string    `json:"summary"`
	Protection           string    `json:"protection,omitempty"`
	ExecutionScope       string    `json:"execution_scope,omitempty"`
	CredentialID         string    `json:"credential_id,omitempty"`
	// JobID links a server-owned automatic job. Empty preserves the exact
	// historical helper facts and digest.
	JobID            string     `json:"job_id,omitempty"`
	TimeoutMS        int64      `json:"timeout_ms,omitempty"`
	OutputLimitBytes int64      `json:"output_limit_bytes,omitempty"`
	LogID            string     `json:"log_id,omitempty"`
	LogExpiresAt     *time.Time `json:"log_expires_at,omitempty"`
	LogTruncated     bool       `json:"log_truncated,omitempty"`
	LogError         string     `json:"log_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	// Sequence is the server-issued repository-wide order. The digests make a
	// retransmit idempotent without touching an accepted log.
	Sequence           int64  `json:"sequence,omitempty"`
	CycleID            string `json:"cycle_id,omitempty"`
	RegistrationDigest string `json:"registration_digest,omitempty"`
	CompletionDigest   string `json:"completion_digest,omitempty"`
	SubmittedLogDigest string `json:"submitted_log_digest,omitempty"`
	SubmittedTruncated bool   `json:"submitted_truncated,omitempty"`
	SubmittedCancelled bool   `json:"submitted_cancelled,omitempty"`
	LogDigest          string `json:"log_digest,omitempty"`
}

type CheckResultManifest struct {
	AttemptID     string `json:"attempt_id"`
	Position      int    `json:"position"`
	Name          string `json:"name"`
	Command       string `json:"command"`
	Status        string `json:"status"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
	OutputExcerpt string `json:"output_excerpt,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	CleanupError  string `json:"cleanup_error,omitempty"`
}

type CheckPolicyManifest struct {
	RepositoryID        string                       `json:"repository_id"`
	PolicyVersion       int64                        `json:"policy_version"`
	PolicyDigest        string                       `json:"policy_digest"`
	Executor            string                       `json:"executor"`
	AllowedEvents       []string                     `json:"allowed_events"`
	MaxTimeoutMS        int64                        `json:"max_timeout_ms"`
	MaxOutputLimitBytes int64                        `json:"max_output_limit_bytes"`
	QueueLimit          int                          `json:"queue_limit"`
	MaxActiveJobs       int                          `json:"max_active_jobs"`
	MaxLeaseMS          int64                        `json:"max_lease_ms"`
	Execution           state.CheckExecutionSettings `json:"execution"`
	ConsentVersion      int64                        `json:"consent_version,omitempty"`
	ConsentDigest       string                       `json:"consent_digest,omitempty"`
	RunnerGeneration    int64                        `json:"runner_generation,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
}

type CheckJobLimitsManifest struct {
	TimeoutMS        int64 `json:"timeout_ms"`
	OutputLimitBytes int64 `json:"output_limit_bytes"`
}

type CheckJobManifest struct {
	ID                   string                       `json:"id"`
	RepositoryID         string                       `json:"repository_id"`
	TaskID               string                       `json:"task_id"`
	Trigger              string                       `json:"trigger"`
	EventKey             string                       `json:"event_key"`
	SourceOID            string                       `json:"source_oid"`
	BaseOID              string                       `json:"base_oid,omitempty"`
	PullRequestNumber    int64                        `json:"pull_request_number,omitempty"`
	TriggerRef           string                       `json:"trigger_ref"`
	WorkflowPath         string                       `json:"workflow_path"`
	WorkflowOID          string                       `json:"workflow_oid,omitempty"`
	WorkflowDigest       string                       `json:"workflow_digest"`
	ConfigurationVersion int64                        `json:"configuration_version"`
	Executor             string                       `json:"executor"`
	PolicyVersion        int64                        `json:"policy_version"`
	ConsentVersion       int64                        `json:"consent_version"`
	Limits               CheckJobLimitsManifest       `json:"limits"`
	Execution            state.CheckExecutionSettings `json:"execution"`
	DedupDigest          string                       `json:"dedup_digest"`
	RerunRoot            string                       `json:"rerun_root,omitempty"`
	RerunGeneration      int64                        `json:"rerun_generation,omitempty"`
	Status               string                       `json:"status"`
	AttemptID            string                       `json:"attempt_id,omitempty"`
	LeaseID              string                       `json:"lease_id,omitempty"`
	LeaseExpiresAt       *time.Time                   `json:"lease_expires_at,omitempty"`
	CredentialID         string                       `json:"credential_id,omitempty"`
	CredentialGeneration int64                        `json:"credential_generation,omitempty"`
	CredentialRole       string                       `json:"credential_role,omitempty"`
	Protection           string                       `json:"protection,omitempty"`
	AdmittedAt           time.Time                    `json:"admitted_at"`
	ClaimedAt            *time.Time                   `json:"claimed_at,omitempty"`
	StartedAt            *time.Time                   `json:"started_at,omitempty"`
	FinishedAt           *time.Time                   `json:"finished_at,omitempty"`
	LeaseLostAt          *time.Time                   `json:"lease_lost_at,omitempty"`
	CancelRequestedAt    *time.Time                   `json:"cancel_requested_at,omitempty"`
	InterruptedAt        *time.Time                   `json:"interrupted_at,omitempty"`
	Summary              string                       `json:"summary,omitempty"`
}

type RepositoryManifest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	// AttemptSequence is the repository-wide counter that issues attempt
	// sequences, so a restore keeps issuing higher sequences.
	AttemptSequence int64  `json:"attempt_sequence,omitempty"`
	Head            Head   `json:"head"`
	Refs            []Ref  `json:"refs"`
	Empty           bool   `json:"empty"`
	Bundle          string `json:"bundle,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

type Head struct {
	Symbolic string `json:"symbolic,omitempty"`
	OID      string `json:"oid,omitempty"`
}

type Ref struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}

type PullRequestManifest struct {
	RepositoryID   string     `json:"repository_id"`
	Number         int64      `json:"number"`
	Title          string     `json:"title"`
	SourceBranch   string     `json:"source_branch"`
	TargetBranch   string     `json:"target_branch"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergeSourceOID string     `json:"merge_source_oid,omitempty"`
	MergeTargetOID string     `json:"merge_target_oid,omitempty"`
	MergeOID       string     `json:"merge_oid,omitempty"`
	MergeReceipt   string     `json:"merge_receipt_ref,omitempty"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
}

type PullRequestRevisionManifest struct {
	RepositoryID      string    `json:"repository_id"`
	PullRequestNumber int64     `json:"pull_request_number"`
	SourceOID         string    `json:"source_oid"`
	TargetOID         string    `json:"target_oid"`
	RecordedAt        time.Time `json:"recorded_at"`
}

type PullRequestReviewManifest struct {
	RepositoryID      string    `json:"repository_id"`
	PullRequestNumber int64     `json:"pull_request_number"`
	Sequence          int64     `json:"sequence"`
	SourceOID         string    `json:"source_oid"`
	TargetOID         string    `json:"target_oid"`
	Status            string    `json:"status"`
	ReviewerLabel     string    `json:"reviewer_label,omitempty"`
	Provenance        string    `json:"provenance"`
	ReviewEventID     string    `json:"review_event_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type PullRequestMergeManifest struct {
	RepositoryID      string    `json:"repository_id"`
	PullRequestNumber int64     `json:"pull_request_number"`
	SourceOID         string    `json:"source_oid"`
	TargetOID         string    `json:"target_oid"`
	Mode              string    `json:"mode,omitempty"`
	TreeOID           string    `json:"tree_oid,omitempty"`
	ResultOID         string    `json:"result_oid,omitempty"`
	ReceiptRef        string    `json:"receipt_ref"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type pendingRestore struct {
	Version          int    `json:"version"`
	StateTarget      string `json:"state_target"`
	RepositoryTarget string `json:"repository_target"`
	StateStage       string `json:"state_stage"`
	RepositoryStage  string `json:"repository_stage"`
}

type commandRunner interface {
	Run(context.Context, string, io.Reader, ...string) (gitexec.Result, error)
}

func Create(ctx context.Context, store *state.Store, manager *repository.Manager, output string) error {
	service := &pullrequest.Service{Store: store, Repositories: manager}
	if err := service.ReconcileAll(ctx); err != nil {
		return fmt.Errorf("reconcile pull request state before backup: %w", err)
	}
	return create(ctx, store, manager, manager.Git, output)
}

func create(ctx context.Context, store *state.Store, manager *repository.Manager, runner commandRunner, output string) error {
	absolute, err := absentTarget(output, "backup")
	if err != nil {
		return err
	}
	stateRoot, err := canonicalExistingDirectory(store.Dir(), "state storage")
	if err != nil {
		return err
	}
	repositoryRoot, err := canonicalExistingDirectory(manager.RepositoryRoot(), "repository storage")
	if err != nil {
		return err
	}
	if pathsOverlap(absolute, stateRoot) || pathsOverlap(absolute, repositoryRoot) {
		return errors.New("backup destination must not overlap state or repository storage")
	}
	parent := filepath.Dir(absolute)
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	stage := filepath.Join(parent, "."+filepath.Base(absolute)+".owngit-backup-"+suffix)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fmt.Errorf("create staged backup: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := state.ProtectPrivatePath(stage, true); err != nil {
		return err
	}
	bundles := filepath.Join(stage, "repositories")
	if err := os.Mkdir(bundles, 0o700); err != nil {
		return err
	}

	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		return err
	}
	manifest := Manifest{
		Format: backupFormat, Version: backupVersion, CreatedAt: time.Now().UTC(),
		AccessMode: snapshot.AccessMode, AccessHash: snapshot.AccessPasswordHash, AdminHash: snapshot.AdminPasswordHash,
	}
	addPullRequestState(&manifest, snapshot)
	addCheckState(&manifest, snapshot)
	addImportState(&manifest, snapshot)
	for _, stored := range snapshot.Repositories {
		if err := repository.ValidateID(stored.ID); err != nil {
			return fmt.Errorf("repository %q has an unsupported ID: %w", stored.ID, err)
		}
		repositoryPath, _, exists, err := manager.ExistingPath(ctx, stored.ID)
		if err != nil || !exists {
			if err == nil {
				err = errors.New("repository not found")
			}
			return fmt.Errorf("open repository %q: %w", stored.ID, err)
		}
		item, err := inspectRepository(ctx, runner, repositoryPath, stored)
		if err != nil {
			return fmt.Errorf("inspect repository %q: %w", stored.ID, err)
		}
		if !item.Empty {
			item.Bundle = path.Join("repositories", stored.ID+".bundle")
			bundlePath := filepath.Join(stage, filepath.FromSlash(item.Bundle))
			arguments := []string{"--git-dir", ".", "bundle", "create", bundlePath, "--all"}
			if item.Head.OID != "" {
				arguments = append(arguments, "HEAD")
			}
			if _, err := runner.Run(ctx, repositoryPath, nil, arguments...); err != nil {
				return fmt.Errorf("bundle repository %q: %w", stored.ID, err)
			}
			if err := os.Chmod(bundlePath, 0o600); err != nil {
				return err
			}
			if err := state.ProtectPrivatePath(bundlePath, false); err != nil {
				return err
			}
			if err := syncRegularFile(bundlePath); err != nil {
				return err
			}
			item.SHA256, err = fileSHA256(bundlePath)
			if err != nil {
				return err
			}
		}
		manifest.Repositories = append(manifest.Repositories, item)
	}
	if err := validateManifest(manifest); err != nil {
		return fmt.Errorf("validate completed backup manifest: %w", err)
	}
	manifestPath := filepath.Join(stage, manifestName)
	file, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := writeManifest(file, manifest, maximumManifest); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := state.ProtectPrivatePath(manifestPath, false); err != nil {
		return err
	}
	if err := syncDirectory(bundles); err != nil {
		return err
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireAbsent(absolute, "backup"); err != nil {
		return err
	}
	if err := renameNoReplace(stage, absolute); err != nil {
		return fmt.Errorf("publish completed backup: %w", err)
	}
	published = true
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("backup was published but its parent directory could not be synchronized: %w", err)
	}
	return nil
}

type restoreOperations struct {
	rename          func(string, string) error
	openState       func(context.Context, string) (*state.Store, error)
	prepareExisting func(context.Context, *repository.Manager, *gitexec.Runner) error
}

func defaultRestoreOperations() restoreOperations {
	return restoreOperations{
		rename:    renameNoReplace,
		openState: state.Open,
		prepareExisting: func(ctx context.Context, manager *repository.Manager, hookRuntime *gitexec.Runner) error {
			return manager.PrepareExistingForRuntime(ctx, hookRuntime)
		},
	}
}

func Restore(ctx context.Context, input, stateDirectory, repositoryRoot, gitPath string) error {
	return restore(ctx, input, stateDirectory, repositoryRoot, gitPath, defaultRestoreOperations())
}

func restore(ctx context.Context, input, stateDirectory, repositoryRoot, gitPath string, operations restoreOperations) error {
	inputRoot, err := checkedInputRoot(input)
	if err != nil {
		return err
	}
	stateTarget, err := absentTarget(stateDirectory, "state")
	if err != nil {
		return err
	}
	repositoryTarget, err := absentTarget(repositoryRoot, "repository")
	if err != nil {
		return err
	}
	if pathsOverlap(stateTarget, repositoryTarget) || pathsOverlap(inputRoot, stateTarget) || pathsOverlap(inputRoot, repositoryTarget) {
		return errors.New("backup, state, and repository paths must not overlap")
	}

	manifest, err := readManifest(filepath.Join(inputRoot, manifestName))
	if err != nil {
		return err
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	for _, item := range manifest.Repositories {
		if item.Empty {
			continue
		}
		bundlePath := filepath.Join(inputRoot, filepath.FromSlash(item.Bundle))
		if err := requireRegularFile(bundlePath); err != nil {
			return fmt.Errorf("inspect bundle for %q: %w", item.ID, err)
		}
		digest, err := fileSHA256(bundlePath)
		if err != nil {
			return err
		}
		if digest != item.SHA256 {
			return fmt.Errorf("bundle checksum mismatch for repository %q", item.ID)
		}
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	stateStage := stateTarget + ".owngit-restore-" + suffix
	repositoryStage := repositoryTarget + ".owngit-restore-" + suffix
	cleanupStateStage := true
	cleanupRepositoryStage := true
	defer func() {
		if cleanupStateStage {
			_ = os.RemoveAll(stateStage)
		}
		if cleanupRepositoryStage {
			_ = os.RemoveAll(repositoryStage)
		}
	}()
	if err := os.Mkdir(stateStage, 0o700); err != nil {
		return fmt.Errorf("create staged state: %w", err)
	}
	if err := state.ProtectPrivatePath(stateStage, true); err != nil {
		return err
	}
	if err := os.Mkdir(repositoryStage, 0o700); err != nil {
		return fmt.Errorf("create staged repository root: %w", err)
	}
	runner, err := gitexec.New(gitPath, filepath.Join(stateStage, "runtime"))
	if err != nil {
		return err
	}
	for _, item := range manifest.Repositories {
		if err := restoreRepository(ctx, runner, inputRoot, repositoryStage, item); err != nil {
			return fmt.Errorf("validate and restore repository %q: %w", item.ID, err)
		}
	}

	store, err := operations.openState(ctx, stateStage)
	if err != nil {
		return err
	}
	snapshot := recoveryState(manifest)
	for _, item := range manifest.Repositories {
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{
			ID: item.ID, Name: item.Name, Description: item.Description, CreatedAt: item.CreatedAt,
			AttemptSequence: item.AttemptSequence,
		})
	}
	if err := store.RestoreRecoveryState(ctx, repositoryTarget, snapshot); err != nil {
		store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	store, err = operations.openState(ctx, stateStage)
	if err != nil {
		return fmt.Errorf("reopen staged state: %w", err)
	}
	hookRuntime := *runner
	hookRuntime.HomeDir = filepath.Join(stateTarget, "runtime", "git-home")
	hookRuntime.TempDir = filepath.Join(stateTarget, "runtime", "tmp")
	hookRuntime.GlobalConfigPath = filepath.Join(stateTarget, "runtime", "gitconfig.empty")
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryStage}
	prepareErr := operations.prepareExisting(ctx, manager, &hookRuntime)
	var reconcileErr error
	if prepareErr == nil {
		reconcileErr = (&pullrequest.Service{Store: store, Repositories: manager}).ReconcileAll(ctx)
	}
	closeErr := store.Close()
	if prepareErr != nil {
		return fmt.Errorf("rebuild staged repository hooks: %w", prepareErr)
	}
	if reconcileErr != nil {
		return fmt.Errorf("reconcile staged pull request merges: %w", reconcileErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pending := pendingRestore{
		Version: 1, StateTarget: stateTarget, RepositoryTarget: repositoryTarget,
		StateStage: stateStage, RepositoryStage: repositoryStage,
	}
	if err := writePendingRestore(stateStage, pending); err != nil {
		return err
	}
	if err := writePendingRestore(repositoryStage, pending); err != nil {
		return err
	}
	if err := syncDirectory(stateStage); err != nil {
		return err
	}
	if err := syncDirectory(repositoryStage); err != nil {
		return err
	}
	if err := requireAbsent(stateTarget, "state"); err != nil {
		return err
	}
	if err := operations.rename(stateStage, stateTarget); err != nil {
		return fmt.Errorf("publish guarded state directory: %w", err)
	}
	cleanupStateStage = false

	publishRepositoryErr := syncDirectory(filepath.Dir(stateTarget))
	if publishRepositoryErr == nil {
		publishRepositoryErr = ctx.Err()
	}
	if publishRepositoryErr == nil {
		publishRepositoryErr = requireAbsent(repositoryTarget, "repository")
	}
	if publishRepositoryErr == nil {
		publishRepositoryErr = operations.rename(repositoryStage, repositoryTarget)
	}
	if publishRepositoryErr != nil {
		if rollbackErr := operations.rename(stateTarget, stateStage); rollbackErr != nil {
			cleanupRepositoryStage = false
			return fmt.Errorf("publish repository root: %v; guarded state remains pending at %s and staged repositories remain at %s because rollback failed: %w", publishRepositoryErr, stateTarget, repositoryStage, rollbackErr)
		}
		if syncErr := syncDirectory(filepath.Dir(stateTarget)); syncErr != nil {
			cleanupRepositoryStage = false
			return fmt.Errorf("publish repository root: %v; rolled-back stages remain at %s and %s because rollback could not be synchronized: %w", publishRepositoryErr, stateStage, repositoryStage, syncErr)
		}
		cleanupStateStage = true
		return fmt.Errorf("publish repository root: %w", publishRepositoryErr)
	}
	cleanupRepositoryStage = false
	if err := syncDirectory(filepath.Dir(repositoryTarget)); err != nil {
		return fmt.Errorf("restored paths remain blocked by completion markers because repository publication could not be synchronized: %w", err)
	}

	if err := os.Remove(filepath.Join(repositoryTarget, pendingRestoreName)); err != nil {
		return fmt.Errorf("restored paths remain blocked by completion markers; follow the interrupted-restore procedure: %w", err)
	}
	if err := syncDirectory(repositoryTarget); err != nil {
		return fmt.Errorf("restored state remains blocked while repository completion is synchronized: %w", err)
	}
	if err := os.Remove(filepath.Join(stateTarget, pendingRestoreName)); err != nil {
		return fmt.Errorf("restored paths remain blocked by a completion marker; follow the interrupted-restore procedure: %w", err)
	}
	if err := syncDirectory(stateTarget); err != nil {
		return fmt.Errorf("restored state was completed but its marker removal could not be synchronized: %w", err)
	}
	return nil
}

func inspectRepository(ctx context.Context, runner commandRunner, repositoryPath string, stored state.Repository) (RepositoryManifest, error) {
	item := RepositoryManifest{
		ID: stored.ID, Name: stored.Name, Description: stored.Description, CreatedAt: stored.CreatedAt,
		AttemptSequence: stored.AttemptSequence,
	}
	refs, err := readRefs(ctx, runner, repositoryPath)
	if err != nil {
		return RepositoryManifest{}, err
	}
	item.Refs = refs
	symbolic, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		item.Head.Symbolic = strings.TrimSpace(string(symbolic.Stdout))
	} else {
		detached, detachedErr := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", "HEAD")
		if detachedErr == nil {
			item.Head.OID = strings.TrimSpace(string(detached.Stdout))
		} else if len(refs) != 0 {
			return RepositoryManifest{}, errors.New("repository HEAD cannot be resolved")
		}
	}
	item.Empty = len(refs) == 0 && item.Head.OID == ""
	return item, nil
}

func readRefs(ctx context.Context, runner commandRunner, repositoryPath string) ([]Ref, error) {
	result, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, string([]byte{0}), 2)
		if len(parts) != 2 {
			return nil, errors.New("Git returned malformed ref data")
		}
		refs = append(refs, Ref{Name: parts[0], OID: parts[1]})
	}
	return refs, nil
}

func restoreRepository(ctx context.Context, runner commandRunner, inputRoot, repositoryStage string, item RepositoryManifest) error {
	repositoryPath := filepath.Join(repositoryStage, item.ID+".git")
	if _, err := runner.Run(ctx, "", nil, "init", "--bare", "--initial-branch=main", repositoryPath); err != nil {
		return err
	}
	if !item.Empty {
		bundlePath := filepath.Join(inputRoot, filepath.FromSlash(item.Bundle))
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "bundle", "verify", bundlePath); err != nil {
			return fmt.Errorf("verify bundle: %w", err)
		}
		heads, err := runner.Run(ctx, "", nil, "bundle", "list-heads", bundlePath)
		if err != nil {
			return err
		}
		listed, bundledHead, err := parseBundleHeads(heads.Stdout)
		if err != nil {
			return err
		}
		if !sameRefs(listed, item.Refs) || (item.Head.OID != "" && bundledHead != item.Head.OID) {
			return errors.New("bundle refs do not match the manifest")
		}
		arguments := []string{"--git-dir", ".", "fetch", "--no-tags", "--no-write-fetch-head", bundlePath}
		for _, ref := range item.Refs {
			arguments = append(arguments, ref.Name+":"+ref.Name)
		}
		if item.Head.OID != "" {
			arguments = append(arguments, "HEAD")
		}
		if _, err := runner.Run(ctx, repositoryPath, nil, arguments...); err != nil {
			return fmt.Errorf("import bundle: %w", err)
		}
	}
	if item.Head.Symbolic != "" {
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "symbolic-ref", "HEAD", item.Head.Symbolic); err != nil {
			return err
		}
	} else if item.Head.OID != "" {
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "update-ref", "--no-deref", "HEAD", item.Head.OID); err != nil {
			return err
		}
	}
	actual, err := readRefs(ctx, runner, repositoryPath)
	if err != nil {
		return err
	}
	if !sameRefs(actual, item.Refs) {
		return errors.New("restored refs do not match the manifest")
	}
	for _, ref := range item.Refs {
		if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "cat-file", "-e", ref.OID+"^{object}"); err != nil {
			return fmt.Errorf("restored object %s is missing: %w", ref.OID, err)
		}
	}
	return nil
}

func checkedInputRoot(input string) (string, error) {
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect backup source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("backup source must be a real directory")
	}
	return canonicalExistingDirectory(absolute, "backup source")
}

func absentTarget(target, label string) (string, error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	parent, err := canonicalExistingDirectory(filepath.Dir(absolute), label+" destination parent")
	if err != nil {
		return "", err
	}
	identity := filepath.Join(parent, filepath.Base(absolute))
	if err := requireAbsent(identity, label); err != nil {
		return "", err
	}
	return filepath.Clean(identity), nil
}

func canonicalExistingDirectory(directory, label string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	identity, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s identity: %w", label, err)
	}
	info, err := os.Stat(identity)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", label)
	}
	return filepath.Clean(identity), nil
}

func requireAbsent(target, label string) error {
	info, err := os.Lstat(target)
	if err == nil {
		if info.IsDir() {
			if _, markerErr := os.Lstat(filepath.Join(target, pendingRestoreName)); markerErr == nil {
				return fmt.Errorf("%s destination contains an incomplete OwnGit restore; preserve it and follow the interrupted-restore procedure", label)
			}
		}
		return fmt.Errorf("%s destination already exists", label)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s destination: %w", label, err)
	}
	return nil
}

func writePendingRestore(root string, pending pendingRestore) error {
	markerPath := filepath.Join(root, pendingRestoreName)
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create incomplete restore marker: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(pending); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := state.ProtectPrivatePath(markerPath, false); err != nil {
		return err
	}
	return nil
}

func syncRegularFile(filePath string) error {
	file, err := os.OpenFile(filePath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

type manifestLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *manifestLimitWriter) Write(content []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, errManifestTooLarge
	}
	allowed := content
	tooLarge := int64(len(content)) > writer.remaining
	if tooLarge {
		allowed = content[:writer.remaining]
	}
	written, err := writer.writer.Write(allowed)
	writer.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	if written != len(allowed) {
		return written, io.ErrShortWrite
	}
	if tooLarge {
		return written, errManifestTooLarge
	}
	return written, nil
}

func writeManifest(destination io.Writer, manifest Manifest, maximum int64) error {
	limited := &manifestLimitWriter{writer: destination, remaining: maximum}
	encoder := json.NewEncoder(limited)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	return nil
}

func readManifestContent(source io.Reader, maximum int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(source, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, errManifestTooLarge
	}
	return content, nil
}

func readManifest(manifestPath string) (Manifest, error) {
	if err := requireRegularFile(manifestPath); err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	content, readErr := readManifestContent(file, maximumManifest)
	closeErr := file.Close()
	if readErr != nil {
		return Manifest{}, readErr
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	// Probe the version before strict decoding so a backup written by a newer
	// OwnGit is rejected with a clear message instead of an unknown-field
	// error, and never silently loses new records.
	// Direct-review records are named here only to refuse them clearly.
	var probe struct {
		Format                   string          `json:"format"`
		Version                  int             `json:"version"`
		DirectReviewSettings     json.RawMessage `json:"direct_review_settings"`
		DirectReviewTaskContexts json.RawMessage `json:"direct_review_task_contexts"`
		DirectReviewRequests     json.RawMessage `json:"direct_review_requests"`
	}
	if err := json.Unmarshal(content, &probe); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if probe.Format != backupFormat {
		return Manifest{}, errors.New("unsupported backup format")
	}
	if err := validateBackupVersion(probe.Version); err != nil {
		return Manifest{}, err
	}
	if probe.DirectReviewSettings != nil || probe.DirectReviewTaskContexts != nil || probe.DirectReviewRequests != nil {
		return Manifest{}, errDirectReviewManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("backup manifest contains trailing data")
	}
	return manifest, nil
}

func addPullRequestState(manifest *Manifest, snapshot state.RecoveryState) {
	for _, record := range snapshot.PullRequests {
		manifest.PullRequests = append(manifest.PullRequests, PullRequestManifest{
			RepositoryID: record.RepositoryID, Number: record.Number, Title: record.Title,
			SourceBranch: record.SourceBranch, TargetBranch: record.TargetBranch, Status: record.Status,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
			MergeSourceOID: record.MergeSourceOID, MergeTargetOID: record.MergeTargetOID,
			MergeOID: record.MergeOID, MergeReceipt: record.MergeReceipt, MergedAt: record.MergedAt,
		})
	}
	for _, revision := range snapshot.PullRequestRevisions {
		manifest.PullRequestRevisions = append(manifest.PullRequestRevisions, PullRequestRevisionManifest{
			RepositoryID: revision.RepositoryID, PullRequestNumber: revision.PullRequestNumber,
			SourceOID: revision.SourceOID, TargetOID: revision.TargetOID, RecordedAt: revision.RecordedAt,
		})
	}
	for _, review := range snapshot.PullRequestReviews {
		manifest.PullRequestReviews = append(manifest.PullRequestReviews, PullRequestReviewManifest{
			RepositoryID: review.RepositoryID, PullRequestNumber: review.PullRequestNumber, Sequence: review.Sequence,
			SourceOID: review.SourceOID, TargetOID: review.TargetOID, Status: review.Status,
			ReviewerLabel: review.ReviewerLabel, Provenance: review.Provenance, ReviewEventID: review.ReviewEventID, CreatedAt: review.CreatedAt,
		})
	}
	for _, intent := range snapshot.PullRequestMergeIntents {
		manifest.PullRequestMergeIntents = append(manifest.PullRequestMergeIntents, PullRequestMergeManifest{
			RepositoryID: intent.RepositoryID, PullRequestNumber: intent.PullRequestNumber,
			SourceOID: intent.SourceOID, TargetOID: intent.TargetOID, Mode: intent.Mode,
			TreeOID: intent.TreeOID, ResultOID: intent.ResultOID, ReceiptRef: intent.ReceiptRef,
			Status: intent.Status, CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
}

func addCheckState(manifest *Manifest, snapshot state.RecoveryState) {
	for _, task := range snapshot.Tasks {
		manifest.Tasks = append(manifest.Tasks, TaskManifest{
			ID: task.ID, RepositoryID: task.RepositoryID, Title: task.Title,
			CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		})
	}
	for _, configuration := range snapshot.CheckConfigurations {
		item := CheckConfigurationManifest{
			RepositoryID: configuration.RepositoryID, Version: configuration.Version,
			ConfigHash: configuration.ConfigHash, CreatedAt: configuration.CreatedAt,
		}
		for _, check := range configuration.Checks {
			item.Checks = append(item.Checks, CheckDefinitionManifest{Name: check.Name, Command: check.Command})
		}
		manifest.CheckConfigurations = append(manifest.CheckConfigurations, item)
	}
	for _, cycle := range snapshot.CheckCycles {
		manifest.CheckCycles = append(manifest.CheckCycles, CheckCycleManifest{
			ID: cycle.ID, TaskID: cycle.TaskID, RepositoryID: cycle.RepositoryID,
			Sequence: cycle.Sequence, ReservedAt: cycle.ReservedAt, ReservedAfterSequence: cycle.ReservedAfterSequence,
		})
	}
	for _, attempt := range snapshot.CheckAttempts {
		manifest.CheckAttempts = append(manifest.CheckAttempts, CheckAttemptManifest{
			ID: attempt.ID, TaskID: attempt.TaskID, RepositoryID: attempt.RepositoryID, RevisionOID: attempt.RevisionOID,
			WorktreeState: attempt.WorktreeState, SubmittedWorktree: attempt.SubmittedWorktreeState,
			ConfigurationVersion: attempt.ConfigurationVersion, Status: attempt.Status,
			ExitCode: attempt.ExitCode, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt, DurationMS: attempt.DurationMS,
			Summary: attempt.Summary, Protection: attempt.Protection, ExecutionScope: attempt.ExecutionScope,
			CredentialID: attempt.CredentialID, JobID: attempt.JobID, TimeoutMS: attempt.TimeoutMS, OutputLimitBytes: attempt.OutputLimitBytes,
			LogID: attempt.LogID, LogExpiresAt: attempt.LogExpiresAt, LogTruncated: attempt.LogTruncated,
			LogError: attempt.LogError, CreatedAt: attempt.CreatedAt,
			Sequence: attempt.Sequence, CycleID: attempt.CycleID, RegistrationDigest: attempt.RegistrationDigest,
			CompletionDigest: attempt.CompletionDigest, SubmittedLogDigest: attempt.SubmittedLogDigest,
			SubmittedTruncated: attempt.SubmittedTruncated, SubmittedCancelled: attempt.SubmittedCancelled,
			LogDigest: attempt.LogDigest,
		})
	}
	for _, result := range snapshot.CheckResults {
		manifest.CheckResults = append(manifest.CheckResults, CheckResultManifest{
			AttemptID: result.AttemptID, Position: result.Position, Name: result.Name, Command: result.Command,
			Status: result.Status, ExitCode: result.ExitCode, DurationMS: result.DurationMS,
			OutputExcerpt: result.OutputExcerpt, Truncated: result.Truncated, CleanupError: result.CleanupError,
		})
	}
	for _, policy := range snapshot.CheckPolicies {
		manifest.CheckPolicies = append(manifest.CheckPolicies, CheckPolicyManifest{
			RepositoryID: policy.RepositoryID, PolicyVersion: policy.Version, PolicyDigest: policy.Digest,
			Executor: policy.Executor, AllowedEvents: policy.AllowedEvents, MaxTimeoutMS: policy.MaxTimeoutMS,
			MaxOutputLimitBytes: policy.MaxOutputLimitBytes, QueueLimit: policy.QueueLimit, MaxActiveJobs: policy.MaxActiveJobs,
			MaxLeaseMS: policy.MaxLeaseMS, Execution: policy.Execution, ConsentVersion: policy.ConsentVersion, ConsentDigest: policy.ConsentDigest,
			RunnerGeneration: policy.RunnerGeneration, CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
		})
	}
	for _, job := range snapshot.CheckJobs {
		manifest.CheckJobs = append(manifest.CheckJobs, CheckJobManifest{
			ID: job.ID, RepositoryID: job.RepositoryID, TaskID: job.TaskID, Trigger: job.Trigger, EventKey: job.EventKey,
			SourceOID: job.SourceOID, BaseOID: job.BaseOID, PullRequestNumber: job.PullRequestNumber, TriggerRef: job.TriggerRef,
			WorkflowPath: job.WorkflowPath, WorkflowOID: job.WorkflowOID, WorkflowDigest: job.WorkflowDigest,
			ConfigurationVersion: job.ConfigurationVersion, Executor: job.Executor, PolicyVersion: job.PolicyVersion,
			ConsentVersion: job.ConsentVersion,
			Limits:         CheckJobLimitsManifest{TimeoutMS: job.Limits.TimeoutMS, OutputLimitBytes: job.Limits.OutputLimitBytes},
			Execution:      job.Execution,
			DedupDigest:    job.DedupDigest, RerunRoot: job.RerunRoot, RerunGeneration: job.RerunGeneration, Status: job.Status,
			AttemptID: job.AttemptID, LeaseID: job.LeaseID, LeaseExpiresAt: job.LeaseExpiresAt, CredentialID: job.CredentialID,
			CredentialGeneration: job.CredentialGeneration, CredentialRole: job.CredentialRole, Protection: job.Protection, AdmittedAt: job.AdmittedAt,
			ClaimedAt: job.ClaimedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, LeaseLostAt: job.LeaseLostAt,
			CancelRequestedAt: job.CancelRequestedAt, InterruptedAt: job.InterruptedAt, Summary: job.Summary,
		})
	}
}

func recoveryState(manifest Manifest) state.RecoveryState {
	snapshot := state.RecoveryState{
		AccessMode: manifest.AccessMode, AccessPasswordHash: manifest.AccessHash, AdminPasswordHash: manifest.AdminHash,
	}
	attachImportState(&snapshot, manifest)
	for _, record := range manifest.PullRequests {
		snapshot.PullRequests = append(snapshot.PullRequests, state.PullRequest{
			RepositoryID: record.RepositoryID, Number: record.Number, Title: record.Title,
			SourceBranch: record.SourceBranch, TargetBranch: record.TargetBranch, Status: record.Status,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
			MergeSourceOID: record.MergeSourceOID, MergeTargetOID: record.MergeTargetOID,
			MergeOID: record.MergeOID, MergeReceipt: record.MergeReceipt, MergedAt: record.MergedAt,
		})
	}
	for _, revision := range manifest.PullRequestRevisions {
		snapshot.PullRequestRevisions = append(snapshot.PullRequestRevisions, state.PullRequestRevision{
			RepositoryID: revision.RepositoryID, PullRequestNumber: revision.PullRequestNumber,
			SourceOID: revision.SourceOID, TargetOID: revision.TargetOID, RecordedAt: revision.RecordedAt,
		})
	}
	for _, review := range manifest.PullRequestReviews {
		snapshot.PullRequestReviews = append(snapshot.PullRequestReviews, state.PullRequestReview{
			RepositoryID: review.RepositoryID, PullRequestNumber: review.PullRequestNumber, Sequence: review.Sequence,
			SourceOID: review.SourceOID, TargetOID: review.TargetOID, Status: review.Status,
			ReviewerLabel: review.ReviewerLabel, Provenance: review.Provenance, ReviewEventID: review.ReviewEventID, CreatedAt: review.CreatedAt,
		})
	}
	for _, intent := range manifest.PullRequestMergeIntents {
		snapshot.PullRequestMergeIntents = append(snapshot.PullRequestMergeIntents, state.PullRequestMergeIntent{
			RepositoryID: intent.RepositoryID, PullRequestNumber: intent.PullRequestNumber,
			SourceOID: intent.SourceOID, TargetOID: intent.TargetOID, Mode: intent.Mode,
			TreeOID: intent.TreeOID, ResultOID: intent.ResultOID, ReceiptRef: intent.ReceiptRef,
			Status: intent.Status, CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
	for _, task := range manifest.Tasks {
		snapshot.Tasks = append(snapshot.Tasks, state.RecoveryTask{
			ID: task.ID, RepositoryID: task.RepositoryID, Title: task.Title,
			CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		})
	}
	for _, configuration := range manifest.CheckConfigurations {
		item := state.CheckConfiguration{
			RepositoryID: configuration.RepositoryID, Version: configuration.Version,
			ConfigHash: configuration.ConfigHash, CreatedAt: configuration.CreatedAt,
		}
		for _, check := range configuration.Checks {
			item.Checks = append(item.Checks, state.CheckDefinition{Name: check.Name, Command: check.Command})
		}
		snapshot.CheckConfigurations = append(snapshot.CheckConfigurations, item)
	}
	for _, cycle := range manifest.CheckCycles {
		snapshot.CheckCycles = append(snapshot.CheckCycles, state.RecoveryCheckCycle{
			ID: cycle.ID, TaskID: cycle.TaskID, RepositoryID: cycle.RepositoryID,
			Sequence: cycle.Sequence, ReservedAt: cycle.ReservedAt, ReservedAfterSequence: cycle.ReservedAfterSequence,
		})
	}
	for _, attempt := range manifest.CheckAttempts {
		protection, scope := attempt.Protection, attempt.ExecutionScope
		if protection == "" {
			protection = state.ProtectionUnknown
		}
		if scope == "" {
			scope = state.ExecutionScopeInherited
		}
		snapshot.CheckAttempts = append(snapshot.CheckAttempts, state.CheckAttempt{
			ID: attempt.ID, TaskID: attempt.TaskID, RepositoryID: attempt.RepositoryID, RevisionOID: attempt.RevisionOID,
			WorktreeState: attempt.WorktreeState, SubmittedWorktreeState: attempt.SubmittedWorktree,
			ConfigurationVersion: attempt.ConfigurationVersion, Status: attempt.Status,
			ExitCode: attempt.ExitCode, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt, DurationMS: attempt.DurationMS,
			Summary: attempt.Summary, Protection: protection, ExecutionScope: scope,
			CredentialID: attempt.CredentialID, JobID: attempt.JobID, TimeoutMS: attempt.TimeoutMS, OutputLimitBytes: attempt.OutputLimitBytes,
			LogID: attempt.LogID, LogExpiresAt: attempt.LogExpiresAt, LogTruncated: attempt.LogTruncated,
			LogError: attempt.LogError, CreatedAt: attempt.CreatedAt,
			Sequence: attempt.Sequence, CycleID: attempt.CycleID, RegistrationDigest: attempt.RegistrationDigest,
			CompletionDigest: attempt.CompletionDigest, SubmittedLogDigest: attempt.SubmittedLogDigest,
			SubmittedTruncated: attempt.SubmittedTruncated, SubmittedCancelled: attempt.SubmittedCancelled,
			LogDigest: attempt.LogDigest,
		})
	}
	for _, result := range manifest.CheckResults {
		snapshot.CheckResults = append(snapshot.CheckResults, state.CheckResultRecord{
			AttemptID: result.AttemptID,
			CheckResult: state.CheckResult{
				Position: result.Position, Name: result.Name, Command: result.Command, Status: result.Status,
				ExitCode: result.ExitCode, DurationMS: result.DurationMS, OutputExcerpt: result.OutputExcerpt,
				Truncated: result.Truncated, CleanupError: result.CleanupError,
			},
		})
	}
	for _, policy := range manifest.CheckPolicies {
		snapshot.CheckPolicies = append(snapshot.CheckPolicies, state.CheckPolicy{
			RepositoryID: policy.RepositoryID, Version: policy.PolicyVersion, Digest: policy.PolicyDigest,
			Executor: policy.Executor, AllowedEvents: policy.AllowedEvents, MaxTimeoutMS: policy.MaxTimeoutMS,
			MaxOutputLimitBytes: policy.MaxOutputLimitBytes, QueueLimit: policy.QueueLimit, MaxActiveJobs: policy.MaxActiveJobs,
			MaxLeaseMS: policy.MaxLeaseMS, Execution: policy.Execution, ConsentVersion: policy.ConsentVersion, ConsentDigest: policy.ConsentDigest,
			RunnerGeneration: policy.RunnerGeneration, CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
		})
	}
	for _, job := range manifest.CheckJobs {
		snapshot.CheckJobs = append(snapshot.CheckJobs, state.CheckJob{
			ID: job.ID, RepositoryID: job.RepositoryID, TaskID: job.TaskID, Trigger: job.Trigger, EventKey: job.EventKey,
			SourceOID: job.SourceOID, BaseOID: job.BaseOID, PullRequestNumber: job.PullRequestNumber, TriggerRef: job.TriggerRef,
			WorkflowPath: job.WorkflowPath, WorkflowOID: job.WorkflowOID, WorkflowDigest: job.WorkflowDigest,
			ConfigurationVersion: job.ConfigurationVersion, Executor: job.Executor, PolicyVersion: job.PolicyVersion,
			ConsentVersion: job.ConsentVersion,
			Limits:         state.CheckJobLimits{TimeoutMS: job.Limits.TimeoutMS, OutputLimitBytes: job.Limits.OutputLimitBytes},
			Execution:      job.Execution,
			DedupDigest:    job.DedupDigest, RerunRoot: job.RerunRoot, RerunGeneration: job.RerunGeneration, Status: job.Status,
			AttemptID: job.AttemptID, LeaseID: job.LeaseID, LeaseExpiresAt: job.LeaseExpiresAt, CredentialID: job.CredentialID,
			CredentialGeneration: job.CredentialGeneration, CredentialRole: job.CredentialRole, Protection: job.Protection, AdmittedAt: job.AdmittedAt,
			ClaimedAt: job.ClaimedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, LeaseLostAt: job.LeaseLostAt,
			CancelRequestedAt: job.CancelRequestedAt, InterruptedAt: job.InterruptedAt, Summary: job.Summary,
		})
	}
	return snapshot
}

// validateBackupVersion accepts the committed baseline formats and the
// current format. Every other version below the current one was written only
// by unreleased development builds.
func validateBackupVersion(version int) error {
	switch {
	case version == legacyBackupVersion || version == pullRequestBackupVersion || version == backupVersion:
		return nil
	case version > backupVersion:
		return fmt.Errorf("unsupported backup version %d: this build supports versions 1, 2, and %d", version, backupVersion)
	default:
		return fmt.Errorf("backup uses the unreleased development format %d; this build supports versions 1, 2, and %d", version, backupVersion)
	}
}

func validateManifest(manifest Manifest) error {
	if manifest.Format != backupFormat {
		return errors.New("unsupported backup format")
	}
	if err := validateBackupVersion(manifest.Version); err != nil {
		return err
	}
	if manifest.CreatedAt.IsZero() || auth.ValidatePasswordHash(manifest.AdminHash) != nil {
		return errors.New("backup manifest metadata is incomplete")
	}
	if manifest.AccessMode != "open" && manifest.AccessMode != "password" {
		return errors.New("backup access mode is invalid")
	}
	if manifest.AccessMode == "password" && auth.ValidatePasswordHash(manifest.AccessHash) != nil {
		return errors.New("backup access password hash is invalid")
	}
	if manifest.AccessMode == "open" && manifest.AccessHash != "" {
		return errors.New("open backup unexpectedly contains an access password hash")
	}
	ids := make(map[string]bool)
	bundles := make(map[string]bool)
	repositoryRefs := make(map[string]map[string]string)
	for _, item := range manifest.Repositories {
		if repository.ValidateID(item.ID) != nil || item.Name == "" || len(item.Name) > 100 || len(item.Description) > 500 || ids[item.ID] {
			return errors.New("backup repository metadata is invalid or not portable")
		}
		ids[item.ID] = true
		if item.CreatedAt.IsZero() {
			return errors.New("backup repository creation time is invalid")
		}
		if item.Head.Symbolic != "" && item.Head.OID != "" {
			return errors.New("backup HEAD has conflicting forms")
		}
		if item.Head.Symbolic != "" && (!strings.HasPrefix(item.Head.Symbolic, "refs/heads/") || !validRefName(item.Head.Symbolic)) {
			return errors.New("backup symbolic HEAD is invalid")
		}
		if item.Head.OID != "" && !validOID(item.Head.OID) {
			return errors.New("backup detached HEAD is invalid")
		}
		refNames := make(map[string]bool)
		repositoryRefs[item.ID] = make(map[string]string)
		for _, ref := range item.Refs {
			if !validRefName(ref.Name) || !validOID(ref.OID) || refNames[ref.Name] {
				return errors.New("backup ref list is invalid")
			}
			refNames[ref.Name] = true
			repositoryRefs[item.ID][ref.Name] = ref.OID
		}
		if item.Empty {
			if len(item.Refs) != 0 || item.Head.OID != "" || item.Bundle != "" || item.SHA256 != "" {
				return errors.New("empty repository backup contains bundle data")
			}
			continue
		}
		expectedBundle := path.Join("repositories", item.ID+".bundle")
		if (len(item.Refs) == 0 && item.Head.OID == "") || item.Bundle != expectedBundle || !validSHA256(item.SHA256) || bundles[item.Bundle] {
			return errors.New("repository bundle metadata is invalid")
		}
		bundles[item.Bundle] = true
	}
	if manifest.Version == legacyBackupVersion {
		if len(manifest.PullRequests) != 0 || len(manifest.PullRequestRevisions) != 0 || len(manifest.PullRequestReviews) != 0 || len(manifest.PullRequestMergeIntents) != 0 {
			return errors.New("version 1 backup contains unsupported pull request metadata")
		}
	}
	if manifest.Version < backupVersion {
		if len(manifest.Tasks) != 0 || len(manifest.CheckConfigurations) != 0 || len(manifest.CheckCycles) != 0 || len(manifest.CheckAttempts) != 0 || len(manifest.CheckResults) != 0 || len(manifest.CheckPolicies) != 0 || len(manifest.CheckJobs) != 0 {
			return fmt.Errorf("version %d backup contains unsupported check metadata", manifest.Version)
		}
		for _, review := range manifest.PullRequestReviews {
			if review.ReviewEventID != "" {
				return fmt.Errorf("version %d backup contains an unsupported review event identity", manifest.Version)
			}
		}
		if len(manifest.ImportSources) != 0 || len(manifest.ImportRuns) != 0 || manifest.ImportRunOrderKnown || manifest.ImportHEADOwnershipVersion != 0 || len(manifest.ImportObservations) != 0 || len(manifest.ImportIntents) != 0 {
			return fmt.Errorf("version %d backup contains unsupported import metadata", manifest.Version)
		}
	}
	snapshot := recoveryState(manifest)
	for _, item := range manifest.Repositories {
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{
			ID: item.ID, Name: item.Name, Description: item.Description, CreatedAt: item.CreatedAt,
			AttemptSequence: item.AttemptSequence,
		})
	}
	if err := state.ValidatePullRequestRecovery(snapshot); err != nil {
		return fmt.Errorf("backup pull request metadata is invalid: %w", err)
	}
	for _, revision := range snapshot.PullRequestRevisions {
		sourceRef, targetRef := pullrequest.RevisionRefNames(revision.PullRequestNumber, revision.SourceOID, revision.TargetOID)
		refs := repositoryRefs[revision.RepositoryID]
		if refs[sourceRef] != revision.SourceOID || refs[targetRef] != revision.TargetOID {
			return errors.New("backup is missing a protected pull request revision ref")
		}
	}
	for _, intent := range snapshot.PullRequestMergeIntents {
		refs := repositoryRefs[intent.RepositoryID]
		if intent.Mode == "merge_commit" && intent.Status != state.MergeIntentPreparing {
			treeRef := pullrequest.MergeTreeRef(intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
			if refs[treeRef] != intent.TreeOID {
				return errors.New("backup is missing its protected merge tree ref")
			}
		}
		if intent.Status == state.MergeIntentReady || intent.Status == state.MergeIntentComplete {
			resultRef := pullrequest.MergeResultRef(intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
			if refs[resultRef] != intent.ResultOID {
				return errors.New("backup is missing its protected merge result ref")
			}
		}
		receiptOID, exists := refs[intent.ReceiptRef]
		if exists && (intent.ResultOID == "" || receiptOID != intent.ResultOID) {
			return errors.New("backup merge receipt does not match its durable intent")
		}
		if intent.Status == state.MergeIntentComplete && !exists {
			return errors.New("completed backup merge is missing its protected receipt")
		}
	}
	if err := state.ValidateCheckRecovery(snapshot); err != nil {
		return fmt.Errorf("backup check metadata is invalid: %w", err)
	}
	if err := validateImportManifest(manifest); err != nil {
		return err
	}
	return nil
}

func parseBundleHeads(output []byte) ([]Ref, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var refs []Ref
	var head string
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, "", errors.New("Git returned malformed bundle heads")
		}
		if fields[1] == "HEAD" {
			head = fields[0]
			continue
		}
		refs = append(refs, Ref{Name: fields[1], OID: fields[0]})
	}
	return refs, head, scanner.Err()
}

func sameRefs(left, right []Ref) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]Ref(nil), left...)
	rightCopy := append([]Ref(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i].Name < leftCopy[j].Name })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i].Name < rightCopy[j].Name })
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func requireRegularFile(filePath string) error {
	info, err := os.Lstat(filePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validRefName(name string) bool {
	return strings.HasPrefix(name, "refs/") && !strings.ContainsAny(name, "\x00\r\n \\~^:?*[") && !strings.Contains(name, "..") && !strings.Contains(name, "@{") && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".lock")
}

func validOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, character := range oid {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func randomSuffix() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	leftKey := pathComparisonKey(left)
	rightKey := pathComparisonKey(right)
	if pathContains(leftKey, rightKey) || pathContains(rightKey, leftKey) {
		return true
	}
	if existingPathContains(left, right) || existingPathContains(right, left) {
		return true
	}
	leftAncestor, leftSuffix, leftOK := nearestExistingPath(left)
	rightAncestor, rightSuffix, rightOK := nearestExistingPath(right)
	return leftOK && rightOK && os.SameFile(leftAncestor, rightAncestor) && componentPathsOverlap(leftSuffix, rightSuffix)
}

func pathComparisonKey(value string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func pathContains(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func existingPathContains(parent, candidate string) bool {
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return false
	}
	current := candidate
	for {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(parentInfo, info) {
			return true
		}
		next := filepath.Dir(current)
		if next == current {
			return false
		}
		current = next
	}
}

func nearestExistingPath(value string) (os.FileInfo, []string, bool) {
	current := value
	var suffix []string
	for {
		info, err := os.Stat(current)
		if err == nil {
			return info, suffix, true
		}
		if !os.IsNotExist(err) {
			return nil, nil, false
		}
		next := filepath.Dir(current)
		if next == current {
			return nil, nil, false
		}
		suffix = append([]string{pathComparisonKey(filepath.Base(current))}, suffix...)
		current = next
	}
}

func componentPathsOverlap(left, right []string) bool {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

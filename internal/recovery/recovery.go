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
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

var (
	errManifestTooLarge = errors.New("backup manifest is larger than the 64 MiB that backup version 10 and older allow")
	errManifestStart    = errors.New("backup manifest does not start with its format and version")
)

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
	// checkBackupVersion added checks, jobs, imports and review event
	// identities; the 1.0 releases wrote it. Versions 3 through 8 were
	// written only by unreleased development builds and are refused.
	checkBackupVersion = 9
	// closedPullRequestBackupVersion added closed pull requests and merges
	// recorded as already up to date. Releases 1.0.3 to 1.1.2 wrote it and
	// restore it, so a backup without newer records is still written in it.
	closedPullRequestBackupVersion = 10
	// backupVersion is the current format. It adds pull request
	// descriptions, edits and review notes, actors, repository names and
	// policies, and import refresh behaviour (format11Content).
	backupVersion = 11
	// manifestLimit is the most a backup holds: 1 GiB of OwnGit records in
	// its manifest, repositories not counted. Backup refuses a larger state
	// rather than cutting it short, and restore refuses a larger file, so
	// the memory a restore of any supplied file needs is what a real state
	// of that size needs.
	manifestLimit = 1 << 30
	// format10ManifestLimit is the manifest size that readers of version 10
	// and older accept.
	format10ManifestLimit = 64 << 20
	// maxManifestStringBytes is the longest string a manifest holds: the
	// longest text a record holds (an import receipt) with every byte
	// escaped, which takes at most six bytes.
	maxManifestStringBytes = 6 * state.MaxImportIntentJSONBytes
	pendingRestoreName     = state.IncompleteRestoreMarkerName
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
	// Since format 11: the names of a renamed repository and its own
	// policies, omitted when it was never renamed or keeps every default.
	Names  []RepositoryNameManifest  `json:"names,omitempty"`
	Policy *RepositoryPolicyManifest `json:"policy,omitempty"`
}

type RepositoryNameManifest struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"created_at"`
	AliasUntil *time.Time `json:"alias_until,omitempty"`
}

type RepositoryPolicyManifest struct {
	RetainHistory        *bool     `json:"retain_history,omitempty"`
	ProtectDefaultBranch bool      `json:"protect_default_branch,omitempty"`
	ExtraRefPrefixes     []string  `json:"extra_ref_prefixes,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
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
	// Since format 11.
	Body         string      `json:"body,omitempty"`
	EditRevision int64       `json:"edit_revision,omitempty"`
	EditedAt     *time.Time  `json:"edited_at,omitempty"`
	CreatedBy    state.Actor `json:"created_by,omitzero"`
	EditedBy     state.Actor `json:"edited_by,omitzero"`
	MergedBy     state.Actor `json:"merged_by,omitzero"`
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
	// Since format 11.
	Note  string      `json:"note,omitempty"`
	Actor state.Actor `json:"actor,omitzero"`
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
	return create(ctx, store, manager, manager.Git, output, manifestLimit)
}

// create writes a backup whose manifest holds at most limit bytes.
func create(ctx context.Context, store *state.Store, manager *repository.Manager, runner commandRunner, output string, limit int64) error {
	// The output is held until create returns; see state.Destination for
	// why its stage is then used by path.
	destination, err := state.OpenDestination(output)
	if err != nil {
		return err
	}
	defer destination.Close()
	absolute := destination.Path
	if err := requireAbsent(absolute, "backup"); err != nil {
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
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		return err
	}
	manifest := Manifest{
		Format: backupFormat, CreatedAt: time.Now().UTC(),
		AccessMode: snapshot.AccessMode, AccessHash: snapshot.AccessPasswordHash, AdminHash: snapshot.AdminPasswordHash,
	}
	addPullRequestState(&manifest, snapshot)
	addCheckState(&manifest, snapshot)
	addImportState(&manifest, snapshot)
	repositoryPaths := make([]string, 0, len(snapshot.Repositories))
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
			// Replaced by the bundle's digest, which has the same length,
			// so the size checked below is the size written.
			item.SHA256 = strings.Repeat("0", sha256.Size*2)
		}
		manifest.Repositories = append(manifest.Repositories, item)
		repositoryPaths = append(repositoryPaths, repositoryPath)
	}
	addRepositoryRecords(&manifest, snapshot)
	// A state too large for a backup is refused before anything is written.
	version, err := backupManifestVersion(manifest, format10ManifestLimit, limit)
	if err != nil {
		return err
	}

	parent := filepath.Dir(absolute)
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	stage, err := destination.CreateStage("." + filepath.Base(absolute) + ".owngit-backup-" + suffix)
	if err != nil {
		return fmt.Errorf("create staged backup: %w", err)
	}
	published := false
	defer func() {
		if !published {
			destination.ReleaseStage()
			_ = os.RemoveAll(stage)
		}
	}()
	bundles := filepath.Join(stage, "repositories")
	if err := os.Mkdir(bundles, 0o700); err != nil {
		return err
	}
	for index := range manifest.Repositories {
		item := &manifest.Repositories[index]
		if item.Empty {
			continue
		}
		bundlePath := filepath.Join(stage, filepath.FromSlash(item.Bundle))
		arguments := []string{"--git-dir", ".", "bundle", "create", bundlePath, "--all"}
		if item.Head.OID != "" {
			arguments = append(arguments, "HEAD")
		}
		if _, err := runner.Run(ctx, repositoryPaths[index], nil, arguments...); err != nil {
			return fmt.Errorf("bundle repository %q: %w", item.ID, err)
		}
		if err := syncRegularFile(bundlePath); err != nil {
			return err
		}
		if item.SHA256, err = fileSHA256(bundlePath); err != nil {
			return err
		}
	}
	manifest.Version = version
	if err := validateManifest(manifest); err != nil {
		return fmt.Errorf("validate completed backup manifest: %w", err)
	}
	manifestPath := filepath.Join(stage, manifestName)
	file, err := state.CreatePrivateFile(manifestPath)
	if err != nil {
		return err
	}
	if err := writeManifest(file, manifest); err != nil {
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
	destination.ReleaseStage()
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
	// rehearsal, set by Verify, is told what the backup holds and how each
	// repository's checks ended.
	rehearsal *Verification
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
	// Both destinations are held until restore returns; see
	// state.Destination for why their stages are then used by path.
	stateDestination, err := state.OpenStateDestination(stateDirectory)
	if err != nil {
		return err
	}
	defer stateDestination.Close()
	stateTarget := stateDestination.Path
	if err := requireAbsent(stateTarget, "state"); err != nil {
		return err
	}
	repositoryDestination, err := state.OpenDestination(repositoryRoot)
	if err != nil {
		return err
	}
	defer repositoryDestination.Close()
	repositoryTarget := repositoryDestination.Path
	if err := requireAbsent(repositoryTarget, "repository"); err != nil {
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
	// Only a valid manifest is reported: its repository IDs are then
	// portable names, safe to show.
	rehearsal := operations.rehearsal
	rehearsal.begin(manifest)
	// Every repository is checked, and each failure is named, before
	// restore stops.
	var failures []error
	for index, item := range manifest.Repositories {
		if err := checkBundleFile(inputRoot, item); err != nil {
			rehearsal.record(index, err)
			failures = append(failures, repositoryFailure(item.ID, err))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	// A stage is removed only once it was created here.
	var stateStage, repositoryStage string
	cleanupStateStage := true
	cleanupRepositoryStage := true
	defer func() {
		if cleanupStateStage && stateStage != "" {
			stateDestination.ReleaseStage()
			_ = os.RemoveAll(stateStage)
		}
		if cleanupRepositoryStage && repositoryStage != "" {
			repositoryDestination.ReleaseStage()
			_ = os.RemoveAll(repositoryStage)
		}
	}()
	if stateStage, err = stateDestination.CreateStage(filepath.Base(stateTarget) + ".owngit-restore-" + suffix); err != nil {
		return fmt.Errorf("create staged state: %w", err)
	}
	if repositoryStage, err = repositoryDestination.CreateStage(filepath.Base(repositoryTarget) + ".owngit-restore-" + suffix); err != nil {
		return fmt.Errorf("create staged repository root: %w", err)
	}
	runner, err := gitexec.New(gitPath, filepath.Join(stateStage, "runtime"))
	if err != nil {
		return err
	}
	for index, item := range manifest.Repositories {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := restoreRepository(ctx, runner, inputRoot, repositoryStage, item)
		rehearsal.record(index, err)
		if err != nil {
			failures = append(failures, repositoryFailure(item.ID, err))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}

	store, err := operations.openState(ctx, stateStage)
	if err != nil {
		return err
	}
	snapshot := recoveryState(manifest)
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
	stateDestination.ReleaseStage()
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
		repositoryDestination.ReleaseStage()
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

// repositoryFailure names the repository whose restore check failed.
func repositoryFailure(id string, err error) error {
	return fmt.Errorf("validate and restore repository %q: %w", id, err)
}

// checkBundleFile checks that the bundle of item is a regular file with the
// digest the manifest records.
func checkBundleFile(inputRoot string, item RepositoryManifest) error {
	if item.Empty {
		return nil
	}
	bundlePath := filepath.Join(inputRoot, filepath.FromSlash(item.Bundle))
	if err := requireRegularFile(bundlePath); err != nil {
		return fmt.Errorf("inspect bundle: %w", err)
	}
	digest, err := fileSHA256(bundlePath)
	if err != nil {
		return err
	}
	if digest != item.SHA256 {
		return errors.New("bundle checksum mismatch: the bundle is not the one the manifest records")
	}
	return nil
}

// restoreRepository restores item from its bundle into a new repository in
// repositoryStage and checks it: the bundle is complete, the refs and HEAD
// are the ones the manifest records, and git fsck finds every object that
// they reach. Git computed each object's name from its content when it
// indexed the bundle, so fsck checks only that the objects connect, as
// import verification does; a full fsck would also refuse history that
// OwnGit accepts on push and import, such as a commit with a malformed time
// zone.
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
	if _, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "fsck", "--connectivity-only", "--no-progress", "--no-dangling"); err != nil {
		return fmt.Errorf("git fsck: %w", err)
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
	file, err := state.CreatePrivateFile(markerPath)
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
	return nil
}

func syncRegularFile(filePath string) error {
	file, err := os.OpenFile(filePath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := state.ProtectPrivateHandle(file, false); err != nil {
		file.Close()
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

// backupManifestVersion chooses the oldest format whose readers accept
// manifest: version 10, which releases 1.0.3 to 1.1.2 restore, when it holds
// no record that only version 11 holds and fits format10Limit; version 11
// otherwise. So every backup made before an upgrade that the earlier release
// could have made itself stays restorable by that release. Both version
// numbers have two digits, so the size does not depend on the choice. A
// manifest whose cost (manifestBudget) passes limit is refused, never cut
// short, so every backup written can be restored.
func backupManifestVersion(manifest Manifest, format10Limit, limit int64) (int, error) {
	manifest.Version = backupVersion
	var counter manifestCounter
	if err := writeManifest(&counter, manifest); err != nil {
		return 0, err
	}
	if cost := counter.charged + recordsCost(reflect.ValueOf(manifest)); cost > limit {
		return 0, fmt.Errorf("cannot back up this state: its OwnGit records take %d MiB, and a backup holds at most %d MiB of them (repositories are not counted)", (cost+1<<20-1)>>20, limit>>20)
	}
	if format11Content(manifest) == "" && counter.written <= format10Limit {
		return closedPullRequestBackupVersion, nil
	}
	return backupVersion, nil
}

// manifestCounter counts the bytes of a written manifest and the bytes that
// restore charges for them.
type manifestCounter struct {
	text             manifestText
	written, charged int64
}

func (counter *manifestCounter) Write(content []byte) (int, error) {
	for _, character := range content {
		_, charged, err := counter.text.next(character)
		if err != nil {
			return 0, err
		}
		if charged {
			counter.charged++
		}
	}
	counter.written += int64(len(content))
	return len(content), nil
}

// writeManifest writes the manifest one record at a time, so the document
// is never held whole; without whitespace between tokens, so its size is the
// size of its records; and without HTML escaping: a manifest is never HTML,
// and escaping would make each <, > and & in a description six bytes.
func writeManifest(destination io.Writer, manifest Manifest) error {
	output := bufio.NewWriter(destination)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	write := func(value any) error {
		encoded.Reset()
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("encode backup manifest: %w", err)
		}
		_, err := output.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
		return err
	}
	value := reflect.ValueOf(manifest)
	separator := "{"
	for index, field := range manifestFields() {
		fieldValue := value.Field(index)
		isList := fieldValue.Kind() == reflect.Slice
		if field.omitEmpty && (fieldValue.IsZero() || isList && fieldValue.Len() == 0) {
			continue
		}
		output.WriteString(separator + strconv.Quote(field.name) + ":")
		separator = ","
		if !isList || fieldValue.IsNil() {
			if err := write(fieldValue.Interface()); err != nil {
				return err
			}
			continue
		}
		output.WriteByte('[')
		for element := range fieldValue.Len() {
			if element > 0 {
				output.WriteByte(',')
			}
			if err := write(fieldValue.Index(element).Interface()); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	}
	output.WriteString("}\n")
	return output.Flush()
}

// manifestField is a top-level manifest field: its JSON name and whether it
// is left out when empty. Fields are in the order of Manifest.
type manifestField struct {
	name      string
	omitEmpty bool
}

func manifestFields() []manifestField {
	manifestType := reflect.TypeFor[Manifest]()
	fields := make([]manifestField, manifestType.NumField())
	for index := range fields {
		name, options, _ := strings.Cut(manifestType.Field(index).Tag.Get("json"), ",")
		fields[index] = manifestField{name: name, omitEmpty: options == "omitempty"}
	}
	return fields
}

func readManifest(manifestPath string) (Manifest, error) {
	if err := requireRegularFile(manifestPath); err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, err
	}
	return decodeManifest(file, info.Size(), manifestLimit)
}

// decodeManifest reads a manifest of size bytes with memory bounded by its
// cost, which the budget limits (manifestBudget): it refuses a file larger
// than limit (or than the 64 MiB of version 10 and older) before and while
// reading, and decodes one value at a time, charging each list element, map
// entry and record behind a pointer before it exists, so neither the
// document nor any record is held whole. Each record must hold every field
// its format always writes, so an empty or partial record stops the read at
// once. Format and version come first in every manifest OwnGit writes; they
// decide how the rest is read, so a backup written by a newer OwnGit is
// refused as such rather than for its new fields.
func decodeManifest(input io.Reader, size, limit int64) (Manifest, error) {
	tooLarge := fmt.Errorf("backup manifest is larger than the %d MiB a backup holds", limit>>20)
	if size > limit {
		return Manifest{}, tooLarge
	}
	budget := &manifestBudget{limit: limit}
	reader := &manifestReader{source: input, budget: budget, limit: limit, tooLarge: tooLarge}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	decodeErr := func(err error) (Manifest, error) {
		if reader.refused != nil {
			err = reader.refused
		}
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		if err == nil {
			err = errors.New("not a JSON object")
		}
		return decodeErr(err)
	}
	fields := fieldsOf(reflect.TypeFor[Manifest]())
	value := reflect.ValueOf(&manifest).Elem()
	leading := []string{"format", "version"}
	seen := make([]bool, len(fields.list))
	position := 0
	for ; decoder.More(); position++ {
		token, err := decoder.Token()
		if err != nil {
			return decodeErr(err)
		}
		key := token.(string)
		switch key {
		case "direct_review_settings", "direct_review_task_contexts", "direct_review_requests":
			return Manifest{}, errDirectReviewManifest
		}
		if position < len(leading) && key != leading[position] {
			return Manifest{}, errManifestStart
		}
		field, err := fields.take(key, seen)
		if err != nil {
			return decodeErr(err)
		}
		if err := decodeValue(decoder, value.Field(field.index), budget); err != nil {
			return decodeErr(err)
		}
		if key != "version" {
			continue
		}
		if manifest.Format != backupFormat {
			return Manifest{}, errors.New("unsupported backup format")
		}
		if err := validateBackupVersion(manifest.Version); err != nil {
			return Manifest{}, err
		}
		if manifest.Version < backupVersion {
			reader.limit, reader.tooLarge = format10ManifestLimit, errManifestTooLarge
			if size > reader.limit {
				return Manifest{}, errManifestTooLarge
			}
		}
	}
	if position < len(leading) {
		return Manifest{}, errManifestStart
	}
	if _, err := decoder.Token(); err != nil {
		return decodeErr(err)
	}
	if err := fields.complete(seen); err != nil {
		return decodeErr(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Manifest{}, errors.New("backup manifest contains trailing data")
	}
	return manifest, nil
}

// manifestBudget limits the cost of a manifest, which is what backup
// checks and restore charges as it reads: every byte except whitespace
// between tokens (manifestText), plus the in-memory size of every list
// element (twice, for the list's growth), map entry (twice, for the map's)
// and record behind a pointer. So the budget bounds the memory a manifest
// takes, however small its records.
type manifestBudget struct {
	limit, used int64
}

func (budget *manifestBudget) charge(cost int64) error {
	budget.used += cost
	if budget.used > budget.limit {
		return fmt.Errorf("backup manifest holds more than the %d MiB of records a backup holds", budget.limit>>20)
	}
	return nil
}

func sliceElementCost(list reflect.Type) int64 { return 2 * int64(list.Elem().Size()) }

func mapEntryCost(table reflect.Type) int64 {
	return 2 * int64(table.Key().Size()+table.Elem().Size())
}

// recordsCost is what decodeValue charges for value beyond its bytes.
func recordsCost(value reflect.Value) int64 {
	valueType := value.Type()
	if !streamed(valueType) {
		if valueType.Kind() == reflect.Pointer && !value.IsNil() {
			return int64(valueType.Elem().Size())
		}
		return 0
	}
	var cost int64
	switch valueType.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			cost = int64(valueType.Elem().Size()) + recordsCost(value.Elem())
		}
	case reflect.Struct:
		for _, field := range fieldsOf(valueType).list {
			cost += recordsCost(value.Field(field.index))
		}
	case reflect.Slice:
		for index := range value.Len() {
			cost += sliceElementCost(valueType) + recordsCost(value.Index(index))
		}
	case reflect.Map:
		for entry := value.MapRange(); entry.Next(); {
			cost += mapEntryCost(valueType) + recordsCost(entry.Value())
		}
	}
	return cost
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// streamed reports whether decodeValue reads a value of this type piece by
// piece: a list, a map, a record, or a record behind a pointer. Anything
// else is one JSON scalar, or a type with its own decoding such as
// time.Time, which encoding/json decodes whole.
func streamed(valueType reflect.Type) bool {
	if valueType.Implements(unmarshalerType) || reflect.PointerTo(valueType).Implements(unmarshalerType) {
		return false
	}
	switch valueType.Kind() {
	case reflect.Slice, reflect.Map, reflect.Struct:
		return true
	case reflect.Pointer:
		return streamed(valueType.Elem()) && valueType.Elem().Kind() == reflect.Struct
	}
	return false
}

// decodeValue decodes the next JSON value into target as encoding/json
// does, but a list, map or record one piece at a time, charging budget for
// each piece before it exists.
func decodeValue(decoder *json.Decoder, target reflect.Value, budget *manifestBudget) error {
	valueType := target.Type()
	if !streamed(valueType) {
		if err := decoder.Decode(target.Addr().Interface()); err != nil {
			return err
		}
		if valueType.Kind() == reflect.Pointer && !target.IsNil() {
			return budget.charge(int64(valueType.Elem().Size()))
		}
		return nil
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		target.SetZero()
		return nil
	}
	open := json.Delim('{')
	if valueType.Kind() == reflect.Slice {
		open = '['
	}
	if token != open {
		return fmt.Errorf("json: cannot unmarshal %v into Go value of type %s", token, valueType)
	}
	switch valueType.Kind() {
	case reflect.Pointer:
		if err := budget.charge(int64(valueType.Elem().Size())); err != nil {
			return err
		}
		target.Set(reflect.New(valueType.Elem()))
		return decodeRecord(decoder, target.Elem(), budget)
	case reflect.Struct:
		return decodeRecord(decoder, target, budget)
	case reflect.Slice:
		target.Set(reflect.MakeSlice(valueType, 0, 0))
		for decoder.More() {
			if err := budget.charge(sliceElementCost(valueType)); err != nil {
				return err
			}
			element := reflect.New(valueType.Elem()).Elem()
			if err := decodeValue(decoder, element, budget); err != nil {
				return err
			}
			target.Set(reflect.Append(target, element))
		}
	case reflect.Map:
		target.Set(reflect.MakeMap(valueType))
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key := reflect.ValueOf(token).Convert(valueType.Key())
			if target.MapIndex(key).IsValid() {
				return fmt.Errorf("json: key %q appears twice", token)
			}
			if err := budget.charge(mapEntryCost(valueType)); err != nil {
				return err
			}
			element := reflect.New(valueType.Elem()).Elem()
			if err := decodeValue(decoder, element, budget); err != nil {
				return err
			}
			target.SetMapIndex(key, element)
		}
	}
	_, err = decoder.Token()
	return err
}

// decodeRecord decodes the fields of a record whose opening brace was read.
func decodeRecord(decoder *json.Decoder, target reflect.Value, budget *manifestBudget) error {
	fields := fieldsOf(target.Type())
	seen := make([]bool, len(fields.list))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		field, err := fields.take(token.(string), seen)
		if err != nil {
			return err
		}
		if err := decodeValue(decoder, target.Field(field.index), budget); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return fields.complete(seen)
}

// recordFields are the JSON fields of a record type. A field without
// omitempty or omitzero is required: every OwnGit version that wrote the
// record wrote it.
type recordFields struct {
	list   []recordField
	byName map[string]int
}

type recordField struct {
	name     string
	index    int
	required bool
}

var recordFieldsCache sync.Map

func fieldsOf(recordType reflect.Type) *recordFields {
	if cached, ok := recordFieldsCache.Load(recordType); ok {
		return cached.(*recordFields)
	}
	fields := &recordFields{byName: map[string]int{}}
	for index := range recordType.NumField() {
		structField := recordType.Field(index)
		name, options, _ := strings.Cut(structField.Tag.Get("json"), ",")
		if !structField.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = structField.Name
		}
		fields.byName[name] = len(fields.list)
		fields.list = append(fields.list, recordField{name: name, index: index, required: !strings.Contains(options, "omit")})
	}
	recordFieldsCache.Store(recordType, fields)
	return fields
}

func (fields *recordFields) take(name string, seen []bool) (recordField, error) {
	position, known := fields.byName[name]
	if !known {
		return recordField{}, fmt.Errorf("json: unknown field %q", name)
	}
	if seen[position] {
		return recordField{}, fmt.Errorf("json: field %q appears twice", name)
	}
	seen[position] = true
	return fields.list[position], nil
}

func (fields *recordFields) complete(seen []bool) error {
	for position, field := range fields.list {
		if field.required && !seen[position] {
			return fmt.Errorf("backup record lacks its %q field", field.name)
		}
	}
	return nil
}

// manifestText is the rule backup and restore apply to the bytes of a
// manifest: whitespace between tokens is not charged and shrinks to one
// space, which keeps tokens apart as encoding/json sees them; every other
// byte is charged; and a string longer than maxManifestStringBytes is
// refused.
type manifestText struct {
	inString, escaped, space bool
	stringBytes              int
}

// next reports whether character is kept and whether it is charged.
func (text *manifestText) next(character byte) (kept, charged bool, err error) {
	if text.inString {
		text.stringBytes++
		if text.stringBytes > maxManifestStringBytes {
			return false, false, errors.New("backup manifest holds a text longer than any OwnGit record")
		}
		switch {
		case text.escaped:
			text.escaped = false
		case character == '\\':
			text.escaped = true
		case character == '"':
			text.inString = false
		}
		return true, true, nil
	}
	if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
		kept = !text.space
		text.space = true
		return kept, false, nil
	}
	text.space = false
	if character == '"' {
		text.inString, text.stringBytes = true, 0
	}
	return true, true, nil
}

// manifestReader passes a manifest to the JSON decoder by manifestText,
// charging budget, so runs of whitespace never fill the decoder's buffer,
// and refuses input longer than limit (with tooLarge). A refusal is final:
// the decoder may read again after an error.
type manifestReader struct {
	source      io.Reader
	budget      *manifestBudget
	text        manifestText
	limit, read int64
	tooLarge    error
	refused     error
}

func (reader *manifestReader) Read(buffer []byte) (int, error) {
	for reader.refused == nil {
		count, err := reader.source.Read(buffer)
		reader.read += int64(count)
		if reader.read > reader.limit {
			reader.refused = reader.tooLarge
			return 0, reader.refused
		}
		kept, charged := 0, int64(0)
		for _, character := range buffer[:count] {
			keep, charge, textErr := reader.text.next(character)
			if textErr != nil {
				reader.refused = textErr
				return 0, reader.refused
			}
			if charge {
				charged++
			}
			if keep {
				buffer[kept] = character
				kept++
			}
		}
		if chargeErr := reader.budget.charge(charged); chargeErr != nil {
			reader.refused = chargeErr
			return 0, reader.refused
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
	return 0, reader.refused
}

func addPullRequestState(manifest *Manifest, snapshot state.RecoveryState) {
	for _, record := range snapshot.PullRequests {
		manifest.PullRequests = append(manifest.PullRequests, PullRequestManifest{
			RepositoryID: record.RepositoryID, Number: record.Number, Title: record.Title,
			SourceBranch: record.SourceBranch, TargetBranch: record.TargetBranch, Status: record.Status,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
			MergeSourceOID: record.MergeSourceOID, MergeTargetOID: record.MergeTargetOID,
			MergeOID: record.MergeOID, MergeReceipt: record.MergeReceipt, MergedAt: record.MergedAt,
			Body: record.Body, EditRevision: record.EditRevision, EditedAt: record.EditedAt,
			CreatedBy: record.CreatedBy, EditedBy: record.EditedBy, MergedBy: record.MergedBy,
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
			Note: review.Note, Actor: review.Actor,
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

// addRepositoryRecords adds each repository's names and, unless it keeps
// every default, its policy.
func addRepositoryRecords(manifest *Manifest, snapshot state.RecoveryState) {
	index := make(map[string]*RepositoryManifest, len(manifest.Repositories))
	for position := range manifest.Repositories {
		index[manifest.Repositories[position].ID] = &manifest.Repositories[position]
	}
	for _, name := range snapshot.RepositoryNames {
		item := index[name.RepositoryID]
		item.Names = append(item.Names, RepositoryNameManifest{Name: name.Name, Kind: name.Kind, CreatedAt: name.CreatedAt, AliasUntil: name.AliasUntil})
	}
	for _, policy := range snapshot.RepositoryPolicies {
		if policy.IsDefault() {
			continue
		}
		index[policy.RepositoryID].Policy = &RepositoryPolicyManifest{
			RetainHistory: policy.RetainHistory, ProtectDefaultBranch: policy.ProtectDefaultBranch,
			ExtraRefPrefixes: policy.ExtraRefPrefixes, UpdatedAt: policy.UpdatedAt,
		}
	}
}

// format11Content names the first record in manifest that format 10 cannot
// hold, or returns "" when there is none. Create writes format 11 only for
// such a backup, and a manifest of an older version that holds one is
// refused instead of losing it.
func format11Content(manifest Manifest) string {
	for _, item := range manifest.Repositories {
		if len(item.Names) != 0 {
			return "a renamed repository"
		}
		if item.Policy != nil {
			return "a repository policy"
		}
	}
	for _, record := range manifest.PullRequests {
		if record.Body != "" || record.EditRevision != 0 || record.EditedAt != nil {
			return "a pull request description or edit"
		}
		if record.CreatedBy != (state.Actor{}) || record.EditedBy != (state.Actor{}) || record.MergedBy != (state.Actor{}) {
			return "a pull request actor"
		}
	}
	for _, review := range manifest.PullRequestReviews {
		if review.Note != "" || review.Actor != (state.Actor{}) {
			return "a review note or actor"
		}
	}
	for _, source := range manifest.ImportSources {
		if source.OverwriteDiverged || source.FollowUpstreamDeletions || len(source.ExtraRefPrefixes) != 0 {
			return "an import refresh option"
		}
	}
	return ""
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
	for _, item := range manifest.Repositories {
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{
			ID: item.ID, Name: item.Name, Description: item.Description, CreatedAt: item.CreatedAt,
			AttemptSequence: item.AttemptSequence,
		})
		for _, name := range item.Names {
			snapshot.RepositoryNames = append(snapshot.RepositoryNames, state.RepositoryName{
				Name: name.Name, RepositoryID: item.ID, Kind: name.Kind, CreatedAt: name.CreatedAt, AliasUntil: name.AliasUntil,
			})
		}
		if item.Policy != nil {
			snapshot.RepositoryPolicies = append(snapshot.RepositoryPolicies, state.RepositoryPolicy{
				RepositoryID: item.ID, RetainHistory: item.Policy.RetainHistory, ProtectDefaultBranch: item.Policy.ProtectDefaultBranch,
				ExtraRefPrefixes: item.Policy.ExtraRefPrefixes, UpdatedAt: item.Policy.UpdatedAt,
			})
		}
	}
	attachImportState(&snapshot, manifest)
	for _, record := range manifest.PullRequests {
		snapshot.PullRequests = append(snapshot.PullRequests, state.PullRequest{
			RepositoryID: record.RepositoryID, Number: record.Number, Title: record.Title,
			SourceBranch: record.SourceBranch, TargetBranch: record.TargetBranch, Status: record.Status,
			CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
			MergeSourceOID: record.MergeSourceOID, MergeTargetOID: record.MergeTargetOID,
			MergeOID: record.MergeOID, MergeReceipt: record.MergeReceipt, MergedAt: record.MergedAt,
			Body: record.Body, EditRevision: record.EditRevision, EditedAt: record.EditedAt,
			CreatedBy: record.CreatedBy, EditedBy: record.EditedBy, MergedBy: record.MergedBy,
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
			Note: review.Note, Actor: review.Actor,
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

// validateBackupVersion accepts the committed baseline formats, the released
// formats and the current format. Every other version below the current one
// was written only by unreleased development builds.
func validateBackupVersion(version int) error {
	switch {
	case version == legacyBackupVersion || version == pullRequestBackupVersion || version == checkBackupVersion ||
		version == closedPullRequestBackupVersion || version == backupVersion:
		return nil
	case version > backupVersion:
		return fmt.Errorf("unsupported backup version %d: this build supports versions 1, 2, %d, %d, and %d", version, checkBackupVersion, closedPullRequestBackupVersion, backupVersion)
	default:
		return fmt.Errorf("backup uses the unreleased development format %d; this build supports versions 1, 2, %d, %d, and %d", version, checkBackupVersion, closedPullRequestBackupVersion, backupVersion)
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
		if repository.ValidateID(item.ID) != nil || item.Name == "" || len(item.Name) > 100 || len(item.Description) > state.MaximumRepositoryDescriptionBytes || ids[item.ID] {
			return errors.New("backup repository metadata is invalid or not portable")
		}
		ids[item.ID] = true
		if item.CreatedAt.IsZero() {
			return errors.New("backup repository creation time is invalid")
		}
		for _, name := range item.Names {
			if repository.ValidateID(name.Name) != nil || repository.ValidateName(name.Name, "") != nil {
				return fmt.Errorf("backup repository name %q is not a valid repository address", name.Name)
			}
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
		if content := format11Content(manifest); content != "" {
			return fmt.Errorf("version %d backup contains %s, which only version %d holds", manifest.Version, content, backupVersion)
		}
	}
	if manifest.Version < closedPullRequestBackupVersion {
		for _, record := range manifest.PullRequests {
			if record.Status == state.PullRequestClosed {
				return fmt.Errorf("version %d backup contains an unsupported closed pull request", manifest.Version)
			}
		}
		for _, intent := range manifest.PullRequestMergeIntents {
			if intent.Mode == "up_to_date" {
				return fmt.Errorf("version %d backup contains an unsupported up-to-date merge", manifest.Version)
			}
		}
	}
	if manifest.Version < checkBackupVersion {
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
	if err := state.ValidateRepositoryRecords(snapshot); err != nil {
		return fmt.Errorf("backup repository metadata is invalid: %w", err)
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
	if err := validateImportManifest(manifest, snapshot); err != nil {
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

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

const (
	manifestName        = "manifest.json"
	backupFormat        = "owngit-offline-backup"
	legacyBackupVersion = 1
	backupVersion       = 2
	maximumManifest     = 8 << 20
	pendingRestoreName  = state.IncompleteRestoreMarkerName
)

type Manifest struct {
	Format                  string                        `json:"format"`
	Version                 int                           `json:"version"`
	CreatedAt               time.Time                     `json:"created_at"`
	AccessMode              string                        `json:"access_mode"`
	AccessHash              string                        `json:"access_password_hash,omitempty"`
	AdminHash               string                        `json:"admin_password_hash"`
	Repositories            []RepositoryManifest          `json:"repositories"`
	PullRequests            []PullRequestManifest         `json:"pull_requests,omitempty"`
	PullRequestRevisions    []PullRequestRevisionManifest `json:"pull_request_revisions,omitempty"`
	PullRequestReviews      []PullRequestReviewManifest   `json:"pull_request_reviews,omitempty"`
	PullRequestMergeIntents []PullRequestMergeManifest    `json:"pull_request_merge_intents,omitempty"`
}

type RepositoryManifest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	Head        Head      `json:"head"`
	Refs        []Ref     `json:"refs"`
	Empty       bool      `json:"empty"`
	Bundle      string    `json:"bundle,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
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
			arguments := []string{"--git-dir", repositoryPath, "bundle", "create", bundlePath, "--all"}
			if item.Head.OID != "" {
				arguments = append(arguments, "HEAD")
			}
			if _, err := runner.Run(ctx, "", nil, arguments...); err != nil {
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
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
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
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{ID: item.ID, Name: item.Name, Description: item.Description, CreatedAt: item.CreatedAt})
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
	item := RepositoryManifest{ID: stored.ID, Name: stored.Name, Description: stored.Description, CreatedAt: stored.CreatedAt}
	refs, err := readRefs(ctx, runner, repositoryPath)
	if err != nil {
		return RepositoryManifest{}, err
	}
	item.Refs = refs
	symbolic, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		item.Head.Symbolic = strings.TrimSpace(string(symbolic.Stdout))
	} else {
		detached, detachedErr := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", "HEAD")
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
	result, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "for-each-ref", "--format=%(refname)%00%(objectname)")
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
		if _, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "bundle", "verify", bundlePath); err != nil {
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
		arguments := []string{"--git-dir", repositoryPath, "fetch", "--no-tags", "--no-write-fetch-head", bundlePath}
		for _, ref := range item.Refs {
			arguments = append(arguments, ref.Name+":"+ref.Name)
		}
		if item.Head.OID != "" {
			arguments = append(arguments, "HEAD")
		}
		if _, err := runner.Run(ctx, "", nil, arguments...); err != nil {
			return fmt.Errorf("import bundle: %w", err)
		}
	}
	if item.Head.Symbolic != "" {
		if _, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "symbolic-ref", "HEAD", item.Head.Symbolic); err != nil {
			return err
		}
	} else if item.Head.OID != "" {
		if _, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "update-ref", "--no-deref", "HEAD", item.Head.OID); err != nil {
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
		if _, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "cat-file", "-e", ref.OID+"^{object}"); err != nil {
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

func readManifest(manifestPath string) (Manifest, error) {
	if err := requireRegularFile(manifestPath); err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maximumManifest+1))
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
			ReviewerLabel: review.ReviewerLabel, Provenance: review.Provenance, CreatedAt: review.CreatedAt,
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

func recoveryState(manifest Manifest) state.RecoveryState {
	snapshot := state.RecoveryState{
		AccessMode: manifest.AccessMode, AccessPasswordHash: manifest.AccessHash, AdminPasswordHash: manifest.AdminHash,
	}
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
			ReviewerLabel: review.ReviewerLabel, Provenance: review.Provenance, CreatedAt: review.CreatedAt,
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
	return snapshot
}

func validateManifest(manifest Manifest) error {
	if manifest.Format != backupFormat || (manifest.Version != legacyBackupVersion && manifest.Version != backupVersion) {
		return errors.New("unsupported backup format or version")
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
		return nil
	}
	snapshot := recoveryState(manifest)
	for _, item := range manifest.Repositories {
		snapshot.Repositories = append(snapshot.Repositories, state.Repository{ID: item.ID, Name: item.Name, Description: item.Description, CreatedAt: item.CreatedAt})
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

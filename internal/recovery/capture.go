package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// A backup describes one instant. Every operation that changes both
// OwnGit's records and a repository's refs (pull requests, import
// publication, kept-history restore, a default branch change, creation and
// deletion) holds that repository's write lock throughout, and so does a
// push. The capture takes every repository's read lock first, then reads
// the database, whose first read fixes the instant, then reads each
// repository's refs and releases that repository at once. So no such
// operation ran on a repository between the instant and the reading of its
// refs, and the records and refs belong together. The bundles are then made
// from the refs read, not from the refs of the moment, so later writes
// change nothing in the backup; OwnGit never deletes objects while a
// repository exists, and the backup hold keeps deletion and maintenance
// away until a repository's bundle is written.
const (
	// captureLockWindow bounds the wait for every repository's read lock in
	// one attempt: a push in progress delays the start of the backup, and
	// the pushes to repositories already locked wait at most this long.
	captureLockWindow = 5 * time.Second
	// captureRetryPause separates attempts, and captureLimit ends them: a
	// repository still busy or being prepared after it fails the backup,
	// named.
	captureRetryPause = 5 * time.Second
	captureLimit      = 2 * time.Minute
)

// captureReaders read refs of that many repositories at once, so the last
// repository is released sooner.
var captureReaders = min(8, runtime.NumCPU())

// Ref storage backends (extensions.refStorage).
const (
	refStorageFiles    = "files"
	refStorageReftable = "reftable"
)

// CaptureReport says how a backup affected OwnGit's work.
type CaptureReport struct {
	// Repositories is the number of repositories in the backup.
	Repositories int `json:"repositories"`
	// Attempts counts the starts of the capture; more than one means
	// repositories were busy, being prepared or created when it began.
	Attempts int `json:"attempts"`
	// LongestHold is the longest time the backup kept one repository's Git
	// writes, such as pushes, waiting, and LongestHoldRepository names it.
	LongestHold           time.Duration `json:"longest_hold"`
	LongestHoldRepository string        `json:"longest_hold_repository,omitempty"`
}

// capturedRepository is one repository as the backup describes it: its
// manifest item with refs and HEAD read at the instant, and its folder.
type capturedRepository struct {
	item RepositoryManifest
	path string
	repositoryStorage
}

// repositoryStorage is how a repository stores objects and refs, read from
// its configuration before the instant, so no Git process for it runs while
// writes wait.
type repositoryStorage struct {
	objectFormat string
	refStorage   string
}

type capturedState struct {
	snapshot     state.RecoveryState
	repositories []capturedRepository
	at           time.Time
}

// capture reads the portable state and every repository's refs at one
// instant. hold keeps the repositories until the caller has written their
// bundles.
func capture(ctx context.Context, store *state.Store, manager *repository.Manager, runner commandRunner, hold *repository.BackupHold, report *CaptureReport) (capturedState, error) {
	deadline := time.Now().Add(captureLimit)
	for {
		report.Attempts++
		captured, busy, err := captureOnce(ctx, store, manager, runner, hold, report)
		if err != nil || busy == "" {
			return captured, err
		}
		if time.Now().Add(captureRetryPause).After(deadline) {
			return capturedState{}, fmt.Errorf("the backup could not start within %s: %s", captureLimit, busy)
		}
		timer := time.NewTimer(captureRetryPause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return capturedState{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// captureOnce makes one attempt. It returns a reason to try again, rather
// than an error, when a repository was busy, still being prepared, or
// created while the attempt began.
func captureOnce(ctx context.Context, store *state.Store, manager *repository.Manager, runner commandRunner, hold *repository.BackupHold, report *CaptureReport) (capturedState, string, error) {
	recorded, err := store.Repositories(ctx)
	if err != nil {
		return capturedState{}, "", err
	}
	ids := make([]string, 0, len(recorded))
	for _, stored := range recorded {
		ids = append(ids, stored.ID)
	}
	slices.Sort(ids)
	hold.Add(ids...)
	if preparing := preparingRepositories(manager, ids); preparing != "" {
		return capturedState{}, preparing, nil
	}
	folders := make(map[string]capturedRepository, len(ids))
	for _, id := range ids {
		if err := repository.ValidateID(id); err != nil {
			return capturedState{}, "", fmt.Errorf("repository %q has an unsupported ID: %w", id, err)
		}
		path, err := manager.StoragePath(id)
		if err != nil {
			return capturedState{}, "", fmt.Errorf("open repository %q: %w", id, err)
		}
		storage, err := readRepositoryStorage(ctx, runner, id, path)
		if err != nil {
			return capturedState{}, "", err
		}
		folders[id] = capturedRepository{path: path, repositoryStorage: storage}
	}

	locks := &captureLocks{manager: manager, taken: map[string]time.Time{}}
	defer locks.releaseAll()
	if busy, err := locks.take(ctx, ids); err != nil || busy != "" {
		return capturedState{}, busy, err
	}
	read, err := store.BeginPortableRead(ctx)
	if err != nil {
		return capturedState{}, "", err
	}
	defer read.Close()
	at := time.Now().UTC()
	roster := read.Repositories()
	for _, stored := range roster {
		if _, locked := locks.taken[stored.ID]; !locked {
			return capturedState{}, fmt.Sprintf("repository %q was created while the backup began", stored.ID), nil
		}
	}
	if preparing := preparingRepositories(manager, ids); preparing != "" {
		return capturedState{}, preparing, nil
	}
	// A repository deleted before its lock was taken is not at the instant.
	for _, id := range ids {
		if !slices.ContainsFunc(roster, func(stored state.Repository) bool { return stored.ID == id }) {
			locks.release(id, report)
			hold.Release(id)
		}
	}

	repositories := make([]capturedRepository, len(roster))
	for index, stored := range roster {
		if manager.UnsettledRefWriter(stored.ID) {
			return capturedState{}, "", fmt.Errorf("repository %q: an import's Git process that writes its refs could not be stopped and may still change them; restart OwnGit, then back up again", stored.ID)
		}
		repositories[index] = folders[stored.ID]
	}
	if err := readCapturedRefs(ctx, runner, roster, repositories, locks, report); err != nil {
		return capturedState{}, "", err
	}
	snapshot, err := read.Finish(ctx)
	if err != nil {
		return capturedState{}, "", err
	}
	return capturedState{snapshot: snapshot, repositories: repositories, at: at}, "", nil
}

func preparingRepositories(manager *repository.Manager, ids []string) string {
	var preparing []string
	for _, id := range ids {
		if manager.Preparing(id) {
			preparing = append(preparing, id)
		}
	}
	if len(preparing) == 0 {
		return ""
	}
	return "OwnGit is still preparing repositories " + quotedList(preparing)
}

// readCapturedRefs reads refs and HEAD of every repository, a few at a
// time, releasing each repository's lock as soon as they are read.
func readCapturedRefs(ctx context.Context, runner commandRunner, roster []state.Repository, repositories []capturedRepository, locks *captureLocks, report *CaptureReport) error {
	indexes := make(chan int)
	failures := make([]error, len(roster))
	var workers sync.WaitGroup
	for range min(captureReaders, len(roster)) {
		workers.Go(func() {
			for index := range indexes {
				captured := &repositories[index]
				item, err := inspectRepository(ctx, runner, captured.path, roster[index], captured.refStorage)
				locks.release(roster[index].ID, report)
				// Objects are never removed while the backup holds the
				// repository, so a detached HEAD's object is checked after
				// the lock is released.
				if err == nil && item.Head.OID != "" {
					if _, err = runner.Run(ctx, captured.path, nil, "--git-dir", ".", "cat-file", "-e", item.Head.OID+"^{object}"); err != nil {
						err = fmt.Errorf("detached HEAD names a missing object: %w", err)
					}
				}
				if err != nil {
					failures[index] = fmt.Errorf("inspect repository %q: %w", roster[index].ID, err)
					continue
				}
				captured.item = item
			}
		})
	}
	for index := range roster {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	return errors.Join(failures...)
}

// captureLocks are the read locks a capture holds, with the time each was
// taken, so the report can say how long writes waited.
type captureLocks struct {
	manager *repository.Manager
	mu      sync.Mutex
	taken   map[string]time.Time
}

// take locks ids within captureLockWindow. It returns a reason to try
// again when a repository stayed busy, and holds nothing then.
func (locks *captureLocks) take(ctx context.Context, ids []string) (string, error) {
	window, cancel := context.WithTimeout(ctx, captureLockWindow)
	defer cancel()
	for _, id := range ids {
		if err := locks.manager.Locks.For(id).RLockContext(window); err != nil {
			locks.releaseAll()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return fmt.Sprintf("repository %q stayed busy with a Git write, such as a push", id), nil
		}
		locks.taken[id] = time.Now()
	}
	return "", nil
}

// release unlocks id if it is still locked and records how long it was.
func (locks *captureLocks) release(id string, report *CaptureReport) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	taken, locked := locks.taken[id]
	if !locked {
		return
	}
	delete(locks.taken, id)
	locks.manager.Locks.For(id).RUnlock()
	if held := time.Since(taken); held > report.LongestHold {
		report.LongestHold = held
		report.LongestHoldRepository = id
	}
}

func (locks *captureLocks) releaseAll() {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	for id := range locks.taken {
		locks.manager.Locks.For(id).RUnlock()
	}
	clear(locks.taken)
}

// bundleCaptured writes the bundle of a captured repository to bundlePath.
// Git makes it in a new repository inside the backup stage that holds
// exactly the captured refs and HEAD and reads the objects of the
// repository through objects/info/alternates. That repository is removed
// once the bundle is written.
func bundleCaptured(ctx context.Context, runner commandRunner, captured capturedRepository, captureRoot, bundlePath string) error {
	item := captured.item
	// The folder is new, made here and owner-only whatever the umask, so
	// only this account can reach what Git writes into it.
	capturePath := filepath.Join(captureRoot, item.ID+".git")
	if err := os.Mkdir(capturePath, 0o700); err != nil {
		return err
	}
	if err := state.ProtectPrivatePath(capturePath, true); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, capturePath, nil, "init", "--bare", "--quiet", "--object-format="+captured.objectFormat, "."); err != nil {
		return err
	}
	alternates := filepath.Join(capturePath, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(alternatesLine(filepath.Join(captured.path, "objects"))), 0o600); err != nil {
		return err
	}
	var commands strings.Builder
	for _, ref := range item.Refs {
		fmt.Fprintf(&commands, "create %s %s\n", ref.Name, ref.OID)
	}
	if len(item.Refs) != 0 {
		if _, err := runner.Run(ctx, capturePath, strings.NewReader(commands.String()), "--git-dir", ".", "update-ref", "--stdin"); err != nil {
			return fmt.Errorf("record captured refs: %w", err)
		}
	}
	if item.Head.Symbolic != "" {
		if _, err := runner.Run(ctx, capturePath, nil, "--git-dir", ".", "symbolic-ref", "HEAD", item.Head.Symbolic); err != nil {
			return err
		}
	} else if item.Head.OID != "" {
		if _, err := runner.Run(ctx, capturePath, nil, "--git-dir", ".", "update-ref", "--no-deref", "HEAD", item.Head.OID); err != nil {
			return err
		}
	}
	arguments := []string{"--git-dir", ".", "bundle", "create", bundlePath, "--all"}
	if item.Head.OID != "" {
		arguments = append(arguments, "HEAD")
	}
	if _, err := runner.Run(ctx, capturePath, nil, arguments...); err != nil {
		return fmt.Errorf("bundle repository %q: %w", item.ID, err)
	}
	return os.RemoveAll(capturePath)
}

// alternatesLine names objectsPath in an objects/info/alternates file. Git
// reads one path per line and unquotes a line that starts with a double
// quote (C-style), so a path with a line break, a quote or a backslash is
// quoted.
func alternatesLine(objectsPath string) string {
	if !strings.ContainsAny(objectsPath, "\"\\\n\r") {
		return objectsPath + "\n"
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + replacer.Replace(objectsPath) + "\"\n"
}

// readRepositoryStorage reads the object format and ref backend of a
// repository in one Git call, and refuses a repository that keeps part of
// its objects elsewhere: in another repository (objects/info/alternates) or
// on a promisor remote (a partial clone). OwnGit creates neither, and a
// backup could not keep the other place from removing them.
func readRepositoryStorage(ctx context.Context, runner commandRunner, id, path string) (repositoryStorage, error) {
	if _, err := os.Lstat(filepath.Join(path, "objects", "info", "alternates")); err == nil {
		return repositoryStorage{}, fmt.Errorf("repository %q borrows objects from another repository (objects/info/alternates), which OwnGit cannot back up; repack it so it holds its own objects", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return repositoryStorage{}, fmt.Errorf("inspect repository %q: %w", id, err)
	}
	storage := repositoryStorage{objectFormat: repository.ObjectFormatSHA1, refStorage: refStorageFiles}
	result, err := runner.Run(ctx, path, nil, "--git-dir", ".", "config", "--local", "--get-regexp",
		`^(extensions\.(objectformat|refstorage|partialclone)|remote\..*\.promisor)$`)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return storage, nil
		}
		return repositoryStorage{}, fmt.Errorf("inspect repository %q: %w", id, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		key, value, _ := strings.Cut(line, " ")
		value = strings.ToLower(strings.TrimSpace(value))
		switch key {
		case "extensions.objectformat":
			if value != repository.ObjectFormatSHA1 && value != repository.ObjectFormatSHA256 {
				return repositoryStorage{}, fmt.Errorf("repository %q uses unsupported object format %q", id, value)
			}
			storage.objectFormat = value
		case "extensions.refstorage":
			if value != refStorageFiles && value != refStorageReftable {
				return repositoryStorage{}, fmt.Errorf("repository %q uses unsupported ref storage %q", id, value)
			}
			storage.refStorage = value
		default:
			return repositoryStorage{}, fmt.Errorf("repository %q is a partial clone whose missing objects are on another server, which OwnGit cannot back up", id)
		}
	}
	return storage, nil
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}

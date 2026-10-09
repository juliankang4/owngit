package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"owngit/internal/gitexec"
)

var (
	ErrPinnedObjectUnavailable = errors.New("pinned Git object is unavailable")
	ErrPinnedRepositoryChanged = errors.New("pinned repository storage changed")
	ErrPinnedRepositoryBusy    = errors.New("pinned repository is busy")
	ErrPinnedRefMoved          = errors.New("pinned repository ref moved or was deleted")
	ErrPinnedOutputLimit       = errors.New("pinned Git output exceeded its limit")
	// ErrPinnedBlobTooLarge reports that this server cannot read the exact
	// object without a Git process that may not fit in the memory it gives
	// Git: either the object is above the server read bound, or the delta it
	// is stored as rebuilds a larger base (see BlobAt).
	ErrPinnedBlobTooLarge      = errors.New("pinned Git blob is above the server read bound")
	ErrPinnedUnsupportedObject = errors.New("pinned Git object type is unsupported")
	ErrPinnedOffset            = errors.New("pinned blob offset is invalid")
	// ErrPinnedPathNotFound reports that the exact commit has no requested
	// workflow path. It is an expected no-config result, not source corruption.
	ErrPinnedPathNotFound = errTreeEntryNotFound
)

// PinnedSide selects one of the two commits captured by PinnedRepository.
type PinnedSide uint8

const (
	PinnedBase PinnedSide = iota + 1
	PinnedHead
)

// PinnedRepository binds one registered repository and two exact commit object
// IDs. It keeps no filesystem handle or repository lock between operations.
// A concurrent repository writer produces ErrPinnedRepositoryBusy. Path and
// identity checks cover cooperative single-writer use on a local namespace;
// they are pre-operation checks, not a tamperproof guarantee against same-user
// replacement during Git execution or hostile network filesystem semantics.
type PinnedRepository struct {
	manager      *Manager
	id           string
	path         string
	identity     os.FileInfo
	baseOID      string
	headOID      string
	objectFormat string
}

// PinnedChange is a bounded patch between the captured commits.
type PinnedChange struct {
	BaseOID   string
	HeadOID   string
	Patch     []byte
	Truncated bool
}

// PinnedBlobChunk is repository object data. Symlink content is returned as
// data. Submodule entries are rejected before this value is constructed.
type PinnedBlobChunk struct {
	Path    string
	OID     string
	Mode    string
	Content []byte
	Offset  int64
	Size    int64
	HasMore bool
	Binary  bool
	Symlink bool
}

// PinRepository verifies exact commit objects without resolving a branch, tag,
// HEAD, or arbitrary revision expression.
func (m *Manager) PinRepository(ctx context.Context, id, baseOID, headOID string) (*PinnedRepository, error) {
	if !isOID(baseOID) || !isOID(headOID) {
		return nil, errors.New("invalid pinned commit ID")
	}
	lock := m.Locks.For(id)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lock.TryRLock() {
		// A check job retries instead of waiting, so the lock never counts
		// it as waiting. Recording the attempt as use makes maintenance let
		// it in after the running step.
		m.NoteRepositoryUse(id)
		return nil, ErrPinnedRepositoryBusy
	}
	defer lock.RUnlock()
	path, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("repository not found")
	}
	identity, err := captureDirectoryIdentity(path)
	if err != nil {
		return nil, fmt.Errorf("inspect repository identity: %w", err)
	}
	objectFormat, err := pinnedObjectFormat(ctx, m.Git, path)
	if err != nil {
		return nil, err
	}
	expectedLength := 40
	if objectFormat == "sha256" {
		expectedLength = 64
	}
	if len(baseOID) != expectedLength || len(headOID) != expectedLength {
		return nil, errors.New("invalid pinned commit ID for repository object format")
	}
	if err := verifyPinnedCommit(ctx, m.Git, path, baseOID); err != nil {
		return nil, fmt.Errorf("verify base commit: %w", err)
	}
	if headOID != baseOID {
		if err := verifyPinnedCommit(ctx, m.Git, path, headOID); err != nil {
			return nil, fmt.Errorf("verify head commit: %w", err)
		}
	}
	return &PinnedRepository{
		manager: m, id: id, path: filepath.Clean(path), identity: identity,
		baseOID: baseOID, headOID: headOID, objectFormat: objectFormat,
	}, nil
}

func (p *PinnedRepository) RepositoryID() string { return p.id }
func (p *PinnedRepository) BaseOID() string      { return p.baseOID }
func (p *PinnedRepository) HeadOID() string      { return p.headOID }

// ObjectFormat is the repository hash algorithm observed when the commits were
// pinned, either "sha1" or "sha256".
func (p *PinnedRepository) ObjectFormat() string { return p.objectFormat }

// ReadChange returns a bounded internal Git diff. External diff commands,
// textconv, replacement objects, submodule recursion, and lazy object fetching
// are disabled.
func (p *PinnedRepository) ReadChange(ctx context.Context, limit int64) (PinnedChange, error) {
	if !validPinnedLimit(limit) {
		return PinnedChange{}, errors.New("invalid pinned change limit")
	}
	change := PinnedChange{BaseOID: p.baseOID, HeadOID: p.headOID}
	err := p.withReadLock(ctx, func(repositoryPath string) error {
		if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, p.baseOID); err != nil {
			return err
		}
		if p.headOID != p.baseOID {
			if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, p.headOID); err != nil {
				return err
			}
		}
		result, runErr := runPinnedGit(ctx, p.manager.Git, limit, repositoryPath,
			"-c", "diff.external=", "-c", "submodule.recurse=false",
			"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--full-index",
			"--find-renames=50%", "--submodule=short", p.baseOID, p.headOID, "--")
		change.Patch = result.Stdout
		var limitErr *gitexec.LimitError
		if errors.As(runErr, &limitErr) && limitErr.Stream == "stdout" {
			change.Truncated = true
			return nil
		}
		if runErr != nil {
			return classifyPinnedGitError(ctx, runErr)
		}
		return nil
	})
	if err != nil {
		return PinnedChange{}, err
	}
	return change, nil
}

// ListTree lists one directory at an already pinned side. Paths are interpreted
// literally as repository paths.
func (p *PinnedRepository) ListTree(ctx context.Context, side PinnedSide, directory string, limit int64) ([]TreeEntry, error) {
	if err := ValidatePinnedPath(directory, true); err != nil {
		return nil, err
	}
	if !validPinnedLimit(limit) {
		return nil, errors.New("invalid pinned tree limit")
	}
	commitOID, err := p.oid(side)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	err = p.withReadLock(ctx, func(repositoryPath string) error {
		if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, commitOID); err != nil {
			return err
		}
		treeOID := commitOID
		if directory != "" {
			entry, err := lookupPinnedTreeEntry(ctx, p.manager.Git, repositoryPath, commitOID, directory, limit)
			if err != nil {
				return err
			}
			if entry.Type != "tree" {
				if entry.Type == "commit" {
					return ErrPinnedUnsupportedObject
				}
				return errTreeEntryNotFound
			}
			treeOID = entry.OID
		}
		listed, err := listPinnedTree(ctx, p.manager.Git, repositoryPath, treeOID, directory, limit)
		if err != nil {
			return err
		}
		entries = listed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// ListTreeRecursive lists every blob and gitlink reachable from a pinned
// commit, with repository paths relative to the tree root. Git does not enter
// gitlinks, so a submodule appears as one 160000 entry and its content is not
// listed. Intermediate trees are not listed because Git does not record empty
// directories. limit bounds the raw listing bytes and a larger listing fails
// with ErrPinnedOutputLimit instead of being truncated.
func (p *PinnedRepository) ListTreeRecursive(ctx context.Context, side PinnedSide, limit int64) ([]TreeEntry, error) {
	if !validPinnedLimit(limit) {
		return nil, errors.New("invalid pinned tree limit")
	}
	commitOID, err := p.oid(side)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	err = p.withReadLock(ctx, func(repositoryPath string) error {
		if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, commitOID); err != nil {
			return err
		}
		// -t lists the tree entries themselves, so a file beside a directory of
		// the same path, and two directories of one path, are one path twice
		// for repeatedTreePath. The result keeps its leaves only, as before.
		result, runErr := runPinnedGit(ctx, p.manager.Git, limit, repositoryPath,
			"ls-tree", "-r", "-t", "-z", "-l", commitOID)
		if runErr != nil {
			return classifyPinnedGitError(ctx, runErr)
		}
		// A tree that names one path twice would list two entries with one
		// path, and a copy of it would silently overwrite the first.
		if err := repeatedTreePath(result.Stdout); err != nil {
			return err
		}
		listed := make([]TreeEntry, 0)
		for _, record := range bytes.Split(result.Stdout, []byte{0}) {
			if len(record) == 0 {
				continue
			}
			entry, parseErr := parseTreeEntry(record)
			if parseErr != nil {
				return parseErr
			}
			if entry.Type == "tree" {
				continue
			}
			entry.Path = entry.Name
			if separator := strings.LastIndexByte(entry.Path, '/'); separator >= 0 {
				entry.Name = entry.Path[separator+1:]
			}
			listed = append(listed, entry)
		}
		entries = listed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// ReadBlobObject returns the complete bytes of one blob named by object ID.
// The object ID must come from a pinned tree listing; this call performs no
// path lookup and follows no reference. It returns ErrPinnedObjectUnavailable
// when Git produces a different byte count than the tree recorded, so a
// truncated or replaced object cannot be mistaken for exact content. The blob
// is held in memory, so callers bound size before calling; a size above the
// server read bound is refused with ErrPinnedBlobTooLarge before Git runs.
//
// The memory a stored delta needs to rebuild is not checked per object here:
// a caller that reads a whole tree prices it with CheckBlobRebuilds first, so
// one metadata walk answers for every object instead of one walk per object.
func (p *PinnedRepository) ReadBlobObject(ctx context.Context, oid string, size int64) ([]byte, error) {
	if !isOID(oid) {
		return nil, errors.New("invalid pinned blob ID")
	}
	if size < 0 || !validPinnedLimit(size+1) {
		return nil, ErrPinnedOffset
	}
	// Git rebuilds a large stored delta in memory before the output limit
	// can stop it, so a blob above the size bound is refused before it is
	// read. The listed size does not bound a smaller blob whose stored delta
	// rebuilds a larger base; that set is priced by CheckBlobRebuilds.
	if bound := p.manager.Git.ReadBound(); bound > 0 && size > bound {
		return nil, ErrPinnedBlobTooLarge
	}
	release, err := p.manager.Git.ReadSlot(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPinnedRepositoryBusy, err)
	}
	defer release()
	var content []byte
	err = p.withReadLock(ctx, func(repositoryPath string) error {
		// The limit is one byte above the recorded size so an oversized object
		// is detected by the length check rather than silently truncated.
		result, runErr := runPinnedGit(ctx, p.manager.Git, size+1, repositoryPath, "cat-file", "blob", oid)
		if runErr != nil {
			return classifyPinnedGitError(ctx, runErr)
		}
		if int64(len(result.Stdout)) != size {
			return ErrPinnedObjectUnavailable
		}
		content = bytes.Clone(result.Stdout)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

// CheckBlobRebuilds refuses the whole list when any of the named objects would
// rebuild a stored delta in memory beyond what this computer gives Git. A
// caller that reads many objects of one pinned tree prices them here, before
// reading any content: one metadata walk answers for the whole list, whatever
// the number of objects and the depth of each chain, and nothing is read when
// one object is refused. The object IDs must come from a pinned tree listing.
//
// The check follows the pinned rules: it holds the pinned read lock with
// TryRLock, so a repository write in progress answers ErrPinnedRepositoryBusy
// for the caller to retry, it reads the path whose identity was pinned rather
// than the registered path, and the pinned error contract is kept. A chain
// whose cost cannot be bounded is an error, not a refusal.
func (p *PinnedRepository) CheckBlobRebuilds(ctx context.Context, oids []string) error {
	if len(oids) == 0 || memoryBudget() <= 0 {
		return nil
	}
	for _, oid := range oids {
		if !isOID(oid) {
			return errors.New("invalid pinned blob ID")
		}
	}
	return p.withReadLock(ctx, func(repositoryPath string) error {
		over, err := p.manager.blobsAboveMemoryLineWithin(ctx, repositoryPath, oids)
		if err != nil {
			return err
		}
		if over {
			return ErrPinnedBlobTooLarge
		}
		return nil
	})
}

// ReadBlob exposes one bounded chunk from a blob prefix. metadataLimit bounds
// the literal tree lookup. maxPrefix bounds the largest offset and prefix that
// OwnGit captures or returns. Git cat-file may still read and decompress the
// complete blob while the runner drains output, so maxPrefix is not a Git I/O
// or subprocess memory bound. The caller's context and runner timeout bound
// operation time. A listed size above the server read bound is refused with
// ErrPinnedBlobTooLarge before Git runs.
func (p *PinnedRepository) ReadBlob(ctx context.Context, side PinnedSide, filePath string, offset, metadataLimit, chunkLimit, maxPrefix int64) (PinnedBlobChunk, error) {
	if err := ValidatePinnedPath(filePath, false); err != nil {
		return PinnedBlobChunk{}, err
	}
	if offset < 0 || !validPinnedLimit(metadataLimit) || !validPinnedLimit(chunkLimit) || !validPinnedLimit(maxPrefix) || chunkLimit > maxPrefix {
		return PinnedBlobChunk{}, ErrPinnedOffset
	}
	commitOID, err := p.oid(side)
	if err != nil {
		return PinnedBlobChunk{}, err
	}
	release, err := p.manager.Git.ReadSlot(ctx)
	if err != nil {
		return PinnedBlobChunk{}, fmt.Errorf("%w: %w", ErrPinnedRepositoryBusy, err)
	}
	defer release()
	var blob PinnedBlobChunk
	err = p.withReadLock(ctx, func(repositoryPath string) error {
		if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, commitOID); err != nil {
			return err
		}
		entry, err := lookupPinnedTreeEntry(ctx, p.manager.Git, repositoryPath, commitOID, filePath, metadataLimit)
		if err != nil {
			return err
		}
		if entry.Type == "commit" {
			return ErrPinnedUnsupportedObject
		}
		if entry.Type != "blob" || entry.Size < 0 {
			return errTreeEntryNotFound
		}
		// The same rule as the ordinary blob read: a listed size above the
		// server read bound is refused before Git rebuilds the object, and a
		// smaller object whose stored delta rebuilds a larger base is refused
		// because the whole set was priced in this lock's own terms.
		if bound := p.manager.Git.ReadBound(); bound > 0 && entry.Size > bound {
			return ErrPinnedBlobTooLarge
		}
		if over, err := p.manager.blobsAboveMemoryLineWithin(ctx, repositoryPath, []string{entry.OID}); err != nil {
			return err
		} else if over {
			return ErrPinnedBlobTooLarge
		}
		if offset > entry.Size {
			return ErrPinnedOffset
		}
		blob = PinnedBlobChunk{
			Path: filePath, OID: entry.OID, Mode: entry.Mode, Offset: offset,
			Size: entry.Size, Symlink: entry.Mode == "120000",
		}
		if offset == entry.Size {
			return nil
		}
		if offset >= maxPrefix {
			return ErrPinnedOutputLimit
		}
		end := offset + chunkLimit
		if end < offset || end > maxPrefix {
			end = maxPrefix
		}
		if end > entry.Size {
			end = entry.Size
		}
		result, runErr := runPinnedGit(ctx, p.manager.Git, end, repositoryPath, "cat-file", "blob", entry.OID)
		var limitErr *gitexec.LimitError
		stdoutLimited := errors.As(runErr, &limitErr) && limitErr.Stream == "stdout"
		if runErr != nil && !stdoutLimited {
			return classifyPinnedGitError(ctx, runErr)
		}
		if int64(len(result.Stdout)) < end || offset > int64(len(result.Stdout)) {
			return ErrPinnedObjectUnavailable
		}
		blob.Content = bytes.Clone(result.Stdout[offset:end])
		blob.Binary = bytes.IndexByte(blob.Content, 0) >= 0
		blob.HasMore = end < entry.Size
		return nil
	})
	if err != nil {
		return PinnedBlobChunk{}, err
	}
	return blob, nil
}

func (p *PinnedRepository) oid(side PinnedSide) (string, error) {
	switch side {
	case PinnedBase:
		return p.baseOID, nil
	case PinnedHead:
		return p.headOID, nil
	default:
		return "", errors.New("invalid pinned side")
	}
}

// WhilePresent runs record under the repository read lock after verifying
// again that the pinned commits exist. Unused object cleanup reads the
// unfinished check jobs under the write lock, so a job that record admits
// is either seen by cleanup, which then waits, or refused here because
// cleanup already removed its source. record must not take the repository
// lock.
func (p *PinnedRepository) WhilePresent(ctx context.Context, record func() error) error {
	return p.whilePresent(ctx, "", "", record)
}

// WhileRefPresent verifies an exact full branch ref under the same read lock
// that protects the admission callback from OwnGit repository writers.
func (p *PinnedRepository) WhileRefPresent(ctx context.Context, ref, oid string, record func() error) error {
	if !strings.HasPrefix(ref, "refs/heads/") || !isOID(oid) || oid != p.headOID {
		return ErrPinnedRefMoved
	}
	return p.whilePresent(ctx, ref, oid, record)
}

func (p *PinnedRepository) whilePresent(ctx context.Context, ref, oid string, record func() error) error {
	return p.withReadLock(ctx, func(repositoryPath string) error {
		for _, pinnedOID := range []string{p.baseOID, p.headOID} {
			if err := verifyPinnedCommit(ctx, p.manager.Git, repositoryPath, pinnedOID); err != nil {
				return fmt.Errorf("verify pinned commit: %w", err)
			}
		}
		if ref != "" {
			result, err := p.manager.Git.Run(ctx, repositoryPath, nil, "--git-dir", ".", "for-each-ref", "--format=%(refname)%00%(objectname)", ref)
			if err != nil {
				return fmt.Errorf("verify pinned ref: %w", err)
			}
			matched := false
			for _, line := range strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n") {
				name, currentOID, ok := strings.Cut(line, "\x00")
				if name == ref {
					matched = ok && currentOID == oid
					break
				}
			}
			if !matched {
				return ErrPinnedRefMoved
			}
		}
		return record()
	})
}

func (p *PinnedRepository) withReadLock(ctx context.Context, operation func(string) error) error {
	lock := p.manager.Locks.For(p.id)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !lock.TryRLock() {
		p.manager.NoteRepositoryUse(p.id)
		return ErrPinnedRepositoryBusy
	}
	defer lock.RUnlock()
	path, _, exists, err := p.manager.ExistingPath(ctx, p.id)
	if err != nil {
		return err
	}
	if !exists || filepath.Clean(path) != p.path {
		return ErrPinnedRepositoryChanged
	}
	identity, err := captureDirectoryIdentity(path)
	if err != nil {
		return fmt.Errorf("inspect pinned repository identity: %w", err)
	}
	if !os.SameFile(p.identity, identity) {
		return ErrPinnedRepositoryChanged
	}
	return operation(path)
}

// captureDirectoryIdentity uses handle-based Stat so Windows records
// the volume and file index while this path still names the inspected
// directory. Path-based Stat may defer that lookup until os.SameFile, after a
// replacement has reused the path.
func captureDirectoryIdentity(path string) (os.FileInfo, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	identity, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil {
		return nil, errors.Join(statErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !identity.IsDir() {
		return nil, errors.New("repository path is not a directory")
	}
	return identity, nil
}

func pinnedObjectFormat(ctx context.Context, runner *gitexec.Runner, repositoryPath string) (string, error) {
	result, err := runPinnedGit(ctx, runner, 1024, repositoryPath, "rev-parse", "--show-object-format")
	if err != nil {
		return "", classifyPinnedGitError(ctx, err)
	}
	format := strings.TrimSpace(string(result.Stdout))
	if format != "sha1" && format != "sha256" {
		return "", ErrPinnedUnsupportedObject
	}
	return format, nil
}

func verifyPinnedCommit(ctx context.Context, runner *gitexec.Runner, repositoryPath, oid string) error {
	result, err := runPinnedGit(ctx, runner, 1024, repositoryPath, "cat-file", "-t", oid)
	if err != nil {
		return classifyPinnedGitError(ctx, err)
	}
	if strings.TrimSpace(string(result.Stdout)) != "commit" {
		return ErrPinnedObjectUnavailable
	}
	return nil
}

func lookupPinnedTreeEntry(ctx context.Context, runner *gitexec.Runner, repositoryPath, rootOID, filePath string, limit int64) (TreeEntry, error) {
	result, err := runPinnedGit(ctx, runner, limit, repositoryPath,
		"ls-tree", "-z", "-l", rootOID, "--", ":(top,literal)"+filePath)
	if err != nil {
		return TreeEntry{}, classifyPinnedGitError(ctx, err)
	}
	records := bytes.Split(bytes.TrimSuffix(result.Stdout, []byte{0}), []byte{0})
	if len(records) == 1 && len(records[0]) == 0 {
		return TreeEntry{}, errTreeEntryNotFound
	}
	if len(records) != 1 {
		return TreeEntry{}, errors.New("Git returned ambiguous pinned tree lookup data")
	}
	entry, err := parseTreeEntry(records[0])
	if err != nil {
		return TreeEntry{}, err
	}
	if entry.Name != filePath {
		return TreeEntry{}, errors.New("Git returned mismatched pinned tree lookup data")
	}
	entry.Path = filePath
	if separator := strings.LastIndexByte(filePath, '/'); separator >= 0 {
		entry.Name = filePath[separator+1:]
	}
	return entry, nil
}

func listPinnedTree(ctx context.Context, runner *gitexec.Runner, repositoryPath, treeOID, prefix string, limit int64) ([]TreeEntry, error) {
	result, err := runPinnedGit(ctx, runner, limit, repositoryPath, "ls-tree", "-z", "-l", treeOID)
	if err != nil {
		return nil, classifyPinnedGitError(ctx, err)
	}
	var entries []TreeEntry
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		entry, err := parseTreeEntry(record)
		if err != nil {
			return nil, err
		}
		entry.Path = entry.Name
		if prefix != "" {
			entry.Path = prefix + "/" + entry.Name
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func runPinnedGit(ctx context.Context, runner *gitexec.Runner, limit int64, repositoryPath string, arguments ...string) (gitexec.Result, error) {
	if runner == nil || !validPinnedLimit(limit) {
		return gitexec.Result{}, errors.New("pinned Git execution is unavailable")
	}
	bounded := *runner
	bounded.OutputLimit = limit
	args := []string{"--no-replace-objects", "--git-dir=."}
	args = append(args, arguments...)
	return bounded.RunWithEnvironment(ctx, repositoryPath, nil, []string{
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0",
	}, args...)
}

func classifyPinnedGitError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var limitErr *gitexec.LimitError
	if errors.As(err, &limitErr) && limitErr.Stream == "stdout" {
		return ErrPinnedOutputLimit
	}
	return ErrPinnedObjectUnavailable
}

// ValidatePinnedPath rejects host-style absolute paths and traversal while
// preserving literal repository path characters.
func ValidatePinnedPath(value string, allowRoot bool) error {
	if value == "" {
		if allowRoot {
			return nil
		}
		return errors.New("repository file path is required")
	}
	if err := validateTreePath(value); err != nil {
		return err
	}
	if strings.HasPrefix(value, `\\`) || isWindowsAbsolutePath(value) || filepath.IsAbs(value) {
		return errors.New("invalid repository path")
	}
	return nil
}

func isWindowsAbsolutePath(value string) bool {
	if len(value) < 3 || value[1] != ':' || value[2] != '/' && value[2] != '\\' {
		return false
	}
	first := value[0]
	return first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
}

func validPinnedLimit(limit int64) bool {
	return limit > 0 && uint64(limit) <= uint64(^uint(0)>>1)
}

package importsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	headAbsent   = "absent"
	headSymbolic = "symbolic"
	headDetached = "detached"
)

type headIdentity struct {
	kind   string
	target string
	oid    string
}

func (h headIdentity) encode() string {
	switch h.kind {
	case headAbsent:
		return headAbsent
	case headSymbolic:
		return headSymbolic + " " + h.target + " " + h.oid
	case headDetached:
		return headDetached + " " + h.oid
	default:
		return ""
	}
}

func decodeHeadIdentity(value string) (headIdentity, error) {
	if value == headAbsent {
		return headIdentity{kind: headAbsent}, nil
	}
	fields := strings.Split(value, " ")
	switch {
	case len(fields) == 3 && fields[0] == headSymbolic && validBranchRef(fields[1]) && (fields[2] == "" || validHeadOID(fields[2])):
		return headIdentity{kind: headSymbolic, target: fields[1], oid: fields[2]}, nil
	case len(fields) == 2 && fields[0] == headDetached && validHeadOID(fields[1]):
		return headIdentity{kind: headDetached, oid: fields[1]}, nil
	default:
		return headIdentity{}, errors.New("invalid encoded HEAD identity")
	}
}

func validBranchRef(value string) bool {
	return strings.HasPrefix(value, "refs/heads/") && len(value) > len("refs/heads/") && !strings.ContainsAny(value, " \000\r\n")
}

func validHeadOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

// sameHEADIdentity compares the identity Git writes in HEAD. A symbolic HEAD
// is identified by its immediate target, not by the target's changing tip.
func sameHEADIdentity(left, right headIdentity) bool {
	if left.kind != right.kind {
		return false
	}
	switch left.kind {
	case headAbsent:
		return true
	case headSymbolic:
		return left.target == right.target
	case headDetached:
		return left.oid == right.oid
	default:
		return false
	}
}

func sameHEADObservation(left, right headIdentity) bool {
	if !sameHEADIdentity(left, right) {
		return false
	}
	if left.kind == headSymbolic {
		return left.oid == right.oid
	}
	return true
}

func sourceHEADIdentity(advertisement *importgit.Advertisement) headIdentity {
	target := headSymrefTarget(advertisement)
	if target != "" {
		oid := ""
		if advertisement.Head.Advertised {
			oid = advertisement.Head.OID
		}
		return headIdentity{kind: headSymbolic, target: target, oid: oid}
	}
	if advertisement.Head.Advertised && advertisement.Head.OID != "" {
		return headIdentity{kind: headDetached, oid: advertisement.Head.OID}
	}
	return headIdentity{kind: headAbsent}
}

func destinationHEADIdentity(symref, oid string) (headIdentity, error) {
	switch {
	case symref != "":
		if !validBranchRef(symref) || (oid != "" && !validHeadOID(oid)) {
			return headIdentity{}, errors.New("destination has an unsupported symbolic HEAD")
		}
		return headIdentity{kind: headSymbolic, target: symref, oid: oid}, nil
	case oid != "":
		if !validHeadOID(oid) {
			return headIdentity{}, errors.New("destination has an invalid detached HEAD")
		}
		return headIdentity{kind: headDetached, oid: oid}, nil
	default:
		return headIdentity{}, errors.New("destination HEAD identity is unavailable")
	}
}

func parseRawHEAD(content []byte) (headIdentity, error) {
	line := strings.TrimSuffix(string(content), "\n")
	if strings.HasSuffix(line, "\r") || strings.Contains(line, "\n") {
		return headIdentity{}, errors.New("HEAD has unsupported content")
	}
	if strings.HasPrefix(line, "ref: ") {
		target := strings.TrimPrefix(line, "ref: ")
		if !validBranchRef(target) {
			return headIdentity{}, errors.New("HEAD has an unsupported symbolic target")
		}
		return headIdentity{kind: headSymbolic, target: target}, nil
	}
	if validHeadOID(line) {
		return headIdentity{kind: headDetached, oid: line}, nil
	}
	return headIdentity{}, errors.New("HEAD has unsupported content")
}

func rawHEADContent(identity headIdentity) ([]byte, error) {
	switch identity.kind {
	case headSymbolic:
		if !validBranchRef(identity.target) {
			return nil, errors.New("invalid symbolic HEAD target")
		}
		return []byte("ref: " + identity.target + "\n"), nil
	case headDetached:
		if !validHeadOID(identity.oid) {
			return nil, errors.New("invalid detached HEAD object")
		}
		return []byte(identity.oid + "\n"), nil
	default:
		return nil, errors.New("an absent HEAD cannot be written")
	}
}

// directRegularFile and directDirectory apply the platform indirect-path check
// (symlink on Unix, any reparse point on Windows) on top of the mode checks so
// every HEAD-related path uses one rule.
func directRegularFile(path string, info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	indirect, err := refPathIsIndirect(path, info)
	return err == nil && !indirect
}

func directDirectory(path string, info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	indirect, err := refPathIsIndirect(path, info)
	return err == nil && !indirect
}

type headLock struct {
	path               string
	headPath           string
	repositoryPath     string
	file               *os.File
	identity           os.FileInfo
	repositoryIdentity os.FileInfo
	expected           headIdentity
	committed          bool
}

func (s *Service) acquireHEADLock(ctx context.Context, run *runState, repositoryPath string, expected headIdentity) (*headLock, error) {
	storage, err := s.repositoryRefStorage(ctx, run, repositoryPath)
	if err != nil {
		return nil, err
	}
	if storage != "files" {
		return nil, newProblem(CodeUnsupported, fmt.Sprintf("destination ref storage %q does not support exact HEAD publication", storage), nil)
	}
	if expected.kind == headAbsent {
		return nil, newProblem(CodeUnsupported, "an absent destination HEAD cannot be updated safely", nil)
	}
	// The identity must be fixed now: it is compared after the lock is created,
	// and a path-based Windows Lstat would resolve it only at that comparison.
	repositoryIdentity, err := state.LstatIdentity(repositoryPath)
	if err != nil || !directDirectory(repositoryPath, repositoryIdentity) {
		return nil, newProblem(CodeRepositoryMissing, "destination repository path is not a stable real directory", err)
	}
	headPath := filepath.Join(repositoryPath, "HEAD")
	if _, err := readRawHEAD(headPath); err != nil {
		return nil, newProblem(CodePublishFailed, "destination HEAD is not a direct regular file", err)
	}
	lockPath := filepath.Join(repositoryPath, "HEAD.lock")
	// O_EXCL refuses any existing pathname, including a link or reparse point,
	// without following it. Such a pathname belongs to someone else.
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, newProblem(CodeDestinationChanged, "destination HEAD is locked by another Git writer", err)
	}
	locked, err := file.Stat()
	if err == nil {
		var onDisk os.FileInfo
		onDisk, err = os.Lstat(lockPath)
		if err == nil && (!directRegularFile(lockPath, onDisk) || !os.SameFile(locked, onDisk)) {
			err = errors.New("owned HEAD lock pathname is not the created direct regular file")
		}
	}
	if err != nil {
		_ = file.Close()
		if locked != nil {
			_ = removeOwnedHEADLock(lockPath, locked)
		}
		return nil, newProblem(CodePublishFailed, "owned HEAD lock could not be inspected", err)
	}
	lock := &headLock{
		path: lockPath, headPath: headPath, repositoryPath: repositoryPath,
		file: file, identity: locked, repositoryIdentity: repositoryIdentity, expected: expected,
	}
	if err := lock.checkRepositoryIdentity(); err != nil {
		_ = lock.rollback()
		return nil, newProblem(CodeRepositoryMissing, "destination repository changed while HEAD was locked", err)
	}
	actual, err := readRawHEAD(lock.headPath)
	if err != nil {
		_ = lock.rollback()
		return nil, newProblem(CodePublishFailed, "destination HEAD could not be read while locked", err)
	}
	if !sameHEADIdentity(actual, expected) {
		_ = lock.rollback()
		return nil, newProblem(CodeDestinationChanged, "destination HEAD changed before its write lock was acquired", nil)
	}
	return lock, nil
}

func (s *Service) repositoryRefStorage(ctx context.Context, run *runState, repositoryPath string) (string, error) {
	result, err := s.Repositories.Git.RunWithLimits(ctx, repositoryPath, nil, run.limits.commandLimits(run.limits.PublishTimeout),
		"--git-dir", ".", "config", "--local", "--get", "extensions.refStorage")
	if err != nil {
		if code, ok := gitexec.ExitCode(err); ok && code == 1 {
			return "files", nil
		}
		// A Git command cut by the run's own stop says nothing about the
		// repository.
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return "", stoppedProblem(ctx, "while identifying the destination ref storage", err)
		}
		return "", newProblem(CodeRepositoryMissing, "destination ref storage could not be identified", err)
	}
	storage := strings.TrimSpace(string(result.Stdout))
	if storage == "" || storage == "files" {
		return "files", nil
	}
	return storage, nil
}

type looseRefIdentity struct {
	exists   bool
	symbolic bool
	target   string
	oid      string
}

func validateDirectRefPath(repositoryPath, refName string) error {
	root, err := os.Lstat(repositoryPath)
	if err != nil || !root.IsDir() {
		return errors.New("repository is not a stable real directory")
	}
	indirect, err := refPathIsIndirect(repositoryPath, root)
	if err != nil || indirect {
		return errors.New("repository path is indirect or cannot be inspected")
	}
	current := repositoryPath
	parts := strings.Split(refName, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("ref has an unsafe path component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		indirect, err := refPathIsIndirect(current, info)
		if err != nil || indirect {
			return errors.New("ref path traverses a filesystem link or reparse point")
		}
		if index < len(parts)-1 {
			if !info.IsDir() {
				return errors.New("ref parent is not a directory")
			}
		} else if !info.Mode().IsRegular() {
			return errors.New("loose ref is not a regular file")
		}
	}
	return nil
}

func validatePackedRefsPath(repositoryPath string) error {
	path := filepath.Join(repositoryPath, "packed-refs")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	indirect, indirectErr := refPathIsIndirect(path, info)
	if !info.Mode().IsRegular() || indirectErr != nil || indirect {
		return errors.New("packed-refs is not a regular direct path")
	}
	return nil
}

func readLooseRefIdentity(repositoryPath, refName string) (looseRefIdentity, error) {
	if err := validateDirectRefPath(repositoryPath, refName); err != nil {
		return looseRefIdentity{}, err
	}
	path := filepath.Join(repositoryPath, filepath.FromSlash(refName))
	before, err := state.LstatIdentity(path)
	if os.IsNotExist(err) {
		return looseRefIdentity{}, nil
	}
	if err != nil {
		return looseRefIdentity{}, err
	}
	indirect, indirectErr := refPathIsIndirect(path, before)
	if !before.Mode().IsRegular() || indirectErr != nil || indirect {
		return looseRefIdentity{}, errors.New("loose ref is not a regular direct path")
	}
	file, err := os.Open(path)
	if err != nil {
		return looseRefIdentity{}, err
	}
	opened, statErr := file.Stat()
	content, readErr := io.ReadAll(io.LimitReader(file, 1025))
	closeErr := file.Close()
	if statErr != nil || readErr != nil || closeErr != nil {
		return looseRefIdentity{}, errors.Join(statErr, readErr, closeErr)
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return looseRefIdentity{}, errors.New("loose ref changed while it was read")
	}
	indirect, indirectErr = refPathIsIndirect(path, after)
	if indirectErr != nil || indirect {
		return looseRefIdentity{}, errors.New("loose ref became an indirect path while it was read")
	}
	if err := validateDirectRefPath(repositoryPath, refName); err != nil {
		return looseRefIdentity{}, err
	}
	if len(content) == 0 || len(content) > 1024 {
		return looseRefIdentity{}, errors.New("loose ref content is empty or exceeds its bound")
	}
	line := strings.TrimSuffix(string(content), "\n")
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return looseRefIdentity{}, errors.New("loose ref content has an invalid line structure")
	}
	if strings.HasPrefix(line, "ref: ") {
		target := strings.TrimPrefix(line, "ref: ")
		if target == "" {
			return looseRefIdentity{}, errors.New("symbolic loose ref has an empty target")
		}
		return looseRefIdentity{exists: true, symbolic: true, target: target}, nil
	}
	if !validHeadOID(line) {
		return looseRefIdentity{}, errors.New("direct loose ref has an invalid object identity")
	}
	return looseRefIdentity{exists: true, oid: line}, nil
}

func readRawHEAD(path string) (headIdentity, error) {
	before, err := state.LstatIdentity(path)
	if err != nil {
		return headIdentity{}, err
	}
	if !directRegularFile(path, before) {
		return headIdentity{}, errors.New("HEAD is not a direct regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return headIdentity{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return headIdentity{}, errors.New("HEAD changed while it was opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return headIdentity{}, err
	}
	if len(content) > 1024 {
		return headIdentity{}, errors.New("HEAD exceeds its supported size")
	}
	after, err := os.Lstat(path)
	if err != nil || !directRegularFile(path, after) || !os.SameFile(opened, after) {
		return headIdentity{}, errors.New("HEAD changed while it was read")
	}
	return parseRawHEAD(content)
}

func (lock *headLock) checkRepositoryIdentity() error {
	current, err := os.Lstat(lock.repositoryPath)
	if err != nil {
		return err
	}
	if !directDirectory(lock.repositoryPath, current) || !os.SameFile(lock.repositoryIdentity, current) {
		return errors.New("destination repository directory was replaced")
	}
	return nil
}

func (lock *headLock) commit(desired headIdentity) error {
	if err := lock.checkRepositoryIdentity(); err != nil {
		return err
	}
	content, err := rawHEADContent(desired)
	if err != nil {
		return err
	}
	if _, err := lock.file.Write(content); err != nil {
		return fmt.Errorf("write owned HEAD lock: %w", err)
	}
	if err := lock.file.Sync(); err != nil {
		return fmt.Errorf("sync owned HEAD lock: %w", err)
	}
	if err := lock.file.Close(); err != nil {
		lock.file = nil
		return fmt.Errorf("close owned HEAD lock: %w", err)
	}
	lock.file = nil
	if err := lock.checkRepositoryIdentity(); err != nil {
		return err
	}
	actual, err := readRawHEAD(lock.headPath)
	if err != nil {
		return fmt.Errorf("recheck destination HEAD: %w", err)
	}
	if !sameHEADIdentity(actual, lock.expected) {
		return errors.New("destination HEAD changed while its lock was held")
	}
	if err := lock.checkLockIdentity(); err != nil {
		return err
	}
	if err := os.Rename(lock.path, lock.headPath); err != nil {
		return fmt.Errorf("commit owned HEAD lock: %w", err)
	}
	lock.committed = true
	return nil
}

func (lock *headLock) checkLockIdentity() error {
	current, err := os.Lstat(lock.path)
	if err != nil {
		return fmt.Errorf("inspect owned HEAD lock before commit: %w", err)
	}
	if !directRegularFile(lock.path, current) || !os.SameFile(current, lock.identity) {
		return errors.New("owned HEAD lock path was replaced before commit")
	}
	return nil
}

func (lock *headLock) rollback() error {
	if lock == nil || lock.committed {
		return nil
	}
	var closeErr error
	if lock.file != nil {
		closeErr = lock.file.Close()
		lock.file = nil
	}
	if err := lock.checkRepositoryIdentity(); err != nil {
		return errors.Join(closeErr, fmt.Errorf("preserving HEAD lock: %w", err))
	}
	return errors.Join(closeErr, removeOwnedHEADLock(lock.path, lock.identity))
}

func removeOwnedHEADLock(path string, identity os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity == nil || !directRegularFile(path, current) || !os.SameFile(identity, current) {
		return errors.New("HEAD lock ownership changed; preserving it")
	}
	return os.Remove(path)
}

func detachedHEADRetentionNames(oid string) []string {
	return []string{
		repository.RetainedRefName("detached-heads", oid),
		repository.ProvenanceRefName("detached-heads", "HEAD", oid),
	}
}

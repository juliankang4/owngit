package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// This file classifies an existing state database before Open changes any
// permission or opens SQLite read-write. A refused database keeps every byte,
// entry and mode. The classification is not migration authority: initialize
// reclassifies through the real connection before it writes.

// schemaClass is the accepted state of a schema. version is set only for a
// released schema, so an opener that inspected one released version refuses a
// database that another opener left at a different one.
type schemaClass struct {
	kind    schemaKind
	version int
}

type schemaKind int

const (
	kindEmpty schemaKind = iota
	kindBaseline
	// kindReleased is a schema that an earlier release wrote. It is upgraded
	// in place like the baseline.
	kindReleased
	kindCurrent
)

var (
	schemaEmpty    = schemaClass{kind: kindEmpty}
	schemaBaseline = schemaClass{kind: kindBaseline}
	schemaCurrent  = schemaClass{kind: kindCurrent}
)

func schemaReleased(version int) schemaClass {
	return schemaClass{kind: kindReleased, version: version}
}

// ErrInspectionUnstable reports that the state directory changed while it was
// being inspected. The source is untouched and the operation can be retried.
var ErrInspectionUnstable = errors.New("state directory changed during inspection; retry the operation")

const (
	walSuffix     = "-wal"
	shmSuffix     = "-shm"
	journalSuffix = "-journal"
	copyChunkSize = 1 << 20
)

// preflightHooks are test seams at the inspection responsibility boundaries:
// where the private directory is created, where private bytes are written and
// at the named points passed to at. Production leaves every field zero.
var preflightHooks struct {
	temporaryRoot string
	privateWriter func(file *os.File) io.Writer
	at            func(point string, in *inspection, privateDir string) error
}

// Named inspection points passed to preflightHooks.at.
const (
	pointMainListed = "main listed"
	pointListed     = "listed"
	pointCapture    = "capture"
	pointHashed     = "hashed"
	pointClassify   = "classify"
	pointClassified = "classified"
	pointAccept     = "accept"
)

// sourceObject binds one inspected filesystem object to the identity observed
// through an open handle, so a pathname replacement is detected later.
type sourceObject struct {
	path        string
	info        os.FileInfo
	fingerprint string
	handle      *os.File
}

// inspection is the result of classifying one state directory. It keeps the
// source handles until Open accepts or refuses the database.
type inspection struct {
	dir      *sourceObject
	main     *sourceObject
	wal      *sourceObject
	shm      *sourceObject
	class    schemaClass
	mainHash []byte
	walHash  []byte
}

// release closes every owned source handle and reports every close failure.
// A failed release means the inspected objects are no longer reliably bound,
// so the caller must not proceed to a writable open.
func (in *inspection) release() error {
	var err error
	for _, object := range []*sourceObject{in.dir, in.main, in.wal, in.shm} {
		if object == nil || object.handle == nil {
			continue
		}
		if closeErr := object.handle.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release %s: %w", filepath.Base(object.path), closeErr))
		}
		object.handle = nil
	}
	return err
}

// validateHeldObjects confirms that every identity handle is still usable
// before acceptance changes permissions. Path validation alone cannot prove
// that a closed or otherwise lost handle still binds the inspected object.
func (in *inspection) validateHeldObjects() error {
	for _, object := range []*sourceObject{in.dir, in.main, in.wal, in.shm} {
		if object == nil {
			continue
		}
		if object.handle == nil {
			return fmt.Errorf("inspect held %s: source handle is unavailable", filepath.Base(object.path))
		}
		info, err := object.handle.Stat()
		if err != nil {
			return fmt.Errorf("inspect held %s: %w", filepath.Base(object.path), err)
		}
		if !os.SameFile(info, object.info) {
			return unstable("held %s identity changed", filepath.Base(object.path))
		}
	}
	return nil
}

// inspectState classifies the database under dir without protecting the
// directory or opening the original files through SQLite read-write.
func inspectState(ctx context.Context, dir string) (result *inspection, err error) {
	in := &inspection{}
	succeeded := false
	defer func() {
		if !succeeded {
			err = errors.Join(err, in.release())
		}
	}()
	directory, err := bindDirectory(dir)
	if err != nil {
		return nil, err
	}
	in.dir = directory
	mainPath := filepath.Join(dir, databaseName)
	mainInfo, err := lstatSourceEntry(mainPath)
	if err != nil {
		return nil, err
	}
	if err := in.at(pointMainListed, ""); err != nil {
		return nil, err
	}
	walInfo, err := lstatSourceEntry(mainPath + walSuffix)
	if err != nil {
		return nil, err
	}
	shmInfo, err := lstatSourceEntry(mainPath + shmSuffix)
	if err != nil {
		return nil, err
	}
	journalPresent, err := rollbackJournalPresent(mainPath)
	if err != nil {
		return nil, err
	}
	if journalPresent {
		return nil, errRollbackJournal
	}
	if err := in.at(pointListed, ""); err != nil {
		return nil, err
	}
	if mainInfo == nil {
		if walInfo != nil || shmInfo != nil {
			// The entries are listed one at a time, so a database that
			// another opener created after its listing, and then opened,
			// can show only its recovery files here.
			if info, err := lstatSourceEntry(mainPath); err != nil {
				return nil, err
			} else if info != nil {
				return nil, unstable("%s appeared during inspection", databaseName)
			}
			return nil, errors.New("state database is missing but its recovery files exist; restore the database or remove the directory deliberately")
		}
		in.class = schemaEmpty
		succeeded = true
		return in, nil
	}
	in.main, err = bindFile(mainPath, mainInfo, false)
	if err != nil {
		return nil, err
	}
	if walInfo == nil && shmInfo == nil {
		err = in.inspectImmutable(ctx)
	} else {
		err = in.inspectPrivateCopy(ctx, walInfo, shmInfo)
	}
	if err != nil {
		return nil, err
	}
	succeeded = true
	return in, nil
}

// inspectImmutable reads a sidecar-free database through SQLite's immutable
// mode, which changes no file. The result is bound to the file identity, size
// and modification time, and to its hash when it will be migrated.
func (in *inspection) inspectImmutable(ctx context.Context) error {
	db, err := sql.Open("sqlite", sqliteURI(in.main.path, "immutable=1&mode=ro"))
	if err != nil {
		return fmt.Errorf("inspect state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	class, classifyErr := classifySchema(ctx, db)
	if closeErr := db.Close(); closeErr != nil {
		classifyErr = errors.Join(classifyErr, fmt.Errorf("close state database inspection: %w", closeErr))
	}
	if classifyErr == nil && class != schemaCurrent {
		in.mainHash, classifyErr = hashExact(ctx, in.main.handle, in.main.info.Size(), io.Discard)
	}
	if err := in.at(pointClassified, ""); err != nil {
		return err
	}
	if err := in.validateSource(true, true); err != nil {
		return err
	}
	if classifyErr != nil {
		return classifyErr
	}
	in.class = class
	return nil
}

// inspectPrivateCopy classifies a database whose WAL or SHM exists. SQLite may
// only touch a private owner-only copy of the database and WAL. The SHM is
// tracked by identity and never read, copied or mapped.
func (in *inspection) inspectPrivateCopy(ctx context.Context, walInfo, shmInfo os.FileInfo) (err error) {
	if walInfo != nil {
		if in.wal, err = bindFile(in.main.path+walSuffix, walInfo, false); err != nil {
			return err
		}
	}
	if shmInfo != nil {
		if in.shm, err = bindFile(in.main.path+shmSuffix, shmInfo, true); err != nil {
			return err
		}
	}
	staging, err := createPrivateStaging()
	if err != nil {
		return err
	}
	privateDir := staging
	defer func() {
		if privateDir == "" {
			return
		}
		if removeErr := os.RemoveAll(privateDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private inspection copy %s: %w", privateDir, removeErr))
		}
	}()
	if err := in.at(pointCapture, staging); err != nil {
		return err
	}
	privateMain := filepath.Join(staging, databaseName)
	in.mainHash, err = copyPrivate(ctx, in.main, privateMain)
	if err != nil {
		return err
	}
	if in.wal != nil {
		in.walHash, err = copyPrivate(ctx, in.wal, privateMain+walSuffix)
		if err != nil {
			return err
		}
	}
	if err := in.at(pointHashed, staging); err != nil {
		return err
	}
	if err := in.verifyHashes(ctx); err != nil {
		return err
	}
	if err := in.validateSource(true, true); err != nil {
		return err
	}
	if err := in.at(pointClassify, staging); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteFileURI(privateMain))
	if err != nil {
		return fmt.Errorf("inspect private state copy: %w", err)
	}
	db.SetMaxOpenConns(1)
	class, classifyErr := classifySchema(ctx, db)
	if closeErr := db.Close(); closeErr != nil {
		classifyErr = errors.Join(classifyErr, fmt.Errorf("close private state copy: %w", closeErr))
	}
	if err := in.at(pointClassified, staging); err != nil {
		return err
	}
	// The private copy is removed before the result is used. A removal
	// failure joins the outcome and blocks the writable open.
	privateDir = ""
	if removeErr := os.RemoveAll(staging); removeErr != nil {
		return errors.Join(classifyErr, fmt.Errorf("remove private inspection copy %s: %w", staging, removeErr))
	}
	if err := in.validateSource(true, true); err != nil {
		return err
	}
	if classifyErr != nil {
		return classifyErr
	}
	in.class = class
	return nil
}

// accept validates the inspected objects, applies the private protections and
// validates the objects again, so the writable open uses exactly what was
// classified. A fresh directory is accepted only while it is still the same
// directory with no database, sidecar or journal. Migrating classifications
// also require their bound hashes.
func (in *inspection) accept(ctx context.Context, dir string) error {
	if err := in.at(pointAccept, ""); err != nil {
		return err
	}
	if err := in.validateHeldObjects(); err != nil {
		return err
	}
	exact := in.class != schemaCurrent
	if err := in.validateSource(exact, true); err != nil {
		return err
	}
	if err := ProtectPrivatePath(dir, true); err != nil {
		return fmt.Errorf("protect state directory: %w", err)
	}
	if in.main != nil {
		if err := ProtectPrivatePath(in.main.path, false); err != nil {
			return fmt.Errorf("protect state database file: %w", err)
		}
	}
	if err := in.validateSource(exact, false); err != nil {
		return err
	}
	if exact {
		return in.verifyHashes(ctx)
	}
	return nil
}

// validateSource compares the current directory entries with the bound
// identities. A fresh inspection has no bound database, so the database must
// still be absent. exact additionally requires unchanged sizes, modification
// times and sidecar set, which the migrating classifications and every
// inspection window need. fingerprints compares modes or security
// descriptors; it is skipped after Open has applied its own protection.
func (in *inspection) validateSource(exact, fingerprints bool) error {
	dirInfo, err := os.Stat(in.dir.path)
	if errors.Is(err, os.ErrNotExist) {
		return unstable("state directory was removed")
	}
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	if !os.SameFile(dirInfo, in.dir.info) {
		return unstable("state directory was replaced")
	}
	if err := in.dir.compareFingerprint(fingerprints); err != nil {
		return err
	}
	mainPath := filepath.Join(in.dir.path, databaseName)
	if in.main == nil {
		info, err := lstatSourceEntry(mainPath)
		if err != nil {
			return err
		}
		if info != nil {
			return unstable("%s appeared during inspection", databaseName)
		}
	} else if err := in.main.validate(exact, fingerprints, true); err != nil {
		return err
	}
	// The SHM index is never read, so only its identity and presence matter.
	for _, sidecar := range []struct {
		object *sourceObject
		path   string
		exact  bool
	}{{in.wal, mainPath + walSuffix, exact}, {in.shm, mainPath + shmSuffix, false}} {
		if sidecar.object != nil {
			if err := sidecar.object.validate(sidecar.exact, fingerprints, exact); err != nil {
				return err
			}
			continue
		}
		info, err := lstatSourceEntry(sidecar.path)
		if err != nil {
			return err
		}
		if info != nil && exact {
			return unstable("%s appeared during inspection", filepath.Base(sidecar.path))
		}
	}
	// The initial snapshot already refused an existing journal, so one found
	// here appeared during inspection. An inspection failure keeps its cause.
	journalPresent, err := rollbackJournalPresent(mainPath)
	if err != nil {
		return err
	}
	if journalPresent {
		return unstable("%s appeared during inspection", databaseName+journalSuffix)
	}
	return nil
}

func (object *sourceObject) validate(exact, fingerprints, required bool) error {
	info, err := lstatSourceEntry(object.path)
	if err != nil {
		return err
	}
	name := filepath.Base(object.path)
	if info == nil {
		if required {
			return unstable("%s was removed", name)
		}
		return nil
	}
	if !os.SameFile(info, object.info) {
		return unstable("%s was replaced", name)
	}
	if exact && (info.Size() != object.info.Size() || !info.ModTime().Equal(object.info.ModTime())) {
		return unstable("%s changed", name)
	}
	return object.compareFingerprint(fingerprints)
}

func (object *sourceObject) compareFingerprint(enabled bool) error {
	if !enabled {
		return nil
	}
	fingerprint, err := protectionFingerprint(object.path)
	if errors.Is(err, os.ErrNotExist) {
		return unstable("%s was removed", filepath.Base(object.path))
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filepath.Base(object.path), err)
	}
	if fingerprint != object.fingerprint {
		return unstable("%s permissions changed", filepath.Base(object.path))
	}
	return nil
}

// verifyHashes re-reads the bound handles and requires the recorded hashes.
func (in *inspection) verifyHashes(ctx context.Context) error {
	for _, item := range []struct {
		object *sourceObject
		hash   []byte
	}{{in.main, in.mainHash}, {in.wal, in.walHash}} {
		if item.object == nil {
			continue
		}
		hash, err := hashExact(ctx, item.object.handle, item.object.info.Size(), io.Discard)
		if err != nil {
			return err
		}
		if !bytes.Equal(hash, item.hash) {
			return unstable("%s content changed", filepath.Base(item.object.path))
		}
	}
	return nil
}

func bindDirectory(dir string) (*sourceObject, error) {
	handle, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open state directory: %w", err)
	}
	info, err := handle.Stat()
	if err != nil {
		return nil, closeAfter(handle, fmt.Errorf("inspect state directory: %w", err))
	}
	if !info.IsDir() {
		return nil, closeAfter(handle, errors.New("state path is not a directory"))
	}
	fingerprint, err := protectionFingerprint(dir)
	if err != nil {
		return nil, closeAfter(handle, fmt.Errorf("inspect state directory: %w", err))
	}
	return &sourceObject{path: dir, info: info, fingerprint: fingerprint, handle: handle}, nil
}

// closeAfter closes a handle that will not be kept and joins its close error
// with the failure that made it unnecessary.
func closeAfter(handle *os.File, err error) error {
	if closeErr := handle.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("release %s: %w", filepath.Base(handle.Name()), closeErr))
	}
	return err
}

// bindFile opens a regular source file and requires the handle to name the
// same object as the path entry, which rejects a symbolic link or reparse point
// substituted between the two calls. A metadata-only handle carries identity
// without data access, which is all the SHM index may provide.
func bindFile(path string, entry os.FileInfo, metadataOnly bool) (*sourceObject, error) {
	handle, err := openSourceHandle(path, metadataOnly)
	if errors.Is(err, os.ErrNotExist) {
		// Listed a moment ago, so it was removed during inspection, for
		// example a WAL that another opener checkpointed and deleted.
		return nil, unstable("%s was removed", filepath.Base(path))
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	info, err := handle.Stat()
	if err != nil {
		return nil, closeAfter(handle, fmt.Errorf("inspect %s: %w", filepath.Base(path), err))
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, entry) {
		return nil, closeAfter(handle, unstable("%s was replaced", filepath.Base(path)))
	}
	fingerprint, err := protectionFingerprint(path)
	if err != nil {
		return nil, closeAfter(handle, fmt.Errorf("inspect %s: %w", filepath.Base(path), err))
	}
	return &sourceObject{path: path, info: info, fingerprint: fingerprint, handle: handle}, nil
}

// lstatSourceEntry returns nil for an absent entry and refuses any entry that
// is not a regular file, including symbolic links and reparse points. The
// identity is fixed at this call, because bindFile compares it after opening.
func lstatSourceEntry(path string) (os.FileInfo, error) {
	info, err := LstatIdentity(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("state database entry %s must be a regular file", filepath.Base(path))
	}
	return info, nil
}

// errRollbackJournal refuses an existing rollback journal without opening it.
// A journal may name a super-journal outside the state directory, and OwnGit
// only recovers write-ahead logs.
var errRollbackJournal = errors.New("state database has a rollback journal; OwnGit uses write-ahead logging and leaves rollback journals for the tool that created them")

// rollbackJournalPresent reports whether a rollback journal entry exists
// beside the database. It never opens the entry. Any entry type counts as
// present, and a filesystem failure other than absence is returned with its
// cause so the caller can distinguish presence from an inability to inspect.
func rollbackJournalPresent(mainPath string) (bool, error) {
	_, err := os.Lstat(mainPath + journalSuffix)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect state rollback journal: %w", err)
}

// createPrivateStaging creates an empty owner-only directory outside the state
// directory for the inspection copy.
func createPrivateStaging() (string, error) {
	staging, err := os.MkdirTemp(preflightHooks.temporaryRoot, "owngit-inspect-*")
	if err != nil {
		return "", fmt.Errorf("create private inspection directory: %w", err)
	}
	fail := func(err error) (string, error) {
		if removeErr := os.RemoveAll(staging); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private inspection directory %s: %w", staging, removeErr))
		}
		return "", err
	}
	if err := ProtectPrivatePath(staging, true); err != nil {
		return fail(fmt.Errorf("protect private inspection directory: %w", err))
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return fail(fmt.Errorf("inspect private inspection directory: %w", err))
	}
	if len(entries) != 0 {
		return fail(errors.New("private inspection directory is not empty"))
	}
	return staging, nil
}

// copyPrivate writes exactly the recorded length of the source into a new
// owner-only file and returns the hash of the copied bytes.
func copyPrivate(ctx context.Context, source *sourceObject, target string) ([]byte, error) {
	file, err := CreatePrivateFile(target)
	if err != nil {
		return nil, fmt.Errorf("create private copy of %s: %w", filepath.Base(source.path), err)
	}
	var writer io.Writer = file
	if preflightHooks.privateWriter != nil {
		writer = preflightHooks.privateWriter(file)
	}
	hash, err := hashExact(ctx, source.handle, source.info.Size(), writer)
	if closeErr := file.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close private copy of %s: %w", filepath.Base(source.path), closeErr))
	}
	if err != nil {
		return nil, err
	}
	return hash, nil
}

// hashExact reads exactly length bytes from the handle in bounded chunks,
// writing them to the sink while hashing. A shorter file is instability.
func hashExact(ctx context.Context, source *os.File, length int64, sink io.Writer) ([]byte, error) {
	if length < 0 {
		return nil, unstable("%s has a negative length", filepath.Base(source.Name()))
	}
	digest := sha256.New()
	buffer := make([]byte, copyChunkSize)
	var offset int64
	for offset < length {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("inspect %s: %w", filepath.Base(source.Name()), err)
		}
		chunk := buffer
		if remaining := length - offset; remaining < int64(len(chunk)) {
			chunk = chunk[:remaining]
		}
		read, err := source.ReadAt(chunk, offset)
		if err != nil && !(errors.Is(err, io.EOF) && read == len(chunk)) {
			if errors.Is(err, io.EOF) {
				return nil, unstable("%s shrank during inspection", filepath.Base(source.Name()))
			}
			return nil, fmt.Errorf("read %s: %w", filepath.Base(source.Name()), err)
		}
		if _, err := sink.Write(chunk[:read]); err != nil {
			return nil, fmt.Errorf("write private copy of %s: %w", filepath.Base(source.Name()), err)
		}
		digest.Write(chunk[:read])
		offset += int64(read)
	}
	return digest.Sum(nil), nil
}

func (in *inspection) at(point, privateDir string) error {
	if preflightHooks.at == nil {
		return nil
	}
	return preflightHooks.at(point, in, privateDir)
}

func unstable(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInspectionUnstable, fmt.Sprintf(format, arguments...))
}

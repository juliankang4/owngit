// Package checksource materializes the exact source of one committed Git tree
// into a new directory owned by the caller.
//
// It reads Git objects only. It never runs a checkout, an archive export, Git
// filters, hooks, submodule commands, or a large-file fetch, so committed
// attributes such as "text eol=crlf", "filter=...", and "export-ignore" cannot
// change or drop the bytes it writes. Unsupported entries fail the whole
// operation instead of being skipped.
//
// The destination is an ordinary directory. It is not a sandbox: any process
// running as the same operating-system user can read, change, or replace its
// contents, and this package makes no claim about later command execution.
//
// The materialized directory contains working-tree files only. It has no .git
// directory, no index, and no empty directories, because Git does not record
// them. Commands that need repository metadata will not find it here.
package checksource

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Supported repository hash algorithms.
const (
	FormatSHA1   = "sha1"
	FormatSHA256 = "sha256"
)

// Git file modes this package materializes. Every other mode, including
// symbolic links (120000) and gitlinks (160000), is refused.
const (
	ModeRegular    = "100644"
	ModeExecutable = "100755"
)

var (
	// ErrLimitExceeded reports that the source tree is larger than the
	// effective limits allow. Nothing is written when a limit is detected
	// before the destination is created.
	ErrLimitExceeded = errors.New("check source exceeds a materialization limit")
	// ErrUnsupportedEntry reports an entry kind this materializer refuses,
	// such as a symbolic link or a submodule reference.
	ErrUnsupportedEntry = errors.New("check source contains an unsupported entry")
	// ErrUnsafePath reports a repository path that cannot be materialized as a
	// plain file name, including Git control-directory aliases.
	ErrUnsafePath = errors.New("check source contains an unsafe path")
	// ErrPathConflict reports duplicate paths, a file and directory claiming
	// the same name, or a name the destination filesystem already holds, which
	// includes case-insensitive and normalization collisions.
	ErrPathConflict = errors.New("check source contains conflicting paths")
	// ErrDestinationExists reports that the destination already exists. The
	// caller's directory is left untouched.
	ErrDestinationExists = errors.New("materialization destination already exists")
	// ErrObjectMismatch reports Git object bytes that do not match the size or
	// object ID recorded in the tree.
	ErrObjectMismatch = errors.New("Git object bytes do not match the tree entry")
	// ErrInvalidDestination reports a destination path this package will not
	// create.
	ErrInvalidDestination = errors.New("invalid materialization destination")
	// ErrUnsupportedObjectFormat reports a repository hash algorithm this
	// package cannot verify.
	ErrUnsupportedObjectFormat = errors.New("unsupported repository object format")
)

// Entry is one raw Git tree entry as the source reported it.
type Entry struct {
	// Path is the repository path with "/" separators, relative to the tree root.
	Path string
	OID  string
	// Mode is the literal Git mode, such as "100644" or "120000".
	Mode string
	// Type is the Git object type, such as "blob", "tree", or "commit".
	Type string
	// Size is the blob size in bytes, or a negative value when Git reported no size.
	Size int64
}

// Source reads one fixed commit from Git object storage.
//
// Implementations must not resolve refs, read a working tree, run filters or
// hooks, or fetch missing objects. ListTree must report every entry reachable
// from the commit tree, including entries this package refuses, so unsupported
// content is never silently dropped.
type Source interface {
	// CommitOID is the exact commit being materialized.
	CommitOID() string
	// ObjectFormat is the repository hash algorithm, FormatSHA1 or FormatSHA256.
	ObjectFormat() string
	// ListTree returns every entry reachable from the commit tree, recursively,
	// without entering gitlinks. metadataLimit bounds the raw listing bytes.
	ListTree(ctx context.Context, metadataLimit int64) ([]Entry, error)
	// ReadBlob returns exactly size bytes of the blob named by oid.
	ReadBlob(ctx context.Context, oid string, size int64) ([]byte, error)
}

// Limits bound the work a single materialization may perform. Every bound is
// checked against the tree listing before the destination directory is
// created, so an oversized source never produces a partial export.
type Limits struct {
	// MaxEntries bounds the number of tree entries, including refused kinds.
	MaxEntries int
	// MaxFileBytes bounds one blob.
	MaxFileBytes int64
	// MaxTotalBytes bounds the sum of all blob sizes.
	MaxTotalBytes int64
	// MaxPathDepth bounds the number of path components.
	MaxPathDepth int
	// MaxPathBytes bounds one complete repository path.
	MaxPathBytes int
	// MaxNameBytes bounds one path component.
	MaxNameBytes int
	// MetadataLimit bounds the raw bytes of the tree listing.
	MetadataLimit int64
}

// DefaultLimits are deliberately modest. Materialization runs one Git object
// read per file, so a caller that raises MaxEntries also raises wall-clock
// cost and should bound it with Options.Timeout or a context deadline.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries:    20000,
		MaxFileBytes:  64 << 20,
		MaxTotalBytes: 256 << 20,
		MaxPathDepth:  64,
		MaxPathBytes:  1024,
		MaxNameBytes:  255,
		MetadataLimit: 16 << 20,
	}
}

// Options configure one materialization.
type Options struct {
	// Limits bound the export. Zero fields take their default value.
	Limits Limits
	// Timeout bounds the complete operation when positive. The caller's
	// context still applies.
	Timeout time.Duration
}

// FileRecord describes one materialized file.
type FileRecord struct {
	// Path is the repository path with "/" separators.
	Path string
	// OID is the Git blob object ID, verified against the written bytes.
	OID string
	// Mode is the original Git mode, preserved even when the destination
	// filesystem cannot represent it.
	Mode string
	Size int64
	// Executable reports the Git mode 100755.
	Executable bool
	// ExecutableApplied reports that the destination filesystem shows an owner
	// execute bit after writing. It is false on filesystems without POSIX
	// permission bits.
	ExecutableApplied bool
	// LFSPointer is set when the preserved bytes are a valid Git LFS pointer.
	// It describes the pointer text only. The referenced large object is never
	// fetched, resolved, or verified, so this is not evidence that the object
	// exists or that its digest matches.
	LFSPointer *LFSPointer
}

// Result describes a complete materialization. It is returned only when every
// entry was written, so an interrupted export never yields a manifest.
type Result struct {
	Destination     string
	CommitOID       string
	ObjectFormat    string
	EffectiveLimits Limits
	// Files are sorted by path.
	Files []FileRecord
	// TotalBytes is the sum of the written file sizes.
	TotalBytes int64
	// LFSPointerPaths lists the files whose bytes validated as Git LFS
	// pointers, sorted with Files. Every materialized file was checked against
	// the pointer grammar, so this is complete for the supported pointer
	// versions. It is not evidence that any referenced large object exists, was
	// retrieved, or matches its recorded digest.
	LFSPointerPaths []string
	// ExecutableBitsUnsupported reports that at least one executable entry did
	// not receive an execute bit on the destination filesystem.
	ExecutableBitsUnsupported bool
}

// EntryError identifies the tree entry that stopped a materialization.
type EntryError struct {
	Path   string
	Mode   string
	Type   string
	Detail string
	Err    error
}

func (e *EntryError) Error() string {
	message := fmt.Sprintf("%v: path %q", e.Err, e.Path)
	if e.Mode != "" {
		message += fmt.Sprintf(" (mode %s)", e.Mode)
	}
	if e.Type != "" {
		message += fmt.Sprintf(" (type %s)", e.Type)
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *EntryError) Unwrap() error { return e.Err }

// Materialize writes the exact bytes of one committed tree into destination.
//
// destination must be an absolute path that does not exist. Materialize
// creates it and writes only inside it, through an os.Root confinement that
// refuses any path resolving outside the new directory. It never follows an
// existing name: every directory is created by this call and every file is
// created exclusively, so a pre-existing symbolic link is refused rather than
// written through.
//
// Ownership on failure is explicit. When Materialize returns
// ErrDestinationExists it created nothing and the caller must not remove the
// existing directory. For any other error after creation, the destination may
// hold a partial export and the caller owns removing it. Materialize itself
// never deletes anything and never returns a Result for an incomplete export.
func Materialize(ctx context.Context, source Source, destination string, options Options) (*Result, error) {
	if source == nil {
		return nil, errors.New("check source is required")
	}
	limits, err := options.Limits.effective()
	if err != nil {
		return nil, err
	}
	if destination == "" || !filepath.IsAbs(destination) || destination != filepath.Clean(destination) {
		return nil, fmt.Errorf("%w: an absolute cleaned path is required", ErrInvalidDestination)
	}
	format := source.ObjectFormat()
	if format != FormatSHA1 && format != FormatSHA256 {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedObjectFormat, format)
	}
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries, err := source.ListTree(ctx, limits.MetadataLimit)
	if err != nil {
		return nil, fmt.Errorf("list check source tree: %w", err)
	}
	// A source may observe cancellation and still return usable data. Rechecking
	// after the call keeps a cancelled run from completing, which matters most
	// for an empty or single-file tree where no later check would notice.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, totalBytes, err := planFiles(entries, limits)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := createPrivateDestination(destination)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	result := &Result{
		Destination:     destination,
		CommitOID:       source.CommitOID(),
		ObjectFormat:    format,
		EffectiveLimits: limits,
		Files:           make([]FileRecord, 0, len(files)),
		TotalBytes:      totalBytes,
	}
	writer := &treeWriter{root: root, directories: make(map[string]struct{})}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := source.ReadBlob(ctx, file.OID, file.Size)
		if err != nil {
			return nil, fmt.Errorf("read blob %s for %q: %w", file.OID, file.Path, err)
		}
		// A source may cancel and still return valid bytes, so the write is
		// skipped rather than treated as progress.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(content)) != file.Size {
			return nil, &EntryError{Path: file.Path, Mode: file.Mode, Err: ErrObjectMismatch,
				Detail: fmt.Sprintf("Git returned %d bytes for a %d-byte entry", len(content), file.Size)}
		}
		observed, err := blobObjectID(format, content)
		if err != nil {
			return nil, err
		}
		if observed != file.OID {
			return nil, &EntryError{Path: file.Path, Mode: file.Mode, Err: ErrObjectMismatch,
				Detail: fmt.Sprintf("content hashes to %s", observed)}
		}
		record, err := writer.writeFile(file, content)
		if err != nil {
			return nil, err
		}
		result.Files = append(result.Files, record)
		if record.LFSPointer != nil {
			result.LFSPointerPaths = append(result.LFSPointerPaths, record.Path)
		}
		if record.Executable && !record.ExecutableApplied {
			result.ExecutableBitsUnsupported = true
		}
	}
	// The export is complete only if it was never cancelled. Checking once more
	// before returning keeps a late cancellation from yielding a manifest.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// plannedFile is an entry that passed every pre-write check.
// ValidateEntries applies the complete source shape and lexical path bounds
// without creating a destination or reading blob bytes. External source
// transport uses it before publishing a manifest.
func ValidateEntries(entries []Entry, limits Limits) error {
	effective, err := limits.effective()
	if err != nil {
		return err
	}
	_, _, err = planFiles(entries, effective)
	return err
}

type plannedFile struct {
	Path       string
	OID        string
	Mode       string
	Size       int64
	Executable bool
}

// planFiles validates the complete listing before anything is created. It
// returns the files to write in path order.
func planFiles(entries []Entry, limits Limits) ([]plannedFile, int64, error) {
	if len(entries) > limits.MaxEntries {
		return nil, 0, fmt.Errorf("%w: %d entries exceed the %d-entry limit", ErrLimitExceeded, len(entries), limits.MaxEntries)
	}
	files := make([]plannedFile, 0, len(entries))
	var totalBytes int64
	for _, entry := range entries {
		if err := validateEntryPath(entry.Path, limits); err != nil {
			return nil, 0, err
		}
		if err := supportedKind(entry); err != nil {
			return nil, 0, err
		}
		if entry.Size > limits.MaxFileBytes {
			return nil, 0, &EntryError{Path: entry.Path, Mode: entry.Mode, Err: ErrLimitExceeded,
				Detail: fmt.Sprintf("%d bytes exceed the %d-byte file limit", entry.Size, limits.MaxFileBytes)}
		}
		// Compare before adding so a hostile size cannot wrap the accumulator
		// past the limit into a small positive value.
		if entry.Size > limits.MaxTotalBytes-totalBytes {
			return nil, 0, fmt.Errorf("%w: the tree exceeds the %d-byte total limit", ErrLimitExceeded, limits.MaxTotalBytes)
		}
		totalBytes += entry.Size
		files = append(files, plannedFile{
			Path: entry.Path, OID: entry.OID, Mode: entry.Mode,
			Size: entry.Size, Executable: entry.Mode == ModeExecutable,
		})
	}
	slices.SortFunc(files, func(a, b plannedFile) int { return strings.Compare(a.Path, b.Path) })
	if err := checkPathConflicts(files); err != nil {
		return nil, 0, err
	}
	return files, totalBytes, nil
}

// supportedKind refuses every entry this materializer cannot write exactly.
// Symbolic links and gitlinks are refused before execution instead of being
// partially resolved or omitted.
func supportedKind(entry Entry) error {
	refuse := func(detail string) error {
		return &EntryError{Path: entry.Path, Mode: entry.Mode, Type: entry.Type, Err: ErrUnsupportedEntry, Detail: detail}
	}
	switch entry.Mode {
	case ModeRegular, ModeExecutable:
	case "120000":
		return refuse("symbolic links are not materialized")
	case "160000":
		return refuse("submodule references are not materialized")
	default:
		return refuse("only regular and executable file modes are materialized")
	}
	if entry.Type != "blob" {
		return refuse("only blob objects are materialized")
	}
	if entry.Size < 0 {
		return &EntryError{Path: entry.Path, Mode: entry.Mode, Err: ErrObjectMismatch, Detail: "Git reported no blob size"}
	}
	if !isObjectID(entry.OID) {
		return &EntryError{Path: entry.Path, Mode: entry.Mode, Err: ErrObjectMismatch, Detail: "malformed object ID"}
	}
	return nil
}

// checkPathConflicts rejects duplicates and entries whose name is also used as
// a directory. Case-insensitive and normalization collisions depend on the
// destination filesystem and are detected during writing.
func checkPathConflicts(files []plannedFile) error {
	directories := make(map[string]struct{}, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, duplicate := seen[file.Path]; duplicate {
			return &EntryError{Path: file.Path, Err: ErrPathConflict, Detail: "the tree lists this path more than once"}
		}
		seen[file.Path] = struct{}{}
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			directories[parent] = struct{}{}
		}
	}
	for _, file := range files {
		if _, conflict := directories[file.Path]; conflict {
			return &EntryError{Path: file.Path, Err: ErrPathConflict, Detail: "the tree uses this name as both a file and a directory"}
		}
	}
	return nil
}

// validateEntryPath keeps repository paths as data. It refuses traversal,
// absolute forms, Git control-directory aliases, and invisible control or
// format characters, and applies Windows name rules only when running on
// Windows so ordinary repositories stay usable elsewhere.
func validateEntryPath(entryPath string, limits Limits) error {
	unsafe := func(detail string) error {
		return &EntryError{Path: entryPath, Err: ErrUnsafePath, Detail: detail}
	}
	if entryPath == "" {
		return unsafe("the tree entry has no path")
	}
	if len(entryPath) > limits.MaxPathBytes {
		return &EntryError{Path: entryPath, Err: ErrLimitExceeded,
			Detail: fmt.Sprintf("the path exceeds the %d-byte limit", limits.MaxPathBytes)}
	}
	if strings.HasPrefix(entryPath, "/") || strings.ContainsRune(entryPath, 0) {
		return unsafe("the path is absolute or contains a null byte")
	}
	components := strings.Split(entryPath, "/")
	if len(components) > limits.MaxPathDepth {
		return &EntryError{Path: entryPath, Err: ErrLimitExceeded,
			Detail: fmt.Sprintf("the path exceeds the %d-component depth limit", limits.MaxPathDepth)}
	}
	for _, component := range components {
		switch component {
		case "":
			return unsafe("the path has an empty component")
		case ".", "..":
			return unsafe("the path contains a relative component")
		}
		if len(component) > limits.MaxNameBytes {
			return &EntryError{Path: entryPath, Err: ErrLimitExceeded,
				Detail: fmt.Sprintf("a name exceeds the %d-byte limit", limits.MaxNameBytes)}
		}
		if hasHiddenRune(component) {
			return unsafe("a name contains a control or invisible formatting character")
		}
		if isGitControlAlias(component) {
			return unsafe("a name aliases the Git control directory")
		}
		if runtime.GOOS == "windows" && !windowsRepresentable(component) {
			return unsafe("a name cannot be represented on this filesystem")
		}
	}
	return nil
}

// hasHiddenRune reports control and format characters. Invalid UTF-8 bytes are
// left to the destination filesystem, which accepts them or fails visibly.
func hasHiddenRune(component string) bool {
	for index := 0; index < len(component); {
		value, size := utf8.DecodeRuneInString(component[index:])
		if value == utf8.RuneError && size == 1 {
			index++
			continue
		}
		if unicode.IsControl(value) || unicode.Is(unicode.Cf, value) {
			return true
		}
		index += size
	}
	return false
}

// isGitControlAlias matches the names that different filesystems resolve to a
// .git directory, including trailing dot or space stripping and the NTFS short
// name. Invisible-character variants are already refused by hasHiddenRune.
func isGitControlAlias(component string) bool {
	trimmed := strings.TrimRight(component, ". ")
	return strings.EqualFold(trimmed, ".git") || strings.EqualFold(trimmed, "git~1")
}

var windowsReservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {}, "com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {}, "lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

func windowsRepresentable(component string) bool {
	if strings.ContainsAny(component, `\/:*?"<>|`) {
		return false
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return false
	}
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	_, reserved := windowsReservedNames[strings.ToLower(base)]
	return !reserved
}

// treeWriter creates every directory and file itself under one confined root.
type treeWriter struct {
	root        *os.Root
	directories map[string]struct{}
}

func (w *treeWriter) writeFile(file plannedFile, content []byte) (FileRecord, error) {
	if err := w.ensureDirectory(path.Dir(file.Path)); err != nil {
		return FileRecord{}, err
	}
	permissions := fs.FileMode(0o600)
	if file.Executable {
		permissions = 0o700
	}
	handle, err := w.root.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return FileRecord{}, &EntryError{Path: file.Path, Err: ErrPathConflict,
				Detail: "the destination filesystem already holds this name"}
		}
		return FileRecord{}, fmt.Errorf("create %q: %w", file.Path, err)
	}
	record := FileRecord{
		Path: file.Path, OID: file.OID, Mode: file.Mode, Size: file.Size,
		Executable: file.Executable, LFSPointer: parseLFSPointer(content),
	}
	applied, writeErr := writeExactFile(handle, content, permissions)
	// Close reports delayed write failures, so it is kept even when the write
	// already failed rather than being discarded.
	if err := errors.Join(writeErr, handle.Close()); err != nil {
		return FileRecord{}, fmt.Errorf("write %q: %w", file.Path, err)
	}
	record.ExecutableApplied = applied
	return record, nil
}

// writeExactFile writes the blob bytes and reports the execute bit the
// filesystem actually shows afterwards, rather than assuming the requested
// mode was honored.
func writeExactFile(handle *os.File, content []byte, permissions fs.FileMode) (bool, error) {
	if _, err := handle.Write(content); err != nil {
		return false, err
	}
	if err := handle.Chmod(permissions); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return false, err
	}
	info, err := handle.Stat()
	if err != nil {
		return false, err
	}
	return info.Mode()&0o100 != 0, nil
}

// ensureDirectory creates each missing ancestor. A name that already exists
// was not created by this materialization, so it is a conflict rather than a
// directory to reuse or follow.
func (w *treeWriter) ensureDirectory(directory string) error {
	if directory == "." || directory == "" {
		return nil
	}
	current := ""
	for _, component := range strings.Split(directory, "/") {
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		if _, created := w.directories[current]; created {
			continue
		}
		err := w.root.Mkdir(current, 0o700)
		switch {
		case err == nil:
			w.directories[current] = struct{}{}
		case errors.Is(err, fs.ErrExist):
			return &EntryError{Path: current, Err: ErrPathConflict,
				Detail: "the destination filesystem already holds this name"}
		default:
			return fmt.Errorf("create directory %q: %w", current, err)
		}
	}
	return nil
}

// blobObjectID recomputes the Git object ID of the received bytes. It detects
// truncated or swapped object data; for SHA-1 repositories it inherits SHA-1's
// collision weakness and is an integrity check, not a security guarantee.
func blobObjectID(format string, content []byte) (string, error) {
	var digest hash.Hash
	switch format {
	case FormatSHA1:
		digest = sha1.New()
	case FormatSHA256:
		digest = sha256.New()
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedObjectFormat, format)
	}
	header := fmt.Appendf(nil, "blob %d", len(content))
	header = append(header, 0)
	digest.Write(header)
	digest.Write(content)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// effective fills unset limits with defaults and rejects negative values.
func (l Limits) effective() (Limits, error) {
	defaults := DefaultLimits()
	limits := l
	for name, value := range map[string]int{
		"MaxEntries": l.MaxEntries, "MaxPathDepth": l.MaxPathDepth,
		"MaxPathBytes": l.MaxPathBytes, "MaxNameBytes": l.MaxNameBytes,
	} {
		if value < 0 {
			return Limits{}, fmt.Errorf("invalid materialization limit %s", name)
		}
	}
	for name, value := range map[string]int64{
		"MaxFileBytes": l.MaxFileBytes, "MaxTotalBytes": l.MaxTotalBytes, "MetadataLimit": l.MetadataLimit,
	} {
		if value < 0 {
			return Limits{}, fmt.Errorf("invalid materialization limit %s", name)
		}
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxPathDepth == 0 {
		limits.MaxPathDepth = defaults.MaxPathDepth
	}
	if limits.MaxPathBytes == 0 {
		limits.MaxPathBytes = defaults.MaxPathBytes
	}
	if limits.MaxNameBytes == 0 {
		limits.MaxNameBytes = defaults.MaxNameBytes
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MetadataLimit == 0 {
		limits.MetadataLimit = defaults.MetadataLimit
	}
	return limits, nil
}

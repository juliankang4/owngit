package githttp

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
	"owngit/internal/logtext"
	"owngit/internal/repository"
)

// Archive formats ServeArchive writes.
const (
	ArchiveZip   = "zip"
	ArchiveTarGz = "tar.gz"
)

// archiveHoldback is how many final bytes of an archive wait until Git has
// finished successfully. A ZIP file ends with its central directory and end
// record, which Git writes last; holding back the tail keeps at least the end
// record from a client when Git fails. A variable so tests can lower it.
var archiveHoldback = 64 << 10

// ArchiveError is an archive request that failed before its response started.
// ServeArchive then wrote nothing, and the caller answers with Status, in the
// error format of its route, adding Retry-After when RetryAfter is set.
type ArchiveError struct {
	Status     int
	RetryAfter time.Duration
	// Message is a short English explanation for the client.
	Message string
	// Reason names a refusal that a page explains in the language of its
	// reader (see the package webui), so the English sentence is not the only
	// explanation. It is empty when English says all there is to say.
	Reason ArchiveRefusal
	// Detail holds the repository data a Reason refers to, shown as it is by
	// every language.
	Detail string
}

// ArchiveRefusal names why an archive request was refused.
type ArchiveRefusal string

const (
	// ArchiveRefusalRepeatedPath: one path of the commit's tree appears twice.
	ArchiveRefusalRepeatedPath ArchiveRefusal = "repeated_path"
	// ArchiveRefusalManyFiles: the commit's files cannot be checked before the
	// archive runs.
	ArchiveRefusalManyFiles ArchiveRefusal = "too_many_files"
	// ArchiveRefusalDeepChain: an object of the commit is stored in a delta
	// chain deeper than Git builds, so the check cannot price the commit's
	// files. This is a property of the repository's packing, not of the number
	// of files, and has its own wording for the owner.
	ArchiveRefusalDeepChain ArchiveRefusal = "deep_chain"
	// ArchiveRefusalMemory: one file needs more memory to rebuild from its
	// stored delta than this computer gives Git at once.
	ArchiveRefusalMemory ArchiveRefusal = "memory"
)

func (e *ArchiveError) Error() string { return e.Message }

// archivePrecheckFailure turns the result of the path check into the refusal
// the caller answers, or nil when the check succeeded. Every reason that
// establishes itself is answered with 409 and a name of its own, so a page can
// state it in the reader's language (see the package webui); a check that could
// not tell keeps the plain gateway failure.
func archivePrecheckFailure(repositoryID string, err error) *ArchiveError {
	var limitErr *gitexec.LimitError
	var budget *repository.DeltaRebuildError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrRepeatedTreePath):
		log.Printf("Git archive request for repository %q: the commit's tree names one path twice", repositoryID)
		return &ArchiveError{Status: http.StatusConflict, Reason: ArchiveRefusalRepeatedPath,
			Message: "This commit holds two entries with one path, so OwnGit cannot make a faithful archive of it."}
	case errors.Is(err, repository.ErrDeltaChainTooDeep):
		// The metadata of this repository describes a chain deeper than Git
		// builds, so no file of it can be judged. The owner sees a refusal that
		// names the repository's packing; the log names the same shape.
		log.Printf("Git archive request for repository %q: an object's delta chain is deeper than Git builds, so the commit's files could not be checked", repositoryID)
		return &ArchiveError{Status: http.StatusConflict, Reason: ArchiveRefusalDeepChain,
			Message: "One object of this commit is stored in a delta chain deeper than Git builds, so OwnGit cannot check the commit's files and did not create the archive. Pack the repository again on a computer with enough memory, or clone it with Git instead."}
	case errors.As(err, &limitErr), errors.Is(err, repository.ErrTreeCheckTooLarge):
		log.Printf("Git archive request for repository %q: the commit's file list is too large to check", repositoryID)
		return &ArchiveError{Status: http.StatusConflict, Reason: ArchiveRefusalManyFiles,
			Message: "This commit has too many files for OwnGit to check its paths, so it did not create the archive."}
	case errors.As(err, &budget):
		log.Printf("Git archive request for repository %q: %v", repositoryID, budget)
		return &ArchiveError{Status: http.StatusConflict, Reason: ArchiveRefusalMemory,
			Detail: fmt.Sprintf("%s (%d MiB > %d MiB)", budget.Path, budget.Cost/(1<<20), budget.Bound/(1<<20)),
			Message: fmt.Sprintf(
				"This computer cannot rebuild %s (%d MiB) in memory, so OwnGit did not create the archive. Clone the repository with Git instead, or run OwnGit on a computer with more memory.",
				budget.Path, budget.Cost/(1<<20))}
	default:
		log.Printf("Git archive request for repository %q: could not read the commit's paths, so no archive was created: %s", repositoryID, logtext.Cause(err))
		return &ArchiveError{Status: http.StatusBadGateway, Message: "Git could not create the archive."}
	}
}

// ServeArchive streams a ZIP or tar.gz archive of commit commitOID of
// repository repositoryID. The caller has authorized the request, resolved
// the commit, and chosen the archive's top folder prefix and the file name.
//
// The archive shares the Smart HTTP bounds: a transfer slot, the operation
// timeout (which also bounds the waits for a slot and for the repository read
// lock), the idle limit, and the response size limit. Git runs under the
// repository read lock, as for a clone, and is stopped when the client goes
// away or stops reading, the time runs out, or the size limit is reached.
//
// Until the first archive byte, a failure writes nothing and is returned as an
// *ArchiveError: 404 for an unknown format or repository, 409 when the
// commit's tree cannot be read as one path per entry, 503 when the
// repository, a slot, the lock or the time is not available, and 502 when Git
// fails. The response is then sent while Git writes it, so a later failure
// cannot change the status. The connection is closed without ending the
// response properly (for HTTP/1.1, without the final chunk), so a client sees
// an incomplete transfer, and ServeArchive does not return. The archive itself
// also stays incomplete: a ZIP file misses its end records and a tar.gz file
// its gzip trailer, which are written only after Git has succeeded.
func (h *Handler) ServeArchive(writer http.ResponseWriter, request *http.Request, repositoryID, commitOID, format, prefix, filename string) error {
	var contentType, gitFormat, extension string
	switch format {
	case ArchiveZip:
		contentType, gitFormat, extension = "application/zip", "zip", ".zip"
	case ArchiveTarGz:
		contentType, gitFormat, extension = "application/gzip", "tar", ".tar.gz"
	default:
		return &ArchiveError{Status: http.StatusNotFound, Message: "The archive format must be zip or tar.gz."}
	}
	repositoryPath, _, exists, err := h.Repositories.ExistingPath(request.Context(), repositoryID)
	switch {
	case errors.Is(err, repository.ErrRepositoryPreparing):
		return &ArchiveError{Status: http.StatusServiceUnavailable, RetryAfter: 30 * time.Second, Message: "The repository is being prepared. Try again later."}
	case err != nil:
		logCause(request.Context(), fmt.Sprintf("Git archive request for repository %q failed: repository storage is unavailable", repositoryID), err)
		return &ArchiveError{Status: http.StatusServiceUnavailable, Message: "The repository storage is unavailable."}
	case !exists:
		return &ArchiveError{Status: http.StatusNotFound, Message: "The repository does not exist."}
	}
	h.operationMu.Lock()
	if h.closing {
		h.operationMu.Unlock()
		return &ArchiveError{Status: http.StatusServiceUnavailable, Message: "The Git service is shutting down."}
	}
	h.operations.Add(1)
	h.operationMu.Unlock()
	defer h.operations.Done()

	ctx := request.Context()
	limits, status, message := h.transferLimits(ctx, fmt.Sprintf("Git archive request for repository %q", repositoryID))
	if status != 0 {
		return &ArchiveError{Status: status, Message: message}
	}
	controller := http.NewResponseController(writer)
	var deadline time.Time
	if limits.Operation > 0 {
		deadline = time.Now().Add(limits.Operation)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
		// A blocked write to a stalled client does not see the context.
		_ = controller.SetWriteDeadline(deadline)
	}
	if limits.Operation > 0 || limits.Idle > 0 {
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	deadlines := &transferDeadlines{controller: controller, idle: limits.Idle, overall: deadline}
	busy := &ArchiveError{Status: http.StatusServiceUnavailable, RetryAfter: 10 * time.Second, Message: "The repository is busy with other Git transfers. Try again shortly."}
	release, err := h.slots.acquirePacking(ctx, repositoryID, limits)
	if err != nil {
		logArchiveFailure(repositoryID, err, deadline, "no Git transfer slot became free within "+limits.QueueWait.String())
		return busy
	}
	defer release()
	h.active.Add(1)
	defer h.active.Add(-1)
	lock := h.Repositories.Locks.For(repositoryID)
	if err := lock.RLockContext(ctx); err != nil {
		logArchiveFailure(repositoryID, err, deadline, "")
		return busy
	}
	defer lock.RUnlock()

	// An archive of a tree that names one path twice is not faithful: it holds
	// two entries with one name, and an unpacker silently overwrites the first
	// with the second. The pages refuse such a tree the same way. A check that
	// did not succeed proves nothing about the commit's paths, so the archive
	// is refused rather than packed unchecked. The check also refuses a file
	// that Git would rebuild in memory while it writes the archive, when the
	// rebuild needs more memory than this computer can give Git at once: the
	// large-file threshold does not bound that read (measured on Git 2.47.3: a
	// 220 MiB delta used 477 MiB, and a 4 MiB file whose base is 100 MiB used
	// 122 MiB).
	bound := archiveRebuildBound()
	err = h.Repositories.VerifyTreePaths(ctx, repositoryPath, commitOID, hostmem.TreeCheckBound(hostmem.Ceiling()), bound)
	if failure := archivePrecheckFailure(repositoryID, err); failure != nil {
		return failure
	}

	sent := &archiveResponse{ResponseWriter: writer, deadlines: deadlines, limit: limits.MaximumResponse, contentType: contentType, disposition: attachmentDisposition(filename, repositoryID+"-"+shortCommitID(commitOID)+extension)}
	held := &holdbackWriter{next: sent, hold: archiveHoldback}
	var compressed *gzip.Writer
	_, err = h.Git.StreamGit(ctx, repositoryPath, func(stdout io.Reader) error {
		destination := io.Writer(held)
		if gitFormat == "tar" {
			compressed = gzip.NewWriter(held)
			destination = compressed
		}
		_, err := io.Copy(destination, stdout)
		return err
	}, "--git-dir", ".", "archive", "--format="+gitFormat, "--prefix="+prefix+"/", commitOID)
	if err == nil && compressed != nil {
		err = compressed.Close()
	}
	if err == nil {
		err = held.flush()
	}
	if err == nil {
		// Send the end of the archive while the deadlines still apply.
		if err = deadlines.flush(); err != nil {
			err = fmt.Errorf("%w: %w", errArchiveWrite, err)
		}
	}
	if err == nil {
		return nil
	}
	if deadlines.stalled.Load() {
		log.Printf("Git archive request for repository %q failed: %s", repositoryID, deadlines.reason())
	} else {
		logArchiveFailure(repositoryID, err, deadline, "")
	}
	if !sent.started {
		// A client that went away does not read this answer, which is
		// harmless; any other reader must not see an empty success.
		if ctx.Err() != nil {
			return busy
		}
		return &ArchiveError{Status: http.StatusBadGateway, Message: "Git could not create the archive."}
	}
	// Abort the response so the client does not take the part it received
	// as a complete download.
	panic(http.ErrAbortHandler)
}

// logArchiveFailure logs why an archive request failed, unless the client
// went away. busy is the reason for a wait that ran out on its own.
func logArchiveFailure(repositoryID string, err error, deadline time.Time, busy string) {
	reason := archiveFailureReason(err, deadline)
	if errors.Is(err, errBusy) && busy != "" {
		reason = busy
	}
	if reason != "" {
		log.Printf("Git archive request for repository %q failed: %s", repositoryID, reason)
	}
}

// attachmentDisposition names filename for a download. A name with other than
// ASCII letters, digits, ".", "-" and "_" also gets the ASCII name fallback
// first, for clients that do not read the UTF-8 form (RFC 6266 section 4.3),
// such as curl --remote-header-name. Characters of fallback outside that set
// are dropped.
func attachmentDisposition(filename, fallback string) string {
	if strings.IndexFunc(filename, notPlainNameCharacter) < 0 {
		return "attachment; filename=" + filename
	}
	fallback = strings.Map(func(character rune) rune {
		if notPlainNameCharacter(character) {
			return -1
		}
		return character
	}, fallback)
	var encoded strings.Builder
	for _, b := range []byte(filename) {
		if b < utf8.RuneSelf && !notPlainNameCharacter(rune(b)) {
			encoded.WriteByte(b)
		} else {
			fmt.Fprintf(&encoded, "%%%02X", b)
		}
	}
	return `attachment; filename="` + fallback + `"; filename*=UTF-8''` + encoded.String()
}

// notPlainNameCharacter reports whether character is other than an ASCII
// letter, digit, ".", "-" or "_".
func notPlainNameCharacter(character rune) bool {
	return !(character == '.' || character == '-' || character == '_' ||
		'0' <= character && character <= '9' || 'a' <= character && character <= 'z' || 'A' <= character && character <= 'Z')
}

// shortCommitID is the first 12 characters of a commit ID, which name a
// commit well enough in an archive's ASCII file name.
func shortCommitID(commitOID string) string {
	if len(commitOID) > 12 {
		return commitOID[:12]
	}
	return commitOID
}

// archiveFailureReason names a failed archive for the server log, or returns
// "" for a client that went away. deadline is the operation deadline, zero
// when there is none, so a timeout names the limit that ended it. Like
// failureReason, it never includes Git's output.
func archiveFailureReason(err error, deadline time.Time) string {
	var exitErr *exec.ExitError
	timedOut := !deadline.IsZero() && !time.Now().Before(deadline)
	switch {
	case errors.Is(err, errResponseTooLarge):
		return "response exceeded the size limit"
	case timedOut:
		return "operation timed out (Git transfer time limit)"
	case errors.Is(err, context.DeadlineExceeded):
		return "request deadline passed"
	case errors.Is(err, context.Canceled):
		return ""
	case errors.As(err, &exitErr) && exitErr.ExitCode() < 0:
		return "git archive was stopped by a signal"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("git archive exited with status %d", exitErr.ExitCode())
	case errors.Is(err, errArchiveWrite):
		return ""
	default:
		return "git archive failed"
	}
}

var errArchiveWrite = errors.New("archive response write failed")

// archiveRebuildBound is the memory this computer can give Git at once (see
// the package hostmem), the line above which the archive refuses a file it
// would have to rebuild in memory. It is 0 when the ceiling is unknown: the
// computer is assumed to be ordinary then and nothing is refused. It is a
// variable so a test can hold a small fixture to a smaller line.
var archiveRebuildBound = func() int64 { return int64(hostmem.GitBudget(hostmem.Ceiling())) }

// archiveResponse writes archive bytes to the client and refuses to pass the
// size limit. The first byte commits the status and the archive headers; until
// then a failure can still answer with an error status.
type archiveResponse struct {
	http.ResponseWriter
	deadlines   *transferDeadlines
	limit       int64
	contentType string
	disposition string
	written     int64
	started     bool
}

func (response *archiveResponse) Write(content []byte) (int, error) {
	if response.limit > 0 && response.written+int64(len(content)) > response.limit {
		return 0, errResponseTooLarge
	}
	if !response.started {
		response.started = true
		header := response.Header()
		header.Set("Content-Type", response.contentType)
		header.Set("Content-Disposition", response.disposition)
		header.Set("X-Content-Type-Options", "nosniff")
		// The content is private and can be large, so no cache keeps a copy.
		header.Set("Cache-Control", "private, no-store")
		response.WriteHeader(http.StatusOK)
	}
	n, err := response.deadlines.write(response.ResponseWriter, content)
	response.written += int64(n)
	if err != nil {
		return n, fmt.Errorf("%w: %w", errArchiveWrite, err)
	}
	return n, nil
}

// holdbackWriter passes bytes on once more than hold bytes follow them, so
// the last hold bytes reach next only through flush.
type holdbackWriter struct {
	next    io.Writer
	hold    int
	pending []byte
}

func (writer *holdbackWriter) Write(content []byte) (int, error) {
	writer.pending = append(writer.pending, content...)
	if excess := len(writer.pending) - writer.hold; excess > 0 {
		if _, err := writer.next.Write(writer.pending[:excess]); err != nil {
			return 0, err
		}
		writer.pending = append(writer.pending[:0], writer.pending[excess:]...)
	}
	return len(content), nil
}

func (writer *holdbackWriter) flush() error {
	_, err := writer.next.Write(writer.pending)
	writer.pending = nil
	return err
}

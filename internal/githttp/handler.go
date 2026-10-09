package githttp

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"owngit/internal/hostmem"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/logtext"
	"owngit/internal/repository"
	"owngit/internal/requestctx"
	"owngit/internal/state"
)

type Handler struct {
	Git          *gitexec.Runner
	Repositories *repository.Manager
	BackendPath  string
	// Authorize reports whether a request may use Git. auth.ErrRateLimited
	// means the client's address may not try a password now. Any other error
	// means that could not be decided: the client is told to try later and is
	// not asked for another password. Authorize logs the cause of that error.
	Authorize func(*http.Request) (bool, error)
	// OnReceive wakes check reconciliation after git-receive-pack exits. It is
	// advisory and must never change the already completed Git response.
	OnReceive func(string)
	// OnPush runs after a push changed at least one ref, with the refs it
	// changed in the order Git reported them. It runs once OwnGit has read
	// Git's complete report and passed it on, also when the client then
	// failed to receive its end, and must never change the response. A push
	// that changed no ref does not run it, and neither does one whose
	// client left before OwnGit read the whole report, although Git may
	// have changed refs for it. The request it gets is the push's, with a
	// context that the end of the push does not cancel.
	OnPush func(request *http.Request, repositoryID string, updates []RefUpdate)
	// Limits returns the limits of a transfer that starts now. New reads
	// the ones the owner saved (state.GitTransferLimits), so a change
	// applies to the next transfer. A transfer whose limits cannot be read
	// is refused with 503, or 409 naming the setting.
	Limits      func(context.Context) (Limits, error)
	slots       *admission
	active      atomic.Int64
	operationMu sync.Mutex
	operations  sync.WaitGroup
	closing     bool
}

// Limits bound one transfer: a clone, fetch or push, or an archive
// download. Zero leaves a size, operation or idle bound out.
type Limits struct {
	// MaximumRequest bounds the bytes a transfer receives, after gzip is
	// inflated, and MaximumResponse the bytes it sends.
	MaximumRequest  int64
	MaximumResponse int64
	// Operation bounds how long it takes: a clone, fetch or push from when
	// it gets a transfer slot, and an archive download from when it starts
	// to wait for one.
	Operation time.Duration
	// Idle stops a transfer whose client moves no data for this long: a
	// response write the client does not accept, or a request body read
	// that receives nothing. Time that Git spends working without output
	// does not count.
	Idle time.Duration
	// PerRepository, ExtraSlots and QueueWait decide when it gets a slot
	// (see admission). A request that finds none within QueueWait is
	// refused with 503 and Retry-After.
	PerRepository, ExtraSlots int
	QueueWait                 time.Duration
	// PackSlots, when above zero, also limits how many requests that build
	// a pack (a clone, a fetch or an archive) run at once. Ref
	// advertisements and pushes do not count against it and never wait for it.
	PackSlots int
	// Memory, when set, is the gate that every Git process using memory
	// shares, and each request takes a slot of it as well.
	Memory *hostmem.Gate
}

func New(git *gitexec.Runner, repositories *repository.Manager, backendPath string) (*Handler, error) {
	if backendPath == "" {
		var err error
		backendPath, err = DiscoverBackend(context.Background(), git)
		if err != nil {
			return nil, err
		}
	}
	info, err := os.Stat(backendPath)
	if err != nil || info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return nil, fmt.Errorf("git-http-backend is not executable at %q", backendPath)
	}
	return &Handler{
		Git: git, Repositories: repositories, BackendPath: backendPath,
		Limits: func(ctx context.Context) (Limits, error) {
			saved, err := repositories.Store.GitTransferLimits(ctx)
			return savedLimits(saved), err
		},
		slots: newAdmission(),
	}, nil
}

// savedLimits are the limits of a transfer under saved.
func savedLimits(saved state.GitTransferLimits) Limits {
	return Limits{
		MaximumRequest: saved.MaximumBytes, MaximumResponse: saved.MaximumBytes, Operation: saved.Operation, Idle: saved.Idle,
		PerRepository: saved.PerRepository, ExtraSlots: saved.ExtraSlots, QueueWait: saved.QueueWait,
	}
}

// transferLimits reads the limits of a transfer that starts now. When they
// cannot be read it logs why and returns the status and message to answer
// with: a saved value to set again names the setting, and anything else is
// a state read failure.
func (h *Handler) transferLimits(ctx context.Context, what string) (Limits, int, string) {
	limits, err := h.Limits(ctx)
	if err == nil {
		return limits, 0, ""
	}
	logCause(ctx, what+" failed: the Git transfer limits could not be read", err)
	var policyErr *state.PolicyError
	if errors.As(err, &policyErr) {
		return Limits{}, http.StatusConflict, policyErr.Advice()
	}
	return Limits{}, http.StatusServiceUnavailable, "The Git transfer limits could not be read. The OwnGit log says why."
}

func DiscoverBackend(ctx context.Context, git *gitexec.Runner) (string, error) {
	result, err := git.Run(ctx, "", nil, "--exec-path")
	if err != nil {
		return "", err
	}
	path := filepath.Join(strings.TrimSpace(string(result.Stdout)), executableName("git-http-backend"))
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", fmt.Errorf("Git Smart HTTP backend was not found at %q", path)
	}
	return path, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h.Authorize != nil {
		allowed, err := h.Authorize(request)
		if err != nil || !allowed {
			RefuseCredentials(writer, err)
			return
		}
	}
	route, ok := parseRoute(request)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	// The path names the repository by its address. An earlier address of a
	// renamed repository is answered here. Git is authenticated before this
	// point, and it does not follow a redirect that arrives after that
	// authentication round, so a redirect would fail the transfer; the
	// answer instead names the current address in the protocol's own
	// message channel (see writeMovedNotice), which reaches the person who
	// runs the command.
	address, found, err := h.Repositories.Store.ResolveRepositoryName(request.Context(), route.repositoryID, time.Now())
	switch {
	case err != nil:
		logCause(request.Context(), fmt.Sprintf("Git %s request for repository %q failed: the repository address could not be read", requestKind(route, request.Method), route.repositoryID), err)
		http.Error(writer, "repository storage is unavailable", http.StatusServiceUnavailable)
		return
	case !found:
		http.NotFound(writer, request)
		return
	case address.Current != route.repositoryID:
		route.moved = address.Current
	}
	route.repositoryID = address.RepositoryID
	h.serve(writer, request, route)
}

// RefuseCredentials answers a Git request whose credentials were not
// accepted: err is nil for credentials that were checked and refused,
// auth.ErrRateLimited for a client address that may not try a password
// now, a state.PolicyError when a wrong password could not be counted, and
// anything else when the check could not be completed, which the caller
// has logged. Only refused credentials ask for a password again.
func RefuseCredentials(writer http.ResponseWriter, err error) {
	var policyErr *state.PolicyError
	switch {
	case err == nil || errors.Is(err, auth.ErrInvalidCredentials):
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit"`)
		http.Error(writer, "authentication required", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrRateLimited):
		// Not 401: Git erases the stored password it sent when the answer
		// is 401, and a right password must outlast a lockout. Git 2.54
		// repeats a 429 at once, without end, unless Retry-After asks it
		// to wait; it stops at a wait over http.maxRetryTime. The wait is
		// what remains of this address's pause.
		if seconds := auth.RetryAfter(err); seconds > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
		http.Error(writer, "too many authentication attempts; try again later", http.StatusTooManyRequests)
	case errors.As(err, &policyErr):
		// The saved login limits cannot be read, so a wrong password
		// could not be counted; the right one still passes.
		http.Error(writer, policyErr.Advice(), http.StatusConflict)
	default:
		http.Error(writer, "authentication is unavailable; try again later", http.StatusServiceUnavailable)
	}
}

// ServeRead serves a clone or fetch of repositoryID, which the caller has
// already authorized, to a request whose path ends with suffix after the
// repository's part, such as info/refs. A push is refused.
func (h *Handler) ServeRead(writer http.ResponseWriter, request *http.Request, repositoryID, suffix string) {
	route, ok := serviceRoute(request, repositoryID, suffix)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if route.service != "git-upload-pack" {
		http.Error(writer, "this address can only be cloned and fetched; pushes are refused", http.StatusForbidden)
		return
	}
	route.config = readOnlyConfig
	route.scope = branchesAndTags
	h.serve(writer, request, route)
}

// readOnlyConfig limits a ServeRead fetch to branches and tags, whatever
// the repository's own config says. Command-line scope comes after the
// repository's config, so the last hideRefs entries decide: every ref is
// hidden, then branches and tags are shown again. The wants of a fetch are
// checked against the same refs before Git runs (fetchWantsGate).
var readOnlyConfig = [][2]string{
	{"uploadpack.hideRefs", "refs/"},
	{"uploadpack.hideRefs", "!refs/heads/"},
	{"uploadpack.hideRefs", "!refs/tags/"},
	{"uploadpack.allowTipSHA1InWant", "false"},
	{"uploadpack.allowReachableSHA1InWant", "false"},
	{"uploadpack.allowAnySHA1InWant", "false"},
}

// serve runs the Git service of route for an authorized request.
func (h *Handler) serve(writer http.ResponseWriter, request *http.Request, route route) {
	gzipped, ok := requestBodyEncoding(request)
	if !ok {
		// RFC 7694: name the accepted request encoding in the refusal.
		writer.Header().Set("Accept-Encoding", "gzip")
		http.Error(writer, "unsupported Content-Encoding; send gzip or an uncompressed body", http.StatusUnsupportedMediaType)
		return
	}
	// A request for a repository postpones its maintenance until it ends.
	h.Repositories.NoteRepositoryUse(route.repositoryID)
	defer h.Repositories.NoteRepositoryUse(route.repositoryID)
	repositoryPath, _, exists, err := h.Repositories.ExistingPath(request.Context(), route.repositoryID)
	if errors.Is(err, repository.ErrRepositoryPreparing) {
		writer.Header().Set("Retry-After", "30")
		http.Error(writer, "repository is being prepared; try again later", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		logCause(request.Context(), fmt.Sprintf("Git %s request for repository %q failed: repository storage is unavailable", requestKind(route, request.Method), route.repositoryID), err)
		http.Error(writer, "repository storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !exists {
		http.NotFound(writer, request)
		return
	}
	limits, status, message := h.transferLimits(request.Context(), fmt.Sprintf("Git %s request for repository %q", requestKind(route, request.Method), route.repositoryID))
	if status != 0 {
		http.Error(writer, message, status)
		return
	}
	if limits.MaximumRequest > 0 && request.ContentLength > limits.MaximumRequest {
		// The limit would stop the body anyway. Refusing it here is certain:
		// a backend that stops reading early can exit before the body reaches
		// the limit, and its failure would hide the cause.
		logGitFailure(route, request.Method, requestTooLarge)
		http.Error(writer, requestTooLarge, http.StatusRequestEntityTooLarge)
		return
	}
	h.operationMu.Lock()
	if h.closing {
		h.operationMu.Unlock()
		http.Error(writer, "Git service is shutting down", http.StatusServiceUnavailable)
		return
	}
	h.operations.Add(1)
	h.operationMu.Unlock()
	defer h.operations.Done()
	acquire := h.slots.acquire
	if buildsPack(request, route) {
		acquire = h.slots.acquirePacking
	}
	release, err := acquire(request.Context(), route.repositoryID, limits)
	if errors.Is(err, errBusy) {
		logGitFailure(route, request.Method, "no Git transfer slot became free within "+limits.QueueWait.String())
		writer.Header().Set("Retry-After", "10")
		http.Error(writer, busyMessage, http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		return
	}
	defer release()
	h.active.Add(1)
	defer h.active.Add(-1)

	// The operation bound starts when the request got its slots, so time spent
	// waiting for a repository held by another transfer counts against the
	// same bound as the transfer itself.
	controller := http.NewResponseController(writer)
	operationContext := request.Context()
	cancel := func() {}
	var deadline time.Time
	if limits.Operation > 0 {
		deadline = time.Now().Add(limits.Operation)
		operationContext, cancel = context.WithDeadline(operationContext, deadline)
	}
	defer cancel()
	deadlines := &transferDeadlines{controller: controller, idle: limits.Idle, overall: deadline}
	// Everything after admission runs under the operation bound, including the
	// lock wait, the ref settings read that a push needs, and the transfer
	// itself. The admitted request keeps its own context for admission only.
	request = request.WithContext(operationContext)
	// A request without a body ends its wait as soon as its client leaves, so
	// the operation limit is the only bound it needs. Go's HTTP server starts
	// its watch for a closed connection only after the body has been consumed
	// (net/http server.go), so a POST whose body the handler still has to read
	// cannot notice a client that leaves. Its wait for the repository is capped
	// by the same queue wait that admission uses, and it then gets the busy
	// answer, so a disconnected peer cannot hold its slots for the whole
	// operation limit. Once the lock is held, the transfer keeps that limit.
	// The shorter of the two bounds ends the wait and names the busy answer.
	// lockWaitLimit is the moment that bound ends, which tells an ended wait
	// from a client that left (see answerBusyLocked).
	lockWait, lockWaitBound, lockWaitLimit := operationContext, "operation limit", deadline
	if request.Method == http.MethodPost && limits.QueueWait > 0 &&
		(limits.Operation <= 0 || limits.QueueWait < limits.Operation) {
		lockWaitLimit = time.Now().Add(limits.QueueWait)
		var cancelLockWait context.CancelFunc
		lockWait, cancelLockWait = context.WithDeadline(operationContext, lockWaitLimit)
		defer cancelLockWait()
		lockWaitBound = "transfer queue wait"
	}

	// The repository lock is taken with the lock wait context, so the wait ends
	// at that bound or when the client leaves instead of when the writer
	// finishes, and the request gives its slots back at once.
	lock := h.Repositories.Locks.For(route.repositoryID)
	// Releasing the write lock invalidates the cached ref snapshot unless the
	// request provably changed no ref: the ref advertisement never writes, and
	// a push only when its complete report refused every command before a
	// write.
	refsUnchanged := request.Method == http.MethodGet
	if route.service == "git-receive-pack" {
		if err := lock.LockContext(lockWait); err != nil {
			if errors.Is(err, repository.ErrStorageChanged) {
				logCause(operationContext, fmt.Sprintf("Git push to repository %q refused", route.repositoryID), err)
				http.Error(writer, storageChangedMessage, http.StatusConflict)
				return
			}
			if errors.Is(err, repository.ErrStorageUnavailable) {
				logCause(operationContext, fmt.Sprintf("Git %s request for repository %q failed: repository storage is unavailable", requestKind(route, request.Method), route.repositoryID), err)
				http.Error(writer, "repository storage is unavailable", http.StatusServiceUnavailable)
				return
			}
			answerBusyLocked(writer, route, request.Method, lockWaitBound, lockWaitLimit, err)
			return
		}
		defer func() {
			if refsUnchanged {
				lock.UnlockWithoutRefChanges()
			} else {
				lock.Unlock()
			}
		}()
	} else {
		if err := lock.RLockContext(lockWait); err != nil {
			answerBusyLocked(writer, route, request.Method, lockWaitBound, lockWaitLimit, err)
			return
		}
		defer lock.RUnlock()
	}
	// The connection deadlines are armed only now, with the repository lock
	// held. No socket read or write happens while the request waits, and a read
	// deadline that passes on a request without a body makes net/http cancel
	// the request (see answerBusyLocked), which would report a wait that ended
	// at its bound as a client that left. Context cancellation alone does not
	// interrupt a blocked socket Read or Write, so both directions keep their
	// deadlines for the transfer without buffering a pack request or response
	// in memory.
	if limits.Operation > 0 {
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
	}
	if limits.Operation > 0 || limits.Idle > 0 {
		defer func() {
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}()
	}
	// A push follows the repository's kept history and default branch
	// protection as they are when it starts; the update hook applies them.
	// Its ref advertisement reads them too, so a Git client shows the
	// refusal of an unreadable choice, which it does not show for the push
	// request itself.
	var refWrites []string
	if route.service == "git-receive-pack" {
		var err error
		if refWrites, err = h.Repositories.RefWriteEnvironment(operationContext, route.repositoryID); err != nil {
			what := fmt.Sprintf("Git push to repository %q failed: its ref settings could not be read", route.repositoryID)
			logCause(operationContext, what, err)
			if policyErr := (*state.PolicyError)(nil); errors.As(err, &policyErr) {
				http.Error(writer, policyErr.Advice(), http.StatusConflict)
			} else {
				http.Error(writer, "The repository's kept history, default branch protection and extra ref namespaces could not be read. The OwnGit log says why.", http.StatusServiceUnavailable)
			}
			return
		}
	}

	// A share address advertises HEAD only when it names a branch or tag tip
	// in its scope (see branchesAndTags). Hiding it otherwise is the same
	// rule checkWants applies to a fetch, so a link cannot serve or show a
	// commit that only kept history reaches. The check decides that, so it must
	// succeed: an address that cannot tell refuses the request instead of
	// advertising whatever HEAD names.
	if route.scope != nil && route.scope.headTips {
		_, advertised, err := h.scopeHead(operationContext, repositoryPath, route.scope)
		if err != nil {
			logCause(request.Context(), fmt.Sprintf("Git %s request for repository %q could not check its HEAD", requestKind(route, request.Method), route.repositoryID), err)
			http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !advertised {
			config := make([][2]string, 0, len(route.config)+1)
			config = append(append(config, route.config...), [2]string{"uploadpack.hideRefs", "HEAD"})
			route.config = config
		}
	}

	contentLength := request.ContentLength
	streamContext, cancelStream := context.WithCancelCause(request.Context())
	defer cancelStream(nil)
	var consumeFailed atomic.Bool
	// input is the body the backend reads. A read error on it, such as the
	// size limit, also abandons the request.
	var input *observedBody
	network := &networkBody{
		ReadCloser: request.Body,
		abandoned: func() bool {
			return streamContext.Err() != nil || consumeFailed.Load() || (input != nil && input.firstError() != nil)
		},
		deadlines: deadlines,
	}
	body := io.ReadCloser(network)
	if limits.MaximumRequest > 0 {
		// The subprocess copies stdin on its own goroutine. Passing the live
		// ResponseWriter to MaxBytesReader would let that goroutine write a 413
		// concurrently with the CGI response copier. The handler reports the
		// returned MaxBytesError after both owned streams have stopped instead.
		body = http.MaxBytesReader(nil, body, limits.MaximumRequest)
	}
	if gzipped {
		// Git clients gzip upload-pack requests over 1 KiB. Inflate here so the
		// same MaximumRequest also bounds the inflated bytes the backend reads.
		// git-http-backend's own inflation has no output bound.
		source := &observedBody{ReadCloser: body}
		// gzip.Reader reads through this buffer, so what follows the gzip
		// stream stays in it for gzipBody to check.
		buffered := bufio.NewReader(source)
		inflated, err := gzip.NewReader(buffered)
		if err != nil {
			_ = body.Close()
			status, reason := http.StatusBadRequest, "could not read the request body"
			var maxErr *http.MaxBytesError
			switch {
			case source.firstError() == nil:
				reason = errInvalidGzip.Error()
				logGitFailure(route, request.Method, reason)
			case errors.As(source.firstError(), &maxErr):
				status, reason = http.StatusRequestEntityTooLarge, requestTooLarge
			case deadlines.stalled.Load():
				status, reason = http.StatusRequestTimeout, deadlines.reason()
				logGitFailure(route, request.Method, reason)
			}
			http.Error(writer, reason, status)
			return
		}
		inflated.Multistream(false)
		body = &gzipBody{inflated: inflated, buffered: buffered, source: source, cancel: cancelStream, closed: make(chan struct{})}
		if limits.MaximumRequest > 0 {
			body = http.MaxBytesReader(nil, body, limits.MaximumRequest)
		}
		contentLength = -1
	}
	if route.service == "git-upload-pack" && request.Method == http.MethodPost && contentLength > int64(maximumFetchRequest) {
		// git-http-backend holds an upload-pack request in memory up to the
		// same bound OwnGit's gate reads, and refuses a longer one before it
		// writes any answer, so its failure would hide the cause. Refusing it
		// here answers what the gate answers, like the request limit does
		// before Git starts.
		logGitFailure(route, request.Method, errFetchTooLarge.Error())
		http.Error(writer, requestTooLarge, http.StatusRequestEntityTooLarge)
		return
	}
	extraEnvironment, err := h.cgiEnvironment(request, route, contentLength)
	if err != nil {
		_ = body.Close()
		http.Error(writer, "invalid Git protocol request", http.StatusBadRequest)
		return
	}
	extraEnvironment = append(extraEnvironment, refWrites...)
	if route.service == "git-receive-pack" && request.Method == http.MethodPost {
		// A push creates, changes and deletes no ref whose name differs from
		// another only in letter case or Unicode form (nameConflictGate).
		path, variable, remove, err := h.nameConflictFile()
		if err != nil {
			_ = body.Close()
			logCause(request.Context(), fmt.Sprintf("Git push to repository %q could not start", route.repositoryID), err)
			http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		defer remove()
		extraEnvironment = append(extraEnvironment, variable)
		body = &nameConflictGate{ReadCloser: body, refuseAll: remove, check: func(updates []pushCommand) error {
			err := h.writeNameConflicts(streamContext, repositoryPath, path, updates)
			if err != nil {
				logCause(request.Context(), fmt.Sprintf("Git push to repository %q could not check its ref names", route.repositoryID), err)
			}
			return err
		}}
	}
	// wantsPending holds the response back while a fetch's wants are being
	// checked: Git's headers must not go out before a refusal can replace
	// them, however long the check takes on a large repository.
	var wantsPending atomic.Bool
	var committed *responseState
	if route.service == "git-upload-pack" && request.Method == http.MethodPost {
		wantsPending.Store(true)
		scope := route.scope
		if scope == nil {
			scope = mainScope
		}
		body = &fetchWantsGate{ReadCloser: body, v2: usesProtocolV2(request.Header.Get("Git-Protocol")), check: func(wants []string) error {
			err := h.checkWants(streamContext, repositoryPath, scope, wants)
			if errors.Is(err, errFetchCheck) {
				logCause(request.Context(), fmt.Sprintf("Git fetch from repository %q could not check its wants", route.repositoryID), err)
			}
			return err
		}, passed: func() {
			wantsPending.Store(false)
			committed.scheduleFlush()
		}}
	}
	input = &observedBody{ReadCloser: body, stop: cancelStream, closed: make(chan struct{})}
	// The response can still go out before the end of the request body, when
	// Git writes more than net/http buffers or stops reading early. Without
	// full duplex, net/http would then read and discard the rest of the
	// request body. In full duplex it neither does that nor announces that it
	// closes the connection after a body left unread, so responseState does:
	// otherwise a client that reuses the connection gets EOF.
	_ = controller.EnableFullDuplex()
	committed = &responseState{ResponseWriter: writer, deadlines: deadlines, flushDelay: responseFlushDelay,
		bodyEnded: func() bool { return !wantsPending.Load() && (request.ContentLength == 0 || network.ended.Load()) }}
	network.onEnd = committed.requestBodyEnded
	defer committed.stop()
	var report *pushReport
	var commands *pushCommands
	var observer io.Writer
	var quarantinesBefore []string
	quarantinesListed := false
	if route.service == "git-receive-pack" && request.Method == http.MethodPost {
		report = &pushReport{}
		observer = report
		commands = &pushCommands{}
		input.tee = commands
		var listErr error
		quarantinesBefore, listErr = repository.IncomingQuarantines(repositoryPath)
		quarantinesListed = listErr == nil
	}
	stderr, err := h.Git.Stream(streamContext, h.BackendPath, repositoryPath, input, extraEnvironment, func(stdout io.Reader) error {
		movedNotice := ""
		if route.moved != "" {
			movedNotice = "This repository moved to " + requestctx.Of(request).Origin() + "/git/" + route.moved + ".git"
		}
		err := h.copyCGIResponse(committed, stdout, observer, limits.MaximumResponse, movedNotice)
		consumeFailed.Store(err != nil)
		return err
	})
	if quarantinesListed {
		removeUnfinishedPushObjects(request.Context(), route, repositoryPath, quarantinesBefore)
	}
	if err == nil && route.service == "git-receive-pack" && h.OnReceive != nil {
		h.OnReceive(route.repositoryID)
	}
	if refusal := input.firstError(); refusal != nil && !committed.started() {
		var notOurRef errNotOurRef
		switch {
		case errors.As(refusal, &notOurRef):
			logGitFailure(route, request.Method, "a want is not reached by an advertised ref")
			answerNotOurRef(committed, notOurRef.oid)
			return
		case errors.Is(refusal, errFetchRequest):
			logGitFailure(route, request.Method, "Git protocol error")
			http.Error(committed, errFetchRequest.Error(), http.StatusBadRequest)
			return
		case errors.Is(refusal, errFetchTooLarge):
			logGitFailure(route, request.Method, errFetchTooLarge.Error())
			http.Error(committed, requestTooLarge, http.StatusRequestEntityTooLarge)
			return
		case errors.Is(refusal, errFetchCheck):
			http.Error(committed, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
	}
	inBand := ""
	if report != nil {
		// Without a side band, receive-pack's messages arrive on stderr.
		report.scan(stderr)
		inBand = report.reason()
		refsUnchanged = err == nil && !consumeFailed.Load() && report.refsUnchanged()
		h.notifyAcceptedPushes(request, route.repositoryID, report.updates(commands))
	}
	if err == nil {
		// Make sure the end of the response went out while the transfer still
		// holds its deadlines; the copy does not report a failed flush after
		// Git's last output. A failure here changes nothing Git already did.
		if flushErr := committed.finish(); flushErr != nil && deadlines.stalled.Load() {
			logGitFailure(route, request.Method, deadlines.reason())
			return
		}
	}
	if err == nil && len(stderr) == 0 && inBand == "" {
		return
	}
	// The backend usually fails after a request-body error cut its input
	// short, so the body error explains the failure better than the exit.
	// git-http-backend also reports some failures only on stderr and exits 0.
	inputErr := input.firstError()
	var maxErr *http.MaxBytesError
	tooLarge := errors.As(err, &maxErr) || errors.As(inputErr, &maxErr)
	invalidGzip := errors.Is(context.Cause(streamContext), errInvalidGzip)
	// The deadline also bounds the connection, so an operation that ran out
	// of time can surface as a write timeout or a cancelled request instead.
	timedOut := err != nil && !deadline.IsZero() && !time.Now().Before(deadline)
	reason := failureReason(err, stderr, tooLarge, invalidGzip, timedOut)
	if err == nil && inBand != "" {
		reason = inBand
	}
	if err != nil && deadlines.stalled.Load() {
		// The client stopped first; what Git reported after it was stopped
		// follows from that.
		reason = deadlines.reason()
	}
	if reason != "" {
		logGitFailure(route, request.Method, reason)
	}
	if err != nil && !committed.started() {
		// A client that stopped sending gets 408, like the log says; 502
		// would blame OwnGit, or behind a proxy OwnGit's backend, for it.
		status := http.StatusBadGateway
		fetchBound := fetchBufferOverflow(route, request, inputErr, reason)
		switch {
		case deadlines.stalled.Load():
			status = http.StatusRequestTimeout
		case tooLarge:
			status = http.StatusRequestEntityTooLarge
		case invalidGzip:
			status = http.StatusBadRequest
		case fetchBound:
			status = http.StatusRequestEntityTooLarge
		}
		// The text names the status the request is answered with, so a refusal
		// never reads as a gateway failure. A fetch over the buffer bound keeps
		// the message the fetch gate answers the same request with.
		message := http.StatusText(status)
		if fetchBound {
			message = requestTooLarge
		}
		http.Error(committed, message, status)
	}
}

// removeUnfinishedPushObjects removes the push quarantine directories that
// appeared in the repository at path since before lists them. receive-pack
// keeps a push's objects in such a directory until the push completes and
// removes it itself, but a receive-pack that was stopped, for example at the
// request limit or when the client went away, leaves it with everything
// received so far, up to the request limit. The caller holds the repository
// write lock, so no other push to this repository ran meanwhile and every new
// directory belongs to this request, whose Git processes have all ended.
// Directories listed before, such as one left before OwnGit started, stay for
// the startup cleanup. A directory that cannot be removed now stays for it
// too.
func removeUnfinishedPushObjects(ctx context.Context, route route, path string, before []string) {
	after, err := repository.IncomingQuarantines(path)
	if err != nil {
		logCause(ctx, fmt.Sprintf("Git push request for repository %q: could not look for the objects of an unfinished push", route.repositoryID), err)
	}
	for _, name := range after {
		if slices.Contains(before, name) {
			continue
		}
		size, err := repository.RemoveIncomingQuarantine(path, name)
		if err != nil {
			logCause(ctx, fmt.Sprintf("Git push request for repository %q: could not remove the objects of an unfinished push in objects/%s", route.repositoryID, name), err)
			continue
		}
		log.Printf("Git push request for repository %q: removed the objects of an unfinished push (objects/%s, %d bytes)", route.repositoryID, name, size)
	}
}

func (h *Handler) notifyAcceptedPushes(request *http.Request, repositoryID string, updates []RefUpdate) {
	if len(updates) > 0 && h.OnPush != nil {
		h.OnPush(request.WithContext(context.WithoutCancel(request.Context())), repositoryID, updates)
	}
}

// requestBodyEncoding reports whether a Smart HTTP request body is gzip. It
// returns ok=false for any other non-identity Content-Encoding, including a
// list of several encodings.
func requestBodyEncoding(request *http.Request) (gzipped bool, ok bool) {
	if request.Method != http.MethodPost {
		return false, true
	}
	var codings []string
	for _, value := range request.Header.Values("Content-Encoding") {
		for _, coding := range strings.Split(value, ",") {
			coding = strings.ToLower(strings.TrimSpace(coding))
			if coding != "" && coding != "identity" {
				codings = append(codings, coding)
			}
		}
	}
	switch {
	case len(codings) == 0:
		return false, true
	case len(codings) == 1 && (codings[0] == "gzip" || codings[0] == "x-gzip"):
		return true, true
	default:
		return false, false
	}
}

var errInvalidGzip = errors.New("request body is not valid gzip")

// requestTooLarge is the reason logged and sent for a request body over
// MaximumRequest.
const requestTooLarge = "request body exceeded the size limit"

var errResponseTooLarge = errors.New("Git response exceeded the configured limit")

// gzipBody inflates a request body. When the network body ended cleanly but
// the gzip stream is corrupt, truncated or fails its checksum, or data follows
// it, Read cancels the operation and blocks until the backend stream closes
// the body. The stream terminates the backend before that close, so the
// backend never sees the end of its input and cannot take a partial request
// as complete. Errors of the network body itself, such as a disconnect or the
// size limit, keep their existing handling.
//
// At the end of the gzip stream, Read also reads the network body to its end.
// Otherwise the end of a chunked body, which arrives after the gzip stream,
// would stay unread, and the response would wait for Git to finish instead of
// for the end of the request.
type gzipBody struct {
	inflated  *gzip.Reader
	buffered  *bufio.Reader
	source    *observedBody
	cancel    context.CancelCauseFunc
	closed    chan struct{}
	closeOnce sync.Once
}

func (body *gzipBody) Read(buffer []byte) (int, error) {
	n, err := body.inflated.Read(buffer)
	if err == io.EOF {
		if endErr := body.confirmEnd(); endErr != io.EOF {
			// Withhold bytes decoded before the failed end.
			n, err = 0, endErr
		}
	}
	if err != nil && err != io.EOF && body.source.firstError() == nil {
		body.cancel(errInvalidGzip)
		<-body.closed
		// Withhold bytes decoded together with the error.
		return 0, err
	}
	return n, err
}

// confirmEnd returns io.EOF when the network body ends with the gzip stream.
func (body *gzipBody) confirmEnd() error {
	var next [1]byte
	for {
		n, err := body.buffered.Read(next[:])
		if n > 0 {
			return errGzipTrailingData
		}
		if err != nil {
			return err
		}
	}
}

var errGzipTrailingData = errors.New("data follows the gzip request body")

// Close releases a blocked Read and closes only the network body: the backend
// stream calls Close concurrently with Read, which the network body allows
// and gzip.Reader does not. gzip.Reader holds nothing to release.
func (body *gzipBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return body.source.Close()
}

// observedBody records the first read error of the backend input, which the
// backend stream does not report when the backend itself also fails.
//
// With stop set, it is the backend's input, and a read error (the request
// limit, a disconnect, the idle limit) stops the backend: Read cancels the
// stream with the error and blocks until the stream closes the body, which it
// does only after it has terminated the backend's process group. Ending the
// input by closing it is not enough: git-http-backend copies a declared
// Content-Length to Git and loops without end, holding the repository lock,
// when its input ends early. The backend also never sees a cut request as
// complete.
type observedBody struct {
	io.ReadCloser
	// tee, when set, receives every byte that Read returns.
	tee       io.Writer
	stop      context.CancelCauseFunc
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err != nil && err != io.EOF {
		body.mu.Lock()
		if body.err == nil {
			body.err = err
		}
		body.mu.Unlock()
		if body.stop != nil {
			body.stop(err)
			<-body.closed
			// Withhold bytes read together with the error.
			return 0, err
		}
	}
	if n > 0 && body.tee != nil {
		_, _ = body.tee.Write(buffer[:n])
	}
	return n, err
}

// Close releases a blocked Read and closes the body.
func (body *observedBody) Close() error {
	if body.closed != nil {
		body.closeOnce.Do(func() { close(body.closed) })
	}
	return body.ReadCloser.Close()
}

func (body *observedBody) firstError() error {
	body.mu.Lock()
	defer body.mu.Unlock()
	return body.err
}

// fetchBufferOverflow reports whether a failed request is an upload-pack
// request over maximumFetchRequest, the bound the fetch gate and
// git-http-backend both hold in memory. ownsError is the gate's own error, if
// the request body reported one, and reason is the failure classification of
// the backend exit (see failureReason), which names the same bound when Git
// refused the request first.
func fetchBufferOverflow(route route, request *http.Request, ownsError error, reason string) bool {
	if route.service != "git-upload-pack" || request.Method != http.MethodPost {
		return false
	}
	return errors.Is(ownsError, errFetchTooLarge) || reason == errFetchTooLarge.Error()
}

// failureReason classifies a failed Smart HTTP operation for the server log.
// It returns "" for successful operations, client disconnects and response
// write failures. An invalid gzip body comes before cancellation because the
// handler cancels the backend for it. Backend diagnostics come before the
// exit status because a backend that stops reading early also makes the input
// copy fail. It never includes backend output, which can echo request bytes.
func failureReason(err error, stderr []byte, tooLarge, invalidGzip, timedOut bool) string {
	message := string(stderr)
	var exitErr *exec.ExitError
	switch {
	case tooLarge:
		return requestTooLarge
	case invalidGzip:
		return errInvalidGzip.Error()
	case errors.Is(err, errResponseTooLarge):
		return "response exceeded the size limit"
	case timedOut || errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, context.Canceled):
		return ""
	case strings.Contains(message, "protocol error"):
		return "Git protocol error"
	case strings.Contains(message, "request was larger than our maximum size"):
		return "request exceeded the Git backend request buffer"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("Git backend exited with status %d", exitErr.ExitCode())
	case err == nil && strings.Contains(message, "fatal:"):
		return "Git backend reported a fatal error"
	default:
		return ""
	}
}

// logGitFailure logs why a Git request failed. reason is githttp's own
// text, such as a limit that was reached.
func logGitFailure(route route, method string, reason string) {
	log.Printf("Git %s request for repository %q failed: %s", requestKind(route, method), route.repositoryID, reason)
}

// busyMessage is the answer of a Git request that found no free transfer slot
// or no repository within its bound. A Git client shows it for a refused ref
// advertisement, and shows only the status for a refused transfer POST.
const busyMessage = "Git service is busy with other transfers; try again shortly"

// storageChangedMessage is the answer of a push refused because the repository
// folder changed after OwnGit claimed it.
const storageChangedMessage = "The repository folder changed after OwnGit started, so OwnGit stopped accepting pushes. Put the original folder back to continue, or restart OwnGit to use the folder now in its place."

// answerBusyLocked answers a request that could not take its repository lock
// within its own bound, or whose client left while it waited, with the same
// busy answer as a request that found no transfer slot. bound names the limit
// that ended the wait and limit is the moment it ends. The caller has already
// given its slots back. A client that left does not read the answer, and its
// leaving is not a failure of the Git service to log, unless the limit had
// already passed: a connection read deadline that passes at the same instant
// cancels the request, so a wait that ended at its bound can reach this point
// as a cancellation.
func answerBusyLocked(writer http.ResponseWriter, route route, method, bound string, limit time.Time, err error) {
	if !errors.Is(err, context.Canceled) || (!limit.IsZero() && !time.Now().Before(limit)) {
		logGitFailure(route, method, "the repository stayed locked for the whole "+bound)
	}
	writer.Header().Set("Retry-After", "10")
	http.Error(writer, busyMessage, http.StatusServiceUnavailable)
}

// logCause logs line followed by err, the error that explains it, quoted
// by the server log's rule (see logtext). Every githttp line that carries an
// error goes through here. Work under ctx whose client went away caused
// nothing to fix, so an error that is only that cancellation is not logged;
// a cancellation beside a real failure, or from another context, is.
func logCause(ctx context.Context, line string, err error) {
	if errors.Is(ctx.Err(), context.Canceled) && logtext.Intended(err, context.Canceled) {
		return
	}
	log.Printf("%s: %s", line, logtext.Cause(err))
}

// requestKind names a Smart HTTP request in the log: fetch or push, and
// whether it asked for the ref advertisement.
func requestKind(route route, method string) string {
	kind := "fetch"
	if route.service == "git-receive-pack" {
		kind = "push"
	}
	if method == http.MethodGet {
		kind += " ref advertisement"
	}
	return kind
}

// buildsPack reports whether the request makes Git build a pack: a fetch
// or clone, not the ref listing that protocol version 2 sends as a POST
// first. It looks at the first 20 bytes of an uncompressed body (Git
// compresses only a large fetch request) and leaves the body to be read as
// before.
func buildsPack(request *http.Request, route route) bool {
	if route.service != "git-upload-pack" || request.Method != http.MethodPost {
		return false
	}
	if request.Header.Get("Content-Encoding") != "" {
		return true
	}
	head := make([]byte, 20)
	n, _ := io.ReadFull(request.Body, head)
	request.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head[:n]), request.Body), request.Body}
	return !bytes.HasPrefix(head[:n], []byte("0014command=ls-refs\n"))
}

func (h *Handler) Active() int64 { return h.active.Load() }

func (h *Handler) Wait(ctx context.Context) error {
	h.operationMu.Lock()
	h.closing = true
	h.operationMu.Unlock()
	done := make(chan struct{})
	go func() {
		h.operations.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type route struct {
	// repositoryID is the repository address the path names until
	// ServeHTTP resolves it to the repository's ID.
	repositoryID string
	// suffix is the path after the repository, such as info/refs.
	suffix  string
	service string
	query   string
	// config is Git config at command-line scope for this request only.
	config [][2]string
	// moved names the repository's current address when the request named an
	// earlier one of its addresses, or "".
	moved string
	// scope names the refs a fetch may reach; nil means mainScope.
	scope *fetchScope
}

func parseRoute(request *http.Request) (route, bool) {
	if !strings.HasPrefix(request.URL.Path, "/git/") {
		return route{}, false
	}
	remainder := strings.TrimPrefix(request.URL.Path, "/git/")
	marker := strings.Index(remainder, ".git/")
	if marker <= 0 {
		return route{}, false
	}
	id := remainder[:marker]
	suffix := remainder[marker+5:]
	// The route accepts exactly the IDs a repository can be created with.
	if repository.ValidateID(id) != nil {
		return route{}, false
	}
	return serviceRoute(request, id, suffix)
}

// serviceRoute is the route of a Smart HTTP request for repository id whose
// path ends with suffix after the repository.
func serviceRoute(request *http.Request, id, suffix string) (route, bool) {
	switch {
	case request.Method == http.MethodGet && suffix == "info/refs":
		services, present := request.URL.Query()["service"]
		if len(request.URL.Query()) != 1 || !present || len(services) != 1 {
			return route{}, false
		}
		service := services[0]
		if service != "git-upload-pack" && service != "git-receive-pack" {
			return route{}, false
		}
		return route{repositoryID: id, suffix: suffix, service: service, query: "service=" + service}, true
	case request.Method == http.MethodPost && (suffix == "git-upload-pack" || suffix == "git-receive-pack"):
		mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || len(parameters) != 0 || mediaType != "application/x-"+suffix+"-request" || request.URL.RawQuery != "" {
			return route{}, false
		}
		return route{repositoryID: id, suffix: suffix, service: suffix}, true
	default:
		return route{}, false
	}
}

// cgiEnvironment describes the request to git-http-backend. contentLength is
// the length of the body the backend reads, or -1 when unknown (chunked or
// inflated here), so the backend reads until end of input.
func (h *Handler) cgiEnvironment(request *http.Request, route route, contentLength int64) ([]string, error) {
	info := requestctx.Of(request)
	host, port, err := net.SplitHostPort(info.Host)
	if err != nil {
		host = info.Host
		if info.Secure() {
			port = "443"
		} else {
			port = "80"
		}
	}
	protocol := request.Header.Get("Git-Protocol")
	if len(protocol) > 256 || strings.ContainsAny(protocol, "\x00\r\n") {
		return nil, errors.New("invalid Git-Protocol header")
	}
	environment := []string{
		"GIT_PROJECT_ROOT=" + h.Repositories.RepositoryRoot(),
		"GIT_HTTP_EXPORT_ALL=1",
		"REQUEST_METHOD=" + request.Method,
		"PATH_INFO=/" + route.repositoryID + ".git/" + route.suffix,
		"QUERY_STRING=" + route.query,
		"CONTENT_TYPE=" + request.Header.Get("Content-Type"),
		"REMOTE_ADDR=" + info.ClientAddress,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"SERVER_PROTOCOL=" + request.Proto,
		"SCRIPT_NAME=/git",
	}
	if contentLength >= 0 {
		environment = append(environment, "CONTENT_LENGTH="+strconv.FormatInt(contentLength, 10))
	}
	if protocol != "" {
		environment = append(environment, "HTTP_GIT_PROTOCOL="+protocol)
	}
	if info.Secure() {
		environment = append(environment, "HTTPS=on")
	}
	for i, setting := range route.config {
		environment = append(environment, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, setting[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting[1]))
	}
	if len(route.config) > 0 {
		environment = append(environment, "GIT_CONFIG_COUNT="+strconv.Itoa(len(route.config)))
	}
	return environment, nil
}

// copyCGIResponse copies the backend's CGI response to writer. A non-nil
// observer receives every body byte the backend sent, once, and a non-empty
// notice is written into the body where the protocol carries a server message
// (writeMovedNotice).
func (h *Handler) copyCGIResponse(writer *responseState, stdout io.Reader, observer io.Writer, maximum int64, notice string) error {
	reader := bufio.NewReaderSize(stdout, 32<<10)
	totalHeaderBytes := 0
	status := http.StatusOK
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read Git CGI headers: %w", err)
		}
		totalHeaderBytes += len(line)
		if totalHeaderBytes > 64<<10 {
			return errors.New("Git CGI response headers are too large")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return errors.New("Git CGI response contained a malformed header")
		}
		name = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name == "" || strings.ContainsAny(value, "\r\n") {
			return errors.New("Git CGI response contained an invalid header")
		}
		if name == "Status" {
			fields := strings.Fields(value)
			if len(fields) == 0 {
				return errors.New("Git CGI response contained an invalid status")
			}
			parsed, err := strconv.Atoi(fields[0])
			if err != nil || parsed < 100 || parsed > 599 {
				return errors.New("Git CGI response contained an invalid status")
			}
			status = parsed
			continue
		}
		if hopByHop(name) {
			continue
		}
		writer.Header().Add(name, value)
	}
	writer.WriteHeader(status)
	// While Git prepares data it writes only small keepalive and progress
	// packets. They must reach the client, and any proxy with a read timeout,
	// soon instead of waiting in the response buffer for more data. So once
	// the request body has ended, whenever everything Git has written so far
	// was passed on, a flush follows: the first after responseFlushDelay, the
	// others at once. Bulk data
	// still goes out in writes of up to 32 KiB. A failed flush leaves the
	// connection failed, so the next write reports it, and the handler
	// reports a failure after Git's last output.
	//
	// Every byte of the backend's body reaches the observer exactly once,
	// whether the notice scan below passes it on or the copy that follows
	// reads it, so the observer sits under the buffer the scan peeks into.
	// The notice is OwnGit's own text and never reaches the observer: it is
	// not part of the backend's report.
	if observer != nil {
		reader = bufio.NewReaderSize(io.TeeReader(reader, observer), 32<<10)
	}
	// A request that named an earlier address tells the client where the
	// repository is now, before the transfer's own data.
	sent := int64(0)
	if notice != "" {
		var err error
		if sent, err = writeMovedNotice(writer, reader, notice); err != nil {
			return err
		}
	}
	if maximum > 0 && sent > maximum {
		return errResponseTooLarge
	}
	if reader.Buffered() == 0 {
		writer.scheduleFlush()
	}
	body := io.Reader(reader)
	if maximum > 0 {
		body = &io.LimitedReader{R: body, N: maximum + 1}
	}
	buffer := make([]byte, 32<<10)
	written := sent
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return err
			}
			written += int64(n)
			if maximum > 0 && written > maximum {
				return errResponseTooLarge
			}
			if reader.Buffered() == 0 {
				writer.scheduleFlush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// movedNoticeScan bounds how far into a response body OwnGit looks for the
// side-band data that carries a Git client's messages.
const movedNoticeScan = 64 << 10

// writeMovedNotice writes notice as a side-band progress message at the one
// point the Git protocol carries a server message to the person who runs the
// command: immediately before the first side-band packet of the response
// body, whatever section or acknowledgement comes before it. The packets
// before that one are protocol data (a ref list, a section header, an
// acknowledgement) and are passed through unchanged. A body that is not
// pkt-lines (a pack without side-band framing), one that ends before a
// side-band packet, and one whose packets carry none within movedNoticeScan
// get no message, so no client ever receives a packet it does not expect. It
// returns how many bytes it wrote to the client, for the response size
// accounting.
func writeMovedNotice(writer *responseState, reader *bufio.Reader, notice string) (int64, error) {
	// The line break ends the message: a Git client shows remote output
	// line by line.
	message := notice + "\n"
	if len(message)+5 > maximumPacket {
		return 0, nil
	}
	packet := fmt.Sprintf("%04x\x02%s", len(message)+5, message)
	var written int64
	for written < movedNoticeScan {
		head, err := reader.Peek(5)
		if err != nil {
			return written, nil
		}
		length, err := strconv.ParseUint(string(head[:4]), 16, 16)
		switch {
		case err != nil || length == 3 || length > maximumPacket:
			// Not a pkt-line: the body carries no side-band data, or it is
			// not a response OwnGit writes a message into.
			return written, nil
		case length == 0 || length == 2:
			// A flush packet ends the response, and a response-end packet
			// ends it for a stateless connection.
			return written, nil
		case length == 1:
			// A delimiter separates sections, so the next section may be
			// the one that carries the side-band data. Every special packet
			// is four bytes on the wire, whatever its length says.
			length = 4
		case length > 4 && head[4] >= 1 && head[4] <= 3:
			// The client reads a packet that starts with its side-band
			// channel as remote output, at any point of the transfer.
			if _, err := writer.Write([]byte(packet)); err != nil {
				return written, err
			}
			return written + int64(len(packet)), nil
		}
		copied, err := io.CopyN(writer, reader, int64(length))
		written += copied
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

// responseFlushDelay is how long the first flush after the end of the request
// body waits, the one that sends the status line and headers. It keeps the
// headers of a response that Git starts at once from overtaking a reverse
// proxy that is still finishing the request: Go's httputil.ReverseProxy over
// HTTP/1.1 reads the end of the request body only after it has forwarded the
// last byte, and a response that it passes on before that read makes it fail
// the request. Later flushes cannot overtake that read, so they go out at
// once, and Git's keepalives reach a proxy with a read timeout in time.
var responseFlushDelay = 200 * time.Millisecond

// responseState passes the backend response on under the transfer deadlines.
//
// It flushes nothing, not even the status line, until the request body was
// read to its end. git-http-backend writes its headers before it reads a
// push, and a reverse proxy that serves HTTP/1.1 half duplex, such as Go's
// httputil.ReverseProxy with default settings, stops forwarding the request
// body once it passes response headers on. After the end of the body, the
// first flush waits responseFlushDelay; later flushes go out at once, and the
// rest of the response when Git finishes.
//
// A response that goes out before the end of the body anyway, because Git
// wrote more than net/http buffers or finished without reading the rest,
// says Connection: close: the rest may never be read, so the connection
// cannot carry another request.
type responseState struct {
	http.ResponseWriter
	deadlines *transferDeadlines
	bodyEnded func() bool
	// mu orders the handler's writes with flushes from the timer, which the
	// end of the request body can start on the goroutine that feeds Git its
	// input.
	mu         sync.Mutex
	status     int
	sent       bool
	flushed    bool
	flushDelay time.Duration
	timer      *time.Timer
	stopped    bool
}

// WriteHeader sets the status. Until a body byte was written or the
// response was flushed, a later call replaces it.
func (writer *responseState) WriteHeader(status int) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if !writer.sent {
		writer.status = status
	}
}

func (writer *responseState) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.sendHeaderLocked()
	return writer.deadlines.write(writer.ResponseWriter, content)
}

func (writer *responseState) sendHeaderLocked() {
	if writer.sent {
		return
	}
	writer.sent = true
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if !writer.bodyEnded() {
		writer.Header().Set("Connection", "close")
	}
	writer.ResponseWriter.WriteHeader(writer.status)
}

// scheduleFlush flushes what was written so far once the request body has
// ended: after flushDelay the first time, at once after that.
func (writer *responseState) scheduleFlush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.scheduleLocked()
}

func (writer *responseState) scheduleLocked() {
	if writer.stopped || !writer.bodyEnded() || (writer.status == 0 && !writer.sent) {
		return
	}
	if writer.flushed {
		_ = writer.flushLocked()
		return
	}
	if writer.timer != nil {
		return
	}
	writer.timer = time.AfterFunc(writer.flushDelay, func() {
		writer.mu.Lock()
		defer writer.mu.Unlock()
		writer.timer = nil
		if !writer.stopped {
			_ = writer.flushLocked()
		}
	})
}

func (writer *responseState) flushLocked() error {
	if writer.status == 0 && !writer.sent {
		return nil
	}
	writer.sendHeaderLocked()
	writer.flushed = true
	return writer.deadlines.flush()
}

// requestBodyEnded schedules the flush of the response held back while the
// request body was read.
func (writer *responseState) requestBodyEnded() { writer.scheduleFlush() }

// finish sends what Git wrote after its last output, whether or not the
// request body has ended.
func (writer *responseState) finish() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.flushLocked()
}

// stop ends the timed flushes before the handler returns.
func (writer *responseState) stop() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.stopped = true
	if writer.timer != nil {
		writer.timer.Stop()
		writer.timer = nil
	}
}

// started reports whether any part of the response was passed on, so its
// status can no longer change.
func (writer *responseState) started() bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.sent
}

func executableName(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}

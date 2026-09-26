package githttp

import (
	"bufio"
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/requestctx"
)

type Handler struct {
	Git          *gitexec.Runner
	Repositories *repository.Manager
	BackendPath  string
	Authorize    func(*http.Request) bool
	// OnReceive wakes check reconciliation after git-receive-pack exits. It is
	// advisory and must never change the already completed Git response.
	OnReceive        func(string)
	MaximumRequest   int64
	MaximumResponse  int64
	OperationTimeout time.Duration
	// IdleTimeout stops a transfer whose client moves no data for this long:
	// a response write the client does not accept, or a request body read
	// that receives nothing. Time that Git spends working without output does
	// not count. Zero leaves only OperationTimeout.
	IdleTimeout time.Duration
	// QueueWait bounds how long a request waits for a transfer slot before
	// it is refused with 503 and Retry-After.
	QueueWait   time.Duration
	slots       *admission
	active      atomic.Int64
	operationMu sync.Mutex
	operations  sync.WaitGroup
	closing     bool
}

func New(git *gitexec.Runner, repositories *repository.Manager, backendPath string, maximumConcurrent int) (*Handler, error) {
	if maximumConcurrent <= 0 {
		maximumConcurrent = 4
	}
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
		MaximumRequest: 4 << 30, MaximumResponse: 4 << 30, OperationTimeout: 30 * time.Minute,
		IdleTimeout: time.Minute, QueueWait: 90 * time.Second, slots: newAdmission(maximumConcurrent),
	}, nil
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
	if h.Authorize != nil && !h.Authorize(request) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit"`)
		http.Error(writer, "authentication required", http.StatusUnauthorized)
		return
	}
	route, ok := parseRoute(request)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	gzipped, ok := requestBodyEncoding(request)
	if !ok {
		// RFC 7694: name the accepted request encoding in the refusal.
		writer.Header().Set("Accept-Encoding", "gzip")
		http.Error(writer, "unsupported Content-Encoding; send gzip or an uncompressed body", http.StatusUnsupportedMediaType)
		return
	}
	repositoryPath, _, exists, err := h.Repositories.ExistingPath(request.Context(), route.repositoryID)
	if errors.Is(err, repository.ErrRepositoryPreparing) {
		writer.Header().Set("Retry-After", "30")
		http.Error(writer, "repository is being prepared; try again later", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(writer, "repository storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !exists {
		http.NotFound(writer, request)
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
	release, err := h.slots.acquire(request.Context(), route.repositoryID, h.QueueWait)
	if errors.Is(err, errBusy) {
		logGitFailure(route, request.Method, "no Git transfer slot became free within "+h.QueueWait.String())
		writer.Header().Set("Retry-After", "10")
		http.Error(writer, "Git service is busy with other transfers; try again shortly", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		return
	}
	defer release()
	h.active.Add(1)
	defer h.active.Add(-1)

	lock := h.Repositories.Locks.For(route.repositoryID)
	// Releasing the write lock invalidates the cached ref snapshot unless the
	// request provably changed no ref: the ref advertisement never writes, and
	// a push only when its complete report refused every command before a
	// write.
	refsUnchanged := request.Method == http.MethodGet
	if route.service == "git-receive-pack" {
		lock.Lock()
		defer func() {
			if refsUnchanged {
				lock.UnlockWithoutRefChanges()
			} else {
				lock.Unlock()
			}
		}()
	} else {
		lock.RLock()
		defer lock.RUnlock()
	}

	controller := http.NewResponseController(writer)
	operationContext := request.Context()
	cancel := func() {}
	var deadline time.Time
	if h.OperationTimeout > 0 {
		deadline = time.Now().Add(h.OperationTimeout)
		operationContext, cancel = context.WithDeadline(operationContext, deadline)
		// Context cancellation alone does not interrupt a blocked socket Read or
		// Write. Connection deadlines bound both directions without buffering a
		// pack request or response in memory.
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
	}
	if h.OperationTimeout > 0 || h.IdleTimeout > 0 {
		defer func() {
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}()
	}
	defer cancel()
	deadlines := &transferDeadlines{controller: controller, idle: h.IdleTimeout, overall: deadline}
	request = request.WithContext(operationContext)

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
	if h.MaximumRequest > 0 {
		// The subprocess copies stdin on its own goroutine. Passing the live
		// ResponseWriter to MaxBytesReader would let that goroutine write a 413
		// concurrently with the CGI response copier. The handler reports the
		// returned MaxBytesError after both owned streams have stopped instead.
		body = http.MaxBytesReader(nil, body, h.MaximumRequest)
	}
	if gzipped {
		// Git clients gzip upload-pack requests over 1 KiB. Inflate here so the
		// same MaximumRequest also bounds the inflated bytes the backend reads.
		// git-http-backend's own inflation has no output bound.
		source := &observedBody{ReadCloser: body}
		inflated, err := gzip.NewReader(source)
		if err != nil {
			_ = body.Close()
			status, reason := http.StatusBadRequest, "could not read the request body"
			var maxErr *http.MaxBytesError
			if source.firstError() == nil {
				reason = errInvalidGzip.Error()
				logGitFailure(route, request.Method, reason)
			} else if errors.As(source.firstError(), &maxErr) {
				status, reason = http.StatusRequestEntityTooLarge, "request body exceeded the size limit"
			}
			http.Error(writer, reason, status)
			return
		}
		inflated.Multistream(false)
		body = &gzipBody{inflated: inflated, source: source, cancel: cancelStream, closed: make(chan struct{})}
		if h.MaximumRequest > 0 {
			body = http.MaxBytesReader(nil, body, h.MaximumRequest)
		}
		contentLength = -1
	}
	input = &observedBody{ReadCloser: body, stop: cancelStream, closed: make(chan struct{})}
	extraEnvironment, err := h.cgiEnvironment(request, route, contentLength)
	if err != nil {
		_ = input.Close()
		http.Error(writer, "invalid Git protocol request", http.StatusBadRequest)
		return
	}
	// git-http-backend writes its response headers before it reads a push,
	// and the response is flushed as Git writes it. Without full duplex,
	// net/http would read and discard the rest of the request body at the
	// first flush. In full duplex it neither does that nor announces that it
	// closes the connection after a body left unread, so the response does:
	// otherwise a client that reuses the connection gets EOF.
	_ = controller.EnableFullDuplex()
	committed := &responseState{ResponseWriter: writer, deadlines: deadlines,
		bodyUnfinished: func() bool { return request.ContentLength != 0 && !network.ended.Load() }}
	var report *pushReport
	var observer io.Writer
	if route.service == "git-receive-pack" && request.Method == http.MethodPost {
		report = &pushReport{}
		observer = report
	}
	stderr, err := h.Git.Stream(streamContext, h.BackendPath, repositoryPath, input, extraEnvironment, func(stdout io.Reader) error {
		err := h.copyCGIResponse(committed, stdout, observer)
		consumeFailed.Store(err != nil)
		return err
	})
	if err == nil && route.service == "git-receive-pack" && h.OnReceive != nil {
		h.OnReceive(route.repositoryID)
	}
	inBand := ""
	if report != nil {
		// Without a side band, receive-pack's messages arrive on stderr.
		report.scan(stderr)
		inBand = report.reason()
		refsUnchanged = err == nil && !consumeFailed.Load() && report.refsUnchanged()
	}
	if err == nil {
		// Make sure the end of the response went out while the transfer still
		// holds its deadlines; the copy does not report a failed flush after
		// Git's last output. A failure here changes nothing Git already did.
		if flushErr := deadlines.flush(); flushErr != nil && deadlines.stalled.Load() {
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
	if err != nil && !committed.wroteHeader {
		status := http.StatusBadGateway
		if tooLarge {
			status = http.StatusRequestEntityTooLarge
		} else if invalidGzip {
			status = http.StatusBadRequest
		}
		http.Error(committed, http.StatusText(status), status)
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

var errResponseTooLarge = errors.New("Git response exceeded the configured limit")

// gzipBody inflates a request body. When the network body ended cleanly but
// the gzip stream is corrupt, truncated or fails its checksum, Read cancels
// the operation and blocks until the backend stream closes the body. The
// stream terminates the backend before that close, so the backend never sees
// the end of its input and cannot take a partial request as complete. Errors
// of the network body itself, such as a disconnect or the size limit, keep
// their existing handling.
type gzipBody struct {
	inflated  *gzip.Reader
	source    *observedBody
	cancel    context.CancelCauseFunc
	closed    chan struct{}
	closeOnce sync.Once
}

func (body *gzipBody) Read(buffer []byte) (int, error) {
	n, err := body.inflated.Read(buffer)
	if err != nil && err != io.EOF && body.source.firstError() == nil {
		body.cancel(errInvalidGzip)
		<-body.closed
		// Withhold bytes decoded together with the error.
		return 0, err
	}
	return n, err
}

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
		return "request body exceeded the size limit"
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

func logGitFailure(route route, method string, reason string) {
	kind := "fetch"
	if route.service == "git-receive-pack" {
		kind = "push"
	}
	if method == http.MethodGet {
		kind += " ref advertisement"
	}
	log.Printf("Git %s request for repository %q failed: %s", kind, route.repositoryID, reason)
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
	repositoryID string
	pathInfo     string
	service      string
	query        string
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
		return route{repositoryID: id, pathInfo: "/" + id + ".git/info/refs", service: service, query: "service=" + service}, true
	case request.Method == http.MethodPost && (suffix == "git-upload-pack" || suffix == "git-receive-pack"):
		mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || len(parameters) != 0 || mediaType != "application/x-"+suffix+"-request" || request.URL.RawQuery != "" {
			return route{}, false
		}
		return route{repositoryID: id, pathInfo: "/" + id + ".git/" + suffix, service: suffix}, true
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
		"PATH_INFO=" + route.pathInfo,
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
	return environment, nil
}

// copyCGIResponse copies the backend's CGI response to writer. A non-nil
// observer also receives the body bytes as they are copied.
func (h *Handler) copyCGIResponse(writer *responseState, stdout io.Reader, observer io.Writer) error {
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
	// at once instead of waiting in the response buffer. So the response is
	// flushed whenever everything Git has written so far was passed on. Bulk
	// data still goes out in writes of up to 32 KiB. A failed flush leaves the
	// connection failed, so the next write reports it, and the handler reports
	// a failure after Git's last output.
	if reader.Buffered() == 0 {
		_ = writer.deadlines.flush()
	}
	body := io.Reader(reader)
	if observer != nil {
		body = io.TeeReader(reader, observer)
	}
	if h.MaximumResponse > 0 {
		body = &io.LimitedReader{R: body, N: h.MaximumResponse + 1}
	}
	buffer := make([]byte, 32<<10)
	var written int64
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return err
			}
			written += int64(n)
			if h.MaximumResponse > 0 && written > h.MaximumResponse {
				return errResponseTooLarge
			}
			if reader.Buffered() == 0 {
				_ = writer.deadlines.flush()
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

func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

// responseState writes the backend response under the transfer deadlines and
// records whether the status was sent. A response sent before the request
// body was read to its end says Connection: close, because the rest may never
// be read and the connection then cannot carry another request.
type responseState struct {
	http.ResponseWriter
	deadlines      *transferDeadlines
	bodyUnfinished func() bool
	wroteHeader    bool
}

func (writer *responseState) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	if writer.bodyUnfinished() {
		writer.Header().Set("Connection", "close")
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseState) Write(content []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.deadlines.write(writer.ResponseWriter, content)
}

func executableName(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}

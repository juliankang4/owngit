package githttp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
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
	semaphore        chan struct{}
	active           atomic.Int64
	operationMu      sync.Mutex
	operations       sync.WaitGroup
	closing          bool
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
		semaphore: make(chan struct{}, maximumConcurrent),
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
	repositoryPath, _, exists, err := h.Repositories.ExistingPath(request.Context(), route.repositoryID)
	if errors.Is(err, repository.ErrRepositoryPreparing) {
		writer.Header().Set("Retry-After", "30")
		http.Error(writer, "repository is being prepared after startup; try again later", http.StatusServiceUnavailable)
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
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	case <-request.Context().Done():
		return
	}
	h.active.Add(1)
	defer h.active.Add(-1)

	lock := h.Repositories.Locks.For(route.repositoryID)
	if route.service == "git-receive-pack" {
		lock.Lock()
		defer lock.Unlock()
	} else {
		lock.RLock()
		defer lock.RUnlock()
	}

	operationContext := request.Context()
	cancel := func() {}
	if h.OperationTimeout > 0 {
		deadline := time.Now().Add(h.OperationTimeout)
		operationContext, cancel = context.WithDeadline(operationContext, deadline)
		controller := http.NewResponseController(writer)
		// Context cancellation alone does not interrupt a blocked socket Read or
		// Write. Connection deadlines bound both directions without buffering a
		// pack request or response in memory.
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		defer func() {
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}()
	}
	defer cancel()
	request = request.WithContext(operationContext)

	body := request.Body
	if h.MaximumRequest > 0 {
		// The subprocess copies stdin on its own goroutine. Passing the live
		// ResponseWriter to MaxBytesReader would let that goroutine write a 413
		// concurrently with the CGI response copier. The handler reports the
		// returned MaxBytesError after both owned streams have stopped instead.
		body = http.MaxBytesReader(nil, request.Body, h.MaximumRequest)
	}
	extraEnvironment, err := h.cgiEnvironment(request, route)
	if err != nil {
		http.Error(writer, "invalid Git protocol request", http.StatusBadRequest)
		return
	}
	committed := &responseState{ResponseWriter: writer}
	_, err = h.Git.Stream(request.Context(), h.BackendPath, repositoryPath, body, extraEnvironment, func(stdout io.Reader) error {
		return h.copyCGIResponse(committed, stdout)
	})
	if err == nil && route.service == "git-receive-pack" && h.OnReceive != nil {
		h.OnReceive(route.repositoryID)
	}
	if err != nil && !committed.wroteHeader {
		status := http.StatusBadGateway
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(writer, http.StatusText(status), status)
	}
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
	if id != strings.ToLower(id) || !validID(id) {
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

func validID(id string) bool {
	if id == "" || len(id) > 100 || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *Handler) cgiEnvironment(request *http.Request, route route) ([]string, error) {
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil {
		host = request.Host
		if request.TLS != nil {
			port = "443"
		} else {
			port = "80"
		}
	}
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		remote = request.RemoteAddr
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
		"REMOTE_ADDR=" + remote,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"SERVER_PROTOCOL=" + request.Proto,
		"SCRIPT_NAME=/git",
	}
	if request.ContentLength >= 0 {
		environment = append(environment, "CONTENT_LENGTH="+strconv.FormatInt(request.ContentLength, 10))
	}
	if protocol != "" {
		environment = append(environment, "HTTP_GIT_PROTOCOL="+protocol)
	}
	if request.TLS != nil {
		environment = append(environment, "HTTPS=on")
	}
	return environment, nil
}

func (h *Handler) copyCGIResponse(writer *responseState, stdout io.Reader) error {
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
	if h.MaximumResponse <= 0 {
		_, err := io.Copy(writer, reader)
		return err
	}
	limited := &io.LimitedReader{R: reader, N: h.MaximumResponse + 1}
	written, err := io.Copy(writer, limited)
	if err != nil {
		return err
	}
	if written > h.MaximumResponse {
		return errors.New("Git response exceeded the configured limit")
	}
	return nil
}

func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

type responseState struct {
	http.ResponseWriter
	wroteHeader bool
}

func (writer *responseState) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseState) Write(content []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(content)
}

func executableName(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}

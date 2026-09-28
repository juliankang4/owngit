package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/checkrun"
	"owngit/internal/importfetch"
	"owngit/internal/importsync"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Every answer with status 503 takes that status from unavailable, and
// every answer with status 500 from internalError, which both log the
// answer's cause. The check reads this package's source: these statuses may
// appear nowhere else, except in a comparison, which reads a status and
// writes none.
//
// Two kinds of 503 answer are written by githttp, which logs their causes
// itself: its Smart HTTP answers on the /git/ routes, which serveHTTP hands
// to githttp, and an archive githttp refused, which the archive handlers
// write with githttp's status. Its access check is AuthorizeGit here, which
// logs a check that could not be completed.
func TestFailureStatusesLogTheirCause(t *testing.T) {
	names, err := filepath.Glob("*.go")
	noErr(t, err)
	files := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, name, nil, 0)
		noErr(t, err)
		httpName := importName(file, "net/http")
		var ancestors []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				ancestors = ancestors[:len(ancestors)-1]
				return true
			}
			if source, ok := failureSource(node, httpName); ok && !readsOrReturns(ancestors, source) {
				t.Errorf("%s: this status does not come from %s", files.Position(node.Pos()), source)
			}
			ancestors = append(ancestors, node)
			return true
		})
	}
}

// importName is the name file uses for the package at importPath: its
// alias, or the last path element, or "" when file does not import it.
func importName(file *ast.File, importPath string) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != strconv.Quote(importPath) {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path.Base(importPath)
	}
	return ""
}

// failureSource reports whether node is status 503 or 500, as net/http's
// constant under the name httpName or as an integer literal in any base,
// and names the function that alone may return it.
func failureSource(node ast.Node, httpName string) (string, bool) {
	sources := map[string]string{"StatusServiceUnavailable": "unavailable", "StatusInternalServerError": "internalError"}
	var name string
	switch node := node.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == httpName {
			name = node.Sel.Name
		}
	case *ast.Ident:
		if httpName == "." {
			name = node.Name
		}
	case *ast.BasicLit:
		value, err := strconv.ParseInt(node.Value, 0, 64)
		if node.Kind == token.INT && err == nil {
			name = map[int64]string{503: "StatusServiceUnavailable", 500: "StatusInternalServerError"}[value]
		}
	}
	source, ok := sources[name]
	return source, ok
}

// readsOrReturns reports whether a status under ancestors is compared, or
// is the status that source returns.
func readsOrReturns(ancestors []ast.Node, source string) bool {
	for _, node := range ancestors {
		if function, ok := node.(*ast.FuncDecl); ok && function.Recv == nil && function.Name.Name == source {
			return true
		}
	}
	switch parent := ancestors[len(ancestors)-1].(type) {
	case *ast.BinaryExpr:
		return parent.Op == token.EQL || parent.Op == token.NEQ
	case *ast.CaseClause:
		return true
	}
	return false
}

// A cause is logged as one line, whatever its text holds, so Git's error
// output cannot start a line of its own in the server log.
func TestUnavailableCauseIsLoggedOnOneLine(t *testing.T) {
	serverLog := captureServerLog(t)
	request := httptestRequest(t, http.MethodGet, "/repositories/project")
	logFailure(request, "file read", errors.New("fatal: bad object\n2026/09/28 12:00:00 GET /forged: \x1b[31mforged"))
	logged := serverLog.String()
	if strings.Count(logged, "\n") != 1 || strings.Contains(logged, "\x1b") || !strings.Contains(logged, `GET /repositories/project: file read could not be completed: "fatal: bad object\n2026/09/28`) {
		t.Fatalf("logged %q, want one line with the cause escaped", logged)
	}
}

// A state that works as intended is answered as unavailable without a log
// line, but only when every cause in the error is such a state. A
// cancellation is intended only when the request's own client went away.
func TestIntendedStatesAreNotLogged(t *testing.T) {
	serverLog := captureServerLog(t)
	alive := httptestRequest(t, http.MethodGet, "/api/v1/repositories/project")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	left := alive.WithContext(ctx)
	deadline, stop := context.WithDeadline(context.Background(), time.Now())
	defer stop()
	expired := alive.WithContext(deadline)
	disk := errors.New("disk I/O error")
	sentinels := []error{
		&pullrequest.Problem{Code: "repository_preparing", Cause: repository.ErrRepositoryPreparing},
		&checkrun.RuntimeUnavailableError{Code: "docker_unavailable", Err: errors.New("no daemon")},
		&importsync.Problem{Code: importsync.CodeRuntimeUnavailable, Message: "OwnGit is shutting down", Cause: importsync.ErrShuttingDown},
	}
	// A lock wait that ended because the client left: the busy repository is
	// only why it waited. The repository page names the bare busy state
	// once its request has ended (handleRepositoryRoute).
	waitLeft := fmt.Errorf("%w (%w)", repository.ErrRepositoryInUse, context.Canceled)
	for _, intended := range append(sentinels, fmt.Errorf("read: %w", context.Canceled), errors.Join(context.Canceled, fmt.Errorf("stop: %w", context.Canceled)),
		waitLeft, repository.ErrRepositoryInUse) {
		if status := unavailable(left, "intended", intended); status != http.StatusServiceUnavailable {
			t.Errorf("unavailable(%v)=%d", intended, status)
		}
	}
	if logged := serverLog.String(); logged != "" {
		t.Fatalf("intended states logged %q", logged)
	}

	for _, check := range []struct {
		what    string
		request *http.Request
		err     error
	}{
		{"a failed read", alive, disk},
		// Another context was cancelled; the client still waits for an answer.
		{"a cancellation the client did not cause", alive, fmt.Errorf("read: %w", context.Canceled)},
		{"a cancellation joined with a failed write", left, errors.Join(context.Canceled, disk)},
		{"preparation joined with a failed read", left, errors.Join(sentinels[0], disk)},
		{"an unavailable runtime joined with a failed read", left, errors.Join(sentinels[1], disk)},
		{"shutdown joined with a failed read", left, errors.Join(sentinels[2], disk)},
		{"a busy repository while the client waits", alive, repository.ErrRepositoryInUse},
		{"a lock wait that ran out of time", expired, fmt.Errorf("%w (%w)", repository.ErrRepositoryInUse, context.DeadlineExceeded)},
		{"a busy repository joined with a failed read", left, errors.Join(repository.ErrRepositoryInUse, disk)},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		unavailable(check.request, "state read", check.err)
		checkLoggedSteps(t, check.what, loggedFailures(serverLog, since), "state read")
	}

	// An import stopped by its owner whose bookkeeping then failed is still
	// a state failure that someone must look at.
	endFailureWindows()
	since := len(serverLog.String())
	stopped := &importsync.Problem{Code: importsync.CodeStateUnavailable, Message: "state store is unavailable", Cause: errors.Join(context.Canceled, disk)}
	if status, _, _, _ := importProblemHTTP(left, "import run", stopped); status != http.StatusServiceUnavailable {
		t.Errorf("stopped import with a failed bookkeeping write status=%d", status)
	}
	checkLoggedSteps(t, "stopped import with a failed bookkeeping write", loggedFailures(serverLog, since), "import run")
}

// A long request path is logged as a short prefix with its full length, so a
// request cannot make one log line as long as its path.
func TestLongPathIsCutInTheLog(t *testing.T) {
	serverLog := captureServerLog(t)
	long := "/repositories/" + strings.Repeat("a", 4000)
	logFailure(httptestRequest(t, http.MethodGet, long), "settings read", errors.New("disk I/O error"))
	logged := serverLog.String()
	if !strings.Contains(logged, " GET "+long[:256]+"...(cut, 4014 bytes): settings read could not be completed: ") || len(logged) > 400 {
		t.Fatalf("logged %q, want the path cut to 256 bytes with its length", logged)
	}
}

func httptestRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://owngit.test"+target, nil)
	noErr(t, err)
	return request
}

// State failures behind the APIs, the task page and Settings are answered
// as unavailable and logged once each.
func TestStateFailuresBehindAPIsAndPagesAreLogged(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	token := "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	_, _, err := fixture.store.CreateHelperCredential(ctx, "project", "logged", "", hash[:], time.Now())
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	if _, status := dashboardGET(t, client, server.URL+"/repositories/project"); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	browserAdminSessionFor(t, fixture, server.URL, jar, "logged-admin")
	api := server.URL + "/api/v1/repositories"
	serverLog := captureServerLog(t)
	for _, check := range []struct {
		what, table string
		send        func() int
		step        string
	}{
		{"repository API list", "repositories", func() int { return responseStatusOf(t, sendJSON(t, http.MethodGet, api, nil)) }, "repository list read"},
		{"repository API record", "repositories", func() int { return responseStatusOf(t, sendJSON(t, http.MethodGet, api+"/project", nil)) }, "repository record read"},
		{"task API list", "tasks", func() int {
			return responseStatusOf(t, checkRequest(t, http.MethodGet, api+"/project/tasks", nil, token))
		}, "task list read"},
		{"task API creation", "tasks", func() int {
			return responseStatusOf(t, checkRequest(t, http.MethodPost, api+"/project/tasks", checkapi.CreateTaskInput{Title: "Logged"}, token))
		}, "task creation"},
		{"configured check policy API", "check_policies", func() int {
			return responseStatusOf(t, adminAPIRequest(t, http.MethodGet, api+"/project/check-policy", nil, "admin-password"))
		}, "configured check policy read"},
		{"configured check policy API save", "check_policies", func() int {
			return responseStatusOf(t, adminAPIRequest(t, http.MethodPut, api+"/project/check-policy", checkapi.PolicyInput{
				Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000,
				MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
			}, "admin-password"))
		}, "configured check policy save"},
		{"task page", "check_configurations", func() int {
			_, status := dashboardGET(t, client, server.URL+"/repositories/project/tasks")
			return status
		}, "check configuration read"},
		{"Settings", "trusted_hosts", func() int {
			_, status := dashboardGET(t, client, server.URL+"/settings")
			return status
		}, "network settings read"},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		restore := hideTable(t, fixture.store, check.table)
		status := check.send()
		restore()
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s status=%d, want 503", check.what, status)
		}
		checkLoggedSteps(t, check.what, loggedFailures(serverLog, since), check.step)
	}
}

// A fault in OwnGit is answered as internal and a run that could not be
// completed now, including an unclassified pull request or import error, as
// unavailable; both log their cause once. An unclassified import error is
// explained on a page with the neutral import failure text.
func TestInternalFaultsAndUnclassifiedRunsAreLogged(t *testing.T) {
	serverLog := captureServerLog(t)
	request := httptestRequest(t, http.MethodPost, "/api/v1/repositories/project/pull-requests/1/merge")
	integrity := pullrequest.NewProblem("repository_integrity_error", "The merge intent is in an unexpected state.")
	gitRun := errors.New("fatal: unable to write new index file")
	for _, check := range []struct {
		what   string
		status func() int
		want   int
		cause  string
	}{
		{"records that contradict each other", func() int { return apiStatus(request, "pull request merge", integrity) }, http.StatusInternalServerError, "unexpected state"},
		{"an unclassified pull request run", func() int { return apiStatus(request, "pull request merge", gitRun) }, http.StatusServiceUnavailable, "new index file"},
		{"an unclassified import error", func() int {
			status, _, _, _ := importProblemHTTP(request, "import run", gitRun)
			return status
		}, http.StatusServiceUnavailable, "new index file"},
		{"a page that cannot be rendered", func() int {
			renderer, err := webui.New()
			noErr(t, err)
			recorder := httptest.NewRecorder()
			(&App{Renderer: renderer}).render(recorder, request, http.StatusOK, nil)
			return recorder.Code
		}, http.StatusInternalServerError, "nil page"},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		if status := check.status(); status != check.want {
			t.Errorf("%s status=%d, want %d", check.what, status, check.want)
		}
		lines := loggedFailures(serverLog, since)
		if len(lines) != 1 || !strings.Contains(lines[0], check.cause) {
			t.Errorf("%s logged %q, want one line naming %q", check.what, lines, check.cause)
		}
	}
	if notice := importFailureNotice(gitRun, ""); notice.Code != webui.MsgImportFailed {
		t.Errorf("an unclassified import error is explained as %q, want %q", notice.Code, webui.MsgImportFailed)
	}
	// Its code says it was not classified, not that a feature is unsupported.
	if _, code, _, _ := importProblemHTTP(request, "import run", gitRun); code != importsync.CodeUnclassified {
		t.Errorf("an unclassified import error is coded %q, want %q", code, importsync.CodeUnclassified)
	}
}

// Input the store refuses stays refused input, not a failure to log.
func TestRefusedInputIsNotLoggedAsAFailure(t *testing.T) {
	fixture := newAPIFixture(t, false)
	token := "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	_, _, err := fixture.store.CreateHelperCredential(context.Background(), "project", "refused", "", hash[:], time.Now())
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	api := server.URL + "/api/v1/repositories/project"
	serverLog := captureServerLog(t)
	for _, check := range []struct {
		what, code string
		send       func() *http.Response
	}{
		{"a policy without a queue", "invalid_check_policy", func() *http.Response {
			return adminAPIRequest(t, http.MethodPut, api+"/check-policy", checkapi.PolicyInput{
				Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000,
				MaxOutputLimitBytes: 64 << 10, QueueLimit: 0, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
			}, "admin-password")
		}},
		{"an overlong task title", "invalid_task", func() *http.Response {
			return checkRequest(t, http.MethodPost, api+"/tasks", checkapi.CreateTaskInput{Title: strings.Repeat("t", 201)}, token)
		}},
	} {
		response := check.send()
		if code := apiErrorCode(t, response); response.StatusCode != http.StatusUnprocessableEntity || code != check.code {
			t.Errorf("%s status=%d code=%q, want 422 %s", check.what, response.StatusCode, code, check.code)
		}
	}
	if logged := serverLog.String(); logged != "" {
		t.Fatalf("refused input logged %q", logged)
	}
}

func responseStatusOf(t *testing.T, response *http.Response) int {
	t.Helper()
	response.Body.Close()
	return response.StatusCode
}

// A cancelled first import whose repository record then cannot be read does
// not claim that no repository was added: the outcome is unconfirmed, the
// answer is unavailable, and the failed read is logged.
func TestCancelledImportWithAnUnreadableRecordIsUnconfirmed(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cancel-admin")
	var restore func()
	fixture.app.Imports.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		if _, err := fixture.app.Imports.Cancel(context.Background(), "fresh"); err != nil {
			t.Errorf("cancel: %v", err)
		}
		<-ctx.Done()
		restore = hideTable(t, fixture.store, "repositories")
		return nil, ctx.Err()
	}
	serverLog := captureServerLog(t)
	created := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"fresh"}, "url": {"https://example.invalid/team/fresh.git"},
		"mode": {"standalone"}, "admin_password": {"admin-password"},
	}, server.URL)
	if restore != nil {
		restore()
	}
	if created.status != http.StatusServiceUnavailable || !strings.Contains(created.body, webui.Text(webui.LangEN, webui.MsgImportCancelledUnsure)) ||
		strings.Contains(created.body, webui.Text(webui.LangEN, webui.MsgImportCancelledNoRepo)) {
		t.Fatalf("cancelled import with an unreadable record status=%d body=%s", created.status, created.body)
	}
	checkLoggedSteps(t, "cancelled import", loggedFailures(serverLog, 0), "repository record read")
}

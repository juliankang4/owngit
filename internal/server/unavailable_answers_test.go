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
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkrun"
	"owngit/internal/importfetch"
	"owngit/internal/importsync"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/webui"
)

// Every answer with status 503 takes that status from unavailable, which
// logs the answer's cause. The check reads this package's source: 503 may
// appear nowhere else, except in a comparison, which reads a status and
// writes none.
//
// Two kinds of 503 answer are written by githttp, which logs their causes
// itself: its Smart HTTP answers on the /git/ routes, which serveHTTP hands
// to githttp, and an archive githttp refused, which the archive handlers
// write with githttp's status. Its access check is AuthorizeGit here, which
// logs a check that could not be completed.
func TestOnlyUnavailableAnswersUnavailable(t *testing.T) {
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
			if isStatus503(node, httpName) && !readsOrDecides503(ancestors) {
				t.Errorf("%s: status 503 does not come from unavailable", files.Position(node.Pos()))
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

// isStatus503 reports whether node is net/http's StatusServiceUnavailable,
// under the name httpName, or an integer literal of 503 in any base.
func isStatus503(node ast.Node, httpName string) bool {
	switch node := node.(type) {
	case *ast.SelectorExpr:
		pkg, ok := node.X.(*ast.Ident)
		return ok && pkg.Name == httpName && node.Sel.Name == "StatusServiceUnavailable"
	case *ast.Ident:
		return httpName == "." && node.Name == "StatusServiceUnavailable"
	case *ast.BasicLit:
		value, err := strconv.ParseInt(node.Value, 0, 64)
		return node.Kind == token.INT && err == nil && value == 503
	}
	return false
}

// readsOrDecides503 reports whether a 503 under ancestors is compared, or is the
// status unavailable returns.
func readsOrDecides503(ancestors []ast.Node) bool {
	for _, node := range ancestors {
		if function, ok := node.(*ast.FuncDecl); ok && function.Recv == nil && function.Name.Name == "unavailable" {
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
	logUnavailable(request, "file read", errors.New("fatal: bad object\n2026/09/28 12:00:00 GET /forged: \x1b[31mforged"))
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
		since := len(serverLog.String())
		unavailable(check.request, "state read", check.err)
		checkLoggedSteps(t, check.what, loggedUnavailable(serverLog, since), "state read")
	}

	// An import stopped by its owner whose bookkeeping then failed is still
	// a state failure that someone must look at.
	since := len(serverLog.String())
	stopped := &importsync.Problem{Code: importsync.CodeStateUnavailable, Message: "state store is unavailable", Cause: errors.Join(context.Canceled, disk)}
	if status, _, _, _ := importProblemHTTP(left, "import run", stopped); status != http.StatusServiceUnavailable {
		t.Errorf("stopped import with a failed bookkeeping write status=%d", status)
	}
	checkLoggedSteps(t, "stopped import with a failed bookkeeping write", loggedUnavailable(serverLog, since), "import run")
}

// A long request path is logged as a short prefix with its full length, so a
// request cannot make one log line as long as its path.
func TestLongPathIsCutInTheLog(t *testing.T) {
	serverLog := captureServerLog(t)
	long := "/repositories/" + strings.Repeat("a", 4000)
	logUnavailable(httptestRequest(t, http.MethodGet, long), "settings read", errors.New("disk I/O error"))
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
		{"configured check policy API", "check_policies", func() int {
			return responseStatusOf(t, adminAPIRequest(t, http.MethodGet, api+"/project/check-policy", nil, "admin-password"))
		}, "configured check policy read"},
		{"task page", "check_configurations", func() int {
			_, status := dashboardGET(t, client, server.URL+"/repositories/project/tasks")
			return status
		}, "check configuration read"},
		{"Settings", "trusted_hosts", func() int {
			_, status := dashboardGET(t, client, server.URL+"/settings")
			return status
		}, "network settings read"},
	} {
		since := len(serverLog.String())
		restore := hideTable(t, fixture.store, check.table)
		status := check.send()
		restore()
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s status=%d, want 503", check.what, status)
		}
		checkLoggedSteps(t, check.what, loggedUnavailable(serverLog, since), check.step)
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
	checkLoggedSteps(t, "cancelled import", loggedUnavailable(serverLog, 0), "repository record read")
}

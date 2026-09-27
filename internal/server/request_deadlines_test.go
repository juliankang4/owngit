package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/webui"
)

// hangBound only keeps a connection the server never answers from hanging a
// test. The operation deadlines in these tests are an hour.
const hangBound = 15 * time.Second

// verifiedPage is a page deadline that leaves room for password
// verification, SQLite and Git work on a loaded test machine, so an
// authorized request only runs out of it when a test holds it past it.
const verifiedPage = 3 * time.Second

// longOperationServer serves a password-protected fixture whose import runs
// and archives may take an hour, under a page deadline of page.
func longOperationServer(t *testing.T, page time.Duration) (apiFixture, string) {
	t.Helper()
	fixture := newAPIFixture(t, true)
	fixture.app.HTTPTimeout = page
	fixture.app.ImportRunTimeout = time.Hour
	fixture.app.GitHTTP.OperationTimeout = time.Hour
	server := httptest.NewUnstartedServer(fixture.app.Handler())
	// The production server settings, with this test's page deadline as the
	// server read limit.
	server.Config.ReadHeaderTimeout = 10 * time.Second
	server.Config.ReadTimeout = page
	server.Config.WriteTimeout = ImportRunRequestTimeout(fixture.app.ImportRunTimeout)
	server.Start()
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	noErr(t, err)
	return fixture, parsed.Host
}

// withheldBody sends a request that announces a one-byte body and never
// sends it. It returns the response, or nil when the server closed the
// connection without one, and whether the server closed the connection. The
// server must do either within hangBound.
func withheldBody(t *testing.T, address, method, target, authorization string) (*http.Response, bool) {
	t.Helper()
	connection, err := net.Dial("tcp", address)
	noErr(t, err)
	defer connection.Close()
	head := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1\r\n", method, target, address)
	if authorization != "" {
		head += "Authorization: " + authorization + "\r\n"
	}
	_, err = io.WriteString(connection, head+"\r\n")
	noErr(t, err)
	noErr(t, connection.SetReadDeadline(time.Now().Add(hangBound)))
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: method})
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, true
	}
	if err != nil {
		t.Fatalf("%s %s: no response within %s: %v", method, target, hangBound, err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	noErrf(t, err, "%s %s: response body", method, target)
	response.Body.Close()
	// The rest of the connection is empty. A server that keeps it open
	// leaves this read to run into the hang bound.
	_, err = reader.ReadByte()
	return response, err == io.EOF
}

// responseStatus describes response for a failure message.
func responseStatus(response *http.Response) string {
	if response == nil {
		return "none"
	}
	return fmt.Sprintf("%d (Connection: close %v)", response.StatusCode, response.Close)
}

func basicCredential(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// A client that withholds its request body is answered at the page deadline
// and the connection is closed, on the import and archive routes as on any
// other. Before authorization such a request never gets the hour its
// operation could take.
func TestWithheldBodyIsAnsweredAtThePageDeadline(t *testing.T) {
	_, address := longOperationServer(t, 200*time.Millisecond)
	for _, test := range []struct {
		name, method, target string
		status               int
	}{
		{"import API", http.MethodPost, "/api/v1/repositories/project/import/run", http.StatusUnauthorized},
		{"new import page", http.MethodPost, "/repositories/new-import", http.StatusSeeOther},
		{"archive API", http.MethodGet, "/api/v1/repositories/project/archive?ref=main&format=zip", http.StatusUnauthorized},
		{"archive page", http.MethodGet, "/repositories/project/archive?ref=main&format=zip", http.StatusSeeOther},
		{"ordinary API", http.MethodPost, "/api/v1/repositories/project/pulls", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response, closed := withheldBody(t, address, test.method, test.target, "")
			if response == nil || response.StatusCode != test.status || !closed {
				t.Fatalf("response=%v closed=%v, want %d and a closed connection", responseStatus(response), closed, test.status)
			}
		})
	}
}

// An authorized caller gets the operation deadline only for the work: its
// import body is still read under the page deadline, and an archive request
// whose body is left unread closes the connection instead of reading it.
func TestAuthorizedOperationReadsItsBodyUnderThePageDeadline(t *testing.T) {
	_, address := longOperationServer(t, verifiedPage)
	t.Run("import API", func(t *testing.T) {
		t.Parallel()
		response, closed := withheldBody(t, address, http.MethodPost, "/api/v1/repositories/fresh/import/run", basicCredential("admin", "admin-password"))
		// The time to answer ends with the page deadline too, so the refusal
		// is sent only when it is written before the runtime notices that.
		if (response != nil && response.StatusCode != http.StatusBadRequest) || !closed {
			t.Fatalf("response=%v closed=%v, want the connection closed, after 400 or without a response", responseStatus(response), closed)
		}
	})
	t.Run("archive API", func(t *testing.T) {
		t.Parallel()
		response, closed := withheldBody(t, address, http.MethodGet, "/api/v1/repositories/project/archive?ref=main&format=zip", basicCredential("reader", "shared-password"))
		if response == nil || response.StatusCode != http.StatusOK || !response.Close || !closed {
			t.Fatalf("response=%v closed=%v, want 200 announcing and doing a close", responseStatus(response), closed)
		}
	})
}

// A request whose handler returns after the connection deadline cannot be
// answered. The server log names it, so the client's empty reply has an
// explanation.
func TestReplyLostAtTheDeadlineIsLogged(t *testing.T) {
	const page = 400 * time.Millisecond
	app := newConfiguredApp(t)
	app.HTTPTimeout = page
	app.requestObserver = func(request *http.Request) {
		// Uninterruptible work, such as a password hash, that outlasts the
		// reply reserve.
		time.Sleep(page + 100*time.Millisecond)
	}
	serverLog := captureServerLog(t)
	server := serve(t, app.Handler())
	// The path is logged escaped, so a line break in it cannot start a
	// forged log line.
	if response, err := http.Get(server.URL + "/settings%0Aforged"); err == nil {
		response.Body.Close()
		t.Fatalf("a request past its deadline was answered %d", response.StatusCode)
	}
	logged := serverLog.String()
	if !strings.Contains(logged, "GET /settings%0Aforged ended ") || !strings.Contains(logged, "after its connection deadline") || strings.Count(logged, "\n") != 1 {
		t.Fatalf("server log after a lost reply: %q", logged)
	}
}

// slowSourceFetch answers an import fetch with an empty source after wait.
// The fetch starts after its request did, so a wait of one page deadline
// holds the run past that deadline.
func slowSourceFetch(wait time.Duration) func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
	return func(ctx context.Context, request importfetch.Request, consumer importfetch.PackConsumer) (*importfetch.Result, error) {
		select {
		case <-time.After(wait):
			return emptySourceFetch(ctx, request, consumer)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// An authorized import that runs past the page deadline completes on the
// API, the new import page and the refresh action, and the API connection
// then serves the next request under its own page deadline.
func TestAuthorizedImportOutlivesThePageDeadline(t *testing.T) {
	const page = verifiedPage
	fixture := newAPIFixture(t, false)
	fixture.app.HTTPTimeout = page
	fixture.app.Imports.Fetch = slowSourceFetch(page)
	server := serve(t, fixture.app.Handler())
	address := server.Listener.Addr().String()

	connection, err := net.Dial("tcp", address)
	noErr(t, err)
	defer connection.Close()
	noErr(t, connection.SetDeadline(time.Now().Add(hangBound)))
	reader := bufio.NewReader(connection)
	send := func(method, target, body string) *http.Response {
		t.Helper()
		head := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nAuthorization: %s\r\n", method, target, address, basicCredential("admin", "admin-password"))
		if body != "" {
			head += fmt.Sprintf("Content-Type: application/json\r\nContent-Length: %d\r\n", len(body))
		}
		_, err := io.WriteString(connection, head+"\r\n"+body)
		noErr(t, err)
		response, err := http.ReadResponse(reader, &http.Request{Method: method})
		noErrf(t, err, "%s %s", method, target)
		return response
	}
	started := time.Now()
	run := send(http.MethodPost, "/api/v1/repositories/apislow/import/run", `{"name":"apislow","url":"https://example.invalid/team/apislow.git","mode":"standalone"}`)
	content, err := io.ReadAll(run.Body)
	noErr(t, err)
	if run.StatusCode != http.StatusOK || !strings.Contains(string(content), `"status":"complete"`) || time.Since(started) < page || run.Close {
		t.Fatalf("API import after %s: status=%d close=%v body=%s", time.Since(started), run.StatusCode, run.Close, content)
	}
	next := send(http.MethodGet, "/api/v1/repositories/apislow/import", "")
	content, err = io.ReadAll(next.Body)
	noErr(t, err)
	if next.StatusCode != http.StatusOK {
		t.Fatalf("request after the import on the same connection: status=%d body=%s", next.StatusCode, content)
	}

	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "slow-import")
	started = time.Now()
	created := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"pageslow"}, "url": {"https://example.invalid/team/pageslow.git"},
		"mode": {"standalone"}, "admin_password": {"admin-password"},
	}, server.URL)
	if created.status != http.StatusSeeOther || !strings.HasSuffix(created.header.Get("Location"), "/repositories/pageslow/import?notice=import_started") || time.Since(started) < page {
		t.Fatalf("new import page after %s: status=%d location=%q", time.Since(started), created.status, created.header.Get("Location"))
	}
	started = time.Now()
	refreshed := browserForm(t, client, server.URL+"/repositories/pageslow/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {"admin-password"},
	}, server.URL)
	if refreshed.status != http.StatusSeeOther || !strings.HasSuffix(refreshed.header.Get("Location"), "/repositories/pageslow/import?notice=import_refreshed") || time.Since(started) < page {
		t.Fatalf("refresh after %s: status=%d location=%q", time.Since(started), refreshed.status, refreshed.header.Get("Location"))
	}
}

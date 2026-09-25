package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/version"
)

// mcpSession is an in-process owngit mcp session over pipes.
type mcpSession struct {
	t      *testing.T
	input  io.WriteCloser
	lines  chan string
	done   chan error
	nextID int
}

func startMCPSession(t *testing.T, options mcpOptions) *mcpSession {
	t.Helper()
	if options.workdir == "" {
		options.workdir = t.TempDir()
	}
	if options.resultLimit == 0 {
		options.resultLimit = defaultMCPResultLimit
	}
	server, err := newMCPServer(context.Background(), options)
	noErr(t, err)
	return serveMCPSession(t, server)
}

func serveMCPSession(t *testing.T, server *mcpServer) *mcpSession {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := server.serve(context.Background(), inputReader, outputWriter)
		_ = outputWriter.Close()
		done <- err
	}()
	return newMCPSession(t, inputWriter, outputReader, done)
}

// newMCPSession reads the messages written to output. done receives the
// result of the session once it ends.
func newMCPSession(t *testing.T, input io.WriteCloser, output io.Reader, done chan error) *mcpSession {
	t.Helper()
	session := &mcpSession{t: t, input: input, lines: make(chan string, 64), done: done}
	go func() {
		reader := bufio.NewReader(output)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(session.lines)
				return
			}
			session.lines <- line
		}
	}()
	t.Cleanup(func() {
		_ = input.Close()
		select {
		case <-session.done:
		case <-time.After(30 * time.Second):
			t.Error("the MCP session did not end after its input closed")
		}
	})
	return session
}

func (session *mcpSession) send(message string) {
	session.t.Helper()
	if _, err := io.WriteString(session.input, message+"\n"); err != nil {
		session.t.Fatalf("write %q: %v", message, err)
	}
}

// receive returns the next message the server wrote.
func (session *mcpSession) receive() rpcTestResponse {
	session.t.Helper()
	select {
	case line, ok := <-session.lines:
		if !ok {
			session.t.Fatal("the MCP output ended")
		}
		if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
			session.t.Fatalf("a message is not one line: %q", line)
		}
		var response rpcTestResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil || response.JSONRPC != "2.0" {
			session.t.Fatalf("invalid response %q: %v", line, err)
		}
		return response
	case <-time.After(60 * time.Second):
		session.t.Fatal("no MCP response")
	}
	return rpcTestResponse{}
}

// expectSilence checks that no message arrives for a short while.
func (session *mcpSession) expectSilence() {
	session.t.Helper()
	select {
	case line := <-session.lines:
		session.t.Fatalf("unexpected message %q", line)
	case <-time.After(300 * time.Millisecond):
	}
}

func (session *mcpSession) request(method string, params any) rpcTestResponse {
	session.t.Helper()
	session.nextID++
	id := session.nextID
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	encoded, err := json.Marshal(message)
	noErr(session.t, err)
	session.send(string(encoded))
	response := session.receive()
	if string(response.ID) != strconv.Itoa(id) {
		session.t.Fatalf("response id %s, want %d", response.ID, id)
	}
	return response
}

// call runs a tool and returns its text and error flag.
func (session *mcpSession) call(tool string, arguments any) (string, bool) {
	session.t.Helper()
	response := session.request("tools/call", map[string]any{"name": tool, "arguments": arguments})
	if response.Error != nil {
		session.t.Fatalf("%s: protocol error %+v", tool, response.Error)
	}
	var result toolResult
	noErr(session.t, json.Unmarshal(response.Result, &result))
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		session.t.Fatalf("%s: content %+v", tool, result.Content)
	}
	return result.Content[0].Text, result.IsError
}

// callError runs a tool that must fail and returns its error code.
func (session *mcpSession) callError(tool string, arguments any) string {
	session.t.Helper()
	text, isError := session.call(tool, arguments)
	var envelope pullrequest.ErrorEnvelope
	if !isError || json.Unmarshal([]byte(text), &envelope) != nil || envelope.OK {
		session.t.Fatalf("%s %v: isError=%v text=%s", tool, arguments, isError, text)
	}
	return envelope.Error.Code
}

type rpcTestResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (session *mcpSession) toolNames() ([]string, map[string]toolInputSchema) {
	session.t.Helper()
	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema toolInputSchema `json:"inputSchema"`
		} `json:"tools"`
	}
	noErr(session.t, json.Unmarshal(session.request("tools/list", nil).Result, &listed))
	var names []string
	schemas := map[string]toolInputSchema{}
	for _, tool := range listed.Tools {
		if tool.Description == "" || tool.InputSchema.Type != "object" || tool.InputSchema.AdditionalProperties {
			session.t.Errorf("tool %s: description or schema incomplete: %+v", tool.Name, tool.InputSchema)
		}
		names = append(names, tool.Name)
		schemas[tool.Name] = tool.InputSchema
	}
	sort.Strings(names)
	return names, schemas
}

// cliOutput runs an owngit command in process and returns its JSON output
// without the final newline.
func cliOutput(t *testing.T, command func([]string) error, arguments ...string) string {
	t.Helper()
	var output string
	_, err := captureStderr(func() error {
		var err error
		output, err = captureStdout(func() error { return command(arguments) })
		return err
	})
	noErr(t, err)
	return strings.TrimSuffix(output, "\n")
}

// startPullRequestFixture serves an open-access repository "project" with
// branches main and feature and one pull request, and returns the server
// URL, the pull request number, and the work tree.
func startPullRequestFixture(t *testing.T) (serverURL, number, work string) {
	t.Helper()
	serverURL, _ = startRepositoryCLIServer(t, "")
	remote := []string{"--server", serverURL, "--accept-insecure-http"}
	cliOutput(t, repoCommand, append([]string{"create", "--name", "project", "--description", "MCP fixture"}, remote...)...)
	work = filepath.Join(t.TempDir(), "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "MCP Test")
	runPRGit(t, work, "config", "user.email", "mcp-test@example.invalid")
	runPRGit(t, work, "remote", "add", "origin", serverURL+"/git/project.git")
	commitFile(t, work, "base.txt", "base\n")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	runPRGit(t, work, "checkout", "-q", "-b", "feature")
	commitFile(t, work, "feature.txt", "<feature>\n")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	created := runPRCommandJSON(t, append([]string{"create", "--title", "Feature", "--source", "feature", "--target", "main", "--repository", "project"}, remote...))
	return serverURL, strconv.FormatInt(created.PullRequest.Number, 10), work
}

func commitFile(t *testing.T, work, name, content string) {
	t.Helper()
	noErr(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o600))
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-q", "-m", name)
}

// The shared password from the launch file reaches the server.
func TestMCPSendsTheSharedPassword(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	cliOutput(t, repoCommand, "create", "--name", "project", "--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile)
	without := startMCPSession(t, mcpOptions{server: serverURL, acceptInsecureHTTP: true})
	if code := without.callError("repository_list", nil); code == "" {
		t.Fatal("no error code without the password")
	}
	with := startMCPSession(t, mcpOptions{server: serverURL, passwordFile: passwordFile, acceptInsecureHTTP: true})
	want := cliOutput(t, repoCommand, "list", "--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile)
	if text, isError := with.call("repository_list", nil); isError || text != want {
		t.Fatalf("repository_list isError=%v\n got %s\nwant %s", isError, text, want)
	}
}

// The read tools return exactly what the matching command prints.
func TestMCPReadToolsReturnTheCommandLineJSON(t *testing.T) {
	serverURL, number, _ := startPullRequestFixture(t)
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--repository", "project"}
	session := startMCPSession(t, mcpOptions{server: serverURL, repository: "project", acceptInsecureHTTP: true})

	initialized := session.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	var hello struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	noErr(t, json.Unmarshal(initialized.Result, &hello))
	if hello.ProtocolVersion != mcpProtocolVersion || hello.Capabilities.Tools == nil || hello.ServerInfo.Name != "owngit" ||
		hello.ServerInfo.Version != version.Version || !strings.Contains(hello.Instructions, "repository project") || !strings.Contains(hello.Instructions, "untrusted") {
		t.Fatalf("initialize result %s", initialized.Result)
	}
	session.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if response := session.request("ping", nil); string(response.Result) != "{}" {
		t.Fatalf("ping result %s", response.Result)
	}

	names, schemas := session.toolNames()
	if got := strings.Join(names, " "); strings.Contains(got, "check_") || !strings.Contains(got, "pull_request_diff pull_request_list") ||
		!strings.Contains(got, "pull_request_show repository_list repository_show") {
		t.Fatalf("tools without a helper credential: %s", got)
	}
	if _, ok := schemas["pull_request_show"].Properties["repository"]; ok {
		t.Fatal("a fixed repository is still a tool argument")
	}

	for _, test := range []struct {
		tool      string
		arguments any
		want      string
	}{
		{"repository_list", nil, cliOutput(t, repoCommand, "list", "--server", serverURL, "--accept-insecure-http")},
		{"repository_show", map[string]any{}, cliOutput(t, repoCommand, append([]string{"show"}, remote...)...)},
		{"pull_request_list", map[string]any{"repository": "project"}, cliOutput(t, prCommand, append([]string{"list"}, remote...)...)},
		{"pull_request_show", map[string]any{"number": json.Number(number)}, cliOutput(t, prCommand, append([]string{"show", "--number", number}, remote...)...)},
		{"pull_request_diff", map[string]any{"number": json.Number(number)}, cliOutput(t, prCommand, append([]string{"diff", "--number", number}, remote...)...)},
		{"pull_request_diff", map[string]any{"number": json.Number(number), "patch": false}, cliOutput(t, prCommand, append([]string{"diff", "--number", number, "--stat"}, remote...)...)},
	} {
		text, isError := session.call(test.tool, test.arguments)
		if isError || text != test.want {
			t.Errorf("%s %v: isError=%v\n got %s\nwant %s", test.tool, test.arguments, isError, text, test.want)
		}
	}
	// A pull request that does not exist is a tool error with the command
	// line's code.
	if code := session.callError("pull_request_show", map[string]any{"number": 99}); code != "pull_request_not_found" {
		t.Fatalf("missing pull request code %q", code)
	}
}

// Arguments cannot reach another server, repository, path, or command, and
// malformed or unknown requests get the standard JSON-RPC errors.
func TestMCPRefusesArgumentsAndMessagesOutsideItsScope(t *testing.T) {
	fake := newFakeOwnGit(t)
	session := startMCPSession(t, mcpOptions{server: fake.server.URL, repository: "project", acceptInsecureHTTP: true})
	for _, arguments := range []map[string]any{
		{"server": "http://127.0.0.1:7839"},
		{"repository": "other"},
		{"path": "/etc"},
		{"workdir": "/tmp"},
		{"command": "true"},
		{"repository": "../project"},
	} {
		code := session.callError("pull_request_list", arguments)
		if code != "invalid_arguments" && code != "repository_not_allowed" {
			t.Errorf("%v: code %q", arguments, code)
		}
	}
	for _, arguments := range []any{map[string]any{"number": "1"}, map[string]any{"number": 1.5}, []int{1}, "text"} {
		if code := session.callError("pull_request_show", arguments); code != "invalid_arguments" {
			t.Errorf("%v: code %q", arguments, code)
		}
	}
	if fake.requests.Load() != 0 {
		t.Fatalf("refused calls sent %d requests", fake.requests.Load())
	}
	// The fixed repository may be named again.
	if _, isError := session.call("pull_request_list", map[string]any{"repository": "project"}); isError || fake.requests.Load() != 1 {
		t.Fatalf("naming the fixed repository failed: requests=%d", fake.requests.Load())
	}

	for _, test := range []struct {
		message string
		code    int
		id      string
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list"`, rpcParseError, "null"},
		{`[{"jsonrpc":"2.0","id":2,"method":"ping"}]`, rpcInvalidRequest, "null"},
		{`{"jsonrpc":"2.0","id":null,"method":"ping"}`, rpcInvalidRequest, "null"},
		{`{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`, rpcInvalidRequest, "null"},
		{`{"id":3,"method":"ping"}`, rpcInvalidRequest, "3"},
		{`{"jsonrpc":"2.0","id":"four","method":"resources/list"}`, rpcMethodNotFound, `"four"`},
		{`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shell","arguments":{}}}`, rpcInvalidParams, "5"},
		{`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{}}`, rpcInvalidParams, "6"},
		{`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":"pull_request_list"}`, rpcInvalidParams, "7"},
		{`{"jsonrpc":"2.0","id":8,"method":7}`, rpcInvalidRequest, "null"},
		{`{"jsonrpc":"2.0","id":9,"method":"ping","params":` + strings.Repeat(" ", maximumMCPMessage) + `{}}`, rpcInvalidRequest, "null"},
	} {
		session.send(test.message)
		response := session.receive()
		if response.Error == nil || response.Error.Code != test.code || string(response.ID) != test.id {
			t.Errorf("%.80s: got id %s error %+v, want %d", test.message, response.ID, response.Error, test.code)
		}
	}
	// Notifications and client responses get no answer, and the session
	// still works after every refusal.
	session.send(`{"jsonrpc":"2.0","method":"notifications/unknown"}`)
	session.send(`{"jsonrpc":"2.0","id":99,"result":{}}`)
	session.send(``)
	session.expectSilence()
	if response := session.request("ping", nil); response.Error != nil {
		t.Fatalf("ping after refusals: %+v", response.Error)
	}
}

// Without a fixed repository each repository tool needs one, and a helper
// credential needs a fixed repository.
func TestMCPWithoutAFixedRepository(t *testing.T) {
	fake := newFakeOwnGit(t)
	session := startMCPSession(t, mcpOptions{server: fake.server.URL, acceptInsecureHTTP: true})
	_, schemas := session.toolNames()
	if schema := schemas["pull_request_list"]; schema.Properties["repository"].Type != "string" || len(schema.Required) != 1 || schema.Required[0] != "repository" {
		t.Fatalf("pull_request_list schema without a fixed repository: %+v", schema)
	}
	if code := session.callError("pull_request_list", map[string]any{}); code != "invalid_arguments" {
		t.Fatalf("missing repository code %q", code)
	}
	if code := session.callError("pull_request_list", map[string]any{"repository": "Bad/Name"}); code != "invalid_arguments" {
		t.Fatalf("invalid repository code %q", code)
	}
	if _, isError := session.call("pull_request_list", map[string]any{"repository": "project"}); isError || fake.requests.Load() != 1 {
		t.Fatalf("named repository failed: requests=%d", fake.requests.Load())
	}

	token := writePrivate(t, filepath.Join(t.TempDir(), "token"), "synthetic-token\n")
	_, err := newMCPServer(context.Background(), mcpOptions{server: fake.server.URL, credentialFile: token, acceptInsecureHTTP: true, workdir: t.TempDir(), resultLimit: defaultMCPResultLimit})
	if commandErrorCode(err) != "invalid_arguments" {
		t.Fatalf("a helper credential without a repository: %v", err)
	}
}

// Launch settings follow the command line's rules: plain HTTP needs the
// acknowledgement, and a credential goes to a server inferred from origin
// only when its file names that server.
func TestMCPLaunchResolvesLikeTheCommandLine(t *testing.T) {
	hostile := newFakeOwnGit(t)
	dir := t.TempDir()
	legacyPassword := writePrivate(t, filepath.Join(dir, "legacy-password"), "shared-password\n")
	legacyToken := writePrivate(t, filepath.Join(dir, "legacy-token"), "synthetic-token\n")
	boundToken := writePrivate(t, filepath.Join(dir, "bound-token"), "owngit-server: "+hostile.server.URL+"\nsynthetic-token\n")
	clone := newClone(t, hostile.server.URL+"/git/project.git")
	for _, test := range []struct {
		name    string
		options mcpOptions
		code    string
	}{
		{"plain HTTP without the acknowledgement", mcpOptions{workdir: clone}, "insecure_http_confirmation_required"},
		{"inferred server with a legacy password", mcpOptions{workdir: clone, acceptInsecureHTTP: true, passwordFile: legacyPassword}, "credential_origin_required"},
		{"inferred server with a legacy token", mcpOptions{workdir: clone, acceptInsecureHTTP: true, credentialFile: legacyToken}, "credential_origin_required"},
		{"explicit other server with a bound token", mcpOptions{workdir: clone, acceptInsecureHTTP: true, server: "http://127.0.0.1:7839", repository: "project", credentialFile: boundToken}, "credential_origin_mismatch"},
		{"result limit too small", mcpOptions{workdir: clone, acceptInsecureHTTP: true, resultLimit: 100}, "invalid_arguments"},
		{"missing working directory", mcpOptions{workdir: filepath.Join(dir, "missing"), acceptInsecureHTTP: true}, "invalid_arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.options.resultLimit == 0 {
				test.options.resultLimit = defaultMCPResultLimit
			}
			if _, err := newMCPServer(context.Background(), test.options); commandErrorCode(err) != test.code {
				t.Fatalf("err=%v, want %s", err, test.code)
			}
		})
	}
	if hostile.requests.Load() != 0 {
		t.Fatalf("launch sent %d requests", hostile.requests.Load())
	}
	// A bound token for the inferred server fixes server and repository.
	var server *mcpServer
	_, err := captureStderr(func() error {
		var err error
		server, err = newMCPServer(context.Background(), mcpOptions{workdir: clone, acceptInsecureHTTP: true, credentialFile: boundToken, resultLimit: defaultMCPResultLimit})
		return err
	})
	noErr(t, err)
	if server.general.repository != "project" || server.checks == nil || !strings.Contains(server.instructions, "origin remote") {
		t.Fatalf("inferred launch: repository=%q instructions=%q", server.general.repository, server.instructions)
	}
}

// The check read tools return what owngit check status, cycle list, and log
// print.
func TestMCPCheckReadTools(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)
	run := cliOutput(t, checkCommand, append([]string{"run", "--task", taskID, "--workdir", work, "--check", "hello=echo hello"}, remoteFlags...)...)
	var output checkRunOutput
	noErr(t, json.Unmarshal([]byte(run), &output))
	session := startMCPSession(t, mcpOptions{server: remoteFlags[1], repository: "project", credentialFile: remoteFlags[6], acceptInsecureHTTP: true, workdir: work})
	names, _ := session.toolNames()
	if got := strings.Join(names, " "); !strings.Contains(got, "check_config_show check_cycle_list") || !strings.Contains(got, "check_log") ||
		!strings.Contains(got, "check_status") || !strings.Contains(got, "check_task_list") {
		t.Fatalf("tools with a helper credential: %s", got)
	}
	for _, test := range []struct {
		tool      string
		arguments map[string]any
		want      string
	}{
		{"check_task_list", nil, cliOutput(t, checkCommand, append([]string{"task", "list"}, remoteFlags...)...)},
		{"check_config_show", map[string]any{}, cliOutput(t, checkCommand, append([]string{"config", "show"}, remoteFlags...)...)},
		{"check_status", map[string]any{"task": taskID}, cliOutput(t, checkCommand, append([]string{"status", "--task", taskID}, remoteFlags...)...)},
		{"check_cycle_list", map[string]any{"task": taskID}, cliOutput(t, checkCommand, append([]string{"cycle", "list", "--task", taskID}, remoteFlags...)...)},
		{"check_log", map[string]any{"attempt": output.AttemptID}, cliOutput(t, checkCommand, append([]string{"log", "--attempt", output.AttemptID}, remoteFlags...)...)},
	} {
		text, isError := session.call(test.tool, test.arguments)
		if isError || text != test.want {
			t.Errorf("%s: isError=%v\n got %s\nwant %s", test.tool, isError, text, test.want)
		}
	}
	if code := session.callError("check_status", map[string]any{"task": taskID, "repository": "other"}); code != "invalid_arguments" {
		t.Fatalf("check tool with a repository argument: %q", code)
	}
}

// slowOwnGit answers requests only when their context ends, and counts the
// requests whose context ended.
type slowOwnGit struct {
	server   *httptest.Server
	started  chan struct{}
	finished atomic.Int32
}

func newSlowOwnGit(t *testing.T) *slowOwnGit {
	t.Helper()
	slow := &slowOwnGit{started: make(chan struct{}, 16)}
	slow.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		slow.started <- struct{}{}
		<-request.Context().Done()
		slow.finished.Add(1)
	}))
	t.Cleanup(slow.server.Close)
	return slow
}

func (slow *slowOwnGit) awaitRequest(t *testing.T) {
	t.Helper()
	select {
	case <-slow.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the request did not arrive")
	}
}

func (slow *slowOwnGit) awaitFinished(t *testing.T, count int32) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for slow.finished.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d requests were stopped", slow.finished.Load(), count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A cancellation stops the call and suppresses its response; the end of the
// input stops every call and ends the session.
func TestMCPCancellationAndEndOfInputStopCalls(t *testing.T) {
	slow := newSlowOwnGit(t)
	session := startMCPSession(t, mcpOptions{server: slow.server.URL, repository: "project", acceptInsecureHTTP: true})
	session.send(`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"pull_request_list","arguments":{}}}`)
	slow.awaitRequest(t)
	session.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow","reason":"test"}}`)
	slow.awaitFinished(t, 1)
	if response := session.request("ping", nil); response.Error != nil {
		t.Fatalf("ping after cancellation: %+v", response.Error)
	}
	session.expectSilence()

	session.send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"pull_request_list","arguments":{}}}`)
	slow.awaitRequest(t)
	started := time.Now()
	noErr(t, session.input.Close())
	select {
	case err := <-session.done:
		noErr(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the session did not end after its input closed")
	}
	slow.awaitFinished(t, 2)
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("ending took %v", elapsed)
	}
	if line, ok := <-session.lines; ok {
		t.Fatalf("a response was written after the input ended: %q", line)
	}
	session.done <- nil // for the cleanup
}

// A result over the limit is cut to fit and says what was cut.
func TestFitResultCutsExplicitly(t *testing.T) {
	var items []string
	for index := 0; index < 400; index++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"pull request %d"}`, index, index))
	}
	list := `{"ok":true,"pull_requests":[` + strings.Join(items, ",") + `]}`
	fitted := fitResult([]byte(list), 4096)
	var cutList struct {
		OK        bool             `json:"ok"`
		Items     []map[string]any `json:"pull_requests"`
		Truncated struct {
			Bytes int      `json:"bytes"`
			Limit int      `json:"limit"`
			Cut   []string `json:"cut"`
		} `json:"result_truncated"`
	}
	noErr(t, json.Unmarshal(fitted, &cutList))
	if len(fitted) > 4096 || !cutList.OK || len(cutList.Items) == 0 || len(cutList.Items) >= 400 || cutList.Items[0]["title"] != "pull request 0" ||
		cutList.Truncated.Bytes != len(list) || cutList.Truncated.Limit != 4096 || strings.Join(cutList.Truncated.Cut, ",") != "pull_requests" {
		t.Fatalf("cut list (%d bytes): %s", len(fitted), fitted)
	}

	logText := strings.Repeat("line of output ✓\n", 2000)
	encodedLog, err := json.Marshal(map[string]any{"ok": true, "log_id": "abc", "content": logText})
	noErr(t, err)
	fitted = fitResult(encodedLog, 8192)
	var cutLog struct {
		LogID     string `json:"log_id"`
		Content   string `json:"content"`
		Truncated struct {
			Cut []string `json:"cut"`
		} `json:"result_truncated"`
	}
	noErr(t, json.Unmarshal(fitted, &cutLog))
	if len(fitted) > 8192 || cutLog.LogID != "abc" || !strings.HasPrefix(logText, cutLog.Content) || len(cutLog.Content) < 4096 ||
		strings.Join(cutLog.Truncated.Cut, ",") != "content" {
		t.Fatalf("cut log (%d bytes): %.200s", len(fitted), fitted)
	}
	if small := []byte(`{"ok":true}`); string(fitResult(small, 4096)) != string(small) {
		t.Fatal("a result that fits was changed")
	}
}

// A diff over the result limit keeps whole files and reports the cut in the
// diff's own fields.
func TestMCPDiffKeepsWholeFilesUnderTheResultLimit(t *testing.T) {
	serverURL, number, work := startPullRequestFixture(t)
	for index := 0; index < 8; index++ {
		commitFile(t, work, fmt.Sprintf("file%d.txt", index), strings.Repeat(fmt.Sprintf("line %d\n", index), 120))
	}
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	session := startMCPSession(t, mcpOptions{server: serverURL, repository: "project", acceptInsecureHTTP: true, resultLimit: minimumMCPResultLimit})
	text, isError := session.call("pull_request_diff", map[string]any{"number": json.Number(number)})
	var diff pullrequest.Diff
	noErr(t, json.Unmarshal([]byte(text), &diff))
	sections := strings.Count(diff.Patch, "diff --git ")
	if isError || len(text) > minimumMCPResultLimit || !diff.Truncated || diff.Reason != "response_limit" || len(diff.Files) != 9 ||
		sections == 0 || sections >= 9 || !strings.HasSuffix(diff.Patch, "\n") || strings.Contains(text, "result_truncated") {
		t.Fatalf("cut diff (%d bytes, %d sections): %+v", len(text), sections, diff)
	}
}

// startMCPCheckFixture serves an open-access repository "project" with a
// helper credential and a task, and returns the helper flags, the task ID,
// and a work tree whose origin is the repository and whose main branch is
// pushed.
func startMCPCheckFixture(t *testing.T) (remoteFlags []string, taskID, work string) {
	t.Helper()
	remoteFlags, taskID, work = startCheckCLIServer(t)
	runPRGit(t, work, "remote", "add", "origin", remoteFlags[1]+"/git/project.git")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	return remoteFlags, taskID, work
}

func decodeToolJSON(t *testing.T, text string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), target); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
}

// The write tools change the same records as the matching commands, and
// check_run runs only the committed checks.
func TestMCPWriteToolsAndCheckRun(t *testing.T) {
	remoteFlags, taskID, work := startMCPCheckFixture(t)
	serverURL, credentialFile := remoteFlags[1], remoteFlags[6]
	general := []string{"--server", serverURL, "--accept-insecure-http", "--repository", "project"}
	runPRGit(t, work, "checkout", "-q", "-b", "feature")
	commitFile(t, work, "feature.txt", "feature\n")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	writeCommittedChecks(t, work, `{"version":1,"events":{"push":{}},"checks":[{"name":"pass","command":"exit 0"}]}`)
	session := startMCPSession(t, mcpOptions{server: serverURL, repository: "project", credentialFile: credentialFile, acceptInsecureHTTP: true, workdir: work})
	names, _ := session.toolNames()
	if got := strings.Join(names, " "); got != "check_config_show check_cycle_list check_cycle_reserve check_log check_run check_status check_task_create check_task_list "+
		"pull_request_close pull_request_create pull_request_diff pull_request_list pull_request_merge pull_request_reopen pull_request_review pull_request_show repository_list repository_show" {
		t.Fatalf("tools: %s", got)
	}

	var created pullrequest.SuccessEnvelope
	text, isError := session.call("pull_request_create", map[string]any{"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "request"})
	decodeToolJSON(t, text, &created)
	if isError || created.PullRequest == nil || created.PullRequest.State != "open" {
		t.Fatalf("create: %s", text)
	}
	pr := created.PullRequest
	number := json.Number(strconv.FormatInt(pr.Number, 10))
	if code := session.callError("pull_request_create", map[string]any{"title": "Again", "source_branch": "feature", "target_branch": "main"}); code == "" {
		t.Fatal("a second open pull request for the pair was not refused")
	}
	pair := map[string]any{"number": number, "source_oid": pr.Source.OID, "target_oid": pr.Target.OID}
	review := map[string]any{"decision": "approved", "reviewer": "mcp-test"}
	for key, value := range pair {
		review[key] = value
	}
	var reviewed pullrequest.SuccessEnvelope
	text, isError = session.call("pull_request_review", review)
	decodeToolJSON(t, text, &reviewed)
	if isError || reviewed.PullRequest == nil || reviewed.PullRequest.Review.Status != "approved" || reviewed.PullRequest.Review.ReviewerLabel != "mcp-test" {
		t.Fatalf("review: %s", text)
	}
	review["target_oid"] = pr.Source.OID
	if code := session.callError("pull_request_review", review); code == "" {
		t.Fatal("a review for other commits was accepted")
	}
	for _, step := range []struct{ tool, state string }{{"pull_request_close", "closed"}, {"pull_request_reopen", "open"}} {
		var result pullrequest.SuccessEnvelope
		text, isError := session.call(step.tool, map[string]any{"number": number})
		decodeToolJSON(t, text, &result)
		if isError || result.PullRequest == nil || result.PullRequest.State != step.state {
			t.Fatalf("%s: %s", step.tool, text)
		}
	}

	// check_run takes only a task and a cycle, never a command or path.
	for _, arguments := range []map[string]any{
		{"task": taskID, "check": "evil=touch evil"},
		{"task": taskID, "workdir": t.TempDir()},
		{"task": taskID, "timeout": "1h"},
		{"task": taskID, "cycle": "not-a-cycle"},
		{},
	} {
		if code := session.callError("check_run", arguments); code != "invalid_arguments" {
			t.Errorf("check_run %v: code %q", arguments, code)
		}
	}
	var task struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	text, isError = session.call("check_task_create", map[string]any{"title": "MCP work"})
	decodeToolJSON(t, text, &task)
	if isError || task.Task.ID == "" {
		t.Fatalf("task: %s", text)
	}
	var run checkRunOutput
	text, isError = session.call("check_run", map[string]any{"task": task.Task.ID})
	decodeToolJSON(t, text, &run)
	if isError || !run.OK || !run.Uploaded || run.Attempt == nil || run.Attempt.Status != "passed" || run.Attempt.WorktreeState != "clean" ||
		run.Attempt.RevisionOID != prGitOutput(t, work, "rev-parse", "HEAD") || len(run.Results) != 1 || run.Results[0].Name != "pass" {
		t.Fatalf("check_run: %s", text)
	}
	var cycle struct {
		Cycle struct {
			ID string `json:"id"`
		} `json:"cycle"`
	}
	text, isError = session.call("check_cycle_reserve", map[string]any{"task": task.Task.ID})
	decodeToolJSON(t, text, &cycle)
	if isError || cycle.Cycle.ID == "" {
		t.Fatalf("cycle: %s", text)
	}
	text, isError = session.call("check_run", map[string]any{"task": task.Task.ID, "cycle": cycle.Cycle.ID})
	decodeToolJSON(t, text, &run)
	if isError || run.CycleID != cycle.Cycle.ID || run.CorrectionCyclesRemaining != 2 {
		t.Fatalf("check_run with a cycle: %s", text)
	}
	// The status tool and the command line agree on the recorded evidence.
	status, _ := session.call("check_status", map[string]any{"task": task.Task.ID})
	if want := cliOutput(t, checkCommand, append([]string{"status", "--task", task.Task.ID}, remoteFlags...)...); status != want {
		t.Fatalf("check_status %s, want %s", status, want)
	}

	var merged pullrequest.SuccessEnvelope
	pair["source_oid"] = prGitOutput(t, work, "rev-parse", "feature")
	text, isError = session.call("pull_request_merge", pair)
	decodeToolJSON(t, text, &merged)
	if !isError {
		t.Fatalf("a merge of commits that are not the branch heads succeeded: %s", text)
	}
	pair["source_oid"] = pr.Source.OID
	text, isError = session.call("pull_request_merge", pair)
	decodeToolJSON(t, text, &merged)
	if isError || merged.PullRequest == nil || merged.PullRequest.State != "merged" {
		t.Fatalf("merge: %s", text)
	}
	if shown := runPRCommandJSON(t, append([]string{"show", "--number", string(number)}, general...)); shown.PullRequest.State != "merged" {
		t.Fatalf("the command line shows state %q after the merge", shown.PullRequest.State)
	}
}

// With --no-run-check the check tools stay, check_run is not listed, and a
// call to it is refused before anything runs.
func TestMCPNoRunCheckRemovesCheckRun(t *testing.T) {
	remoteFlags, taskID, work := startMCPCheckFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	writeCommittedChecks(t, work, `{"version":1,"events":{"push":{}},"checks":[{"name":"marker","command":"echo ran > `+filepath.ToSlash(marker)+`"}]}`)
	session := startMCPSession(t, mcpOptions{server: remoteFlags[1], repository: "project", credentialFile: remoteFlags[6], acceptInsecureHTTP: true, workdir: work, noRunCheck: true})
	names, _ := session.toolNames()
	if got := strings.Join(names, " "); strings.Contains(got, "check_run") || !strings.Contains(got, "check_status") {
		t.Fatalf("tools with --no-run-check: %s", got)
	}
	response := session.request("tools/call", map[string]any{"name": "check_run", "arguments": map[string]any{"task": taskID}})
	if response.Error == nil || response.Error.Code != rpcInvalidParams || !strings.Contains(response.Error.Message, "--no-run-check") {
		t.Fatalf("check_run with --no-run-check: %+v", response.Error)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a check ran with --no-run-check")
	}
}

// buildOwngit builds this command into a temporary directory.
func buildOwngit(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "owngit")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

// startMCPBinary runs "owngit mcp" as a separate process in dir.
func startMCPBinary(t *testing.T, binary, dir string, arguments ...string) (*mcpSession, *exec.Cmd, *strings.Builder) {
	t.Helper()
	command := exec.Command(binary, append([]string{"mcp"}, arguments...)...)
	command.Dir = dir
	var stderr strings.Builder
	command.Stderr = &stderr
	input, err := command.StdinPipe()
	noErr(t, err)
	// Wait returns after the output was copied, so no message is lost.
	outputReader, outputWriter := io.Pipe()
	command.Stdout = outputWriter
	noErr(t, command.Start())
	done := make(chan error, 1)
	go func() {
		err := command.Wait()
		_ = outputWriter.Close()
		done <- err
	}()
	return newMCPSession(t, input, outputReader, done), command, &stderr
}

// The built binary serves a whole session over its standard input and
// output: the server and repository come from the launch directory's
// origin, and a read, a write, and a check run work.
func TestMCPBinaryRoundTrip(t *testing.T) {
	binary := buildOwngit(t)
	remoteFlags, _, work := startMCPCheckFixture(t)
	serverURL := remoteFlags[1]
	token, err := os.ReadFile(remoteFlags[6])
	noErr(t, err)
	bound := writePrivate(t, filepath.Join(t.TempDir(), "bound-token"), "owngit-server: "+serverURL+"\n"+string(token))
	runPRGit(t, work, "checkout", "-q", "-b", "feature")
	writeCommittedChecks(t, work, `{"version":1,"events":{"push":{}},"checks":[{"name":"pass","command":"exit 0"}]}`)
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")

	session, command, stderr := startMCPBinary(t, binary, work, "--accept-insecure-http", "--credential-file", bound)
	response := session.request("initialize", map[string]any{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if !strings.Contains(string(response.Result), `"protocolVersion":"`+mcpProtocolVersion+`"`) {
		t.Fatalf("initialize: %s", response.Result)
	}
	session.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if names, _ := session.toolNames(); len(names) != 18 {
		t.Fatalf("tools: %v", names)
	}
	if text, isError := session.call("pull_request_list", nil); isError || text != `{"ok":true,"pull_requests":[]}` {
		t.Fatalf("pull_request_list: %s", text)
	}
	text, isError := session.call("pull_request_create", map[string]any{"title": "Binary", "source_branch": "feature", "target_branch": "main"})
	if isError || !strings.Contains(text, `"title":"Binary"`) {
		t.Fatalf("pull_request_create: %s", text)
	}
	var task struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	text, _ = session.call("check_task_create", map[string]any{"title": "binary"})
	decodeToolJSON(t, text, &task)
	var run checkRunOutput
	text, isError = session.call("check_run", map[string]any{"task": task.Task.ID})
	decodeToolJSON(t, text, &run)
	if isError || !run.Uploaded || run.Attempt == nil || run.Attempt.Status != "passed" {
		t.Fatalf("check_run: %s", text)
	}
	noErr(t, session.input.Close())
	select {
	case err := <-session.done:
		if err != nil {
			t.Fatalf("owngit mcp exited with %v; stderr:\n%s", err, stderr.String())
		}
		session.done <- nil // for the cleanup
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("owngit mcp did not exit after its input closed")
	}
	if !strings.Contains(stderr.String(), "from the origin remote") {
		t.Fatalf("stderr did not name the inference: %q", stderr.String())
	}
}

// A launch failure is reported on standard error and exits 1, leaving
// standard output empty for the protocol.
func TestMCPBinaryLaunchFailureUsesStandardError(t *testing.T) {
	binary := buildOwngit(t)
	command := exec.Command(binary, "mcp", "--server", "http://127.0.0.1:7839")
	command.Dir = t.TempDir()
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "insecure_http_confirmation_required") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

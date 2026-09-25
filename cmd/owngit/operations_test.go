package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// An operation returns the server's JSON bytes unchanged, and the command
// prints exactly those bytes with one final newline.
func TestOperationResultIsPrintedByteForByte(t *testing.T) {
	const body = `{"ok":true,"pull_requests":[{"number":7,"title":"a  b"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	noErr(t, err)
	content, err := listPullRequests(context.Background(), connection{server: parsed, repository: "project", credential: credential{kind: credentialNone}})
	noErr(t, err)
	if string(content) != body {
		t.Fatalf("operation returned %q, want %q", content, body)
	}
	output, err := captureStdout(func() error {
		return prCommand([]string{"list", "--server", server.URL, "--accept-insecure-http", "--repository", "project"})
	})
	noErr(t, err)
	if output != body+"\n" {
		t.Fatalf("command printed %q, want the server bytes and a newline", output)
	}
}

// A cancelled context stops the retries of an idempotent POST at once instead
// of waiting out the 200 and 400 millisecond pauses between attempts.
func TestPostWithRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		cancel()
		// A non-JSON answer is an invalid response, which is retried.
		_, _ = writer.Write([]byte("not json"))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	noErr(t, err)
	target := connection{server: parsed, repository: "project", credential: credential{kind: credentialHelperToken, secret: "synthetic-token"}}
	started := time.Now()
	if _, err := postWithRetry(ctx, target.client(), target.taskPath("task")+"/cycles", struct{}{}); err == nil {
		t.Fatal("a cancelled retry reported success")
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("cancelled retries took %v, want them to stop without the pauses", elapsed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("the server received %d requests after the context was cancelled, want 1", got)
	}
}

// The check run steps are separable: a cancelled execution still observes
// the worktree and is recorded by the completion step with its own context.
func TestCheckAttemptStepsRecordACancelledExecution(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)
	flags := newCheckFlagSet("check run")
	remote := addCheckRemoteFlags(flags)
	noErr(t, parseCheckFlags(flags, remoteFlags))
	target, err := remote.connection(".")
	noErr(t, err)
	attempt, err := prepareCheckAttempt(context.Background(), &target, checkRunRequest{
		TaskID: taskID, Workdir: work, Timeout: time.Minute, OutputLimit: 1024,
		Checks: []checkexec.Definition{{Name: "marker", Command: "echo ran > ran-marker"}},
	})
	noErr(t, err)
	noErr(t, attempt.register(context.Background()))
	if !attempt.output.Registered {
		t.Fatalf("registration was not confirmed: %q", attempt.output.UploadError)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	attempt.execute(cancelled)
	output := attempt.complete(context.Background())
	if !output.OK || !output.Uploaded || output.Attempt == nil {
		t.Fatalf("cancelled run was not recorded: %+v", output)
	}
	if output.Attempt.Status != checkexec.StatusCancelled || output.Attempt.WorktreeState != state.WorktreeClean {
		t.Fatalf("recorded status=%q worktree=%q", output.Attempt.Status, output.Attempt.WorktreeState)
	}
	if markerExists(t, work, "ran-marker") {
		t.Fatal("a check ran after the context was cancelled")
	}
	var exit *checkExit
	if err := attempt.outcome(); !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("outcome=%v, want exit 130", err)
	}
}

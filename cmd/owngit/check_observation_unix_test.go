//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// A configured clean filter that never answers must not hold a check run: the
// run must end within the bound of its worktree observation, before and after
// the checks, the check's own result must survive, and a state nobody could
// read must be recorded as unknown with the reason. A stop must end the
// observation at once and stop the filter with it, and an interrupt before the
// checks must leave neither the filter nor a registered attempt behind, while an
// interrupt after them keeps the check's own result and still exits as that
// signal stops a process.
//
// The fixture commits one tracked file whose clean filter passes content
// through until an arming file exists, and then records its process id and
// waits on a pipe nobody opens, so no sleep decides when the wait ends.
//
// A Windows checkout cannot run this fixture: the filter is a POSIX shell
// script, and the process check uses the POSIX group model. Owned containment
// on Windows is a job object in the shared Git runner, which this package does
// not replace.
func TestBlockedCleanFilterCannotHoldTheWorktreeObservation(t *testing.T) {
	var binary string
	builtBinary := func() string {
		if binary == "" {
			binary = buildOwngit(t)
		}
		return binary
	}

	// The bound belongs to the observation: nobody stops this run, and the
	// result still arrives, with the check's own result and an unknown
	// worktree state that names the reason in the recorded log. The check
	// rewrites its tracked file with the same size, so the observation after
	// the checks has to run the filter and cannot finish.
	t.Run("the observation bound", func(t *testing.T) {
		setWorktreeObservationBound(t, 750*time.Millisecond)
		fixture := blockedCleanFilterFixture(t)
		request := checkRunRequest{
			TaskID: "local", Workdir: fixture.work, Timeout: 30 * time.Second, OutputLimit: checkexec.DefaultOutputLimit(),
			Checks: []checkexec.Definition{{Name: "edit", Command: "printf 'edit\\n' > tracked.txt && : > \"$OWN_FILTER_ARMING\""}},
		}
		type runResult struct {
			output checkRunOutput
			log    string
			err    error
		}
		done := make(chan runResult, 1)
		go func() {
			attempt, err := prepareCheckAttempt(context.Background(), nil, request)
			if err != nil {
				done <- runResult{err: err}
				return
			}
			attempt.execute(context.Background())
			done <- runResult{output: attempt.complete(context.Background()), log: attempt.log, err: attempt.outcome()}
		}()
		var result runResult
		select {
		case result = <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the run did not return: a clean filter held the worktree observation")
		}
		noErr(t, result.err)
		if result.output.Attempt == nil || result.output.Attempt.WorktreeState != state.WorktreeUnknown {
			t.Fatalf("run result=%+v, want an unknown worktree state", result.output)
		}
		if !strings.Contains(result.output.Attempt.Summary, "worktree state unknown") {
			t.Fatalf("summary %q does not state the unknown worktree", result.output.Attempt.Summary)
		}
		if len(result.output.Results) != 1 || result.output.Results[0].Status != checkexec.StatusPassed {
			t.Fatalf("the check's own result was lost: %+v", result.output.Results)
		}
		// The log is the recorded place for the reason, and it is what the
		// command line, the server record and an MCP caller show.
		for _, expected := range []string{"unknown", worktreeObservationBound.String()} {
			if !strings.Contains(result.log, expected) {
				t.Fatalf("recorded log %q does not tell why the state is unknown", result.log)
			}
		}
		waitForFilterGone(t, waitForFilterStart(t, fixture.pidFile))
	})

	// The observation before the checks is bounded too: a filter that does not
	// answer cannot keep a run from starting, the check still runs, and the
	// state nobody could read is recorded as unknown with its reason.
	t.Run("the observation before the checks", func(t *testing.T) {
		setWorktreeObservationBound(t, 750*time.Millisecond)
		fixture := blockedCleanFilterFixture(t)
		armBlockedFilter(t, fixture)
		request := checkRunRequest{
			TaskID: "local", Workdir: fixture.work, Timeout: 30 * time.Second, OutputLimit: checkexec.DefaultOutputLimit(),
			// The check puts the tracked file back and disarms the filter, so only
			// the observation before it pays the bound.
			Checks: []checkexec.Definition{{Name: "restore", Command: "printf 'base\\n' > tracked.txt && rm -f \"$OWN_FILTER_ARMING\""}},
		}
		started := time.Now()
		attempt, err := prepareCheckAttempt(context.Background(), nil, request)
		noErr(t, err)
		attempt.execute(context.Background())
		output := attempt.complete(context.Background())
		t.Logf("the run took %v", time.Since(started))
		noErr(t, attempt.outcome())
		if output.Attempt == nil || output.Attempt.WorktreeState != state.WorktreeUnknown {
			t.Fatalf("run result=%+v, want an unknown worktree state", output)
		}
		if len(output.Results) != 1 || output.Results[0].Status != checkexec.StatusPassed {
			t.Fatalf("the check's own result was lost: %+v", output.Results)
		}
		content, err := os.ReadFile(filepath.Join(fixture.work, "tracked.txt"))
		if err != nil || string(content) != "base\n" {
			t.Fatalf("the check did not run: %q err=%v", content, err)
		}
		for _, expected := range []string{"unknown", worktreeObservationBound.String()} {
			if !strings.Contains(attempt.log, expected) {
				t.Fatalf("recorded log %q does not tell why the state is unknown", attempt.log)
			}
		}
	})

	// A stopped run ends the observation at once and stops the filter with it.
	// The bound is a minute here, so only the stop can release the run.
	t.Run("a stopped run", func(t *testing.T) {
		setWorktreeObservationBound(t, time.Minute)
		fixture := blockedCleanFilterFixture(t)
		armBlockedFilter(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan string, 1)
		go func() {
			got, _ := confirmWorktree(ctx, fixture.work, fixture.revision, state.WorktreeClean)
			done <- got
		}()
		// The filter is waiting, so the observation is inside Git.
		pid := waitForFilterStart(t, fixture.pidFile)
		stopped := time.Now()
		cancel()
		select {
		case got := <-done:
			if got != state.WorktreeUnknown {
				t.Fatalf("worktree state=%q after the run was stopped, want unknown", got)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the observation did not return after the run was stopped")
		}
		if elapsed := time.Since(stopped); elapsed > 10*time.Second {
			t.Fatalf("the observation took %v to end after the run was stopped", elapsed)
		}
		waitForFilterGone(t, pid)
	})

	// A terminal interrupt during the observation before the checks stops that
	// observation and the filter with it, registers nothing, and ends the
	// command as a process stopped by that signal does. Ctrl-C sends SIGINT and
	// a closed terminal sends SIGHUP, and both reach only the command's own
	// group, so the command must stop the Git it owns in a group of its own. A
	// command that starts with the signal ignored, as nohup starts one, keeps
	// the owner's choice instead.
	t.Run("an interrupt before the checks", func(t *testing.T) {
		if testing.Short() {
			t.Skip("builds the program and sends it a terminal interrupt")
		}
		for _, interrupt := range []struct {
			name    string
			signal  syscall.Signal
			code    int // exit status; zero means the signal must not stop the run
			started func(*testing.T)
		}{
			{name: "SIGINT", signal: syscall.SIGINT, code: 130},
			{name: "SIGHUP", signal: syscall.SIGHUP, code: 129, started: startWithSighupNotIgnored},
			{name: "SIGHUP ignored at start", signal: syscall.SIGHUP, started: startWithSighupIgnored},
		} {
			t.Run(interrupt.name, func(t *testing.T) {
				if interrupt.started != nil {
					interrupt.started(t)
				}
				run := startBlockedRun(t, builtBinary(), true)
				noErr(t, syscall.Kill(-run.command.Process.Pid, interrupt.signal))
				if interrupt.code == 0 {
					// The signal changed nothing: the command and its observation
					// are still waiting a second later.
					done := make(chan error, 1)
					go func() { done <- run.command.Wait() }()
					select {
					case err := <-done:
						t.Fatalf("%s stopped a run that was started with it ignored: %v", interrupt.name, err)
					case <-time.After(time.Second):
					}
					if err := syscall.Kill(run.filter, 0); err != nil {
						t.Fatalf("the observation ended although %s is ignored: %v", interrupt.name, err)
					}
					assertCheckDidNotRun(t, run.work)
					// Teardown stops the run the hard way: SIGKILL is the signal
					// nothing catches.
					noErr(t, syscall.Kill(-run.command.Process.Pid, syscall.SIGKILL))
					<-done
					return
				}
				waitForExit(t, run.command, "the interrupted run")
				// The filter must go with the interrupt, and the command must end
				// as a process stopped by that signal does, without a result of
				// its own.
				waitForFilterGone(t, run.filter)
				if code := run.command.ProcessState.ExitCode(); code != interrupt.code {
					t.Fatalf("the run stopped by %s exited %d, want %d; stdout=%q stderr=%q", interrupt.name, code, interrupt.code, run.stdout.String(), run.stderr.String())
				}
				if run.stdout.String() != "" {
					t.Fatalf("the interrupted run printed a result: %q", run.stdout.String())
				}
				assertCheckDidNotRun(t, run.work)
				var status checkapi.TaskResponse
				noErr(t, json.Unmarshal([]byte(cliOutput(t, checkCommand, append([]string{"status", "--task", run.taskID}, run.remoteFlags...)...)), &status))
				if status.Attempt != nil || (status.Task != nil && status.Task.PendingAttemptID != "") {
					t.Fatalf("the interrupted run registered an attempt: %+v", status)
				}
			})
		}
	})

	// A stop signal during the observation after the checks ends that
	// observation and its filter too, and the run still prints what it did: the
	// check passed, the worktree state is unknown with the fixed reason, and the
	// exit names the signal. A passed check must not report a run the owner
	// stopped as a success.
	t.Run("an interrupt after the checks", func(t *testing.T) {
		if testing.Short() {
			t.Skip("builds the program and sends it a terminal interrupt")
		}
		for _, interrupt := range []struct {
			name    string
			signal  syscall.Signal
			code    int
			started func(*testing.T)
		}{
			{name: "SIGINT", signal: syscall.SIGINT, code: 130},
			{name: "SIGHUP", signal: syscall.SIGHUP, code: 129, started: startWithSighupNotIgnored},
			{name: "SIGTERM", signal: syscall.SIGTERM, code: 143},
		} {
			t.Run(interrupt.name, func(t *testing.T) {
				if interrupt.started != nil {
					interrupt.started(t)
				}
				run := startBlockedRun(t, builtBinary(), false)
				noErr(t, syscall.Kill(-run.command.Process.Pid, interrupt.signal))
				waitForExit(t, run.command, "the interrupted run")
				waitForFilterGone(t, run.filter)
				var output checkRunOutput
				noErr(t, json.Unmarshal([]byte(run.stdout.String()), &output))
				if output.Attempt == nil || output.Attempt.WorktreeState != state.WorktreeUnknown {
					t.Fatalf("run result=%+v, want an unknown worktree state", output)
				}
				if len(output.Results) != 1 || output.Results[0].Status != checkexec.StatusPassed {
					t.Fatalf("the check's own result was lost: %+v", output.Results)
				}
				if want := "[OwnGit could not read the worktree: the run was stopped while the worktree was read. The worktree state is unknown.]"; output.WorktreeNote != want {
					t.Fatalf("result note=%q, want the fixed text %q", output.WorktreeNote, want)
				}
				if code := run.command.ProcessState.ExitCode(); code != interrupt.code {
					t.Fatalf("the run stopped by %s exited %d, want %d; stderr=%q", interrupt.name, code, interrupt.code, run.stderr.String())
				}
			})
		}
	})
}

// blockedRun is the real command started against a checkout whose clean filter
// blocks the worktree observation, with what the test needs to signal the
// command and to read its result.
type blockedRun struct {
	command     *exec.Cmd
	stdout      *strings.Builder
	stderr      *strings.Builder
	remoteFlags []string
	taskID      string
	work        string
	filter      int
}

// startBlockedRun starts the command as its own process group, as a shell runs
// it, against a fresh server task and a checkout whose configured clean filter
// blocks the worktree observation: the one before the checks when armedBefore is
// true, and the one after them, which the check itself arms, otherwise.
func startBlockedRun(t *testing.T, binary string, armedBefore bool) blockedRun {
	t.Helper()
	setWorktreeObservationBound(t, time.Minute)
	remoteFlags, taskID, work := startCheckCLIServer(t)
	fixture := installBlockedCleanFilter(t, work)
	check := "edit=printf 'edit\\n' > tracked.txt && : > \"$OWN_FILTER_ARMING\""
	if armedBefore {
		// The check puts the tracked file back and disarms the filter, so only
		// the observation before it pays for the blocked filter.
		armBlockedFilter(t, fixture)
		check = "restore=printf 'base\\n' > tracked.txt && rm -f \"$OWN_FILTER_ARMING\""
	}
	command := exec.Command(binary, append([]string{
		"check", "run", "--task", taskID, "--workdir", work, "--check", check,
	}, remoteFlags...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	command.Stdout, command.Stderr = stdout, stderr
	noErr(t, command.Start())
	t.Cleanup(func() {
		// A run that never returned must not leave its group behind.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	})
	run := blockedRun{command: command, stdout: stdout, stderr: stderr, remoteFlags: remoteFlags, taskID: taskID, work: work}
	run.filter = waitForFilterStart(t, fixture.pidFile)
	t.Cleanup(func() { stopFilter(run.filter) })
	return run
}

// assertCheckDidNotRun asserts the check left the tracked file as the armed
// fixture wrote it, which a run of the check would have replaced.
func assertCheckDidNotRun(t *testing.T, work string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(work, "tracked.txt"))
	if err != nil || string(content) != "edit\n" {
		t.Fatalf("the check ran although the run was stopped before it: %q err=%v", content, err)
	}
}

// The first stop signal stops the run and still lets the attempt be registered,
// so a stopped run is not left pending. The owner who insists with a second
// signal must not wait for the registration retries: the run ends at once, and
// its exit names the signal.
func TestSecondStopSignalEndsARegistrationWait(t *testing.T) {
	requests := make(chan string, 4)
	// The server holds the registration open until the test releases it: a
	// client that gives up does not cancel the request context of a handler
	// that never writes, so the server alone decides when the handler returns.
	var released sync.Once
	release := make(chan struct{})
	unblock := func() { released.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case requests <- request.URL.Path:
		default:
		}
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		unblock()
		server.Close()
	})
	remoteFlags, taskID, work := startCheckCLIServer(t)
	done := make(chan error, 1)
	go func() {
		_, err := captureStdout(func() error {
			return checkCommand(append([]string{
				"run", "--task", taskID, "--workdir", work, "--check", "pass=exit 0", "--server", server.URL,
			}, remoteFlags[2:]...))
		})
		done <- err
	}()
	select {
	case path := <-requests:
		if !strings.HasSuffix(path, "/attempts") {
			t.Fatalf("the run's first request was %q, not its registration", path)
		}
	case err := <-done:
		t.Fatalf("the run ended before it registered: %v", err)
	case <-time.After(30 * time.Second):
		unblock()
		<-done
		t.Fatal("the run did not reach its registration")
	}
	// The first signal stops the run. Its registration is still worth
	// completing, so the run keeps waiting for the answer.
	noErr(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case err := <-done:
		t.Fatalf("the first signal ended the run while it was registering: %v", err)
	case <-time.After(time.Second):
	}
	// The owner insists: the second signal ends the wait at once, and the exit
	// names the signal instead of the retries.
	noErr(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case err := <-done:
		var exit *checkExit
		if !errors.As(err, &exit) || exit.code != 130 {
			t.Fatalf("the run that a second signal stopped reported %v, want exit 130", err)
		}
	case <-time.After(10 * time.Second):
		unblock()
		<-done
		t.Fatal("the run kept waiting for its registration after a second signal")
	}
}

// A stop signal that arrives while the result is recorded must name the exit
// status as any other stop does. The checks passed and the server answered the
// completion, so the recorded result looks like a finished run; the owner
// stopped it all the same, so a process status alone must not read it as a
// success.
func TestStopSignalDuringTheCompletionNamesTheExitStatus(t *testing.T) {
	held := make(chan struct{}, 1)
	// The server answers the registration at once and holds the completion until
	// the test releases it, so the signal arrives while that request waits.
	var released sync.Once
	release := make(chan struct{})
	unblock := func() { released.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/complete") {
			select {
			case held <- struct{}{}:
			default:
			}
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}
		answer, err := json.Marshal(map[string]any{
			"ok":      true,
			"attempt": map[string]any{"id": path.Base(request.URL.Path), "status": checkexec.StatusPassed},
		})
		noErr(t, err)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(answer)
	}))
	t.Cleanup(func() {
		unblock()
		server.Close()
	})
	// The command records a signal on its own goroutine, and this test must know
	// that the record happened before it lets the completion answer, so it waits
	// for the record itself instead of for a duration. The signal goes to this
	// process, where the command under test runs. The hook is set before that run
	// starts and never cleared, so neither the run nor a later test can race with
	// it.
	recorded := make(chan os.Signal, 1)
	onStopSignalRecorded = func(arrived os.Signal) {
		select {
		case recorded <- arrived:
		default:
		}
	}
	remoteFlags, taskID, work := startCheckCLIServer(t)
	type finished struct {
		output string
		err    error
	}
	done := make(chan finished, 1)
	go func() {
		output, err := captureStdout(func() error {
			return checkCommand(append([]string{
				"run", "--task", taskID, "--workdir", work, "--check", "pass=exit 0", "--server", server.URL,
			}, remoteFlags[2:]...))
		})
		done <- finished{output: output, err: err}
	}()
	select {
	case <-held:
	case result := <-done:
		t.Fatalf("the run ended before it recorded its result: %v", result.err)
	case <-time.After(30 * time.Second):
		unblock()
		<-done
		t.Fatal("the run did not reach its completion")
	}
	// The command reads the signal where it decides the exit status, so the
	// completion must not answer before the record happened.
	noErr(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	select {
	case arrived := <-recorded:
		if arrived != os.Interrupt {
			t.Fatalf("the command recorded %v, want an interrupt", arrived)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not record the signal")
	}
	unblock()
	select {
	case result := <-done:
		var printed checkRunOutput
		noErr(t, json.Unmarshal([]byte(result.output), &printed))
		if !printed.Uploaded || printed.Attempt == nil {
			t.Fatalf("the completion was not recorded: %s", result.output)
		}
		var exit *checkExit
		if !errors.As(result.err, &exit) || exit.code != 130 {
			t.Fatalf("the run the owner stopped while it recorded its result reported %v, want exit 130", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end after its completion answered")
	}
}

// startWithSighupNotIgnored makes the command the test starts next see SIGHUP as
// a terminal starts it. A child inherits an ignored signal, and the session the
// test runs in ignores SIGHUP, but a Go handler is reset to the default when the
// child is executed, so notifying this process of the signal is what the command
// then inherits.
func startWithSighupNotIgnored(t *testing.T) {
	t.Helper()
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(sighup) })
}

// startWithSighupIgnored makes the command the test starts next see SIGHUP
// ignored, as nohup starts it, so the owner's choice is the state it starts in.
func startWithSighupIgnored(t *testing.T) {
	t.Helper()
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
}

// setWorktreeObservationBound lowers the one observation bound for a test and
// restores it, so no test waits the production value.
func setWorktreeObservationBound(t *testing.T, bound time.Duration) {
	t.Helper()
	previous := worktreeObservationBound
	worktreeObservationBound = bound
	t.Cleanup(func() { worktreeObservationBound = previous })
}

type filterFixture struct {
	work     string
	revision string
	arming   string
	pidFile  string
}

// blockedCleanFilterFixture returns an empty committed checkout with the
// blocked clean filter installed.
func blockedCleanFilterFixture(t *testing.T) filterFixture {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Check Test")
	runPRGit(t, work, "config", "user.email", "check-test@example.invalid")
	return installBlockedCleanFilter(t, work)
}

// installBlockedCleanFilter commits one tracked file whose clean filter passes
// content through until the arming file exists, and then records its process
// id and waits on a pipe nobody opens. The test owns both files, so nothing
// else decides when the filter runs or when it ends. The filter reads three
// paths from its environment, which the test sets for its own process, so a
// command the test starts as a child process reads the same fixture.
func installBlockedCleanFilter(t *testing.T, work string) filterFixture {
	t.Helper()
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	noErr(t, syscall.Mkfifo(blocked, 0o600))
	fixture := filterFixture{
		work:    work,
		arming:  filepath.Join(root, "arming"),
		pidFile: filepath.Join(root, "filter.pid"),
	}
	t.Setenv("OWN_FILTER_ARMING", fixture.arming)
	t.Setenv("OWN_FILTER_PID", fixture.pidFile)
	t.Setenv("OWN_FILTER_BLOCKED", blocked)
	filter := filepath.Join(root, "clean-filter")
	noErr(t, os.WriteFile(filter, []byte("#!/bin/sh\n"+
		"if [ ! -e \"$OWN_FILTER_ARMING\" ]; then exec cat; fi\n"+
		"printf '%s' \"$$\" > \"$OWN_FILTER_PID\"\n"+
		"read -r line < \"$OWN_FILTER_BLOCKED\"\n"+
		"exec cat\n"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("tracked.txt filter=probe\n"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("base\n"), 0o600))
	runPRGit(t, work, "config", "filter.probe.clean", filter)
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")
	fixture.revision = prGitOutput(t, work, "rev-parse", "HEAD")
	return fixture
}

// armBlockedFilter changes the tracked file with a same-size replacement and
// lets the clean filter block, as a check that edits the tree does.
func armBlockedFilter(t *testing.T, fixture filterFixture) {
	t.Helper()
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "tracked.txt"), []byte("edit\n"), 0o600))
	noErr(t, os.WriteFile(fixture.arming, []byte("armed\n"), 0o600))
}

// waitForFilterStart returns the process id of the blocked clean filter. The
// filter writes it before it waits, so its arrival also says the observation
// reached Git. The poll is a hang guard; the fixture, not the poll, decides
// when the filter is reached.
func waitForFilterStart(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(content))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was not written: the blocked clean filter did not start", pidFile)
	return 0
}

// waitForFilterGone waits until the filter process has exited, and stops it if
// the wait times out, so a failure leaves no task-owned process behind. The
// poll is a hang guard for a busy machine; the filter waits until the test
// stops it, so a filter left running still fails the wait.
func waitForFilterGone(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() { stopFilter(pid) })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the blocked clean filter %d still runs after the observation returned", pid)
}

// stopFilter kills the test's own clean filter if it is still alive, so a
// failed assertion leaves no fixture process running.
func stopFilter(pid int) {
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// waitForExit waits for a command the test started, so a failure leaves no
// process behind: on a timeout the group it owns is killed.
func waitForExit(t *testing.T, command *exec.Cmd, what string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		t.Fatalf("%s did not exit", what)
	}
}

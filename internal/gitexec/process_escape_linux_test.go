package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// runSessionFixture runs the stream fixture modes that start a descendant in a
// new session, so it leaves the process group.
func runSessionFixture(mode string) bool {
	switch mode {
	case "setsid-parent", "setsid-parent-exit":
		// Tell the test through the inherited pipe once the descendant runs, then
		// wait to be stopped or exit.
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), streamFixtureEnv+"=setsid-child")
		child.ExtraFiles = []*os.File{os.NewFile(3, "pipe")}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		out, err := child.StdoutPipe()
		if err != nil || child.Start() != nil {
			os.Exit(93)
		}
		_, _ = out.Read(make([]byte, 1))
		_, _ = os.NewFile(3, "pipe").Write([]byte("r"))
		if mode == "setsid-parent" {
			time.Sleep(30 * time.Second)
		}
	case "setsid-child":
		_, _ = os.Stdout.WriteString("r")
		time.Sleep(30 * time.Second)
	default:
		return false
	}
	return true
}

// An absent process list must not turn a finished run into an error:
// the run keeps what the process group gave it.
func TestMissingProcessListKeepsTheRunResult(t *testing.T) {
	previous := procDirectory
	procDirectory = t.TempDir() + "/missing"
	t.Cleanup(func() { procDirectory = previous })
	if err := RunOwned(context.Background(), exec.Command("true"), nil, 0); err != nil {
		t.Fatalf("RunOwned error=%v, want the run's own result", err)
	}
}

// An unrelated process with an empty environment, which started before the run,
// must not make a scan wait for it to become readable.
func TestEmptyEnvironmentOfAnUnrelatedProcessIsNotWaitedFor(t *testing.T) {
	unrelated := exec.Command("env", "-i", "sleep", "60")
	noErr(t, unrelated.Start())
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	// Wait until env has executed sleep, whose environment is empty.
	deadline := time.Now().Add(10 * time.Second)
	for !isSleep(unrelated.Process.Pid) || !emptyEnvironment(unrelated.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("env did not execute sleep with an empty environment within 10 s")
		}
		runtime.Gosched()
	}
	since := processStartTime(unrelated.Process.Pid) + 1
	_, unknown, err := markedProcesses("none", since)
	noErr(t, err)
	if unknown != 0 {
		t.Fatalf("unknown=%d, want an older unrelated process skipped", unknown)
	}
	if _, unknown, _ = markedProcesses("none", 0); unknown == 0 {
		t.Fatal("a process that could belong to the run was not counted")
	}
}

// A real child isolates RLIMIT_NOFILE from the package's other tests.
func TestMarkedCleanupReportsDescriptorExhaustion(t *testing.T) {
	const childMode = "OWNGIT_TEST_MARKED_CLEANUP_NOFILE"
	if os.Getenv(childMode) == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, streamTestExecutable(t), "-test.run=^"+t.Name()+"$", "-test.v")
		child.Env = append(os.Environ(), childMode+"=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("descriptor exhaustion child: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}

	const token = "descriptor-exhaustion"
	target := exec.Command("sleep", "60")
	target.Env = append(os.Environ(), ownedRunMark(token))
	noErr(t, target.Start())
	defer func() { _ = target.Process.Kill(); _ = target.Wait() }()

	var original syscall.Rlimit
	noErr(t, syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original))
	limited := original
	limited.Cur = 0
	noErr(t, syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limited))
	err := killMarkedProcesses(token, 0)
	noErr(t, syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original))

	var readErr *os.PathError
	if !errors.As(err, &readErr) || !errors.Is(err, syscall.EMFILE) || readErr.Path != procDirectory {
		t.Fatalf("cleanup error=%v, want the failed process listing with EMFILE", err)
	}
	fields := statFields(procDirectory + "/" + strconv.Itoa(target.Process.Pid))
	if len(fields) == 0 || fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		t.Fatalf("unconfirmed target is gone or not live: stat fields=%v", fields)
	}
	t.Logf("cleanup listing: %v; target state=%s (not a zombie)", err, fields[0])
}

func isSleep(pid int) bool {
	name, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return err == nil && string(name) == "sleep\n"
}

func emptyEnvironment(pid int) bool {
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return err == nil && len(content) == 0
}

// A descendant that keeps starting marked processes must not keep the cleanup
// running: it returns an error once the bound, counted from the start, passes.
func TestMarkedProcessCleanupEndsWhenNewProcessesKeepAppearing(t *testing.T) {
	previous := markedProcessBound
	markedProcessBound = 200 * time.Millisecond
	t.Cleanup(func() { markedProcessBound = previous })
	token := "producer"
	var children []*exec.Cmd
	previousList := listMarkedProcesses
	// Before every scan one more marked process exists, as when a descendant
	// keeps starting new ones.
	listMarkedProcesses = func(token string, since uint64) ([]int, int, error) {
		child := exec.Command("sleep", "60")
		child.Env = append(os.Environ(), ownedRunMark(token))
		if err := child.Start(); err != nil {
			t.Errorf("start marked process: %v", err)
			return nil, 0, err
		}
		children = append(children, child)
		return previousList(token, since)
	}
	t.Cleanup(func() {
		listMarkedProcesses = previousList
		for _, child := range children {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	done := make(chan error, 1)
	go func() { done <- killMarkedProcesses(token, 0) }()
	// The bound is 200 ms; this deadline only turns a regression into a quick
	// failure instead of the package timeout.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cleanup returned no error although new marked processes kept appearing")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cleanup did not return within 20 s although its bound is 200 ms")
	}
}

// A descendant that starts a new session leaves the process group, so ending an
// owned run must still end it, whether the run is stopped or the process exits
// on its own. The test holds the read end of a pipe that only the descendant
// keeps open, so EOF means the descendant is gone.
func TestOwnedRunEndsDescendantThatLeavesTheSession(t *testing.T) {
	for _, test := range []struct {
		mode    string
		stopped bool
	}{{"setsid-parent", true}, {"setsid-parent-exit", false}} {
		t.Run(test.mode, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			noErr(t, err)
			defer reader.Close()
			command := exec.Command(streamTestExecutable(t))
			command.Env = append(os.Environ(), streamFixtureEnv+"="+test.mode)
			command.ExtraFiles = []*os.File{writer}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunOwned(ctx, command, nil, 25*time.Millisecond) }()
			ready := make([]byte, 1)
			noErr(t, reader.SetReadDeadline(time.Now().Add(30*time.Second)))
			if _, err := reader.Read(ready); err != nil {
				t.Fatalf("descendant did not start: %v", err)
			}
			want := error(nil)
			if test.stopped {
				cancel()
				want = context.Canceled
			}
			if err := <-done; err != want {
				t.Fatalf("RunOwned error=%v, want %v", err, want)
			}
			// Closing the test's copy only after RunOwned returned leaves the
			// descendant as the one holder of the write end.
			noErr(t, writer.Close())
			noErr(t, reader.SetReadDeadline(time.Now().Add(10*time.Second)))
			if _, err := reader.Read(ready); os.IsTimeout(err) {
				t.Fatal("descendant in a new session survived the end of the owned run")
			}
		})
	}
}

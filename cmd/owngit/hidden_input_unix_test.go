//go:build darwin || linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const hiddenInputMarkerEnv = "OWNGIT_HIDDEN_INPUT_MARKER"

// TestHiddenInputHelperProcess is the child of the signal test. It records
// echo state in a marker file instead of changing a terminal.
func TestHiddenInputHelperProcess(t *testing.T) {
	marker := os.Getenv(hiddenInputMarkerEnv)
	if marker == "" {
		t.Skip("runs only as the child of TestHiddenInputRestoresEchoOnEveryExit")
	}
	disableEcho = func() (func() error, error) {
		if err := os.WriteFile(marker, []byte("off"), 0o600); err != nil {
			return nil, err
		}
		return func() error { return os.WriteFile(marker, []byte("restored"), 0o600) }, nil
	}
	_, err := readHiddenLine(bufio.NewReader(os.Stdin), "Password: ")
	fmt.Fprintln(os.Stderr, "readHiddenLine returned:", err)
	os.Exit(0)
}

func TestHiddenInputRestoresEchoOnEveryExit(t *testing.T) {
	for _, exit := range []struct {
		name   string
		signal syscall.Signal
	}{
		{"line", 0},
		{"SIGINT", syscall.SIGINT},
		{"SIGQUIT", syscall.SIGQUIT},
		{"SIGTERM", syscall.SIGTERM},
		{"SIGHUP", syscall.SIGHUP},
	} {
		t.Run(exit.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "echo")
			child := exec.Command(os.Args[0], "-test.run=^TestHiddenInputHelperProcess$")
			child.Env = append(os.Environ(), hiddenInputMarkerEnv+"="+marker)
			input, err := child.StdinPipe()
			noErr(t, err)
			noErr(t, child.Start())
			defer input.Close()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if content, _ := os.ReadFile(marker); string(content) == "off" {
					break
				}
				if time.Now().After(deadline) {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatal("the child never turned echo off")
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := 0
			if exit.signal == 0 {
				_, err = input.Write([]byte("synthetic-secret\n"))
				noErr(t, err)
			} else {
				noErr(t, child.Process.Signal(exit.signal))
				want = 128 + int(exit.signal)
			}
			err = child.Wait()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				noErr(t, err)
			}
			if code != want {
				t.Fatalf("exit code=%d, want %d", code, want)
			}
			if content, _ := os.ReadFile(marker); string(content) != "restored" {
				t.Fatalf("echo state after exit=%q, want restored", content)
			}
		})
	}
}

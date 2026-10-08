package checkexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunCommandLine(t *testing.T) {
	for _, test := range []struct {
		name         string
		raw          bool
		noExecutable bool
	}{
		{name: "empty preserves shell", raw: false},
		{name: "trusted command line", raw: true},
		{name: "explicit executable required", raw: true, noExecutable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, _ := HostEnvironment(t.TempDir())
			definition := Definition{Command: "echo COMMAND_LINE_OK"}
			want := StatusPassed
			if test.raw {
				definition.CommandLine = "unavailable on other platforms"
				definition.Executable = "sh"
				want = StatusError
				if runtime.GOOS == "windows" {
					directory := filepath.Join(t.TempDir(), "script & path")
					if err := os.Mkdir(directory, 0o700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(directory, "step.cmd")
					if err := os.WriteFile(path, []byte("@echo off\r\necho COMMAND_LINE_OK\r\nexit /b 0\r\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					definition.Executable = os.Getenv("ComSpec")
					definition.CommandLine = `"` + definition.Executable + `" /D /E:ON /V:OFF /S /C "CALL "` + path + `""`
					want = StatusPassed
				}
			}
			if test.noExecutable {
				definition.Executable = ""
				want = StatusError
			}
			results, cancelled := Run(context.Background(), []Definition{definition}, Options{Env: environment})
			if cancelled || results[0].Status != want || want == StatusPassed && !strings.Contains(results[0].Output, "COMMAND_LINE_OK") {
				t.Fatal(results, cancelled)
			}
			if want == StatusError && !strings.Contains(results[0].Output, "CommandLine requires") {
				t.Fatal(results)
			}
		})
	}
}

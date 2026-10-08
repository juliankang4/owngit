package checkexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/testfixture"
)

func TestRunEnvironment(t *testing.T) {
	fixture := testfixture.NewCheckEnvironment(t)
	root := t.TempDir()
	blocked := filepath.Join(root, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dump := "env"
	line := strings.Repeat("x", 1024)
	large := `i=0; while [ "$i" -lt 300 ]; do echo ` + line + `; i=$((i+1)); done; exit 3`
	if runtime.GOOS == "windows" {
		dump = "set"
		large = "(for /l %i in (1,1,300) do @echo " + line + ") & exit /b 3"
	}
	for _, test := range []struct {
		name, command, status string
		options               Options
	}{
		{"host allowlist", fixture.Command, StatusFailed, Options{TempRoot: root}},
		{"bounded failure note", large, StatusFailed, Options{TempRoot: root, OutputLimit: 1 << 20}},
		{"explicit adapter", dump, StatusPassed, Options{Env: os.Environ(), TempRoot: blocked}},
		{"temporary directory unavailable", "echo must-not-run", StatusError, Options{TempRoot: blocked}},
		{"process and temporary cleanup errors", `mkdir "$TMPDIR/locked" && printf retained > "$TMPDIR/locked/file" && chmod 000 "$TMPDIR/locked"`, StatusError, Options{TempRoot: root, Redact: []string{"synthetic-secret"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "process and temporary cleanup errors" {
				if runtime.GOOS == "windows" || os.Geteuid() == 0 {
					t.Skip("requires Unix directory permissions and a nonroot account")
				}
				originalTerminate := terminateOwnedProcess
				first := true
				terminateOwnedProcess = func(owner *gitexec.ProcessOwner, grace time.Duration) error {
					if first {
						first = false
						return errors.New("process cleanup failed: synthetic-secret")
					}
					return originalTerminate(owner, grace)
				}
				t.Cleanup(func() {
					terminateOwnedProcess = originalTerminate
					entries, _ := os.ReadDir(root)
					for _, entry := range entries {
						if entry.IsDir() {
							if err := os.Chmod(filepath.Join(root, entry.Name(), "locked"), 0o700); err != nil {
								t.Error(err)
							}
						}
					}
				})
			}
			results, cancelled := Run(context.Background(), []Definition{{Name: "environment", Command: test.command}}, test.options)
			if cancelled || len(results) != 1 || results[0].Status != test.status {
				t.Fatalf("results=%+v cancelled=%v", results, cancelled)
			}
			if code := results[0].ExitCode; test.status == StatusFailed && (code == nil || *code != 3) {
				t.Fatal("failed command did not return exit code 3")
			}
			if test.name == "process and temporary cleanup errors" {
				cleanup := results[0].CleanupError
				if !strings.HasPrefix(cleanup, "process cleanup failed: [redacted]\n") || !strings.Contains(cleanup, "remove check temporary directory:") || strings.Contains(cleanup, "synthetic-secret") {
					t.Fatalf("cleanup diagnostics were lost or not redacted: %q", cleanup)
				}
			} else if test.name == "host allowlist" {
				fixture.Assert(t, results[0].Output, root)
			} else if test.name == "bounded failure note" {
				result := results[0]
				if len(result.Output) > KeptOutputBytes || result.OutputGap.Omitted == 0 || strings.Count(result.Output, "[OwnGit did not pass") != 1 {
					t.Fatalf("failure log: bytes=%d gap=%+v", len(result.Output), result.OutputGap)
				}
			} else if test.name == "explicit adapter" && !strings.Contains(results[0].Output, "CHECK_LOCAL_SETTING=parent-only-value") {
				t.Fatal("trusted adapter did not receive its explicit environment")
			} else if strings.Contains(results[0].Output, "did not pass") || strings.Contains(results[0].Output, "must-not-run") {
				t.Fatalf("unexpected execution or guidance: %q", results[0].Output)
			}
		})
	}
}

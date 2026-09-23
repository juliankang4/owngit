//go:build !windows

package checkexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunReapsRedirectedBackgroundDescendantsOnUnix(t *testing.T) {
	directory := t.TempDir()
	positiveMarker := filepath.Join(directory, "positive")
	positiveCommand := backgroundMarkerCommand(positiveMarker, "exit 0")
	if err := exec.Command("sh", "-c", positiveCommand).Run(); err != nil {
		t.Fatalf("positive control did not start: %v", err)
	}
	if !waitForMarker(positiveMarker, 3*time.Second) {
		t.Fatal("positive control descendant did not survive its parent")
	}
	if err := os.Remove(positiveMarker); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		tail   string
		status string
	}{
		{name: "passing", tail: "exit 0", status: StatusPassed},
		{name: "failing", tail: "exit 7", status: StatusFailed},
		{name: "missing", tail: "definitely-not-a-command-owngit", status: StatusUnavailable},
	}
	markers := make([]string, 0, len(cases))
	for _, testCase := range cases {
		marker := filepath.Join(directory, testCase.name)
		markers = append(markers, marker)
		definition := Definition{Name: testCase.name, Command: backgroundMarkerCommand(marker, testCase.tail)}
		results, _ := Run(context.Background(), []Definition{definition}, Options{Timeout: 10 * time.Second})
		if len(results) != 1 || results[0].Status != testCase.status {
			t.Fatalf("%s result=%+v", testCase.name, results)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	for _, marker := range markers {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("background descendant wrote %q after cleanup: %v", marker, err)
		}
	}
}

func backgroundMarkerCommand(marker, tail string) string {
	return fmt.Sprintf("(sleep 1; printf done > %s) >/dev/null 2>&1 & %s", shellQuoteFixture(marker), tail)
}

func shellQuoteFixture(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func waitForMarker(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

package checkexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

type failingOutputTap struct{}

func (failingOutputTap) Write([]byte) (int, error) { return 0, fmt.Errorf("tap stopped") }

func TestRunOutputTap(t *testing.T) {
	command := "printf 'raw-value'"
	if runtime.GOOS == "windows" {
		command = "echo raw-value"
	}
	for _, test := range []struct {
		name  string
		limit int64
		fail  bool
		want  string
	}{
		{name: "capture delegated", limit: 100, want: StatusPassed},
		{name: "raw bytes still limited", limit: 2, want: StatusIncomplete},
		{name: "tap error stops execution", limit: 100, fail: true, want: StatusError},
	} {
		t.Run(test.name, func(t *testing.T) {
			var captured bytes.Buffer
			var tap io.Writer = &captured
			if test.fail {
				tap = failingOutputTap{}
			}
			environment, _ := HostEnvironment(t.TempDir())
			results, cancelled := Run(context.Background(), []Definition{{Command: command}}, Options{Env: environment, OutputTap: tap, OutputLimit: test.limit})
			result := results[0]
			if cancelled || result.Status != test.want || strings.Contains(result.Output, "raw-value") {
				t.Fatal(result, cancelled)
			}
			if !test.fail && !strings.Contains(captured.String(), "raw-value") {
				t.Fatal(captured.String())
			}
			if test.fail && !strings.Contains(result.Output, "tap stopped") {
				t.Fatal(result)
			}
		})
	}
}

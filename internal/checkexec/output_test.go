package checkexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const outputFixtureBytes = "OWNGIT_CHECKEXEC_OUTPUT_BYTES"

// TestOutputFixture prints the requested number of bytes when a test runs the
// test binary as a check, so output size does not depend on a shell.
func TestOutputFixture(t *testing.T) {
	size, err := strconv.Atoi(os.Getenv(outputFixtureBytes))
	if err != nil {
		t.Skip("output fixture only")
	}
	_, _ = os.Stdout.WriteString(strings.Repeat("x", size))
	os.Exit(0)
}

func outputChecks(count int) []Definition {
	definitions := make([]Definition, count)
	for index := range definitions {
		definitions[index] = Definition{Name: "output-" + strconv.Itoa(index), Command: "output fixture",
			Executable: os.Args[0], Arguments: []string{"-test.run=^TestOutputFixture$"}}
	}
	return definitions
}

func outputEnvironment(size int) []string {
	return append(os.Environ(), outputFixtureBytes+"="+strconv.Itoa(size))
}

// A large output limit counts every byte but keeps only the evidence prefix,
// so many checks with much output hold no more than that prefix each.
func TestRunKeepsOnlyTheEvidenceOfManyLargeOutputs(t *testing.T) {
	const checks, size = 20, 4 << 20
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	results, cancelled := Run(context.Background(), outputChecks(checks), Options{
		Timeout: time.Minute, OutputLimit: 1 << 30, Env: outputEnvironment(size),
	})
	runtime.GC()
	runtime.ReadMemStats(&after)
	if cancelled || len(results) != checks {
		t.Fatalf("cancelled=%v results=%d", cancelled, len(results))
	}
	retained := 0
	for _, result := range results {
		if result.Status != StatusPassed || result.Truncated || len(result.Output) != KeptOutputBytes {
			t.Fatalf("result=%s status=%s truncated=%v output=%d", result.Name, result.Status, result.Truncated, len(result.Output))
		}
		retained += len(result.Output)
	}
	// Keeping whole outputs would hold checks*size = 80 MiB.
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if limit := int64(checks*KeptOutputBytes + 4<<20); growth > limit {
		t.Fatalf("heap grew %d bytes with %d kept; want at most %d", growth, retained, limit)
	}
	runtime.KeepAlive(results)
}

// Bytes past the kept prefix still count against the output limit, and
// passing the limit stops the check and says so.
func TestRunCountsOutputBeyondTheKeptPrefix(t *testing.T) {
	const limit = 1 << 20
	results, _ := Run(context.Background(), outputChecks(1), Options{
		Timeout: time.Minute, OutputLimit: limit, Env: outputEnvironment(limit),
	})
	if result := results[0]; result.Status != StatusPassed || result.Truncated || len(result.Output) != KeptOutputBytes {
		t.Fatalf("at the limit: status=%s truncated=%v output=%d", result.Status, result.Truncated, len(result.Output))
	}
	results, _ = Run(context.Background(), outputChecks(1), Options{
		Timeout: time.Minute, OutputLimit: limit, Env: outputEnvironment(limit + 1),
	})
	note := "[OwnGit stopped this check: its output passed the limit of 1048576 bytes.]\n"
	if result := results[0]; result.Status != StatusIncomplete || !result.Truncated ||
		!strings.HasPrefix(result.Output, note) || len(result.Output) != len(note)+KeptOutputBytes {
		t.Fatalf("past the limit: status=%s truncated=%v output=%d", result.Status, result.Truncated, len(result.Output))
	}
}

// TestOverLimitChild prints past a 1 KiB limit on stdout or stderr, waits, and
// then leaves a marker that only a check still running could write.
func TestOverLimitChild(t *testing.T) {
	marker := os.Getenv("OWNGIT_CHECKEXEC_AFTER_OUTPUT_MARKER")
	if marker == "" {
		t.Skip("over-limit fixture only")
	}
	stream := os.Stdout
	if os.Getenv("OWNGIT_CHECKEXEC_STDERR") != "" {
		stream = os.Stderr
	}
	_, _ = stream.WriteString(strings.Repeat("x", 2048))
	time.Sleep(time.Second)
	if err := os.WriteFile(marker, []byte("still running after the output limit"), 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// The reviewer's reproduction: output past the limit stops the command as a
// timeout does, on either stream.
func TestRunStopsACheckWhoseOutputPassesTheLimit(t *testing.T) {
	for _, stderr := range []string{"", "1"} {
		marker := filepath.Join(t.TempDir(), "after-output-limit")
		started := time.Now()
		results, cancelled := Run(context.Background(), []Definition{{Name: "over-limit", Command: "over-limit output",
			Executable: os.Args[0], Arguments: []string{"-test.run=^TestOverLimitChild$"}}}, Options{
			Timeout: 5 * time.Second, OutputLimit: 1024,
			Env: append(os.Environ(), "OWNGIT_CHECKEXEC_AFTER_OUTPUT_MARKER="+marker, "OWNGIT_CHECKEXEC_STDERR="+stderr),
		})
		elapsed := time.Since(started)
		time.Sleep(1500 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("stderr=%q: the command kept running after its output passed the limit", stderr)
		}
		result := results[0]
		if cancelled || result.Status != StatusIncomplete || !result.Truncated || result.CleanupError != "" || elapsed >= time.Second {
			t.Fatalf("stderr=%q: cancelled=%v status=%s truncated=%v cleanup=%q elapsed=%s", stderr, cancelled, result.Status, result.Truncated, result.CleanupError, elapsed)
		}
	}
}

func TestKeptOutputDropsASecretCutAtTheEnd(t *testing.T) {
	secrets := []string{"secret-token"}
	for _, testCase := range []struct {
		written, want string
	}{
		{"log secret-token done", "log [redacted] done"},
		{"log secret-tok", "log "},
		{"log secret-tokXsec", "log secret-tokX"},
	} {
		buffer := newBoundedBuffer(1 << 20)
		buffer.keep = len(testCase.written)
		_, _ = buffer.Write([]byte(testCase.written + "more"))
		if got := buffer.text(secrets); got != testCase.want {
			t.Fatalf("written=%q text=%q want %q", testCase.written, got, testCase.want)
		}
	}
	// Nothing dropped: the output is whole, so nothing is cut.
	buffer := newBoundedBuffer(64)
	_, _ = buffer.Write([]byte("ends with sec"))
	if got := buffer.text(secrets); got != "ends with sec" {
		t.Fatalf("whole output=%q", got)
	}
}

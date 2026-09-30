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
		if result.Status != StatusPassed || result.Truncated || len(result.Output) != KeptOutputBytes+1 {
			t.Fatalf("result=%s status=%s truncated=%v output=%d", result.Name, result.Status, result.Truncated, len(result.Output))
		}
		retained += len(result.Output)
	}
	// Keeping whole outputs would hold checks*size = 80 MiB.
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if limit := int64(checks*(KeptOutputBytes+1) + 4<<20); growth > limit {
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
	if result := results[0]; result.Status != StatusPassed || result.Truncated || len(result.Output) != KeptOutputBytes+1 {
		t.Fatalf("at the limit: status=%s truncated=%v output=%d", result.Status, result.Truncated, len(result.Output))
	}
	results, _ = Run(context.Background(), outputChecks(1), Options{
		Timeout: time.Minute, OutputLimit: limit, Env: outputEnvironment(limit + 1),
	})
	note := "[OwnGit stopped this check: its output passed the limit of 1048576 bytes.]\n"
	if result := results[0]; result.Status != StatusIncomplete || !result.Truncated ||
		!strings.HasPrefix(result.Output, note) || len(result.Output) != len(note)+KeptOutputBytes+1 {
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
		// Output past the limit is dropped, so the kept text ends at the limit.
		buffer := newBoundedBuffer(int64(len(testCase.written)), secrets)
		_, _ = buffer.Write([]byte(testCase.written + "more"))
		if got := buffer.text(); got != testCase.want {
			t.Fatalf("written=%q text=%q want %q", testCase.written, got, testCase.want)
		}
	}
	// Nothing dropped: the output is whole, so nothing is cut.
	buffer := newBoundedBuffer(64, secrets)
	_, _ = buffer.Write([]byte("ends with sec"))
	if got := buffer.text(); got != "ends with sec" {
		t.Fatalf("whole output=%q", got)
	}
}

// The kept text is the head of the whole output with secrets replaced and
// invalid UTF-8 replaced as the evidence builders do, however the output
// arrives in pieces.
func TestKeptOutputIsTheHeadOfTheConvertedOutput(t *testing.T) {
	secret := "synthetic-token-" + strings.Repeat("s", 49)
	long := strings.Repeat("가나다", KeptOutputBytes/9+10)
	for name, full := range map[string]string{
		"ordinary":           "hello 가나다 world\n",
		"invalid runs":       "a\xff\xfe\xfdb\xe4\xb8c\xef\xbf\xbdd\xff",
		"secrets":            secret + " x " + secret[:10] + " " + secret + secret + " tail " + secret[:20],
		"secret and invalid": "\xff" + secret + "\xff\xff" + secret + "\xe4",
		"many invalid bytes": strings.Repeat("\xff", 1<<20) + "END",
		"many secrets":       strings.Repeat(secret+"\n", 16000) + "END",
		"longer than kept":   long,
	} {
		want := strings.ToValidUTF8(strings.ReplaceAll(full, secret, "[redacted]"), "\uFFFD")
		for _, piece := range []int{1, 2, 3, 7, 64, 1 << 20} {
			buffer := newBoundedBuffer(1<<30, []string{secret})
			for start := 0; start < len(full); start += piece {
				_, _ = buffer.Write([]byte(full[start:min(start+piece, len(full))]))
			}
			got := buffer.text()
			whole := len(want) <= KeptOutputBytes
			if whole && got != want || !whole && (!strings.HasPrefix(want, got) || len(got) <= KeptOutputBytes || len(got) > KeptOutputBytes+len("[redacted]")) {
				t.Fatalf("%s in pieces of %d: kept %d bytes of %d converted", name, piece, len(got), len(want))
			}
		}
	}
}

// withoutSecretLetters reports whether text, with every replacement removed,
// has none of the letters the overlap secrets are made of. Filler output uses
// other letters, so any such letter is part of a secret.
func withoutSecretLetters(text string) bool {
	return !strings.ContainsAny(strings.ReplaceAll(text, "[redacted]", ""), "abcde")
}

var overlapCases = []struct {
	name    string
	output  string
	secrets []string
	want    string
}{
	{"self overlap", "ababababababababab", []string{"abababababab"}, "[redacted]"},
	{"two secrets", "abcde", []string{"abc", "cde"}, "[redacted]"},
	{"two secrets reversed", "abcde", []string{"cde", "abc"}, "[redacted]"},
	{"touching", "abcabc", []string{"abc"}, "[redacted][redacted]"},
	{"in text", "x abababababab!ab y", []string{"abababababab"}, "x [redacted]!ab y"},
	{"inside a character", "가b가", []string{"\xb0\x80b"}, "\uFFFD[redacted]가"},
}

// Overlapping secret occurrences are one replaced range, however the output
// is split into writes.
func TestOverlappingSecretsAreReplacedWhole(t *testing.T) {
	for _, testCase := range overlapCases {
		for split := 0; split <= len(testCase.output); split++ {
			for _, piece := range []int{1, len(testCase.output)} {
				buffer := newBoundedBuffer(1<<20, testCase.secrets)
				for _, part := range []string{testCase.output[:split], testCase.output[split:]} {
					for start := 0; start < len(part); start += piece {
						_, _ = buffer.Write([]byte(part[start:min(start+piece, len(part))]))
					}
				}
				if got := buffer.text(); got != testCase.want {
					t.Fatalf("%s split at %d in pieces of %d: %q, want %q", testCase.name, split, piece, got, testCase.want)
				}
			}
		}
		if got := redact(testCase.output, testCase.secrets); got != testCase.want {
			t.Fatalf("%s message: %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// No part of an overlapping secret is kept where the kept text or the output
// limit cuts through it.
func TestOverlappingSecretsAtTheKeptCutAndTheLimit(t *testing.T) {
	for _, testCase := range overlapCases[:2] {
		for shift := -len(testCase.output); shift <= len(testCase.output); shift++ {
			output := strings.Repeat("x", KeptOutputBytes+shift) + testCase.output + strings.Repeat("y", 64)
			buffer := newBoundedBuffer(1<<30, testCase.secrets)
			_, _ = buffer.Write([]byte(output))
			if got := buffer.text(); !withoutSecretLetters(got) || len(got) <= KeptOutputBytes {
				t.Fatalf("%s at the kept cut, shift %d: kept %d bytes ending %q", testCase.name, shift, len(got), got[max(0, len(got)-40):])
			}
			for limit := 1; limit <= len(testCase.output)+1; limit++ {
				cut := newBoundedBuffer(int64(len("xx")+limit), testCase.secrets)
				_, _ = cut.Write([]byte("xx" + testCase.output + "yy"))
				if got := cut.text(); !withoutSecretLetters(got) {
					t.Fatalf("%s cut by a limit of %d: %q", testCase.name, len("xx")+limit, got)
				}
			}
		}
	}
}

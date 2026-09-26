package markdown

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
)

var testLinks = Links{Dir: "docs", File: "/repositories/r1/code?ref=main&path=", Raw: "/repositories/r1/raw?ref=main&path="}

// withChild runs the test with a misbehaving child from TestMain.
func withChild(t *testing.T, mode string) {
	t.Helper()
	helper.Lock()
	saved := helper.env
	helper.env = []string{"MDTEST_CHILD=" + mode}
	helper.Unlock()
	t.Cleanup(func() {
		helper.Lock()
		helper.env = saved
		helper.Unlock()
	})
}

// withKillAfter shortens the time a child may run.
func withKillAfter(t *testing.T, limit time.Duration) {
	t.Helper()
	saved := killAfter
	killAfter = limit
	t.Cleanup(func() { killAfter = saved })
}

// forget removes a source from the remembered refusals, so tests stay
// independent.
func forget(source []byte) {
	key := sha256.Sum256(source)
	refused.mu.Lock()
	defer refused.mu.Unlock()
	delete(refused.seen, key)
}

func TestChildRendersWithTheLinksItIsGiven(t *testing.T) {
	source := []byte("# Guide\n\n[setup](setup.md) ![d](img/d.png) [top](#Guide)\n")
	out, err := Render(context.Background(), source, testLinks)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id="md-guide"`,
		`href="/repositories/r1/code?ref=main&amp;path=docs%2Fsetup.md"`,
		`src="/repositories/r1/raw?ref=main&amp;path=docs%2Fimg%2Fd.png"`,
		`href="#md-guide"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
}

// A document already rendered for the same links is served from memory;
// other links, or another source, start a child.
func TestRenderingsAreKept(t *testing.T) {
	source := []byte(fmt.Sprintf("# Kept %d\n\n[a](a.md)\n", time.Now().UnixNano()))
	starts := childStarts.Load()
	first, err := Render(context.Background(), source, testLinks)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := Render(context.Background(), source, testLinks)
		if err != nil || again != first {
			t.Fatalf("a kept rendering differs: %v", err)
		}
	}
	if started := childStarts.Load() - starts; started != 1 {
		t.Fatalf("six views of one document started %d children", started)
	}
	other := testLinks
	other.Dir = "elsewhere"
	elsewhere, err := Render(context.Background(), source, other)
	if err != nil || elsewhere == first || childStarts.Load()-starts != 2 {
		t.Fatalf("the same source in another folder reused links it should not: %v", err)
	}
}

func TestRenderedSetIsBounded(t *testing.T) {
	set := &renderedSet{entries: map[[sha256.Size]byte]*list.Element{}, order: list.New()}
	key := func(i int) [sha256.Size]byte { return sha256.Sum256([]byte(fmt.Sprint(i))) }
	for i := 0; i < renderedEntries+10; i++ {
		set.put(key(i), "x")
	}
	if len(set.entries) != renderedEntries {
		t.Fatalf("kept %d renderings", len(set.entries))
	}
	if _, ok := set.get(key(0)); ok {
		t.Fatal("the oldest rendering was kept")
	}
	big := strings.Repeat("x", renderedBytes/3)
	for i := 0; i < 4; i++ {
		set.put(key(1000+i), big)
	}
	if set.size > renderedBytes {
		t.Fatalf("kept %d bytes of renderings", set.size)
	}
}

// stopWithin bounds how long a stopped child may take to be reaped. It is
// generous because it must hold on a machine busy with other tests; the
// limits themselves are much shorter.
const stopWithin = 20 * time.Second

// Each limit stops the child, the page gets ErrTooComplex, and the source
// is not tried again. What is checked does not depend on the share of the
// processor the child gets: the reason it stopped, that it was reaped within
// a generous bound, its peak memory, and how many children later views start.
func TestChildLimits(t *testing.T) {
	cases := []struct {
		mode, reason string
		// killAfter is short only where the parent's time limit is what is
		// being tested; elsewhere the child's own limit must act first.
		killAfter time.Duration
	}{
		{"spin", "time", 300 * time.Millisecond},
		{"flood", "output", time.Minute},
		{"hog", "memory", time.Minute},
		{"panic", "failed", time.Minute},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			withChild(t, c.mode)
			withKillAfter(t, c.killAfter)
			source := []byte("# limits " + c.mode + fmt.Sprint(time.Now().UnixNano()))
			defer forget(source)
			defer forgetSlow(source)
			start := time.Now()
			result := renderInChild(source, testLinks)
			elapsed := time.Since(start)
			if !errors.Is(result.err, ErrTooComplex) || !result.documentFault || result.reason != c.reason {
				t.Fatalf("got %v, fault %v, reason %q", result.err, result.documentFault, result.reason)
			}
			if elapsed > stopWithin {
				t.Fatalf("the child was stopped after %v", elapsed)
			}
			if result.state == nil || !result.state.Exited() && c.reason != "time" && c.reason != "output" {
				t.Fatalf("the child was not reaped: %v", result.state)
			}
			checkChildMemory(t, c.mode, result)
			t.Logf("%s stopped by %s after %v, cpu %v, peak %d MiB", c.mode, result.reason, elapsed.Round(time.Millisecond), result.cpu().Round(time.Millisecond), peakMemory(result.state)>>20)

			// Render has not seen this source yet. Memory, output and a
			// failure are remembered at the first view. A timeout is too when
			// the child used the processor for most of it, which a spinning
			// child does on an idle machine; on a busy one it is remembered at
			// the second (see TestRememberFailure).
			starts := childStarts.Load()
			for view := 0; view < 3; view++ {
				if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrTooComplex) {
					t.Fatalf("view %d: %v", view, err)
				}
			}
			started := childStarts.Load() - starts
			if c.reason == "time" && started > 2 || c.reason != "time" && started != 1 {
				t.Fatalf("three views of a failing source started %d children", started)
			}
			if !refused.has(sha256.Sum256(source)) {
				t.Fatal("the failing source is not remembered")
			}
		})
	}
}

// The policy for failed children, with processor and time values given
// rather than measured, so it holds however busy the machine is.
func TestRememberFailure(t *testing.T) {
	limit := 4 * time.Second
	cases := []struct {
		name                        string
		fault, timedOut, slowBefore bool
		cpu                         time.Duration
		want                        int
	}{
		{"helper unavailable", false, false, false, 0, rememberNothing},
		{"memory", true, false, false, time.Second, rememberRefused},
		{"output", true, false, false, 0, rememberRefused},
		{"signal", true, false, false, 0, rememberRefused},
		{"timeout using the processor", true, true, false, 3 * time.Second, rememberRefused},
		{"timeout at exactly half", true, true, false, 2 * time.Second, rememberRefused},
		{"timeout on a busy machine", true, true, false, 140 * time.Millisecond, rememberSlow},
		{"second timeout on a busy machine", true, true, true, 140 * time.Millisecond, rememberRefused},
		{"first non-time failure after a slow timeout", true, false, true, 0, rememberRefused},
	}
	for _, c := range cases {
		if got := rememberFailure(c.fault, c.timedOut, c.cpu, limit, c.slowBefore); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func forgetSlow(source []byte) {
	slow.mu.Lock()
	defer slow.mu.Unlock()
	delete(slow.seen, sha256.Sum256(source))
}

// runtimeAllowance is how much memory a child may hold beside its heap
// objects: the runtime's span and collector metadata, stacks, and freed pages
// not yet returned. Measured at 20 to 30 MiB on macOS.
const runtimeAllowance = 64 << 20

// checkChildMemory checks what the memory limit guarantees. A child stopped
// by it saw a heap past childMemoryLimit, and its peak resident memory is that
// heap plus the runtime's own memory, so the peak measures the heap and not
// memory the process had given back. How far the heap got past the limit
// depends on when the watcher could run (see the comment on the containment in
// child.go), so it is logged, not bounded. A child stopped otherwise never
// had its heap pass the limit at a sample.
func checkChildMemory(t *testing.T, name string, result childResult) {
	t.Helper()
	peak := uint64(peakMemory(result.state))
	if result.reason != "memory" {
		if peak > (childMemoryLimit+runtimeAllowance)*memoryScale {
			t.Errorf("%s: stopped by %s, the child reached %d MiB", name, result.reason, peak>>20)
		}
		return
	}
	if result.heapAtStop <= childMemoryLimit {
		t.Errorf("%s: stopped for memory with a reported heap of %d MiB, not past the %d MiB limit", name, result.heapAtStop>>20, childMemoryLimit>>20)
		return
	}
	t.Logf("%s: heap %d MiB at the stop (%d MiB past the limit), peak %d MiB", name, result.heapAtStop>>20, (result.heapAtStop-childMemoryLimit)>>20, peak>>20)
	if peak > (result.heapAtStop+runtimeAllowance)*memoryScale {
		t.Errorf("%s: peak %d MiB, more than the %d MiB heap at the stop and the runtime's own memory", name, peak>>20, result.heapAtStop>>20)
	}
}

// The review's documents: a small table whose rows are padded to thousands
// of cells, and reference links that multiply the output. Rendered in a
// child without the estimate, each is stopped by one of the child's limits.
// Which limit comes first depends on the machine: a slow one reaches the time
// limit before the heap passes the memory limit. Either stop is correct, and
// checkChildMemory bounds the memory in both cases.
// The tables never reach a child in the product, because the estimate
// refuses them first.
func TestAmplifyingDocumentsStayWithinTheChildLimits(t *testing.T) {
	cases := map[string]string{
		"padded table, 2000 short rows (20 KB)":      tableRows(4000, 2000),
		"padded table, 400 short rows, text (24 KB)": tableRows(4000, 400) + strings.Repeat("a\n", 4000),
		"reused reference links":                     referenceAmplifier(40000, 10000, 20),
		"reference links, one per line":              referenceAmplifier(40000, 10000, 1),
	}
	for name, source := range cases {
		source := []byte(source)
		if !strings.Contains(name, "reference") {
			forget(source)
			starts := childStarts.Load()
			if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrTooComplex) || childStarts.Load() != starts {
				t.Errorf("%s: Render returned %v after starting %d children; the estimate should refuse it first", name, err, childStarts.Load()-starts)
			}
		}
		start := time.Now()
		result := renderInChild(source, testLinks)
		elapsed := time.Since(start)
		t.Logf("%s: %d bytes, %s after %v", name, len(source), result.reason, elapsed.Round(time.Millisecond))
		if !errors.Is(result.err, ErrTooComplex) || !result.documentFault {
			t.Errorf("%s: got %v", name, result.err)
		}
		// A renderer failure or a signal the parent did not send would mean a
		// bug or a breached limit, whatever the machine's speed.
		if result.reason != "memory" && result.reason != "time" && result.reason != "output" {
			t.Errorf("%s: stopped by %s, want one of the child's limits", name, result.reason)
		}
		checkChildMemory(t, name, result)
	}
}

func tableRows(columns, rows int) string {
	return strings.Repeat("|a", columns) + "|\n" + strings.Repeat("|-", columns) + "|\n" + strings.Repeat("a\n", rows)
}

func referenceAmplifier(urlLength, uses, perLine int) string {
	var b strings.Builder
	b.WriteString("[a]: http://x.y/" + strings.Repeat("a", urlLength) + " \"" + strings.Repeat("t", urlLength) + "\"\n\n")
	for i := 0; i < uses; i++ {
		b.WriteString("[a]")
		if i%perLine == perLine-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestWithoutAHelperNothingRenders(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	helper.Lock()
	saved := helper.path
	helper.path = ""
	helper.Unlock()
	defer SetHelper(saved)
	if _, err := Render(context.Background(), []byte("# no helper\n"), testLinks); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

// A child from an older or newer binary refuses a request it cannot read.
func TestChildRefusesAnotherProtocol(t *testing.T) {
	var out strings.Builder
	code := serveRequest(strings.NewReader(`{"protocol":99,"links":{}}`+"\n# a\n"), &out, convert)
	if code != exitBadRequest || out.Len() != 0 {
		t.Fatalf("code %d, output %q", code, out.String())
	}
}

// A badge is an outside image inside a link. The link keeps its target and
// the image leaves its description, so no link holds another.
func TestBadgesKeepTheirLinks(t *testing.T) {
	out := render(t, "", "[![build status](https://img.example.test/b.svg)](https://ci.example.test/r) [see https://example.test/x](docs.md)")
	if strings.Count(out, "<a ") != 2 || strings.Contains(out, "img.example.test") {
		t.Fatalf("a link holds another or the badge address: %q", out)
	}
	if !strings.Contains(out, `<a href="https://ci.example.test/r" rel="noopener noreferrer">build status</a>`) {
		t.Fatalf("the badge lost its link: %q", out)
	}
	if !strings.Contains(out, `<a href="/repositories/r1/code?path=docs.md">see https://example.test/x</a>`) {
		t.Fatalf("an address inside link text became a link: %q", out)
	}
}

// A child that ran out of time while hardly using the processor was starved
// by a busy machine, so its source is tried once more before it is refused.
//
// The child sleeps, so it uses almost no processor time however busy the
// machine is; one second leaves its start-up far below half of the limit.
func TestTimeoutOnABusyMachineIsRetriedOnce(t *testing.T) {
	withChild(t, "hang")
	withKillAfter(t, time.Second)
	source := []byte(fmt.Sprintf("# starved %d\n", time.Now().UnixNano()))
	defer forget(source)
	defer forgetSlow(source)
	starts := childStarts.Load()
	for view := 1; view <= 3; view++ {
		if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrTooComplex) {
			t.Fatalf("view %d: %v", view, err)
		}
		want := int64(min(view, 2))
		if started := childStarts.Load() - starts; started != want {
			t.Fatalf("after view %d, %d children started, want %d", view, started, want)
		}
	}
}

// Without a parent to kill it, as after the server was killed with SIGKILL,
// a child stops itself at its own deadline.
func TestChildStopsItselfWithoutItsParent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, ChildCommand)
	cmd.Env = append(os.Environ(), "MDTEST_CHILD=hang", "MDTEST_CHILD_DEADLINE=300ms")
	cmd.Stdin = strings.NewReader(`{"protocol":1,"links":{}}` + "\n# a\n")
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("the child ran on without its parent")
	}
	if code, _ := gitexec.ExitCode(err); code != exitTimeout {
		t.Fatalf("exit %v, want %d", err, exitTimeout)
	}
	if elapsed := time.Since(start); elapsed > stopWithin {
		t.Fatalf("the child stopped after %v", elapsed)
	}
	if childDeadline <= killAfter+childGrace {
		t.Fatalf("a child's own deadline %v must come after the parent's kill at %v", childDeadline, killAfter+childGrace)
	}
}

// A child killed by a signal the parent did not send, such as the system's
// out-of-memory killer, is remembered like a child that stopped itself.
func TestChildKilledBySignalIsRemembered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a killed process by exit code, not by signal")
	}
	withChild(t, "killed")
	source := []byte(fmt.Sprintf("# killed %d\n", time.Now().UnixNano()))
	defer forget(source)
	starts := childStarts.Load()
	for view := 0; view < 3; view++ {
		if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrTooComplex) {
			t.Fatalf("view %d: %v", view, err)
		}
	}
	if started := childStarts.Load() - starts; started != 1 {
		t.Fatalf("three views started %d children", started)
	}
}

// When no child can run, the page is told so, the cause is logged once, and
// nothing about the document is remembered.
func TestUnavailableHelperIsLoggedAndNotRemembered(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	unavailableLog.Lock()
	unavailableLog.last = map[string]time.Time{}
	unavailableLog.Unlock()

	source := []byte(fmt.Sprintf("# secret title %d\n", time.Now().UnixNano()))
	helper.Lock()
	saved := helper.path
	helper.path = filepath.Join(t.TempDir(), "missing-owngit")
	helper.Unlock()
	for view := 0; view < 3; view++ {
		if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing helper, view %d: %v", view, err)
		}
	}
	SetHelper(saved)

	// A child of another request format, as after an upgrade.
	withChild(t, "another-protocol")
	starts := childStarts.Load()
	for view := 0; view < 3; view++ {
		if _, err := Render(context.Background(), source, testLinks); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("refusing helper, view %d: %v", view, err)
		}
	}
	if started := childStarts.Load() - starts; started != 3 {
		t.Fatalf("a refusing helper was remembered as a document failure: %d starts", started)
	}
	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "could not start") || !strings.Contains(lines[1], "refused the request") {
		t.Fatalf("want one line per cause, got %q", logged.String())
	}
	if strings.Contains(logged.String(), "secret title") {
		t.Fatal("the log names the document")
	}
	if refused.has(sha256.Sum256(source)) {
		t.Fatal("the document was remembered as refused")
	}
}

package markdown

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"owngit/internal/gitexec"
)

// Containment. goldmark has shapes whose time or memory grows faster than
// the source, and estimates of them kept missing cases. A document is
// therefore rendered in a child process: the owngit binary started again with
// ChildCommand, the request on stdin and the HTML on stdout. The parent kills
// the child when it runs past renderBudget or writes more than maxOutput, which
// stops its processor use for real, and the child stops itself when its heap
// passes childMemoryLimit. A document that fails any of these is remembered
// and not rendered again while the server runs.
//
// These limits are the second line of defense. estimateCost refuses the known
// costly shapes before any child starts. The memory limit is sampled: the
// child runs Go on one processor, so its watcher runs only when the renderer
// is preempted, and the heap can grow past the limit by what the renderer
// allocates in between. For the padded tables in the tests, the heap at the
// stop was measured at 256 to 303 MiB on an idle Mac and up to 320 MiB with
// every core busy, with the peak resident memory 10 to 44 MiB above it. A
// second processor for the watcher would shorten the gaps but let garbage
// collection use two cores. The stop is certain; how far past the limit it
// comes depends on scheduling.
const (
	// ChildCommand is the hidden first argument that makes the owngit binary
	// render one document from stdin. It is not a user command.
	ChildCommand = "internal-render-markdown"

	// childProtocol changes when the request format changes, so a child from
	// a replaced binary refuses a request it would misread.
	childProtocol = 1

	// childMemoryLimit is the heap at which a child gives up. Ordinary
	// documents of MaxSource need well under 32 MiB.
	childMemoryLimit = 256 << 20
	// childMemoryCheck is how often the child samples its heap.
	childMemoryCheck = 2 * time.Millisecond

	// childGrace is the time between asking a child to stop and killing it.
	childGrace = 200 * time.Millisecond
)

// childDeadline is when a child stops itself. The parent kills it earlier,
// at renderBudget; this bound matters when the parent was killed without a
// chance to do so, since on Unix the child is in its own process group. A
// variable only so tests can shorten it.
var childDeadline = renderBudget + time.Second

// killAfter is renderBudget, as a variable so tests can shorten it.
var killAfter = renderBudget

// Child exit codes. Every code other than exitRendered means the document
// is shown as source.
const (
	exitRendered   = 0
	exitBadRequest = 2
	exitTooLarge   = 3 // the HTML passed maxOutput
	exitMemory     = 4 // the heap passed childMemoryLimit
	exitFailed     = 5 // goldmark failed or panicked
	exitTimeout    = 6 // childDeadline passed
)

// childRequest is the first line of a child's stdin; the source follows.
type childRequest struct {
	Protocol int   `json:"protocol"`
	Links    Links `json:"links"`
}

var helper struct {
	sync.Mutex
	path string
	env  []string // added to the child's environment; tests only
}

// childStarts counts started children, for tests.
var childStarts atomic.Int64

// SetHelper names the owngit binary that renders documents in a child
// process. The binary must pass ChildCommand to RunChild. Until SetHelper is
// called, Render returns ErrUnavailable for every document.
func SetHelper(executable string) {
	helper.Lock()
	defer helper.Unlock()
	helper.path = executable
}

// IsChild reports whether the process was started to render one document.
func IsChild(arguments []string) bool {
	return len(arguments) == 2 && arguments[1] == ChildCommand
}

// RunChild renders the request on stdin to stdout and returns the exit code.
// The caller exits with it at once.
func RunChild(stdin io.Reader, stdout io.Writer) int {
	return runChild(stdin, stdout, convert)
}

func runChild(stdin io.Reader, stdout io.Writer, render func([]byte, Resolver) (string, error)) int {
	// One processor keeps a child, garbage collection included, to about
	// one core; the parent runs at most renderSlots children.
	runtime.GOMAXPROCS(1)
	// The collector works harder as the heap nears the limit, which keeps
	// ordinary garbage from counting against it.
	debug.SetMemoryLimit(childMemoryLimit / 2)
	go watchMemory(childMemoryLimit, childMemoryCheck, func(heap uint64) {
		// The parent reads this line; the tests check the heap it reports.
		fmt.Fprintf(os.Stderr, "%s%d\n", heapAtStopPrefix, heap)
		os.Exit(exitMemory)
	})
	time.AfterFunc(childDeadline, func() { os.Exit(exitTimeout) })
	return serveRequest(stdin, stdout, render)
}

// serveRequest reads one request and writes its HTML. It is runChild without
// the process-wide limits, which exit the process.
func serveRequest(stdin io.Reader, stdout io.Writer, render func([]byte, Resolver) (string, error)) (code int) {
	defer func() {
		if recover() != nil {
			code = exitFailed
		}
	}()
	reader := bufio.NewReaderSize(stdin, 64<<10)
	header, err := reader.ReadSlice('\n')
	if err != nil {
		return exitBadRequest
	}
	var request childRequest
	if json.Unmarshal(header, &request) != nil || request.Protocol != childProtocol {
		return exitBadRequest
	}
	source, err := io.ReadAll(io.LimitReader(reader, MaxSource+1))
	if err != nil || len(source) > MaxSource {
		return exitBadRequest
	}
	html, err := render(source, request.Links.resolver())
	switch {
	case errors.Is(err, ErrTooComplex):
		return exitTooLarge
	case err != nil:
		return exitFailed
	}
	if _, err := io.WriteString(stdout, html); err != nil {
		return exitFailed
	}
	return exitRendered
}

// heapAtStopPrefix starts the stderr line on which a child stopped by the
// memory limit reports the heap it saw.
const heapAtStopPrefix = "owngit-render-heap-at-stop: "

// watchMemory calls exceeded with the heap size when the heap passes limit.
// It reads the heap size the runtime keeps current on every allocation,
// without stopping the program.
func watchMemory(limit uint64, every time.Duration, exceeded func(heap uint64)) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	for {
		metrics.Read(sample)
		if sample[0].Value.Kind() == metrics.KindUint64 && sample[0].Value.Uint64() > limit {
			exceeded(sample[0].Value.Uint64())
			return
		}
		time.Sleep(every)
	}
}

// childResult is the outcome of one child process.
type childResult struct {
	html string
	err  error
	// documentFault is true when the source itself failed (time, memory,
	// output or a goldmark failure), so it is remembered and not tried again.
	documentFault bool
	// reason says which limit stopped the child, or why no child could
	// render, for tests and the log.
	reason string
	// heapAtStop is the heap the child saw when its memory limit stopped
	// it, or zero.
	heapAtStop uint64
	state      *os.ProcessState
}

// timedOut reports a child stopped for time, and cpu is the processor time
// it used. A child can run out of time because the machine was busy rather
// than because the document is costly; see Render.
func (r childResult) timedOut() bool { return r.reason == "time" }

func (r childResult) cpu() time.Duration {
	if r.state == nil {
		return 0
	}
	return r.state.UserTime() + r.state.SystemTime()
}

// renderInChild renders source in a child process.
func renderInChild(source []byte, links Links) childResult {
	helper.Lock()
	path, extraEnv := helper.path, helper.env
	helper.Unlock()
	if path == "" {
		return childResult{err: ErrUnavailable, reason: "no helper is configured"}
	}
	header, err := json.Marshal(childRequest{Protocol: childProtocol, Links: links})
	if err != nil {
		return childResult{err: err, reason: "request"}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ctx, stop := context.WithTimeoutCause(ctx, killAfter, errRenderTimeout)
	defer stop()

	stdout := &cappedWriter{limit: maxOutput, exceeded: func() { cancel(errOutputTooLarge) }}
	cmd := exec.Command(path, ChildCommand)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = stdout
	stderr := &headWriter{limit: 4 << 10}
	cmd.Stderr = stderr
	input := io.MultiReader(bytes.NewReader(header), bytes.NewReader([]byte{'\n'}), bytes.NewReader(source))
	childStarts.Add(1)
	runErr := gitexec.RunOwned(ctx, cmd, input, childGrace)
	result := childResult{state: cmd.ProcessState}
	complex := func(reason string) childResult {
		result.err, result.documentFault, result.reason = ErrTooComplex, true, reason
		return result
	}
	if ctx.Err() != nil {
		switch cause := context.Cause(ctx); {
		case errors.Is(cause, errRenderTimeout):
			return complex("time")
		case errors.Is(cause, errOutputTooLarge):
			return complex("output")
		}
	}
	if code, ok := gitexec.ExitCode(runErr); ok {
		switch code {
		case exitTooLarge:
			return complex("output")
		case exitMemory:
			result.heapAtStop = parseHeapAtStop(stderr.String())
			return complex("memory")
		case exitFailed:
			return complex("failed")
		case exitTimeout:
			return complex("time")
		case -1:
			// Killed by a signal the parent did not send, such as the
			// system's out-of-memory killer: the document is the likely
			// cause, as with the child's own memory limit.
			return complex("signal")
		case exitBadRequest:
			// Most likely a binary replaced by an upgrade with another
			// request format. The child's output says nothing more.
			result.err, result.reason = ErrUnavailable, "the helper refused the request (exit 2); was owngit upgraded while running?"
			return result
		}
		result.err, result.reason = ErrUnavailable, fmt.Sprintf("the helper exited with %d", code)
		return result
	}
	if runErr != nil {
		result.err, result.reason = ErrUnavailable, "the helper could not start: "+runErr.Error()
		return result
	}
	result.html = stdout.String()
	return result
}

// parseHeapAtStop returns the heap a child reported on stderr when its memory
// limit stopped it, or zero.
func parseHeapAtStop(stderr string) uint64 {
	_, value, found := strings.Cut(stderr, heapAtStopPrefix)
	if !found {
		return 0
	}
	value, _, _ = strings.Cut(value, "\n")
	heap, _ := strconv.ParseUint(value, 10, 64)
	return heap
}

var (
	errRenderTimeout  = errors.New("markdown render ran past its budget")
	errOutputTooLarge = errors.New("markdown render output passed its limit")
)

// cappedWriter keeps at most limit bytes and calls exceeded, once, when more
// arrive. It is safe for the concurrent use os/exec may make of it.
type cappedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	over     bool
	exceeded func()
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.over {
		w.mu.Unlock()
		return 0, errOutputTooLarge
	}
	if w.buf.Len()+len(p) > w.limit {
		w.over = true
		w.mu.Unlock()
		if w.exceeded != nil {
			w.exceeded()
		}
		return 0, errOutputTooLarge
	}
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// headWriter keeps the first limit bytes and accepts the rest without keeping
// it, so a child is never blocked on its stderr.
type headWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (w *headWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.limit - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (w *headWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

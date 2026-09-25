package githttp

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// progressChunk is the most a transfer writes to the client under one idle
// deadline, so a slow but moving client is not cut in the middle of a large
// write.
const progressChunk = 32 << 10

// transferDeadlines moves the connection deadlines of one Git transfer forward
// while data moves. The overall deadline bounds the whole transfer. The idle
// limit bounds each single wait for the client: a response write that the
// client does not accept, or a request body read that receives nothing. While
// Git itself works without output, no connection read or write is pending, so
// a quiet Git phase never runs against the idle limit.
//
// A connection deadline that passes makes net/http cancel the request, which
// stops Git as a disconnect does and releases the transfer slot and the
// repository lock.
type transferDeadlines struct {
	controller *http.ResponseController
	idle       time.Duration
	// overall is the operation deadline, or zero for none.
	overall time.Time
	stalled atomic.Bool
}

// next returns the deadline for a read or write that starts now.
func (d *transferDeadlines) next() time.Time {
	if d.idle <= 0 {
		return d.overall
	}
	next := time.Now().Add(d.idle)
	if !d.overall.IsZero() && d.overall.Before(next) {
		return d.overall
	}
	return next
}

// observe records that a read or write armed with deadline armed failed
// because the client moved no data within the idle limit.
func (d *transferDeadlines) observe(err error, armed time.Time) {
	if err != nil && errors.Is(err, os.ErrDeadlineExceeded) && !armed.IsZero() &&
		(d.overall.IsZero() || armed.Before(d.overall)) && !time.Now().Before(armed) {
		d.stalled.Store(true)
	}
}

// write writes content to writer in pieces of at most progressChunk bytes,
// each with a fresh write deadline.
func (d *transferDeadlines) write(writer io.Writer, content []byte) (int, error) {
	written := 0
	for len(content) > 0 {
		piece := content[:min(len(content), progressChunk)]
		armed := d.next()
		_ = d.controller.SetWriteDeadline(armed)
		n, err := writer.Write(piece)
		written += n
		if err != nil {
			d.observe(err, armed)
			return written, err
		}
		content = content[n:]
	}
	return written, nil
}

// flush sends the buffered response under a fresh write deadline, so the end
// of a response is bounded like the rest instead of being sent after the
// handler without one. A writer that cannot flush sends its response after
// the handler as before.
func (d *transferDeadlines) flush() error {
	armed := d.next()
	_ = d.controller.SetWriteDeadline(armed)
	err := d.controller.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	d.observe(err, armed)
	return err
}

// reason names the idle limit for the server log.
func (d *transferDeadlines) reason() string {
	return "no data moved for " + d.idle.String() + " (Git transfer idle limit)"
}

// networkBody is the request body read from the connection. Each Read waits
// for the client at most the idle limit. After the end of the body the read
// deadline returns to the operation deadline: net/http then keeps a
// background read pending to notice a disconnect, and that read must not end
// a request whose Git works quietly. (net/http also clears the deadline when
// it starts that read; setting it here keeps the bound explicit.)
//
// net/http's Close waits for a Read blocked on a stalled client and then
// reads up to 256 KiB more, so closing alone does not release an abandoned
// upload before the read deadline. When the operation was abandoned and the
// body is unfinished, Close first moves that deadline to now, which ends the
// blocked Read and the drain at once. The backend stream closes its input
// only after it has terminated the backend, so the backend never sees the cut
// input as a complete request.
//
// A backend that exits normally before reading the whole request is not an
// abandoned operation. Its Close keeps the idle limit for the rest of the
// body, so a client that stalls then is cut after the idle limit. Expiring
// the deadline at once would make net/http cancel the request context, which
// races with the stream's final wait and can turn a completed push into a
// cancelled one, and would cut a client that is still sending the rest.
type networkBody struct {
	io.ReadCloser
	abandoned func() bool
	deadlines *transferDeadlines
	ended     atomic.Bool
	// mu keeps a Read from arming a new deadline after Close expired it.
	mu      sync.Mutex
	expired bool
}

func (body *networkBody) Read(buffer []byte) (int, error) {
	body.mu.Lock()
	var armed time.Time
	if !body.expired {
		armed = body.deadlines.next()
		_ = body.deadlines.controller.SetReadDeadline(armed)
	}
	body.mu.Unlock()
	n, err := body.ReadCloser.Read(buffer)
	switch {
	case err == io.EOF:
		body.ended.Store(true)
		_ = body.deadlines.controller.SetReadDeadline(body.deadlines.overall)
	case err != nil:
		body.deadlines.observe(err, armed)
	}
	return n, err
}

func (body *networkBody) Close() error {
	body.mu.Lock()
	var armed time.Time
	if !body.ended.Load() {
		if body.abandoned() {
			body.expired = true
			_ = body.deadlines.controller.SetReadDeadline(time.Now())
		} else {
			armed = body.deadlines.next()
			_ = body.deadlines.controller.SetReadDeadline(armed)
		}
	}
	body.mu.Unlock()
	err := body.ReadCloser.Close()
	if !armed.IsZero() {
		body.deadlines.observe(err, armed)
		if err == nil {
			// The drain may have reached the end of the body, which starts
			// the background read.
			_ = body.deadlines.controller.SetReadDeadline(body.deadlines.overall)
		}
	}
	return err
}

package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"owngit/internal/logtext"
)

// Request deadlines
//
// Every application request starts under its page deadline: until then the
// connection may be read and written and the handler may work. Import runs
// and archive downloads take longer, but only a caller allowed to start one
// needs that time. Their handlers call beginOperation after they authorized
// the caller and read the request body, and only then do the connection and
// the work move to the route's operation deadline. A client that withholds
// its request body, or is refused, therefore holds the connection no longer
// than the page deadline, and so does the rest of a body the handler left
// unread, which net/http reads after the handler to reuse the connection.
//
// Work stops a reply reserve earlier, so a request that runs out of time
// can still get an error page. A fully rendered page starts a bounded-chunk
// transfer before its deadline, and each chunk has a fresh idle deadline.
// Reading the body and authentication never gain that extra time. A handler
// that already outlasted its deadline cannot start a transfer.

// requestDeadlinesKey finds a request's requestDeadlines in its context.
type requestDeadlinesKey struct{}

// requestDeadlines are the deadlines of one application request.
type requestDeadlines struct {
	controller *http.ResponseController
	body       *trackedBody
	// parent is the request context before any deadline, so the operation
	// context does not inherit the page deadline.
	parent context.Context
	// page bounds reading the request body, and everything else until an
	// operation begins.
	page time.Time
	// operation and reserve are the route's operation deadline and the part
	// of it kept for writing the response after the work deadline.
	operation time.Time
	reserve   time.Duration
	// current is the connection deadline in force.
	current time.Time
	// replyIdle bounds one page chunk, independently of total transfer time.
	replyIdle time.Duration
	// name is the request's method and escaped path, for the log. The
	// escaping keeps a decoded line break in the path out of the log.
	name string
	// cancel ends the operation context once an operation began.
	cancel context.CancelFunc
}

// trackedBody records whether the request body was read to its end.
type trackedBody struct {
	io.ReadCloser
	ended atomic.Bool
}

func (body *trackedBody) Read(content []byte) (int, error) {
	n, err := body.ReadCloser.Read(content)
	if errors.Is(err, io.EOF) {
		body.ended.Store(true)
	}
	return n, err
}

// startDeadlines installs the page deadline on the connection and returns the
// request to serve, whose context ends pageReserve before that deadline.
func startDeadlines(writer http.ResponseWriter, request *http.Request, pageTimeout, pageReserve, operationTimeout, operationReserve time.Duration) (*http.Request, *requestDeadlines, context.CancelFunc) {
	started := time.Now()
	body := &trackedBody{ReadCloser: request.Body}
	body.ended.Store(request.ContentLength == 0)
	if request.ContentLength != 0 {
		request.Body = body
	}
	// A route whose operation is shorter than a page keeps the shorter limit
	// for everything.
	page := started.Add(min(pageTimeout, operationTimeout))
	deadlines := &requestDeadlines{
		controller: http.NewResponseController(writer), body: body, parent: request.Context(),
		page: page, operation: started.Add(operationTimeout), reserve: operationReserve, current: page,
		name: logtext.Request(request), replyIdle: min(pageTimeout, operationTimeout),
	}
	_ = deadlines.controller.SetReadDeadline(page)
	_ = deadlines.controller.SetWriteDeadline(page)
	requestContext, cancel := context.WithDeadline(context.WithValue(request.Context(), requestDeadlinesKey{}, deadlines), page.Add(-pageReserve))
	return request.WithContext(requestContext), deadlines, cancel
}

// beginOperation moves an import run or an archive download from the page
// deadline to its route's operation deadline and returns the request to
// continue with, whose context has the operation's work deadline. Call it
// only after the caller is authorized and the request body is read. A body
// the handler left unread is not read any more: the reply closes the
// connection instead. Later calls change nothing.
func (app *App) beginOperation(writer http.ResponseWriter, request *http.Request) *http.Request {
	deadlines, _ := request.Context().Value(requestDeadlinesKey{}).(*requestDeadlines)
	if deadlines == nil || deadlines.cancel != nil {
		return request
	}
	deadlines.current = deadlines.operation
	if deadlines.body.ended.Load() {
		// net/http watches the connection for a client that goes away once
		// the body ended. A read deadline passing during the operation would
		// look like one and cancel the request.
		_ = deadlines.controller.SetReadDeadline(deadlines.operation)
	} else {
		_ = deadlines.controller.SetReadDeadline(time.Now())
		writer.Header().Set("Connection", "close")
	}
	_ = deadlines.controller.SetWriteDeadline(deadlines.operation)
	operationContext, cancel := context.WithDeadline(deadlines.parent, deadlines.operation.Add(-deadlines.reserve))
	deadlines.cancel = cancel
	request = request.WithContext(operationContext)
	if app.requestObserver != nil {
		app.requestObserver(request)
	}
	return request
}

// beginImportRun begins an import run whose own deadline is runTimeout. The
// operation keeps ImportResponseMargin beyond the run, as requestTimeout
// gives a run with the server's own run time, so a source with a longer or
// shorter run time keeps its request open for that run and no longer.
func (app *App) beginImportRun(writer http.ResponseWriter, request *http.Request, runTimeout time.Duration) *http.Request {
	if deadlines, _ := request.Context().Value(requestDeadlinesKey{}).(*requestDeadlines); deadlines != nil && deadlines.cancel == nil {
		deadlines.operation = time.Now().Add(ImportRunRequestTimeout(runTimeout))
	}
	return app.beginOperation(writer, request)
}

const pageReplyChunkBytes = 32 << 10

// writePage sends an already rendered body in bounded, flushed chunks. An
// expired work/operation connection deadline is never revived. Operations
// and backup transfers keep their own existing connection rules.
func writePage(writer http.ResponseWriter, request *http.Request, status int, content []byte) error {
	controller := http.NewResponseController(writer)
	deadlines, _ := request.Context().Value(requestDeadlinesKey{}).(*requestDeadlines)
	progress := deadlines != nil && deadlines.cancel == nil && time.Now().Before(deadlines.current)
	if progress {
		if deadlines.body.ended.Load() {
			// net/http's disconnect watcher must not mistake the old body-read
			// deadline for a disconnect while the reply is still progressing.
			_ = controller.SetReadDeadline(time.Time{})
		} else {
			writer.Header().Set("Connection", "close")
		}
	}
	writer.WriteHeader(status)
	for len(content) > 0 {
		if progress {
			deadlines.current = time.Now().Add(deadlines.replyIdle)
			if err := controller.SetWriteDeadline(deadlines.current); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
		}
		chunk := content[:min(len(content), pageReplyChunkBytes)]
		n, err := writer.Write(chunk)
		if err != nil {
			return err
		}
		if n != len(chunk) {
			return io.ErrShortWrite
		}
		if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		content = content[n:]
	}
	return nil
}

// finish runs after the handler returns. Without an operation, net/http may
// read the rest of a body the handler left unread, to reuse the connection,
// until the page deadline. Other deadlines are cleared while they have not
// passed, because a read deadline that passes while net/http watches the
// connection for a client that goes away would cancel the connection's later
// requests. Deadlines that passed stay, so the connection closes.
func (deadlines *requestDeadlines) finish() {
	began := deadlines.cancel != nil
	if began {
		deadlines.cancel()
	}
	// A backup transfer has no connection deadline (beginTransfer).
	live := deadlines.current.IsZero() || time.Now().Before(deadlines.current)
	if live {
		_ = deadlines.controller.SetWriteDeadline(time.Time{})
	} else {
		log.Printf("%s ended %s after its connection deadline; its reply may be missing or cut off", deadlines.name, time.Since(deadlines.current).Round(time.Millisecond))
	}
	ended := deadlines.body.ended.Load()
	switch {
	case !ended && !began:
		_ = deadlines.controller.SetReadDeadline(deadlines.page)
	case ended && live:
		_ = deadlines.controller.SetReadDeadline(time.Time{})
	}
}

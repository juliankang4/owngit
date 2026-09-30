package server

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Backup downloads and uploads can be as large as all repositories
// together, so they have no overall time limit. Once the caller is
// authorized, beginTransfer lifts the page deadline, and each read of the
// upload and each write of the download must move data within
// backupIdleLimit; the request's context ends when the client goes away.

// backupIdleLimit stops a backup transfer whose client moves no data for
// this long.
const backupIdleLimit = time.Minute

// beginTransfer moves an authorized backup transfer off the page deadline:
// the connection has no deadline of its own, the reads and writes arm
// theirs (idleReader, idleWriter), and the returned request's context ends
// only when the client goes away or the server stops. Unlike
// beginOperation, the rest of the request body is still read.
func (app *App) beginTransfer(writer http.ResponseWriter, request *http.Request) *http.Request {
	deadlines, _ := request.Context().Value(requestDeadlinesKey{}).(*requestDeadlines)
	if deadlines == nil || deadlines.cancel != nil {
		return request
	}
	deadlines.current = time.Time{}
	_ = deadlines.controller.SetReadDeadline(time.Time{})
	_ = deadlines.controller.SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithCancel(deadlines.parent)
	deadlines.cancel = cancel
	request = request.WithContext(ctx)
	if app.requestObserver != nil {
		app.requestObserver(request)
	}
	return request
}

// idleReader reads a request body, each read within backupIdleLimit.
type idleReader struct {
	reader     io.Reader
	controller *http.ResponseController
}

func (r idleReader) Read(content []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(backupIdleLimit))
	return r.reader.Read(content)
}

// idleWriter writes a response, each write within backupIdleLimit.
type idleWriter struct {
	writer     io.Writer
	controller *http.ResponseController
}

func (w idleWriter) Write(content []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(backupIdleLimit))
	return w.writer.Write(content)
}

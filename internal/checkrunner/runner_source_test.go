package checkrunner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// The runner fetches each source blob from the server and the materializer
// verifies its hash before writing it. A blob or manifest the server changed
// in transit stops the job before any check starts, in both object formats.
func TestExternalRunnerRefusesChangedSourceBeforeStart(t *testing.T) {
	manifestFormat := func(format string) func(http.Header, []byte) []byte {
		return func(_ http.Header, body []byte) []byte {
			var manifest map[string]any
			if json.Unmarshal(body, &manifest) != nil {
				return body
			}
			manifest["object_format"] = format
			changed, _ := json.Marshal(manifest)
			return changed
		}
	}
	cases := []struct {
		name, path string
		edit       func(http.Header, []byte) []byte
		cancel     bool
		status     string
		summary    string
	}{
		{name: "valid source", status: state.CheckJobPassed},
		{name: "same-size different bytes", path: "/files/", edit: func(_ http.Header, body []byte) []byte {
			return bytes.ToUpper(body)
		}, status: state.CheckJobUnavailable, summary: checksource.ErrObjectMismatch.Error()},
		{name: "wrong blob identity", path: "/files/", edit: func(header http.Header, body []byte) []byte {
			header.Set("X-OwnGit-Blob-OID", strings.Repeat("0", len(header.Get("X-OwnGit-Blob-OID"))))
			return body
		}, status: state.CheckJobUnavailable, summary: "identity or size"},
		{name: "wrong blob size", path: "/files/", edit: func(_ http.Header, body []byte) []byte {
			return body[:len(body)-1]
		}, status: state.CheckJobUnavailable, summary: "identity or size"},
		{name: "unsupported object format", path: "/source", edit: manifestFormat("md5"), status: state.CheckJobUnavailable, summary: checksource.ErrUnsupportedObjectFormat.Error()},
		{name: "cancelled during the final file", path: "/files/", cancel: true, status: state.CheckJobInterrupted},
	}
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		for _, c := range cases {
			t.Run(format+"/"+c.name, func(t *testing.T) {
				sentinel := filepath.Join(t.TempDir(), "command-ran")
				fixture := newRunnerIntegrationFixtureWithFormat(t, writeRunnerSentinelCommand(sentinel), format)
				path, err := fixture.manager.Path(fixture.repository.ID)
				noErr(t, err)
				if actual, err := fixture.manager.ObjectFormat(fixture.ctx, path); err != nil || actual != format {
					t.Fatalf("repository object format=%q err=%v, want %s", actual, err, format)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				var starts atomic.Int64
				httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						if strings.HasSuffix(request.URL.Path, "/start") {
							starts.Add(1)
						}
						if c.path == "" || !strings.Contains(request.URL.Path, c.path) {
							next.ServeHTTP(writer, request)
							return
						}
						if c.cancel {
							cancel()
						}
						recorder := httptest.NewRecorder()
						next.ServeHTTP(recorder, request)
						body := recorder.Body.Bytes()
						if c.edit != nil {
							body = c.edit(recorder.Header(), body)
						}
						for name, values := range recorder.Header() {
							writer.Header()[name] = values
						}
						writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
						writer.WriteHeader(recorder.Code)
						_, _ = writer.Write(body)
					})
				})
				defer httpServer.Close()
				runErr := fixture.runner(fixture.client(origin)).Run(ctx)
				job := fixture.readJob()
				if job.Status != c.status || !strings.Contains(job.Summary, c.summary) {
					t.Fatalf("job status=%s summary=%q run=%v, want %s with %q", job.Status, job.Summary, runErr, c.status, c.summary)
				}
				if c.status == state.CheckJobPassed {
					noErr(t, runErr)
					return
				}
				if starts.Load() != 0 || job.AttemptID != "" || job.StartedAt != nil {
					t.Fatalf("refused source started a check: starts=%d attempt=%t started=%t", starts.Load(), job.AttemptID != "", job.StartedAt != nil)
				}
				assertRunnerSentinelAbsent(t, sentinel)
			})
		}
	}
}

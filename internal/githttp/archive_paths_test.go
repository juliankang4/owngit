package githttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An archive of a tree that names one path twice would hold two entries with
// one name, and an unpacker would silently overwrite the first with the
// second, so the archive is refused before its response starts.
func TestArchiveRefusesATreeThatNamesOnePathTwice(t *testing.T) {
	handler, _, remote, _ := pushFixture(t)
	logs := captureLog(t)
	collision := collisionCommit(t, remote)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/repositories/sample/archive", nil)
	err := handler.ServeArchive(recorder, request, "sample", collision, ArchiveZip, "sample", "sample.zip")
	var failure *ArchiveError
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict {
		t.Fatalf("archive error=%v, want a %d refusal", err, http.StatusConflict)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("the refused archive wrote %d bytes", recorder.Body.Len())
	}
	if !strings.Contains(logs.String(), "one path twice") {
		t.Fatalf("the server log does not name the reason:\n%s", logs.String())
	}
}

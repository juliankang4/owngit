package importsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A readable marker in an unknown directory is not evidence that this service
// created the directory. Repeated reconciliation must not manufacture ownership.
func TestParentUnknownMarkerNeverBecomesCleanupAuthority(t *testing.T) {
	f := newFixture(t)
	id := strings.Repeat("a", 32)
	name := "run-" + id
	directory := filepath.Join(f.service.stagingRootPath(), name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := stagingMarker{Version: stagingMarkerVersion, Name: name, RunID: id,
		RepositoryID: "foreign", Token: strings.Repeat("b", 64), CreatedAt: f.now.Unix()}
	if err := writeStagingMarker(directory, marker); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "unrelated-content.txt")
	if err := os.WriteFile(path, []byte("preserve unrelated synthetic content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := f.store.ImportStaging(context.Background(), name); err != nil || exists {
		t.Fatalf("precondition: ownership row exists=%v error=%v", exists, err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := f.service.Reconcile(context.Background()); err != nil {
			t.Fatalf("reconcile pass %d: %v", pass, err)
		}
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "preserve unrelated synthetic content" {
			t.Fatalf("reconcile pass %d removed or changed unknown content: %v", pass, err)
		}
	}
}

//go:build !windows

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPinnedBlobPrefixLimitDoesNotClaimGitIOBound(t *testing.T) {
	manager, _, work := newTestRepository(t)
	const blobBytes = 2 << 20
	content := strings.Repeat("0123456789abcdef", blobBytes/16)
	commitFile(t, work, content, "large blob", "2024-01-01T00:00:00Z")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	pinned, err := manager.PinRepository(context.Background(), "sample", oid, oid)
	noErr(t, err)

	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	dir := t.TempDir()
	capture := filepath.Join(dir, "blob-output")
	marker := filepath.Join(dir, "producer-bytes")
	script := fmt.Sprintf(`#!/bin/sh
is_blob=false
previous=
for arg do
  if [ "$previous" = cat-file ] && [ "$arg" = blob ]; then
    is_blob=true
  fi
  previous=$arg
done
if [ "$is_blob" = true ]; then
  %s "$@" > %s
  status=$?
  wc -c < %s > %s
  cat %s
  exit $status
fi
exec %s "$@"
`, quote(manager.Git.GitPath), quote(capture), quote(capture), quote(marker), quote(capture), quote(manager.Git.GitPath))
	wrapper := filepath.Join(dir, "git-fixture")
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	manager.Git.GitPath = wrapper

	blob, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 0, 4096, 16, 32)
	noErr(t, err)
	producedRaw, err := os.ReadFile(marker)
	noErr(t, err)
	produced, err := strconv.Atoi(strings.TrimSpace(string(producedRaw)))
	noErr(t, err)
	if len(blob.Content) != 16 || string(blob.Content) != content[:16] || blob.Size != blobBytes || !blob.HasMore {
		t.Fatalf("returned blob chunk=%+v", blob)
	}
	if produced != blobBytes || produced <= len(blob.Content) {
		t.Fatalf("Git producer bytes=%d returned prefix=%d", produced, len(blob.Content))
	}
}

func TestPinnedChangeRejectsLimitPlusExecutionFailure(t *testing.T) {
	for _, mode := range []string{"exit failure", "runner timeout"} {
		t.Run(mode, func(t *testing.T) {
			manager, _, work := newTestRepository(t)
			commitFile(t, work, "base\n", "base", "2024-01-01T00:00:00Z")
			base := gitOutput(t, work, "rev-parse", "HEAD")
			commitFile(t, work, "head\n", "head", "2024-01-02T00:00:00Z")
			head := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
			pinned, err := manager.PinRepository(context.Background(), "sample", base, head)
			noErr(t, err)
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
			dir := t.TempDir()
			marker := filepath.Join(dir, "emitted")
			ending := "exit 2"
			if mode == "runner timeout" {
				ending = "exec /bin/sleep 5"
				manager.Git.Timeout = time.Second
				manager.Git.TerminationGrace = 20 * time.Millisecond
			}
			script := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = diff ]; then\n  printf '0123456789abcdef0123456789abcdef'\n  printf emitted > %s\n  %s\n fi\ndone\nexec %s \"$@\"\n", quote(marker), ending, quote(manager.Git.GitPath))
			wrapper := filepath.Join(dir, "git-fixture")
			noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
			manager.Git.GitPath = wrapper
			change, err := pinned.ReadChange(context.Background(), 8)
			if _, markerErr := os.Stat(marker); markerErr != nil {
				t.Fatalf("fault fixture did not emit output: %v", markerErr)
			}
			if err == nil {
				t.Fatalf("Git execution failure was accepted as a successful truncated diff (truncated=%v)", change.Truncated)
			}
			if mode == "runner timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("runner timeout lost its cause: %v", err)
			}
		})
	}
}

package checksource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// assertUntouchedWorkspaceRoot checks that a refused root kept its mode and
// received no marker, lock or job envelope.
func assertUntouchedWorkspaceRoot(t *testing.T, root string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != mode {
		t.Fatalf("refused root mode changed from %v to %v", mode, info.Mode())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused root has %d entries err=%v, want none", len(entries), err)
	}
}

// useWorkspaceOwnerAnswers makes successive owner checks return answers in
// order, so a test can present a root owned by another account without
// running as root.
func useWorkspaceOwnerAnswers(t *testing.T, answers ...bool) *int {
	t.Helper()
	calls := 0
	original := ownedByCurrentUser
	ownedByCurrentUser = func(*os.File) (bool, error) {
		answer := answers[min(calls, len(answers)-1)]
		calls++
		return answer, nil
	}
	t.Cleanup(func() { ownedByCurrentUser = original })
	return &calls
}

func TestAcquireWorkspaceRootRefusesRootOwnedByAnotherAccount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	noErr(t, os.Mkdir(root, 0o755))
	info, err := os.Stat(root)
	noErr(t, err)
	useWorkspaceOwnerAnswers(t, false)

	workspace, err := AcquireWorkspaceRoot(root)
	var unsafe *UnsafeWorkspaceRootError
	if !errors.As(err, &unsafe) || unsafe.Root != root || unsafe.Directory != "" {
		workspace.Close()
		t.Fatalf("acquire err=%v, want a refusal for a root owned by another account", err)
	}
	assertUntouchedWorkspaceRoot(t, root, info.Mode())
	if OwnsWorkspaceDirectory(root) {
		t.Fatal("OwnsWorkspaceDirectory accepted a root owned by another account")
	}
}

func TestAcquireWorkspaceRootChecksOwnerAgainAfterProtectingIt(t *testing.T) {
	root := t.TempDir()
	calls := useWorkspaceOwnerAnswers(t, true, false)

	workspace, err := AcquireWorkspaceRoot(root)
	var unsafe *UnsafeWorkspaceRootError
	if !errors.As(err, &unsafe) || *calls != 2 {
		workspace.Close()
		t.Fatalf("acquire err=%v after %d owner checks, want a refusal from the second check", err, *calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused root has %d entries err=%v, want no marker or lock", len(entries), err)
	}
}

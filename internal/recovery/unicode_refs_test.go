package recovery

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRestoresRefsWithUnicodeWhitespace(t *testing.T) {
	for _, name := range []string{"refs/heads/ending\u00a0", "refs/heads/topic\u2003next", "refs/heads/line\u2028name"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			remote, err := manager.Path("project")
			noErr(t, err)
			oid := gitOutput(t, remote, "--git-dir", ".", "rev-parse", "refs/heads/main")
			runGit(t, remote, "--git-dir", ".", "update-ref", name, oid)
			backup := filepath.Join(root, "backup")
			_, err = CreateWithReport(context.Background(), store, manager, backup)
			noErr(t, err)
			restored := filepath.Join(root, "restored-repositories")
			if _, err := RestoreWithReport(context.Background(), backup, filepath.Join(root, "restored-state"), restored, ""); err != nil {
				t.Fatal(err)
			}
			assertRef(t, filepath.Join(restored, "project.git"), name, oid)
		})
	}
}

func TestBundleHeadsPreserveNamesAndValidateOIDs(t *testing.T) {
	sha1, sha256 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	name := "refs/heads/topic\u2003next\u00a0"
	refs, head, err := parseBundleHeads([]byte(sha1 + " " + name + "\n" + sha256 + " HEAD\n"))
	noErr(t, err)
	if len(refs) != 1 || refs[0] != (Ref{Name: name, OID: sha1}) || head != sha256 {
		t.Fatalf("bundle heads changed: %+v HEAD=%q", refs, head)
	}
	// Only LF separates records; other bytes remain in the ref for the
	// subsequent comparison with the validated manifest.
	refs, _, err = parseBundleHeads([]byte(sha1 + " refs/heads/main\r\n"))
	noErr(t, err)
	if refs[0].Name != "refs/heads/main\r" {
		t.Fatalf("bundle parser removed a byte other than LF: %q", refs[0].Name)
	}
	for _, line := range []string{strings.Repeat("a", 39) + " refs/heads/main\n", strings.Repeat("g", 40) + " refs/heads/main\n", sha1 + "\n", sha1 + " \n"} {
		if _, _, err := parseBundleHeads([]byte(line)); err == nil {
			t.Fatalf("malformed bundle head was accepted: %q", line)
		}
	}
}

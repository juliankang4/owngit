package checksource

import (
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func TestScrubActionsPayloads(t *testing.T) {
	for _, kind := range []string{"owned", "unowned", "linked actions", "JSON envelope"} {
		t.Run(kind, func(t *testing.T) {
			root, err := AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "jobs"))
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			id, err := state.RandomID()
			if err != nil {
				t.Fatal(err)
			}
			envelope, _, err := root.PrepareJob(id)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unowned" {
				if err := os.Remove(filepath.Join(envelope, workspaceJobMarker)); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "linked actions" {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(envelope, "actions")); err != nil {
					t.Fatal(err)
				}
			}
			payload := filepath.Join(envelope, "actions", "scripts", "step-1.script")
			if kind == "JSON envelope" {
				payload = filepath.Join(envelope, "check.script")
			}
			if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(payload, []byte("synthetic-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			count, more, err := root.ScrubActionsPayloads(1)
			preserved := kind != "owned"
			if more || (err != nil) != (kind == "unowned" || kind == "linked actions") {
				t.Fatalf("scrub count=%d more=%v err=%v", count, more, err)
			}
			_, statErr := os.Stat(payload)
			if preserved && statErr != nil || !preserved && !os.IsNotExist(statErr) {
				t.Fatalf("payload preservation: %v", statErr)
			}
			if _, err := os.Stat(envelope); err != nil {
				t.Fatalf("envelope lost: %v", err)
			}
		})
	}
}

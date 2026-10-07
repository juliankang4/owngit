package recovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/repository"
)

// A SHA-256 repository, with history or empty, restores as a SHA-256
// repository with the same refs, and its backup passes verification. The
// empty one needs format 11, which records its object format.
func TestSHA256RepositoriesRestoreAndVerify(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	for _, name := range []string{"modern", "modern-empty"} {
		if _, err := manager.CreateWithOptions(ctx, name, "", repository.CreateOptions{ObjectFormat: repository.ObjectFormatSHA256}); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(root, "modern-work")
	runGit(t, "", "init", "--object-format=sha256", "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Backup Test")
	runGit(t, work, "config", "user.email", "backup@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file"), []byte("data"), 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "data")
	modern, err := manager.Path("modern")
	noErr(t, err)
	runGit(t, work, "push", modern, "HEAD:refs/heads/main")
	tip := strings.TrimSpace(gitOutput(t, work, "rev-parse", "HEAD"))

	backup := filepath.Join(root, "backup")
	_, err = CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != backupVersion {
		t.Fatalf("version=%d, want %d for an empty SHA-256 repository", manifest.Version, backupVersion)
	}
	for _, item := range manifest.Repositories {
		want := map[string]string{"modern-empty": "sha256"}[item.ID]
		if item.ObjectFormat != want {
			t.Fatalf("%s records object format %q, want %q", item.ID, item.ObjectFormat, want)
		}
	}

	for name, edit := range map[string]func(*Manifest){
		"a format on a repository with a bundle": func(m *Manifest) { m.Repositories[indexOf(m, "modern")].ObjectFormat = "sha256" },
		"an unknown format":                      func(m *Manifest) { m.Repositories[indexOf(m, "modern-empty")].ObjectFormat = "sha512" },
		"version 10":                             func(m *Manifest) { m.Version = closedPullRequestBackupVersion },
	} {
		changed := manifest
		changed.Repositories = append([]RepositoryManifest(nil), manifest.Repositories...)
		edit(&changed)
		if err := validateManifest(changed); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	result, err := Verify(ctx, backup, temporary, "")
	noErr(t, err)
	if !result.Verified {
		t.Fatalf("result=%+v", result)
	}

	restoredRoot := filepath.Join(root, "restored-repositories")
	noErr(t, Restore(ctx, backup, filepath.Join(root, "restored-state"), restoredRoot, ""))
	for id, wantTip := range map[string]string{"modern": tip, "modern-empty": "", "project": ""} {
		path := filepath.Join(restoredRoot, id+".git")
		format := strings.TrimSpace(gitOutput(t, path, "rev-parse", "--show-object-format"))
		want := "sha256"
		if id == "project" {
			want = "sha1"
		}
		if format != want {
			t.Fatalf("restored %s uses %s, want %s", id, format, want)
		}
		if wantTip != "" {
			if got := strings.TrimSpace(gitOutput(t, path, "rev-parse", "refs/heads/main")); got != wantTip {
				t.Fatalf("restored %s main=%s, want %s", id, got, wantTip)
			}
		}
	}
}

func indexOf(manifest *Manifest, id string) int {
	for index, item := range manifest.Repositories {
		if item.ID == id {
			return index
		}
	}
	panic("no repository " + id)
}

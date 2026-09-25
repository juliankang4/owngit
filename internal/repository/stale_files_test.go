package repository

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Preparation removes Git's temporary pack files and the maintenance lock
// files that an interrupted command left before OwnGit started, and nothing
// else: newer files may belong to a running import, and other names are not
// Git's temporary files or not maintenance locks.
func TestPreparationRemovesStaleGitFilesFromBeforeTheStart(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	old := processStart.Add(-time.Hour)
	write := func(relative string, modified time.Time) string {
		t.Helper()
		path := filepath.Join(remote, filepath.FromSlash(relative))
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
		noErr(t, os.WriteFile(path, []byte("synthetic\n"), 0o600))
		noErr(t, os.Chtimes(path, modified, modified))
		return path
	}
	stale := []string{
		"objects/pack/tmp_pack_XwZc1J",
		"objects/pack/tmp_idx_jY22jX",
		"objects/pack/tmp_rev_a1B2c3",
		"objects/pack/.tmp-87266-pack-988a612729762639a3086bf0e9b758d7fc8a3ca4.pack",
		"objects/pack/.tmp-87266-pack-988a612729762639a3086bf0e9b758d7fc8a3ca4.rev",
		"packed-refs.lock",
		"objects/info/commit-graphs/commit-graph-chain.lock",
	}
	for _, name := range stale {
		write(name, old)
	}
	kept := []string{
		"objects/pack/tmp_pack_Recent",
		"objects/pack/pack-988a612729762639a3086bf0e9b758d7fc8a3ca4.keep",
		"objects/pack/tmp_pack_toolong1",
		"objects/pack/notes.txt",
		"refs/heads/main.lock",
		"index.lock",
		"objects/info/commit-graphs/tmp_graph_abcdef",
	}
	write(kept[0], time.Now().Add(time.Minute))
	for _, name := range kept[1:] {
		write(name, old)
	}
	// A link is never followed or removed, whatever its name.
	target := write("outside-target", old)
	link := filepath.Join(remote, "objects", "pack", "tmp_pack_Link01")
	noErr(t, os.Symlink(target, link))
	kept = append(kept, "objects/pack/tmp_pack_Link01", "outside-target")

	log := &maintenanceLog{}
	noErr(t, manager.StartPreparation(context.Background(), nil, 10*time.Second, log.logf))
	t.Cleanup(func() { _ = manager.StopPreparation(context.Background()) })
	waitFor(t, "preparation", func() bool { return !manager.Preparing("sample") })

	for _, name := range stale {
		if _, err := os.Lstat(filepath.Join(remote, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf("stale %s remains: %v", name, err)
		}
	}
	for _, name := range kept {
		if _, err := os.Lstat(filepath.Join(remote, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	lines := log.matching("removed files left by a Git command")
	if len(lines) != 1 || !slices.ContainsFunc(stale, func(name string) bool { return strings.Contains(lines[0], name) }) {
		t.Fatalf("removal log %v", lines)
	}
	if failures := log.matching("could not"); len(failures) != 0 {
		t.Fatalf("unexpected failures %v", failures)
	}
}

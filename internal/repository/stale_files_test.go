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

// Preparation removes the quarantine directory of a push whose receive-pack
// was killed before OwnGit started, with the read-only objects in it. It
// keeps a quarantine directory with anything changed since the start, other
// temporary object directories, and anything that is not a directory, and it
// never follows a link.
func TestPreparationRemovesStalePushQuarantines(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	old := processStart.Add(-time.Hour)
	objects := filepath.Join(remote, "objects")
	write := func(relative string, modified time.Time, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(objects, filepath.FromSlash(relative))
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
		noErr(t, os.WriteFile(path, []byte("synthetic object\n"), 0o600))
		noErr(t, os.Chmod(path, mode))
		noErr(t, os.Chtimes(path, modified, modified))
	}
	touch := func(relative string, modified time.Time) {
		t.Helper()
		noErr(t, os.Chtimes(filepath.Join(objects, filepath.FromSlash(relative)), modified, modified))
	}
	quarantine := func(name string, objectTime time.Time) {
		t.Helper()
		write(name+"/4b/825dc642cb6eb9a060e54bf8d69288fbee4904", objectTime, 0o444)
		noErr(t, os.MkdirAll(filepath.Join(objects, name, "pack"), 0o700))
		for _, directory := range []string{name + "/4b", name + "/pack", name} {
			touch(directory, old)
		}
	}
	quarantine("tmp_objdir-incoming-hNElv0", old)
	quarantine("tmp_objdir-incoming-Recent", time.Now().Add(time.Minute))
	quarantine("tmp_objdir-bulk-fsync-abcdef", old)
	quarantine("tmp_objdir-incoming-toolong1", old)
	write("tmp_objdir-incoming-File01", old, 0o600)
	outside := filepath.Join(t.TempDir(), "outside")
	noErr(t, os.MkdirAll(outside, 0o700))
	noErr(t, os.WriteFile(filepath.Join(outside, "kept"), []byte("outside\n"), 0o600))
	link := filepath.Join(objects, "tmp_objdir-incoming-Link01")
	noErr(t, os.Symlink(outside, link))

	log := &maintenanceLog{}
	noErr(t, manager.StartPreparation(context.Background(), nil, 10*time.Second, log.logf))
	t.Cleanup(func() { _ = manager.StopPreparation(context.Background()) })
	waitFor(t, "preparation", func() bool { return !manager.Preparing("sample") })

	if _, err := os.Lstat(filepath.Join(objects, "tmp_objdir-incoming-hNElv0")); !os.IsNotExist(err) {
		t.Errorf("stale quarantine remains: %v", err)
	}
	for _, name := range []string{
		"tmp_objdir-incoming-Recent/4b/825dc642cb6eb9a060e54bf8d69288fbee4904",
		"tmp_objdir-bulk-fsync-abcdef/4b/825dc642cb6eb9a060e54bf8d69288fbee4904",
		"tmp_objdir-incoming-toolong1/4b/825dc642cb6eb9a060e54bf8d69288fbee4904",
		"tmp_objdir-incoming-File01",
		"tmp_objdir-incoming-Link01",
	} {
		if _, err := os.Lstat(filepath.Join(objects, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "kept")); err != nil {
		t.Errorf("a link was followed: %v", err)
	}
	lines := log.matching("removed files left by a Git command")
	if len(lines) != 1 || !strings.Contains(lines[0], "objects/tmp_objdir-incoming-hNElv0 (directory, 17 bytes)") {
		t.Fatalf("removal log %v", lines)
	}
	if failures := log.matching("could not"); len(failures) != 0 {
		t.Fatalf("unexpected failures %v", failures)
	}
}

// IncomingQuarantines lists only receive-pack's quarantine directories, and
// RemoveIncomingQuarantine removes only such a directory, never a link.
func TestIncomingQuarantinesListAndRemoveOnlyPushQuarantines(t *testing.T) {
	remote := t.TempDir()
	objects := filepath.Join(remote, "objects")
	for _, name := range []string{"tmp_objdir-incoming-Abc123/pack", "tmp_objdir-bulk-fsync-abcdef", "tmp_objdir-incoming-toolong1", "pack"} {
		noErr(t, os.MkdirAll(filepath.Join(objects, filepath.FromSlash(name)), 0o700))
	}
	object := filepath.Join(objects, "tmp_objdir-incoming-Abc123", "pack", "tmp_pack_Xyz789")
	noErr(t, os.WriteFile(object, []byte("synthetic pack\n"), 0o444))
	noErr(t, os.WriteFile(filepath.Join(objects, "tmp_objdir-incoming-File01"), nil, 0o600))
	outside := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(outside, "kept"), []byte("outside\n"), 0o600))
	noErr(t, os.Symlink(outside, filepath.Join(objects, "tmp_objdir-incoming-Link01")))

	names, err := IncomingQuarantines(remote)
	noErr(t, err)
	if !slices.Equal(names, []string{"tmp_objdir-incoming-Abc123"}) {
		t.Fatalf("listed %q", names)
	}
	for _, name := range []string{"tmp_objdir-incoming-Link01", "tmp_objdir-incoming-File01", "tmp_objdir-bulk-fsync-abcdef", "../outside"} {
		if _, err := RemoveIncomingQuarantine(remote, name); err == nil {
			t.Errorf("removed %s", name)
		}
	}
	size, err := RemoveIncomingQuarantine(remote, "tmp_objdir-incoming-Abc123")
	noErr(t, err)
	if size != int64(len("synthetic pack\n")) {
		t.Errorf("reported %d bytes", size)
	}
	for _, kept := range []string{filepath.Join(outside, "kept"), filepath.Join(objects, "tmp_objdir-incoming-Link01"), filepath.Join(objects, "tmp_objdir-bulk-fsync-abcdef")} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s is gone: %v", kept, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(objects, "tmp_objdir-incoming-Abc123")); !os.IsNotExist(err) {
		t.Errorf("the quarantine remains: %v", err)
	}
	if names, err := IncomingQuarantines(t.TempDir()); err != nil || len(names) != 0 {
		t.Errorf("a repository without objects: %q, %v", names, err)
	}
}

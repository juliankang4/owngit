package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup written as an archive unpacks into a folder that verifies and
// holds the same files.
func TestBackupArchiveRoundTrip(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	noErr(t, os.WriteFile(filepath.Join(backup, "notes.txt"), []byte("left out"), 0o600))
	folder, err := OpenBackupFolder(root)
	noErr(t, err)
	defer folder.Close()
	opened, err := folder.Open(filepath.Base(backup))
	noErr(t, err)
	defer opened.Close()
	var archive bytes.Buffer
	noErr(t, opened.WriteArchive(context.Background(), &archive))

	unpacked := filepath.Join(root, "unpacked")
	noErr(t, os.Mkdir(unpacked, 0o700))
	name, err := UnpackArchive(context.Background(), bytes.NewReader(archive.Bytes()), unpacked)
	noErr(t, err)
	if name != filepath.Base(backup) {
		t.Fatalf("top folder %q", name)
	}
	for _, file := range []string{manifestName, filepath.Join("repositories", "project.bundle"), filepath.Join("repositories", "second.bundle")} {
		original, err := os.ReadFile(filepath.Join(backup, file))
		noErr(t, err)
		copied, err := os.ReadFile(filepath.Join(unpacked, name, file))
		noErr(t, err)
		if !bytes.Equal(original, copied) {
			t.Fatalf("%s differs after the round trip", file)
		}
	}
	if _, err := os.Lstat(filepath.Join(unpacked, name, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file the backup did not write was archived: %v", err)
	}
	result, err := Verify(context.Background(), filepath.Join(unpacked, name), filepath.Join(root, "rehearsal"), "")
	noErr(t, err)
	if !result.Verified {
		t.Fatalf("the unpacked backup did not verify: %+v", result)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := opened.WriteArchive(cancelled, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled archive: %v", err)
	}
}

type entry struct {
	name     string
	kind     byte
	content  string
	linkname string
}

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, item := range entries {
		header := &tar.Header{Name: item.name, Typeflag: item.kind, Mode: 0o600, Size: int64(len(item.content)), Linkname: item.linkname}
		if item.kind != tar.TypeReg {
			header.Size = 0
		}
		noErr(t, archive.WriteHeader(header))
		if item.kind == tar.TypeReg {
			_, err := archive.Write([]byte(item.content))
			noErr(t, err)
		}
	}
	noErr(t, archive.Close())
	return buffer.Bytes()
}

// Only what a backup holds is unpacked, and only inside the folder.
func TestUnpackArchiveRefusesWhatABackupDoesNotHold(t *testing.T) {
	dir, repos := entry{name: "b/", kind: tar.TypeDir}, entry{name: "b/repositories/", kind: tar.TypeDir}
	manifest := entry{name: "b/manifest.json", kind: tar.TypeReg, content: "{}"}
	bundle := entry{name: "b/repositories/one.bundle", kind: tar.TypeReg, content: strings.Repeat("bundle", 400)}
	good := tarOf(t, dir, manifest, repos, bundle)
	cases := map[string][]byte{
		// Cut inside the bundle. A cut between two entries leaves an
		// archive that tar cannot tell from a shorter one; the
		// verification then misses the bundles the manifest names.
		"truncated":        good[:3000],
		"parent element":   tarOf(t, dir, manifest, repos, entry{name: "b/repositories/../../escape.bundle", kind: tar.TypeReg, content: "x"}),
		"leading parent":   tarOf(t, entry{name: "../b/", kind: tar.TypeDir}),
		"absolute path":    tarOf(t, entry{name: "/b/", kind: tar.TypeDir}),
		"symbolic link":    tarOf(t, dir, manifest, repos, entry{name: "b/repositories/one.bundle", kind: tar.TypeSymlink, linkname: "/etc/passwd"}),
		"hard link":        tarOf(t, dir, manifest, repos, entry{name: "b/repositories/one.bundle", kind: tar.TypeLink, linkname: "b/manifest.json"}),
		"extra file":       tarOf(t, dir, manifest, entry{name: "b/notes.txt", kind: tar.TypeReg, content: "x"}),
		"extra folder":     tarOf(t, dir, manifest, entry{name: "b/more/", kind: tar.TypeDir}),
		"two top folders":  tarOf(t, dir, manifest, entry{name: "c/", kind: tar.TypeDir}),
		"same file twice":  tarOf(t, dir, manifest, entry{name: "b/manifest.json", kind: tar.TypeReg, content: "{}"}),
		"no manifest":      tarOf(t, dir, repos, bundle),
		"file before dir":  tarOf(t, manifest),
		"current element":  tarOf(t, dir, entry{name: "b/./manifest.json", kind: tar.TypeReg, content: "{}"}),
		"backslash":        tarOf(t, dir, entry{name: `b\manifest.json`, kind: tar.TypeReg, content: "{}"}),
		"not a tar stream": []byte(strings.Repeat("not a tar archive ", 100)),
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			target := t.TempDir()
			if _, err := UnpackArchive(context.Background(), bytes.NewReader(archive), target); !errors.Is(err, ErrNotABackupArchive) {
				t.Fatalf("err=%v", err)
			}
			if entries, err := os.ReadDir(filepath.Dir(target)); err != nil || len(entries) != 1 {
				t.Fatalf("something was written beside the folder: %v %v", entries, err)
			}
		})
	}
	target := t.TempDir()
	name, err := UnpackArchive(context.Background(), bytes.NewReader(good), target)
	noErr(t, err)
	if name != "b" {
		t.Fatalf("name %q", name)
	}
}

// A folder whose file system renames without replacing has no restore
// limit, and the test leaves nothing there.
func TestRestoreLimitOfAnOrdinaryFolder(t *testing.T) {
	dir := t.TempDir()
	fileSystem, err := RestoreLimit(dir)
	if err != nil || fileSystem != "" {
		t.Fatalf("restore limit: %q %v", fileSystem, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("the test left %v %v", entries, err)
	}
}

package repository

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRepeatedTreePathReadsRecordsOfAFullListing(t *testing.T) {
	for _, test := range []struct {
		name    string
		listing string
		want    bool
	}{
		{name: "one record per path", listing: "100644 blob aaa\tone\x00100644 blob bbb\ttwo\x00"},
		{name: "empty listing", listing: ""},
		{name: "record without a path", listing: "junk\x00"},
		{name: "one path twice", listing: "100644 blob aaa\tnode\x00100644 blob bbb\tnode\x00", want: true},
		{name: "one path twice after other paths", listing: "100644 blob aaa\tone\x00100644 blob bbb\ttwo\x00100644 blob ccc\tone\x00", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := repeatedTreePath([]byte(test.listing))
			if got := err != nil; got != test.want {
				t.Fatalf("repeatedTreePath(%q)=%v, want a report=%v", test.listing, err, test.want)
			}
		})
	}
}

// storedTreeEntry is one record of a tree object written literally.
type storedTreeEntry struct{ mode, name, oid string }

// storedTree writes a tree object with the entries as given and returns its ID.
// A name that already contains the separator, two entries of one name, and a
// file beside a directory of the same path are the shapes that make two tree
// entries flatten to one path; Git warns about the first and refuses the
// others, so only a repository written before the receive-side object check
// holds one, and the fixture writes the objects directly.
func storedTree(t *testing.T, directory string, entries ...storedTreeEntry) string {
	t.Helper()
	var raw bytes.Buffer
	for _, entry := range entries {
		raw.WriteString(entry.mode + " " + entry.name)
		raw.WriteByte(0)
		decoded, err := hex.DecodeString(entry.oid)
		noErr(t, err)
		raw.Write(decoded)
	}
	return gitInputOutput(t, directory, raw.Bytes(), "hash-object", "--literally", "-t", "tree", "-w", "--stdin")
}

// storedTreeCommit writes a commit whose root tree holds the entries as given.
func storedTreeCommit(t *testing.T, directory string, entries ...storedTreeEntry) string {
	t.Helper()
	return gitInputOutput(t, directory, []byte("stored collision\n"), "-c", "user.name=Stored Tree",
		"-c", "user.email=stored@example.invalid", "commit-tree", storedTree(t, directory, entries...))
}

// storedTreeReading is one reader applied to a stored commit, with the text its
// refusal must hold.
type storedTreeReading struct {
	name string
	want string
	read func() error
}

// A stored tree can name one path twice in three shapes: an entry whose name
// already contains the separator, a file beside a directory of the same path,
// and one directory stored twice. Every reader that maps a flattened path
// refuses such a tree instead of picking one entry, merging line counts,
// listing the path twice, or copying over one of them. A repository without
// such a tree keeps reading.
func TestReadersRefuseAStoredTreeThatNamesOnePathTwice(t *testing.T) {
	ctx := context.Background()
	manager, remote, _ := newTestRepository(t)
	repositoryPath, err := manager.Path("sample")
	noErr(t, err)
	// The leaf shape comes from the restore fixture.
	valid, leafPath := duplicateFlattenedPathCommits(t, remote)
	blob := gitInputOutput(t, remote, []byte("stored data\n"), "hash-object", "-w", "--stdin")
	inner := gitInputOutput(t, remote, []byte("100644 blob "+blob+"\tb\n"), "mktree")
	fileAndDirectory := storedTreeCommit(t, remote,
		storedTreeEntry{mode: "100644", name: "a", oid: blob},
		storedTreeEntry{mode: "40000", name: "a", oid: inner})
	left := gitInputOutput(t, remote, []byte("100644 blob "+blob+"\tleft\n"), "mktree")
	right := gitInputOutput(t, remote, []byte("100644 blob "+blob+"\tright\n"), "mktree")
	twoDirectories := storedTreeCommit(t, remote,
		storedTreeEntry{mode: "40000", name: "a", oid: left},
		storedTreeEntry{mode: "40000", name: "a", oid: right})
	// The same collision one level down, under a folder that is itself sound.
	nested := storedTreeCommit(t, remote, storedTreeEntry{mode: "40000", name: "p", oid: storedTree(t, remote,
		storedTreeEntry{mode: "40000", name: "a", oid: left},
		storedTreeEntry{mode: "40000", name: "a", oid: right})})
	// A folder name is a byte string, so it can hold a line feed or a carriage
	// return. The pathspec keeps such a name literal, and the listing names the
	// records of its levels by the path's own bytes, so the readers refuse the
	// collision below it like any other.
	twoDirectoriesUnder := func(name string) string {
		return storedTreeCommit(t, remote, storedTreeEntry{mode: "40000", name: name, oid: storedTree(t, remote,
			storedTreeEntry{mode: "40000", name: "a", oid: left},
			storedTreeEntry{mode: "40000", name: "a", oid: right})})
	}
	lineFeedAncestor := twoDirectoriesUnder("p\nq")
	carriageReturnAncestor := twoDirectoriesUnder("p\r")
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", valid)
	if listing := gitOutput(t, remote, "--git-dir", ".", "ls-tree", "-r", "--full-tree", leafPath); strings.Count(listing, "\ta/b/c") != 2 {
		t.Fatalf("the restore fixture no longer lists a/b/c twice:\n%s", listing)
	}

	verifications := func(commit string) []storedTreeReading {
		return []storedTreeReading{
			{name: "full tree check", want: ErrRepeatedTreePath.Error(), read: func() error {
				return manager.VerifyTreePaths(ctx, repositoryPath, commit, 1<<20)
			}},
			{name: "language count", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.Languages(ctx, "sample", commit)
				return err
			}},
			{name: "pinned recursive listing", want: ErrRepeatedTreePath.Error(), read: func() error {
				pinned, err := manager.PinRepository(ctx, "sample", commit, commit)
				if err != nil {
					return err
				}
				_, err = pinned.ListTreeRecursive(ctx, PinnedHead, 1<<20)
				return err
			}},
		}
	}
	// Opening the colliding path itself must refuse too: the children of both
	// trees, or the file and the folder, form one increasing listing, so only
	// the levels above the request show that one path holds two entries.
	directReadings := func(commit, path string) []storedTreeReading {
		return []storedTreeReading{
			{name: "direct folder listing", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.TreeAt(ctx, "sample", commit, path)
				return err
			}},
			{name: "direct path view", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.PathAt(ctx, "sample", commit, path)
				return err
			}},
			{name: "direct folder page", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.TreePageAt(ctx, "sample", commit, path, "")
				return err
			}},
			{name: "direct path page", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, _, err := manager.PathPageAt(ctx, "sample", commit, path, "")
				return err
			}},
		}
	}
	for _, stored := range []struct {
		name   string
		commit string
		path   string
		extra  []storedTreeReading
	}{
		{name: "leaf path", commit: leafPath, path: "a/b", extra: []storedTreeReading{
			{name: "path view", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.PathAt(ctx, "sample", leafPath, "a/b/c")
				return err
			}},
			{name: "path page", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, _, err := manager.PathPageAt(ctx, "sample", leafPath, "a/b/c", "")
				return err
			}},
			{name: "commit files", want: "Git returned malformed changed-file records", read: func() error {
				_, _, err := manager.CommitFiles(ctx, "sample", leafPath)
				return err
			}},
		}},
		// A listing of a folder that holds both entries names the path twice,
		// so even the page that would show them as one folder refuses.
		{name: "file and directory of one path", commit: fileAndDirectory, path: "a", extra: []storedTreeReading{
			{name: "root folder page", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.TreePageAt(ctx, "sample", fileAndDirectory, "", "")
				return err
			}},
		}},
		{name: "two directories of one path", commit: twoDirectories, path: "a", extra: []storedTreeReading{
			{name: "root folder page", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.TreePageAt(ctx, "sample", twoDirectories, "", "")
				return err
			}},
		}},
		{name: "nested directories of one path", commit: nested, path: "p/a", extra: []storedTreeReading{
			{name: "folder above the collision", want: ErrRepeatedTreePath.Error(), read: func() error {
				_, err := manager.TreePageAt(ctx, "sample", nested, "p", "")
				return err
			}},
		}},
		{name: "ancestor name with a line feed", commit: lineFeedAncestor, path: "p\nq/a"},
		{name: "ancestor name with a carriage return", commit: carriageReturnAncestor, path: "p\r/a"},
	} {
		t.Run(stored.name, func(t *testing.T) {
			readings := append(verifications(stored.commit), stored.extra...)
			readings = append(readings, directReadings(stored.commit, stored.path)...)
			for _, reading := range readings {
				if err := reading.read(); err == nil || !strings.Contains(err.Error(), reading.want) {
					t.Errorf("%s: error=%v, want %q", reading.name, err, reading.want)
				}
			}
		})
	}

	// A valid nested folder still reads: every level holds its component once.
	if entries, err := manager.TreeAt(ctx, "sample", storedTreeCommit(t, remote, storedTreeEntry{mode: "40000", name: "a", oid: inner}), "a"); err != nil || len(entries) != 1 || entries[0].Path != "a/b" {
		t.Errorf("valid nested folder: entries=%+v err=%v", entries, err)
	}

	// The same readers answer the valid commit of the same repository.
	if view, err := manager.PathAt(ctx, "sample", valid, "z"); err != nil || view.File.OID == "" {
		t.Errorf("valid path view: view=%+v err=%v", view, err)
	}
	if _, files, err := manager.CommitFiles(ctx, "sample", valid); err != nil || len(files) != 1 || files[0].Path != "z" {
		t.Errorf("valid commit files: files=%+v err=%v", files, err)
	}
	if _, err := manager.Languages(ctx, "sample", valid); err != nil {
		t.Errorf("valid language count: %v", err)
	}
	if err := manager.VerifyTreePaths(ctx, repositoryPath, valid, 1<<20); err != nil {
		t.Errorf("valid tree check: %v", err)
	}
}

// A folder whose stored directory entry appears twice lists the children of
// both trees. ls-tree -r names only leaves, so this listing is where a page
// reader must see it: otherwise the page would pick one of two README
// documents, count rows twice, and page through rows that share a path.
func TestFolderPagesRefuseAStoredFolderThatAppearsTwice(t *testing.T) {
	ctx := context.Background()
	manager, remote, _ := newTestRepository(t)
	first := gitInputOutput(t, remote, []byte("first readme\n"), "hash-object", "-w", "--stdin")
	second := gitInputOutput(t, remote, []byte("second readme\n"), "hash-object", "-w", "--stdin")
	// The children of the two trees interleave, so the second README does not
	// follow the first record directly.
	left := gitInputOutput(t, remote, []byte("100644 blob "+first+"\tREADME.md\n100644 blob "+first+"\tx.txt\n"), "mktree")
	right := gitInputOutput(t, remote, []byte("100644 blob "+second+"\tREADME.md\n100644 blob "+second+"\ty.txt\n"), "mktree")
	colliding := storedTreeCommit(t, remote,
		storedTreeEntry{mode: "40000", name: "a", oid: left},
		storedTreeEntry{mode: "40000", name: "a", oid: right})
	if page, err := manager.TreePageAt(ctx, "sample", colliding, "a", ""); !errors.Is(err, ErrRepeatedTreePath) {
		t.Errorf("folder page: total=%d entries=%d readme=%d err=%v, want a repeated path", page.Total, len(page.Entries), len(page.Readme), err)
	}
	if view, page, err := manager.PathPageAt(ctx, "sample", colliding, "a", ""); !errors.Is(err, ErrRepeatedTreePath) {
		t.Errorf("folder path page: view=%+v total=%d readme=%d err=%v, want a repeated path", view, page.Total, len(page.Readme), err)
	}

	// An ordinary folder keeps reading, including the listing order Git uses,
	// where a file name such as a.txt comes before a directory named a
	// (treePathLess), and a folder that holds a readme.
	ordinary := gitInputOutput(t, remote, []byte("100644 blob "+first+"\tREADME.md\n100644 blob "+first+"\tx.txt\n"), "mktree")
	directory := gitInputOutput(t, remote, []byte("100644 blob "+first+"\tinner\n"), "mktree")
	valid := storedTreeCommit(t, remote,
		storedTreeEntry{mode: "100644", name: "a.txt", oid: first},
		storedTreeEntry{mode: "40000", name: "a", oid: ordinary},
		storedTreeEntry{mode: "40000", name: "b", oid: directory})
	page, err := manager.TreePageAt(ctx, "sample", valid, "a", "")
	if err != nil || page.Total != 2 || len(page.Readme) != 1 || page.Readme[0].OID != first {
		t.Errorf("ordinary folder page: total=%d readme=%+v err=%v, want its README", page.Total, page.Readme, err)
	}
	if page, err := manager.TreePageAt(ctx, "sample", valid, "", ""); err != nil || page.Total != 3 {
		t.Errorf("ordinary root page: total=%d err=%v, want three entries", page.Total, err)
	}
}

// pathspecBytes totals the bytes of one read's pathspec arguments.
func pathspecBytes(specs []string) int {
	total := 0
	for _, spec := range specs {
		total += len(spec)
	}
	return total
}

// levelPathspecBytes totals the pathspecs one per level of path, which
// treeListingSpecs keeps while they fit treeSpecLimit.
func levelPathspecBytes(path string) int {
	total := 0
	for prefix := path; prefix != ""; prefix = treeParentPath(prefix) {
		total += len(treeSpecMagic) + len(prefix)
	}
	return total
}

// One pathspec per level of the requested path makes a same-name file beside a
// directory of an ancestor level visible, but their total grows with the square
// of the depth: a path within MaximumTreePathBytes names about 4.2 MB of
// pathspecs above depth 2000, past the argument limit of every platform, and
// the Windows command line of 32,767 characters is passed near depth 60 with
// ordinary folder names. Past treeSpecLimit a read asks for the requested path
// and its folder only, which keeps the command line of a read far below that
// limit, and no valid path is refused because of its depth.
func TestTreeListingSpecsStayWithinTheArgumentBound(t *testing.T) {
	shallow := []string{treeSpecMagic + "a/b/c", treeSpecMagic + "a/b", treeSpecMagic + "a", treeSpecMagic + "a/b/c/"}
	if got := strings.Join(treeListingSpecs("a/b/c"), "\x00"); got != strings.Join(shallow, "\x00") {
		t.Errorf("shallow pathspecs=%q, want %q", strings.Split(got, "\x00"), shallow)
	}
	if size := levelPathspecBytes("a/b/c"); size > treeSpecLimit {
		t.Errorf("shallow pathspecs=%d bytes, want at most the bound of %d", size, treeSpecLimit)
	}

	deep := strings.Repeat("a/", 2047) + "f"
	if len(deep) > MaximumTreePathBytes {
		t.Fatalf("fixture path=%d bytes, want at most %d", len(deep), MaximumTreePathBytes)
	}
	specs := treeListingSpecs(deep)
	if len(specs) != 2 || specs[0] != treeSpecMagic+deep || specs[1] != treeSpecMagic+deep+"/" {
		t.Errorf("deep pathspecs=%d, want the requested path and its folder: %q", len(specs), specs)
	}
	// The whole command line of the deepest read: the pathspecs, the flags, the
	// object ID, the separator, and the parent pathspec of a path read. Windows
	// counts the characters after it quotes the arguments, where a byte reaches
	// two characters at most (a quoted quote, or a backslash doubled before it)
	// and each argument adds up to three characters around it.
	const windowsCommandLine, commandLineAllowance = 32767, 512
	parentSpec := len(treeSpecMagic) + len(treeParentPath(deep)) + 1
	specBytes := pathspecBytes(specs) + parentSpec
	commandLine := 2*(specBytes+1) + 3*(len(specs)+1) + commandLineAllowance
	if commandLine > windowsCommandLine {
		t.Errorf("deep read command line=%d characters, want at most %d", commandLine, windowsCommandLine)
	}
	if levels := levelPathspecBytes(deep); levels <= windowsCommandLine {
		t.Fatalf("deep fixture names %d bytes of level pathspecs, want more than the %d-byte command line the bound guards", levels, windowsCommandLine)
	}
}

// deepTreeCommit writes one file below directories nested as deep as filePath
// says and returns the commit. fast-import writes the whole chain in one
// process, and the fixture uses no shell, so its test runs on Windows too.
func deepTreeCommit(t *testing.T, remote, filePath string) string {
	t.Helper()
	fixture := "blob\nmark :1\ndata 5\ndeep\n" +
		"commit refs/heads/deep\ncommitter Deep Test <deep@example.invalid> 1704067200 +0000\ndata 4\ndeep\n" +
		"deleteall\nM 100644 :1 " + filePath + "\n\ndone\n"
	command := exec.Command("git", "--git-dir", remote, "-c", "core.maxTreeDepth=100000", "fast-import", "--quiet", "--force")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create deep tree fixture: %v\n%s", err, output)
	}
	return gitOutput(t, remote, "--git-dir", ".", "rev-parse", "refs/heads/deep")
}

// A valid path at the depth where the levels' pathspecs pass the bound still
// reads, at every reader.
func TestReadersReadAValidPathAtTheDepthBound(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	components := make([]string, 2047)
	for index := range components {
		components[index] = "a"
	}
	directory := strings.Join(components, "/")
	filePath := directory + "/f"
	commit := deepTreeCommit(t, remote, filePath)
	ctx := context.Background()

	entries, err := manager.TreeAt(ctx, "sample", commit, directory)
	if err != nil || len(entries) != 1 || entries[0].Name != "f" || entries[0].Path != filePath {
		t.Errorf("deep folder read: entries=%+v err=%v", entries, err)
	}
	page, err := manager.TreePageAt(ctx, "sample", commit, directory, "")
	if err != nil || page.Total != 1 || len(page.Entries) != 1 || page.Entries[0].Name != "f" {
		t.Errorf("deep folder page: total=%d entries=%d err=%v", page.Total, len(page.Entries), err)
	}
	view, err := manager.PathAt(ctx, "sample", commit, filePath)
	if err != nil || view.Folder || view.File.Path != filePath || view.File.OID == "" {
		t.Errorf("deep path read: folder=%v path=%q oid=%q err=%v", view.Folder, view.File.Path, view.File.OID, err)
	}
	view, page, err = manager.PathPageAt(ctx, "sample", commit, filePath, "")
	if err != nil || view.Folder || view.File.Path != filePath || view.File.OID == "" || page.Total != 1 {
		t.Errorf("deep path page: folder=%v path=%q oid=%q total=%d err=%v", view.Folder, view.File.Path, view.File.OID, page.Total, err)
	}
}

// A collision below a level whose pathspecs pass treeSpecLimit is still
// refused. The listing falls back to the requested path and its folder, and
// ls-tree -t prints the record of every directory it descends into, so the
// level that holds two trees of one name arrives as one path twice. A
// same-name file beside a directory of an ancestor level is not seen at such a
// depth: new pushes are refused by receive.fsckObjects and imports by
// index-pack --strict, a tree restored from a backup or stored before this
// change is not checked again, and the gap is an accepted limitation of this
// unit.
func TestReadersRefuseACollisionBelowTheSpecBound(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	file := gitInputOutput(t, remote, []byte("deep\n"), "hash-object", "-w", "--stdin")
	level := gitInputOutput(t, remote, []byte("100644 blob "+file+"\tf\n"), "mktree")
	other := gitInputOutput(t, remote, []byte("100644 blob "+file+"\tother\n"), "mktree")
	names := make([]string, 64)
	for index := range names {
		names[index] = fmt.Sprintf("level%03d", index)
	}
	for index := len(names) - 1; index >= 0; index-- {
		entries := []storedTreeEntry{{mode: "40000", name: names[index], oid: level}}
		if index == 30 {
			// One level holds a second tree of the name the chain continues
			// with, so the folder below it has two identities.
			entries = append(entries, storedTreeEntry{mode: "40000", name: names[index], oid: other})
		}
		level = storedTree(t, remote, entries...)
	}
	commit := gitInputOutput(t, remote, []byte("collision below the bound\n"), "-c", "user.name=Stored Tree",
		"-c", "user.email=stored@example.invalid", "commit-tree", level)
	directory := strings.Join(names, "/")
	filePath := directory + "/f"
	if size := levelPathspecBytes(filePath); size <= treeSpecLimit {
		t.Fatalf("fixture pathspecs=%d bytes, want more than the bound of %d", size, treeSpecLimit)
	}

	ctx := context.Background()
	for _, reading := range []storedTreeReading{
		{name: "deep folder listing", read: func() error {
			_, err := manager.TreeAt(ctx, "sample", commit, directory)
			return err
		}},
		{name: "deep path view", read: func() error {
			_, err := manager.PathAt(ctx, "sample", commit, filePath)
			return err
		}},
		{name: "deep folder page", read: func() error {
			_, err := manager.TreePageAt(ctx, "sample", commit, directory, "")
			return err
		}},
		{name: "deep path page", read: func() error {
			_, _, err := manager.PathPageAt(ctx, "sample", commit, filePath, "")
			return err
		}},
	} {
		if err := reading.read(); !errors.Is(err, ErrRepeatedTreePath) {
			t.Errorf("%s: error=%v, want %v", reading.name, err, ErrRepeatedTreePath)
		}
	}
}

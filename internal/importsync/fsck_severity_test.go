package importsync

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// writeLiteral stores an object exactly as given, without Git's own checks,
// the way old history holds it.
func (f *fixture) writeLiteral(kind, content string) string {
	f.t.Helper()
	return strings.TrimSpace(string(f.gitInput(f.source, []byte(content), "hash-object", "--literally", "-w", "-t", kind, "--stdin")))
}

// treeEntry encodes one raw tree entry.
func (f *fixture) treeEntry(mode, name, oid string) string {
	f.t.Helper()
	raw, err := hex.DecodeString(oid)
	if err != nil {
		f.t.Fatal(err)
	}
	return mode + " " + name + "\x00" + string(raw)
}

// Popular repositories hold old commits, tags and trees that old versions of
// Git spelled wrongly. An import accepts exactly those faults, in staging and
// in the destination.
func TestImportAcceptsHistoricFormatFaults(t *testing.T) {
	f := newFixture(t)
	base := f.commit("one", "one\n")
	tree := f.git(f.source, "rev-parse", base+"^{tree}")
	// badTimezone, as in rails/rails commit 4cf94979.
	commit := f.writeLiteral("commit", fmt.Sprintf("tree %s\nparent %s\n"+
		"author A <a@example.invalid> 1312735823 +051800\ncommitter A <a@example.invalid> 1312735823 +051800\n\nbad time zone\n", tree, base))
	// missingSpaceBeforeDate, as in coreutils/coreutils tag v4.5.1.
	tag := f.writeLiteral("tag", fmt.Sprintf("object %s\ntype commit\ntag v1\ntagger T <t@example.invalid>\n\nno date\n", commit))
	// zeroPaddedFilemode, as in 141 rails/rails trees.
	padded := f.writeLiteral("tree", f.treeEntry("040000", "dir", tree))
	old := f.writeLiteral("commit", fmt.Sprintf("tree %s\nparent %s\n"+
		"author A <a@example.invalid> 1312735823 +0000\ncommitter A <a@example.invalid> 1312735823 +0000\n\npadded mode\n", padded, base))
	f.git(f.source, "update-ref", "refs/heads/main", commit)
	f.git(f.source, "update-ref", "refs/heads/old", old)
	f.git(f.source, "update-ref", "refs/tags/v1", tag)

	f.mustImport(ImportInput{})
	refs := f.destinationRefs()
	require(t, refs["refs/heads/main"] == commit && refs["refs/heads/old"] == old && refs["refs/tags/v1"] == tag,
		"destination refs = %v, want main %s, old %s and v1 %s", refs, commit, old, tag)
	for _, oid := range []string{commit, tag, base, old, padded} {
		f.git(f.destinationPath(), "--git-dir", ".", "cat-file", "-e", oid)
	}
}

// Every other strict check still refuses the pack: the path, link and
// submodule checks strict indexing exists for, and faults outside the list,
// even beside an accepted one.
func TestImportStillRefusesOtherObjectFaults(t *testing.T) {
	for _, test := range []struct {
		name string
		tree func(f *fixture, blob string) string
		// commit returns the commit header lines after tree and parent.
		idents string
	}{
		{name: "hasDotgit", tree: func(f *fixture, blob string) string {
			return f.writeLiteral("tree", f.treeEntry("100644", ".git", blob))
		}},
		{name: "gitmodulesSymlink", tree: func(f *fixture, blob string) string {
			return f.writeLiteral("tree", f.treeEntry("120000", ".gitmodules", blob)+f.treeEntry("100644", "a.txt", blob))
		}},
		{name: "missingEmail", idents: "author A 1312735823 +0000\ncommitter A <a@example.invalid> 1312735823 +0000\n"},
		{name: "missingEmail beside badTimezone", idents: "author A <a@example.invalid> 1312735823 +051800\ncommitter A 1312735823 +0000\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			base := f.commit("one", "one\n")
			tree := f.git(f.source, "rev-parse", base+"^{tree}")
			if test.tree != nil {
				tree = test.tree(f, f.git(f.source, "rev-parse", base+":file.txt"))
			}
			idents := test.idents
			if idents == "" {
				idents = "author A <a@example.invalid> 1312735823 +0000\ncommitter A <a@example.invalid> 1312735823 +0000\n"
			}
			commit := f.writeLiteral("commit", fmt.Sprintf("tree %s\nparent %s\n%s\nfault\n", tree, base, idents))
			f.git(f.source, "update-ref", "refs/heads/main", commit)

			result, err := f.importProject(ImportInput{})
			require(t, err != nil && problemCode(err) == CodeIndexFailed,
				"import result=%+v err=%v, want %s", result.Run, err, CodeIndexFailed)
			assertImportDestinationAbsent(t, f)
		})
	}
}

// Strict indexing lets malformed dates through by their fsck message ID, but
// an import completes only if every commit it brings has author and committer
// dates the history pages can show. A commit that fails is named, and nothing
// is published, even when no ref reaches the commit.
func TestImportRefusesCommitDatesOwnGitCannotShow(t *testing.T) {
	for _, test := range []struct {
		name, author string
		unreferenced bool
	}{
		{name: "letters as the offset", author: "author A <a@example.invalid> 10 +abcd"},
		{name: "no date", author: "author A <a@example.invalid>not-a-date"},
		{name: "unreferenced commit in the pack", author: "author A <a@example.invalid> 10 +abcd", unreferenced: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			base := f.commit("one", "one\n")
			tree := f.git(f.source, "rev-parse", base+"^{tree}")
			bad := f.writeLiteral("commit", fmt.Sprintf("tree %s\nparent %s\n%s\ncommitter A <a@example.invalid> 10 +0000\n\nunreadable date\n", tree, base, test.author))
			if test.unreferenced {
				f.transport.packOverride = func() []byte {
					return f.gitInput(f.source, []byte("refs/heads/main\n"+bad+"\n"), "pack-objects", "--revs", "--stdout")
				}
			} else {
				f.git(f.source, "update-ref", "refs/heads/main", bad)
			}

			result, err := f.importProject(ImportInput{})
			require(t, err != nil && problemCode(err) == CodeVerifyFailed && strings.Contains(err.Error(), bad),
				"import result=%+v err=%v, want %s naming %s", result.Run, err, CodeVerifyFailed, bad)
			assertImportDestinationAbsent(t, f)
		})
	}
}

// The date check reads commits in batches of commitDateBatch. A history
// longer than one batch imports, and an unreadable commit is found in the
// first batch and in the last.
func TestCommitDateCheckCoversEveryBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("imports a history past one 10000 commit batch")
	}
	f := newFixture(t)
	f.commit("one", "one\n")
	tree := f.git(f.source, "rev-parse", "HEAD^{tree}")
	var stream strings.Builder
	for index := 1; index <= commitDateBatch+1; index++ {
		fmt.Fprintf(&stream, "commit refs/heads/long\ncommitter A <a@example.invalid> %d +0900\ndata 0\n", 1_000_000+index)
		if index == 1 {
			stream.WriteString("from refs/heads/main\n")
		}
		fmt.Fprintf(&stream, "M 040000 %s \"\"\n", tree)
	}
	f.gitInput(f.source, []byte(stream.String()), "fast-import", "--quiet")
	f.mustImport(ImportInput{})
	got, want := f.destinationRefs()["refs/heads/long"], f.git(f.source, "rev-parse", "refs/heads/long")
	require(t, got == want, "long history = %s, want %s", got, want)

	f.git(f.source, "commit", "--allow-empty", "-m", "two")
	// Git packs recent commits first, so the newest bad commit lands in the
	// first batch and the oldest in the last.
	for _, committed := range []string{"4000000000", "10"} {
		bad := f.writeLiteral("commit", fmt.Sprintf("tree %s\nauthor A <a@example.invalid> 10 +abcd\ncommitter A <a@example.invalid> %s +0000\n\nunreadable date\n", tree, committed))
		f.transport.packOverride = func() []byte {
			return f.gitInput(f.source, []byte("refs/heads/main\nrefs/heads/long\n"+bad+"\n"), "pack-objects", "--revs", "--stdout")
		}
		_, err := f.refresh()
		require(t, err != nil && problemCode(err) == CodeVerifyFailed && strings.Contains(err.Error(), bad),
			"refresh with a bad commit at %s: err=%v, want %s naming %s", committed, err, CodeVerifyFailed, bad)
	}
}

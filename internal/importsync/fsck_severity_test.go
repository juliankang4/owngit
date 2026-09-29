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

// Popular repositories hold old commits and tags whose dates are malformed.
// An import accepts exactly those faults, in staging and in the destination.
func TestImportAcceptsHistoricDateFaults(t *testing.T) {
	f := newFixture(t)
	base := f.commit("one", "one\n")
	tree := f.git(f.source, "rev-parse", base+"^{tree}")
	// badTimezone, as in rails/rails commit 4cf94979.
	commit := f.writeLiteral("commit", fmt.Sprintf("tree %s\nparent %s\n"+
		"author A <a@example.invalid> 1312735823 +051800\ncommitter A <a@example.invalid> 1312735823 +051800\n\nbad time zone\n", tree, base))
	// missingSpaceBeforeDate, as in coreutils/coreutils tag v4.5.1.
	tag := f.writeLiteral("tag", fmt.Sprintf("object %s\ntype commit\ntag v1\ntagger T <t@example.invalid>\n\nno date\n", commit))
	f.git(f.source, "update-ref", "refs/heads/main", commit)
	f.git(f.source, "update-ref", "refs/tags/v1", tag)

	f.mustImport(ImportInput{})
	refs := f.destinationRefs()
	if refs["refs/heads/main"] != commit || refs["refs/tags/v1"] != tag {
		t.Fatalf("destination refs = %v, want main %s and v1 %s", refs, commit, tag)
	}
	for _, oid := range []string{commit, tag, base} {
		f.git(f.destinationPath(), "--git-dir", ".", "cat-file", "-e", oid)
	}
}

// Every other strict check still refuses the pack: the path, link and
// submodule checks strict indexing exists for, and date faults outside the
// list, even beside an accepted one.
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
			if err == nil || problemCode(err) != CodeIndexFailed {
				t.Fatalf("import result=%+v err=%v, want %s", result.Run, err, CodeIndexFailed)
			}
			assertImportDestinationAbsent(t, f)
		})
	}
}

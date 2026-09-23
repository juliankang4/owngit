package importsync

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParentSourceReplacementCannotHideLFSPointer(t *testing.T) {
	f := newFixture(t)
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 1234\n"
	original := f.commit("original pointer", pointer)
	f.commit("clean tree", "ordinary bytes\n")
	tree := f.git(f.source, "write-tree")
	replacement := f.git(f.source, "commit-tree", tree, "-m", "clean root replacement")
	f.git(f.source, "update-ref", "refs/heads/main", original)
	f.git(f.source, "update-ref", "refs/replace/"+original, replacement)
	before := f.sourceRefs()
	if got := f.gitInput(f.source, nil, "--no-replace-objects", "cat-file", "blob", original+":file.txt"); !bytes.Equal(got, []byte(pointer)) {
		t.Fatalf("original fixture bytes=%q", got)
	}
	if got := f.git(f.source, "show", "refs/heads/main:file.txt"); got != "ordinary bytes" {
		t.Fatalf("replacement fixture did not mask original: %q", got)
	}
	result, err := f.importProject(ImportInput{})
	if err == nil || problemCode(err) != CodeLFSRequired {
		t.Fatalf("replacement hid original LFS content: status=%s pointers=%d complete=%v err=%v", result.Run.Status, result.Run.LFSDetected, result.Run.LFSInspectionDone, err)
	}
	assertImportDestinationAbsent(t, f)
	if after := f.sourceRefs(); !reflect.DeepEqual(before, after) {
		t.Fatalf("source refs changed: before=%v after=%v", before, after)
	}
}

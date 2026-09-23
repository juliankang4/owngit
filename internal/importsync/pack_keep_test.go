package importsync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"owngit/internal/state"
)

func destinationKeepFiles(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.keep"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	sort.Strings(names)
	return names
}

// Destination indexing uses index-pack --keep only to protect the new pack
// until the ref transaction ends, as git receive-pack does. A .keep file left
// behind would exclude every imported pack from later repacking.
func TestImportRemovesItsDestinationPackKeep(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	initial := f.mustImport(ImportInput{})
	if initial.Run.Status != state.ImportRunComplete {
		t.Fatalf("initial run=%+v", initial.Run)
	}
	path := f.destinationPath()
	if keeps := destinationKeepFiles(t, path); len(keeps) != 0 {
		t.Fatalf("initial import left keep files %v", keeps)
	}
	packs, err := filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("initial packs=%v err=%v", packs, err)
	}
	// An operator's .keep file on an existing pack is not this run's to remove.
	operatorKeep := packs[0][:len(packs[0])-len(".pack")] + ".keep"
	if err := os.WriteFile(operatorKeep, []byte("operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.commit("two", "two\n")
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	packs, err = filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.pack"))
	if err != nil || len(packs) != 2 {
		t.Fatalf("refresh did not index a new destination pack: packs=%v err=%v", packs, err)
	}
	if keeps := destinationKeepFiles(t, path); len(keeps) != 1 || keeps[0] != filepath.Base(operatorKeep) {
		t.Fatalf("keep files after refresh=%v want only %s", keeps, filepath.Base(operatorKeep))
	}
	if content, err := os.ReadFile(operatorKeep); err != nil || string(content) != "operator\n" {
		t.Fatalf("operator keep file changed: %q err=%v", content, err)
	}
}

// The .keep file protects the pack while the ref transaction is prepared,
// names the import run that created it, and is removed after a refused ref
// transaction too.
func TestFailedPublicationRemovesItsDestinationPackKeep(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var preparedKeeps []string
	var preparedContent string
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		path := f.destinationPath()
		preparedKeeps = destinationKeepFiles(t, path)
		if len(preparedKeeps) == 1 {
			content, _ := os.ReadFile(filepath.Join(path, "objects", "pack", preparedKeeps[0]))
			preparedContent = string(content)
		}
		cancel()
	}
	if run, err := f.service.Refresh(ctx, "project", Limits{}); err == nil || run.Status == state.ImportRunComplete {
		t.Fatalf("cancelled refresh run=%+v err=%v", run, err)
	}
	path := f.destinationPath()
	if got := f.git(path, "--git-dir", ".", "rev-parse", "refs/heads/main"); got != old {
		t.Fatalf("cancelled refresh moved main to %s", got)
	}
	if keeps := destinationKeepFiles(t, path); len(keeps) != 0 {
		t.Fatalf("failed refresh left keep files %v", keeps)
	}
	if want := "owngit import " + f.lastRun().ID + "\n"; len(preparedKeeps) != 1 || preparedContent != want {
		t.Fatalf("keep files while prepared=%v content=%q want one with %q", preparedKeeps, preparedContent, want)
	}
}

func TestCreatedPackKeepOnlyTrustsKeepReports(t *testing.T) {
	sha1 := "0123456789abcdef0123456789abcdef01234567"
	sha256 := sha1 + "0123456789abcdef01234567"
	for _, test := range []struct {
		stdout string
		want   string
	}{
		{"keep\t" + sha1 + "\n", sha1},
		{"keep\t" + sha256 + "\n", sha256},
		{"keep\t" + sha1 + "\ntrailing input", sha1},
		// The pack and its .keep already existed; that file is not ours.
		{"pack\t" + sha1 + "\n", ""},
		{"keep\t../../" + sha1[6:] + "\n", ""},
		{"keep\t" + sha1[:39] + "\n", ""},
		{"keep\t" + "0123456789ABCDEF0123456789abcdef01234567\n", ""},
		{"", ""},
	} {
		got, created := createdPackKeep([]byte(test.stdout))
		if got != test.want || created != (test.want != "") {
			t.Fatalf("createdPackKeep(%q)=%q,%v want %q", test.stdout, got, created, test.want)
		}
	}
}

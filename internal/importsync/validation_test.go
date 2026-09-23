package importsync

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/importgit"
	"owngit/internal/state"
)

func (f *fixture) gitInput(directory string, input []byte, arguments ...string) []byte {
	f.t.Helper()
	command := exec.Command(f.gitPath, arguments...)
	command.Dir = directory
	command.Env = f.env
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, stderr.Bytes())
	}
	return stdout.Bytes()
}

func assertImportDestinationAbsent(t *testing.T, f *fixture) {
	t.Helper()
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("destination exists=%v err=%v", exists, err)
	}
}

func TestImportValidatesSkippedAdvertisedObjectsAndPeels(t *testing.T) {
	t.Run("missing skipped object", func(t *testing.T) {
		f := newFixture(t)
		tip := f.commit("one", "one\n")
		f.git(f.source, "update-ref", "refs/notes/review", tip)
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			for index := range advertisement.Refs {
				if advertisement.Refs[index].Name == "refs/notes/review" {
					advertisement.Refs[index].OID = strings.Repeat("f", 40)
				}
			}
		}

		result, err := f.importProject(ImportInput{})
		if err == nil || problemCode(err) != CodeVerifyFailed {
			t.Fatalf("missing skipped object result=%+v err=%v", result.Run, err)
		}
		assertImportDestinationAbsent(t, f)
		if actual := f.git(f.source, "rev-parse", "refs/notes/review"); actual != tip {
			t.Fatalf("source note changed to %s, want %s", actual, tip)
		}
	})

	t.Run("malformed skipped object", func(t *testing.T) {
		f := newFixture(t)
		invalidOID := strings.TrimSpace(string(f.gitInput(f.source, []byte("not a commit\n"),
			"hash-object", "--literally", "-w", "-t", "commit", "--stdin")))
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			advertisement.Empty = false
			advertisement.Refs = []importgit.Ref{{Name: "refs/notes/malformed", OID: invalidOID}}
		}
		f.transport.packOverride = func() []byte {
			return f.gitInput(f.source, []byte(invalidOID+"\n"), "pack-objects", "--stdout")
		}

		result, err := f.importProject(ImportInput{})
		if err == nil || problemCode(err) != CodeIndexFailed {
			t.Fatalf("malformed skipped object result=%+v err=%v", result.Run, err)
		}
		assertImportDestinationAbsent(t, f)
		if refs := f.git(f.source, "for-each-ref", "--format=%(refname)"); refs != "" {
			t.Fatalf("synthetic advertisement wrote source refs: %q", refs)
		}
	})

	t.Run("false skipped peel", func(t *testing.T) {
		f := newFixture(t)
		first := f.commit("one", "one\n")
		f.git(f.source, "tag", "-a", "v1", "-m", "release")
		tagOID := f.git(f.source, "rev-parse", "refs/tags/v1")
		f.git(f.source, "update-ref", "refs/notes/release", tagOID)
		second := f.commit("two", "two\n")
		if first == second {
			t.Fatal("fixture commits unexpectedly match")
		}
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			for index := range advertisement.Refs {
				if advertisement.Refs[index].Name == "refs/notes/release" {
					advertisement.Refs[index].PeeledOID = second
				}
			}
		}

		result, err := f.importProject(ImportInput{})
		if err == nil || problemCode(err) != CodeVerifyFailed {
			t.Fatalf("false skipped peel result=%+v err=%v", result.Run, err)
		}
		assertImportDestinationAbsent(t, f)
		if actual := f.git(f.source, "rev-parse", "refs/notes/release"); actual != tagOID {
			t.Fatalf("source note changed to %s, want %s", actual, tagOID)
		}
	})
}

func TestImportRejectsUnsupportedHEADTargetsAndRecordsRawFact(t *testing.T) {
	for _, target := range []string{"refs/tags/v1", "refs/remotes/origin/main"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
				advertisement.Head.SymrefTarget = target
			}
			result, err := f.importProject(ImportInput{})
			if err == nil || problemCode(err) != CodeUnsupportedRefs {
				t.Fatalf("unsupported HEAD result=%+v err=%v", result.Run, err)
			}
			if result.Run.HeadSymref != target || !result.Run.HeadAdvertised {
				t.Fatalf("raw HEAD facts were lost: %+v", result.Run)
			}
			if result.Status.LastRun == nil || result.Status.LastRun.HeadSymref != target || result.Status.LastRun.Status != state.ImportRunFailed {
				t.Fatalf("stored raw HEAD facts were lost: %+v", result.Status.LastRun)
			}
			assertImportDestinationAbsent(t, f)
		})
	}
}

func TestImportValidatesAdvertisedHEADTypeAndPeel(t *testing.T) {
	t.Run("HEAD must name a commit", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		blobOID := f.git(f.source, "rev-parse", "HEAD:file.txt")
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			advertisement.Head.OID = blobOID
			for index := range advertisement.Refs {
				if advertisement.Refs[index].Name == "HEAD" {
					advertisement.Refs[index].OID = blobOID
				}
			}
		}

		result, err := f.importProject(ImportInput{})
		if err == nil || problemCode(err) != CodeVerifyFailed {
			t.Fatalf("blob HEAD result=%+v err=%v", result.Run, err)
		}
		assertImportDestinationAbsent(t, f)
	})

	t.Run("HEAD peel must be exact", func(t *testing.T) {
		f := newFixture(t)
		first := f.commit("one", "one\n")
		f.commit("two", "two\n")
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			advertisement.Head.PeeledOID = first
			for index := range advertisement.Refs {
				if advertisement.Refs[index].Name == "HEAD" {
					advertisement.Refs[index].PeeledOID = first
				}
			}
		}

		result, err := f.importProject(ImportInput{})
		if err == nil || problemCode(err) != CodeVerifyFailed {
			t.Fatalf("false HEAD peel result=%+v err=%v", result.Run, err)
		}
		assertImportDestinationAbsent(t, f)
	})
}

func TestSourceReplacementCannotChangeConsentedImportBytes(t *testing.T) {
	f := newFixture(t)
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("b", 64) + "\nsize 42\n"
	original := f.commit("original pointer", pointer)
	f.commit("replacement", "ordinary bytes\n")
	tree := f.git(f.source, "write-tree")
	replacement := f.git(f.source, "commit-tree", tree, "-m", "replacement root")
	f.git(f.source, "update-ref", "refs/heads/main", original)
	f.git(f.source, "update-ref", "refs/replace/"+original, replacement)

	result := f.mustImport(ImportInput{GitOnlyConsent: true})
	if result.Run.LFSDetected != 1 || !result.Run.LFSInspectionDone {
		t.Fatalf("replacement changed inspection=%+v", result.Run)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != original {
		t.Fatalf("destination main=%s want %s", got, original)
	}
	content := f.gitInput(f.destinationPath(), nil, "--no-replace-objects", "--git-dir", ".", "cat-file", "blob", original+":file.txt")
	if !bytes.Equal(content, []byte(pointer)) {
		t.Fatalf("original pointer bytes changed: got=%q want=%q", content, pointer)
	}
	if refs := f.git(f.destinationPath(), "--git-dir", ".", "for-each-ref", "--format=%(refname)", "refs/replace"); refs != "" {
		t.Fatalf("source replacement refs were published: %q", refs)
	}
	if got := f.git(f.source, "rev-parse", "refs/replace/"+original); got != replacement {
		t.Fatalf("source replacement changed to %s, want %s", got, replacement)
	}
}

func TestDestinationReplacementCannotStandInForMissingAdvertisedObject(t *testing.T) {
	f := newFixture(t)
	original := f.commit("source", "source bytes\n")
	if _, err := f.manager.Create(context.Background(), "project", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git",
	}); err != nil {
		t.Fatal(err)
	}
	destination := f.destinationPath()
	replacement := strings.TrimSpace(string(f.gitInput(destination, []byte("replacement bytes\n"),
		"--git-dir", ".", "hash-object", "-w", "--stdin")))
	f.git(destination, "--git-dir", ".", "update-ref", "refs/replace/"+original, replacement)
	if kind := f.git(destination, "--git-dir", ".", "cat-file", "-t", original); kind != "blob" {
		t.Fatalf("replacement fixture type=%q", kind)
	}
	if raw := f.gitMaybe(destination, "--no-replace-objects", "--git-dir", ".", "cat-file", "-t", original); raw != "" {
		t.Fatalf("destination unexpectedly contained original object: %q", raw)
	}

	// Import refuses this existing destination. Refresh is the publication
	// that must still ignore the local replacement ref.
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != original {
		t.Fatalf("destination main=%s want %s", got, original)
	}
	sourceObject := f.gitInput(f.source, nil, "--no-replace-objects", "cat-file", "commit", original)
	destinationObject := f.gitInput(destination, nil, "--no-replace-objects", "--git-dir", ".", "cat-file", "commit", original)
	if !bytes.Equal(destinationObject, sourceObject) {
		t.Fatal("destination original object bytes differ from source")
	}
	if got := f.git(destination, "--git-dir", ".", "rev-parse", "refs/replace/"+original); got != replacement {
		t.Fatalf("local replacement changed to %s, want %s", got, replacement)
	}
}

func TestStrictPackIndexingRejectsMalformedObject(t *testing.T) {
	f := newFixture(t)
	invalidOID := strings.TrimSpace(string(f.gitInput(f.source, []byte("not a commit\n"),
		"hash-object", "--literally", "-w", "-t", "commit", "--stdin")))
	pack := f.gitInput(f.source, []byte(invalidOID+"\n"), "pack-objects", "--stdout")

	control := filepath.Join(f.root, "nonstrict-control.git")
	f.git("", "init", "--bare", control)
	f.gitInput(control, pack, "--git-dir", ".", "index-pack", "--stdin", "--keep")

	staging := filepath.Join(f.root, "strict-staging.git")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	limits, err := (Limits{}).effective()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.createStagingRepository(context.Background(), staging, importgit.FormatSHA1, limits); err != nil {
		t.Fatal(err)
	}
	if err := f.service.indexStagingPack(context.Background(), staging, bytes.NewReader(pack), limits); err == nil || problemCode(err) != CodeIndexFailed {
		t.Fatalf("strict indexing error=%v", err)
	}
}

func TestIncompleteLFSInspectionRequiresConsentForInitialImport(t *testing.T) {
	f := newFixture(t)
	tip := f.commit("ordinary", "ordinary content\n")
	limits := Limits{LFS: LFSLimits{MaxObjects: 1}}

	refused, err := f.importProject(ImportInput{Limits: limits})
	if err == nil || problemCode(err) != CodeLFSRequired {
		t.Fatalf("incomplete import result=%+v err=%v", refused.Run, err)
	}
	if refused.Run.LFSDetected != 0 || refused.Run.LFSInspectionDone {
		t.Fatalf("refused inspection=%+v", refused.Run)
	}
	assertImportDestinationAbsent(t, f)

	accepted := f.mustImport(ImportInput{GitOnlyConsent: true, Limits: limits})
	if accepted.Run.Status != state.ImportRunComplete || accepted.Run.LFSDetected != 0 || accepted.Run.LFSInspectionDone {
		t.Fatalf("consented run=%+v", accepted.Run)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != tip {
		t.Fatalf("destination main=%s want %s", got, tip)
	}
	if !accepted.Status.Content.Incomplete || accepted.Status.Content.InspectionComplete || accepted.Status.Content.LFSDetected != 0 {
		t.Fatalf("content status=%+v", accepted.Status.Content)
	}
}

func TestIncompleteLFSInspectionRequiresConsentForRefresh(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	second := f.commit("two", "two\n")
	beforeDestination := f.destinationRefs()
	beforeSource := f.sourceRefs()
	limits := Limits{LFS: LFSLimits{MaxObjects: 1}}

	refused, err := f.service.Refresh(context.Background(), "project", limits)
	if err == nil || problemCode(err) != CodeLFSRequired {
		t.Fatalf("incomplete refresh result=%+v err=%v", refused, err)
	}
	if refused.LFSDetected != 0 || refused.LFSInspectionDone {
		t.Fatalf("refused inspection=%+v", refused)
	}
	if after := f.destinationRefs(); !reflect.DeepEqual(after, beforeDestination) || after["refs/heads/main"] != first {
		t.Fatalf("destination changed on refusal: before=%v after=%v", beforeDestination, after)
	}
	if after := f.sourceRefs(); !reflect.DeepEqual(after, beforeSource) {
		t.Fatalf("source changed on refusal: before=%v after=%v", beforeSource, after)
	}
	refusedStatus, err := f.service.Status(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if refusedStatus.LastRun == nil || refusedStatus.LastRun.Status != state.ImportRunFailed || refusedStatus.LastRun.LFSInspectionDone {
		t.Fatalf("last attempt=%+v", refusedStatus.LastRun)
	}
	if !refusedStatus.Content.InspectionComplete || refusedStatus.Content.Incomplete || refusedStatus.Content.LFSDetected != 0 {
		t.Fatalf("failed refresh replaced accepted content status: %+v", refusedStatus.Content)
	}

	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", GitOnlyConsent: true,
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.service.Refresh(context.Background(), "project", limits)
	if err != nil {
		t.Fatalf("consented refresh: %v", err)
	}
	if accepted.Status != state.ImportRunComplete || accepted.LFSDetected != 0 || accepted.LFSInspectionDone {
		t.Fatalf("consented refresh=%+v", accepted)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != second {
		t.Fatalf("destination main=%s want %s", got, second)
	}
	status, err := f.service.Status(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Content.Incomplete || status.Content.InspectionComplete || status.Content.LFSDetected != 0 {
		t.Fatalf("content status=%+v", status.Content)
	}
}

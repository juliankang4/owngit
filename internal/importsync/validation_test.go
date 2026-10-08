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
	_, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	require(t, err == nil && !exists, "destination exists=%v err=%v", exists, err)
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
		require(t, err != nil && problemCode(err) == CodeVerifyFailed,
			"missing skipped object result=%+v err=%v", result.Run, err)
		assertImportDestinationAbsent(t, f)
		eq(t, "source note changed to", f.git(f.source, "rev-parse", "refs/notes/review"), tip)
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
		require(t, err != nil && problemCode(err) == CodeIndexFailed,
			"malformed skipped object result=%+v err=%v", result.Run, err)
		assertImportDestinationAbsent(t, f)
		eq(t, "synthetic advertisement wrote source refs", f.git(f.source, "for-each-ref", "--format=%(refname)"), "")
	})

	t.Run("false skipped peel", func(t *testing.T) {
		f := newFixture(t)
		first := f.commit("one", "one\n")
		f.git(f.source, "tag", "-a", "v1", "-m", "release")
		tagOID := f.git(f.source, "rev-parse", "refs/tags/v1")
		f.git(f.source, "update-ref", "refs/notes/release", tagOID)
		second := f.commit("two", "two\n")
		require(t, first != second, "fixture commits unexpectedly match")
		f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
			for index := range advertisement.Refs {
				if advertisement.Refs[index].Name == "refs/notes/release" {
					advertisement.Refs[index].PeeledOID = second
				}
			}
		}

		result, err := f.importProject(ImportInput{})
		require(t, err != nil && problemCode(err) == CodeVerifyFailed,
			"false skipped peel result=%+v err=%v", result.Run, err)
		assertImportDestinationAbsent(t, f)
		eq(t, "source note changed to", f.git(f.source, "rev-parse", "refs/notes/release"), tagOID)
	})
}

// A source whose HEAD really is symbolic to a non-branch ref is refused, not
// imported as a detached HEAD, and the source is left exactly as it was.
func TestImportRejectsUnsupportedHEADTargetsAndRecordsRawFact(t *testing.T) {
	for _, target := range []string{"refs/tags/v1", "refs/remotes/origin/main"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			oid := f.commit("one", "one\n")
			f.git(f.source, "update-ref", target, oid)
			f.git(f.source, "symbolic-ref", "HEAD", target)
			before := f.sourceRefs()
			result, err := f.importProject(ImportInput{})
			require(t, err != nil && problemCode(err) == CodeUnsupportedRefs,
				"unsupported HEAD result=%+v err=%v", result.Run, err)
			require(t, result.Run.HeadSymref == target && result.Run.HeadAdvertised,
				"raw HEAD facts were lost: %+v", result.Run)
			stored := f.lastRun()
			require(t, stored.HeadSymref == target && stored.Status == state.ImportRunFailed,
				"stored raw HEAD facts were lost: %+v", stored)
			assertImportDestinationAbsent(t, f)
			after := f.sourceRefs()
			require(t, reflect.DeepEqual(before, after), "source refs changed: before=%v after=%v", before, after)
			eq(t, "source HEAD changed", f.git(f.source, "symbolic-ref", "HEAD"), target)
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
		require(t, err != nil && problemCode(err) == CodeVerifyFailed, "blob HEAD result=%+v err=%v", result.Run, err)
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
		require(t, err != nil && problemCode(err) == CodeVerifyFailed,
			"false HEAD peel result=%+v err=%v", result.Run, err)
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
	eq(t, "replacement fixture did not mask original",
		f.git(f.source, "show", "refs/heads/main:file.txt"), "ordinary bytes")
	before := f.sourceRefs()

	// Without consent the masked pointer still requires it.
	result, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeLFSRequired,
		"replacement hid original LFS content: status=%s pointers=%d complete=%v err=%v", result.Run.Status, result.Run.LFSDetected, result.Run.LFSInspectionDone, err)
	assertImportDestinationAbsent(t, f)
	after := f.sourceRefs()
	require(t, reflect.DeepEqual(before, after), "source refs changed: before=%v after=%v", before, after)

	result = f.mustImport(ImportInput{GitOnlyConsent: true})
	require(t, result.Run.LFSDetected == 1 && result.Run.LFSInspectionDone,
		"replacement changed inspection=%+v", result.Run)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == original, "destination main=%s want %s", got, original)
	content := f.gitInput(f.destinationPath(), nil, "--no-replace-objects", "--git-dir", ".", "cat-file", "blob", original+":file.txt")
	require(t, bytes.Equal(content, []byte(pointer)),
		"original pointer bytes changed: got=%q want=%q", content, pointer)
	eq(t, "source replacement refs were published",
		f.git(f.destinationPath(), "--git-dir", ".", "for-each-ref", "--format=%(refname)", "refs/replace"), "")
	eq(t, "source replacement changed to", f.git(f.source, "rev-parse", "refs/replace/"+original), replacement)
}

func TestDestinationReplacementCannotStandInForMissingAdvertisedObject(t *testing.T) {
	f := newFixture(t)
	original := f.commit("source", "source bytes\n")
	_, err := f.manager.Create(context.Background(), "project", "")
	noErr(t, err)
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git",
	}); err != nil {
		t.Fatal(err)
	}
	destination := f.destinationPath()
	replacement := strings.TrimSpace(string(f.gitInput(destination, []byte("replacement bytes\n"),
		"--git-dir", ".", "hash-object", "-w", "--stdin")))
	f.git(destination, "--git-dir", ".", "update-ref", "refs/replace/"+original, replacement)
	eq(t, "replacement fixture type", f.git(destination, "--git-dir", ".", "cat-file", "-t", original), "blob")
	raw := f.gitMaybe(destination, "--no-replace-objects", "--git-dir", ".", "cat-file", "-t", original)
	require(t, raw == "", "destination unexpectedly contained original object: %q", raw)

	// Import refuses this existing destination. Refresh is the publication
	// that must still ignore the local replacement ref.
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh=%+v err=%v", run, err)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == original, "destination main=%s want %s", got, original)
	sourceObject := f.gitInput(f.source, nil, "--no-replace-objects", "cat-file", "commit", original)
	destinationObject := f.gitInput(destination, nil, "--no-replace-objects", "--git-dir", ".", "cat-file", "commit", original)
	require(t, bytes.Equal(destinationObject, sourceObject), "destination original object bytes differ from source")
	eq(t, "local replacement changed to",
		f.git(destination, "--git-dir", ".", "rev-parse", "refs/replace/"+original), replacement)
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
	noErr(t, os.Mkdir(staging, 0o700))
	limits, err := (Limits{}).effective()
	noErr(t, err)
	noErr(t, f.service.createStagingRepository(context.Background(), staging, importgit.FormatSHA1, limits))
	err = f.service.indexStagingPack(context.Background(), staging, bytes.NewReader(pack), limits)
	require(t, err != nil && problemCode(err) == CodeIndexFailed, "strict indexing error=%v", err)
}

func TestIncompleteLFSInspectionRequiresConsentForInitialImport(t *testing.T) {
	for _, test := range []struct {
		name     string
		limits   LFSLimits
		complete bool
	}{
		{"object count truncated", LFSLimits{MaxObjects: 1}, false},
		{"exact object cap", LFSLimits{MaxObjects: 3}, true},
		{"pointer size bound", LFSLimits{MaxPointerBytes: 16}, false},
		{"candidate byte bound", LFSLimits{MaxCandidateBytes: 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			tip := f.commit("ordinary", "ordinary content\n")
			limits := Limits{LFS: test.limits}
			if !test.complete {
				refused, err := f.importProject(ImportInput{Limits: limits})
				require(t, err != nil && problemCode(err) == CodeLFSRequired,
					"incomplete import result=%+v err=%v", refused.Run, err)
				require(t, refused.Run.LFSDetected == 0 && !refused.Run.LFSInspectionDone, "refused inspection=%+v", refused.Run)
				assertImportDestinationAbsent(t, f)
			}
			accepted := f.mustImport(ImportInput{GitOnlyConsent: !test.complete, Limits: limits})
			require(t, accepted.Run.Status == state.ImportRunComplete && accepted.Run.LFSDetected == 0 &&
				accepted.Run.LFSInspectionDone == test.complete, "consented run=%+v", accepted.Run)
			got := f.destinationRefs()["refs/heads/main"]
			require(t, got == tip, "destination main=%s want %s", got, tip)
			status, err := f.service.Status(context.Background(), "project")
			require(t, err == nil && status.Content.Incomplete == !test.complete && status.Content.InspectionComplete == test.complete &&
				status.Content.LFSDetected == 0, "content status=%+v err=%v", status.Content, err)
		})
	}
}

func TestLFSInspectionListingBounds(t *testing.T) {
	f := newFixture(t)
	f.commit("ordinary", "ordinary content\n")
	_, err := f.service.Prepare(context.Background())
	noErr(t, err)
	root, ok := f.service.preparedRuntime()
	require(t, ok, "runtime not prepared")
	objects := f.gitInput(f.source, nil, "rev-list", "--objects", "--no-object-names", "HEAD")
	types := f.gitInput(f.source, objects, "cat-file", "--batch-check")
	for _, test := range []struct {
		name        string
		objectBytes int64
		typeBytes   int64
		wantObjects int64
		complete    bool
	}{
		{"object record cut", 40, int64(len(types)), 0, false},
		{"object prefix", 82, int64(len(types)), 2, false},
		{"exact object and type byte caps", int64(len(objects)), int64(len(types)), 3, true},
		{"type record cut", int64(len(objects)), 40, 3, false},
		{"type prefix", int64(len(objects)), int64(len(types) - 1), 3, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			limits.LFS.MaxObjectListBytes = test.objectBytes
			limits.LFS.MaxTypeListBytes = test.typeBytes
			run := &runState{stagingPath: filepath.Join(f.source, ".git"), runtimeGeneration: root.generation,
				advertisement: f.transport.advertisement(), limits: limits}
			noErr(t, f.service.inspectLFS(context.Background(), run))
			require(t, run.inspection.Objects == test.wantObjects && run.inspection.Complete == test.complete &&
				run.inspection.Pointers == 0, "inspection=%+v", run.inspection)
		})
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
	require(t, err != nil && problemCode(err) == CodeLFSRequired, "incomplete refresh result=%+v err=%v", refused, err)
	require(t, refused.LFSDetected == 0 && !refused.LFSInspectionDone, "refused inspection=%+v", refused)
	after := f.destinationRefs()
	require(t, reflect.DeepEqual(after, beforeDestination) && after["refs/heads/main"] == first,
		"destination changed on refusal: before=%v after=%v", beforeDestination, after)
	after = f.sourceRefs()
	require(t, reflect.DeepEqual(after, beforeSource),
		"source changed on refusal: before=%v after=%v", beforeSource, after)
	refusedStatus, err := f.service.Status(context.Background(), "project")
	noErr(t, err)
	require(t, refusedStatus.LastRun != nil && refusedStatus.LastRun.Status == state.ImportRunFailed &&
		!refusedStatus.LastRun.LFSInspectionDone, "last attempt=%+v", refusedStatus.LastRun)
	require(t, refusedStatus.Content.InspectionComplete && !refusedStatus.Content.Incomplete &&
		refusedStatus.Content.LFSDetected == 0,
		"failed refresh replaced accepted content status: %+v", refusedStatus.Content)

	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", GitOnlyConsent: true,
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.service.Refresh(context.Background(), "project", limits)
	noErr(t, err, "consented refresh")
	require(t, accepted.Status == state.ImportRunComplete && accepted.LFSDetected == 0 && !accepted.LFSInspectionDone,
		"consented refresh=%+v", accepted)
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == second, "destination main=%s want %s", got, second)
	status, err := f.service.Status(context.Background(), "project")
	noErr(t, err)
	require(t, status.Content.Incomplete && !status.Content.InspectionComplete && status.Content.LFSDetected == 0,
		"content status=%+v", status.Content)
}

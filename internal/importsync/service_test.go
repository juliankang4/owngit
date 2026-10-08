package importsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

// fixture starts one service over fresh state, a bare-repository root, and a
// synthetic source. The fake transport replaces the HTTPS transport so tests
// exercise staging, verification, publication, and reconciliation for real.
type fixture struct {
	t         *testing.T
	root      string
	gitPath   string
	env       []string
	store     *state.Store
	manager   *repository.Manager
	service   *Service
	source    string
	format    string
	transport *fakeTransport
	now       time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Import Test", "GIT_AUTHOR_EMAIL=import@example.invalid",
		"GIT_COMMITTER_NAME=Import Test", "GIT_COMMITTER_EMAIL=import@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	env = testfixture.GitEnvironment(env)
	instance := &fixture{t: t, root: root, gitPath: runner.GitPath, env: env, store: store, manager: manager, now: time.Unix(1_800_000_000, 0).UTC()}
	instance.transport = &fakeTransport{fixture: instance}
	instance.service = &Service{
		Store: store, Repositories: manager, Fetch: instance.transport.fetch,
		Clock: func() time.Time { return instance.now },
	}
	t.Cleanup(func() { _ = instance.service.Close() })
	instance.source = filepath.Join(root, "source")
	instance.initSource()
	return instance
}

func (f *fixture) initSource() {
	f.t.Helper()
	arguments := []string{"init", "--initial-branch=main"}
	if f.format == "sha256" {
		arguments = append(arguments, "--object-format=sha256")
	}
	arguments = append(arguments, f.source)
	f.git("", arguments...)
}

func (f *fixture) git(directory string, arguments ...string) string {
	f.t.Helper()
	command := exec.Command(f.gitPath, arguments...)
	command.Dir = directory
	command.Env = f.env
	output, err := command.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimRight(string(output), "\n")
}

func (f *fixture) gitMaybe(directory string, arguments ...string) string {
	command := exec.Command(f.gitPath, arguments...)
	command.Dir = directory
	command.Env = f.env
	output, err := command.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(output), "\n")
}

func (f *fixture) commit(message, content string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.source, "file.txt"), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.source, "add", "file.txt")
	f.git(f.source, "commit", "-m", message)
	return f.git(f.source, "rev-parse", "HEAD")
}

func (f *fixture) sourceRefs() map[string]string {
	f.t.Helper()
	refs := map[string]string{}
	for _, line := range strings.Split(f.git(f.source, "for-each-ref", "--format=%(refname)%09%(objectname)", "refs/heads", "refs/tags"), "\n") {
		if line == "" {
			continue
		}
		name, oid, _ := strings.Cut(line, "\t")
		refs[name] = oid
	}
	return refs
}

func (f *fixture) destinationPath() string {
	f.t.Helper()
	path, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	if err != nil || !exists {
		f.t.Fatalf("destination exists=%v err=%v", exists, err)
	}
	return path
}

func (f *fixture) destinationRefs() map[string]string {
	f.t.Helper()
	records, _, err := f.manager.ReadRefs(context.Background(), f.destinationPath(), 0, "refs/heads", "refs/tags", "refs/owngit")
	if err != nil {
		f.t.Fatal(err)
	}
	refs := map[string]string{}
	for _, record := range records {
		refs[record.Name] = record.OID
	}
	return refs
}

func (f *fixture) lastRun() state.ImportRun {
	f.t.Helper()
	runs, _, err := f.store.ImportRuns(context.Background(), "project", 1)
	if err != nil || len(runs) == 0 {
		f.t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	return runs[0]
}

func (f *fixture) importProject(input ImportInput) (ImportResult, error) {
	f.t.Helper()
	return f.importProjectUnder(context.Background(), input)
}

// importProjectUnder imports the project with parent as the parent context of
// the run.
func (f *fixture) importProjectUnder(parent context.Context, input ImportInput) (ImportResult, error) {
	f.t.Helper()
	if input.Name == "" {
		input.Name = "project"
	}
	if input.URL == "" {
		input.URL = "https://example.invalid/team/project.git"
	}
	return f.service.Import(parent, input)
}

func (f *fixture) mustImport(input ImportInput) ImportResult {
	f.t.Helper()
	result, err := f.importProject(input)
	if err != nil {
		f.t.Fatalf("import failed: %v", err)
	}
	return result
}

func (f *fixture) refresh() (state.ImportRun, error) {
	f.t.Helper()
	return f.service.Refresh(context.Background(), "project", Limits{})
}

// headRef returns the immediate target of the destination HEAD.
func (f *fixture) headRef(path string) string {
	f.t.Helper()
	return f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD")
}

// rev resolves a ref in a destination repository.
func (f *fixture) rev(path, name string) string {
	f.t.Helper()
	return f.git(path, "--git-dir", ".", "rev-parse", name)
}

// symref returns the immediate target of a symbolic ref in a destination.
func (f *fixture) symref(path, name string) string {
	f.t.Helper()
	return f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", name)
}

// refreshFails runs a refresh that must fail with the given run status and,
// when code is not empty, the given problem code.
func (f *fixture) refreshFails(code, status string) state.ImportRun {
	f.t.Helper()
	run, err := f.refresh()
	require(f.t, err != nil && run.Status == status && (code == "" || problemCode(err) == code), "refresh run=%+v err=%v, want %s/%s", run, err, status, code)
	return run
}

// importOwned imports the source and marks the destination HEAD import-owned.
func (f *fixture) importOwned() {
	f.t.Helper()
	markHEADOwnedForTest(f.t, f, f.mustImport(ImportInput{}).Run.ID)
}

// ownedDevCheckout imports an owned destination with branches main and dev at
// one commit, checks dev out in the source and returns that commit.
func (f *fixture) ownedDevCheckout() string {
	f.t.Helper()
	old := f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.importOwned()
	f.git(f.source, "checkout", "--quiet", "dev")
	return old
}

// seedIntent stores an interrupted refresh run (id repeats the byte) with a
// planning publication intent (id repeats the next byte) and returns the intent.
func (f *fixture) seedIntent(id byte, expected, desired, observed map[string]string) state.ImportIntent {
	f.t.Helper()
	ctx := context.Background()
	run := state.ImportRun{
		ID: strings.Repeat(string(id), 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(f.t, f.store.BeginImportRun(ctx, run))
	run.Status, run.FinishedAt = state.ImportRunInterrupted, f.now
	noErr(f.t, f.store.FinishImportRun(ctx, run))
	intent := state.ImportIntent{
		ID: strings.Repeat(string(id+1), 32), RepositoryID: "project", RunID: run.ID,
		SourceGeneration: 1, AuthorityRevision: 1, Status: state.ImportIntentPlanning,
		Expected: expected, Desired: desired, Observed: observed, Retained: map[string]string{}, CreatedAt: f.now,
	}
	noErr(f.t, f.store.CreateImportIntent(ctx, intent))
	return intent
}

// fakeTransport turns the local synthetic source into transport facts and one
// complete pack, bypassing HTTPS without touching the process environment.
type fakeTransport struct {
	fixture          *fixture
	fail             error
	gate             chan struct{}
	before           func()
	mutateAdvertised func(*importgit.Advertisement)
	packOverride     func() []byte
	calls            int
	requests         []importfetch.Request
}

func (f *fakeTransport) fetch(ctx context.Context, request importfetch.Request, consume importfetch.PackConsumer) (*importfetch.Result, error) {
	f.calls++
	f.requests = append(f.requests, request)
	if f.fail != nil {
		return nil, f.fail
	}
	if f.before != nil {
		f.before()
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	advertisement := f.advertisement()
	if f.mutateAdvertised != nil {
		f.mutateAdvertised(advertisement)
	}
	if advertisement.Empty {
		return &importfetch.Result{Advertisement: advertisement}, nil
	}
	pack := f.pack()
	if f.packOverride != nil {
		pack = f.packOverride()
	}
	if consume != nil {
		if err := consume(ctx, advertisement, bytes.NewReader(pack)); err != nil {
			return nil, err
		}
	}
	return &importfetch.Result{Advertisement: advertisement, PackBytes: int64(len(pack)), HTTPBodyBytes: int64(len(pack))}, nil
}

func (f *fakeTransport) advertisement() *importgit.Advertisement {
	f.fixture.t.Helper()
	advertisement := &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1}
	if f.fixture.format == "sha256" {
		advertisement.ObjectFormat = importgit.FormatSHA256
	}
	output := f.fixture.git(f.fixture.source, "for-each-ref", "--format=%(refname)%09%(objectname)%09%(*objectname)")
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			f.fixture.t.Fatalf("unexpected for-each-ref line %q", line)
		}
		peeled := fields[2]
		if peeled != "" {
			// Older Git (2.39) peels %(*objectname) one level; upload-pack
			// advertises the fully peeled object, like this.
			peeled = strings.TrimSpace(f.fixture.git(f.fixture.source, "rev-parse", "--verify", fields[1]+"^{}"))
		}
		advertisement.Refs = append(advertisement.Refs, importgit.Ref{Name: fields[0], OID: fields[1], PeeledOID: peeled})
	}
	symref := f.fixture.gitMaybe(f.fixture.source, "symbolic-ref", "-q", "HEAD")
	headOID := f.fixture.gitMaybe(f.fixture.source, "rev-parse", "--verify", "HEAD^{commit}")
	if symref != "" {
		advertisement.Head.SymrefTarget = symref
	}
	if headOID != "" {
		advertisement.Refs = append(advertisement.Refs, importgit.Ref{Name: "HEAD", OID: headOID})
		advertisement.Head.Advertised = true
		advertisement.Head.OID = headOID
	}
	if len(advertisement.Refs) == 0 {
		advertisement.Empty = true
	}
	return advertisement
}

func (f *fakeTransport) pack() []byte {
	f.fixture.t.Helper()
	command := exec.Command(f.fixture.gitPath, "--no-replace-objects", "-C", f.fixture.source, "pack-objects", "--stdout", "--all")
	command.Env = f.fixture.env
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		f.fixture.t.Fatalf("pack-objects: %v\n%s", err, stderr.String())
	}
	return stdout.Bytes()
}

func TestImportPublishesBranchesAndTagsExactly(t *testing.T) {
	f := newFixture(t)
	main := f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.git(f.source, "tag", "-a", "v1", "-m", "release one")
	f.git(f.source, "update-ref", "refs/notes/commits", main)
	f.git(f.source, "update-ref", "refs/owngit/keep", main)

	result := f.mustImport(ImportInput{Description: "imported"})
	require(t, result.Run.Status == state.ImportRunComplete && result.Run.ErrorClass == "", "run=%+v", result.Run)
	require(t, result.Run.RefsCreated == 3 && result.Run.RefsSkipped == 2 && result.Run.RefsDivergent == 0,
		"counts created=%d skipped=%d divergent=%d", result.Run.RefsCreated, result.Run.RefsSkipped, result.Run.RefsDivergent)
	require(t, result.Run.HeadSymref == "refs/heads/main" && result.Run.ObjectFormat == importgit.FormatSHA1,
		"head=%q format=%q", result.Run.HeadSymref, result.Run.ObjectFormat)
	destination := f.destinationRefs()
	source := f.sourceRefs()
	for ref, oid := range source {
		require(t, destination[ref] == oid, "destination %s=%s want %s", ref, destination[ref], oid)
	}
	require(t, len(destination) == len(source),
		"destination refs=%v source refs=%v", sortedKeys(destination), sortedKeys(source))
	headSymref, _, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	require(t, err == nil && headSymref == "refs/heads/main", "destination HEAD=%q err=%v", headSymref, err)
	// The source repository was never written to.
	eq(t, "source worktree is dirty", f.git(f.source, "status", "--porcelain"), "")
	// An initial import creates no check consent or attempt state.
	for _, table := range []string{"check_policies", "check_jobs", "check_attempts"} {
		count, err := f.store.TableRowCount(context.Background(), table)
		if err != nil {
			// Some tables may only exist after later migrations.
			continue
		}
		require(t, count == 0, "%s has %d rows after an import", table, count)
	}
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	require(t, err == nil && len(observations) == 4, "observations=%d err=%v", len(observations), err)
	// The run staging is removed. Only the two private regular files that define
	// the prepared runtime root remain; no run directory or unknown entry does.
	entries, err := os.ReadDir(f.service.stagingRootPath())
	noErr(t, err, "read staging root")
	expectedMetadata := map[string]bool{
		runtimeRootMarkerName: false,
		runtimeRootLockName:   false,
	}
	require(t, len(entries) == len(expectedMetadata), "staging entries=%v", entries)
	for _, entry := range entries {
		_, expected := expectedMetadata[entry.Name()]
		require(t, expected, "unexpected staging entry %q", entry.Name())
		path := filepath.Join(f.service.stagingRootPath(), entry.Name())
		info, err := os.Lstat(path)
		require(t, err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0,
			"runtime metadata %q is not a regular file: info=%v err=%v", entry.Name(), info, err)
		err = state.ValidatePrivateFile(path)
		require(t, err == nil, "runtime metadata %q is not private: %v", entry.Name(), err)
		expectedMetadata[entry.Name()] = true
	}
	for name, found := range expectedMetadata {
		require(t, found, "runtime metadata %q is missing", name)
	}
}

func TestEmptySourceImportPublishesNothingHonestly(t *testing.T) {
	f := newFixture(t)
	result := f.mustImport(ImportInput{})
	require(t, result.Run.Status == state.ImportRunComplete && result.Run.RefsSeen == 0, "run=%+v", result.Run)
	refs := f.destinationRefs()
	require(t, len(refs) == 0, "empty import created refs=%v", refs)
}

func TestLFSGateNeedsExplicitGitOnlyConsent(t *testing.T) {
	f := newFixture(t)
	digest := strings.Repeat("a", 64)
	pointer := "version https://hawser.github.com/spec/v1\r\n" +
		"ext-0-transform sha256:" + digest + "\r\n" +
		"oid sha256:" + digest + "\r\nsize 1234\r\n"
	f.commit("pointer", pointer)

	refused, err := f.importProject(ImportInput{})
	require(t, err != nil, "import without consent succeeded: %+v", refused.Run)
	code := problemCode(err)
	require(t, code == CodeLFSRequired, "refusal code=%s err=%v", code, err)
	require(t, refused.Run.Status == state.ImportRunFailed && refused.Run.ErrorClass == CodeLFSRequired,
		"refused run=%+v", refused.Run)
	_, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	require(t, err == nil && !exists, "destination exists=%v err=%v", exists, err)

	accepted := f.mustImport(ImportInput{GitOnlyConsent: true})
	require(t, accepted.Run.Status == state.ImportRunComplete && accepted.Run.LFSDetected == 1 &&
		accepted.Run.LFSInspectionDone && accepted.Run.LFSScannedBlobs == 1 &&
		accepted.Run.LFSScannedBytes == int64(len(pointer)), "consented run=%+v", accepted.Run)
	refs := f.destinationRefs()
	require(t, len(refs) == 1, "published refs=%v", refs)
	content := f.gitInput(f.destinationPath(), nil, "--git-dir", ".", "cat-file", "blob", "refs/heads/main:file.txt")
	require(t, bytes.Equal(content, []byte(pointer)), "pointer bytes changed: got %q want %q", content, pointer)
	status, err := f.service.Status(context.Background(), "project")
	require(t, err == nil && status.Content.Incomplete && status.Content.InspectionComplete &&
		status.Content.LFSDetected == 1, "content status=%+v err=%v", status.Content, err)
}

func TestRefreshReplacesRewrittenTipAndRetainsHistory(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	// A local ref the source never had is kept by the refresh.
	f.git(f.destinationPath(), "update-ref", "refs/heads/local-only", first)
	// Amend the root commit so the new tip is not a descendant of the imported
	// history. The replaced tip must survive through retention refs.
	noErr(t, os.WriteFile(filepath.Join(f.source, "file.txt"), []byte("rewritten\n"), 0o600))
	f.git(f.source, "add", "file.txt")
	f.git(f.source, "commit", "--amend", "-m", "rewritten root")
	third := f.git(f.source, "rev-parse", "HEAD")
	require(t, first != third && !strings.Contains(f.git(f.source, "rev-list", "--all"), first),
		"old tip first=%s new=%s is still reachable", first, third)

	run, err := f.refresh()
	noErr(t, err, "refresh")
	require(t, run.Status == state.ImportRunComplete && run.RefsUpdated == 1, "refresh run=%+v", run)
	refs := f.destinationRefs()
	require(t, refs["refs/heads/main"] == third, "main=%s want %s", refs["refs/heads/main"], third)
	retained := refs[repository.RetainedRefName("heads", first)]
	require(t, retained == first, "retained ref=%q refs=%v", retained, sortedKeys(refs))
	require(t, refs[repository.ProvenanceRefName("heads", "main", first)] == first,
		"provenance ref missing: %v", sortedKeys(refs))
	eq(t, "unrelated local ref", refs["refs/heads/local-only"], first)
	require(t, f.gitMaybe(f.destinationPath(), "rev-parse", "--verify", first+"^{commit}") != "",
		"retained object %s is unreachable", first)
	// The observation now records the new source tip.
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	require(t, err == nil && len(observations) == 2, "observations=%+v err=%v", observations, err)
	for _, observation := range observations {
		require(t, observation.RefName != "refs/heads/main" || observation.OID == third,
			"main observation=%s want %s", observation.OID, third)
	}
}

// A local push to a branch the source also moved, or to a branch the source
// left alone, keeps the local value and counts one divergent ref; the status
// lists it as diverged and other branches still follow the source.
func TestRefreshLeavesLocallyPushedBranchDiverged(t *testing.T) {
	for _, test := range []struct {
		name, branch      string
		sourceMovesBranch bool
	}{{"branch only the owner moved", "dev", false}, {"branch both moved", "main", true}} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			f.git(f.source, "branch", "dev")
			f.mustImport(ImportInput{})
			localTip := f.localWork(test.branch, "local\n")
			f.commit("two", "two\n")
			run, err := f.refresh()
			noErr(t, err, "refresh")
			updated := int64(1)
			if test.sourceMovesBranch {
				updated = 0
			}
			require(t, run.Status == state.ImportRunComplete && run.RefsDivergent == 1 && run.RefsUpdated == updated,
				"refresh run=%+v", run)
			refs := f.destinationRefs()
			eq(t, "local "+test.branch, refs["refs/heads/"+test.branch], localTip)
			if !test.sourceMovesBranch {
				eq(t, "main follows the source",
					refs["refs/heads/main"], f.git(f.source, "rev-parse", "refs/heads/main"))
			}
			eq(t, "diverged state", f.refState("refs/heads/"+test.branch), "diverged")
		})
	}
}

func TestImportRequestCarriesOnlyBoundCredentials(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	result, err := f.service.Import(context.Background(), ImportInput{
		Name: "project", URL: "https://example.invalid/team/project.git",
		AllowPrivateNetwork: true,
		Credentials:         &Credentials{Username: "user", Password: "secret"},
	})
	noErr(t, err)
	require(t, len(f.transport.requests) == 1, "transport calls=%d", len(f.transport.requests))
	request := f.transport.requests[0]
	require(t, request.Authentication.Basic != nil && request.Authentication.Basic.Password == "secret" &&
		request.AllowPrivateNetwork, "request=%+v", request)
	// A changed source URL must not receive the old credential.
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/other/project.git",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.service.Refresh(context.Background(), "project", Limits{})
	noErr(t, err)
	require(t, len(f.transport.requests) == 2, "transport calls=%d", len(f.transport.requests))
	second := f.transport.requests[1]
	require(t, second.Authentication.Basic == nil && second.URL == "https://example.invalid/other/project.git",
		"second request=%+v", second)
	require(t, result.Run.SourceGeneration == 1, "first run generation=%d", result.Run.SourceGeneration)
}

func TestSupersededSourcePreventsStalePublication(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.transport.before = func() {
		if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
			RepositoryID: "project", URL: "https://example.invalid/moved/project.git",
		}); err != nil {
			t.Errorf("configure during fetch: %v", err)
		}
	}
	f.git(f.source, "update-ref", "refs/heads/main", old)
	newTip := f.commit("two", "two\n")
	run, err := f.refresh()
	require(t, err != nil && problemCode(err) == CodeSuperseded, "refresh err=%v", err)
	require(t, run.Status == state.ImportRunSuperseded, "run=%+v", run)
	eq(t, "stale publication applied", f.destinationRefs()["refs/heads/main"], old)
	_ = newTip
}

func TestCancelPreventsPublication(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.transport.gate = make(chan struct{}, 1)
	f.transport.before = func() {
		if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}
	done := make(chan struct{})
	var run state.ImportRun
	var runErr error
	go func() {
		defer close(done)
		run, runErr = f.refresh()
	}()
	f.commit("two", "two\n")
	f.transport.gate <- struct{}{}
	<-done
	require(t, runErr != nil && problemCode(runErr) == CodeCancelled, "refresh err=%v", runErr)
	require(t, run.Status == state.ImportRunCancelled && run.ErrorClass == CodeCancelled, "run=%+v", run)
	if refs := f.destinationRefs(); refs["refs/heads/main"] != first {
		// The cancelled refresh staged the new pack but never published.
		t.Fatalf("cancelled run published: %v", refs["refs/heads/main"])
	}
	active, exists, err := f.store.ActiveImportRun(context.Background(), "project")
	require(t, err == nil && !exists, "active run=%+v exists=%v err=%v", active, exists, err)
}

// An interrupted refresh whose destination holds the desired refs (the process
// stopped after the Git write, before the receipt) is confirmed and its run
// completed. When the destination holds neither the expected nor the desired
// value, the intent and the run are unresolved and the ref is left alone.
func TestReconcileSettlesUnfinishedIntent(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name := "mismatch is unresolved"
		if confirmed {
			name = "desired refs are confirmed"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			old := f.commit("one", "one\n")
			f.mustImport(ImportInput{})
			path := f.destinationPath()
			newTip := f.commit("two", "two\n")
			desired := newTip
			if confirmed {
				// Push the object and ref so the destination holds neither the
				// recorded expected value nor any receipt.
				f.git(f.source, "push", "--quiet", path, newTip+":refs/heads/main", newTip+":refs/heads/staged")
			} else {
				f.git(f.source, "push", "--quiet", path, newTip+":refs/heads/main")
				desired = strings.Repeat("b", 40)
			}
			head := func(oid string) string {
				return (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}).encode()
			}
			intent := f.seedIntent('1',
				map[string]string{"refs/heads/main": old, state.ImportHeadRef: head(old)},
				map[string]string{"refs/heads/main": desired, state.ImportHeadRef: head(desired)},
				map[string]string{"refs/heads/main": desired, state.ImportHeadRef: head(desired)})
			err := f.service.Reconcile(context.Background())
			stored, exists, readErr := f.store.ImportIntent(context.Background(), intent.ID)
			noErr(t, readErr)
			run, _, readErr := f.store.ImportRun(context.Background(), intent.RunID)
			noErr(t, readErr)
			if confirmed {
				noErr(t, err, "reconcile")
				require(t, exists && stored.Status == state.ImportIntentComplete && stored.ReceiptJSON != "" &&
					stored.ReceiptDigest != "", "intent=%+v exists=%v", stored, exists)
				eq(t, "promoted run status", run.Status, state.ImportRunComplete)
				return
			}
			require(t, err != nil && problemCode(err) == CodeUnresolved,
				"mismatched intent reconciliation error=%v", err)
			eq(t, "independent destination ref", f.destinationRefs()["refs/heads/main"], newTip)
			eq(t, "intent status", stored.Status, state.ImportIntentUnresolved)
			eq(t, "run status", run.Status, state.ImportRunUnresolved)
		})
	}
}

func TestReconcilePreservesUnownedStagingContent(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	unknown := filepath.Join(f.service.stagingRootPath(), "dangling")
	noErr(t, os.Mkdir(unknown, 0o700))
	noErr(t, os.WriteFile(filepath.Join(unknown, "keep.txt"), []byte("keep"), 0o600))
	valid := filepath.Join(f.service.stagingRootPath(), "run-"+strings.Repeat("5", 32))
	noErr(t, os.Mkdir(valid, 0o700))
	noErr(t, f.service.Reconcile(context.Background()), "reconcile")
	_, err := os.Stat(filepath.Join(unknown, "keep.txt"))
	noErr(t, err, "unowned content removed")
	_, err = os.Stat(valid)
	noErr(t, err, "markerless staging removed")
	row, exists, err := f.store.ImportStaging(context.Background(), filepath.Base(valid))
	require(t, err == nil && exists && row.State == state.ImportStagingUnknown,
		"staging row=%+v exists=%v err=%v", row, exists, err)
}

func TestImportMatchesSha256SourceFormat(t *testing.T) {
	f := newFixture(t)
	noErr(t, os.RemoveAll(f.source))
	f.format = "sha256"
	f.initSource()
	f.commit("one", "one\n")
	result := f.mustImport(ImportInput{})
	require(t, result.Run.Status == state.ImportRunComplete && result.Run.ObjectFormat == importgit.FormatSHA256,
		"run=%+v", result.Run)
	format, err := f.manager.ObjectFormat(context.Background(), f.destinationPath())
	require(t, err == nil && format == importgit.FormatSHA256, "destination format=%q err=%v", format, err)
	refs := f.destinationRefs()
	require(t, len(refs) == 1, "refs=%v", refs)
}

func TestBusyRefusalDoesNotRecordASecondRun(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.transport.gate = make(chan struct{}, 1)
	started := make(chan struct{})
	done := make(chan state.ImportRun, 1)
	f.transport.before = func() { close(started) }
	go func() {
		run, err := f.refresh()
		if err != nil {
			t.Errorf("refresh: %v", err)
		}
		done <- run
	}()
	<-started
	_, err := f.refresh()
	require(t, problemCode(err) == CodeBusy, "second refresh err=%v", err)
	f.transport.gate <- struct{}{}
	run := <-done
	require(t, run.Status == state.ImportRunComplete, "first run=%+v", run)
}

func startDeadlineAtLockWait(caller *hookDeadline, deadline time.Duration) *time.Timer {
	return time.AfterFunc(deadline, func() {
		caller.once.Do(func() { close(caller.done) })
	})
}

// Every pre-write wait for a repository held by another writer ends at the
// caller's own deadline or cancellation: a source change, a credential change,
// a refresh, an import add and an orphan cleanup report that end instead of
// waiting for the writer, and they record, publish or remove nothing. An
// expired deadline is a busy repository; a cancelled caller stays cancelled.
func TestPreWriteRepositoryWaitsEndAtTheCallerDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("each case waits out a 500ms caller deadline behind a held repository writer")
	}
	const deadline = 500 * time.Millisecond

	run := func(t *testing.T, lock *gitexec.RepositoryLock, cancelInstead bool, start func(context.Context) error) error {
		t.Helper()
		caller := newHookDeadline()
		ctx, cancel := context.WithCancel(caller)
		defer cancel()
		var result error
		done := make(chan struct{})
		go func() {
			defer close(done)
			result = start(ctx)
		}()
		defer func() {
			// Free the writer and let the operation drain before the fixture ends.
			lock.Unlock()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}()
		waitUntil(t, "the operation to wait for another repository writer", lock.Waiting)
		if cancelInstead {
			cancel()
		} else {
			timer := startDeadlineAtLockWait(caller, deadline)
			defer timer.Stop()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the operation kept waiting for the writer past its deadline")
		}
		return result
	}

	t.Run("source change", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.mustImport(ImportInput{})
		lock := f.manager.Locks.For("project")
		lock.Lock()
		err := run(t, lock, false, func(ctx context.Context) error {
			_, err := f.service.ConfigureSource(ctx, ConfigureInput{
				RepositoryID: "project", URL: "https://example.invalid/moved/project.git", Mode: ModeStandalone,
			})
			return err
		})
		require(t, errors.Is(err, context.DeadlineExceeded) && problemCode(err) == CodeBusy,
			"a source change queued behind a writer ended with %v", err)
		source, exists, readErr := f.store.ImportSource(context.Background(), "project")
		require(t, readErr == nil && exists && source.URL == "https://example.invalid/team/project.git",
			"the stopped source change stored %+v (exists=%v err=%v)", source, exists, readErr)
	})

	t.Run("source change whose caller leaves", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.mustImport(ImportInput{})
		lock := f.manager.Locks.For("project")
		lock.Lock()
		err := run(t, lock, true, func(ctx context.Context) error {
			_, err := f.service.ConfigureSource(ctx, ConfigureInput{
				RepositoryID: "project", URL: "https://example.invalid/moved/project.git", Mode: ModeStandalone,
			})
			return err
		})
		require(t, errors.Is(err, context.Canceled) && problemCode(err) == CodeCancelled,
			"a source change whose caller left ended with %v", err)
		source, exists, readErr := f.store.ImportSource(context.Background(), "project")
		require(t, readErr == nil && exists && source.URL == "https://example.invalid/team/project.git",
			"the abandoned source change stored %+v (exists=%v err=%v)", source, exists, readErr)
	})

	t.Run("credential change", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.mustImport(ImportInput{})
		noErr(t, f.service.SetCredentials(context.Background(),
			"project", &Credentials{BearerToken: "synthetic-token"}))
		lock := f.manager.Locks.For("project")
		lock.Lock()
		err := run(t, lock, false, func(ctx context.Context) error {
			return f.service.SetCredentials(ctx, "project", nil)
		})
		require(t, errors.Is(err, context.DeadlineExceeded) && problemCode(err) == CodeBusy,
			"a credential change queued behind a writer ended with %v", err)
		_, stored, readErr := f.store.LoadImportCredentials(context.Background(), "project")
		require(t, readErr == nil && stored,
			"the stopped credential change removed the stored credential (stored=%v err=%v)", stored, readErr)
	})

	t.Run("import add", func(t *testing.T) {
		f := newFixture(t)
		lock := f.manager.Locks.For("queued")
		lock.Lock()
		err := run(t, lock, false, func(ctx context.Context) error {
			_, err := f.service.Import(ctx, ImportInput{
				Name: "queued", URL: "https://example.invalid/team/queued.git", Mode: ModeStandalone,
			})
			return err
		})
		require(t, problemCode(err) == CodeBusy, "an import add queued behind a writer ended with %v", err)
		assertNoBinding(t, f, "queued")
	})

	t.Run("orphan cleanup", func(t *testing.T) {
		f := newFixture(t)
		bindOrphan(t, f, "orphan")
		lock := f.manager.Locks.For("orphan")
		lock.Lock()
		var forgotten bool
		err := run(t, lock, false, func(ctx context.Context) error {
			var err error
			forgotten, err = f.service.ForgetOrphanImport(ctx, "orphan")
			return err
		})
		require(t, problemCode(err) == CodeBusy && !forgotten,
			"an orphan cleanup queued behind a writer ended with forgotten=%v err=%v", forgotten, err)
		_, exists, readErr := f.store.ImportSource(context.Background(), "orphan")
		require(t, readErr == nil && exists,
			"the stopped orphan cleanup removed the binding (exists=%v err=%v)", exists, readErr)
	})

	t.Run("refresh", func(t *testing.T) {
		f := newFixture(t)
		first := f.commit("one", "one\n")
		f.mustImport(ImportInput{})
		f.commit("two", "two\n")
		lock := f.manager.Locks.For("project")
		lock.Lock()
		err := run(t, lock, false, func(ctx context.Context) error {
			_, err := f.service.Refresh(ctx, "project", Limits{})
			return err
		})
		require(t, errors.Is(err, context.DeadlineExceeded) && problemCode(err) == CodeLimit,
			"a refresh queued behind a writer ended with %v", err)
		run := f.lastRun()
		require(t, run.Status == state.ImportRunFailed && run.ErrorClass == CodeLimit,
			"the stopped refresh recorded %+v", run)
		eq(t, "main after the stopped refresh", f.destinationRefs()["refs/heads/main"], first)
	})
}

// A refresh that waits for a repository held by another writer stops with its
// run context before publication instead of waiting for the writer, publishes
// nothing, and its pack keep cleanup gives up on the held repository within
// its own bound instead of following the stopped run forever.
func TestRefreshWaitingForTheRepositoryAtPublicationStopsWithTheRun(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 5s pack keep cleanup bound behind a writer that stays held")
	}
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	// The refresh indexes a destination pack before it waits for the writer, so
	// the keep file of that pack exists while the writer is held and the cleanup
	// that follows the publication wait must take the same lock.

	f.transport.gate = make(chan struct{}, 1)
	fetched := make(chan struct{})
	f.transport.before = func() { close(fetched) }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var run state.ImportRun
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		run, runErr = f.service.Refresh(ctx, "project", Limits{})
	}()
	<-fetched
	lock := f.manager.Locks.For("project")
	lock.Lock()
	defer lock.Unlock()
	f.transport.gate <- struct{}{}
	waitUntil(t, "the refresh to wait for the repository writer at publication", lock.Waiting)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the refresh kept waiting for the writer instead of stopping with its run")
	}
	require(t, problemCode(runErr) == CodeCancelled && run.Status == state.ImportRunCancelled,
		"run=%+v err=%v", run, runErr)
	eq(t, "main after the stopped refresh", f.destinationRefs()["refs/heads/main"], first)
	// The cleanup waited for the same writer, gave up within its bound, and left
	// the keep file that names the run which created it.
	keeps := destinationKeepFiles(t, f.destinationPath())
	require(t, len(keeps) == 1, "keep files after the stopped refresh=%v, want the one it left", keeps)
	content, err := os.ReadFile(filepath.Join(f.destinationPath(), "objects", "pack", keeps[0]))
	noErr(t, err)
	want := "owngit import " + run.ID + "\n"
	require(t, string(content) == want, "left keep file content=%q want %q", content, want)
}

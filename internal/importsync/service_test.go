package importsync

import (
	"bytes"
	"context"
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	repositoriesRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Import Test", "GIT_AUTHOR_EMAIL=import@example.invalid",
		"GIT_COMMITTER_NAME=Import Test", "GIT_COMMITTER_EMAIL=import@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
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
	if input.Name == "" {
		input.Name = "project"
	}
	if input.URL == "" {
		input.URL = "https://example.invalid/team/project.git"
	}
	return f.service.Import(context.Background(), input)
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
	if result.Run.Status != state.ImportRunComplete || result.Run.ErrorClass != "" {
		t.Fatalf("run=%+v", result.Run)
	}
	if result.Run.RefsCreated != 3 || result.Run.RefsSkipped != 2 || result.Run.RefsDivergent != 0 {
		t.Fatalf("counts created=%d skipped=%d divergent=%d", result.Run.RefsCreated, result.Run.RefsSkipped, result.Run.RefsDivergent)
	}
	if result.Run.HeadSymref != "refs/heads/main" || result.Run.ObjectFormat != importgit.FormatSHA1 {
		t.Fatalf("head=%q format=%q", result.Run.HeadSymref, result.Run.ObjectFormat)
	}
	destination := f.destinationRefs()
	source := f.sourceRefs()
	for ref, oid := range source {
		if destination[ref] != oid {
			t.Fatalf("destination %s=%s want %s", ref, destination[ref], oid)
		}
	}
	if len(destination) != len(source) {
		t.Fatalf("destination refs=%v source refs=%v", sortedKeys(destination), sortedKeys(source))
	}
	headSymref, _, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	if err != nil || headSymref != "refs/heads/main" {
		t.Fatalf("destination HEAD=%q err=%v", headSymref, err)
	}
	// The source repository was never written to.
	if dirty := f.git(f.source, "status", "--porcelain"); dirty != "" {
		t.Fatalf("source worktree is dirty: %q", dirty)
	}
	// An initial import creates no check consent or attempt state.
	for _, table := range []string{"check_policies", "check_jobs", "check_attempts"} {
		count, err := f.store.TableRowCount(context.Background(), table)
		if err != nil {
			// Some tables may only exist after later migrations.
			continue
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after an import", table, count)
		}
	}
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	if err != nil || len(observations) != 4 {
		t.Fatalf("observations=%d err=%v", len(observations), err)
	}
	// The run staging is removed. Only the two private regular files that define
	// the prepared runtime root remain; no run directory or unknown entry does.
	entries, err := os.ReadDir(f.service.stagingRootPath())
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	expectedMetadata := map[string]bool{
		runtimeRootMarkerName: false,
		runtimeRootLockName:   false,
	}
	if len(entries) != len(expectedMetadata) {
		t.Fatalf("staging entries=%v", entries)
	}
	for _, entry := range entries {
		if _, expected := expectedMetadata[entry.Name()]; !expected {
			t.Fatalf("unexpected staging entry %q", entry.Name())
		}
		path := filepath.Join(f.service.stagingRootPath(), entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("runtime metadata %q is not a regular file: info=%v err=%v", entry.Name(), info, err)
		}
		if err := state.ValidatePrivateFile(path); err != nil {
			t.Fatalf("runtime metadata %q is not private: %v", entry.Name(), err)
		}
		expectedMetadata[entry.Name()] = true
	}
	for name, found := range expectedMetadata {
		if !found {
			t.Fatalf("runtime metadata %q is missing", name)
		}
	}
}

func TestImportKeepsUnrelatedLocalRefs(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	// Import refuses a repository that already exists. The local ref is added
	// after the initial publication, which is the case this test observes.
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	f.git(path, "update-ref", "refs/heads/local-only", first)
	// Rewrite upstream so the refresh publishes a new value for main.
	if err := os.WriteFile(filepath.Join(f.source, "file.txt"), []byte("rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "add", "file.txt")
	f.git(f.source, "commit", "--amend", "-m", "rewritten")
	if _, err := f.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	refs := f.destinationRefs()
	if refs["refs/heads/local-only"] != first {
		t.Fatalf("unrelated local ref was lost: %v", refs)
	}
	if refs["refs/heads/main"] == first {
		t.Fatalf("main did not follow upstream: %v", refs)
	}
}

func TestEmptySourceImportPublishesNothingHonestly(t *testing.T) {
	f := newFixture(t)
	result := f.mustImport(ImportInput{})
	if result.Run.Status != state.ImportRunComplete || result.Run.RefsSeen != 0 {
		t.Fatalf("run=%+v", result.Run)
	}
	refs := f.destinationRefs()
	if len(refs) != 0 {
		t.Fatalf("empty import created refs=%v", refs)
	}
}

func TestLFSGateNeedsExplicitGitOnlyConsent(t *testing.T) {
	f := newFixture(t)
	digest := strings.Repeat("a", 64)
	pointer := "version https://hawser.github.com/spec/v1\r\n" +
		"ext-0-transform sha256:" + digest + "\r\n" +
		"oid sha256:" + digest + "\r\nsize 1234\r\n"
	f.commit("pointer", pointer)

	refused, err := f.importProject(ImportInput{})
	if err == nil {
		t.Fatalf("import without consent succeeded: %+v", refused.Run)
	}
	if code := problemCode(err); code != CodeLFSRequired {
		t.Fatalf("refusal code=%s err=%v", code, err)
	}
	if refused.Run.Status != state.ImportRunFailed || refused.Run.ErrorClass != CodeLFSRequired {
		t.Fatalf("refused run=%+v", refused.Run)
	}
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("destination exists=%v err=%v", exists, err)
	}

	accepted := f.mustImport(ImportInput{GitOnlyConsent: true})
	if accepted.Run.Status != state.ImportRunComplete || accepted.Run.LFSDetected != 1 || !accepted.Run.LFSInspectionDone ||
		accepted.Run.LFSScannedBlobs != 1 || accepted.Run.LFSScannedBytes != int64(len(pointer)) {
		t.Fatalf("consented run=%+v", accepted.Run)
	}
	if refs := f.destinationRefs(); len(refs) != 1 {
		t.Fatalf("published refs=%v", refs)
	}
	content := f.gitInput(f.destinationPath(), nil, "--git-dir", ".", "cat-file", "blob", "refs/heads/main:file.txt")
	if !bytes.Equal(content, []byte(pointer)) {
		t.Fatalf("pointer bytes changed: got %q want %q", content, pointer)
	}
	if !accepted.Status.Content.Incomplete || !accepted.Status.Content.InspectionComplete || accepted.Status.Content.LFSDetected != 1 {
		t.Fatalf("content status=%+v", accepted.Status.Content)
	}
}

func TestRefreshReplacesRewrittenTipAndRetainsHistory(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	// Amend the root commit so the new tip is not a descendant of the imported
	// history. The replaced tip must survive through retention refs.
	if err := os.WriteFile(filepath.Join(f.source, "file.txt"), []byte("rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "add", "file.txt")
	f.git(f.source, "commit", "--amend", "-m", "rewritten root")
	third := f.git(f.source, "rev-parse", "HEAD")
	if first == third || strings.Contains(f.git(f.source, "rev-list", "--all"), first) {
		t.Fatalf("old tip first=%s new=%s is still reachable", first, third)
	}

	run, err := f.refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.Status != state.ImportRunComplete || run.RefsUpdated != 1 {
		t.Fatalf("refresh run=%+v", run)
	}
	refs := f.destinationRefs()
	if refs["refs/heads/main"] != third {
		t.Fatalf("main=%s want %s", refs["refs/heads/main"], third)
	}
	retained := refs[repository.RetainedRefName("heads", first)]
	if retained != first {
		t.Fatalf("retained ref=%q refs=%v", retained, sortedKeys(refs))
	}
	if refs[repository.ProvenanceRefName("heads", "main", first)] != first {
		t.Fatalf("provenance ref missing: %v", sortedKeys(refs))
	}
	if f.gitMaybe(f.destinationPath(), "rev-parse", "--verify", first+"^{commit}") == "" {
		t.Fatalf("retained object %s is unreachable", first)
	}
	// The observation now records the new source tip.
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	if err != nil || len(observations) != 2 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	for _, observation := range observations {
		if observation.RefName == "refs/heads/main" && observation.OID != third {
			t.Fatalf("main observation=%s want %s", observation.OID, third)
		}
	}
}

func TestRefreshLeavesLocalAheadBranchDiverged(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")

	work := filepath.Join(f.root, "work")
	f.git("", "clone", "--quiet", f.destinationPath(), work)
	f.git(work, "checkout", "--quiet", "dev")
	if err := os.WriteFile(filepath.Join(work, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "local.txt")
	f.git(work, "commit", "-m", "local only")
	localTip := f.git(work, "rev-parse", "HEAD")
	f.git(work, "push", "--quiet", "origin", "dev")

	run, err := f.refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.Status != state.ImportRunComplete || run.RefsDivergent != 1 {
		t.Fatalf("refresh run=%+v", run)
	}
	refs := f.destinationRefs()
	if refs["refs/heads/dev"] != localTip {
		t.Fatalf("local dev=%s want %s", refs["refs/heads/dev"], localTip)
	}
	if refs["refs/heads/main"] != f.git(f.source, "rev-parse", "refs/heads/main") {
		t.Fatalf("main did not follow the source: %v", refs["refs/heads/main"])
	}
}

func TestRefreshDivergesWhenLocalAndSourceBothMoved(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})

	work := filepath.Join(f.root, "work")
	f.git("", "clone", "--quiet", f.destinationPath(), work)
	if err := os.WriteFile(filepath.Join(work, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "local.txt")
	f.git(work, "commit", "-m", "local only")
	localTip := f.git(work, "rev-parse", "HEAD")
	f.git(work, "push", "--quiet", "origin", "main")

	sourceTip := f.commit("source rewrite", "rewritten\n")
	if strings.Contains(f.git(f.source, "rev-list", "--all"), sourceTip) == false {
		t.Fatal("source tip missing")
	}

	run, err := f.refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.Status != state.ImportRunComplete || run.RefsDivergent != 1 || run.RefsUpdated != 0 {
		t.Fatalf("refresh run=%+v", run)
	}
	if refs := f.destinationRefs(); refs["refs/heads/main"] != localTip {
		t.Fatalf("divergent local branch was overwritten: %v", refs["refs/heads/main"])
	}
	status, err := f.service.Status(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	var divergent int
	for _, ref := range status.Refs {
		if ref.State == "diverged" {
			divergent++
		}
	}
	if divergent != 1 {
		t.Fatalf("status divergent=%d refs=%+v", divergent, status.Refs)
	}
}

func TestRefreshRetainsLocallyDeletedUpstreamRef(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	f.git(f.source, "branch", "-D", "dev")

	run, err := f.refresh()
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if run.RefsDeletedUpstream != 1 {
		t.Fatalf("deleted-upstream count=%d", run.RefsDeletedUpstream)
	}
	if refs := f.destinationRefs(); refs["refs/heads/dev"] == "" {
		t.Fatalf("upstream deletion removed the local ref: %v", sortedKeys(refs))
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
	if err != nil {
		t.Fatal(err)
	}
	if len(f.transport.requests) != 1 {
		t.Fatalf("transport calls=%d", len(f.transport.requests))
	}
	request := f.transport.requests[0]
	if request.Authentication.Basic == nil || request.Authentication.Basic.Password != "secret" || !request.AllowPrivateNetwork {
		t.Fatalf("request=%+v", request)
	}
	// A changed source URL must not receive the old credential.
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/other/project.git",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Refresh(context.Background(), "project", Limits{}); err != nil {
		t.Fatal(err)
	}
	if len(f.transport.requests) != 2 {
		t.Fatalf("transport calls=%d", len(f.transport.requests))
	}
	if second := f.transport.requests[1]; second.Authentication.Basic != nil || second.URL != "https://example.invalid/other/project.git" {
		t.Fatalf("second request=%+v", second)
	}
	if result.Run.SourceGeneration != 1 {
		t.Fatalf("first run generation=%d", result.Run.SourceGeneration)
	}
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
	if err == nil || problemCode(err) != CodeSuperseded {
		t.Fatalf("refresh err=%v", err)
	}
	if run.Status != state.ImportRunSuperseded {
		t.Fatalf("run=%+v", run)
	}
	if refs := f.destinationRefs(); refs["refs/heads/main"] != old {
		t.Fatalf("stale publication applied: %v", refs["refs/heads/main"])
	}
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
	if runErr == nil || problemCode(runErr) != CodeCancelled {
		t.Fatalf("refresh err=%v", runErr)
	}
	if run.Status != state.ImportRunCancelled || run.ErrorClass != CodeCancelled {
		t.Fatalf("run=%+v", run)
	}
	if refs := f.destinationRefs(); refs["refs/heads/main"] != first {
		// The cancelled refresh staged the new pack but never published.
		t.Fatalf("cancelled run published: %v", refs["refs/heads/main"])
	}
	active, exists, err := f.store.ActiveImportRun(context.Background(), "project")
	if err != nil || exists {
		t.Fatalf("active run=%+v exists=%v err=%v", active, exists, err)
	}
}

func TestReconcileConfirmsOrphansUnfinishedIntent(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	newTip := f.commit("two", "two\n")
	// Push the object and ref so the destination holds neither the recorded
	// expected value nor any receipt, exactly like a process that stopped after
	// the Git write and before the intent receipt.
	f.git(f.source, "push", "--quiet", path, newTip+":refs/heads/main", newTip+":refs/heads/staged")

	run := state.ImportRun{
		ID: strings.Repeat("1", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing,
		StartedAt: f.now, CreatedAt: f.now,
	}
	if err := f.store.BeginImportRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = f.now
	if err := f.store.FinishImportRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	intent := state.ImportIntent{
		ID: strings.Repeat("2", 32), RepositoryID: "project", RunID: run.ID, SourceGeneration: 1, AuthorityRevision: 1,
		Status:   state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": old, state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()},
		Desired:  map[string]string{"refs/heads/main": newTip, state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: newTip}).encode()},
		Observed: map[string]string{"refs/heads/main": newTip, state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: newTip}).encode()},
		Retained: map[string]string{}, CreatedAt: f.now,
	}
	if err := f.store.CreateImportIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, exists, err := f.store.ImportIntent(context.Background(), intent.ID)
	if err != nil || !exists || stored.Status != state.ImportIntentComplete || stored.ReceiptJSON == "" || stored.ReceiptDigest == "" {
		t.Fatalf("intent=%+v exists=%v err=%v", stored, exists, err)
	}
	promoted, exists, err := f.store.ImportRun(context.Background(), run.ID)
	if err != nil || !exists || promoted.Status != state.ImportRunComplete {
		t.Fatalf("promoted run=%+v exists=%v err=%v", promoted, exists, err)
	}
}

func TestReconcileMarksMismatchedIntentUnresolved(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	moved := f.commit("two", "two\n")
	path := f.destinationPath()
	// The destination holds neither the expected nor the desired value.
	f.git(f.source, "push", "--quiet", path, moved+":refs/heads/main")
	run := state.ImportRun{
		ID: strings.Repeat("3", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing,
		StartedAt: f.now, CreatedAt: f.now,
	}
	if err := f.store.BeginImportRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = f.now
	if err := f.store.FinishImportRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	intent := state.ImportIntent{
		ID: strings.Repeat("4", 32), RepositoryID: "project", RunID: run.ID, SourceGeneration: 1, AuthorityRevision: 1,
		Status:   state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": old, state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()},
		Desired:  map[string]string{"refs/heads/main": strings.Repeat("b", 40), state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: strings.Repeat("b", 40)}).encode()},
		Observed: map[string]string{"refs/heads/main": strings.Repeat("b", 40), state.ImportHeadRef: (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: strings.Repeat("b", 40)}).encode()},
		Retained: map[string]string{}, CreatedAt: f.now,
	}
	if err := f.store.CreateImportIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("mismatched intent reconciliation error=%v", err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != moved {
		t.Fatalf("reconciliation changed independent destination ref: got=%s want=%s", got, moved)
	}
	stored, _, err := f.store.ImportIntent(context.Background(), intent.ID)
	if err != nil || stored.Status != state.ImportIntentUnresolved {
		t.Fatalf("intent status=%q err=%v", stored.Status, err)
	}
	unresolved, _, err := f.store.ImportRun(context.Background(), run.ID)
	if err != nil || unresolved.Status != state.ImportRunUnresolved {
		t.Fatalf("run status=%q err=%v", unresolved.Status, err)
	}
}

func TestReconcilePreservesUnownedStagingContent(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	unknown := filepath.Join(f.service.stagingRootPath(), "dangling")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknown, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(f.service.stagingRootPath(), "run-"+strings.Repeat("5", 32))
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unknown, "keep.txt")); err != nil {
		t.Fatalf("unowned content removed: %v", err)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("markerless staging removed: %v", err)
	}
	row, exists, err := f.store.ImportStaging(context.Background(), filepath.Base(valid))
	if err != nil || !exists || row.State != state.ImportStagingUnknown {
		t.Fatalf("staging row=%+v exists=%v err=%v", row, exists, err)
	}
}

func TestSchedulerRunsDueRefresh(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	if _, err := f.service.SetSchedule(context.Background(), "project", true, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Minute)
	scheduler := &Scheduler{Service: f.service, Interval: 10 * time.Millisecond}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := scheduler.Stop(ctx); err != nil {
			t.Errorf("scheduler stop: %v", err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, _, err := f.store.ImportRuns(context.Background(), "project", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].Kind == state.ImportKindScheduled && runs[0].Status == state.ImportRunComplete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled run never completed: %+v", runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestImportMatchesSha256SourceFormat(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.source); err != nil {
		t.Fatal(err)
	}
	f.format = "sha256"
	f.initSource()
	f.commit("one", "one\n")
	result := f.mustImport(ImportInput{})
	if result.Run.Status != state.ImportRunComplete || result.Run.ObjectFormat != importgit.FormatSHA256 {
		t.Fatalf("run=%+v", result.Run)
	}
	format, err := f.manager.ObjectFormat(context.Background(), f.destinationPath())
	if err != nil || format != importgit.FormatSHA256 {
		t.Fatalf("destination format=%q err=%v", format, err)
	}
	if refs := f.destinationRefs(); len(refs) != 1 {
		t.Fatalf("refs=%v", refs)
	}
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
	if _, err := f.refresh(); problemCode(err) != CodeBusy {
		t.Fatalf("second refresh err=%v", err)
	}
	f.transport.gate <- struct{}{}
	if run := <-done; run.Status != state.ImportRunComplete {
		t.Fatalf("first run=%+v", run)
	}
}

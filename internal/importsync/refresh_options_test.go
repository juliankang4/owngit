package importsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func prefixesPointer(prefixes ...string) *[]string { return &prefixes }

// localWork commits on branch in a clone of the destination and pushes it,
// as a person working in OwnGit would, and returns the new tip.
func (f *fixture) localWork(branch, content string) string {
	f.t.Helper()
	work := filepath.Join(f.root, "work-"+strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(work); err != nil {
		f.git("", "clone", "--quiet", f.destinationPath(), work)
	}
	f.git(work, "fetch", "--quiet", "origin")
	f.git(work, "checkout", "--quiet", "-B", branch, "origin/"+branch)
	noErr(f.t, os.WriteFile(filepath.Join(work, "local.txt"), []byte(content), 0o600))
	f.git(work, "add", "local.txt")
	f.git(work, "commit", "--quiet", "-m", content)
	f.git(work, "push", "--quiet", "origin", branch)
	return f.git(work, "rev-parse", "HEAD")
}

func (f *fixture) refState(name string) string {
	f.t.Helper()
	status, err := f.service.Status(context.Background(), "project")
	noErr(f.t, err)
	for _, ref := range status.Refs {
		if ref.Name == name {
			return ref.State
		}
	}
	return ""
}

func (f *fixture) refreshEffects() map[string]RefreshEffect {
	f.t.Helper()
	status, err := f.service.Status(context.Background(), "project")
	noErr(f.t, err)
	effects := map[string]RefreshEffect{}
	for _, effect := range status.RefreshEffects {
		effects[effect.Name] = effect
	}
	return effects
}

// Refs in an extra namespace are asked for, published and followed like
// branches. A namespace the source no longer imports keeps its local refs:
// dropping it is not an upstream deletion.
func TestExtraRefNamespacesArePublishedAndDroppingOneKeepsItsRefs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first := f.commit("one", "one\n")
	f.git(f.source, "update-ref", "refs/notes/commits", first)
	f.git(f.source, "update-ref", "refs/pull/1/head", first)
	result := f.mustImport(ImportInput{Options: OptionsChange{ExtraRefPrefixes: prefixesPointer("refs/notes/")}})
	if request := f.transport.requests[0]; !slices.Equal(request.ExtraRefPrefixes, []string{"refs/notes/"}) {
		t.Fatalf("transport extra namespaces = %v", request.ExtraRefPrefixes)
	}
	if result.Run.RefsCreated != 2 || result.Run.RefsSkipped != 1 {
		t.Fatalf("first import run = %+v", result.Run)
	}
	notes := func() string {
		records, _, err := f.manager.ReadRefs(ctx, f.destinationPath(), 0, "refs/notes", "refs/pull")
		noErr(t, err)
		for _, record := range records {
			if record.Name == "refs/pull/1/head" {
				t.Fatal("a ref outside the imported namespaces was published")
			}
			if record.Name == "refs/notes/commits" {
				return record.OID
			}
		}
		return ""
	}
	if notes() != first {
		t.Fatalf("notes = %q, want %s", notes(), first)
	}
	second := f.commit("two", "two\n")
	f.git(f.source, "update-ref", "refs/notes/commits", second)
	run, err := f.refresh()
	noErr(t, err)
	if run.RefsUpdated != 2 || notes() != second {
		t.Fatalf("refresh run = %+v, notes = %s", run, notes())
	}

	// Dropping the namespace, even with upstream deletions followed, leaves
	// its refs as they are.
	before, _, err := f.store.ImportSource(ctx, "project")
	noErr(t, err)
	dropped, err := f.service.ChangeOptions(ctx, "project", OptionsChange{ExtraRefPrefixes: prefixesPointer(), FollowUpstreamDeletions: boolPointer(true)})
	noErr(t, err)
	if dropped.AuthorityRevision != before.AuthorityRevision+1 || len(dropped.ExtraRefPrefixes) != 0 {
		t.Fatalf("dropped namespace source = %+v", dropped)
	}
	f.git(f.source, "update-ref", "-d", "refs/notes/commits")
	run, err = f.refresh()
	noErr(t, err)
	if run.RefsDeletedUpstream != 0 || notes() != second || f.refState("refs/notes/commits") != "not_imported" {
		t.Fatalf("run = %+v, notes = %s, state = %s", run, notes(), f.refState("refs/notes/commits"))
	}
	if !slices.Equal(f.transport.requests[len(f.transport.requests)-1].ExtraRefPrefixes, nil) {
		t.Fatal("a dropped namespace was still asked for")
	}

	for _, prefixes := range [][]string{{"refs/heads/"}, {"refs/owngit/keep/"}, {"refs/notes"}, {"refs/notes/", "refs/notes/"}} {
		if _, err := f.service.ChangeOptions(ctx, "project", OptionsChange{ExtraRefPrefixes: &prefixes}); problemCode(err) != CodeInvalidSource {
			t.Fatalf("extra namespaces %v: %v", prefixes, err)
		}
	}
}

// Overwrite replaces only a diverged ref this source generation tracks,
// keeping the local tip while kept history is on, and leaves a local ref
// the source never had alone.
func TestOverwriteDivergedReplacesTrackedRefs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.git(f.source, "branch", "keep")
	f.mustImport(ImportInput{})
	localDev := f.localWork("dev", "local dev\n")
	localOnly := f.git(f.destinationPath(), "rev-parse", "refs/heads/dev")
	f.git(f.destinationPath(), "update-ref", "refs/heads/mine", localOnly)
	f.git(f.source, "checkout", "--quiet", "dev")
	sourceDev := f.commit("source dev", "source dev\n")

	run, err := f.refresh()
	noErr(t, err)
	if run.RefsDivergent != 1 || f.destinationRefs()["refs/heads/dev"] != localDev {
		t.Fatalf("without overwrite run = %+v", run)
	}
	effects := f.refreshEffects()
	if effect := effects["refs/heads/dev"]; len(effects) != 1 || effect.Effect != "replace" || effect.LocalOID != localDev || effect.History != "kept" {
		t.Fatalf("refresh effects = %+v", effects)
	}

	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)})
	noErr(t, err)
	run, err = f.refresh()
	noErr(t, err)
	refs := f.destinationRefs()
	if run.RefsUpdated != 1 || run.RefsDivergent != 0 || refs["refs/heads/dev"] != sourceDev || refs["refs/heads/mine"] != localOnly ||
		refs[repository.RetainedRefName("heads", localDev)] != localDev || refs[repository.ProvenanceRefName("heads", "dev", localDev)] != localDev {
		t.Fatalf("with overwrite run = %+v refs = %v", run, refs)
	}
	if len(f.refreshEffects()) != 0 {
		t.Fatalf("effects after overwrite = %+v", f.refreshEffects())
	}

	// Without kept history the replaced tip is gone.
	off := false
	noErr(t, f.store.SavePolicies(ctx, state.PolicyChange{KeptHistory: &off}))
	localAgain := f.localWork("dev", "local again\n")
	if effect := f.refreshEffects()["refs/heads/dev"]; effect.History != "not_kept" {
		t.Fatalf("effect without kept history = %+v", effect)
	}
	sourceAgain := f.commit("source again", "source again\n")
	_, err = f.refresh()
	noErr(t, err)
	refs = f.destinationRefs()
	if refs["refs/heads/dev"] != sourceAgain || refs[repository.RetainedRefName("heads", localAgain)] != "" {
		t.Fatalf("overwrite without kept history refs = %v", refs)
	}
}

// With overwrite on, a diverged protected default branch is still refused
// as a rewrite, and nothing is published.
func TestOverwriteDoesNotRewriteTheProtectedDefaultBranch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{Options: OptionsChange{OverwriteDiverged: boolPointer(true)}})
	localMain := f.localWork("main", "local main\n")
	protect := true
	_, err := f.store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, err)
	f.commit("source main", "source main\n")
	f.git(f.source, "checkout", "--quiet", "dev")
	f.commit("source dev", "source dev\n")
	devBefore := f.destinationRefs()["refs/heads/dev"]
	if _, err := f.refresh(); problemCode(err) != CodeProtectedBranch {
		t.Fatalf("refresh err = %v", err)
	}
	if refs := f.destinationRefs(); refs["refs/heads/main"] != localMain || refs["refs/heads/dev"] != devBefore {
		t.Fatalf("a refused refresh changed refs: %v", refs)
	}
}

// Upstream deletions remove only refs this source generation tracks that
// still hold the source's value, unless overwrite covers a locally changed
// one. The branch a HEAD names, symbolic refs and a source that lists
// nothing keep their refs.
func TestFollowUpstreamDeletionsRemovesEligibleTrackedRefs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	for _, branch := range []string{"dev", "topic", "alias"} {
		f.git(f.source, "branch", branch)
	}
	f.git(f.source, "tag", "-a", "v1", "-m", "v1")
	f.mustImport(ImportInput{})
	refs := f.destinationRefs()
	main, topic, tag := refs["refs/heads/main"], refs["refs/heads/topic"], refs["refs/tags/v1"]
	localDev := f.localWork("dev", "local dev\n")
	f.git(f.destinationPath(), "symbolic-ref", "refs/heads/alias", "refs/heads/topic")
	for _, branch := range []string{"topic", "alias"} {
		f.git(f.source, "branch", "-D", branch)
	}
	f.git(f.source, "tag", "-d", "v1")

	run, err := f.refresh()
	noErr(t, err)
	if run.RefsDeletedUpstream != 3 || f.destinationRefs()["refs/heads/topic"] != topic {
		t.Fatalf("without the choice run = %+v", run)
	}
	effects := f.refreshEffects()
	if len(effects) != 3 || effects["refs/heads/topic"].Effect != "delete" || effects["refs/heads/topic"].LocalChanged ||
		effects["refs/tags/v1"].History != "kept" || effects["refs/heads/dev"].Effect != "replace" {
		t.Fatalf("refresh effects = %+v", effects)
	}

	// The source moves HEAD to a new branch and deletes main and dev: main
	// is still the branch the destination HEAD names during this refresh,
	// and dev changed locally, so both stay.
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{FollowUpstreamDeletions: boolPointer(true)})
	noErr(t, err)
	f.git(f.source, "checkout", "--quiet", "-b", "next")
	f.git(f.source, "branch", "-D", "main", "dev")
	run, err = f.refresh()
	noErr(t, err)
	refs = f.destinationRefs()
	if refs["refs/heads/topic"] != "" || refs["refs/tags/v1"] != "" || refs["refs/heads/dev"] != localDev || refs["refs/heads/main"] != main ||
		refs[repository.RetainedRefName("heads", topic)] != topic || refs[repository.RetainedRefName("tags", tag)] != tag {
		t.Fatalf("with the choice run = %+v refs = %v", run, refs)
	}
	if target := f.git(f.destinationPath(), "symbolic-ref", "refs/heads/alias"); target != "refs/heads/topic" {
		t.Fatalf("symbolic alias = %q", target)
	}
	if head := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); head != "refs/heads/next" {
		t.Fatalf("destination HEAD = %s", head)
	}
	if state := f.refState("refs/heads/topic"); state != "" {
		t.Fatalf("a deleted ref is still tracked as %q", state)
	}

	// A local ref of the same name made later is local work, even with
	// overwrite on. With overwrite, the locally changed dev follows the
	// deletion, and main, no longer named by HEAD, does too.
	f.git(f.destinationPath(), "update-ref", "refs/heads/topic", localDev)
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)})
	noErr(t, err)
	_, err = f.refresh()
	noErr(t, err)
	refs = f.destinationRefs()
	if refs["refs/heads/topic"] != localDev || refs["refs/heads/dev"] != "" || refs["refs/heads/main"] != "" ||
		refs[repository.RetainedRefName("heads", localDev)] != localDev || refs["refs/heads/next"] == "" {
		t.Fatalf("with overwrite refs = %v", refs)
	}

	// A source that lists nothing is not read as deleting everything.
	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		*advertisement = importgit.Advertisement{Service: advertisement.Service, ObjectFormat: advertisement.ObjectFormat, Empty: true}
	}
	_, err = f.refresh()
	f.transport.mutateAdvertised = nil
	if refs := f.destinationRefs(); refs["refs/heads/next"] == "" {
		t.Fatalf("an empty source deleted refs (err %v): %v", err, refs)
	}
}

// Refresh choices changed while a run is in progress stop that run before
// it publishes; a new address starts with overwrite and deletions off.
func TestRefreshChoicesAreRunAuthorityAndBelongToTheAddress(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{Options: OptionsChange{ExtraRefPrefixes: prefixesPointer("refs/notes/")}})
	f.git(f.source, "branch", "-D", "dev")
	f.service.beforeStagingVerification = func(context.Context) {
		_, err := f.service.ChangeOptions(ctx, "project", OptionsChange{FollowUpstreamDeletions: boolPointer(true)})
		noErr(t, err)
	}
	if _, err := f.refresh(); problemCode(err) != CodeSuperseded {
		t.Fatalf("refresh with a changed choice err = %v", err)
	}
	f.service.beforeStagingVerification = nil
	if f.destinationRefs()["refs/heads/dev"] == "" {
		t.Fatal("a superseded run deleted a ref")
	}

	_, err := f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)})
	noErr(t, err)
	moved, err := f.service.ConfigureSource(ctx, ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/other/project.git"})
	noErr(t, err)
	if moved.OverwriteDiverged || moved.FollowUpstreamDeletions || !slices.Equal(moved.ExtraRefPrefixes, []string{"refs/notes/"}) {
		t.Fatalf("new address choices = %+v", moved)
	}
	// A form that repeats the saved choices for a new address keeps only
	// the ones it changed.
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)})
	noErr(t, err)
	repeated, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/third/project.git", RepeatsSaved: true,
		Options: OptionsChange{OverwriteDiverged: boolPointer(true), FollowUpstreamDeletions: boolPointer(true)},
	})
	noErr(t, err)
	if repeated.OverwriteDiverged || !repeated.FollowUpstreamDeletions {
		t.Fatalf("repeated form choices = %+v", repeated)
	}
	// The new source generation observed nothing yet, so a ref only the
	// earlier source had is not deleted.
	_, err = f.refresh()
	noErr(t, err)
	if f.destinationRefs()["refs/heads/dev"] == "" {
		t.Fatal("a refresh of a new source deleted a ref observed only by the earlier source")
	}
}

// A saved list of extra namespaces that cannot be used is named: status
// shows it, a run stops before the transport, and saving a list repairs it.
func TestUnusableSavedExtraNamespacesStopTheRun(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	noErr(t, f.store.Exec(ctx, `UPDATE import_sources SET extra_ref_prefixes='["refs/heads/"]' WHERE repository_id='project'`))
	status, err := f.service.Status(ctx, "project")
	noErr(t, err)
	if !strings.Contains(status.Options.Problem, "extra ref namespaces") || len(status.Options.ExtraRefPrefixes) != 0 {
		t.Fatalf("status options = %+v", status.Options)
	}
	calls := f.transport.calls
	if _, err := f.refresh(); err == nil || !strings.Contains(err.Error(), "extra ref namespaces cannot be used") {
		t.Fatalf("refresh err = %v", err)
	}
	if f.transport.calls != calls {
		t.Fatal("a run with unusable namespaces reached the transport")
	}
	if _, err := f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)}); err == nil {
		t.Fatal("a change that leaves the unusable namespaces was saved")
	}
	repaired, err := f.service.ChangeOptions(ctx, "project", OptionsChange{ExtraRefPrefixes: prefixesPointer("refs/notes/")})
	noErr(t, err)
	if !slices.Equal(repaired.ExtraRefPrefixes, []string{"refs/notes/"}) {
		t.Fatalf("repaired = %+v", repaired)
	}
	_, err = f.refresh()
	noErr(t, err)
}

// Saving an empty list over an unreadable one writes it, although the empty
// list is also what the unreadable one reads as, and keeps sibling choices.
func TestClearingUnusableSavedExtraNamespacesRepairsThem(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
	noErr(t, f.store.Exec(ctx, `UPDATE import_sources SET extra_ref_prefixes='["refs/heads/"]' WHERE repository_id='project'`))
	repaired, err := f.service.ChangeOptions(ctx, "project", OptionsChange{ExtraRefPrefixes: prefixesPointer()})
	noErr(t, err)
	if len(repaired.ExtraRefPrefixes) != 0 || !repaired.FollowUpstreamDeletions {
		t.Fatalf("repaired = %+v", repaired)
	}
	source, _, err := f.store.ImportSource(ctx, "project")
	noErr(t, err)
	if len(source.ExtraRefPrefixes) != 0 || !source.FollowUpstreamDeletions {
		t.Fatalf("stored source = %+v", source)
	}
	_, err = f.refresh()
	noErr(t, err)
}

// A publication confirmed during reconciliation counts a ref it deleted as
// deleted upstream, like the run that planned it.
func TestReconciledDeletionIsCountedAsDeletedUpstream(t *testing.T) {
	oid := strings.Repeat("a", 40)
	run := state.ImportRun{}
	completeRunFromIntent(&run, state.ImportIntent{
		Expected: map[string]string{"refs/heads/gone": oid, "refs/heads/main": oid},
		Desired:  map[string]string{"refs/heads/gone": "", "refs/heads/main": oid},
		Observed: map[string]string{"refs/heads/main": oid},
	}, time.Unix(1_800_000_000, 0))
	if run.RefsDeletedUpstream != 1 || run.RefsUnchanged != 1 || run.Status != state.ImportRunComplete {
		t.Fatalf("reconciled run = %+v", run)
	}
}

// The preview is a plan by the run's own planner. A source that listed
// nothing at its last refresh deletes nothing, so the preview lists no
// deletion, and a refresh with the choice on keeps the refs.
func TestPreviewOfAnEmptySourceDeletesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		*advertisement = importgit.Advertisement{Service: advertisement.Service, ObjectFormat: advertisement.ObjectFormat, Empty: true}
	}
	_, err := f.refresh()
	noErr(t, err)
	if effects := f.refreshEffects(); len(effects) != 0 {
		t.Fatalf("preview of an empty source = %+v", effects)
	}
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{FollowUpstreamDeletions: boolPointer(true)})
	noErr(t, err)
	_, err = f.refresh()
	noErr(t, err)
	if refs := f.destinationRefs(); refs["refs/heads/dev"] == "" || refs["refs/heads/main"] == "" {
		t.Fatalf("an empty source deleted refs: %v", refs)
	}
}

// A diverged protected default branch makes overwriting refuse the whole
// refresh. The preview says so instead of promising a replacement, and
// still lists the deletion that following deletions alone makes; the runs
// that follow do exactly that.
func TestPreviewShowsTheProtectedBranchRefusal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.git(f.source, "branch", "topic")
	f.mustImport(ImportInput{})
	localMain := f.localWork("main", "local main\n")
	localDev := f.localWork("dev", "local dev\n")
	protect := true
	_, err := f.store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, err)
	f.git(f.source, "branch", "-D", "topic")
	_, err = f.refresh()
	noErr(t, err)

	effects := f.refreshEffects()
	if len(effects) != 2 || effects["refs/heads/main"].Effect != "refused" || effects["refs/heads/main"].LocalOID != localMain ||
		effects["refs/heads/topic"].Effect != "delete" {
		t.Fatalf("preview = %+v", effects)
	}
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true), FollowUpstreamDeletions: boolPointer(true)})
	noErr(t, err)
	if _, err := f.refresh(); problemCode(err) != CodeProtectedBranch {
		t.Fatalf("refresh with overwrite err = %v", err)
	}
	if refs := f.destinationRefs(); refs["refs/heads/topic"] == "" || refs["refs/heads/dev"] != localDev {
		t.Fatalf("a refused refresh changed refs: %v", refs)
	}
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(false)})
	noErr(t, err)
	_, err = f.refresh()
	noErr(t, err)
	if refs := f.destinationRefs(); refs["refs/heads/topic"] != "" || refs["refs/heads/dev"] != localDev || refs["refs/heads/main"] != localMain {
		t.Fatalf("following deletions alone refs = %v", refs)
	}
}

// The branch at the end of a symbolic HEAD chain is the default branch:
// following deletions never deletes it, and default branch protection
// covers it, so an alias between HEAD and the branch bypasses neither.
func TestSymbolicHEADChainKeepsAndProtectsItsBranch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
	path := f.destinationPath()
	f.git(path, "symbolic-ref", "refs/heads/local-alias", "refs/heads/dev")
	f.git(path, "symbolic-ref", "HEAD", "refs/heads/local-alias")
	f.git(f.source, "branch", "-D", "dev")
	dev := f.destinationRefs()["refs/heads/dev"]
	if effects := f.refreshEffects(); len(effects) != 0 {
		t.Fatalf("preview with HEAD through an alias = %+v", effects)
	}
	_, err := f.refresh()
	noErr(t, err)
	if f.destinationRefs()["refs/heads/dev"] != dev || f.git(path, "rev-parse", "--verify", "HEAD") != dev {
		t.Fatal("a refresh deleted the branch HEAD resolves to through an alias")
	}

	f.git(path, "symbolic-ref", "refs/heads/local-alias", "refs/heads/main")
	localMain := f.localWork("main", "local main\n")
	protect := true
	_, err = f.store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, err)
	if effect := f.refreshEffects()["refs/heads/main"]; effect.Effect != "refused" {
		t.Fatalf("preview of the protected branch behind an alias = %+v", effect)
	}
	_, err = f.service.ChangeOptions(ctx, "project", OptionsChange{OverwriteDiverged: boolPointer(true)})
	noErr(t, err)
	if _, err := f.refresh(); problemCode(err) != CodeProtectedBranch {
		t.Fatalf("refresh err = %v", err)
	}
	if f.destinationRefs()["refs/heads/main"] != localMain {
		t.Fatal("overwrite rewrote the protected branch behind an alias")
	}
}

// A HEAD moved onto a ref being deleted after the plan was made stops the
// transaction before the deletion. Like any other change made here during
// a publication, it is left for the owner to resolve.
func TestDeletionStopsWhenHEADMovesOntoItsRef(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
	path := f.destinationPath()
	f.git(f.source, "branch", "-D", "dev")
	dev := f.destinationRefs()["refs/heads/dev"]
	f.service.beforeRefTransaction = func() {
		f.git(path, "symbolic-ref", "refs/heads/local-alias", "refs/heads/dev")
		f.git(path, "symbolic-ref", "HEAD", "refs/heads/local-alias")
	}
	if _, err := f.refresh(); err == nil || !strings.Contains(err.Error(), `now resolves to "refs/heads/dev", which this refresh deletes`) {
		t.Fatalf("refresh err = %v", err)
	}
	if f.destinationRefs()["refs/heads/dev"] != dev {
		t.Fatal("the branch HEAD moved onto was deleted")
	}
}

// Another sign-in may see fewer refs. A ref no run since the sign-in
// change observed is never taken as deleted upstream, while refs the new
// sign-in sees keep following the source, deletions included. Retrying the
// same sign-in or changing only the CA is not a sign-in change.
func TestChangedSignInNeverDeletesRefsItCannotSee(t *testing.T) {
	for _, change := range []struct {
		name       string
		credential *Credentials
	}{
		{"replaced", &Credentials{BearerToken: "synthetic-narrow"}},
		{"cleared", nil},
	} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			f.commit("one", "one\n")
			f.git(f.source, "branch", "dev")
			f.git(f.source, "branch", "topic")
			f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
			noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-first"}))
			_, err := f.refresh()
			noErr(t, err)
			before, _, err := f.store.ImportSource(ctx, "project")
			noErr(t, err)
			noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-first"}))
			noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{RootCAPEM: []byte("synthetic CA")}))
			same, _, err := f.store.ImportSource(ctx, "project")
			noErr(t, err)
			if same.SignInRevision != before.SignInRevision {
				t.Fatalf("the same sign-in moved the sign-in revision %d to %d", before.SignInRevision, same.SignInRevision)
			}
			noErr(t, f.service.SetCredentials(ctx, "project", change.credential))
			after, _, err := f.store.ImportSource(ctx, "project")
			noErr(t, err)
			if after.SignInRevision != after.AuthorityRevision || after.SourceGeneration != before.SourceGeneration || !after.FollowUpstreamDeletions {
				t.Fatalf("after the sign-in change source = %+v", after)
			}
			f.transport.mutateAdvertised = func(a *importgit.Advertisement) {
				a.Refs = slices.DeleteFunc(a.Refs, func(ref importgit.Ref) bool { return ref.Name == "refs/heads/dev" })
			}
			if effect, listed := f.refreshEffects()["refs/heads/dev"]; listed {
				t.Fatalf("preview before a run with the new sign-in = %+v", effect)
			}
			_, err = f.refresh()
			noErr(t, err)
			if f.destinationRefs()["refs/heads/dev"] == "" {
				t.Fatal("a ref the new sign-in cannot see was deleted")
			}
			if effect, listed := f.refreshEffects()["refs/heads/dev"]; listed {
				t.Fatalf("preview of a ref the new sign-in cannot see = %+v", effect)
			}
			f.git(f.source, "branch", "-D", "topic")
			if effect, listed := f.refreshEffects()["refs/heads/topic"]; listed {
				t.Fatalf("preview before the deletion was observed = %+v", effect)
			}
			_, err = f.refresh()
			noErr(t, err)
			if refs := f.destinationRefs(); refs["refs/heads/topic"] != "" || refs["refs/heads/dev"] == "" {
				t.Fatalf("after a deletion the new sign-in saw refs = %v", refs)
			}
		})
	}
}

// Replacing the token, as when one expires, keeps the source generation, so
// branches the source moves afterwards still fast-forward.
func TestTokenRotationKeepsFollowingTheSource(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{Credentials: &Credentials{BearerToken: "synthetic-first"}})
	noErr(t, f.service.SetCredentials(ctx, "project", &Credentials{BearerToken: "synthetic-second"}))
	for _, content := range []string{"two\n", "three\n"} {
		tip := f.commit(content, content)
		_, err := f.refresh()
		noErr(t, err)
		if got := f.destinationRefs()["refs/heads/main"]; got != tip {
			t.Fatalf("main = %s, want %s (%s)", got, tip, f.refState("refs/heads/main"))
		}
	}
}

// A destination folder spelled in another case than an imported namespace,
// such as refs/Notes for refs/notes/, is one folder on storage that ignores
// case. The refs the source lists under it are left uncreated as diverged,
// whether their own names differ or match, and the rest publishes.
func TestExtraNamespaceFolderInAnotherCaseIsNotWritten(t *testing.T) {
	for _, existing := range []string{"refs/Notes/local", "refs/Notes/incoming"} {
		t.Run(existing, func(t *testing.T) {
			f := newFixture(t)
			oid := f.commit("one", "one\n")
			f.mustImport(ImportInput{Options: OptionsChange{ExtraRefPrefixes: prefixesPointer("refs/notes/")}})
			f.git(f.destinationPath(), "update-ref", existing, oid)
			f.git(f.source, "update-ref", "refs/notes/incoming", oid)
			next := f.commit("two", "two\n")
			run, err := f.refresh()
			noErr(t, err)
			names := strings.Fields(f.git(f.destinationPath(), "for-each-ref", "--format=%(refname)", "refs/Notes", "refs/notes"))
			if !slices.Equal(names, []string{existing}) || run.RefsDivergent != 1 || f.destinationRefs()["refs/heads/main"] != next {
				t.Fatalf("run = %+v, namespace refs = %v", run, names)
			}
		})
	}
}

// While a refresh deletes refs, no Git process can move HEAD, or a symbolic
// ref HEAD resolves through, onto a ref being deleted: OwnGit locks them, or
// Git does when the refresh also writes the branch HEAD names.
func TestHEADCannotMoveWhileRefsAreDeleted(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(f *fixture)
		move  []string
	}{
		{"HEAD", func(f *fixture) {}, []string{"symbolic-ref", "HEAD", "refs/heads/dev"}},
		{"alias", func(f *fixture) {
			f.git(f.destinationPath(), "symbolic-ref", "refs/heads/local-alias", "refs/heads/main")
			f.git(f.destinationPath(), "symbolic-ref", "HEAD", "refs/heads/local-alias")
		}, []string{"symbolic-ref", "refs/heads/local-alias", "refs/heads/dev"}},
		{"HEAD branch written", func(f *fixture) { f.commit("two", "two\n") }, []string{"symbolic-ref", "HEAD", "refs/heads/dev"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			f.git(f.source, "branch", "dev")
			f.mustImport(ImportInput{Options: OptionsChange{FollowUpstreamDeletions: boolPointer(true)}})
			path := f.destinationPath()
			test.setup(f)
			f.git(f.source, "branch", "-D", "dev")
			var moveErr error
			var moveOutput []byte
			f.service.whileRefsPrepared = func() {
				f.service.whileRefsPrepared = nil
				command := exec.Command(f.gitPath, append([]string{"--git-dir", path}, test.move...)...)
				moveOutput, moveErr = command.CombinedOutput()
			}
			_, err := f.refresh()
			noErr(t, err)
			if moveErr == nil || !strings.Contains(string(moveOutput), ".lock") {
				t.Fatalf("a Git process moved %s during the deletion: %v %s", test.name, moveErr, moveOutput)
			}
			if f.destinationRefs()["refs/heads/dev"] != "" || f.gitMaybe(path, "rev-parse", "--verify", "HEAD") == "" {
				t.Fatalf("refs = %v, HEAD = %s", f.destinationRefs(), f.gitMaybe(path, "symbolic-ref", "HEAD"))
			}
			if _, err := os.Stat(filepath.Join(path, "HEAD.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("HEAD.lock left behind: %v", err)
			}
		})
	}
}

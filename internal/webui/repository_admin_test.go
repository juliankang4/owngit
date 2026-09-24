package webui

import (
	"strings"
	"testing"
)

// Repository overview (variant A), the administrator Settings tab, and the
// delete confirmation, rendered from fixtures.

func adminRepoTabs(active RepoTab) RepoTabs {
	tabs := evidenceTabs(active)
	tabs.ImportsURL = "/repositories/r1/import"
	tabs.SettingsURL = "/repositories/r1/settings"
	tabs.DeleteURL = "/repositories/r1/delete"
	return tabs
}

func repositorySettingsPage(c Chrome) RepositorySettingsPage {
	return RepositorySettingsPage{
		Chrome: c, Repo: evidenceRepo(), Tabs: adminRepoTabs(RepoTabSettings),
		SelfURL: "/repositories/r1/settings", DefaultBranchURL: "/repositories/r1/settings/default-branch",
		DefaultBranch: "main", Branches: []string{"fix/cursor", "main"}, Selected: "main",
		ConfiguredChecksURL: "/repositories/r1/configured-checks", RunnerTokensURL: "/repositories/r1/runner-tokens",
		HelperCredentialsURL: "/repositories/r1/helper-credentials", ImportURL: "/repositories/r1/import",
		DeleteURL: "/repositories/r1/delete",
	}
}

func repositoryDeletePage(c Chrome) RepositoryDeletePage {
	return RepositoryDeletePage{
		Chrome: c, Repo: evidenceRepo(), Tabs: adminRepoTabs(RepoTabDelete),
		SelfURL: "/repositories/r1/delete", SubmitURL: "/repositories/r1/delete", CancelURL: "/repositories/r1/settings",
		GitPath: "/srv/git/r1.git", RemovedPath: "/srv/git/.owngit-removed",
	}
}

func TestDeleteControlIsSeparateAndNotColourOnly(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, repositoryDeletePage(fullChrome(lang)))
		menu := out[strings.Index(out, `<nav class="sidebar"`):]
		menu = menu[:strings.Index(menu, "</nav>")]
		settings := strings.Index(menu, `href="/repositories/r1/settings"`)
		rule := strings.Index(menu, `<hr class="sb__rule">`)
		danger := strings.Index(menu, `class="sb__item sb__item--danger" href="/repositories/r1/delete" aria-current="page"`)
		if settings < strings.Index(menu, `href="/repositories/r1/import"`) || rule < settings || danger < rule {
			t.Fatalf("%s: settings must follow import, and delete must come last behind a rule:\n%s", lang, menu)
		}
		if !strings.Contains(menu[danger:], `<svg class="icon"`) || !strings.Contains(menu[danger:], Text(lang, MsgRepoDeleteTab)) {
			t.Errorf("%s: the delete control relies on colour alone", lang)
		}
		// Inside a repository its settings say whose settings they are.
		if !strings.Contains(menu, Text(lang, MsgNavRepoSettings)) {
			t.Errorf("%s: the repository settings entry is not named as such", lang)
		}
	}
}

func TestDeletePageNeedsAChoiceANameAndThePassword(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repositoryDeletePage(fullChrome(LangEN)))
	for _, want := range []string{
		`<form class="form rdel__form" method="post" action="/repositories/r1/delete">`,
		`name="csrf" value="csrf-token-value"`,
		`name="mode" value="keep_files" required`, `name="mode" value="delete_files" required`,
		`name="confirm_name" type="text" required`, `name="admin_password" type="password" required autocomplete="current-password"`,
		`<span class="mono rdel__need">forge-cli</span>`, "/srv/git/.owngit-removed",
		Text(LangEN, MsgRepoDeleteFilesWarn), Text(LangEN, MsgRepoDeleteKeepBack),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete page lacks %q", want)
		}
	}
	if strings.Contains(out, " checked") {
		t.Error("a consequence is preselected")
	}
	// The irreversible choice carries a warning shape and words, not only a
	// red border.
	danger := out[strings.Index(out, `value="delete_files"`):]
	danger = danger[:strings.Index(danger, "</label>")]
	if !strings.Contains(danger, `class="choice__warn"><svg`) {
		t.Error("the delete-files warning has no shape")
	}

	refused := repositoryDeletePage(fullChrome(LangKO))
	refused.Mode = DeleteModeDeleteFiles
	refused.Chrome.Notices = []Notice{Error("confirm_name", MsgRepoDeleteNameMismatch)}
	out = render(t, r, refused)
	if !strings.Contains(out, `value="delete_files" required checked`) {
		t.Error("a refusal loses the chosen mode")
	}
	if !strings.Contains(out, `aria-invalid="true" aria-describedby="confirm_name-note"`) || !strings.Contains(out, Text(LangKO, MsgRepoDeleteNameMismatch)) {
		t.Error("the name error is not tied to its field")
	}
}

func TestSettingsPageOffersOnlyExistingBranches(t *testing.T) {
	r := newRenderer(t)
	page := repositorySettingsPage(fullChrome(LangEN))
	page.DefaultBranch, page.DefaultBranchMissing, page.Branches, page.Selected = "main", true, []string{"master"}, ""
	out := render(t, r, page)
	if !strings.Contains(out, `<option value="master">master</option>`) || strings.Contains(out, `<option value="main"`) {
		t.Error("the selector offers a branch that does not exist")
	}
	if !strings.Contains(out, Text(LangEN, MsgRepoDefaultBranchMissing)) {
		t.Error("a missing default branch is not named")
	}
	if !strings.Contains(out, `<option value="" selected disabled>`) {
		t.Error("with the default missing, the selector preselects a branch the administrator did not choose")
	}
	page.DefaultBranchMissing, page.Selected = false, "master"
	if out := render(t, r, page); strings.Contains(out, `<option value="" selected disabled>`) || !strings.Contains(out, `<option value="master" selected>`) {
		t.Error("an existing default branch is not the preselected choice")
	}
	page.DefaultBranchMissing, page.Selected = true, ""
	for _, link := range []string{"/configured-checks", "/runner-tokens", "/helper-credentials", "/import", "/delete"} {
		if !strings.Contains(out, `href="/repositories/r1`+link+`"`) {
			t.Errorf("settings page lacks the %s entry", link)
		}
	}
	page.Branches = nil
	out = render(t, r, page)
	if strings.Contains(out, `name="branch"`) || !strings.Contains(out, Text(LangEN, MsgRepoDefaultBranchNone)) {
		t.Error("a repository without branches still shows the selector")
	}
}

func TestOverviewSummaryStatesEachFigure(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Overview.DefaultBranch = "main"
	page.Overview.OpenPullRequests, page.Overview.OpenPullRequestsMore, page.Overview.OpenPullRequestsKnown = 99, true, true
	page.Overview.DefaultCheckKnown = true
	out := render(t, r, page)
	for _, want := range []string{`href="/repositories/r1/pull-requests">99+</a>`, Text(LangEN, MsgCheckStateAbsent),
		`class="clonefield__v mono" type="text" readonly value="http://owngit.local:8080/git/forge-cli.git"`,
		`data-copy="clone-url" hidden`} {
		if !strings.Contains(out, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	// The graph comes before the summary strip, which comes before the columns.
	graph, facts, cols := strings.Index(out, `class="sec ov-graph"`), strings.Index(out, `<dl class="facts"`), strings.Index(out, `class="ov-cols"`)
	if graph < 0 || facts < graph || cols < facts {
		t.Errorf("overview order graph=%d facts=%d columns=%d", graph, facts, cols)
	}

	page.Overview.HasDefaultCheck = true
	page.Overview.DefaultCheck = AttemptRecord{Status: CheckPassed, RevisionShortOID: "a41c9e2"}
	out = render(t, r, page)
	if !strings.Contains(out, Text(LangEN, MsgCheckStatePassed)) || !strings.Contains(out, `href="/repositories/r1/tasks">a41c9e2</a>`) {
		t.Error("a recorded check is not shown with its revision")
	}

	// An unreadable figure says so rather than showing zero or "no check".
	page.Overview.OpenPullRequestsKnown, page.Overview.DefaultCheckKnown = false, false
	out = render(t, r, page)
	if strings.Count(out, ">"+Text(LangEN, MsgRepoFactUnreadable)+"<") != 2 || strings.Contains(out, `pull-requests">0</a>`) {
		t.Error("an unreadable figure is not reported as unreadable")
	}
}

func TestOverviewSideColumnSaysWhenRefsAreHidden(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangKO), RepoTabOverview)
	page.Overview.TagCount = 51
	page.Overview.AllRefsURL = "/repositories/r1?refs=all#refs"
	out := render(t, r, page)
	tags := out[strings.Index(out, `id="ov-tags-h"`):]
	tags = tags[:strings.Index(tags, "</section>")]
	if !strings.Contains(tags, "태그 51개") || !strings.Contains(tags, `href="/repositories/r1?refs=all#refs"`) || !strings.Contains(tags, Text(LangKO, MsgRepoNewestShown)) {
		t.Errorf("a cut tag list does not give the total, the order, and a way to all:\n%s", tags)
	}
	branches := out[strings.Index(out, `id="ov-branches-h"`):]
	branches = branches[:strings.Index(branches, "</section>")]
	if strings.Contains(branches, "refs=all") {
		t.Error("a complete branch list offers a show-all link")
	}

	page.Overview.Tags = nil
	page.Overview.TagCount = 0
	out = render(t, r, page)
	if !strings.Contains(out, Text(LangKO, MsgRepoNoTagsYet)) {
		t.Error("an empty tag list is not one line")
	}
}

func TestSidebarScrollsOnItsOwnOnlyOnWideScreens(t *testing.T) {
	wide := cssRule(t, ".sidebar__inner")
	for _, want := range []string{"position: sticky", "overflow-y: auto", "overscroll-behavior: contain", "max-height: 100dvh", "calc(24px + 47px)"} {
		if !strings.Contains(wide, want) {
			t.Errorf("the sidebar list lacks %q", want)
		}
	}
	narrow := declarationsOf(t, mediaBlock(t, readSheet(t), "@media (max-width: 900px)"), ".sidebar__inner")
	for _, want := range []string{"position: static", "max-height: none", "overflow: visible"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("the 900px layout does not reset %q", want)
		}
	}
	reveal := section(t, scriptSource(t), "var sideList", "}\n  }\n")
	for _, banned := range []string{"scrollIntoView", "window.scroll", ".focus("} {
		if strings.Contains(reveal, banned) {
			t.Errorf("the sidebar reveal moves more than its own list: %q", banned)
		}
	}
}

// TestAutomaticChecksLinksBackToSettings pins the way back from Automatic
// checks to the Settings tab that lists it. The line depends on the Settings
// address, which only an administrator's page carries.
func TestAutomaticChecksLinksBackToSettings(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		admin := render(t, r, cc(lang, ccFixtureEnabled, func(p *ConfiguredChecksPage) {
			p.Tabs.SettingsURL = "/repositories/r1/settings"
		}))
		sentence := `">` + wantText(lang, MsgRepoSettingsListed) + `</span> <a href="/repositories/r1/settings">`
		if !strings.Contains(admin, sentence) {
			t.Errorf("%s: the Automatic checks page does not link back to Settings", lang)
		}
		visitor := render(t, r, cc(lang, ccFixtureEnabled, unchanged))
		if strings.Contains(visitor, wantText(lang, MsgRepoSettingsListed)) || strings.Contains(visitor, "/repositories/r1/settings") {
			t.Errorf("%s: the Settings line appears without a Settings address", lang)
		}
	}
}

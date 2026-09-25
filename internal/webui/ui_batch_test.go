package webui

import (
	"strings"
	"testing"
	"time"
)

// Screens changed by the UI batch: dated activity rows, the neutral empty
// repository badge, the overview README, pictures in the file view, the ref
// picker's explicit button, the Import tab's two views, and the credential
// fields that follow the chosen form.
func TestUIBatchScreens(t *testing.T) {
	older := time.Date(2025, 8, 2, 1, 0, 0, 0, time.UTC)
	entry := func(when time.Time) ActivityEntry {
		return ActivityEntry{RepositoryID: "r1", RepositoryName: "forge-cli", Ref: "main",
			Commit: CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: when, URL: "/repositories/r1/commits/a41c9e2"}}
	}
	dashboard := func(lang Lang) OverviewPage {
		return OverviewPage{Chrome: fullChrome(lang), Activity: sampleGraph(), TotalCount: 1,
			Repositories: []RepositorySummary{{ID: "r2", Name: "cedar-config", URL: "/repositories/r2", Empty: true, Description: "Shared settings"}},
			Recent:       []ActivityEntry{entry(testNow), entry(older)}}
	}
	importPage := func(lang Lang, admin bool) ImportPage {
		return ImportPage{
			Chrome: fullChrome(lang), Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabImport), Admin: admin,
			SubmitURL: "/repositories/r1/import", SelfURL: "/repositories/r1/import",
			AdminLoginURL: "/admin/login?next=%2Frepositories%2Fr1%2Fimport", SetupURL: "/admin/login?next=%2Frepositories%2Fr1%2Fimport%3Fsetup%3D1",
			Available: true, Configured: true, Mode: "standalone",
		}
	}
	picture := func(p *RepositoryPage) {
		p.Code.File = &FileView{Path: "docs/logo.png", Size: 2048, Binary: true, RawURL: "/repositories/r1/raw?path=docs%2Flogo.png",
			Image: true, ImageWidth: 640, ImageHeight: 480}
	}

	checkScreens(t,
		screen{name: "dashboard activity rows name their day and exact time", page: dashboard(LangEN),
			markup: []string{`class="row row--act row--dated"`, `datetime="2025-08-02T01:00:00Z"`, `title="Aug 2, 2025 01:00"`,
				`<span aria-hidden="true"><span data-en="Today 14:32" data-ko="오늘 14:32">Today 14:32</span></span>`,
				`<span aria-hidden="true"><span data-en="Aug 2, 2025" data-ko="2025년 8월 2일">Aug 2, 2025</span></span>`,
				`<span class="visually-hidden"><span data-en="Aug 2, 2025 01:00" data-ko="2025년 8월 2일 01:00">Aug 2, 2025 01:00</span></span>`}},
		screen{name: "dashboard activity dates are Korean in Korean", page: dashboard(LangKO), lang: LangKO,
			markup: []string{`title="2025년 8월 2일 01:00"`, `오늘 14:32`, `2025년 8월 2일`}},
		screen{name: "the activity page keeps the clock under its day headings",
			page: ActivityPage{Chrome: fullChrome(LangEN), Activity: sampleGraph(),
				Days: []ActivityDayGroup{{Date: testNow, Entries: []ActivityEntry{entry(testNow)}}}},
			markup: []string{`<span class="row__time">14:32</span>`}, noMarkup: []string{"row--dated"}},
		screen{name: "an empty repository is a neutral first state", page: dashboard(LangEN),
			want: []MessageCode{MsgRepoEmptyShort}, absent: []MessageCode{MsgRepoDefaultGoneShort, MsgRepoEmpty},
			markup: []string{"Shared settings"}, noMarkup: []string{"pill--missing"}},
		screen{name: "the overview shows the README after the recent commits",
			page: with(repoPage(fullChrome(LangEN), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Readme = &ReadmeView{Path: "README.md", URL: "/repositories/r1/code?path=README.md", Rendered: "<p>What forge-cli does</p>"}
			}),
			extra: inOrder(`id="ov-commits-h"`, `<section class="readme"`, `href="/repositories/r1/code?path=README.md"`, "What forge-cli does", `class="ov-side"`)},
		screen{name: "an unrendered overview README says why", lang: LangKO,
			page: with(repoPage(fullChrome(LangKO), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Readme = &ReadmeView{Path: "README.md", URL: "/x", Note: MsgReadmeBusy}
			}),
			want: []MessageCode{MsgReadmeBusy}},
		screen{name: "a picture is shown through the raw address with its size",
			page: with(repoPage(fullChrome(LangEN), RepoTabCode), picture),
			markup: []string{`<img class="imgview__img" src="/repositories/r1/raw?path=docs%2Flogo.png" alt="docs/logo.png"`,
				`width="640" height="480"`, "640 &times; 480 px"},
			absent: []MessageCode{MsgCodeBinary}},
		screen{name: "a binary file that is not a picture keeps its note",
			page: with(repoPage(fullChrome(LangEN), RepoTabCode), func(p *RepositoryPage) {
				p.Code.File = &FileView{Path: "tool.bin", Size: 10, Binary: true, RawURL: "/x"}
			}),
			want: []MessageCode{MsgCodeBinary, MsgCodeRawLink}, noMarkup: []string{"imgview"}},
		screen{name: "the ref picker keeps its button with scripting",
			page:     repoPage(fullChrome(LangEN), RepoTabCode),
			markup:   []string{`<button type="submit" class="btn refbar__go">`},
			noMarkup: []string{"data-hide-with-script"}},
		screen{name: "the import status hides administrator data and locks each change", page: importPage(LangEN, false),
			want:     []MessageCode{MsgImportAdminOnly, MsgImportRefresh, MsgImportChangeSettings, MsgImportModeStandalone},
			markup:   []string{`href="/admin/login?next=%2Frepositories%2Fr1%2Fimport"`, `class="adminlock"`},
			noMarkup: []string{`name="admin_password"`, `method="post"`, "example.invalid", "%3Fsetup%3D1"}},
		screen{name: "an unconfigured import offers Set up import with the lock",
			page:     with(importPage(LangKO, false), func(p *ImportPage) { p.Configured = false }),
			lang:     LangKO,
			want:     []MessageCode{MsgImportNotConfigured, MsgImportSetUp},
			markup:   []string{`href="/admin/login?next=%2Frepositories%2Fr1%2Fimport%3Fsetup%3D1"`, `class="adminlock"`},
			noMarkup: []string{`name="url"`}},
		screen{name: "the source form explains each mode",
			page:     with(importPage(LangEN, true), func(p *ImportPage) { p.Configured, p.Setup = false, true }),
			want:     []MessageCode{MsgImportSetUp, MsgImportSetUpIntro, MsgImportModeStandaloneHelp, MsgImportModeCoexistHelp, MsgImportURLHelp},
			markup:   []string{`type="radio" name="mode" value="standalone" checked`, `id="import-url" name="url" type="url"`},
			noMarkup: []string{`class="adminlock"`}},
		screen{name: "a stored credential form names what a save keeps and how to clear it",
			page: with(importPage(LangKO, true), func(p *ImportPage) {
				p.CredentialBound, p.CredentialForm, p.CAPresent = true, "none", true
				p.Last = &ImportRunRow{ID: "run1", Kind: "refresh", Status: "failed", ErrorClass: "network"}
				p.History = []ImportRunRow{*p.Last}
			}),
			lang:   LangKO,
			want:   []MessageCode{MsgImportCredentialKeep, MsgImportCredentialSaveHelp, MsgImportClearCredentials, MsgImportCAOnly, MsgImportNoRefs},
			absent: []MessageCode{MsgImportNoRun},
			markup: []string{`value="import_clear_credentials"`}},
		screen{name: "credential fields belong to their form", page: NewImportPage{Chrome: fullChrome(LangEN), SubmitURL: "/repositories/new-import", CredentialForm: "bearer"},
			markup: []string{`data-cred-form`, `<option value="bearer" selected>`, `data-cred-for="basic"`, `data-cred-for="bearer"`},
			extra: allOf(
				inOrder(`data-cred-for="basic"`, `name="username"`, `name="password"`, `data-cred-for="bearer"`, `name="token"`, `name="ca_pem"`),
				countIs(`name="username"`, 1))},
		screen{name: "the languages panel shows a bar and a legend under Tags",
			page: with(repoPage(fullChrome(LangEN), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.RetainedRefs = []RefLine{{Name: "7f2c1a0", Kind: "branch", Retained: true}}
				p.Overview.Languages = LanguageSummary{Rows: []LanguageRow{
					{Name: "Go", Color: "#00add8", Share: 93.94, Percent: "93.9%"},
					{Name: `<b>"x"</b>`, Color: "#e34c26", Share: 5.3, Percent: "5.3%"},
					{Name: "Other", Share: 0.76, Percent: "0.8%", Other: true},
				}}
			}),
			want: []MessageCode{MsgRepoLanguagesTitle, MsgRepoLanguagesOther},
			// The bar only repeats the legend, so assistive technology reads
			// the legend once, as a named list, and skips the bar.
			markup: []string{`<div class="langs__bar" aria-hidden="true">`,
				`<ul class="langs__list" aria-label="Language shares" data-en-aria-label="Language shares" data-ko-aria-label="언어 비율">`,
				`style="flex-grow:93.94;--lang:#00add8"`, `style="flex-grow:0.76;"`,
				`<span class="langs__name">&lt;b&gt;&#34;x&#34;&lt;/b&gt;</span> <span class="langs__pct">5.3%</span>`,
				`<span class="langs__dot" aria-hidden="true" style="--lang:#00add8"></span><span class="langs__name">Go</span>`},
			noMarkup: []string{`<b>"x"</b>`, "ZgotmplZ", `role="img"`, "Language shares: "},
			extra:    inOrder(`id="ov-tags-h"`, `id="ov-langs-h"`, `id="ov-kept-h"`)},
		screen{name: "the languages panel says when nothing was detected", lang: LangKO,
			page: with(repoPage(fullChrome(LangKO), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Languages = LanguageSummary{Note: MsgRepoLanguagesNone}
			}),
			want: []MessageCode{MsgRepoLanguagesTitle, MsgRepoLanguagesNone}, noMarkup: []string{"langs__bar"}},
		screen{name: "an overview without a language summary has no panel",
			page: repoPage(fullChrome(LangEN), RepoTabOverview), absent: []MessageCode{MsgRepoLanguagesTitle, MsgErrGeneric}},
		screen{name: "unreadable language settings are named as unreadable, not as an old Git",
			page: with(repoPage(fullChrome(LangEN), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Languages = LanguageSummary{Rows: []LanguageRow{{Name: "Go", Color: "#00add8", Share: 100, Percent: "100.0%"}},
					AttributesNote: MsgRepoLanguagesAttrsFailed}
			}),
			want: []MessageCode{MsgRepoLanguagesAttrsFailed}, absent: []MessageCode{MsgRepoLanguagesNoAttrs}},
		screen{name: "a count that took too long says so and when it is tried again", lang: LangKO,
			page: with(repoPage(fullChrome(LangKO), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Languages = LanguageSummary{Note: MsgRepoLanguagesSlow}
			}),
			want: []MessageCode{MsgRepoLanguagesSlow}, noMarkup: []string{"langs__bar"}},
		screen{name: "a language count past its bounds shows a note, not a share",
			page: with(repoPage(fullChrome(LangEN), RepoTabOverview), func(p *RepositoryPage) {
				p.Overview.Languages = LanguageSummary{Note: MsgRepoLanguagesTooLarge}
			}),
			want: []MessageCode{MsgRepoLanguagesTooLarge}, noMarkup: []string{"langs__bar", "%</span>"}},
		screen{name: "the administrator prompt keeps the repository",
			page: AuthPage{Chrome: fullChrome(LangEN), Scope: AuthAdmin, SubmitURL: "/admin/login", Next: "/repositories/r1/import",
				Repo: evidenceRepo(), Tabs: evidenceTabs(RepoTabImport)},
			markup: []string{`<h1 class="rhead__name">forge-cli</h1>`, `class="sb__repo"`},
			extra:  inOrder(`<h1 class="rhead__name">forge-cli</h1>`, "<h2>")},
	)
}

// The ref picker opens a ref when a pointer picks it or on Enter, never on
// each arrow-key step, and the credential script disables what it hides.
func TestUIBatchScriptRules(t *testing.T) {
	js := scriptSource(t)
	picker := section(t, js, "all('[data-submit-on-change]')", "/* Import credentials")
	for _, want := range []string{"'pointerdown'", "event.key === 'Enter'", "if (pointer) { submit(); }"} {
		if !strings.Contains(picker, want) {
			t.Errorf("the ref picker script lacks %q", want)
		}
	}
	if strings.Contains(picker, "hidden = true") {
		t.Error("the ref picker script hides the explicit button")
	}
	credentials := section(t, js, "all('[data-cred-form]')", "/* Restore")
	for _, want := range []string{"group.hidden = !on", "field.disabled = !on"} {
		if !strings.Contains(credentials, want) {
			t.Errorf("the credential script lacks %q", want)
		}
	}
	if strings.Contains(credentials, ".value =") {
		t.Error("the credential script clears what was typed")
	}
	sheet := readSheet(t)
	for _, want := range []string{
		`form:has([data-cred-form] option[value="none"]:checked) [data-cred-for]`,
		`.f input[type="url"]`,
		`.imgview__img { max-width: 100%; height: auto;`,
		`.restore .card > .actions { margin-top: 18px; }`,
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
}

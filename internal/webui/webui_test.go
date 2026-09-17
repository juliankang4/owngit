package webui

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 3, 12, 14, 32, 0, 0, time.UTC)

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func render(t *testing.T, r *Renderer, page Page) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.Render(&buf, page); err != nil {
		t.Fatalf("Render %T: %v", page, err)
	}
	return buf.String()
}

// wantText is the catalog text as it appears in the rendered HTML, so a
// sentence containing an apostrophe or a quote still matches.
func wantText(lang Lang, code MessageCode) string {
	return template.HTMLEscapeString(Text(lang, code))
}

// fullChrome is a signed-in viewer with a repository in the sidebar.
func fullChrome(lang Lang) Chrome {
	return Chrome{
		Lang:       lang,
		Now:        testNow,
		CurrentURL: "/repositories/r1/code?ref=main&path=internal",
		CSRF:       "csrf-token-value",
		Viewer: Viewer{
			AccessMode:      AccessOpen,
			GeneralUnlocked: true,
			SetupComplete:   true,
		},
		Connection: Connection{Encrypted: false, Host: "owngit.local:8080"},
		Nav: Nav{
			Section:      SectionOverview,
			Total:        2,
			OverviewURL:  "/",
			ActivityURL:  "/activity",
			SettingsURL:  "/settings",
			NewRepoURL:   "/repositories/new",
			ActiveRepoID: "r1",
			Repositories: []NavRepository{
				{ID: "r1", Name: "forge-cli", URL: "/repositories/r1", CommitCount: 12, CountKnown: true},
				{ID: "r2", Name: "cedar-config", URL: "/repositories/r2"},
			},
		},
	}
}

func sampleGraph() ActivityGraph {
	days := make([]ActivityDay, 0, 365)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := start; d.Year() == 2026; d = d.AddDate(0, 0, 1) {
		count := 0
		switch d.YearDay() % 7 {
		case 1:
			count = 3
		case 3:
			count = 11
		}
		days = append(days, ActivityDay{
			Date:   d,
			Count:  count,
			Future: d.After(testNow),
		})
	}
	return ActivityGraph{
		Year:            2026,
		Years:           []ActivityYear{{Year: 2026, URL: "/activity?year=2026"}, {Year: 2025, URL: "/activity?year=2025"}},
		Days:            days,
		Total:           412,
		RepositoryCount: 2,
		Complete:        true,
		Available:       true,
	}
}

// ---------------------------------------------------------------------------
// every screen renders in both languages with a complete document
// ---------------------------------------------------------------------------

func allPages(lang Lang) map[string]Page {
	c := fullChrome(lang)
	bare := Chrome{Lang: lang, Now: testNow, CurrentURL: "/setup", CSRF: "csrf-token-value"}

	return map[string]Page{
		"setup-welcome": SetupPage{
			Chrome: bare, Stage: SetupWelcome, RedeemURL: "/setup/redeem",
			Prerequisites: []Prerequisite{
				{Name: "git", Satisfied: true, Code: MsgPrereqGitFound, Detail: "2.54.0"},
				{Name: "git-http-backend", Satisfied: false, Code: MsgPrereqHTTPMiss},
			},
		},
		"setup-wizard": SetupPage{
			Chrome: bare, Stage: SetupWizard, SubmitURL: "/setup",
			Form: SetupForm{StoragePath: "/srv/git", SuggestedPath: "/srv/git", AccessMode: AccessPassword},
		},
		"setup-unavailable": SetupPage{
			Chrome: bare, Stage: SetupUnavailable,
			Reason: MsgSetupLinkExpired, RecoveryHint: MsgSetupReissueHint,
		},
		"auth-general": AuthPage{Chrome: bare, Scope: AuthGeneral, SubmitURL: "/login", Next: "/"},
		"auth-admin":   AuthPage{Chrome: bare, Scope: AuthAdmin, SubmitURL: "/admin/login"},
		"settings": SettingsPage{
			Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
			Storage:   StorageInfo{Visible: true, Label: "Home server", Path: "/volume1/git"},
			CloneHint: "http://owngit.local:8080/git/",
		},
		"overview": OverviewPage{
			Chrome: c, Activity: sampleGraph(), TotalCount: 2,
			Repositories: []RepositorySummary{
				{ID: "r1", Name: "forge-cli", URL: "/repositories/r1", DefaultBranch: "main",
					Head: CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: testNow}},
				{ID: "r2", Name: "cedar-config", URL: "/repositories/r2", Empty: true},
			},
			Recent: []ActivityEntry{{
				RepositoryID: "r1", RepositoryName: "forge-cli", Ref: "main",
				Commit: CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: testNow, URL: "/repositories/r1/commits/a41c9e2"},
			}},
			RecentMoreURL: "/activity",
		},
		"overview-empty": OverviewPage{Chrome: c, Activity: sampleGraph()},
		"activity": ActivityPage{
			Chrome: c, Activity: sampleGraph(),
			Days: []ActivityDayGroup{{Date: testNow, Entries: []ActivityEntry{{
				RepositoryName: "forge-cli", Ref: "fix/cursor", RefRetained: true,
				Commit: CommitSummary{ShortOID: "77b30d5", Subject: "Cap retry delays", AuthorDate: testNow, URL: "/x"},
			}}}},
		},
		"repo-overview":  repoPage(c, RepoTabOverview),
		"repo-code":      repoPage(c, RepoTabCode),
		"repo-commits":   repoPage(c, RepoTabCommits),
		"new-repository": NewRepositoryPage{Chrome: c, SubmitURL: "/repositories"},
		"error":          ErrorPage{Chrome: c, Status: 404, Code: MsgErrNotFound, Detail: "/nope", RetryURL: "/"},
	}
}

func repoPage(c Chrome, tab RepoTab) RepositoryPage {
	head := CommitSummary{OID: "a41c9e2ff", ShortOID: "a41c9e2", Subject: "Use stable key in pagination cursor",
		AuthorName: "Dana", AuthorDate: testNow, URL: "/repositories/r1/commits/a41c9e2ff"}
	return RepositoryPage{
		Chrome: c, Tab: tab,
		Repo: RepositoryHeader{ID: "r1", Name: "forge-cli", Description: "Command line tool",
			URL: "/repositories/r1", CloneURL: "http://owngit.local:8080/git/forge-cli.git"},
		OverviewURL: "/repositories/r1", CodeURL: "/repositories/r1/code", CommitsURL: "/repositories/r1/commits",
		Ref: RefSelection{
			Name: "main", Kind: "branch", IsDefault: true, Revision: "a41c9e2ff", ShortRevision: "a41c9e2",
			Branches: []RefOption{{Name: "main", URL: "/repositories/r1/code?ref=main", Selected: true, IsDefault: true},
				{Name: "fix/cursor", URL: "/repositories/r1/code?ref=fix/cursor"}},
			Tags: []RefOption{{Name: "v1.2.0", URL: "/repositories/r1/code?ref=v1.2.0"}},
		},
		Overview: RepositoryOverview{
			Head:     head,
			Branches: []RefLine{{Name: "main", URL: "/x", Kind: "branch", IsDefault: true, Tip: head}},
			Tags:     []RefLine{{Name: "v1.2.0", URL: "/x", Kind: "tag", Annotated: true, Tip: head}},
			RetainedRefs: []RefLine{{Name: "old/main@7f2c1a", URL: "/x", Kind: "branch", Retained: true,
				Tip: CommitSummary{ShortOID: "7f2c1a0", Subject: "Replaced by force push", AuthorDate: testNow}}},
			Activity:     sampleGraph(),
			PushCommands: []string{"git remote add origin http://owngit.local:8080/git/forge-cli.git", "git push -u origin main"},
		},
		Code: CodeView{
			Path:   "internal/pagination/cursor.go",
			Crumbs: []Crumb{{Name: "forge-cli", URL: "/repositories/r1/code"}, {Name: "internal", URL: "/x"}, {Name: "cursor.go", Current: true}},
			UpURL:  "/repositories/r1/code?path=internal",
			Entries: []TreeEntry{
				{Name: "pagination", Path: "internal/pagination", URL: "/x", Kind: "dir"},
				{Name: "cursor.go", Path: "internal/pagination/cursor.go", URL: "/x", Kind: "file", Size: 1420},
			},
			File: &FileView{Path: "internal/pagination/cursor.go", Size: 1420,
				Lines:  []string{"package pagination", "", `// <script>alert(1)</script> not markup`},
				RawURL: "/repositories/r1/raw/internal/pagination/cursor.go"},
		},
		Commits: CommitsView{
			List: []CommitSummary{head, {OID: "77b30d5aa", ShortOID: "77b30d5", Subject: "Add cursor boundary fixtures", AuthorDate: testNow, URL: "/y"}},
			Detail: &CommitDetail{
				Commit: head, Body: "Ties are broken by record id.",
				CommitterName: "Rebase Bot", CommitterDate: testNow,
				Parents: []CommitSummary{{ShortOID: "77b30d5", URL: "/y"}},
				Files: []DiffFile{{Path: "internal/pagination/cursor.go", Status: "modified",
					Additions: 7, Deletions: 1, URL: "/z", Selected: true,
					Hunks: []DiffHunk{{Header: "@@ -9,7 +9,9 @@ type Cursor struct", Lines: []DiffLine{
						{Kind: "context", OldLine: 9, NewLine: 9, Text: "// Cursor points at the last record"},
						{Kind: "add", NewLine: 10, Text: "// RecordID breaks ties"},
						{Kind: "del", OldLine: 11, Text: "// removed line"},
					}}}}},
				SelectedPath: "internal/pagination/cursor.go",
			},
		},
	}
}

func TestAllScreensRenderInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			if !strings.HasPrefix(out, "<!doctype html>") {
				t.Errorf("%s/%s: missing doctype", lang, name)
			}
			if !strings.Contains(out, `<html lang="`+string(lang)+`"`) {
				t.Errorf("%s/%s: html lang attribute not set", lang, name)
			}
			if !strings.Contains(out, "</html>") {
				t.Errorf("%s/%s: document not closed", lang, name)
			}
			// A raw message key reaching the page means a missing catalog entry.
			for _, key := range []string{"setup.", "login.", "repo.new.", "error.", "activity."} {
				if strings.Contains(out, ">"+key) {
					t.Errorf("%s/%s: raw message key %q rendered", lang, name, key)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// localization
// ---------------------------------------------------------------------------

func TestCatalogIsCompleteInBothLanguages(t *testing.T) {
	if missing := MissingMessages(); len(missing) > 0 {
		t.Fatalf("catalog entries missing a translation: %v", missing)
	}
}

func TestEveryMessageCodeUsedByPagesExists(t *testing.T) {
	// Codes the page types carry must resolve, otherwise a handler reporting
	// them would render the generic fallback instead of the real explanation.
	codes := []MessageCode{
		MsgSetupLinkExpired, MsgSetupReissueHint, MsgPrereqGitMissing,
		MsgLoginFailed, MsgAdminFailed, MsgSettingsSaved,
		MsgRepoNameTaken, MsgRepoUnreadable, MsgCodePathMissing,
		MsgCommitDiffMerge, MsgActivityLimit, MsgErrCSRF,
	}
	for _, code := range codes {
		if !Has(code) {
			t.Errorf("message code %q is not in the catalog", code)
		}
	}
}

func TestKoreanUsesStandardGitTerms(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangKO), RepoTabOverview))
	for _, term := range []string{"브랜치", "커밋", "태그", "저장소"} {
		if !strings.Contains(out, term) {
			t.Errorf("Korean repository page is missing the standard term %q", term)
		}
	}
	// Decorative separators were deliberately removed from the accepted design.
	for _, bad := range []string{"·", "—", "–"} {
		if strings.Contains(out, bad) {
			t.Errorf("interface text contains the decorative separator %q", bad)
		}
	}
}

func TestBothLanguagesAreCarriedForInPlaceSwitching(t *testing.T) {
	// Form pages must switch language without a reload, which is why the
	// server renders both languages onto the element.
	r := newRenderer(t)
	for _, page := range []Page{
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup"}, Stage: SetupWizard, SubmitURL: "/setup"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings"},
		AuthPage{Chrome: Chrome{Lang: LangEN, Now: testNow}, Scope: AuthGeneral, SubmitURL: "/login"},
		NewRepositoryPage{Chrome: fullChrome(LangEN), SubmitURL: "/repositories"},
	} {
		out := render(t, r, page)
		if !strings.Contains(out, `data-ko="`) {
			t.Errorf("%T: no Korean text carried for in-place switching", page)
		}
		if !strings.Contains(out, `data-en="`) {
			t.Errorf("%T: no English text carried for in-place switching", page)
		}
		if !strings.Contains(out, `data-title-ko="`) {
			t.Errorf("%T: document title is not switchable", page)
		}
	}
}

func TestLanguageLinksKeepTheCurrentScreen(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	out := render(t, r, repoPage(c, RepoTabCode))
	// The branch and path must survive a language change.
	if !strings.Contains(out, "lang=ko") {
		t.Fatal("no Korean language link rendered")
	}
	for _, keep := range []string{"ref=main", "path=internal"} {
		if !strings.Contains(out, keep) {
			t.Errorf("language link dropped %q from the current URL", keep)
		}
	}
}

func TestWithLangPreservesPathAndQuery(t *testing.T) {
	got := withLang("/repositories/r1/code?ref=fix%2Fcursor&path=internal%2Fx&lang=en", LangKO)
	for _, want := range []string{"/repositories/r1/code", "ref=fix%2Fcursor", "path=internal%2Fx", "lang=ko"} {
		if !strings.Contains(got, want) {
			t.Errorf("withLang(%q) = %q, missing %q", "…", got, want)
		}
	}
	if strings.Contains(got, "lang=en") {
		t.Errorf("withLang left the previous language in %q", got)
	}
}

func TestWithQueryDropsSchemeAndHost(t *testing.T) {
	// A caller must not be able to turn an interface link into an external one.
	got := withLang("https://evil.example/steal?x=1", LangKO)
	if strings.Contains(got, "evil.example") || strings.HasPrefix(got, "https:") {
		t.Fatalf("withLang kept an external target: %q", got)
	}
}

// ---------------------------------------------------------------------------
// secrets and escaping
// ---------------------------------------------------------------------------

func TestPasswordsAreNeverEchoedOnValidationFailure(t *testing.T) {
	r := newRenderer(t)
	const secret = "hunter2-should-never-appear"

	c := Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup", CSRF: "csrf",
		Notices: []Notice{
			Error("access_password", MsgSetupAccessPassShort),
			Error("admin_password", MsgSetupAdminShort),
		}}

	pages := []Page{
		SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup",
			Form: SetupForm{StoragePath: "/srv/git", AccessMode: AccessPassword,
				AccessPasswordSet: true, AdminPasswordSet: true}},
		AuthPage{Chrome: c, Scope: AuthGeneral, SubmitURL: "/login"},
		AuthPage{Chrome: c, Scope: AuthAdmin, SubmitURL: "/admin/login"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessPassword,
			PendingAction: ActionChangeAdminPassword},
	}
	for _, page := range pages {
		out := render(t, r, page)
		if strings.Contains(out, secret) {
			t.Errorf("%T: a password value reached the page", page)
		}
		// Every password input must render without a value attribute.
		for _, chunk := range strings.Split(out, "<input") {
			if !strings.Contains(chunk, `type="password"`) {
				continue
			}
			field := chunk
			if end := strings.Index(field, ">"); end >= 0 {
				field = field[:end]
			}
			if strings.Contains(field, "value=") {
				t.Errorf("%T: password input carries a value attribute: <input%s>", page, field)
			}
		}
	}
}

func TestNoticeDetailIsEscapedAndNotExecuted(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("", MsgRepoCreateFail).WithDetail(`<img src=x onerror="alert(1)">`)}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "<img src=x") {
		t.Fatal("notice detail was rendered as markup")
	}
	if !strings.Contains(out, "&lt;img") {
		t.Fatal("notice detail was not escaped")
	}
}

func TestRepositoryContentIsEscaped(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	out := render(t, r, page)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("file content was rendered as markup")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("file content was not escaped")
	}
}

func TestRepositoryNameAndRefAreEscaped(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	page := repoPage(c, RepoTabOverview)
	page.Repo.Name = `<b>bold</b>`
	page.Ref.Name = `"><script>x</script>`
	page.Ref.Detached = true
	out := render(t, r, page)
	if strings.Contains(out, "<b>bold</b>") || strings.Contains(out, "<script>x</script>") {
		t.Fatal("repository metadata was rendered as markup")
	}
}

func TestSetupWelcomeRedeemsOnlyByExplicitPost(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, SetupPage{
		Chrome:    Chrome{Lang: LangEN, Now: testNow, CurrentURL: "/setup", CSRF: "csrf"},
		Stage:     SetupWelcome,
		RedeemURL: "/setup/redeem",
	})
	if !strings.Contains(out, `method="post"`) || !strings.Contains(out, `action="/setup/redeem"`) {
		t.Fatal("the welcome page does not post to the redemption endpoint")
	}
	if !strings.Contains(out, `name="token" value=""`) {
		t.Fatal("the token field is not empty in the served HTML")
	}
	if !strings.Contains(out, "data-redeem-start") {
		t.Fatal("no explicit start control: redemption must be a deliberate action")
	}
	// The served document must not contain the secret in any form.
	if strings.Contains(out, "localStorage") || strings.Contains(out, "sessionStorage") {
		t.Fatal("the setup page references browser storage")
	}
}

func TestStorageLocationIsHiddenFromOrdinaryVisitors(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Storage = StorageInfo{Visible: false, Label: "Home server", Path: "/volume1/secret-git"}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "/volume1/secret-git") || strings.Contains(out, "Home server") {
		t.Fatal("the storage location leaked to a viewer who should not see it")
	}

	c.Storage.Visible = true
	out = render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if !strings.Contains(out, "/volume1/secret-git") {
		t.Fatal("the storage location is not shown to the owner")
	}
}

func TestCSRFTokenIsPresentOnEveryMutatingForm(t *testing.T) {
	r := newRenderer(t)
	for _, page := range []Page{
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Stage: SetupWelcome, RedeemURL: "/setup/redeem"},
		SetupPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Stage: SetupWizard, SubmitURL: "/setup"},
		AuthPage{Chrome: Chrome{Lang: LangEN, Now: testNow, CSRF: "tok"}, Scope: AuthGeneral, SubmitURL: "/login"},
		SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings"},
		NewRepositoryPage{Chrome: fullChrome(LangEN), SubmitURL: "/repositories"},
	} {
		out := render(t, r, page)
		forms := strings.Count(out, `method="post"`)
		tokens := strings.Count(out, `name="csrf"`)
		if forms == 0 {
			t.Errorf("%T: no POST form rendered", page)
		}
		if tokens < forms {
			t.Errorf("%T: %d POST forms but only %d csrf fields", page, forms, tokens)
		}
	}
}

// ---------------------------------------------------------------------------
// settings contract
// ---------------------------------------------------------------------------

func TestSecuritySettingsAlwaysCollectTheAdminPassword(t *testing.T) {
	r := newRenderer(t)
	for _, mode := range []AccessMode{AccessOpen, AccessPassword} {
		out := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: mode})
		// Split into forms and check each mutating one.
		for _, form := range strings.Split(out, `method="post"`)[1:] {
			if end := strings.Index(form, "</form>"); end >= 0 {
				form = form[:end]
			}
			if !strings.Contains(form, `name="action"`) {
				continue
			}
			if !strings.Contains(form, `name="admin_password"`) {
				t.Errorf("mode %s: a settings form does not verify the administrator password: %.160s", mode, form)
			}
		}
	}
}

func TestSettingsOffersTheExpectedActions(t *testing.T) {
	r := newRenderer(t)

	openOut := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessOpen})
	for _, want := range []string{ActionEnableAccessPassword, ActionChangeAdminPassword} {
		if !strings.Contains(openOut, `value="`+want+`"`) {
			t.Errorf("password-free mode is missing the %q action", want)
		}
	}
	if strings.Contains(openOut, `value="`+ActionDisableAccessPassword+`"`) {
		t.Error("password-free mode offers a disable action that does not apply")
	}

	passOut := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessPassword})
	for _, want := range []string{ActionChangeAccessPassword, ActionDisableAccessPassword} {
		if !strings.Contains(passOut, `value="`+want+`"`) {
			t.Errorf("password mode is missing the %q action", want)
		}
	}
}

func TestInsecureAcknowledgementIsAdministratorProtected(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Connection = Connection{Encrypted: false, Host: "owngit.local:8080"}
	out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen})

	idx := strings.Index(out, `value="`+ActionAcknowledgeInsecure+`"`)
	if idx < 0 {
		t.Fatal("the plain-HTTP acknowledgement form is missing")
	}
	form := out[idx:]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	if !strings.Contains(form, `name="insecure_ack"`) {
		t.Error("the acknowledgement form does not collect insecure_ack")
	}
	if !strings.Contains(form, `name="admin_password"`) {
		t.Error("the acknowledgement form does not verify the administrator password")
	}
}

func TestAcknowledgedConnectionStopsPromptingAndShowsStatus(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Connection = Connection{Encrypted: false, InsecureAcknowledged: true}
	out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen})
	if strings.Contains(out, `value="`+ActionAcknowledgeInsecure+`"`) {
		t.Error("the acknowledgement is asked again after it was already given")
	}
	if !strings.Contains(out, "conn--plain") {
		t.Error("the persistent connection indicator is missing")
	}
}

func TestConnectionIndicatorDoesNotOverclaim(t *testing.T) {
	r := newRenderer(t)

	plain := fullChrome(LangEN)
	plain.Connection = Connection{Encrypted: false, Host: "owngit.ts.net"}
	out := render(t, r, OverviewPage{Chrome: plain, Activity: sampleGraph()})
	if strings.Contains(out, "conn--secure") {
		t.Error("a plain request was reported as encrypted")
	}
	if !strings.Contains(out, wantText(LangEN, MsgConnNoProof)) {
		t.Error("the indicator does not say what the application actually knows")
	}

	secure := fullChrome(LangEN)
	secure.Connection = Connection{Encrypted: true, Host: "owngit.ts.net"}
	out = render(t, r, OverviewPage{Chrome: secure, Activity: sampleGraph()})
	if !strings.Contains(out, "conn--secure") {
		t.Error("an encrypted request was not reported as encrypted")
	}
}

// ---------------------------------------------------------------------------
// honest repository and activity states
// ---------------------------------------------------------------------------

func TestEmptyRepositoryExplainsItselfInsteadOfShowingBlankHistory(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCommits)
	page.Repo.Empty = true
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoEmpty)) {
		t.Error("an empty repository does not say it is empty")
	}
	if !strings.Contains(out, "git push") {
		t.Error("an empty repository does not show how to fill it")
	}
	if strings.Contains(out, "Use stable key in pagination cursor") {
		t.Error("an empty repository rendered commit history anyway")
	}
}

func TestUnreadableRepositoryIsReportedNotHidden(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Repo.Unreadable = true
	page.Repo.UnreadableReason = MsgErrInternal
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoUnreadable)) {
		t.Error("an unreadable repository was not reported")
	}
}

func TestMissingRefIsNamedRatherThanSubstituted(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Ref.Missing = true
	page.Ref.Name = "deleted-branch"
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRepoRefMissing)) {
		t.Error("a missing ref did not say so")
	}
	if strings.Contains(out, "package pagination") {
		t.Error("file content was shown for a ref that does not resolve")
	}
}

func TestDeletedDefaultBranchIsHandledHonestly(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	out := render(t, r, OverviewPage{Chrome: c, TotalCount: 1, Activity: sampleGraph(),
		Repositories: []RepositorySummary{{ID: "r1", Name: "forge-cli", URL: "/repositories/r1",
			DefaultBranchMissing: true}}})
	// The row states the status; the badge is one short line, so it carries
	// the short wording rather than the full instruction.
	if !strings.Contains(out, wantText(LangEN, MsgRepoDefaultGoneShort)) {
		t.Error("a deleted default branch was not reported on the overview")
	}
}

func TestRetainedHistoryIsLabelledAndHasNoRestoreControl(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangEN), RepoTabOverview))
	if !strings.Contains(out, wantText(LangEN, MsgRepoRetainTitle)) {
		t.Fatal("retained history is not labelled")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRepoRetainHelp)) {
		t.Error("retained history does not say that restoring is unavailable")
	}
	// Restoration is deferred, so there must be no control promising it.
	for _, dead := range []string{"Restore", "restore_", "Run checks", "Review with AI"} {
		if strings.Contains(out, dead) {
			t.Errorf("a control for unimplemented behaviour is present: %q", dead)
		}
	}
}

func TestIncompleteActivityIsStatedNotShownAsZero(t *testing.T) {
	r := newRenderer(t)
	g := sampleGraph()
	g.Complete = false
	g.IncompleteReason = MsgActivityLimit
	out := render(t, r, ActivityPage{Chrome: fullChrome(LangEN), Activity: g})
	if !strings.Contains(out, wantText(LangEN, MsgActivityIncomplete)) {
		t.Error("an incomplete count was presented as final")
	}
	if !strings.Contains(out, wantText(LangEN, MsgActivityLimit)) {
		t.Error("the reason for the incomplete count is missing")
	}
}

func TestUnavailableActivityExplainsInsteadOfDrawingAnEmptyGraph(t *testing.T) {
	r := newRenderer(t)
	g := ActivityGraph{Year: 2026, Available: false, UnavailableReason: MsgActivityNotBuilt}
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: g})
	if !strings.Contains(out, wantText(LangEN, MsgActivityNotBuilt)) {
		t.Error("unavailable activity did not explain itself")
	}
	if strings.Contains(out, `class="hm__cell"`) {
		t.Error("an empty graph was drawn for activity that could not be computed")
	}
}

func TestActivityDoesNotClaimChecksRan(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, wantText(LangEN, MsgActivityNoChecks)) {
		t.Error("the activity graph does not say what it actually measures")
	}
	for _, claim := range []string{"Checks passed", "All checks", "Build succeeded"} {
		if strings.Contains(out, claim) {
			t.Errorf("the dashboard claims check results it does not have: %q", claim)
		}
	}
}

func TestEmptyDashboardInvitesWithoutForcingARepository(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, wantText(LangEN, MsgOverviewEmpty)) {
		t.Error("the empty dashboard does not say it is empty")
	}
	if !strings.Contains(out, "/repositories/new") {
		t.Error("the empty dashboard does not offer repository creation")
	}
}

func TestNoSyntheticMockDataReachesTheInterface(t *testing.T) {
	// The accepted mockup's sample repositories and check vocabulary must not
	// appear in the product.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, fixture := range []string{
		"Northstar Gateway", "Cedar Config", "Relay Queue", "Sentinel Auth",
		"Prism UI", "Atlas Migrations", "Sample commit activity",
		"Preview with sample data", "예시 데이터",
	} {
		if strings.Contains(out, fixture) {
			t.Errorf("mockup fixture %q reached the product interface", fixture)
		}
	}
}

// ---------------------------------------------------------------------------
// accessibility
// ---------------------------------------------------------------------------

func TestStatusIsNotConveyedByColourAlone(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{
		Error("", MsgRepoCreateFail),
		Success(MsgSettingsSaved),
		{Kind: NoticeWarning, Code: MsgActivityIncomplete},
	}
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	// Each notice carries its own text and its own icon shape.
	for _, want := range []string{
		wantText(LangEN, MsgRepoCreateFail),
		wantText(LangEN, MsgSettingsSaved),
		wantText(LangEN, MsgActivityIncomplete),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice text %q is missing", want)
		}
	}
	if strings.Count(out, "<svg") < 3 {
		t.Error("notices do not carry distinct icon shapes")
	}
	if !strings.Contains(out, `role="alert"`) {
		t.Error("an error notice is not announced")
	}
}

func TestDiffLinesCarryATextMarkerNotOnlyColour(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangEN), RepoTabCommits))
	if !strings.Contains(out, `class="difftable__s"`) {
		t.Fatal("diff rows have no +/- marker")
	}
	if !strings.Contains(out, "is-add") || !strings.Contains(out, "is-del") {
		t.Error("added and removed rows are not distinguished")
	}
}

func TestFieldErrorsAreLinkedToTheirInputs(t *testing.T) {
	r := newRenderer(t)
	c := Chrome{Lang: LangEN, Now: testNow, CSRF: "tok", Notices: []Notice{
		Error("storage_path", MsgSetupStorageDenied),
	}}
	out := render(t, r, SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"})
	if !strings.Contains(out, `aria-invalid="true"`) {
		t.Error("the failing input is not marked invalid")
	}
	if !strings.Contains(out, `aria-describedby="storage_path-note"`) {
		t.Error("the input is not linked to its error message")
	}
	if !strings.Contains(out, `id="storage_path-note"`) {
		t.Error("the error message has no matching id")
	}
}

func TestEveryFieldErrorReachesItsScreen(t *testing.T) {
	// A handler reporting a field error must see it rendered, in both
	// languages, on the screen that owns that field.
	r := newRenderer(t)
	cases := []struct {
		name  string
		field string
		code  MessageCode
		build func(Chrome) Page
		// scope is the settings action whose form owns the field, empty on
		// pages that show the field only once.
		scope string
	}{
		{"setup storage", "storage_path", MsgSetupStorageNotDir,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"setup access password", "access_password", MsgSetupAccessPassShort,
			func(c Chrome) Page {
				return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup",
					Form: SetupForm{AccessMode: AccessPassword}}
			}, ""},
		{"setup admin password", "admin_password", MsgSetupAdminSameAsGen,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"setup insecure ack", "insecure_ack", MsgSetupInsecureNeed,
			func(c Chrome) Page { return SetupPage{Chrome: c, Stage: SetupWizard, SubmitURL: "/setup"} }, ""},
		{"general login", "password", MsgLoginFailed,
			func(c Chrome) Page { return AuthPage{Chrome: c, Scope: AuthGeneral, SubmitURL: "/login"} }, ""},
		{"admin login", "admin_password", MsgAdminFailed,
			func(c Chrome) Page { return AuthPage{Chrome: c, Scope: AuthAdmin, SubmitURL: "/admin/login"} }, ""},
		{"repository name", "name", MsgRepoNameTaken,
			func(c Chrome) Page { return NewRepositoryPage{Chrome: c, SubmitURL: "/repositories"} }, ""},
		// Settings repeats one field name across several forms, so its notes
		// are scoped by the action that was submitted.
		{"settings access password", "access_password", MsgSetupAccessPassShort,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
					PendingAction: ActionChangeAccessPassword}
			}, ActionChangeAccessPassword},
		{"settings admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionChangeAdminPassword}
			}, ActionChangeAdminPassword},
		{"settings new admin password", "new_admin_password", MsgSetupAdminShort,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionChangeAdminPassword}
			}, ActionChangeAdminPassword},
		{"settings insecure ack", "insecure_ack", MsgSetupInsecureNeed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionAcknowledgeInsecure}
			}, ActionAcknowledgeInsecure},
		{"settings enable admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen,
					PendingAction: ActionEnableAccessPassword}
			}, ActionEnableAccessPassword},
		{"settings disable admin password", "admin_password", MsgAdminFailed,
			func(c Chrome) Page {
				return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessPassword,
					PendingAction: ActionDisableAccessPassword}
			}, ActionDisableAccessPassword},
	}

	for _, tc := range cases {
		for _, lang := range Langs() {
			c := fullChrome(lang)
			c.Notices = []Notice{Error(tc.field, tc.code)}
			out := render(t, r, tc.build(c))
			if !strings.Contains(out, wantText(lang, tc.code)) {
				t.Errorf("%s (%s): the error text is not rendered", tc.name, lang)
			}
			wantID := noteID(tc.scope, tc.field)
			if !strings.Contains(out, `id="`+wantID+`"`) {
				t.Errorf("%s (%s): the error is not attached to the %q field (no %s)", tc.name, lang, tc.field, wantID)
			}
			if !strings.Contains(out, `aria-describedby="`+wantID+`"`) {
				t.Errorf("%s (%s): the %q input does not point at its message", tc.name, lang, tc.field)
			}
		}
	}
}

func TestSkipLinkAndMainLandmarkExist(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, `href="#main"`) || !strings.Contains(out, `id="main"`) {
		t.Error("the skip link has no target")
	}
	if !strings.Contains(out, `<main id="main" class="content" tabindex="-1">`) {
		t.Error("the main landmark is not focusable from the skip link")
	}
}

func TestRepositoryTabsUseSemanticNavigation(t *testing.T) {
	r := newRenderer(t)
	for _, tab := range []RepoTab{RepoTabOverview, RepoTabCode, RepoTabCommits} {
		out := render(t, r, repoPage(fullChrome(LangEN), tab))
		if strings.Count(out, `class="rtabs__btn"`) != 3 {
			t.Errorf("tab %s: expected three section links", tab)
		}
		if strings.Count(out, `class="rtabs__btn" href="/repositories/r1`) != 3 {
			t.Errorf("tab %s: section links are not real URLs", tab)
		}
		if !strings.Contains(out, `aria-current="page"`) {
			t.Errorf("tab %s: the current section is not marked", tab)
		}
	}
}

func TestActivityGraphIsKeyboardReachableAndLabelled(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, `role="grid"`) || !strings.Contains(out, `role="gridcell"`) {
		t.Error("the activity graph has no grid semantics")
	}
	if !strings.Contains(out, `role="row"`) {
		t.Error("the activity graph rows are not marked")
	}
	if !strings.Contains(out, `data-ko-aria-label="`) {
		t.Error("graph cells do not carry a Korean accessible name")
	}
	if !strings.Contains(out, `role="status"`) {
		t.Error("the graph readout is not a live region")
	}
	if !strings.Contains(out, `class="hm__scroll" tabindex="0"`) {
		t.Error("the horizontally scrolling graph is not keyboard reachable")
	}
}

// ---------------------------------------------------------------------------
// assets
// ---------------------------------------------------------------------------

func TestBrandRendersInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		out := render(t, r, OverviewPage{Chrome: fullChrome(lang), Activity: sampleGraph()})
		if !strings.Contains(out, "OwnGit") {
			t.Errorf("%s: the rendered page does not show the OwnGit brand", lang)
		}
	}
}

func TestAssetURLsChangeWithContent(t *testing.T) {
	// Fixed asset URLs plus a long cache lifetime would keep serving the old
	// stylesheet and script after an update, so the URL carries a content
	// version.
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	for _, asset := range []string{"owngit.css", "owngit.js", "logo.svg"} {
		marker := "/assets/" + asset + "?v="
		if !strings.Contains(out, marker) {
			t.Errorf("%s is referenced without a content version", asset)
		}
	}

	css := r.prints["owngit.css"]
	js := r.prints["owngit.js"]
	if css == "" || js == "" {
		t.Fatal("assets were not fingerprinted")
	}
	if css == js {
		t.Error("different assets produced the same version")
	}
}

func TestAssetHandlerRevalidatesUnversionedRequests(t *testing.T) {
	r := newRenderer(t)
	version := r.prints["owngit.css"]

	stale := doAssetRequest(t, r, "/owngit.css?v=old")
	if !strings.Contains(stale, "must-revalidate") {
		t.Errorf("a stale asset URL was cached without revalidation: %q", stale)
	}

	bare := doAssetRequest(t, r, "/owngit.css")
	if !strings.Contains(bare, "must-revalidate") {
		t.Errorf("an unversioned asset URL was cached without revalidation: %q", bare)
	}

	current := doAssetRequest(t, r, "/owngit.css?v="+version)
	if !strings.Contains(current, "immutable") {
		t.Errorf("the current asset URL is not cacheable: %q", current)
	}
}

func TestFontLicenceShipsWithTheFont(t *testing.T) {
	r := newRenderer(t)
	if _, ok := r.prints["fonts/PretendardVariable.woff2"]; !ok {
		t.Fatal("the bundled font is missing")
	}
	if _, ok := r.prints["fonts/PRETENDARD-LICENSE.txt"]; !ok {
		t.Fatal("the font licence is not bundled with the font")
	}
}

func TestStylesheetHasNoOutboundDependency(t *testing.T) {
	data, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)
	for _, outbound := range []string{"http://", "https://", "//fonts.", "@import url(http"} {
		if strings.Contains(css, outbound) {
			t.Errorf("the stylesheet reaches outside this installation: %q", outbound)
		}
	}
}

func TestScriptStoresNoSecrets(t *testing.T) {
	data, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	// The appearance preference is the only thing allowed in local storage.
	for _, line := range strings.Split(js, "\n") {
		if !strings.Contains(line, "localStorage") && !strings.Contains(line, "sessionStorage") {
			continue
		}
		if strings.Contains(line, "APPEARANCE_KEY") || strings.Contains(line, "//") || strings.Contains(line, "* ") {
			continue
		}
		t.Errorf("browser storage is used for something other than appearance: %s", strings.TrimSpace(line))
	}
	for _, forbidden := range []string{"password", "csrf", "admin_password"} {
		if strings.Contains(strings.ToLower(js), `"`+forbidden+`"`) {
			t.Errorf("the script references %q", forbidden)
		}
	}
}

func doAssetRequest(t *testing.T, r *Renderer, target string) string {
	t.Helper()
	req := newAssetRequest(t, target)
	rec := newRecorder()
	r.Assets().ServeHTTP(rec, req)
	return rec.Header().Get("Cache-Control")
}

// ---------------------------------------------------------------------------
// formatting
// ---------------------------------------------------------------------------

func TestRelativeTimesUseTheAuthorsRecordedDate(t *testing.T) {
	// A commit keeps the calendar date and clock its author recorded. Now is
	// only the reference calendar for "today" and "yesterday"; it never moves
	// a stored date into another zone.
	seoul := time.FixedZone("KST", 9*60*60)
	now := time.Date(2026, 3, 12, 9, 0, 0, 0, seoul)

	sameDay := time.Date(2026, 3, 12, 1, 30, 0, 0, seoul)
	if got := formatRelative(LangEN, now, sameDay); !strings.HasPrefix(got, "Today") {
		t.Errorf("same-day time rendered as %q", got)
	}
	if got := formatRelative(LangKO, now, now.AddDate(0, 0, -1)); !strings.HasPrefix(got, "\uc5b4\uc81c") {
		t.Errorf("previous-day time rendered as %q", got)
	}

	// An author who recorded 11 March in UTC is shown on 11 March, with the
	// clock they wrote. Restating it as 12 March 05:00 would report a day the
	// author never recorded.
	utcInstant := time.Date(2026, 3, 11, 20, 0, 0, 0, time.UTC)
	got := formatRelative(LangEN, now, utcInstant)
	if !strings.Contains(got, "20:00") {
		t.Errorf("the author's recorded clock was changed: %q", got)
	}
	if strings.HasPrefix(got, "Today") {
		t.Errorf("a commit recorded on 11 March is presented as today: %q", got)
	}
}

func TestActivityLevelsSpanTheRamp(t *testing.T) {
	for _, tc := range []struct{ count, level int }{{0, 0}, {1, 1}, {2, 1}, {5, 2}, {9, 3}, {40, 4}} {
		if got := activityLevel(tc.count); got != tc.level {
			t.Errorf("activityLevel(%d) = %d, want %d", tc.count, got, tc.level)
		}
	}
}

func TestGraphLayoutCoversTheWholeYear(t *testing.T) {
	layout := activityWeeks(sampleGraph())
	if len(layout.Rows) != 7 {
		t.Fatalf("expected 7 weekday rows, got %d", len(layout.Rows))
	}
	drawn := 0
	for _, row := range layout.Rows {
		if len(row.Cells) != layout.Weeks {
			t.Fatalf("row has %d cells, expected %d", len(row.Cells), layout.Weeks)
		}
		for _, cell := range row.Cells {
			if !cell.Blank {
				drawn++
			}
		}
	}
	if drawn != 365 {
		t.Errorf("drew %d days of 2026, expected 365", drawn)
	}
	if len(layout.Months) != 12 {
		t.Errorf("expected 12 month labels, got %d", len(layout.Months))
	}
}

func TestCountedNounsReadNaturallyInBothLanguages(t *testing.T) {
	cases := []struct {
		lang Lang
		kind string
		n    int
		want string
	}{
		{LangEN, "repository", 1, "1 repository"},
		{LangEN, "repository", 7, "7 repositories"},
		{LangEN, "commit", 1, "1 commit"},
		{LangKO, "repository", 7, "저장소 7곳"},
		{LangKO, "commit", 200, "커밋 200건"},
		{LangKO, "branch", 3, "브랜치 3개"},
	}
	for _, tc := range cases {
		if got := formatCount(tc.lang, tc.kind, tc.n); got != tc.want {
			t.Errorf("formatCount(%s, %s, %d) = %q, want %q", tc.lang, tc.kind, tc.n, got, tc.want)
		}
	}
}

func TestParseLangRejectsUnknownValues(t *testing.T) {
	for _, bad := range []string{"", "fr", "en-US", "ko-KR", "<script>"} {
		if got, ok := ParseLang(bad); ok || got != DefaultLang {
			t.Errorf("ParseLang(%q) = (%q, %v), want (%q, false)", bad, got, ok, DefaultLang)
		}
	}
	for _, good := range []Lang{LangEN, LangKO} {
		if got, ok := ParseLang(string(good)); !ok || got != good {
			t.Errorf("ParseLang(%q) = (%q, %v)", good, got, ok)
		}
	}
}

func TestRenderRejectsAnUnknownPage(t *testing.T) {
	r := newRenderer(t)
	var buf bytes.Buffer
	if err := r.Render(&buf, nil); err == nil {
		t.Error("rendering a nil page succeeded")
	}
}

package webui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"time"
)

//go:embed templates/*.html templates/pages/*.html
var templateFS embed.FS

//go:embed assets
var assetFS embed.FS

// pageNames are the screens this package can render. Each has one file under
// templates/pages/ that defines "body"; the shared shell lives in
// templates/*.html.
var pageNames = []string{
	"setup",
	"auth",
	"settings",
	"overview",
	"activity",
	"repository",
	"new-repository",
	"new-import",
	"import",
	"restore",
	"pull-requests",
	"new-pull-request",
	"pull-request",
	"tasks",
	"helper-credentials",
	"configured-checks",
	"runner-credentials",
	"repository-settings",
	"repository-delete",
	"error",
}

// Renderer holds the parsed templates and the static asset handler. It is
// immutable after New and safe for concurrent use.
type Renderer struct {
	// templates maps a page name to its own set. Each set combines the shared
	// shell with exactly one page file, because every page file defines the
	// same "body" template name.
	templates map[string]*template.Template
	assets    http.Handler
	prints    fingerprints
}

// New parses the embedded templates and prepares the asset handler. It fails
// only on a programming error in this package, so the caller can treat an
// error as fatal at startup.
func New() (*Renderer, error) {
	sets := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		set, err := template.New(name).Funcs(templateFuncs()).ParseFS(
			templateFS,
			"templates/*.html",
			"templates/pages/"+name+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("parse webui page %q: %w", name, err)
		}
		if set.Lookup("body") == nil {
			return nil, fmt.Errorf("parse webui page %q: no body template", name)
		}
		sets[name] = set
	}
	sub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("open webui assets: %w", err)
	}
	prints, err := buildFingerprints(sub)
	if err != nil {
		return nil, err
	}
	return &Renderer{
		templates: sets,
		assets:    assetHandler(sub, prints),
		prints:    prints,
	}, nil
}

// Render writes the complete HTML document for page. The caller sets the
// status code and content type before calling; Render never touches the
// response header and never performs an authorization decision.
func (r *Renderer) Render(w io.Writer, page Page) error {
	if page == nil {
		return fmt.Errorf("render webui page: nil page")
	}
	name := page.page()
	set, ok := r.templates[name]
	if !ok {
		return fmt.Errorf("render webui page %q: unknown page", name)
	}
	data, err := newViewData(page, r.prints)
	if err != nil {
		return err
	}
	if err := set.ExecuteTemplate(w, "layout", data); err != nil {
		return fmt.Errorf("render webui page %q: %w", name, err)
	}
	return nil
}

// Assets serves the embedded stylesheet, script, font, and logo. The caller
// mounts it with the "/assets/" prefix stripped.
func (r *Renderer) Assets() http.Handler { return r.assets }

// viewData is what every template receives. It carries the shared chrome plus
// the concrete page value, so a template reaches its own fields through .Page
// without type switches in Go.
type viewData struct {
	Chrome Chrome
	Lang   Lang
	Page   Page
	Name   string

	// LangLinks are the language switch targets for the current URL.
	LangLinks []LangLink
	// PageNotices are the notices not attached to a form field.
	PageNotices []Notice
	// Title is the document title; TitleEN and TitleKO let the client-side
	// language switch update it without a reload.
	Title   string
	TitleEN string
	TitleKO string

	prints fingerprints
}

// Asset returns the versioned URL of an embedded asset.
func (v *viewData) Asset(name string) string { return v.prints.url(name) }

// LangLink is one entry in the language picker.
type LangLink struct {
	Lang     Lang
	Label    string
	URL      string
	Selected bool
}

func newViewData(page Page, prints fingerprints) (*viewData, error) {
	chrome, ok := chromeOf(page)
	if !ok {
		return nil, fmt.Errorf("render webui page %q: page has no Chrome field", page.page())
	}
	if chrome.Lang == "" {
		chrome.Lang = DefaultLang
	}
	if chrome.Now.IsZero() {
		chrome.Now = time.Now()
	}
	titleEN, titleKO := titlePair(page)
	title := titleEN
	if chrome.Lang == LangKO {
		title = titleKO
	}
	var pageNotices []Notice
	for _, n := range chrome.Notices {
		if isPageNotice(n) {
			pageNotices = append(pageNotices, n)
		}
	}
	return &viewData{
		Chrome:      chrome,
		Lang:        chrome.Lang,
		Page:        page,
		Name:        page.page(),
		LangLinks:   languageLinks(chrome, canonicalURL(page, chrome)),
		PageNotices: pageNotices,
		Title:       title,
		TitleEN:     titleEN,
		TitleKO:     titleKO,
		prints:      prints,
	}, nil
}

// hiddenFormFields name inputs the reader never fills in. A notice about one
// of them has no visible input to sit beside, so it is shown at page level
// instead of being attached to a control and silently dropped.
var hiddenFormFields = map[string]bool{
	"action": true,
	"csrf":   true,
	"token":  true,
	"next":   true,
}

// isPageNotice reports whether a notice belongs above the page rather than
// beside a field.
func isPageNotice(n Notice) bool {
	return n.Field == "" || hiddenFormFields[n.Field]
}

// chromeOf reads the Chrome field from a page value. Every page type in this
// package embeds one; the check keeps a future page from rendering without it.
// Pages may be passed by value or by pointer.
func chromeOf(page Page) (Chrome, bool) {
	switch p := page.(type) {
	case SetupPage:
		return p.Chrome, true
	case *SetupPage:
		return p.Chrome, true
	case AuthPage:
		return p.Chrome, true
	case *AuthPage:
		return p.Chrome, true
	case SettingsPage:
		return p.Chrome, true
	case *SettingsPage:
		return p.Chrome, true
	case OverviewPage:
		return p.Chrome, true
	case *OverviewPage:
		return p.Chrome, true
	case ActivityPage:
		return p.Chrome, true
	case *ActivityPage:
		return p.Chrome, true
	case RepositoryPage:
		return p.Chrome, true
	case *RepositoryPage:
		return p.Chrome, true
	case NewRepositoryPage:
		return p.Chrome, true
	case *NewRepositoryPage:
		return p.Chrome, true
	case NewImportPage:
		return p.Chrome, true
	case *NewImportPage:
		return p.Chrome, true
	case ImportPage:
		return p.Chrome, true
	case *ImportPage:
		return p.Chrome, true
	case RestorePage:
		return p.Chrome, true
	case *RestorePage:
		return p.Chrome, true
	case PullRequestsPage:
		return p.Chrome, true
	case *PullRequestsPage:
		return p.Chrome, true
	case NewPullRequestPage:
		return p.Chrome, true
	case *NewPullRequestPage:
		return p.Chrome, true
	case PullRequestPage:
		return p.Chrome, true
	case *PullRequestPage:
		return p.Chrome, true
	case TasksPage:
		return p.Chrome, true
	case *TasksPage:
		return p.Chrome, true
	case HelperCredentialsPage:
		return p.Chrome, true
	case *HelperCredentialsPage:
		return p.Chrome, true
	case ConfiguredChecksPage:
		return p.Chrome, true
	case *ConfiguredChecksPage:
		return p.Chrome, true
	case RunnerCredentialsPage:
		return p.Chrome, true
	case RepositorySettingsPage:
		return p.Chrome, true
	case *RepositorySettingsPage:
		return p.Chrome, true
	case RepositoryDeletePage:
		return p.Chrome, true
	case *RepositoryDeletePage:
		return p.Chrome, true
	case *RunnerCredentialsPage:
		return p.Chrome, true
	case ErrorPage:
		return p.Chrome, true
	case *ErrorPage:
		return p.Chrome, true
	default:
		return Chrome{}, false
	}
}

// documentTitle builds the <title> for a page.
func documentTitle(page Page, lang Lang) string {
	const product = "OwnGit"
	section := ""
	switch p := page.(type) {
	case SetupPage:
		section = Text(lang, MsgSetupWizardTitle)
	case *SetupPage:
		section = Text(lang, MsgSetupWizardTitle)
	case AuthPage:
		section = authTitle(lang, p.Scope)
	case *AuthPage:
		section = authTitle(lang, p.Scope)
	case SettingsPage:
		section = Text(lang, MsgSettingsTitle)
	case *SettingsPage:
		section = Text(lang, MsgSettingsTitle)
	case ActivityPage:
		section = Text(lang, MsgActivityTitle)
	case *ActivityPage:
		section = Text(lang, MsgActivityTitle)
	case RepositoryPage:
		section = p.Repo.Name
	case *RepositoryPage:
		section = p.Repo.Name
	case NewRepositoryPage:
		section = Text(lang, MsgRepoNewTitle)
	case *NewRepositoryPage:
		section = Text(lang, MsgRepoNewTitle)
	case NewImportPage:
		section = Text(lang, MsgImportNewTitle)
	case *NewImportPage:
		section = Text(lang, MsgImportNewTitle)
	case ImportPage:
		section = scopedTitle(lang, MsgImportTitle, p.Repo.Name)
	case *ImportPage:
		section = scopedTitle(lang, MsgImportTitle, p.Repo.Name)
	// The restore title names the repository as well as the action, because
	// this page writes to that repository and a tab strip full of "Restore"
	// would not say which one.
	case RestorePage:
		section = restoreTitle(lang, p.Repo.Name)
	case *RestorePage:
		section = restoreTitle(lang, p.Repo.Name)
	// The pull request and evidence screens name their repository for the same
	// reason restore does: several of them can be open at once, and a tab strip
	// reading "Pull requests" four times would not say which project each one
	// belongs to.
	case PullRequestsPage:
		section = scopedTitle(lang, MsgPRTitle, p.Repo.Name)
	case *PullRequestsPage:
		section = scopedTitle(lang, MsgPRTitle, p.Repo.Name)
	case NewPullRequestPage:
		section = scopedTitle(lang, MsgPRNewTitle, p.Repo.Name)
	case *NewPullRequestPage:
		section = scopedTitle(lang, MsgPRNewTitle, p.Repo.Name)
	case PullRequestPage:
		section = pullRequestTitle(p)
	case *PullRequestPage:
		section = pullRequestTitle(*p)
	case TasksPage:
		section = scopedTitle(lang, MsgTasksTitle, p.Repo.Name)
	case *TasksPage:
		section = scopedTitle(lang, MsgTasksTitle, p.Repo.Name)
	case HelperCredentialsPage:
		section = scopedTitle(lang, MsgHelperTitle, p.Repo.Name)
	case *HelperCredentialsPage:
		section = scopedTitle(lang, MsgHelperTitle, p.Repo.Name)
	case ConfiguredChecksPage:
		section = scopedTitle(lang, MsgCCTitle, p.Repo.Name)
	case *ConfiguredChecksPage:
		section = scopedTitle(lang, MsgCCTitle, p.Repo.Name)
	case RunnerCredentialsPage:
		section = scopedTitle(lang, MsgRTTitle, p.Repo.Name)
	case *RunnerCredentialsPage:
		section = scopedTitle(lang, MsgRTTitle, p.Repo.Name)
	case RepositorySettingsPage:
		section = scopedTitle(lang, MsgRepoSettingsTitle, p.Repo.Name)
	case *RepositorySettingsPage:
		section = scopedTitle(lang, MsgRepoSettingsTitle, p.Repo.Name)
	case RepositoryDeletePage:
		section = scopedTitle(lang, MsgRepoDeleteTitle, p.Repo.Name)
	case *RepositoryDeletePage:
		section = scopedTitle(lang, MsgRepoDeleteTitle, p.Repo.Name)
	case ErrorPage:
		section = Text(lang, p.Code)
	case *ErrorPage:
		section = Text(lang, p.Code)
	}
	if section == "" {
		return product
	}
	return section + " " + product
}

// languageLinks builds switch links that keep the current screen. The backend
// persists the choice in a cookie when it sees the lang parameter.
func languageLinks(chrome Chrome, current string) []LangLink {
	if current == "" {
		current = chrome.CurrentURL
	}
	links := make([]LangLink, 0, len(Langs()))
	for _, l := range Langs() {
		links = append(links, LangLink{
			Lang:     l,
			Label:    languageLabel(l),
			URL:      withLang(current, l),
			Selected: l == chrome.Lang,
		})
	}
	return links
}

// canonicalURL is the address a reader can follow back to the current screen
// with an ordinary GET.
//
// For most pages that is the request URL. A page reached by POST has no such
// address of its own: the restore preview is rendered from a POST-only route,
// so a language link built from the request URL is a GET at a route that
// refuses GET, which is a 404 for anyone whose browser follows the link
// normally. Such pages state where their equivalent GET lives.
func canonicalURL(page Page, chrome Chrome) string {
	switch p := page.(type) {
	case RestorePage:
		return restoreSelectionURL(p)
	case *RestorePage:
		return restoreSelectionURL(*p)
	// The create screen is rendered both from its own GET and from a refused
	// POST. Its selection URL is the GET that reaches the same branch pair.
	case NewPullRequestPage:
		return pullRequestSelectionURL(p)
	case *NewPullRequestPage:
		return pullRequestSelectionURL(*p)
	// A refused review or merge answers on the POST route, which no browser
	// can follow with a GET. SelfURL is where this pull request lives.
	case PullRequestPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *PullRequestPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case HelperCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *HelperCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	// The configured-check and runner-token screens answer their forms on the
	// POST route for the same reason, and the runner response that shows a new
	// token must never hand that value to a followable link.
	case ConfiguredChecksPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *ConfiguredChecksPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case RunnerCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *RunnerCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case ImportPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *ImportPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case NewImportPage:
		return firstURL(p.SubmitURL, chrome.CurrentURL)
	case *NewImportPage:
		return firstURL(p.SubmitURL, chrome.CurrentURL)
	// Both administrator screens answer their forms on the POST route.
	case RepositorySettingsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *RepositorySettingsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case RepositoryDeletePage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case *RepositoryDeletePage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	}
	return chrome.CurrentURL
}

// firstURL prefers a page's stated GET address over the request URL.
func firstURL(stated, current string) string {
	if stated != "" {
		return stated
	}
	return current
}

func restoreTitle(lang Lang, repo string) string {
	return scopedTitle(lang, MsgRestoreTitle, repo)
}

// scopedTitle names a screen and the repository it acts on.
func scopedTitle(lang Lang, code MessageCode, repo string) string {
	title := Text(lang, code)
	if repo == "" {
		return title
	}
	return title + " " + repo
}

// pullRequestTitle identifies one pull request by its number and title, which
// is what distinguishes two tabs on the same repository.
func pullRequestTitle(p PullRequestPage) string {
	number := "#" + strconv.FormatInt(p.Number, 10)
	if p.Title == "" {
		return number + " " + p.Repo.Name
	}
	return number + " " + p.Title
}

func authTitle(lang Lang, scope AuthScope) string {
	if scope == AuthAdmin {
		return Text(lang, MsgAdminTitle)
	}
	return Text(lang, MsgLoginTitle)
}

func languageLabel(l Lang) string {
	if l == LangKO {
		return "한국어"
	}
	return "English"
}

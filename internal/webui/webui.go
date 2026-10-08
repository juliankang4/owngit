package webui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"reflect"
	"strconv"
	"text/template/parse"
	"time"

	"owngit/internal/bidi"
)

//go:embed templates/*.html templates/pages/*.html templates/standalone/*.html
var templateFS embed.FS

//go:embed assets
var assetFS embed.FS

var pageHelpers = map[string][]string{
	"setup":               nil,
	"auth":                nil,
	"settings":            {"backups"},
	"overview":            {"activity"},
	"activity":            {"activity"},
	"coding-tools":        {"evidence"},
	"repository":          {"activity", "diff", "evidence", "repository"},
	"new-repository":      nil,
	"new-import":          {"import"},
	"import":              {"import"},
	"restore":             {"diff"},
	"pull-requests":       {"evidence"},
	"new-pull-request":    {"diff", "evidence"},
	"pull-request":        {"diff", "evidence"},
	"tasks":               {"evidence"},
	"helper-credentials":  nil,
	"configured-checks":   {"configured-checks", "evidence"},
	"runner-credentials":  nil,
	"repository-settings": nil,
	"repository-delete":   nil,
	"share-links":         nil,
	"share-password":      nil,
	"error":               nil,
}

// Renderer holds the parsed templates and the static asset handler. It is
// immutable after New and safe for concurrent use.
type Renderer struct {
	pageSets map[string]*template.Template
	// standalone holds pages that do not use the shared shell.
	standalone *template.Template
	assets     http.Handler
	prints     fingerprints
}

// New parses the embedded templates and prepares the asset handler. It fails
// only on a programming error in this package, so the caller can treat an
// error as fatal at startup.
func New() (*Renderer, error) {
	pageSets := make(map[string]*template.Template, len(pageHelpers))
	for name, helpers := range pageHelpers {
		set, err := parsePageSet(name, helpers)
		if err != nil {
			return nil, fmt.Errorf("parse webui page %q: %w", name, err)
		}
		pageSets[name] = set
	}
	standalone, err := template.New("standalone").Funcs(templateFuncs()).ParseFS(templateFS, "templates/standalone/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse webui standalone pages: %w", err)
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
		pageSets:   pageSets,
		standalone: standalone,
		assets:     assetHandler(sub, prints),
		prints:     prints,
	}, nil
}

func parsePageSet(name string, helpers []string) (*template.Template, error) {
	files := []string{"templates/layout.html"}
	for _, helper := range helpers {
		files = append(files, "templates/"+helper+".html")
	}
	files = append(files, "templates/pages/"+name+".html")
	set, err := template.New(name).Funcs(templateFuncs()).ParseFS(templateFS, files...)
	if err != nil {
		return nil, err
	}
	if set.Lookup("body") == nil {
		return nil, fmt.Errorf("no body template")
	}
	if err := checkReachableTemplates(set, "layout"); err != nil {
		return nil, err
	}
	return set, nil
}

func checkReachableTemplates(set *template.Template, entry string) error {
	nodes := []parse.Node{&parse.TemplateNode{Name: entry}}
	seen := make(map[string]bool)
	for len(nodes) > 0 {
		node := nodes[len(nodes)-1]
		nodes = nodes[:len(nodes)-1]
		switch node := node.(type) {
		case *parse.ListNode:
			if node != nil {
				nodes = append(nodes, node.Nodes...)
			}
		case *parse.IfNode:
			nodes = append(nodes, node.List, node.ElseList)
		case *parse.RangeNode:
			nodes = append(nodes, node.List, node.ElseList)
		case *parse.WithNode:
			nodes = append(nodes, node.List, node.ElseList)
		case *parse.TemplateNode:
			if seen[node.Name] {
				continue
			}
			called := set.Lookup(node.Name)
			if called == nil || called.Tree == nil || called.Tree.Root == nil {
				return fmt.Errorf("template %q not defined", node.Name)
			}
			seen[node.Name] = true
			nodes = append(nodes, called.Tree.Root)
		}
	}
	return nil
}

// Render writes the complete HTML document for page. The caller sets the
// status code and content type before calling; Render never touches the
// response header and never performs an authorization decision.
func (r *Renderer) Render(w io.Writer, page Page) error {
	value := reflect.Indirect(reflect.ValueOf(page))
	if !value.IsValid() {
		return fmt.Errorf("render webui page: nil page")
	}
	page = value.Interface().(Page)
	name := page.page()
	set, ok := r.pageSets[name]
	if !ok {
		return fmt.Errorf("render webui page %q: unknown page", name)
	}
	if err := set.ExecuteTemplate(w, "layout", newViewData(page, r.prints)); err != nil {
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
	// Appearance is the colour choice the document starts in.
	Appearance Appearance
	// current is the address the language and appearance links keep.
	current string
	// PageNotices are the notices not attached to a form field.
	PageNotices []Notice
	// Sidebar selects the general or the repository sidebar.
	Sidebar Sidebar
	// Wide lets the page use a wide window: code, commits, and a pull
	// request's changes.
	Wide bool
	// Title is the document title; TitleEN and TitleKO let the client-side
	// language switch update it without a reload.
	Title   string
	TitleEN string
	TitleKO string

	prints fingerprints
}

// Asset returns the versioned URL of an embedded asset.
func (v *viewData) Asset(name string) string { return v.prints.url(name) }

// AppearanceURL is the link that saves an appearance choice and returns to
// the current screen. It works without JavaScript; with the script loaded the
// choice switches in place instead.
func (v *viewData) AppearanceURL(choice string) string {
	return withQuery(v.current, "appearance", choice)
}

// LangLink is one entry in the language picker.
type LangLink struct {
	Lang     Lang
	Label    string
	URL      string
	Selected bool
}

// newViewData normalizes a copy of the page's Chrome: the defaults below
// never touch the caller's page.
func newViewData(page Page, prints fingerprints) *viewData {
	chrome := page.chrome()
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
	chrome.Nav.Order, _ = ParseListOrder(string(chrome.Nav.Order))
	appearance, ok := ParseAppearance(string(chrome.Appearance))
	if !ok {
		appearance = AppearanceSystem
	}
	// The appearance and order parameters have done their work once the
	// backend saved the choice, so links built from this screen do not carry
	// them on.
	current := canonicalURL(page, chrome)
	if current != "" {
		current = withQuery(withQuery(current, "appearance", ""), "order", "")
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
		LangLinks:   languageLinks(chrome, current),
		Appearance:  appearance,
		current:     current,
		PageNotices: pageNotices,
		Sidebar:     sidebarOf(page),
		Wide:        wideOf(page),
		Title:       title,
		TitleEN:     titleEN,
		TitleKO:     titleKO,
		prints:      prints,
	}
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

// documentTitle builds the <title> for a page.
func documentTitle(page Page, lang Lang) string {
	const product = "OwnGit"
	section := ""
	switch p := page.(type) {
	case SetupPage:
		section = Text(lang, MsgSetupWizardTitle)
	case AuthPage:
		section = authTitle(lang, p.Scope)
	case SettingsPage:
		section = Text(lang, MsgSettingsTitle)
	case ActivityPage:
		section = Text(lang, MsgActivityTitle)
	case CodingToolsPage:
		section = Text(lang, MsgCodingTitle)
	case RepositoryPage:
		section = p.Repo.Name
	case NewRepositoryPage:
		section = Text(lang, MsgRepoNewTitle)
	case NewImportPage:
		section = Text(lang, MsgImportNewTitle)
	case ImportPage:
		section = scopedTitle(lang, MsgImportTitle, p.Repo.Name)
	// The restore title names the repository as well as the action, because
	// this page writes to that repository and a tab strip full of "Restore"
	// would not say which one.
	case RestorePage:
		section = restoreTitle(lang, p.Repo.Name)
	// The pull request and evidence screens name their repository for the same
	// reason restore does: several of them can be open at once, and a tab strip
	// reading "Pull requests" four times would not say which project each one
	// belongs to.
	case PullRequestsPage:
		section = scopedTitle(lang, MsgPRTitle, p.Repo.Name)
	case NewPullRequestPage:
		section = scopedTitle(lang, MsgPRNewTitle, p.Repo.Name)
	case PullRequestPage:
		section = pullRequestTitle(p)
	case TasksPage:
		section = scopedTitle(lang, MsgTasksTitle, p.Repo.Name)
	case HelperCredentialsPage:
		section = scopedTitle(lang, MsgHelperTitle, p.Repo.Name)
	case ConfiguredChecksPage:
		section = scopedTitle(lang, MsgCCTitle, p.Repo.Name)
	case RunnerCredentialsPage:
		section = scopedTitle(lang, MsgRTTitle, p.Repo.Name)
	case RepositorySettingsPage:
		section = scopedTitle(lang, MsgRepoSettingsTitle, p.Repo.Name)
	case RepositoryDeletePage:
		section = scopedTitle(lang, MsgRepoDeleteTitle, p.Repo.Name)
	case ShareLinksPage:
		section = scopedTitle(lang, MsgShareTitle, p.Repo.Name)
	case SharePasswordPage:
		section = Text(lang, MsgSharePasswordTitle)
	case ErrorPage:
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
	// The create screen is rendered both from its own GET and from a refused
	// POST. Its selection URL is the GET that reaches the same branch pair.
	case NewPullRequestPage:
		return pullRequestSelectionURL(p)
	// A refused review or merge answers on the POST route, which no browser
	// can follow with a GET. SelfURL is where this pull request lives.
	case PullRequestPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case HelperCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	// The configured-check and runner-token screens answer their forms on the
	// POST route for the same reason, and the runner response that shows a new
	// token must never hand that value to a followable link.
	case ConfiguredChecksPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case RunnerCredentialsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case ImportPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case NewImportPage:
		return firstURL(p.SubmitURL, chrome.CurrentURL)
	// Both administrator screens answer their forms on the POST route.
	case RepositorySettingsPage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case RepositoryDeletePage:
		return firstURL(p.SelfURL, chrome.CurrentURL)
	case ShareLinksPage:
		return firstURL(p.SubmitURL, chrome.CurrentURL)
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
	return number + " " + bidi.Isolate(p.Title)
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

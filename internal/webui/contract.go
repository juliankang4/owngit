// Package webui renders OwnGit's server-side HTML interface.
//
// The package owns presentation only. It never imports backend packages,
// reads or writes storage, performs authorization, or decides what a viewer is
// allowed to see. The caller builds a typed page value with already-authorized
// data and already-safe URLs, then calls Render.
//
// Localization lives here: the backend reports outcomes as MessageCode values
// and the renderer turns them into English or Korean text.
package webui

import "time"

// Lang is a supported interface language. English is the default.
type Lang string

const (
	LangEN Lang = "en"
	LangKO Lang = "ko"
)

// DefaultLang is used whenever no valid language was selected.
const DefaultLang = LangEN

// ParseLang validates a language value received from a query parameter or a
// cookie. It reports whether the value named a supported language; unsupported
// values resolve to DefaultLang.
func ParseLang(value string) (Lang, bool) {
	switch Lang(value) {
	case LangEN:
		return LangEN, true
	case LangKO:
		return LangKO, true
	default:
		return DefaultLang, false
	}
}

// Langs lists the supported languages in display order.
func Langs() []Lang { return []Lang{LangEN, LangKO} }

// Appearance is the Light, Dark, or System colour choice. System follows the
// operating system and is the default.
type Appearance string

const (
	AppearanceSystem Appearance = "system"
	AppearanceLight  Appearance = "light"
	AppearanceDark   Appearance = "dark"
)

// ParseAppearance validates a submitted or stored appearance value.
func ParseAppearance(value string) (Appearance, bool) {
	switch Appearance(value) {
	case AppearanceSystem, AppearanceLight, AppearanceDark:
		return Appearance(value), true
	default:
		return AppearanceSystem, false
	}
}

// AccessMode describes how general repository access is protected.
type AccessMode string

const (
	// AccessOpen allows reading and writing without a general password.
	AccessOpen AccessMode = "open"
	// AccessPassword requires the shared general-access password.
	AccessPassword AccessMode = "password"
)

// NavSection marks the active area of the interface.
type NavSection string

const (
	SectionNone       NavSection = ""
	SectionOverview   NavSection = "overview"
	SectionRepository NavSection = "repository"
	SectionActivity   NavSection = "activity"
	SectionSettings   NavSection = "settings"
	SectionSetup      NavSection = "setup"
	SectionAuth       NavSection = "auth"
)

// Chrome carries the data every page needs for the shared shell. The backend
// fills it once per request.
type Chrome struct {
	// Lang is the resolved interface language for this request.
	Lang Lang
	// Appearance is the saved colour choice, so a browser without
	// JavaScript renders it too. Empty means System.
	Appearance Appearance
	// Now is the reference calendar for relative dates: it decides which date
	// counts as today, yesterday, and the current year.
	//
	// It does not restate a stored time in another zone. A commit keeps the
	// calendar date and clock its author recorded, so the same commit names
	// one day in the graph, the activity groups, the list rows, and the commit
	// detail, whatever offset the author or this server uses.
	Now time.Time
	// CurrentURL is the current request path with its query string, for
	// example "/repositories/r1/code?ref=main&path=cmd". The renderer uses it
	// to build language links that keep the current screen. Only the path and
	// query are used; any scheme or host is ignored.
	CurrentURL string
	// CSRF is the shared form token. Leave empty on pages without a form.
	CSRF string
	// Viewer describes the current access state, never a credential.
	Viewer Viewer
	// Connection drives the persistent transport indicator.
	Connection Connection
	// Nav holds sidebar contents. Leave zero on setup and sign-in pages.
	Nav Nav
	// Notices are page-level results such as "Settings saved".
	Notices []Notice
	// Version is the version of the application that served this request.
	//
	// The backend reads it from the one authoritative version source compiled
	// into the binary. It is not a stored value, not a latest-available value,
	// and this package supplies no fallback: an empty Version renders nothing
	// rather than a number that could be wrong.
	Version string
}

// Viewer is the request's access state. It is presentation input only; the
// backend has already enforced every decision reflected here.
type Viewer struct {
	// AccessMode is the configured general-access mode.
	AccessMode AccessMode
	// GeneralUnlocked is true when general access is currently satisfied,
	// either because AccessMode is AccessOpen or a valid session exists.
	GeneralUnlocked bool
	// AdminConfirmed is true during a short administrator confirmation state.
	// It is never permanent browser trust.
	AdminConfirmed bool
	// AdminExpiresAt is when the confirmation lapses. Zero when not confirmed.
	AdminExpiresAt time.Time
	// SetupComplete is false only before the installation is configured.
	SetupComplete bool
}

// Connection reports what the application actually knows about the current
// request's transport. It must not claim more than that.
type Connection struct {
	// Encrypted is true when this request arrived over TLS.
	Encrypted bool
	// Host is the host name the browser used.
	Host string
	// InsecureAcknowledged is true once the owner accepted plain HTTP, which
	// switches the interface from a prompt to a persistent indicator.
	InsecureAcknowledged bool
}

// StorageInfo is the repository storage location shown on the Settings page.
// Visible must be false for general visitors so the page never leaks the
// host's filesystem layout.
type StorageInfo struct {
	Visible bool
	// Label is an optional installation name, for example "Home server".
	Label string
	// Path is the configured repository root.
	Path string
}

// Nav is the sidebar. Repositories are already ordered and filtered by the
// backend.
//
// Outside a repository the sidebar lists places (Home, All activity,
// Settings) and then the repositories. Inside one it shows that repository's
// sections instead; the renderer takes those from the page's RepoTabs, so Nav
// needs no second copy of them.
type Nav struct {
	Section      NavSection
	ActiveRepoID string
	Repositories []NavRepository
	// Total is the repository count shown beside the Repositories heading.
	Total int
	// Query is the current repository search text, echoed into the field.
	Query string

	OverviewURL  string
	ActivityURL  string
	SettingsURL  string
	NewRepoURL   string
	NewImportURL string
	// AdminLoginURL is offered when an action needs administrator confirmation.
	AdminLoginURL string
	// LogoutURL is non-empty only when a general session can be ended.
	LogoutURL string
	// AdminLogoutURL is non-empty only while AdminConfirmed is true.
	AdminLogoutURL string
}

// NavRepository is one sidebar entry.
type NavRepository struct {
	ID   string
	Name string
	URL  string
	// CommitCount is displayed only when CountKnown is true, so an
	// uncomputed count never renders as zero.
	CommitCount int
	CountKnown  bool
	// LastActivity is the author date of the default branch tip, when the
	// backend already knows it. Zero shows no time. The backend orders
	// Repositories by it, newest first.
	LastActivity time.Time
}

// NoticeKind selects the visual treatment and the accessible role of a notice.
type NoticeKind string

const (
	NoticeError   NoticeKind = "error"
	NoticeWarning NoticeKind = "warning"
	NoticeSuccess NoticeKind = "success"
	NoticeInfo    NoticeKind = "info"
)

// Notice is one localized message. The backend chooses a Code; the renderer
// supplies the text in the request's language.
//
// Detail carries an untranslated fragment such as a path, a ref name, or a Git
// error line. It is rendered as text and is always HTML-escaped.
type Notice struct {
	Kind NoticeKind
	Code MessageCode
	// Field names the form input this notice belongs to, using the exact
	// submitted field name ("storage_path", "admin_password", ...). Empty for
	// page-level notices.
	Field  string
	Detail string
	// Link, when set, is a local address the Detail text links to.
	Link string
}

// Error builds a field or page level error notice.
func Error(field string, code MessageCode) Notice {
	return Notice{Kind: NoticeError, Code: code, Field: field}
}

// Success builds a page level success notice.
func Success(code MessageCode) Notice {
	return Notice{Kind: NoticeSuccess, Code: code}
}

// Info builds a page level informational notice.
func Info(code MessageCode) Notice {
	return Notice{Kind: NoticeInfo, Code: code}
}

// WithDetail returns a copy of the notice carrying an untranslated detail.
func (n Notice) WithDetail(detail string) Notice {
	n.Detail = detail
	return n
}

// WithLink returns a copy of the notice whose untranslated detail links to a
// local address, such as the pull request a refusal points to.
func (n Notice) WithLink(detail, href string) Notice {
	n.Detail = detail
	n.Link = href
	return n
}

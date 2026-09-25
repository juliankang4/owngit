package webui

import (
	"html/template"
	"strings"
	"time"
)

// Page is one renderable screen. Every page type in this package implements it.
// The method reports the template name the renderer executes, which keeps the
// set of pages closed: the backend cannot invent a page the renderer does not
// know.
type Page interface {
	page() string
}

// ---------------------------------------------------------------------------
// Setup and bootstrap
// ---------------------------------------------------------------------------

// SetupStage selects which part of the first-run flow to show.
type SetupStage string

const (
	// SetupWelcome greets the installation owner who opened the one-time link.
	// The page holds the secret in memory only, removes it from the address
	// bar immediately, and redeems it with an explicit POST.
	SetupWelcome SetupStage = "welcome"
	// SetupWizard collects storage, general access, and administrator password.
	// It is reachable only after redemption, using the narrow setup session.
	SetupWizard SetupStage = "wizard"
	// SetupUnavailable explains why setup cannot continue: the link expired,
	// was already redeemed, or the installation is already configured.
	SetupUnavailable SetupStage = "unavailable"
)

// SetupPage renders /setup.
type SetupPage struct {
	Chrome Chrome
	Stage  SetupStage

	// RedeemURL receives the POST that exchanges the one-time secret. The
	// page script reads the secret from the URL fragment, clears the fragment
	// with history.replaceState, keeps the value in memory, and submits it in
	// the "token" field only when the owner presses the start button.
	RedeemURL string
	// SubmitURL receives the completed wizard.
	SubmitURL string

	// Prerequisites are environment findings such as a missing system Git.
	Prerequisites []Prerequisite

	// Form holds the values to redisplay. Passwords are never echoed.
	Form SetupForm

	// Reason explains an unavailable stage.
	Reason MessageCode
	// RecoveryHint is shown with an unavailable stage to point the owner at
	// installation-host reissue. It never describes an email or an external
	// recovery service.
	RecoveryHint MessageCode
}

func (SetupPage) page() string { return "setup" }

// SetupForm is the redisplayed wizard state. Password fields are deliberately
// absent: a validation failure must not echo a submitted password.
type SetupForm struct {
	// StoragePath is the "storage_path" field.
	StoragePath string
	// SuggestedPath is offered when the owner has not typed a path yet.
	SuggestedPath string
	// AccessMode is the "access_mode" field, AccessOpen or AccessPassword.
	AccessMode AccessMode
	// InsecureAck is the "insecure_ack" checkbox state.
	InsecureAck bool
	// AccessPasswordSet and AdminPasswordSet report whether a value was
	// accepted on an earlier attempt, so the wizard can say "already entered"
	// without holding the secret.
	AccessPasswordSet bool
	AdminPasswordSet  bool
}

// Prerequisite is one environment requirement and its observed state.
type Prerequisite struct {
	// Name is shown verbatim, for example "git".
	Name string
	// Satisfied is true when the requirement was found.
	Satisfied bool
	// Code describes the finding, satisfied or not.
	Code MessageCode
	// Detail is an untranslated fragment such as a discovered version.
	Detail string
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// AuthScope distinguishes the two sign-in forms.
type AuthScope string

const (
	// AuthGeneral is the shared general-access password at /login,
	// submitted in the "password" field.
	AuthGeneral AuthScope = "general"
	// AuthAdmin is the administrator password at /admin/login,
	// submitted in the "admin_password" field.
	AuthAdmin AuthScope = "admin"
)

// AuthPage renders /login and /admin/login.
type AuthPage struct {
	Chrome Chrome
	Scope  AuthScope
	// SubmitURL is the POST target.
	SubmitURL string
	// Next is the path to return to after a successful sign-in. It is
	// submitted back in the "next" field and must already be validated as a
	// local path by the backend.
	Next string
	// Locked is true while attempts are blocked; the form is disabled.
	Locked bool
	// RetryAfter is when attempts resume. Zero when not locked.
	RetryAfter time.Time
	// Repo and Tabs keep the repository the administrator prompt was opened
	// from, so the sidebar and the heading stay on it. Empty elsewhere.
	Repo RepositoryHeader
	Tabs RepoTabs
}

func (AuthPage) page() string { return "auth" }

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// Settings form actions submitted in the "action" field of POST /settings.
const (
	// ActionEnableAccessPassword turns on general password protection.
	// Fields: admin_password, access_password.
	ActionEnableAccessPassword = "enable_access_password"
	// ActionChangeAccessPassword replaces the general-access password.
	// Fields: admin_password, access_password.
	ActionChangeAccessPassword = "change_access_password"
	// ActionDisableAccessPassword removes general password protection.
	// Fields: admin_password.
	ActionDisableAccessPassword = "disable_access_password"
	// ActionChangeAdminPassword replaces the administrator password.
	// Fields: admin_password (current), new_admin_password.
	ActionChangeAdminPassword = "change_admin_password"
	// ActionAcknowledgeInsecure records the informed choice to keep using
	// plain HTTP. Field: insecure_ack.
	ActionAcknowledgeInsecure = "acknowledge_insecure"
	// ActionSetUpdateCheck turns the new-release check on or off.
	// Fields: admin_password, update_check ("on" or "off").
	ActionSetUpdateCheck = "set_update_check"
)

// SettingsPage renders /settings.
type SettingsPage struct {
	Chrome Chrome
	// SubmitURL is the POST target for every settings form.
	SubmitURL string
	// AccessMode is the currently stored mode.
	AccessMode AccessMode
	// AdminRequired is true when the viewer must confirm the administrator
	// password before the controls become usable. The forms stay visible and
	// each one collects "admin_password" inline.
	AdminRequired bool
	// PendingAction is the action whose form should be expanded after a
	// validation failure.
	PendingAction string
	// Storage is the owner-only storage location. Only an administrator's
	// settings page shows it.
	Storage StorageInfo
	// CloneHint shows the base Git clone URL, for example
	// "http://host:port/git/". Empty when the backend cannot determine it.
	CloneHint string
	// UpdateCheck is the new-release check setting.
	UpdateCheck UpdateCheckInfo
}

// UpdateCheckInfo describes the new-release check on the Settings page.
type UpdateCheckInfo struct {
	// Enabled is the saved setting.
	Enabled bool
	// ForcedOff is true when the server started with --no-update-check,
	// which overrides the saved setting.
	ForcedOff bool
}

func (SettingsPage) page() string { return "settings" }

// ---------------------------------------------------------------------------
// Overview and activity
// ---------------------------------------------------------------------------

// OverviewPage renders "/". It is the empty dashboard right after setup, so
// every list can legitimately be empty.
type OverviewPage struct {
	Chrome Chrome
	// Activity is the annual graph across all repositories.
	Activity ActivityGraph
	// Repositories is the full list, already ordered and search-filtered.
	Repositories []RepositorySummary
	// Recent is the short "Latest activity" list under the repositories.
	Recent []ActivityEntry
	// RecentMoreURL links to the full activity page.
	RecentMoreURL string
	// TotalCount is the number of repositories before search filtering.
	TotalCount int
	// Release announces a newer OwnGit release. Nil shows nothing.
	Release *ReleaseNotice
}

func (OverviewPage) page() string { return "overview" }

// ReleaseNotice is the dashboard notice for a newer OwnGit release. Every
// URL is set by the backend; NotesURL is built from the checked version.
type ReleaseNotice struct {
	// Version is the newer release, "X.Y.Z".
	Version string
	// Current is the running version.
	Current string
	// NotesURL is the release page.
	NotesURL string
	// GuideURL explains how to update.
	GuideURL string
	// DismissURL takes a POST with csrf and version.
	DismissURL string
}

// ActivityPage renders "/activity": the chronological list across every
// repository.
type ActivityPage struct {
	Chrome   Chrome
	Activity ActivityGraph
	// Days groups entries by calendar day, newest first.
	Days []ActivityDayGroup
	// OlderURL and NewerURL page through history. Empty when unavailable.
	OlderURL string
	NewerURL string
}

func (ActivityPage) page() string { return "activity" }

// ActivityGraph is the annual daily commit graph.
//
// Counting rules are the backend's: author dates, current and retained
// historical branches, one count per commit per repository. The renderer only
// draws what it is given and reports incompleteness honestly.
type ActivityGraph struct {
	// Year is the displayed calendar year.
	Year int
	// Years are the selectable years, newest first.
	Years []ActivityYear
	// Days covers Jan 1 through Dec 31 of Year in calendar order. Days after
	// today carry Future.
	Days []ActivityDay
	// Total is the sum for the year.
	Total int
	// RepositoryCount is how many repositories were aggregated.
	RepositoryCount int
	// Scope names a single repository when the graph is scoped to one.
	Scope string
	// Complete is false when the backend could not finish counting. The
	// renderer then says the count is incomplete instead of showing a total
	// that reads as authoritative.
	Complete bool
	// IncompleteReason explains why. Used only when Complete is false.
	IncompleteReason MessageCode
	// Available is false when activity could not be computed at all; the
	// renderer shows an explanation instead of an empty graph.
	Available bool
	// UnavailableReason explains why. Used only when Available is false.
	UnavailableReason MessageCode
	// SelectedDate highlights one day. Zero when nothing is selected.
	SelectedDate time.Time
}

// ActivityYear is one selectable year.
type ActivityYear struct {
	Year int
	URL  string
}

// ActivityDay is one calendar day of the graph.
type ActivityDay struct {
	Date   time.Time
	Count  int
	Future bool
	// URL filters the activity list to this day. Optional.
	URL string
}

// ActivityDayGroup is one day of the chronological list.
type ActivityDayGroup struct {
	Date    time.Time
	Entries []ActivityEntry
}

// ActivityEntry is one commit in an activity list.
type ActivityEntry struct {
	RepositoryID   string
	RepositoryName string
	RepositoryURL  string
	// Ref is the branch or ref this commit was observed on.
	Ref string
	// RefRetained marks a ref that no longer exists but whose history is
	// retained after a force-push or deletion.
	RefRetained bool
	Commit      CommitSummary
}

// RepositorySummary is one repository row on the overview.
type RepositorySummary struct {
	ID          string
	Name        string
	Description string
	URL         string
	// CloneURL is the Git clone address supplied by the backend.
	CloneURL  string
	CreatedAt time.Time
	// Empty is true for a repository with no commits yet.
	Empty bool
	// DefaultBranch is the branch shown for this repository. Empty when the
	// repository has no branches or the default branch is missing.
	DefaultBranch string
	// DefaultBranchMissing is true when the recorded default branch no longer
	// resolves, for example after it was deleted.
	DefaultBranchMissing bool
	// Head is the latest commit. Zero when Empty.
	Head CommitSummary
	// BranchCount and TagCount are displayed when Counted is true.
	BranchCount   int
	TagCount      int
	RetainedCount int
	Counted       bool
	// Preparing is true while OwnGit prepares the repository after startup.
	// Nothing was read from Git, so only the name and description are known.
	Preparing bool
	// Unreadable is true when the repository's Git data could not be read
	// for this page. Only the name and description are known.
	Unreadable bool
	// Busy is true when another Git operation held the repository and no
	// earlier listing was available. Like Preparing, only the name and
	// description are known.
	Busy bool
}

// ---------------------------------------------------------------------------
// Repository
// ---------------------------------------------------------------------------

// RepoTab selects the active repository tab.
type RepoTab string

const (
	RepoTabOverview RepoTab = "overview"
	RepoTabCode     RepoTab = "code"
	RepoTabCommits  RepoTab = "commits"
	// RepoTabPullRequests and RepoTabChecks are rendered by their own page
	// types, which share this repository's tab strip.
	RepoTabPullRequests RepoTab = "pull-requests"
	RepoTabChecks       RepoTab = "checks"
)

// RepoTabs are the addresses of a repository's sections.
//
// Every screen that belongs to one repository carries them so the strip stays
// identical wherever the reader is. A section with no URL is not rendered,
// which is how a caller that does not offer one avoids a dead tab.
type RepoTabs struct {
	OverviewURL     string
	CodeURL         string
	CommitsURL      string
	PullRequestsURL string
	TasksURL        string
	ImportsURL      string
	// SettingsURL and DeleteURL are set for every viewer. Settings sits in
	// the normal tab order; Delete is the right-aligned control that opens
	// the confirmation page. Both routes ask a viewer without an
	// administrator session to log in first.
	SettingsURL string
	DeleteURL   string
	// AdminLocked is true when the viewer has no administrator session, so
	// the administrator entries carry a lock shape and words that say the
	// link opens the administrator login first.
	AdminLocked bool
	// Active marks the current section.
	Active RepoTab
}

// RepositoryPage renders every /repositories/{id} screen. Tab selects which
// panel is filled; the other panels are links, not hidden content.
type RepositoryPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tab    RepoTab

	OverviewURL string
	CodeURL     string
	CommitsURL  string
	// RestoreURL opens the restore screen for this repository. Empty means no
	// link, which is what an older caller that never sets it gets.
	RestoreURL string
	// PullRequestsURL, TasksURL, and ImportsURL add sections to the tab strip.
	// A caller that does not set one renders no dead link for it.
	PullRequestsURL string
	TasksURL        string
	ImportsURL      string
	// SettingsURL and DeleteURL are the administrator sections. They are
	// offered to every viewer; the routes ask for the administrator login
	// themselves and return to the page afterwards.
	SettingsURL string
	DeleteURL   string

	// Ref is the currently selected branch, tag, or revision.
	Ref RefSelection

	// Overview is used when Tab is RepoTabOverview.
	Overview RepositoryOverview
	// Code is used when Tab is RepoTabCode.
	Code CodeView
	// Commits is used when Tab is RepoTabCommits.
	Commits CommitsView
}

func (RepositoryPage) page() string { return "repository" }

// RepositoryHeader identifies the repository on every tab.
type RepositoryHeader struct {
	ID          string
	Name        string
	Description string
	URL         string
	// CloneURL is the Smart HTTP address the backend generated.
	CloneURL string
	// Empty is true when the repository has no commits. Every tab then shows
	// push instructions instead of pretending a history exists.
	Empty bool
	// Unreadable is true when the repository exists but its Git data could not
	// be read. The renderer shows the reason instead of an empty history.
	Unreadable bool
	// UnreadableReason explains why. Used only when Unreadable is true.
	UnreadableReason MessageCode
	// Preparing is true while OwnGit prepares the repository after startup.
	// Unreadable is then true as well, and the page explains the wait.
	Preparing bool
}

// RefSelection describes the selected ref and the alternatives.
type RefSelection struct {
	// Name is the selected ref, for example "main" or "v1.2.0".
	Name string
	// Kind is "branch", "tag", or "revision".
	Kind string
	// IsDefault marks the repository's default branch.
	IsDefault bool
	// Missing is true when the requested ref does not resolve. The renderer
	// says so rather than silently substituting another ref.
	Missing bool
	// Detached is true when the view resolved to a bare revision.
	Detached bool
	// Retained is true when the ref itself is gone but its history is kept.
	Retained bool
	// Branches and Tags populate the picker.
	Branches []RefOption
	Tags     []RefOption
	// Revision is the resolved object id for the selected ref.
	Revision string
	// ShortRevision is the abbreviated form for display.
	ShortRevision string
}

// RefOption is one entry in the ref picker.
type RefOption struct {
	Name      string
	URL       string
	Selected  bool
	IsDefault bool
	// Retained marks history preserved after a force-push or deletion.
	Retained bool
}

// RepositoryOverview is the repository landing panel.
type RepositoryOverview struct {
	// Head is the latest commit on the selected ref. Zero when empty.
	Head CommitSummary
	// Recent are the newest commits on the selected ref, Head first. The
	// backend bounds the list; RecentMore says older commits exist.
	Recent     []CommitSummary
	RecentMore bool
	// DefaultBranch names the repository's default branch. Empty when the
	// repository has none that resolves.
	DefaultBranch string
	// OpenPullRequests counts open pull requests up to a bound.
	// OpenPullRequestsMore means the real count is above it, and
	// OpenPullRequestsKnown is false when the count could not be read.
	OpenPullRequests      int
	OpenPullRequestsMore  bool
	OpenPullRequestsKnown bool
	// DefaultCheck is the latest check attempt for the default branch tip.
	// DefaultCheckKnown is false when the record could not be read, and
	// HasDefaultCheck is false when no check has run for that revision.
	DefaultCheck      AttemptRecord
	HasDefaultCheck   bool
	DefaultCheckKnown bool
	DefaultCheckRev   string
	// Branches and Tags are the ref rows shown in the side column, newest
	// first. With many refs only the newest few are listed; BranchCount and
	// TagCount keep the real totals, and AllRefsURL shows every ref.
	Branches      []RefLine
	Tags          []RefLine
	BranchCount   int
	TagCount      int
	RetainedCount int
	// AllRefsURL lists every branch and tag. Empty when nothing is hidden.
	// FewerRefsURL returns to the short lists while every ref is shown.
	AllRefsURL   string
	FewerRefsURL string
	// RetainedRefs lists preserved history whose ref no longer exists.
	RetainedRefs []RefLine
	// Activity is the graph scoped to this repository.
	Activity ActivityGraph
	// PushCommands are the copyable first-push lines for an empty repository.
	// They contain no credentials.
	PushCommands []string
	// Readme is the README at the top of the selected ref, rendered like a
	// folder README in the code view. Nil when there is none.
	Readme *ReadmeView
	// Languages is the language make-up of the default branch.
	Languages LanguageSummary
}

// LanguageSummary is the Languages panel: up to six languages by size and
// then "Other", or a short note when no share can be shown.
type LanguageSummary struct {
	Rows []LanguageRow
	// Note explains an empty panel: nothing detected, too large, or not
	// counted. Empty when Rows are shown.
	Note MessageCode
	// AttributesIgnored says .gitattributes language settings were not
	// applied because the host Git cannot read them from a commit.
	AttributesIgnored bool
}

// LanguageRow is one language, or the folded rest when Other is true.
type LanguageRow struct {
	Name string
	// Color is a "#rrggbb" value from the language table. Empty for Other,
	// which uses the neutral theme color.
	Color string
	// Share is the exact percentage, used for the bar segment width.
	Share float64
	// Percent is the shown share with one decimal, for example "93.9%".
	Percent string
	Other   bool
}

// BarLabel is the text alternative of the language bar, in lang.
func (summary LanguageSummary) BarLabel(lang Lang) string {
	parts := make([]string, 0, len(summary.Rows))
	for _, row := range summary.Rows {
		name := row.Name
		if row.Other {
			name = Text(lang, MsgRepoLanguagesOther)
		}
		parts = append(parts, name+" "+row.Percent)
	}
	return Text(lang, MsgRepoLanguagesBarLabel) + ": " + strings.Join(parts, ", ")
}

// RefLine is one branch or tag row.
type RefLine struct {
	Name string
	URL  string
	// Kind is "branch" or "tag".
	Kind string
	// IsDefault marks the default branch.
	IsDefault bool
	// Retained marks preserved history after a force-push or deletion.
	Retained bool
	// Annotated marks an annotated tag.
	Annotated bool
	// Tip is the commit this ref points at. Zero when unavailable.
	Tip CommitSummary
	// RestoreURL opens the restore screen with this ref's tip preselected as
	// the source. Empty means no link.
	RestoreURL string
}

// CodeView is the file browser panel.
type CodeView struct {
	// Path is the current repository-relative path. Empty at the root.
	Path string
	// Crumbs are the navigable path segments, root first.
	Crumbs []Crumb
	// UpURL leaves the listed folder for its parent. Empty when the listed
	// folder is the root.
	UpURL string
	// Dir is the folder Entries list: Path for a folder, and the file's
	// folder when a file is open. Empty at the root.
	Dir string
	// Entries are the directory listing, already sorted.
	Entries []TreeEntry
	// File is set when Path names a file.
	File *FileView
	// Readme is the folder's rendered README. Nil when the folder has none or
	// a file is open.
	Readme *ReadmeView
	// NotFound is true when Path does not exist at the selected ref.
	NotFound bool
}

// Crumb is one breadcrumb segment.
type Crumb struct {
	Name string
	URL  string
	// Current marks the final segment, which is not a link.
	Current bool
}

// TreeEntry is one directory or file in the listing.
type TreeEntry struct {
	Name string
	Path string
	URL  string
	// Kind is "dir", "file", "symlink", or "submodule".
	Kind string
	// Size is the blob size in bytes. Valid for files only.
	Size int64
}

// FileView is one file's displayed content. The renderer escapes every line;
// repository content is never treated as markup. The one exception is
// Rendered, which is HTML the backend produced with internal/markdown.
type FileView struct {
	Path string
	Size int64
	// Lines are the text lines without trailing newlines.
	Lines []string
	// Binary is true when the file is not displayable text.
	Binary bool
	// Truncated is true when only the first Lines were loaded.
	Truncated bool
	// RawURL downloads the file. Empty when the backend does not offer it.
	RawURL string
	// RawTooLarge is true when the file is above the download limit; the
	// page then says so instead of offering RawURL.
	RawTooLarge bool
	// Image is true for a raster picture (PNG, JPEG, GIF or WebP) that the
	// page shows through RawURL. The backend sets it only when the file's
	// bytes match its type and it is within the download limit. SVG and other
	// formats that can carry script are never shown as a picture.
	// ImageWidth and ImageHeight are its pixel size, zero when unknown.
	Image       bool
	ImageWidth  int
	ImageHeight int
	// RestoreURL opens the restore screen with this file preselected. Empty
	// means no link.
	RestoreURL string

	// Document is true for a Markdown file. PreviewURL and SourceURL switch
	// between the rendered document and its source lines; ShowSource says
	// which one this page shows. Rendered is empty when the file could not
	// be rendered; the page then shows the source without the switch, with
	// NotRendered saying why.
	Document    bool
	NotRendered MessageCode
	ShowSource  bool
	PreviewURL  string
	SourceURL   string
	// Rendered is the document as HTML. The backend produces it with
	// internal/markdown, which never passes raw HTML from the file through
	// and resolves every link and image itself.
	Rendered template.HTML
}

// ReadmeView is a folder's README, rendered below the folder listing.
type ReadmeView struct {
	// Path is the README's repository path; URL opens it in the file view.
	Path string
	URL  string
	// Rendered is produced like FileView.Rendered. When it is empty, Note
	// says why and the page links to the file instead.
	Rendered template.HTML
	Note     MessageCode
}

// CommitsView is the commit history panel.
type CommitsView struct {
	// List is the history of the selected ref, newest first.
	List []CommitSummary
	// OlderURL and NewerURL page through history. Empty when unavailable.
	OlderURL string
	NewerURL string
	// Detail is the opened commit. Nil on the list-only view.
	Detail *CommitDetail
	// NotFound is true when a requested commit id does not resolve.
	NotFound bool
}

// CommitSummary is one commit row.
type CommitSummary struct {
	// OID is the full object id; ShortOID is the abbreviated display form.
	OID      string
	ShortOID string
	// Subject is the first line of the message.
	Subject string
	// AuthorName is the commit author. AuthorDate drives activity counting.
	AuthorName string
	AuthorDate time.Time
	URL        string
}

// CommitDetail is one opened commit with its diff.
type CommitDetail struct {
	Commit CommitSummary
	// Body is the message after the subject line.
	Body string
	// CommitterName and CommitterDate are shown when they differ from the
	// author, so the author date is not read as the whole story. See
	// HasDistinctCommitter.
	CommitterName string
	CommitterDate time.Time
	// Parents are the parent commits.
	Parents []CommitSummary
	// Files are the changed paths.
	Files []DiffFile
	// SelectedPath is set when the commit was opened for one file: Files
	// then holds only that file, and AllFilesURL shows every file again.
	SelectedPath string
	AllFilesURL  string
	// Truncated is true when the diff was too large to load completely.
	Truncated bool
	// Unavailable is true when the diff could not be produced, for example for
	// a merge commit the backend does not expand.
	Unavailable bool
	// RestoreURL opens the restore screen with this commit as the source.
	// Empty means no link.
	RestoreURL string

	// UnavailableReason explains why. Used only when Unavailable is true.
	UnavailableReason MessageCode
}

// HasDistinctCommitter reports whether the committer identity differs from the
// author's in name or in time. Git records both, and they are ordinary
// metadata a client may set to any value, so a difference is only a difference:
// it does not prove an amend, a rebase, or any particular order of events. A
// committer date can even precede the author date. The interface therefore
// shows both identities and leaves the interpretation to the reader.
//
// Comparing only names would hide the common case where one person rewrites
// their own commit, since the name stays the same while the dates diverge.
//
// The dates are compared as instants, so the same moment recorded with a
// different UTC offset is not a difference.
func (d CommitDetail) HasDistinctCommitter() bool {
	if d.CommitterName == "" {
		// The backend has nothing to report about the committer.
		return false
	}
	if d.CommitterName != d.Commit.AuthorName {
		return true
	}
	if d.CommitterDate.IsZero() || d.Commit.AuthorDate.IsZero() {
		// Without both dates there is no difference to state.
		return false
	}
	return !d.CommitterDate.Equal(d.Commit.AuthorDate)
}

// DiffFile is one changed path in a commit.
type DiffFile struct {
	// Path is the current path; OldPath is set for renames and copies.
	Path    string
	OldPath string
	// Status is "added", "modified", "deleted", "renamed", or "copied".
	Status string
	// Additions and Deletions are the line counts.
	Additions int
	Deletions int
	// Binary is true when no text diff exists.
	Binary bool
	// URL shows this file's changes alone, for example within a commit.
	URL string
	// Selected marks the file a single-file commit view was opened for.
	Selected bool
	// Hunks hold the diff body.
	Hunks []DiffHunk
	// NotLoaded is true when the file's text changes were left out because
	// the whole change was too large for one page. A non-empty URL then
	// shows them.
	NotLoaded bool
}

// DiffTotals sums the line counts of a list of changed files.
type DiffTotals struct {
	Additions int
	Deletions int
	// Text is true when at least one file has line counts, so a list of
	// binary files shows no misleading "+0 -0".
	Text bool
}

// DiffHunk is one @@ section.
type DiffHunk struct {
	// Header is the literal hunk header line.
	Header string
	Lines  []DiffLine
}

// DiffLine is one diff row. Kind is "context", "add", or "del".
type DiffLine struct {
	Kind string
	// OldLine and NewLine are 0 when the side has no number.
	OldLine int
	NewLine int
	// Text excludes the leading +/-/space marker.
	Text string
}

// ---------------------------------------------------------------------------
// Repository creation
// ---------------------------------------------------------------------------

// NewRepositoryPage renders GET /repositories/new. The form posts to
// /repositories with "name" and optional "description".
type NewRepositoryPage struct {
	Chrome    Chrome
	SubmitURL string
	// Name and Description redisplay the submitted values after a failure.
	Name        string
	Description string
	// NameRules is shown next to the field so the rule is visible before a
	// failure, not only after it.
	NameRules MessageCode
}

func (NewRepositoryPage) page() string { return "new-repository" }

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// Restore selection values submitted in the "mode" field.
const (
	// RestoreModeAll restores the whole project tree as the source commit
	// recorded it, including deleting files the source does not have.
	RestoreModeAll = "all"
	// RestoreModeFiles restores only the paths submitted in the repeated
	// "path" field.
	RestoreModeFiles = "files"
)

// RestoreConfirm is the value the "confirm" field must carry before the apply
// request is a restore request at all. The reader ticks it after reading the
// preview, and the backend checks it again; neither side treats a rendered
// form as permission.
const RestoreConfirm = "restore"

// RestorePage renders GET /repositories/{id}/restore and the result of
// POST /repositories/{id}/restore/preview.
//
// Restoring is two deliberate steps. The first form posts the selection to
// PreviewURL and gets back the same page with Previewed set and Changes
// filled. The second form posts that reviewed selection to ApplyURL together
// with ExpectedHead and confirm=restore. Every field is redisplayed input:
// the backend revalidates the selection, recomputes the result, and refuses a
// target whose tip no longer matches ExpectedHead.
//
// Applying writes a new commit on the target branch. It never rewrites the
// branch's existing history, and it cannot reach a working copy on another
// computer.
type RestorePage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	// Tabs are the repository's sections, shown in the sidebar.
	Tabs RepoTabs
	// Source is the commit the files come from, already resolved by the
	// backend. Its OID is submitted in the "source" field.
	Source CommitSummary
	// TargetBranch is the branch the restore writes to, submitted in the
	// "target" field. It may name a branch that does not exist, which the
	// restore creates at the selected commit.
	TargetBranch string
	// CreatesBranch reports that restoring would create TargetBranch rather
	// than add to an existing one. The backend observes this together with
	// ExpectedHead, so the page describes the branch from one observation
	// instead of inferring it from Branches, which is read separately and can
	// disagree with what the preview was computed against.
	CreatesBranch bool
	// Branches are existing branch names, offered only as suggestions. The
	// target is typed, because recreating a branch that was deleted is how
	// deleted work comes back and such a name is not in this list.
	Branches []RefOption
	// Mode is RestoreModeAll or RestoreModeFiles, submitted in the "mode"
	// field.
	Mode string
	// Paths are the source commit's files, each with the change restoring it
	// would make and whether it is currently selected. Selected paths are
	// submitted in the repeated "path" field.
	Paths []RestorePath
	// Changes are the previewed differences between the target branch and the
	// restored result, including deletions. Filled only after a preview.
	Changes []DiffFile
	// ExpectedHead is the target tip the preview was computed against, or the
	// zero OID when the target does not exist. It is submitted with the apply
	// request so a branch that moved in the meantime fails instead of being
	// overwritten.
	ExpectedHead string
	// Previewed is true once the page shows the result of a preview. Until
	// then there is no apply form at all.
	Previewed bool
	// CanApply is the backend's decision that this previewed selection is
	// still applicable. False disables the apply control; the reason arrives
	// as a notice.
	CanApply bool
	// DiffTruncated is true when a file's text diff was too large to show in
	// full. The changed paths are still listed completely.
	DiffTruncated bool

	// PreviewURL and ApplyURL are the two POST targets. CancelURL returns to
	// the repository.
	PreviewURL string
	ApplyURL   string
	CancelURL  string
}

func (RestorePage) page() string { return "restore" }

// RestorePath is one file of the source commit offered for selection.
//
// Path is a repository path presented as text. It is never a host path, and
// the renderer escapes it like any other repository content.
type RestorePath struct {
	Path string
	// Status is the change restoring this path would make to the target
	// branch: "added", "modified", or "deleted". A deletion is listed as its
	// own entry, because a selected source that lacks a file removes that file
	// from the branch, and nobody should have to infer that from a directory.
	Status string
	// Selected is the current checkbox state.
	Selected bool
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrorPage renders an HTTP error inside the normal shell when possible.
type ErrorPage struct {
	Chrome Chrome
	// Status is the HTTP status code.
	Status int
	// Code selects the explanation text.
	Code MessageCode
	// Detail is an untranslated fragment such as an unknown path.
	Detail string
	// RetryURL offers a way forward. Empty when there is none.
	RetryURL string
}

func (ErrorPage) page() string { return "error" }

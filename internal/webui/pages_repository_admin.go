package webui

// Administrator repository screens: the Settings tab and the delete
// confirmation. The repository tab strip offers both to every viewer, with a
// lock mark for a viewer without an administrator session. The routes require
// that session and send anyone else to the administrator login, which returns
// to the screen afterwards.

const (
	RepoTabSettings RepoTab = "settings"
	RepoTabDelete   RepoTab = "delete"
)

// Delete modes, as the delete form submits them. They match the backend's
// repository.DeleteMode values.
const (
	DeleteModeKeepFiles   = "keep_files"
	DeleteModeDeleteFiles = "delete_files"
)

// RepositorySettingsPage renders /repositories/{id}/settings.
type RepositorySettingsPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs
	// SelfURL is this screen's GET address, used as the canonical address when
	// the page answers a refused POST.
	SelfURL string
	// DefaultBranchURL is the POST target that changes the default branch.
	DefaultBranchURL string
	// DefaultBranch is the current default branch. DefaultBranchMissing is
	// true when HEAD names a branch that does not exist, which is how an
	// imported repository with only "master" can look.
	DefaultBranch        string
	DefaultBranchMissing bool
	// Branches are the existing branch names, the only values the selector
	// offers. Selected is the value to preselect, which after a refusal is
	// the one the administrator submitted. An empty Selected renders a
	// disabled "Choose a branch" entry first, so nothing is chosen for them.
	Branches []string
	Selected string
	// HistoryURL is the POST target that saves kept history and default
	// branch protection.
	HistoryURL string
	// KeptHistory is the repository's own choice, one of "default", "on"
	// and "off". ServerKeepsHistory is the server setting it follows under
	// "default": "on", "off", or "" when it cannot be read.
	KeptHistory        string
	ServerKeepsHistory string
	// ProtectDefaultBranch is the saved protection of the default branch.
	ProtectDefaultBranch bool
	// HistoryUnreadable is true when the saved choices cannot be read. The
	// form then shows the defaults, and saving it replaces the saved row.
	HistoryUnreadable bool
	// HistoryNotices are the notices of a refused save of these choices,
	// shown in their form.
	HistoryNotices []Notice
	// NamespacesURL is the POST target that saves the extra ref
	// namespaces. Namespaces holds them one per line, or what a refused
	// save sent. NamespacesUnreadable is true when the saved list cannot be
	// read; the form then starts empty, and saving it replaces the list.
	NamespacesURL        string
	Namespaces           string
	NamespacesUnreadable bool
	NamespacesNotices    []Notice
	// The repository's other administrator screens, each with one line of
	// explanation on the page. An empty URL renders no entry.
	ConfiguredChecksURL  string
	RunnerTokensURL      string
	HelperCredentialsURL string
	ImportURL            string
	DeleteURL            string
}

func (RepositorySettingsPage) page() string     { return "repository-settings" }
func (p RepositorySettingsPage) chrome() Chrome { return p.Chrome }

// RepositoryDeletePage renders /repositories/{id}/delete, the confirmation
// that asks for a mode, the typed repository name unless Settings turned
// that off, and the administrator password.
type RepositoryDeletePage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs
	// SelfURL is this screen's GET address; SubmitURL is the POST target.
	SelfURL   string
	SubmitURL string
	// Mode is the choice to keep selected after a refusal. Empty on first
	// render: neither consequence is chosen for the administrator.
	Mode string
	// GitPath is where the repository's Git folder lives now, and RemovedPath
	// is the hidden folder a kept copy moves into. Either may be empty when
	// the backend cannot name it.
	GitPath     string
	RemovedPath string
	// Name says whether the typed name is asked for.
	Name DeleteNameRule
	// CancelURL leaves without deleting.
	CancelURL string
}

// DeleteNameRule is the Settings choice whether deleting a repository asks
// for its typed name. Unreadable means the saved choice cannot be read:
// the page says so and a deletion is refused until it is set again.
type DeleteNameRule struct {
	Required   bool
	Unreadable bool
}

func (RepositoryDeletePage) page() string     { return "repository-delete" }
func (p RepositoryDeletePage) chrome() Chrome { return p.Chrome }

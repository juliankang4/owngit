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
	// The repository's other administrator screens, each with one line of
	// explanation on the page. An empty URL renders no entry.
	ConfiguredChecksURL  string
	RunnerTokensURL      string
	HelperCredentialsURL string
	ImportURL            string
	DeleteURL            string
}

func (RepositorySettingsPage) page() string { return "repository-settings" }

// RepositoryDeletePage renders /repositories/{id}/delete, the confirmation
// that asks for a mode, the typed repository name, and the administrator
// password.
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
	// CancelURL leaves without deleting.
	CancelURL string
}

func (RepositoryDeletePage) page() string { return "repository-delete" }

package webui

const (
	ActionImportConfigure        = "import_configure"
	ActionImportCredentials      = "import_credentials"
	ActionImportClearCredentials = "import_clear_credentials"
	ActionImportRefresh          = "import_refresh"
	ActionImportCancel           = "import_cancel"
	ActionImportSchedule         = "import_schedule"
	ActionImportResolve          = "import_resolve"
	RepoTabImport                = "import"
)

// ImportRunRow is one credential-free run shown on the import screen.
type ImportRunRow struct {
	ID      string
	Kind    string
	Status  string
	Started string
	Message string
	// ErrorClass is the stable failure class. The page explains it in the
	// reader's language and keeps Message as secondary technical detail.
	ErrorClass string
	RowID      int64
}

// ImportRefRow is one observed ref. The state word is visible text.
type ImportRefRow struct {
	Name   string
	State  string
	Source string
	Local  string
}

// ImportPage renders GET /repositories/{id}/import.
type ImportPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	SelfURL   string
	SubmitURL string
	OlderURL  string

	// Admin is true for an administrator session. Only then does the backend
	// fill the administrator data below (URL, the consents, the credential
	// fields and run messages) and does the page draw the change forms.
	// Everyone else sees the status and each change as a locked link.
	Admin bool
	// AdminLoginURL asks for the administrator password and returns to this
	// tab. SetupURL opens the source form: for an administrator this tab
	// with the form shown, for anyone else the password prompt first.
	AdminLoginURL string
	SetupURL      string
	// Setup shows the source form. It opens on request and stays open after
	// a refused change.
	Setup bool
	// CredentialChoice is the credential form submitted with a refused
	// change, so the same fields are shown again. Empty otherwise.
	CredentialChoice string

	Available         bool
	Configured        bool
	URL               string
	Mode              string
	GitOnlyConsent    bool
	PrivateNetwork    bool
	CredentialForm    string
	CredentialBound   bool
	CAPresent         bool
	ContentIncomplete bool
	Unresolved        int
	StagingIssues     int
	// SchedulerFailed and ReconcileFailed report this process's import
	// runtime problems, so they are visible beside the repository's status.
	SchedulerFailed  bool
	ReconcileFailed  bool
	RefsTruncated    bool
	ScheduleEnabled  bool
	ScheduleInterval string
	Refs             []ImportRefRow
	Last             *ImportRunRow
	Active           *ImportRunRow
	History          []ImportRunRow
}

func (ImportPage) page() string { return "import" }

// NewImportPage renders GET /repositories/new-import.
type NewImportPage struct {
	Chrome         Chrome
	SubmitURL      string
	Name           string
	Description    string
	URL            string
	Mode           string
	GitOnlyConsent bool
	PrivateNetwork bool
	// CredentialForm is the submitted credential form, so a refused import
	// shows the same fields again. The secrets themselves are never echoed.
	CredentialForm string
}

func (NewImportPage) page() string { return "new-import" }

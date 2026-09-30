package webui

// CodingToolsPage renders "/coding-tools": how to connect a coding tool to
// this server, the helper credentials of every repository, and the tasks
// updated most recently.
type CodingToolsPage struct {
	Chrome Chrome
	// Server is this server's address as coding tools reach it.
	Server string
	// PlainHTTP is true when Server uses plain HTTP, so the commands carry
	// --accept-insecure-http.
	PlainHTTP bool
	// PasswordFile is true when general access needs the shared password,
	// so the commands name a password file the owner fills in.
	PasswordFile bool
	// SkillCommand installs the skill; ClaudeCommand, CodexCommand and
	// MCPCommand add or start the MCP server for this server.
	SkillCommand  string
	ClaudeCommand string
	CodexCommand  string
	MCPCommand    string

	// CredentialsShown is true when the viewer may see administrator data.
	// Otherwise the credentials are not read and the page offers the
	// administrator sign-in.
	CredentialsShown bool
	Credentials      []RepositoryHelperCredential
	// CredentialsUnavailable is true when the credentials could not be read.
	CredentialsUnavailable bool

	Tasks []RecentTask
	// TasksTruncated is true when older tasks were left out; TasksLimit is
	// how many are shown then.
	TasksTruncated bool
	TasksLimit     int
	// TasksUnavailable is true when the tasks could not be read.
	TasksUnavailable bool
}

func (CodingToolsPage) page() string     { return "coding-tools" }
func (p CodingToolsPage) chrome() Chrome { return p.Chrome }

// RepositoryHelperCredential is one helper credential with the repository
// it belongs to. ManageURL opens that repository's helper credentials.
type RepositoryHelperCredential struct {
	RepositoryName string
	ManageURL      string
	Credential     HelperCredentialRow
}

// RecentTask is one check task with the repository it belongs to.
type RecentTask struct {
	RepositoryName string
	Task           TaskSummary
}

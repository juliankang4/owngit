package webui

import "time"

const (
	ChecksSectionTasks     = "tasks"
	ChecksSectionWorkflows = "workflows"
	ChecksSectionRuns      = "runs"
)

const (
	WorkflowViewFiles    = "files"
	WorkflowViewRuns     = "runs"
	WorkflowViewRun      = "run"
	WorkflowViewJob      = "job"
	WorkflowViewDispatch = "dispatch"
)

const (
	ActionTurnOnWorkflows = "turn_on"
	ActionCancelRun       = "cancel"
	ActionRerunRun        = "rerun"
	ActionSetSecret       = "set"
	ActionRemoveSecret    = "remove"
)

type ChecksNav struct {
	TasksURL     string
	WorkflowsURL string
	RunsURL      string
	Active       string
}

// WorkflowsPage renders the Workflows and Runs sections of the Checks tab, one
// run, one job and the Run workflow form. View selects which.
type WorkflowsPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs
	Nav    ChecksNav
	View   string
	// SelfURL is this screen's GET address. A refused form answers on its
	// POST route, so language links use this address.
	SelfURL   string
	SubmitURL string
	// Problem is a workflow refusal from the last submitted form.
	Problem *WorkflowMessage
	// Unavailable is true when the records this view needs could not be read.
	Unavailable bool
	NotFound    bool

	Files    *WorkflowFilesView
	Runs     *WorkflowRunsView
	Run      *WorkflowRunView
	Job      *WorkflowJobView
	Dispatch *WorkflowDispatchView
}

func (WorkflowsPage) page() string     { return "workflows" }
func (p WorkflowsPage) chrome() Chrome { return p.Chrome }

type WorkflowFilesView struct {
	Branch    string
	Branches  []string
	SourceOID string
	ShortOID  string
	Status    WorkflowSwitch
	Files     []WorkflowFileView
	// NoCommits is true when the repository has no branch to read.
	NoCommits          bool
	SecretsURL         string
	AutomaticChecksURL string
}

type WorkflowSwitch struct {
	// PolicySaved is false before any check policy exists. Turning on then
	// saves Review as the first policy.
	PolicySaved  bool
	RunWorkflows bool
	// ConsentActive is true while checks are on for the current policy.
	ConsentActive bool
	Version       int64
	Events        []WorkflowEventState
	// Review is the policy Turn on saves. Nil while workflows are on, and
	// for a legacy policy that Automatic checks must save first.
	Review *WorkflowTurnOn
}

func (s WorkflowSwitch) On() bool { return s.RunWorkflows && s.ConsentActive }

type WorkflowEventState struct {
	Event   string
	Allowed bool
}

type WorkflowTurnOn struct {
	Executor     string
	Events       []string
	QueueLimit   int
	MaxTimeoutMS int64
	Digest       string
	BaseVersion  int64
	BaseDigest   string
	// Counts summarise the files below as Turn on would admit them, with
	// the same verdicts as the file cards.
	Run, Noted, Partial, Refused, NeverFits, Unknown int
}

const (
	VerdictRuns      = "runs"
	VerdictNoted     = "noted"
	VerdictPartial   = "partial"
	VerdictRefused   = "refused"
	VerdictNeverFits = "never_fits"
	VerdictUnknown   = "unknown"
)

type WorkflowFileView struct {
	Path     string
	Name     string
	ShortOID string
	Verdict  string
	Refusal  *WorkflowMessage
	Notes    []WorkflowMessage
	Triggers []WorkflowTriggerView
	Inputs   []WorkflowInputView
	// Schedules are the stored schedule entries of the default branch.
	Schedules []WorkflowScheduleView
	Jobs      []WorkflowJobPreview
	// ExpandedJobs is how many jobs one run starts, when known.
	ExpandedJobs   int
	ExpansionKnown bool
	// DispatchURL opens the Run workflow form. Empty without a
	// workflow_dispatch trigger.
	DispatchURL string
}

type WorkflowTriggerView struct {
	Event   string
	Allowed bool
	Filters []WorkflowFilter
	Notes   []WorkflowMessage
}

type WorkflowFilter struct {
	Label  MessageCode
	Values []string
}

type WorkflowInputView struct {
	Name        string
	Description string
	Type        string
	Required    bool
	Default     string
	Options     []string
}

type WorkflowScheduleView struct {
	Cron           string
	NextDueAt      time.Time
	LastAdmittedAt time.Time
	LastRunURL     string
	Paused         bool
	Notes          []WorkflowMessage
}

type WorkflowJobPreview struct {
	Key     string
	Name    string
	RunsOn  []string
	Needs   []string
	Verdict string
	Refusal *WorkflowMessage
	Steps   []WorkflowStepLine
}

// WorkflowStepLine is a step's display name and command, never a full script.
type WorkflowStepLine struct {
	Name    string
	Command string
	Uses    string
}

type WorkflowRunsView struct {
	Runs []WorkflowRunRow
	// Truncated is true when older runs exist beyond Limit.
	Truncated bool
	Limit     int
}

type WorkflowRunRow struct {
	URL          string
	WorkflowPath string
	Event        string
	Conclusion   string
	CreatedAt    time.Time
	Counts       WorkflowCounts
	Stale        bool
	ShortOID     string
}

type WorkflowCounts struct {
	Total, Waiting, Queued, Running, Passed, Failed, Cancelled, Skipped, Incomplete, Refused int
}

type WorkflowRunView struct {
	ID              string
	Number          int64
	WorkflowPath    string
	WorkflowName    string
	Event           string
	Branch          string
	SourceOID       string
	ShortOID        string
	Conclusion      string
	CreatedAt       time.Time
	ScheduledFor    time.Time
	RerunGeneration int64
	PullRequest     int64
	PullRequestURL  string
	Inputs          []WorkflowValue
	Notes           []WorkflowMessage
	Refused         []WorkflowRefusedJob
	Jobs            []WorkflowJobRow
	Secrets         []WorkflowSecretUse
	// SecretsKnown is false when secret metadata could not be read; the
	// secrets are then neither set nor unset on this screen.
	SecretsKnown    bool
	CancelRequested bool
	Cancellable     bool
	Rerunnable      bool
}

type WorkflowValue struct{ Name, Value string }

type WorkflowRefusedJob struct {
	Key    string
	Reason WorkflowMessage
}

type WorkflowSecretUse struct {
	Name string
	Set  bool
}

type WorkflowJobRow struct {
	ID              string
	Key             string
	MatrixIndex     int
	URL             string
	Status          string
	Tolerated       bool
	CancelRequested bool
	Summary         string
	StartedAt       time.Time
	FinishedAt      time.Time
	Steps           []WorkflowStepRow
}

// WorkflowStepRow is one step's outcome. Command and Excerpt are set only on
// the job page.
type WorkflowStepRow struct {
	Name          string
	Command       string
	Status        string
	Role          string
	ExitCode      string
	DurationMS    int64
	Excerpt       string
	Truncated     bool
	CleanupError  string
	CleanupFailed bool
}

type WorkflowJobView struct {
	RunURL       string
	RunNumber    int64
	WorkflowPath string
	Job          WorkflowJobRow
	// LogURL opens the raw log. Empty when the log cannot be read.
	LogURL       string
	LogStatus    string
	LogExpiresAt time.Time
	LogTruncated bool
	// Unreadable is true when the job's attempt could not be read.
	Unreadable bool
}

type WorkflowDispatchView struct {
	Path      string
	Name      string
	Branch    string
	Branches  []string
	SourceOID string
	ShortOID  string
	Inputs    []WorkflowInputField
	// Blocked explains why the policy refuses a manual run now.
	Blocked *WorkflowMessage
	BackURL string
}

type WorkflowInputField struct {
	WorkflowInputView
	Value   string
	Checked bool
}

type WorkflowSecretsPage struct {
	Chrome    Chrome
	Repo      RepositoryHeader
	Tabs      RepoTabs
	SubmitURL string
	Secrets   []WorkflowSecretRow
	// Unavailable is true when the secret metadata could not be read.
	Unavailable          bool
	RunnerTokensMayExist bool
	// PendingAction and PendingName scope a refused form.
	PendingAction string
	PendingName   string
	// Confirm names the secret whose removal is being confirmed.
	Confirm string
	// Name is the name typed into a refused set form. The value is never
	// shown again.
	Name               string
	AutomaticChecksURL string
}

func (WorkflowSecretsPage) page() string     { return "workflow-secrets" }
func (p WorkflowSecretsPage) chrome() Chrome { return p.Chrome }

type WorkflowSecretRow struct {
	Name      string
	UpdatedAt time.Time
}

package webui

import "time"

// Configured-check screens.
//
// These two pages let a repository owner read and change the execution policy
// of repository-configured checks, turn execution on or off, read what the
// recorded jobs actually did, and manage the tokens a separately connected
// runner uses.
//
// They follow the same rule as the rest of this package: every value arrives
// already authorized and already observed by the backend. Nothing here reads
// storage, decides authority, or infers an outcome. A saved policy, a current
// consent, and an observed runtime are three separate facts and are rendered
// as three separate facts.

// Actions submitted in the "action" field of the configured-check forms.
// These are the exact strings the forms send, so a handler compares against
// the same values the markup writes.
const (
	// ActionSaveCheckPolicy stores the policy without enabling execution.
	// Fields: csrf, action, admin_password, and the policy fields.
	ActionSaveCheckPolicy = "save_policy"
	// ActionEnableChecks binds consent to the saved policy.
	// Fields: csrf, action, admin_password.
	ActionEnableChecks = "enable_checks"
	// ActionDisableChecks revokes consent. Same fields as enabling.
	ActionDisableChecks = "disable_checks"
	// ActionCancelCheckJob asks the owner of the work to stop.
	// Fields: csrf, action, admin_password, job_id.
	ActionCancelCheckJob = "cancel_job"
	// ActionRerunCheckJob requests one explicit new run of a finished job.
	// Same fields as cancelling.
	ActionRerunCheckJob = "rerun_job"
	// ActionIssueRunnerToken issues one repository-scoped runner token.
	// Fields: csrf, action, admin_password, label, creation_id.
	ActionIssueRunnerToken = "issue_runner_token"
	// ActionRevokeRunnerToken retires one.
	// Fields: csrf, action, admin_password, credential_id.
	ActionRevokeRunnerToken = "revoke_runner_token"
)

// Executors the owner can select. They are modes, not degrees of safety, and
// OwnGit never moves work from one to another.
const (
	ExecutorHost           = "host"
	ExecutorContainer      = "container"
	ExecutorExternalRunner = "external_runner"
)

// Events a policy may admit.
const (
	CheckEventPush        = "push"
	CheckEventPullRequest = "pull_request"
)

// Container network modes.
const (
	ContainerNetworkNone   = "none"
	ContainerNetworkBridge = "bridge"
)

// Job states. They mirror the durable record. Queued, claimed, and started are
// unfinished; the rest are final. None of them is a pass except JobPassed, and
// a value this package does not recognise renders as unrecognised.
const (
	JobPending     = "pending"
	JobClaimed     = "claimed"
	JobStarted     = "started"
	JobPassed      = "passed"
	JobFailed      = "failed"
	JobError       = "error"
	JobCancelled   = "cancelled"
	JobIncomplete  = "incomplete"
	JobUnavailable = "unavailable"
	JobAmbiguous   = "ambiguous"
	JobInterrupted = "interrupted"
)

// Runtime conditions the backend reports when configured-check execution is
// closed. An unrecognised code is shown as recorded rather than explained
// wrongly.
const (
	RuntimeWorkspaceUnavailable = "workspace_unavailable"
	RuntimeRestartUnavailable   = "restart_reconciliation_unavailable"
)

// ConfiguredChecksPage renders GET /repositories/{id}/configured-checks and
// the responses to its forms.
//
// The screen is administrator only. That is decided by the backend route, not
// by which controls this page draws.
type ConfiguredChecksPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// SelfURL is this screen's own GET address. A refused form is rendered on
	// the POST route, so a language link built from the request URL would be a
	// GET at an address that answers a form. SubmitURL receives the forms.
	SelfURL   string
	SubmitURL string
	// TasksURL opens the recorded check history, and RunnerTokensURL the
	// runner tokens. Empty renders no link.
	TasksURL        string
	RunnerTokensURL string

	// Policy is what is stored right now, including whether anything is.
	Policy CheckPolicyView
	// Form is what the fields render. After a refused submission it carries
	// the values that were submitted, so nothing the owner typed is lost.
	Form CheckPolicyForm
	// Runtime is what the backend observed about the execution environment.
	// It is not consent and not a check result.
	Runtime CheckRuntimeView

	// Jobs are the recorded jobs, newest first. Detail is one opened job.
	Jobs          []CheckJobRow
	JobsTruncated bool
	Detail        *CheckJobDetail

	// PendingAction is the action whose form carries a validation failure.
	PendingAction string

	// JobsUnavailable is true when the job records could not be read. The
	// section says so instead of rendering an empty list that reads as "none".
	JobsUnavailable bool

	// CheckFile is what the default branch currently holds at the check file
	// path.
	CheckFile CheckFileView
	// ActiveRunnerTokens counts the repository's unrevoked runner tokens, and
	// RunnerTokensKnown says whether that count could be read. A token is not
	// proof that a runner is connected; the count only tells an owner who
	// chose the runner mode whether they have created one at all.
	ActiveRunnerTokens int
	RunnerTokensKnown  bool
}

func (ConfiguredChecksPage) page() string { return "configured-checks" }

// CheckPolicyView is the stored policy as the backend read it.
type CheckPolicyView struct {
	// Saved is false when this repository has no policy at all.
	Saved bool
	// Version is the stored generation. Digest is the full identity of that
	// generation and ShortDigest the abbreviation shown to a reader.
	//
	// Both are present because they answer different needs. The abbreviation
	// is what a person compares at a glance; the full value is what the enable
	// form quotes back so the store can confirm, inside the transaction that
	// records the consent, that the approval belongs to the generation the
	// screen actually drew. An abbreviation is not a safe identity for that:
	// two generations can share a prefix. The digest is a content identity the
	// API already returns, not a secret.
	Version     int64
	Digest      string
	ShortDigest string
	Executor    string
	// AllowedEvents is the stored event set, already ordered by the backend.
	AllowedEvents       []string
	MaxTimeoutMS        int64
	MaxOutputLimitBytes int64
	QueueLimit          int
	MaxActiveJobs       int
	MaxLeaseMS          int64
	Source              CheckSourceLimitsView
	Container           CheckContainerView
	// Legacy marks a policy restored without its execution settings. It is
	// history: it cannot receive consent until a current policy is saved.
	Legacy bool
	// ConsentActive is the local confirmation, and ConsentVersion its
	// generation. Consent is not the policy and not the runtime.
	ConsentActive  bool
	ConsentVersion int64
	UpdatedAt      time.Time
}

// AllowsPush and AllowsPullRequest report the stored event selection.
func (p CheckPolicyView) AllowsPush() bool { return containsEvent(p.AllowedEvents, CheckEventPush) }

func (p CheckPolicyView) AllowsPullRequest() bool {
	return containsEvent(p.AllowedEvents, CheckEventPullRequest)
}

// UsesContainer reports whether the stored container settings apply.
func (p CheckPolicyView) UsesContainer() bool { return p.Executor == ExecutorContainer }

func containsEvent(events []string, event string) bool {
	for _, candidate := range events {
		if candidate == event {
			return true
		}
	}
	return false
}

// CheckSourceLimitsView are the stored bounds for one exact source snapshot.
type CheckSourceLimitsView struct {
	MaxEntries    int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxPathDepth  int
	MaxPathBytes  int
	MaxNameBytes  int
	MetadataLimit int64
}

// CheckContainerView are the stored container settings. They are meaningful
// only for the container executor.
type CheckContainerView struct {
	Image        string
	Runtime      string
	Network      string
	CPUMillis    int64
	MemoryBytes  int64
	PIDs         int64
	ScratchBytes int64
}

// FieldRange is the accepted range of one numeric field, as the backend
// published it.
//
// The interface never computes, stores or guesses a bound. It receives the
// numbers the backend enforces and prints them, so the range an operator reads
// is the range that will actually refuse their value.
type FieldRange struct {
	// Min is the fixed lower bound, meaningful when MinLabel is empty.
	Min int64
	// Max is the upper bound. Every numeric field has one, so it is always
	// shown.
	Max int64
	// MinLabel names the field this one cannot fall below, for a limit whose
	// floor is another setting on the same screen. A moving floor is still a
	// statement that can be made, and it never suppresses the maximum.
	MinLabel MessageCode
	// Known is false for a field the backend publishes no range for. Nothing
	// is claimed in that case.
	Known bool
}

// CheckPolicyForm holds the policy fields as text.
//
// Text rather than numbers on purpose: a refused submission re-renders exactly
// what was typed, including a value the backend rejected, instead of silently
// replacing it with a parsed zero.
type CheckPolicyForm struct {
	// Ranges maps a field name to the range the backend accepts for it. The
	// adapter fills it from the backend's own table; the editor prints it
	// beside each field so "the accepted range" is shown rather than merely
	// referred to.
	Ranges map[string]FieldRange
	// Defaults maps a field name to the value the backend uses when the field
	// is left empty. A field without an entry has no default and is required.
	Defaults map[string]int64

	Executor            string
	PushSelected        bool
	PullRequestSelected bool

	// Limits holds every numeric field by its backend name, as an amount and
	// a unit. See PolicyLimitFields for the list.
	Limits map[string]LimitInput

	ContainerImage   string
	ContainerNetwork string
}

// Limit is one numeric field as the form shows it. A field the map does not
// hold starts empty in its usual unit.
func (f CheckPolicyForm) Limit(field string) LimitInput {
	if value, present := f.Limits[field]; present {
		return value
	}
	if limit, known := PolicyLimitFor(field); known {
		return LimitInput{Unit: limit.Unit}
	}
	return LimitInput{}
}

// IsHost, IsContainer, and IsExternalRunner select the checked radio.
func (f CheckPolicyForm) IsHost() bool      { return f.Executor == ExecutorHost }
func (f CheckPolicyForm) IsContainer() bool { return f.Executor == ExecutorContainer }
func (f CheckPolicyForm) IsExternalRunner() bool {
	return f.Executor == ExecutorExternalRunner
}

// NetworkIsBridge reports the selected container network.
func (f CheckPolicyForm) NetworkIsBridge() bool { return f.ContainerNetwork == ContainerNetworkBridge }

// Check file states on the default branch.
const (
	// CheckFileFound means the file is there and OwnGit accepts it.
	CheckFileFound = "found"
	// CheckFileMissing means the default branch has no check file.
	CheckFileMissing = "missing"
	// CheckFileInvalid means the file is there but OwnGit refuses it.
	CheckFileInvalid = "invalid"
	// CheckFileNoCommits means the repository has no default branch commit
	// to look in yet.
	CheckFileNoCommits = "no_commits"
	// CheckFileUnreadable means the lookup itself failed. It is not a
	// statement that the file is absent.
	CheckFileUnreadable = "unreadable"
)

// CheckFileView is what the default branch says about the check file right
// now. It is a hint for the owner, not a job input: every job reads the file
// from the exact commit it checks.
type CheckFileView struct {
	// State is one of the CheckFile* values. Empty means it was not looked up.
	State string
	// Branch is the default branch name the lookup used.
	Branch string
	// Checks is the number of commands an accepted file defines.
	Checks int
	// Events are the events an accepted file turns on, in canonical order.
	Events []string
	// Problem is the parser's untranslated reason for refusing the file.
	Problem string
}

// Found reports an accepted check file.
func (v CheckFileView) Found() bool { return v.State == CheckFileFound }

// CheckRuntimeView is what the backend observed about the execution
// environment at startup. It is deliberately separate from policy and consent:
// an unavailable runtime is not a failed check and does not stop ordinary Git,
// pull request, or merge work.
type CheckRuntimeView struct {
	Available bool
	// Code is the backend's bounded reason code, shown as recorded data when
	// this package has no words for it.
	Code string
}

// CheckJobRow is one recorded job.
type CheckJobRow struct {
	ID      string
	ShortID string
	// URL opens this job on the same screen. Empty renders no link.
	URL string
	// Status is one of the Job* values.
	Status string
	// Trigger is CheckEventPush or CheckEventPullRequest as recorded.
	Trigger    string
	TriggerRef string
	// SourceOID is the exact commit the job was pinned to, and SourceShortOID
	// its abbreviation. Both are rendered: the abbreviation reads well in a
	// list, but only the full object id can be copied into a Git command or
	// compared with certainty, so it is never the only form offered.
	SourceOID      string
	SourceShortOID string
	// BaseOID is the pull request base the job was compared against, when the
	// record states one. It is shown in full for the same reason.
	BaseOID      string
	BaseShortOID string
	// Executor is the mode captured when the job was admitted, which is what
	// actually applied rather than the policy's current selection.
	Executor          string
	PullRequestNumber int64
	PullRequestURL    string
	TaskURL           string

	WorkflowPath         string
	ConfigurationVersion int64
	PolicyVersion        int64

	// CancelRequested is true once a cancellation was recorded. It is not a
	// claim that anything has stopped.
	CancelRequested bool
	Summary         string

	AdmittedAt time.Time
	StartedAt  time.Time
	FinishedAt time.Time

	// Cancellable and Rerunnable are the backend's rules, already applied.
	// A control the backend does not offer is not drawn.
	Cancellable bool
	Rerunnable  bool
}

// Unfinished reports a job with no final state recorded.
func (r CheckJobRow) Unfinished() bool {
	return r.Status == JobPending || r.Status == JobClaimed || r.Status == JobStarted
}

// CheckJobDetail is one opened job with what was recorded for it.
//
// Absence and unreadability are separate fields throughout. "This job is not in
// this repository" and "the store could not answer" lead a reader to opposite
// conclusions, so the second never borrows the first one's wording.
type CheckJobDetail struct {
	Job CheckJobRow
	// Checks are the commands captured with the job's configuration version.
	Checks []CheckDefinitionLine
	// ChecksMissing means the captured configuration version is gone.
	// ChecksUnreadable means it could not be read, which is not the same as a
	// job that defined no commands.
	ChecksMissing    bool
	ChecksUnreadable bool
	// Attempt is the recorded run. Nil alone does not mean nothing ran: read
	// it together with AttemptMissing and AttemptUnreadable below.
	Attempt *AttemptRecord
	// AttemptMissing and AttemptUnreadable apply only when the job names an
	// attempt. A job that names one has a run registered, so neither state may
	// ever render as "no run was registered".
	AttemptMissing    bool
	AttemptUnreadable bool
	// Log is the disposable raw log as far as it can be read now.
	Log CheckJobLogView
	// SubmitURL receives this job's cancel and rerun forms.
	SubmitURL string
	// BackURL returns to the job list.
	BackURL string
	// NotFound is true when the requested job id does not resolve in this
	// repository. Unreadable is true when the record could not be read at all.
	NotFound   bool
	Unreadable bool
}

// AttemptRegistered reports that the job names an attempt, whatever came of
// reading it. It is what separates "nothing ran" from "the run is unreadable".
func (d CheckJobDetail) AttemptRegistered() bool {
	return d.Attempt != nil || d.AttemptMissing || d.AttemptUnreadable
}

// CheckJobLogView is the raw log of one job's attempt.
//
// The content is recorded bytes. It is rendered as text and never as markup,
// and what is shown is bounded: a log larger than the display bound is cut and
// says so, rather than being presented as the whole log.
type CheckJobLogView struct {
	// Status is one of the Log* values.
	Status  string
	Content string
	// Truncated is the backend's own record that the captured output was cut
	// while the check ran. DisplayTruncated is this screen's separate bound.
	Truncated        bool
	DisplayTruncated bool
	ExpiresAt        time.Time
	// Error is the backend's untranslated reason a log could not be read.
	Error string
}

// RunnerCredentialsPage renders GET /repositories/{id}/runner-tokens.
//
// Issuing and revoking are security changes, so each form collects the current
// administrator password. A remembered administrator session reads the list
// and nothing more.
type RunnerCredentialsPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// SelfURL is this screen's own GET address, which deliberately never
	// carries a token. SubmitURL is the POST target for both forms.
	SelfURL   string
	SubmitURL string
	// ConfiguredChecksURL opens the execution policy screen.
	ConfiguredChecksURL string

	// Credentials are this repository's tokens, including revoked ones, so a
	// revocation stays visible as a record.
	Credentials []RunnerCredentialRow

	// CreationID is the identity this page's issue form carries. Repeating the
	// same submission returns the same credential instead of minting another
	// token.
	CreationID string

	// Issued carries the credential just created and IssuedToken its value.
	// The value exists on this one response and nowhere else: OwnGit keeps
	// only a verifier, it is never placed in a URL, a link, a log, or browser
	// storage, and returning to this screen shows the credential without it.
	Issued      RunnerCredentialRow
	IssuedToken string

	// PolicyMissing is true when no execution policy is saved yet, which is
	// what runner authority is scoped to.
	PolicyMissing bool

	// Commands are the example runner commands the backend built. They carry
	// no token value and no password.
	Commands []string

	PendingAction       string
	PendingCredentialID string
	// PendingLabel is the label the operator typed when an attempt was
	// refused, so they do not have to type it again. It is a name they chose
	// and carries no token value.
	PendingLabel string
}

func (RunnerCredentialsPage) page() string { return "runner-credentials" }

// HasIssuedToken reports whether this response is the one-time handover.
func (p RunnerCredentialsPage) HasIssuedToken() bool { return p.IssuedToken != "" }

// RunnerCredentialRow is one runner token's record. The token value is never
// part of it.
type RunnerCredentialRow struct {
	ID      string
	ShortID string
	Label   string
	// Generation is the server-issued authority generation.
	Generation int64
	CreatedAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
	Revoked    bool
}

// Revocable reports whether this row offers revocation.
func (r RunnerCredentialRow) Revocable() bool { return r.ID != "" && !r.Revoked }

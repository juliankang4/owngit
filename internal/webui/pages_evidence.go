package webui

import "time"

// Pull request, task, and helper credential screens.
//
// These pages carry the same kind of value as the rest of the package: data
// the backend already authorized, URLs it already built, and states it already
// observed. Nothing here reads storage or decides what may happen.

// Pull request list

// PullRequestsPage renders GET /repositories/{id}/pull-requests.
type PullRequestsPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// Items are the pull requests, already ordered by the backend.
	Items []PullRequestRow
	// NewURL opens the creation screen. Empty when the backend does not offer
	// it, for example on an empty repository with no branches to compare.
	NewURL string
	// Unavailable is true when pull request records could not be read. The
	// screen says so instead of showing an empty list that reads as "none".
	Unavailable bool
	// UnavailableReason explains why. Used only when Unavailable is true.
	UnavailableReason MessageCode
}

func (PullRequestsPage) page() string { return "pull-requests" }

// PullRequestRow is one entry in the list.
type PullRequestRow struct {
	Number int64
	Title  string
	URL    string
	// State is PullRequestOpen, PullRequestMerged, or PullRequestCreating.
	State  string
	Source RevisionState
	Target RevisionState
	// Checks and Review are the evidence bound to the current revision. They
	// are advisory: neither one holds a merge.
	Checks    CheckEvidence
	Review    ReviewEvidence
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Pull request creation

// NewPullRequestPage renders GET /repositories/{id}/pull-requests/new.
//
// Creating is two steps. Branches are chosen by GET, the backend answers with
// the tips it observed for that pair, and only then is there a create form
// submitting those ids. A one-step picker would submit the initial pair's ids
// beside a different branch. The backend revalidates inside its repository
// lock, so a tip that moved fails rather than being acted on.
type NewPullRequestPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// SelectURL receives the branch choice as a GET with "source" and
	// "target". It is the address of this screen, so it is followable and a
	// language switch keeps the choice.
	SelectURL string
	// SubmitURL receives the create request: csrf, title, source_branch,
	// target_branch, review, source_oid, target_oid.
	SubmitURL string
	// CancelURL returns to the pull request list.
	CancelURL string

	// Branches are the existing branches offered in both pickers.
	Branches []RefOption
	// Source and Target are the tips the backend observed for the submitted
	// pair. They are filled only once Observed is true.
	Source RevisionState
	Target RevisionState
	// Observed is true once the backend has resolved the chosen pair. Until
	// then the screen is a selection and there is no create form at all.
	Observed bool
	// Title redisplays the typed title.
	Title string
	// ReviewChoice is the "review" field: "request", "skip", or empty for
	// leaving review unrequested. An omitted choice is a real option, not a
	// hidden waiting state.
	ReviewChoice string
	// Changes are the differences between the two observed tips. The list is
	// informational; the backend recomputes the merge.
	Changes []DiffFile
	// DiffTruncated is true when a file's text diff was too large to show in
	// full. The changed paths are still listed completely.
	DiffTruncated bool
	// ChangesUnavailable is true when the comparison could not be produced.
	ChangesUnavailable bool
	// ChangesReason explains an unavailable comparison.
	ChangesReason MessageCode
}

// Comparable reports whether both observed tips are commits, which is what
// creating a pull request needs.
func (p NewPullRequestPage) Comparable() bool {
	return p.Observed && p.Source.Resolved() && p.Target.Resolved()
}

func (NewPullRequestPage) page() string { return "new-pull-request" }

// Review choices submitted in the "review" field of the create form.
const (
	// ReviewChoiceRequest records a review request for the created revision.
	ReviewChoiceRequest = "request"
	// ReviewChoiceSkip records an explicit skip.
	ReviewChoiceSkip = "skip"
	// ReviewChoiceNone leaves review unrequested. It is submitted as an empty
	// value and must not become a waiting state later.
	ReviewChoiceNone = ""
)

// Pull request detail

// PullRequestPage renders GET /repositories/{id}/pull-requests/{number}.
type PullRequestPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	Number int64
	Title  string
	// State is PullRequestOpen, PullRequestMerged, PullRequestClosed, or
	// PullRequestCreating.
	State  string
	Source RevisionState
	Target RevisionState

	// Checks and Review are advisory evidence bound to the current revision.
	Checks CheckEvidence
	Review ReviewEvidence
	// Merge is the backend's decision. Its blockers are Git and state
	// reasons only; a failed or pending check never appears among them.
	Merge MergeAvailability
	// Merged describes a completed merge. Nil while the request is open.
	Merged *MergeRecord

	// Changes are the differences between the two current tips.
	Changes []DiffFile
	// DiffTruncated is true when a file's text diff was too large to show in
	// full. The changed paths are still listed completely.
	DiffTruncated bool
	// ChangesUnavailable is true when the comparison could not be produced.
	ChangesUnavailable bool
	// ChangesReason explains an unavailable comparison.
	ChangesReason MessageCode

	// SelfURL is this screen's own GET address. A refused review or merge is
	// rendered on a POST-only route, so a language link built from the request
	// URL would be a GET at an address that refuses GET. This is where the
	// same pull request actually lives.
	SelfURL string
	// ListURL returns to the pull request list. ReviewRequestURL,
	// ReviewSkipURL, and MergeURL receive their POSTs. An empty action URL
	// renders no control, which is what a viewer without that action gets.
	ListURL          string
	ReviewRequestURL string
	ReviewSkipURL    string
	MergeURL         string
	// CloseURL closes an open pull request and ReopenURL reopens a closed
	// one. Each is set only in the state where it applies.
	CloseURL  string
	ReopenURL string
	// TasksURL opens the repository's task and check history.
	TasksURL string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (PullRequestPage) page() string { return "pull-request" }

// Tasks and check evidence

// TasksPage renders GET /repositories/{id}/tasks and, with a task selected,
// that task's attempt history.
//
// Reading task results is ordinary repository access. The page carries no
// helper credential, no bearer token, and no server secret: the backend reads
// the records and hands over what it already decided this viewer may see.
type TasksPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// Tasks are the repository's tasks, newest activity first.
	Tasks []TaskSummary
	// Detail is the opened task. Nil on the list-only view.
	Detail *TaskDetail
	// Configuration is the latest recorded check configuration.
	Configuration CheckConfigurationView
	// ListURL is this screen without a selected task.
	ListURL string
	// HelperURL opens helper credential management, and ConfiguredChecksURL
	// the repository's configured-check execution policy. Both are
	// administrator screens offered to every viewer; the page marks them
	// with a lock for a viewer without an administrator session.
	HelperURL           string
	ConfiguredChecksURL string
	// NotFound is true when a requested task id does not resolve.
	NotFound bool
	// Unavailable is true when task records could not be read.
	Unavailable bool
	// UnavailableReason explains why. Used only when Unavailable is true.
	UnavailableReason MessageCode
}

func (TasksPage) page() string { return "tasks" }

// TaskDetail is one opened task with its recorded attempts.
type TaskDetail struct {
	Task TaskSummary
	// Attempts are the recorded runs, newest first. Each one keeps the
	// revision it actually tested, so a later attempt never turns an older
	// revision into a pass.
	Attempts []AttemptRecord
	// AttemptsTruncated is true when only the most recent attempts were
	// loaded.
	AttemptsTruncated bool
}

// Helper credentials

// Helper credential actions submitted in the "action" field. These are the
// exact values the forms send, so a handler can compare against them.
const (
	// ActionIssueHelperCredential creates a scoped credential.
	// Fields: csrf, action, admin_password, label.
	ActionIssueHelperCredential = "issue"
	// ActionRevokeHelperCredential revokes one.
	// Fields: csrf, action, admin_password, credential_id.
	ActionRevokeHelperCredential = "revoke"
)

// HelperCredentialsPage renders GET /repositories/{id}/helper-credentials.
//
// Issuing and revoking are security changes, so each one collects the current
// administrator password in its own form. A remembered administrator session
// is enough to read the list and nothing more.
type HelperCredentialsPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs

	// SelfURL is this screen's own GET address, used for the same reason as
	// the pull request detail's: the response that shows a new token is a POST
	// result, and its language link has to lead somewhere followable. That GET
	// deliberately does not carry the token.
	SelfURL string
	// SubmitURL is the POST target for both forms.
	SubmitURL string
	// Credentials are the repository's credentials, including revoked ones,
	// so a revocation stays visible as a record.
	Credentials []HelperCredentialRow
	// PendingAction is the action whose form carries a validation failure. It
	// is one of the two action constants above.
	PendingAction string
	// PendingCredentialID scopes a failed revocation to one row, since every
	// row submits the same action. Empty means the notices belong to the form
	// named by PendingAction alone.
	PendingCredentialID string

	// Issued carries the credential just created, and IssuedToken its secret.
	//
	// The token exists on this one response and nowhere else: OwnGit keeps
	// only its hash, the value is never placed in a URL, a link, or browser
	// storage, and returning to this screen shows the credential without it.
	Issued      HelperCredentialRow
	IssuedToken string
}

func (HelperCredentialsPage) page() string { return "helper-credentials" }

// HasIssuedToken reports whether this response is the one-time handover.
func (p HelperCredentialsPage) HasIssuedToken() bool { return p.IssuedToken != "" }

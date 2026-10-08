package actions

// JobPlan keeps workflow text and admission values, never resolved secret values.
// Step expressions are evaluated at step start. Job expressions with needs are
// evaluated when dependencies finish.
type JobPlan struct {
	JobKey           string            `json:"job_key"`
	MatrixIndex      int               `json:"matrix_index"`
	Name             string            `json:"name,omitempty"`
	RunsOn           []string          `json:"runs_on,omitempty"`
	Needs            []string          `json:"needs,omitempty"`
	If               string            `json:"if,omitempty"`
	ContinueOnError  string            `json:"continue_on_error,omitempty"`
	TimeoutMinutes   string            `json:"timeout_minutes,omitempty"`
	Concurrency      Concurrency       `json:"concurrency"`
	WorkflowEnv      map[string]string `json:"workflow_env,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	WorkflowDefaults RunDefaults       `json:"workflow_defaults"`
	Defaults         RunDefaults       `json:"defaults"`
	Context          PlanContext       `json:"context"`
	Steps            []Step            `json:"steps"`
	SecretNames      []string          `json:"secret_names,omitempty"`
	Notes            []Message         `json:"notes,omitempty"`
}

// Step keeps stable, unevaluated display identities for result matching.
// Scalar expression fields include literal booleans and numbers as text.
type Step struct {
	ID               string            `json:"id,omitempty"`
	Name             string            `json:"name,omitempty"`
	If               string            `json:"if,omitempty"`
	Run              string            `json:"run,omitempty"`
	Uses             string            `json:"uses,omitempty"`
	With             map[string]string `json:"with,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	ContinueOnError  string            `json:"continue_on_error,omitempty"`
	TimeoutMinutes   string            `json:"timeout_minutes,omitempty"`
}

type RunDefaults struct {
	Shell            string `json:"shell,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type Concurrency struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress string `json:"cancel_in_progress,omitempty"`
	Queue            string `json:"queue,omitempty"`
}

// PlanContext contains only admission contexts. Secrets, env, steps, job and
// runner contexts are supplied during execution, not saved in the plan.
type PlanContext struct {
	GitHub   GitHubContext     `json:"github"`
	Inputs   map[string]any    `json:"inputs,omitempty"`
	Matrix   map[string]any    `json:"matrix,omitempty"`
	Strategy StrategyContext   `json:"strategy"`
	Needs    map[string]string `json:"needs,omitempty"`
}

type GitHubContext struct {
	SHA             string      `json:"sha"`
	Ref             string      `json:"ref"`
	RefName         string      `json:"ref_name"`
	RefType         string      `json:"ref_type"`
	HeadRef         string      `json:"head_ref,omitempty"`
	BaseRef         string      `json:"base_ref,omitempty"`
	EventName       string      `json:"event_name"`
	Event           GitHubEvent `json:"event"`
	Repository      string      `json:"repository"`
	Actor           string      `json:"actor"`
	TriggeringActor string      `json:"triggering_actor"`
	RunID           string      `json:"run_id"`
	RunNumber       int64       `json:"run_number"`
	RunAttempt      int64       `json:"run_attempt"`
	Workflow        string      `json:"workflow"`
	Job             string      `json:"job"`
}

type GitHubEvent struct {
	Action      string              `json:"action,omitempty"`
	Inputs      map[string]any      `json:"inputs,omitempty"`
	Number      int64               `json:"number,omitempty"`
	PullRequest *PullRequestContext `json:"pull_request,omitempty"`
}

type PullRequestContext struct {
	Number int64      `json:"number"`
	Head   RefContext `json:"head"`
	Base   RefContext `json:"base"`
}

type RefContext struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type StrategyContext struct {
	JobIndex    int  `json:"job-index"`
	JobTotal    int  `json:"job-total"`
	FailFast    bool `json:"fail-fast"`
	MaxParallel int  `json:"max-parallel"`
}

type Message struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RunFacts remain portable after job plans are deleted.
type RunFacts struct {
	WorkflowName   string       `json:"workflow_name,omitempty"`
	WorkflowOID    string       `json:"workflow_oid,omitempty"`
	WorkflowDigest string       `json:"workflow_digest,omitempty"`
	RefusedJobs    []RefusedJob `json:"refused_jobs,omitempty"`
	Notes          []Message    `json:"notes,omitempty"`
	SecretNames    []string     `json:"secret_names,omitempty"`
}

type RefusedJob struct {
	JobKey      string  `json:"job_key"`
	MatrixIndex int     `json:"matrix_index"`
	Reason      Message `json:"reason"`
}

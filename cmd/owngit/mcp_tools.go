package main

import (
	"context"
	"encoding/json"

	"owngit/internal/apiclient"
	"owngit/internal/checkexec"
	"owngit/internal/pullrequest"
)

// This file lists the tools of owngit mcp. Each tool wraps one shared
// operation and returns the JSON the matching owngit command prints.
// Descriptions are written for coding agents: they state the side effects
// and which text in a result is untrusted.

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema toolInputSchema `json:"inputSchema"`
	Annotations toolAnnotations `json:"annotations"`
	// call runs the tool; see callTool for its result convention.
	call func(ctx context.Context, arguments json.RawMessage) ([]byte, error)
	// unbounded tools run without mcpCallTimeout and bound their own steps.
	unbounded bool
}

type toolInputSchema struct {
	Type                 string                    `json:"type"`
	Properties           map[string]toolInputField `json:"properties"`
	Required             []string                  `json:"required,omitempty"`
	AdditionalProperties bool                      `json:"additionalProperties"`
}

type toolInputField struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
	Minimum     int      `json:"minimum,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Default     any      `json:"default,omitempty"`
}

type toolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

var (
	readOnly = toolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	// A write that can be repeated with the same result, such as closing.
	repeatableWrite = toolAnnotations{IdempotentHint: true}
)

// Input fields shared by several tools.
var (
	numberField = toolInputField{Type: "integer", Minimum: 1, Description: "Pull request number."}
	taskField   = toolInputField{Type: "string", Description: "Check task ID, from check_task_create or the user."}
	// objectIDField matches a SHA-1 or SHA-256 commit ID.
	objectIDField  = `^[0-9a-f]{40}([0-9a-f]{24})?$`
	sourceOIDField = toolInputField{Type: "string", Pattern: objectIDField, Description: "Exact source commit ID, from pull_request_show or pull_request_diff."}
	targetOIDField = toolInputField{Type: "string", Pattern: objectIDField, Description: "Exact target commit ID, from pull_request_show or pull_request_diff."}
)

// schema builds an input schema. Repository tools get a required repository
// field only when no repository was fixed at launch.
func (server *mcpServer) schema(repositoryTool bool, required []string, fields map[string]toolInputField) toolInputSchema {
	properties := map[string]toolInputField{}
	for name, field := range fields {
		properties[name] = field
	}
	if repositoryTool && server.general.repository == "" {
		properties["repository"] = toolInputField{Type: "string", Description: "Repository ID (its lowercase name), from repository_list."}
		required = append([]string{"repository"}, required...)
	}
	return toolInputSchema{Type: "object", Properties: properties, Required: required, AdditionalProperties: false}
}

func (server *mcpServer) tool(name string) *mcpTool {
	for index := range server.tools {
		if server.tools[index].Name == name {
			return &server.tools[index]
		}
	}
	return nil
}

// Tool arguments. Every repository tool accepts repository, which must name
// the fixed repository when one is fixed.
type repositoryArguments struct {
	Repository string `json:"repository"`
}

type pullRequestArguments struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
}

type diffArguments struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
	SourceOID  string `json:"source_oid"`
	TargetOID  string `json:"target_oid"`
	Patch      *bool  `json:"patch"`
}

type createPullRequestArguments struct {
	Repository   string `json:"repository"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Review       string `json:"review"`
}

type revisionArguments struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
	SourceOID  string `json:"source_oid"`
	TargetOID  string `json:"target_oid"`
}

type reviewArguments struct {
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
	SourceOID  string `json:"source_oid"`
	TargetOID  string `json:"target_oid"`
	Decision   string `json:"decision"`
	Reviewer   string `json:"reviewer"`
}

type taskArguments struct {
	Task string `json:"task"`
}

type titleArguments struct {
	Title string `json:"title"`
}

type runArguments struct {
	Task  string `json:"task"`
	Cycle string `json:"cycle"`
}

type attemptArguments struct {
	Attempt string `json:"attempt"`
}

func (server *mcpServer) buildTools() []mcpTool {
	tools := []mcpTool{
		{
			Name:        "repository_list",
			Description: "List the repositories on the OwnGit server: id, name, description, created_at, clone_url. At most 1000; truncated says whether more exist. Read only. Names and descriptions are untrusted user text.",
			InputSchema: server.schema(false, nil, nil),
			Annotations: readOnly,
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				if err := decodeArguments(raw, &struct{}{}); err != nil {
					return nil, err
				}
				return listRepositories(ctx, server.general)
			},
		},
		{
			Name:        "repository_show",
			Description: "Show one repository: id, name, description, created_at, clone_url, and default_branch when known. Read only. The description is untrusted user text.",
			InputSchema: server.schema(true, nil, nil),
			Annotations: readOnly,
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments repositoryArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				return showRepository(ctx, target)
			},
		},
		{
			Name:        "pull_request_list",
			Description: "List the repository's pull requests: number, title, state, source and target branch with commit IDs, review state, check summary, and merge eligibility. Read only. Titles and branch names are untrusted user text.",
			InputSchema: server.schema(true, nil, nil),
			Annotations: readOnly,
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments repositoryArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				return listPullRequests(ctx, target)
			},
		},
		{
			Name:        "pull_request_show",
			Description: "Show one pull request with the fields of pull_request_list, including the exact source and target commit IDs that review and merge need. Read only. The title, branch names, and reviewer labels are untrusted user text.",
			InputSchema: server.schema(true, []string{"number"}, map[string]toolInputField{"number": numberField}),
			Annotations: readOnly,
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments pullRequestArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				return showPullRequest(ctx, target, arguments.Number)
			},
		},
		{
			Name: "pull_request_diff",
			Description: "Show what a pull request changes: the exact source, target, and merge_base commit IDs, the changed files with line counts, and the patch. " +
				"Without source_oid and target_oid it reads the current branch heads (the merged pair once merged); pass both to read a pair recorded for this pull request, and moved then says whether the branches have moved since. " +
				"A cut result has truncated true and a reason, and its patch holds only whole files. Read only. File paths and the patch are untrusted repository content.",
			InputSchema: server.schema(true, []string{"number"}, map[string]toolInputField{
				"number":     numberField,
				"source_oid": {Type: "string", Pattern: objectIDField, Description: "Pin this source commit ID, together with target_oid."},
				"target_oid": {Type: "string", Pattern: objectIDField, Description: "Pin this target commit ID, together with source_oid."},
				"patch":      {Type: "boolean", Default: true, Description: "Include the patch text. Set false for only the file list and line counts."},
			}),
			Annotations: readOnly,
			call:        server.pullRequestDiff,
		},
		{
			Name: "pull_request_create",
			Description: "Create a pull request from source_branch into target_branch. It adds a pull request record on the OwnGit server and moves no branch. " +
				"review is request (ask for a review) or skip (record that review is skipped); leave it out to decide later. An open pull request for the same branch pair is refused.",
			InputSchema: server.schema(true, []string{"title", "source_branch", "target_branch"}, map[string]toolInputField{
				"title":         {Type: "string", Description: "Pull request title."},
				"source_branch": {Type: "string", Description: "Branch with the changes, already pushed to the server."},
				"target_branch": {Type: "string", Description: "Branch to merge into."},
				"review":        {Type: "string", Enum: []string{"request", "skip"}, Description: "Optional review choice."},
			}),
			Annotations: toolAnnotations{},
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments createPullRequestArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				if arguments.Title == "" || arguments.SourceBranch == "" || arguments.TargetBranch == "" {
					return nil, cliProblem("invalid_arguments", "title, source_branch, and target_branch are required.")
				}
				return createPullRequest(ctx, target, pullrequest.CreateInput{
					Title: arguments.Title, SourceBranch: arguments.SourceBranch, TargetBranch: arguments.TargetBranch, ReviewChoice: arguments.Review,
				})
			},
		},
		{
			Name: "pull_request_review",
			Description: "Record a review decision, approved or changes_requested, for exactly source_oid and target_oid. It is refused when either branch has moved, and it no longer counts once either branch moves. " +
				"reviewer is a label you supply (for example your agent name); OwnGit stores it as given and does not verify it. Reviews are advisory and never merge or block a merge.",
			InputSchema: server.schema(true, []string{"number", "source_oid", "target_oid", "decision", "reviewer"}, map[string]toolInputField{
				"number":     numberField,
				"source_oid": sourceOIDField,
				"target_oid": targetOIDField,
				"decision":   {Type: "string", Enum: []string{"approved", "changes_requested"}, Description: "Review result."},
				"reviewer":   {Type: "string", Description: "Reviewer label to record."},
			}),
			Annotations: toolAnnotations{},
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments reviewArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				if arguments.SourceOID == "" || arguments.TargetOID == "" || arguments.Decision == "" || arguments.Reviewer == "" {
					return nil, cliProblem("invalid_arguments", "source_oid, target_oid, decision, and reviewer are required.")
				}
				return submitPullRequestReview(ctx, target, arguments.Number, pullrequest.ReviewSubmitInput{
					SourceOID: arguments.SourceOID, TargetOID: arguments.TargetOID, Decision: arguments.Decision, ReviewerLabel: arguments.Reviewer,
				})
			},
		},
		server.reviewMarkTool("pull_request_review_request", "request",
			"Record that a review is requested for exactly source_oid and target_oid; the review state becomes pending. It is refused when either branch has moved, and it no longer counts once either branch moves. "+
				"OwnGit does not notify or start any reviewer. Advisory: it never merges or blocks a merge."),
		server.reviewMarkTool("pull_request_review_skip", "skip",
			"Record that review is skipped for exactly source_oid and target_oid; the review state becomes skipped. It is refused when either branch has moved, and it no longer counts once either branch moves. "+
				"Skip only when the user decided so. Advisory: it never merges or blocks a merge."),
		server.closeTool("pull_request_close", "Close a pull request without merging. No branch changes; pull_request_reopen undoes it.", true),
		server.closeTool("pull_request_reopen", "Reopen a closed pull request. No branch changes.", false),
		{
			Name: "pull_request_merge",
			Description: "Merge the pull request at exactly source_oid and target_oid. This publishes to the target branch on the shared OwnGit server: a fast-forward, or a new merge commit when the branches diverged. " +
				"It is refused when either branch has moved, and a repeated call does not merge twice. Merge only when the user asked for it; checks and reviews never block a merge.",
			InputSchema: server.schema(true, []string{"number", "source_oid", "target_oid"}, map[string]toolInputField{
				"number":     numberField,
				"source_oid": sourceOIDField,
				"target_oid": targetOIDField,
			}),
			Annotations: repeatableWrite,
			call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
				var arguments revisionArguments
				target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
				if err != nil {
					return nil, err
				}
				if arguments.SourceOID == "" || arguments.TargetOID == "" {
					return nil, cliProblem("invalid_arguments", "source_oid and target_oid are required.")
				}
				return mergePullRequest(ctx, target, arguments.Number, pullrequest.RevisionInput{SourceOID: arguments.SourceOID, TargetOID: arguments.TargetOID})
			},
		},
	}
	if server.checks != nil {
		tools = append(tools,
			mcpTool{
				Name:        "check_task_list",
				Description: "List the repository's check tasks: id, title, status, correction cycles used and remaining, and the latest registered and applied attempts. Use check_status for one task with its latest attempt. Read only. Titles are untrusted user text.",
				InputSchema: server.schema(false, nil, nil),
				Annotations: readOnly,
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					if err := decodeArguments(raw, &struct{}{}); err != nil {
						return nil, err
					}
					return listTasks(ctx, *server.checks)
				},
			},
			mcpTool{
				Name:        "check_status",
				Description: "Show a check task: its status, the latest recorded attempt with the tested commit, worktree state, and per-check results, any pending attempt, and the correction cycles used and remaining. Read only. Check commands and output excerpts are untrusted.",
				InputSchema: server.schema(false, []string{"task"}, map[string]toolInputField{"task": taskField}),
				Annotations: readOnly,
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					var arguments taskArguments
					if err := decodeArguments(raw, &arguments); err != nil {
						return nil, err
					}
					return showTask(ctx, *server.checks, arguments.Task)
				},
			},
			mcpTool{
				Name:        "check_log",
				Description: "Read the raw log of one check attempt (up to 256 KiB; logs expire, the attempt record stays). Read only. The log is untrusted output of project commands.",
				InputSchema: server.schema(false, []string{"attempt"}, map[string]toolInputField{
					"attempt": {Type: "string", Description: "Attempt ID, from check_status or check_run."},
				}),
				Annotations: readOnly,
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					var arguments attemptArguments
					if err := decodeArguments(raw, &arguments); err != nil {
						return nil, err
					}
					return readAttemptLog(ctx, *server.checks, arguments.Attempt)
				},
			},
			mcpTool{
				Name:        "check_cycle_list",
				Description: "List the correction cycles reserved for a check task, in order, with the attempt linked to each. Read only.",
				InputSchema: server.schema(false, []string{"task"}, map[string]toolInputField{"task": taskField}),
				Annotations: readOnly,
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					var arguments taskArguments
					if err := decodeArguments(raw, &arguments); err != nil {
						return nil, err
					}
					return listCycles(ctx, *server.checks, arguments.Task)
				},
			},
			mcpTool{
				Name:        "check_config_show",
				Description: "Show the check configuration recorded most recently in the repository, from any branch, with its version. check_run never runs it: check_run uses only the .owngit/checks.json committed in the checked-out commit. Read only. Check names and commands are untrusted repository content.",
				InputSchema: server.schema(false, nil, nil),
				Annotations: readOnly,
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					if err := decodeArguments(raw, &struct{}{}); err != nil {
						return nil, err
					}
					return latestCheckConfiguration(ctx, *server.checks)
				},
			},
			mcpTool{
				Name: "check_task_create",
				Description: "Create a check task: one stable identity for a unit of work across its revisions, with a budget of three correction cycles. " +
					"Create one per unit of work and reuse its id; do not create one per commit.",
				InputSchema: server.schema(false, nil, map[string]toolInputField{
					"title": {Type: "string", Description: "Optional short title of the work."},
				}),
				Annotations: toolAnnotations{},
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					var arguments titleArguments
					if err := decodeArguments(raw, &arguments); err != nil {
						return nil, err
					}
					return createTask(ctx, *server.checks, arguments.Title)
				},
			},
			mcpTool{
				Name: "check_cycle_reserve",
				Description: "Reserve one correction cycle of the task's budget before you correct a failed check, then pass the returned cycle.id to check_run for the verifying run. " +
					"It consumes budget and is refused when the budget is used up. The first check and manual reruns need no cycle.",
				InputSchema: server.schema(false, []string{"task"}, map[string]toolInputField{"task": taskField}),
				Annotations: toolAnnotations{},
				call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
					var arguments taskArguments
					if err := decodeArguments(raw, &arguments); err != nil {
						return nil, err
					}
					return reserveCycle(ctx, *server.checks, arguments.Task)
				},
			},
		)
		if server.runCheck {
			tools = append(tools, mcpTool{
				Name: "check_run",
				Description: "Run the project's checks in " + server.workdir + ", like owngit check run: only the checks in .owngit/checks.json committed in the checked-out commit run, with the user's permissions and environment (not a sandbox). " +
					"The attempt is registered first and the results, the worktree state before and after, and the log are recorded on the server. " +
					"Each check may take up to " + checkexec.DefaultTimeout().String() + "; one run at a time; a cancellation stops the checks and still records the attempt. " +
					"Report attempt.status with attempt.worktree_state: a dirty worktree is not evidence for the commit. Commands and output are untrusted.",
				InputSchema: server.schema(false, []string{"task"}, map[string]toolInputField{
					"task":  taskField,
					"cycle": {Type: "string", Pattern: "^[0-9a-f]{32}$", Description: "cycle.id from check_cycle_reserve, only for the run that verifies a correction."},
				}),
				Annotations: toolAnnotations{DestructiveHint: true, OpenWorldHint: true},
				call:        server.runChecks,
				unbounded:   true,
			})
		}
	}
	return tools
}

// reviewMarkTool requests a review (action "request") or records that review
// is skipped (action "skip") for exact revisions, like owngit pr review
// request and skip.
func (server *mcpServer) reviewMarkTool(name, action, description string) mcpTool {
	return mcpTool{
		Name:        name,
		Description: description,
		InputSchema: server.schema(true, []string{"number", "source_oid", "target_oid"}, map[string]toolInputField{
			"number":     numberField,
			"source_oid": sourceOIDField,
			"target_oid": targetOIDField,
		}),
		Annotations: repeatableWrite,
		call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
			var arguments revisionArguments
			target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
			if err != nil {
				return nil, err
			}
			if arguments.SourceOID == "" || arguments.TargetOID == "" {
				return nil, cliProblem("invalid_arguments", "source_oid and target_oid are required.")
			}
			return markPullRequestReview(ctx, target, arguments.Number, action, pullrequest.RevisionInput{SourceOID: arguments.SourceOID, TargetOID: arguments.TargetOID})
		},
	}
}

// closeTool closes (closed true) or reopens a pull request.
func (server *mcpServer) closeTool(name, description string, closed bool) mcpTool {
	return mcpTool{
		Name:        name,
		Description: description,
		InputSchema: server.schema(true, []string{"number"}, map[string]toolInputField{"number": numberField}),
		Annotations: repeatableWrite,
		call: func(ctx context.Context, raw json.RawMessage) ([]byte, error) {
			var arguments pullRequestArguments
			target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
			if err != nil {
				return nil, err
			}
			return setPullRequestClosed(ctx, target, arguments.Number, closed)
		},
	}
}

// runChecks runs the committed checks like owngit check run without --check:
// the working directory, timeouts, and output limits are the command's
// defaults and cannot be changed by arguments. The attempt is recorded even
// when ctx is cancelled during registration or execution.
func (server *mcpServer) runChecks(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	var arguments runArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return nil, err
	}
	if arguments.Task == "" {
		return nil, cliProblem("invalid_arguments", "task is required. Create one with check_task_create.")
	}
	if arguments.Cycle != "" && !validHexID(arguments.Cycle) {
		return nil, cliProblem("invalid_arguments", "cycle must be the 32 character cycle.id from check_cycle_reserve.")
	}
	if !server.checkRunning.CompareAndSwap(false, true) {
		return nil, cliProblem("check_run_busy", "Another check run of this MCP server is in progress. Wait for its result.")
	}
	defer server.checkRunning.Store(false)
	attempt, err := prepareCheckAttempt(ctx, server.checks, checkRunRequest{
		TaskID: arguments.Task, CycleID: arguments.Cycle, Workdir: server.workdir,
		Timeout: checkexec.DefaultTimeout(), OutputLimit: checkexec.DefaultOutputLimit(),
	})
	if err != nil {
		return nil, err
	}
	// Registration and completion outlive a cancellation, as in the command
	// line: once the server may have recorded the attempt, the run must reach
	// its completion, or the attempt would stay pending.
	registerCtx, cancelRegister := context.WithTimeout(context.WithoutCancel(ctx), mcpCallTimeout)
	err = attempt.register(registerCtx)
	cancelRegister()
	if err != nil {
		return nil, err
	}
	attempt.execute(ctx)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpCallTimeout)
	defer cancel()
	content, err := json.Marshal(attempt.complete(recordCtx))
	if err != nil {
		return nil, &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be encoded.", Cause: err}
	}
	if !attempt.output.OK {
		return content, cliProblem("check_attempt_not_recorded", "The check attempt was not recorded; upload_error says why.")
	}
	return content, nil
}

// decodeRepositoryArguments decodes a repository tool's arguments into
// arguments and returns the connection for the repository they name.
func (server *mcpServer) decodeRepositoryArguments(raw json.RawMessage, arguments any, repository *string) (connection, error) {
	if err := decodeArguments(raw, arguments); err != nil {
		return connection{}, err
	}
	return server.repositoryTarget(*repository)
}

// pullRequestDiff reads a diff like owngit pr diff, or like --stat when patch
// is false. A result over the limit keeps whole files of the patch and says
// that it was cut, as the API does for its own response limit.
func (server *mcpServer) pullRequestDiff(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	var arguments diffArguments
	target, err := server.decodeRepositoryArguments(raw, &arguments, &arguments.Repository)
	if err != nil {
		return nil, err
	}
	content, err := pullRequestDiff(ctx, target, arguments.Number, pullrequest.RevisionInput{SourceOID: arguments.SourceOID, TargetOID: arguments.TargetOID})
	switch {
	case err != nil:
		return nil, err
	case arguments.Patch != nil && !*arguments.Patch:
		return diffStat(content)
	case len(content) <= server.resultLimit:
		return content, nil
	}
	var diff pullrequest.Diff
	if err := json.Unmarshal(content, &diff); err != nil {
		return nil, &apiclient.Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid diff.", Cause: err}
	}
	diff.Fit(server.resultLimit)
	return json.Marshal(diff)
}

package checkrun

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkexec"
	"owngit/internal/state"
)

// ActionsContainer is prepared before the job's one-shot start grant. RunScript
// uses the same runtime ownership record and cleanup path as JSON checks.
type ActionsContainer struct {
	Environment  []string
	Architecture string
	Description  string
	coordinator  *Coordinator
	job          state.CheckJob
	prepared     preparedContainer
	workspace    string
}

func (coordinator *Coordinator) PrepareActionsContainer(ctx context.Context, job state.CheckJob, workspace string) (*ActionsContainer, error) {
	if coordinator.Store == nil || job.Executor != state.CheckExecutorContainer {
		return nil, fmt.Errorf("workflow.executor: a leased container job and store are required")
	}
	if workspace == "" {
		return nil, fmt.Errorf("workflow.workspace: workspace is required")
	}
	workspace, err := filepath.Abs(workspace)
	if err == nil {
		workspace, err = filepath.EvalSymlinks(workspace)
	}
	if err != nil {
		return nil, fmt.Errorf("workflow.workspace: %w", err)
	}
	prepared, err := coordinator.containerPreflight(ctx, job, workspace)
	if err != nil {
		return nil, err
	}
	for _, volume := range prepared.volumes {
		if pathsOverlap(volume, "/owngit") {
			return nil, fmt.Errorf("configured check image declares volume %q, which overlaps the workflow folders", volume)
		}
	}
	output, err := runDockerControl(ctx, prepared.docker, prepared.dockerHost, "image", "inspect", "--format",
		`{"env":{{json .Config.Env}},"arch":{{json .Architecture}}}`, prepared.imageID)
	if err != nil {
		return nil, fmt.Errorf("inspect workflow image environment: %w", err)
	}
	var image struct {
		Environment  []string `json:"env"`
		Architecture string   `json:"arch"`
	}
	if err := json.Unmarshal([]byte(output), &image); err != nil {
		return nil, fmt.Errorf("decode workflow image environment: %w", err)
	}
	arch := map[string]string{"amd64": "X64", "arm64": "ARM64", "arm": "ARM"}[image.Architecture]
	if arch == "" {
		return nil, fmt.Errorf("workflow.executor: unsupported image architecture %q", image.Architecture)
	}
	if image.Environment == nil {
		image.Environment = []string{}
	}
	return &ActionsContainer{Environment: image.Environment, Architecture: arch, Description: prepared.header(job.Execution.ContainerImage),
		coordinator: coordinator, job: job, prepared: prepared, workspace: workspace}, nil
}

func (container *ActionsContainer) Limits() (time.Duration, int64) {
	return time.Duration(container.job.Limits.TimeoutMS) * time.Millisecond, container.job.Limits.OutputLimitBytes
}

// RunScript accepts a trusted launcher command, not workflow environment values.
// The adapter writes those values to the mounted private environment file.
func (container *ActionsContainer) RunScript(ctx context.Context, script actions.Script, command ...string) actions.ScriptResult {
	return container.runScript(ctx, script, false, command...)
}

func (container *ActionsContainer) CheckInterpreter(ctx context.Context, script actions.Script, command ...string) actions.ScriptResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	script.Output, script.Timeout, script.OutputLimit = nil, 10*time.Second, 0
	return container.runScript(ctx, script, true, command...)
}

func (container *ActionsContainer) runScript(ctx context.Context, script actions.Script, lookup bool, command ...string) actions.ScriptResult {
	fail := func(err error) actions.ScriptResult {
		return actions.ScriptResult{Status: actions.StatusError, Output: err.Error()}
	}
	if ctx.Err() != nil {
		return actions.ScriptResult{Status: actions.StatusCancelled}
	}
	if script.Workspace != container.workspace || script.ActionsRoot == nil {
		return fail(fmt.Errorf("workflow.workspace: script does not belong to the prepared workspace"))
	}
	stored, found, err := container.coordinator.Store.CheckJob(ctx, container.job.RepositoryID, container.job.ID)
	if err != nil {
		return fail(err)
	}
	if !found || stored.Status != state.CheckJobStarted {
		return fail(fmt.Errorf("workflow.executor: a job start grant is required"))
	}
	step, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(script.Path), "step-"), ".script"))
	if err != nil || step < 1 || filepath.Base(script.Path) != fmt.Sprintf("step-%d.script", step) {
		return fail(fmt.Errorf("workflow.workspace: invalid step script path"))
	}
	root := script.ActionsRoot
	mounts := []containerMount{
		{source: script.ScriptsDirectory, target: "/owngit/scripts", readOnly: true},
		{source: script.WorkDirectory, target: "/owngit/work"},
		{source: script.FilesDirectory, target: "/owngit/files"},
	}
	folders := []string{"scripts", "work", filepath.Join("files", fmt.Sprintf("step-%d", step))}
	if lookup {
		directory := fmt.Sprintf("step-%d.lookup", step)
		mounts[0].source = filepath.Join(script.ScriptsDirectory, directory)
		folders[0] = filepath.Join("scripts", directory)
		mounts, folders = mounts[:2], folders[:2]
	}
	for index, relative := range folders {
		if mounts[index].source != filepath.Join(root.Name(), relative) {
			return fail(fmt.Errorf("workflow.workspace: invalid mounted workflow folder"))
		}
		info, err := root.Lstat(relative)
		if err != nil || !info.IsDir() {
			return fail(fmt.Errorf("workflow.workspace: mounted workflow folder is not a directory"))
		}
		if _, err := containerUserForWorkspace(mounts[index].source); err != nil {
			return fail(err)
		}
	}
	relative, err := filepath.Rel(container.workspace, script.Directory)
	if err != nil || !filepath.IsLocal(relative) {
		return fail(fmt.Errorf("workflow.workspace: step directory is outside the workspace"))
	}
	prepared := container.prepared
	prepared.mounts = mounts
	job := container.job
	timeout, outputLimit := container.Limits()
	if script.Timeout > 0 {
		timeout = min(timeout, script.Timeout)
	}
	if script.OutputLimit > 0 {
		outputLimit = min(outputLimit, script.OutputLimit)
	}
	job.Limits.TimeoutMS, job.Limits.OutputLimitBytes = max(int64(1), timeout.Milliseconds()), outputLimit
	name := fmt.Sprintf("owngit-check-%s-step-%d", job.ID, step)
	if lookup {
		name += "-lookup"
	}
	arguments, err := container.coordinator.containerCreateArguments(job, prepared, container.workspace, name, strconv.Itoa(step), command...)
	if err != nil {
		return actions.ScriptResult{Status: actions.StatusUnavailable, Output: err.Error()}
	}
	result, _, stopped := container.coordinator.runContainerStep(ctx, job, prepared, name, checkexec.Definition{Name: "workflow-step"}, arguments, script.Output)
	if stopped && result.CleanupError == "" {
		result.CleanupError = "Container execution stopped because its ownership could not be established."
	}
	return actions.ScriptResult{Status: result.Status, ExitCode: result.ExitCode, Duration: result.Duration,
		Output: result.Output, OutputGap: result.OutputGap, Truncated: result.Truncated, CleanupError: result.CleanupError}
}

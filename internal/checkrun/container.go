package checkrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

const dockerProbeOutputLimit = 64 << 10

func (coordinator *Coordinator) containerPreflight(ctx context.Context, job state.CheckJob, workspace string) error {
	docker, dockerHost, daemonID, err := coordinator.containerRuntimeIdentity(ctx, true)
	if err != nil {
		return err
	}
	image, err := runDockerControl(ctx, docker, dockerHost, "image", "inspect", "--format",
		`{"id":{{json .Id}},"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"volumes":{{json (index .Config "Volumes")}}}`, job.Execution.ContainerImage)
	if err != nil {
		return fmt.Errorf("inspect cached immutable image (automatic pulls are disabled): %w", err)
	}
	if err := verifyContainerImageIdentity(job.Execution.ContainerImage, image); err != nil {
		return err
	}

	probeName := "owngit-check-probe-" + job.ID
	arguments, err := coordinator.containerCreateArguments(job, workspace, probeName, "probe", "/bin/true")
	if err != nil {
		return err
	}
	authority := checkJobAuthority(job)
	if err := coordinator.Store.PlanCheckContainer(ctx, authority, probeName, daemonID, time.Now().UTC()); err != nil {
		return fmt.Errorf("record restricted validation container plan: %w", err)
	}
	created, err := runDockerControl(ctx, docker, dockerHost, arguments...)
	if err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, probeName, "", daemonID)
		return errors.Join(fmt.Errorf("validate restricted container creation: %w", err), cleanupErr)
	}
	containerID := strings.TrimSpace(created)
	if !validContainerID(containerID) {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, probeName, "", daemonID)
		return errors.Join(errors.New("Docker returned an invalid probe container identity"), cleanupErr)
	}
	if err := coordinator.confirmContainerRuntime(ctx, docker, dockerHost, daemonID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		return errors.Join(err, cleanupErr)
	}
	if err := coordinator.Store.ConfirmCheckContainer(ctx, job.ID, probeName, containerID, daemonID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		return errors.Join(fmt.Errorf("confirm restricted validation container ownership: %w", err), cleanupErr)
	}
	_, startErr := runDockerControl(ctx, docker, dockerHost, "start", "--attach", containerID)
	cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, containerID, daemonID)
	if startErr != nil {
		return errors.Join(fmt.Errorf("start restricted validation container: %w", startErr), cleanupErr)
	}
	if cleanupErr != nil {
		return fmt.Errorf("remove restricted validation container: %w", cleanupErr)
	}
	return nil
}

func (coordinator *Coordinator) runContainerChecks(ctx context.Context, job state.CheckJob, workspace string, definitions []checkexec.Definition) ([]checkexec.Result, bool) {
	docker, dockerHost, daemonID, err := coordinator.containerRuntimeIdentity(ctx, true)
	if err != nil {
		return unavailableResults(definitions, err), false
	}
	authority := checkJobAuthority(job)
	results := make([]checkexec.Result, 0, len(definitions))
	cancelled := false
	for index, definition := range definitions {
		if ctx.Err() != nil {
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusCancelled})
			cancelled = true
			continue
		}
		name := fmt.Sprintf("owngit-check-%s-%d", job.ID, index)
		arguments, argumentsErr := coordinator.containerCreateArguments(job, workspace, name, strconv.Itoa(index), "/bin/sh", "-c", definition.Command)
		if argumentsErr != nil {
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusUnavailable, Output: argumentsErr.Error()})
			continue
		}
		if err := coordinator.Store.PlanCheckContainer(ctx, authority, name, daemonID, time.Now().UTC()); err != nil {
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusError, Output: boundedSummary("record container plan: " + err.Error())})
			break
		}
		created, createErr := runDockerControl(ctx, docker, dockerHost, arguments...)
		if createErr != nil {
			status := checkexec.StatusError
			if strings.Contains(createErr.Error(), "No such image") || strings.Contains(createErr.Error(), "not found") {
				status = checkexec.StatusUnavailable
			}
			cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, name, "", daemonID)
			output, truncated := clipContainerOutput(errors.Join(createErr, cleanupErr).Error(), job.Limits.OutputLimitBytes)
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: status, Output: output, Truncated: truncated})
			if cleanupErr != nil {
				break
			}
			continue
		}
		containerID := strings.TrimSpace(created)
		if !validContainerID(containerID) {
			cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, name, "", daemonID)
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusError, Output: "Docker returned an invalid container identity.", CleanupError: boundedError(cleanupErr)})
			break
		}
		if err := coordinator.confirmContainerRuntime(ctx, docker, dockerHost, daemonID); err != nil {
			cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusError,
				CleanupError: boundedSummary(errors.Join(err, cleanupErr).Error())})
			break
		}
		if err := coordinator.Store.ConfirmCheckContainer(ctx, job.ID, name, containerID, daemonID); err != nil {
			cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
			results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusError,
				CleanupError: boundedSummary(errors.Join(fmt.Errorf("confirm container ownership: %w", err), cleanupErr).Error())})
			break
		}
		direct := checkexec.Definition{
			Name: definition.Name, Command: definition.Command, Executable: docker,
			Arguments: []string{"--host", dockerHost, "start", "--attach", containerID},
		}
		runResults, runCancelled := checkexec.Run(ctx, []checkexec.Definition{direct}, checkexec.Options{
			Timeout:     time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
			OutputLimit: job.Limits.OutputLimitBytes,
		})
		result := runResults[0]
		if runCancelled {
			cancelled = true
		}
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, containerID, daemonID)
		if cleanupErr != nil {
			result.Status = checkexec.StatusError
			result.CleanupError = boundedSummary("container cleanup is uncertain: " + cleanupErr.Error())
		}
		results = append(results, result)
		if cleanupErr != nil {
			break
		}
	}
	for len(results) < len(definitions) {
		definition := definitions[len(results)]
		results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusIncomplete, Output: "A previous container could not be proved cleaned up."})
	}
	return results, cancelled
}

func (coordinator *Coordinator) containerCreateArguments(job state.CheckJob, workspace, name, index string, command ...string) ([]string, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("container command is missing")
	}
	settings := job.Execution
	cpu := fmt.Sprintf("%.3f", float64(settings.ContainerCPUMillis)/1000)
	user, err := containerUserForWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	arguments := []string{
		"create", "--pull=never", "--name", name,
		"--label", "com.owngit.check-job=" + job.ID,
		"--label", "com.owngit.check-index=" + index,
		"--log-driver", "none",
		"--network", settings.ContainerNetwork,
		"--user", user,
		"--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--cpus", cpu,
		"--memory", strconv.FormatInt(settings.ContainerMemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(settings.ContainerMemoryBytes, 10),
		"--pids-limit", strconv.FormatInt(settings.ContainerPIDs, 10),
		"--mount", "type=bind,src=" + filepath.Clean(workspace) + ",dst=/workspace",
		"--workdir", "/workspace",
		"--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "TMP=/tmp", "--env", "TEMP=/tmp",
		"--env", "XDG_CACHE_HOME=/tmp/.cache", "--env", "GOCACHE=/tmp/go-build", "--env", "GOTMPDIR=/tmp",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=" + strconv.FormatInt(settings.ContainerScratchBytes, 10),
		"--entrypoint", command[0],
		settings.ContainerImage,
	}
	return append(arguments, command[1:]...), nil
}

func verifyContainerImageIdentity(configured, output string) error {
	var inspection struct {
		ID          string          `json:"id"`
		RepoDigests []string        `json:"repo_digests"`
		OS          string          `json:"os"`
		Volumes     json.RawMessage `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &inspection); err != nil {
		return fmt.Errorf("decode immutable container image identity: %w", err)
	}
	if len(inspection.Volumes) == 0 {
		return errors.New("container image inspection did not report volume declarations")
	}
	var volumes map[string]json.RawMessage
	if err := json.Unmarshal(inspection.Volumes, &volumes); err != nil {
		return fmt.Errorf("decode container image volume declarations: %w", err)
	}
	if len(volumes) != 0 {
		return errors.New("configured check image declares writable volumes")
	}
	if inspection.OS != "linux" {
		return errors.New("configured check image is not a Linux image")
	}
	if strings.HasPrefix(configured, "sha256:") {
		if inspection.ID != configured {
			return errors.New("cached container image ID does not match the configured immutable ID")
		}
		return nil
	}
	marker := strings.LastIndex(configured, "@sha256:")
	if marker < 1 {
		return errors.New("configured container image identity is invalid")
	}
	digest := configured[marker:]
	for _, candidate := range inspection.RepoDigests {
		if strings.HasSuffix(candidate, digest) {
			return nil
		}
	}
	return errors.New("cached container image does not report the configured repository digest")
}

func (coordinator *Coordinator) removeOwnedContainer(ctx context.Context, docker, dockerHost, jobID, containerID string) error {
	inspection, err := runDockerControl(ctx, docker, dockerHost, "inspect", "--format", `{{index .Config.Labels "com.owngit.check-job"}}`, containerID)
	if err != nil {
		// A successful --rm or daemon-side removal leaves no owned object. Other
		// failures remain uncertain because absence was not proved independently.
		if strings.Contains(err.Error(), "No such object") || strings.Contains(err.Error(), "No such container") {
			return nil
		}
		return fmt.Errorf("inspect owned container: %w", err)
	}
	if strings.TrimSpace(inspection) != jobID {
		return errors.New("container ownership label does not match the immutable job")
	}
	if _, err := runDockerControl(ctx, docker, dockerHost, "rm", "--force", containerID); err != nil {
		return fmt.Errorf("remove owned container: %w", err)
	}
	return nil
}

func (coordinator *Coordinator) containerRuntimeIdentity(ctx context.Context, requireResourceEnforcement bool) (string, string, string, error) {
	docker, err := coordinator.dockerExecutable()
	if err != nil {
		return "", "", "", err
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return "", "", "", fmt.Errorf("%s selects an unsupported mutable or remote Docker endpoint", name)
		}
	}
	endpoint, err := runDockerControl(ctx, docker, "", "context", "inspect", "--format", `{{(index .Endpoints "docker").Host}}`)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect Docker endpoint: %w", err)
	}
	endpoint = strings.TrimSpace(endpoint)
	if !strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://") {
		return "", "", "", fmt.Errorf("Docker endpoint %q is not a supported local daemon", endpoint)
	}
	server, err := runDockerControl(ctx, docker, endpoint, "version", "--format", `{{.Server.Os}}`)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect Docker daemon: %w", err)
	}
	if strings.TrimSpace(server) != "linux" {
		return "", "", "", errors.New("configured checks require a local Linux Docker daemon")
	}
	if !requireResourceEnforcement {
		daemonID, err := runDockerControl(ctx, docker, endpoint, "info", "--format", `{{.ID}}`)
		if err != nil {
			return "", "", "", fmt.Errorf("inspect Docker daemon identity: %w", err)
		}
		daemonID = strings.TrimSpace(daemonID)
		if daemonID == "" || len(daemonID) > 200 || strings.ContainsAny(daemonID, "\x00\r\n") {
			return "", "", "", errors.New("Docker returned an invalid daemon identity")
		}
		return docker, endpoint, daemonID, nil
	}
	info, err := runDockerControl(ctx, docker, endpoint, "info", "--format",
		`{"id":{{json .ID}},"memory_limit":{{json .MemoryLimit}},"swap_limit":{{json .SwapLimit}},"cpu_cfs_period":{{json .CPUCfsPeriod}},"cpu_cfs_quota":{{json .CPUCfsQuota}},"pids_limit":{{json .PidsLimit}}}`)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect Docker daemon identity and resource support: %w", err)
	}
	daemonID, err := validateContainerRuntimeInfo(info)
	if err != nil {
		return "", "", "", err
	}
	return docker, endpoint, daemonID, nil
}

func validateContainerRuntimeInfo(output string) (string, error) {
	var info struct {
		ID           string `json:"id"`
		MemoryLimit  bool   `json:"memory_limit"`
		SwapLimit    bool   `json:"swap_limit"`
		CPUCFSPeriod bool   `json:"cpu_cfs_period"`
		CPUCFSQuota  bool   `json:"cpu_cfs_quota"`
		PIDsLimit    bool   `json:"pids_limit"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &info); err != nil {
		return "", fmt.Errorf("decode Docker daemon resource support: %w", err)
	}
	if info.ID == "" || len(info.ID) > 200 || info.ID != strings.TrimSpace(info.ID) || strings.ContainsAny(info.ID, "\x00\r\n") {
		return "", errors.New("Docker returned an invalid daemon identity")
	}
	if !info.MemoryLimit || !info.SwapLimit || !info.CPUCFSPeriod || !info.CPUCFSQuota || !info.PIDsLimit {
		return "", errors.New("Docker daemon does not enforce every configured memory, swap, CPU, and PID limit")
	}
	return info.ID, nil
}

func (coordinator *Coordinator) confirmContainerRuntime(ctx context.Context, docker, dockerHost, daemonID string) error {
	currentDocker, currentDockerHost, currentDaemonID, err := coordinator.containerRuntimeIdentity(ctx, true)
	if err != nil {
		return fmt.Errorf("revalidate Docker daemon identity after container creation: %w", err)
	}
	if currentDocker != docker || currentDockerHost != dockerHost || currentDaemonID != daemonID {
		return errors.New("Docker daemon identity changed during container creation")
	}
	return nil
}

func checkJobAuthority(job state.CheckJob) state.CheckJobCompletionAuthority {
	return state.CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration,
	}
}

func (coordinator *Coordinator) cleanupRecordedContainer(ctx context.Context, docker, dockerHost, jobID, containerReference, recordedID, daemonID string) error {
	if err := coordinator.removeOwnedContainer(ctx, docker, dockerHost, jobID, containerReference); err != nil {
		return err
	}
	if err := coordinator.Store.ClearCheckContainer(ctx, jobID, recordedID, daemonID); err != nil {
		return fmt.Errorf("clear container ownership: %w", err)
	}
	return nil
}

var (
	// ErrNoContainerRecord means the job has no container cleanup record.
	ErrNoContainerRecord = errors.New("no container cleanup is recorded for this job")
	// ErrContainerOnCurrentDaemon means the recorded container belongs to the
	// Docker daemon that is running now, so a start of OwnGit removes it.
	ErrContainerOnCurrentDaemon = errors.New("the recorded container belongs to the current Docker daemon; start OwnGit to remove it")
)

// ForgottenContainer is a cleanup record that ForgetForeignContainer removed.
type ForgottenContainer struct {
	Record state.CheckContainerOwnership
	// DockerUnavailable says why the current daemon could not be identified.
	// It is nil when Docker answered with another daemon identity.
	DockerUnavailable error
}

// ForgetForeignContainer removes the cleanup record of one job whose container
// OwnGit cannot reach: it was created on another Docker daemon, or Docker is
// unavailable. The caller has the owner's confirmation that the container is
// gone. No container is removed here. A record of the current daemon is
// refused, because the next start proves and removes that container itself,
// and so is the record of an unfinished job, which a running server may still
// clean up.
func (coordinator *Coordinator) ForgetForeignContainer(ctx context.Context, jobID string) (ForgottenContainer, error) {
	record, exists, err := coordinator.Store.CheckContainerOwnershipForJob(ctx, jobID)
	if err != nil {
		return ForgottenContainer{}, err
	}
	if !exists {
		return ForgottenContainer{}, ErrNoContainerRecord
	}
	forgotten := ForgottenContainer{Record: record}
	_, _, daemonID, dockerErr := coordinator.containerRuntimeIdentity(ctx, false)
	if dockerErr == nil && daemonID == record.DaemonID {
		return forgotten, ErrContainerOnCurrentDaemon
	}
	forgotten.DockerUnavailable = dockerErr
	return forgotten, coordinator.Store.ForgetCheckContainer(ctx, record)
}

func (coordinator *Coordinator) reconcileContainers(ctx context.Context) error {
	ownership, err := coordinator.Store.ActiveCheckContainers(ctx, 1000)
	if err != nil || len(ownership) == 0 {
		return err
	}
	docker, dockerHost, daemonID, err := coordinator.containerRuntimeIdentity(ctx, false)
	if err != nil {
		return fmt.Errorf("reconcile active configured-check containers: %w", err)
	}
	var failures error
	for _, item := range ownership {
		if item.DaemonID != daemonID {
			failures = errors.Join(failures, fmt.Errorf("job %s belongs to another Docker daemon; cleanup remains uncertain", item.JobID))
			continue
		}
		reference := item.ContainerID
		if reference == "" {
			reference = item.ContainerName
		}
		if err := coordinator.cleanupRecordedContainer(ctx, docker, dockerHost, item.JobID, reference, item.ContainerID, item.DaemonID); err != nil {
			failures = errors.Join(failures, fmt.Errorf("job %s container cleanup: %w", item.JobID, err))
		}
	}
	return failures
}

func (coordinator *Coordinator) dockerExecutable() (string, error) {
	if coordinator.DockerPath != "" {
		absolute, err := filepath.Abs(coordinator.DockerPath)
		if err != nil {
			return "", err
		}
		return absolute, nil
	}
	return exec.LookPath("docker")
}

func runDockerControl(ctx context.Context, docker, dockerHost string, arguments ...string) (string, error) {
	if dockerHost != "" {
		arguments = append([]string{"--host", dockerHost}, arguments...)
	}
	definition := checkexec.Definition{Name: "docker-control", Command: "docker control", Executable: docker, Arguments: arguments}
	results, cancelled := checkexec.Run(ctx, []checkexec.Definition{definition}, checkexec.Options{Timeout: 30 * time.Second, OutputLimit: dockerProbeOutputLimit})
	result := results[0]
	if cancelled {
		return result.Output, ctx.Err()
	}
	if result.Status != checkexec.StatusPassed {
		message := strings.TrimSpace(result.Output)
		if message == "" {
			message = result.Status
		}
		return result.Output, errors.New(message)
	}
	return result.Output, nil
}

func validContainerID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func unavailableResults(definitions []checkexec.Definition, err error) []checkexec.Result {
	results := make([]checkexec.Result, 0, len(definitions))
	for _, definition := range definitions {
		results = append(results, checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusUnavailable, Output: err.Error()})
	}
	return results
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	return boundedSummary(err.Error())
}

func clipContainerOutput(value string, limit int64) (string, bool) {
	if limit < 0 || int64(len(value)) <= limit {
		return value, false
	}
	return value[:limit], true
}

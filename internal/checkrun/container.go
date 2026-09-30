package checkrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

const dockerProbeOutputLimit = 64 << 10

// preparedContainer is what a job's preflight established. Every command
// container of the job is created from it, so a tag or a pull resolved once
// cannot change between commands.
type preparedContainer struct {
	docker, dockerHost, daemonID string
	// imageID is the immutable image every container of the job runs.
	imageID string
	// volumes are the image's declared volume paths, each given a disposable
	// in-memory mount. It is empty unless the policy allows image volumes.
	volumes []string
	// unenforced names the resource limits this Docker does not enforce and
	// the policy accepts.
	unenforced []string
	pulled     bool
}

// header states what the job actually ran with when it differs from what
// the policy names exactly, so the record says which image ran and which
// limits did not apply.
func (prepared preparedContainer) header(configured string) string {
	var lines []string
	if prepared.pulled || !state.ImmutableContainerImage(configured) {
		line := "Container image: " + prepared.imageID + " (from " + configured
		if prepared.pulled {
			line += ", downloaded for this job"
		}
		lines = append(lines, line+")")
	}
	if len(prepared.unenforced) != 0 {
		lines = append(lines, "Limits this Docker does not enforce: "+strings.Join(prepared.unenforced, ", "))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func (coordinator *Coordinator) containerPreflight(ctx context.Context, job state.CheckJob, workspace string) (preparedContainer, error) {
	docker, dockerHost, daemonID, unenforced, err := coordinator.containerRuntime(ctx, job.Execution.ContainerMissingEnforcement)
	if err != nil {
		return preparedContainer{}, err
	}
	prepared := preparedContainer{docker: docker, dockerHost: dockerHost, daemonID: daemonID, unenforced: unenforced}
	settings := job.Execution
	image, err := inspectContainerImage(ctx, docker, dockerHost, settings.ContainerImage)
	if err != nil && settings.ContainerPullMissing && strings.Contains(err.Error(), "No such image") {
		if pullErr := pullContainerImage(ctx, docker, dockerHost, filepath.Dir(workspace), settings.ContainerImage); pullErr != nil {
			return preparedContainer{}, pullErr
		}
		prepared.pulled = true
		image, err = inspectContainerImage(ctx, docker, dockerHost, settings.ContainerImage)
	}
	if err != nil {
		if settings.ContainerPullMissing {
			return preparedContainer{}, fmt.Errorf("inspect container image: %w", err)
		}
		return preparedContainer{}, fmt.Errorf("inspect cached image (downloading missing images is off): %w", err)
	}
	if prepared.imageID, prepared.volumes, err = verifyContainerImageIdentity(settings, image); err != nil {
		return preparedContainer{}, err
	}
	if err := verifyContainerNetwork(ctx, docker, dockerHost, settings.ContainerNetwork); err != nil {
		return preparedContainer{}, err
	}

	probeName := "owngit-check-probe-" + job.ID
	arguments, err := coordinator.containerCreateArguments(job, prepared, workspace, probeName, "probe", "/bin/true")
	if err != nil {
		return preparedContainer{}, err
	}
	authority := checkJobAuthority(job)
	if err := coordinator.Store.PlanCheckContainer(ctx, authority, probeName, daemonID, time.Now().UTC()); err != nil {
		return preparedContainer{}, fmt.Errorf("record restricted validation container plan: %w", err)
	}
	created, err := runDockerControl(ctx, docker, dockerHost, arguments...)
	if err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, probeName, "", daemonID)
		return preparedContainer{}, errors.Join(fmt.Errorf("validate restricted container creation: %w", err), cleanupErr)
	}
	containerID := strings.TrimSpace(created)
	if !validContainerID(containerID) {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, probeName, "", daemonID)
		return preparedContainer{}, errors.Join(errors.New("Docker returned an invalid probe container identity"), cleanupErr)
	}
	if err := coordinator.confirmCreatedContainer(ctx, job, prepared, containerID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		return preparedContainer{}, errors.Join(err, cleanupErr)
	}
	if err := coordinator.Store.ConfirmCheckContainer(ctx, job.ID, probeName, containerID, daemonID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		return preparedContainer{}, errors.Join(fmt.Errorf("confirm restricted validation container ownership: %w", err), cleanupErr)
	}
	_, startErr := runDockerControl(ctx, docker, dockerHost, "start", "--attach", containerID)
	cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, containerID, daemonID)
	if startErr != nil {
		return preparedContainer{}, errors.Join(fmt.Errorf("start restricted validation container: %w", startErr), cleanupErr)
	}
	if cleanupErr != nil {
		return preparedContainer{}, fmt.Errorf("remove restricted validation container: %w", cleanupErr)
	}
	return prepared, nil
}

// verifyContainerNetwork checks that a named network is one the owner
// created, under exactly that name. Docker would also accept a network ID,
// and the host network's ID must not stand in for the refused host network.
func verifyContainerNetwork(ctx context.Context, docker, dockerHost, network string) error {
	if network == state.ContainerNetworkNone || network == state.ContainerNetworkBridge {
		return nil
	}
	output, err := runDockerControl(ctx, docker, dockerHost, "network", "inspect", "--format", `{"name":{{json .Name}},"driver":{{json .Driver}}}`, network)
	if err != nil {
		return fmt.Errorf("inspect Docker network %s: %w", network, err)
	}
	var inspection struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &inspection); err != nil {
		return fmt.Errorf("decode Docker network %s: %w", network, err)
	}
	if inspection.Name != network {
		return fmt.Errorf("Docker network %s is not a network of that name", network)
	}
	if inspection.Driver == "host" || inspection.Driver == "null" {
		return fmt.Errorf("Docker network %s uses the %s driver, which configured checks never use", network, inspection.Driver)
	}
	return nil
}

func inspectContainerImage(ctx context.Context, docker, dockerHost, image string) (string, error) {
	return runDockerControl(ctx, docker, dockerHost, "image", "inspect", "--format",
		`{"id":{{json .Id}},"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"volumes":{{json (index .Config "Volumes")}}}`, image)
}

// Downloading an image is bounded like every other Docker call, with room
// for a large image.
const (
	containerPullTimeout     = 15 * time.Minute
	containerPullOutputLimit = 64 << 10
)

// pullContainerImage downloads image from its registry. The Docker client
// reads its settings from an empty private folder inside the job envelope,
// so no stored registry login or credential helper is used: only images the
// registry serves anonymously can be downloaded.
func pullContainerImage(ctx context.Context, docker, dockerHost, envelope, image string) error {
	configDir := filepath.Join(envelope, "docker-config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		return fmt.Errorf("prepare image download: %w", err)
	}
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DOCKER_CONFIG=") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "DOCKER_CONFIG="+configDir)
	definition := checkexec.Definition{Name: "docker-pull", Command: "docker pull", Executable: docker,
		Arguments: []string{"--host", dockerHost, "pull", "--quiet", image}}
	results, cancelled := checkexec.Run(ctx, []checkexec.Definition{definition}, checkexec.Options{
		Timeout: containerPullTimeout, OutputLimit: containerPullOutputLimit, Env: environment,
	})
	if cancelled {
		return fmt.Errorf("download container image %s: %w", image, ctx.Err())
	}
	if result := results[0]; result.Status != checkexec.StatusPassed {
		message := strings.TrimSpace(result.Output)
		if message == "" {
			message = result.Status
		}
		return fmt.Errorf("download container image %s: %s", image, message)
	}
	return nil
}

func (coordinator *Coordinator) runContainerChecks(ctx context.Context, job state.CheckJob, prepared preparedContainer, workspace string, definitions []checkexec.Definition) ([]checkexec.Result, bool) {
	docker, dockerHost, daemonID := prepared.docker, prepared.dockerHost, prepared.daemonID
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
		arguments, argumentsErr := coordinator.containerCreateArguments(job, prepared, workspace, name, strconv.Itoa(index), "/bin/sh", "-c", definition.Command)
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
		if err := coordinator.confirmCreatedContainer(ctx, job, prepared, containerID); err != nil {
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
	if header := prepared.header(job.Execution.ContainerImage); header != "" && len(results) != 0 {
		results[0].Output = header + results[0].Output
	}
	return results, cancelled
}

func (coordinator *Coordinator) containerCreateArguments(job state.CheckJob, prepared preparedContainer, workspace, name, index string, command ...string) ([]string, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("container command is missing")
	}
	settings := job.Execution
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
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--mount", "type=bind,src=" + filepath.Clean(workspace) + ",dst=/workspace",
		"--workdir", "/workspace",
		"--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "TMP=/tmp", "--env", "TEMP=/tmp",
		"--env", "XDG_CACHE_HOME=/tmp/.cache", "--env", "GOCACHE=/tmp/go-build", "--env", "GOTMPDIR=/tmp",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=" + strconv.FormatInt(settings.ContainerScratchBytes, 10),
	}
	// Each limit is passed only when this Docker enforces it. Docker
	// refuses a CPU limit it cannot enforce and drops the others, so a
	// limit the policy accepted as unenforced is left out.
	enforced := func(limit string) bool { return !slices.Contains(prepared.unenforced, limit) }
	memory := strconv.FormatInt(settings.ContainerMemoryBytes, 10)
	if enforced(state.ContainerLimitCPU) {
		arguments = append(arguments, "--cpus", fmt.Sprintf("%.3f", float64(settings.ContainerCPUMillis)/1000))
	}
	if enforced(state.ContainerLimitMemory) {
		arguments = append(arguments, "--memory", memory)
		if enforced(state.ContainerLimitSwap) {
			arguments = append(arguments, "--memory-swap", memory)
		}
	}
	if enforced(state.ContainerLimitPIDs) {
		arguments = append(arguments, "--pids-limit", strconv.FormatInt(settings.ContainerPIDs, 10))
	}
	if !settings.ContainerWritableRoot {
		arguments = append(arguments, "--read-only")
	}
	for _, volume := range prepared.volumes {
		arguments = append(arguments, "--tmpfs", volume+":rw,exec,nosuid,nodev,size="+strconv.FormatInt(settings.ContainerScratchBytes, 10))
	}
	if prepared.imageID == "" {
		return nil, errors.New("container image was not resolved")
	}
	arguments = append(arguments, "--entrypoint", command[0], prepared.imageID)
	return append(arguments, command[1:]...), nil
}

// verifyContainerImageIdentity checks the inspected image against the policy
// and returns the image ID every container runs, with the declared volume
// paths that get disposable mounts.
func verifyContainerImageIdentity(settings state.CheckExecutionSettings, output string) (string, []string, error) {
	var inspection struct {
		ID          string          `json:"id"`
		RepoDigests []string        `json:"repo_digests"`
		OS          string          `json:"os"`
		Volumes     json.RawMessage `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &inspection); err != nil {
		return "", nil, fmt.Errorf("decode container image identity: %w", err)
	}
	if len(inspection.Volumes) == 0 {
		return "", nil, errors.New("container image inspection did not report volume declarations")
	}
	var declared map[string]json.RawMessage
	if err := json.Unmarshal(inspection.Volumes, &declared); err != nil {
		return "", nil, fmt.Errorf("decode container image volume declarations: %w", err)
	}
	volumes := make([]string, 0, len(declared))
	for volume := range declared {
		volumes = append(volumes, volume)
	}
	slices.Sort(volumes)
	if len(volumes) != 0 {
		if !settings.ContainerImageVolumes {
			return "", nil, errors.New("configured check image declares volumes (allow image volumes in the check policy to give them disposable mounts)")
		}
		if err := validateImageVolumes(volumes); err != nil {
			return "", nil, err
		}
	}
	if inspection.OS != "linux" {
		return "", nil, errors.New("configured check image is not a Linux image")
	}
	if !state.ImmutableContainerImage(inspection.ID) || !strings.HasPrefix(inspection.ID, "sha256:") {
		return "", nil, errors.New("Docker reported an invalid image ID")
	}
	configured := settings.ContainerImage
	switch {
	case strings.HasPrefix(configured, "sha256:"):
		if inspection.ID != configured {
			return "", nil, errors.New("cached container image ID does not match the configured immutable ID")
		}
	case strings.Contains(configured, "@sha256:"):
		digest := configured[strings.LastIndex(configured, "@sha256:"):]
		if !slices.ContainsFunc(inspection.RepoDigests, func(candidate string) bool { return strings.HasSuffix(candidate, digest) }) {
			return "", nil, errors.New("cached container image does not report the configured repository digest")
		}
	case !settings.ContainerAllowTags:
		return "", nil, errors.New("configured container image identity is invalid")
	}
	return inspection.ID, volumes, nil
}

// Paths the container needs for itself or that OwnGit mounts. A volume at or
// around one of them would hide it or be hidden by it.
var reservedContainerPaths = []string{"/workspace", "/tmp", "/proc", "/sys", "/dev", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}

const maximumImageVolumes = 32

// validateImageVolumes accepts the declared volume paths only when each can
// be given its own in-memory mount without touching the paths the container
// runs on. Anything else keeps the image refused.
func validateImageVolumes(volumes []string) error {
	if len(volumes) > maximumImageVolumes {
		return fmt.Errorf("configured check image declares more than %d volumes", maximumImageVolumes)
	}
	for _, volume := range volumes {
		if !strings.HasPrefix(volume, "/") || volume == "/" || path.Clean(volume) != volume || len(volume) > 1024 ||
			strings.ContainsAny(volume, ",:\x00\r\n") {
			return fmt.Errorf("configured check image declares volume %q, which cannot be given a disposable mount", volume)
		}
		for _, reserved := range reservedContainerPaths {
			if pathsOverlap(volume, reserved) {
				return fmt.Errorf("configured check image declares volume %q, which overlaps %s", volume, reserved)
			}
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

// confirmCreatedContainer proves a created container is the one planned: the
// Docker daemon is still the one the job prepared on, and, when the image
// declares volumes, Docker made no volume of its own for any of them.
func (coordinator *Coordinator) confirmCreatedContainer(ctx context.Context, job state.CheckJob, prepared preparedContainer, containerID string) error {
	docker, dockerHost, daemonID, _, err := coordinator.containerRuntime(ctx, job.Execution.ContainerMissingEnforcement)
	if err != nil {
		return fmt.Errorf("revalidate Docker daemon identity after container creation: %w", err)
	}
	if docker != prepared.docker || dockerHost != prepared.dockerHost || daemonID != prepared.daemonID {
		return errors.New("Docker daemon identity changed during container creation")
	}
	if len(prepared.volumes) == 0 {
		return nil
	}
	output, err := runDockerControl(ctx, docker, dockerHost, "container", "inspect", "--format", `{{json .Mounts}}`, containerID)
	if err != nil {
		return fmt.Errorf("inspect container mounts: %w", err)
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &mounts); err != nil {
		return fmt.Errorf("decode container mounts: %w", err)
	}
	for _, mount := range mounts {
		if mount.Type != "bind" || mount.Destination != "/workspace" {
			return fmt.Errorf("Docker added a %s mount at %s; image volumes could not be kept disposable", mount.Type, mount.Destination)
		}
	}
	return nil
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

// containerRuntimeIdentity finds the local Docker daemon and its identity.
func (coordinator *Coordinator) containerRuntimeIdentity(ctx context.Context) (string, string, string, error) {
	docker, endpoint, err := coordinator.dockerEndpoint(ctx)
	if err != nil {
		return "", "", "", err
	}
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

// containerRuntime finds the local Docker daemon and checks that it enforces
// every resource limit, except those in accepted. It returns the accepted
// limits the daemon does not enforce.
func (coordinator *Coordinator) containerRuntime(ctx context.Context, accepted []string) (string, string, string, []string, error) {
	docker, endpoint, err := coordinator.dockerEndpoint(ctx)
	if err != nil {
		return "", "", "", nil, err
	}
	info, err := runDockerControl(ctx, docker, endpoint, "info", "--format",
		`{"id":{{json .ID}},"memory_limit":{{json .MemoryLimit}},"swap_limit":{{json .SwapLimit}},"cpu_cfs_period":{{json .CPUCfsPeriod}},"cpu_cfs_quota":{{json .CPUCfsQuota}},"pids_limit":{{json .PidsLimit}}}`)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("inspect Docker daemon identity and resource support: %w", err)
	}
	daemonID, unenforced, err := validateContainerRuntimeInfo(info, accepted)
	if err != nil {
		return "", "", "", nil, err
	}
	return docker, endpoint, daemonID, unenforced, nil
}

func (coordinator *Coordinator) dockerEndpoint(ctx context.Context) (string, string, error) {
	docker, err := coordinator.dockerExecutable()
	if err != nil {
		return "", "", err
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return "", "", fmt.Errorf("%s selects an unsupported mutable or remote Docker endpoint", name)
		}
	}
	endpoint, err := runDockerControl(ctx, docker, "", "context", "inspect", "--format", `{{(index .Endpoints "docker").Host}}`)
	if err != nil {
		return "", "", fmt.Errorf("inspect Docker endpoint: %w", err)
	}
	endpoint = strings.TrimSpace(endpoint)
	if !strings.HasPrefix(endpoint, "unix://") && !strings.HasPrefix(endpoint, "npipe://") {
		return "", "", fmt.Errorf("Docker endpoint %q is not a supported local daemon", endpoint)
	}
	server, err := runDockerControl(ctx, docker, endpoint, "version", "--format", `{{.Server.Os}}`)
	if err != nil {
		return "", "", fmt.Errorf("inspect Docker daemon: %w", err)
	}
	if strings.TrimSpace(server) != "linux" {
		return "", "", errors.New("configured checks require a local Linux Docker daemon")
	}
	return docker, endpoint, nil
}

// validateContainerRuntimeInfo returns the daemon identity and the accepted
// limits it does not enforce. A missing limit the policy did not accept
// refuses the run and is named.
func validateContainerRuntimeInfo(output string, accepted []string) (string, []string, error) {
	var info struct {
		ID           string `json:"id"`
		MemoryLimit  bool   `json:"memory_limit"`
		SwapLimit    bool   `json:"swap_limit"`
		CPUCFSPeriod bool   `json:"cpu_cfs_period"`
		CPUCFSQuota  bool   `json:"cpu_cfs_quota"`
		PIDsLimit    bool   `json:"pids_limit"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &info); err != nil {
		return "", nil, fmt.Errorf("decode Docker daemon resource support: %w", err)
	}
	if info.ID == "" || len(info.ID) > 200 || info.ID != strings.TrimSpace(info.ID) || strings.ContainsAny(info.ID, "\x00\r\n") {
		return "", nil, errors.New("Docker returned an invalid daemon identity")
	}
	var unenforced, refused []string
	for _, limit := range []struct {
		name     string
		enforced bool
	}{
		{state.ContainerLimitMemory, info.MemoryLimit},
		{state.ContainerLimitSwap, info.SwapLimit},
		{state.ContainerLimitCPU, info.CPUCFSPeriod && info.CPUCFSQuota},
		{state.ContainerLimitPIDs, info.PIDsLimit},
	} {
		switch {
		case limit.enforced:
		case slices.Contains(accepted, limit.name):
			unenforced = append(unenforced, limit.name)
		default:
			refused = append(refused, limit.name)
		}
	}
	if len(refused) != 0 {
		return "", nil, fmt.Errorf("Docker daemon does not enforce the %s limit (the check policy can accept this)", strings.Join(refused, ", "))
	}
	return info.ID, unenforced, nil
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
	_, _, daemonID, dockerErr := coordinator.containerRuntimeIdentity(ctx)
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
	docker, dockerHost, daemonID, err := coordinator.containerRuntimeIdentity(ctx)
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

package checkrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"owngit/internal/checkexec"
	"owngit/internal/state"
)

type containerMount struct {
	source, target string
	readOnly       bool
}

func (coordinator *Coordinator) runContainerStep(ctx context.Context, job state.CheckJob, prepared preparedContainer, name string, definition checkexec.Definition, arguments []string, output io.Writer) (checkexec.Result, bool, bool) {
	docker, dockerHost, daemonID := prepared.docker, prepared.dockerHost, prepared.daemonID
	result := checkexec.Result{Name: definition.Name, Command: definition.Command, Status: checkexec.StatusError}
	if err := coordinator.Store.PlanCheckContainer(ctx, checkJobAuthority(job), name, daemonID, time.Now().UTC()); err != nil {
		result.Output = boundedSummary("record container plan: " + err.Error())
		return result, false, true
	}
	created, createErr := runDockerControl(ctx, docker, dockerHost, arguments...)
	if createErr != nil {
		if strings.Contains(createErr.Error(), "No such image") || strings.Contains(createErr.Error(), "not found") {
			result.Status = checkexec.StatusUnavailable
		}
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, name, "", daemonID)
		result.Output, result.Truncated = clipContainerOutput(errors.Join(createErr, cleanupErr).Error(), job.Limits.OutputLimitBytes)
		return result, false, cleanupErr != nil
	}
	containerID := strings.TrimSpace(created)
	if !validContainerID(containerID) {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, name, "", daemonID)
		result.Output, result.CleanupError = "Docker returned an invalid container identity.", boundedError(cleanupErr)
		return result, false, true
	}
	if err := coordinator.confirmCreatedContainer(ctx, job, prepared, containerID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		result.CleanupError = boundedSummary(errors.Join(err, cleanupErr).Error())
		return result, false, true
	}
	if err := coordinator.Store.ConfirmCheckContainer(ctx, job.ID, name, containerID, daemonID); err != nil {
		cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, "", daemonID)
		result.CleanupError = boundedSummary(errors.Join(fmt.Errorf("confirm container ownership: %w", err), cleanupErr).Error())
		return result, false, true
	}
	direct := checkexec.Definition{
		Name: definition.Name, Command: definition.Command, Executable: docker,
		Arguments: []string{"--host", dockerHost, "start", "--attach", containerID},
	}
	results, cancelled := checkexec.Run(ctx, []checkexec.Definition{direct}, checkexec.Options{
		Timeout:     time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
		OutputLimit: job.Limits.OutputLimitBytes, Env: os.Environ(), OutputTap: output,
	})
	result = results[0]
	cleanupErr := coordinator.cleanupRecordedContainer(context.WithoutCancel(ctx), docker, dockerHost, job.ID, containerID, containerID, daemonID)
	if cleanupErr != nil {
		result.Status = checkexec.StatusError
		result.CleanupError = boundedSummary("container cleanup is uncertain: " + cleanupErr.Error())
	}
	return result, cancelled, cleanupErr != nil
}

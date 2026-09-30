package checkrunner_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// realDockerRegistryEnv names a local registry, such as localhost:5000, that
// accepts anonymous pushes and pulls. The download test needs one.
const realDockerRegistryEnv = "OWNGIT_DOCKER_REGISTRY"

// The container options, each on a real local Docker daemon.
func TestRealDockerContainerOptions(t *testing.T) {
	config := requireRealDocker(t)
	imageID := strings.TrimSpace(mustDocker(t, config, "image", "inspect", "--format", "{{.Id}}", config.image))
	unique := strconv.FormatInt(time.Now().UnixNano(), 36)

	t.Run("tag_resolves_to_one_image_ID", func(t *testing.T) {
		tag := "owngit-check-test/tagged:" + unique
		mustDocker(t, config, "tag", imageID, tag)
		t.Cleanup(func() { removeDockerImageTag(t, config, tag) })
		fixture := newRealDockerFixtureWith(t, config, heldCommand(`true`), func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = tag
			settings.ContainerAllowTags = true
		})
		job, ownership := fixture.waitForRunningContainer()
		if created := strings.TrimSpace(mustDocker(t, config, "inspect", "--format", "{{.Config.Image}}", ownership.ContainerID)); created != imageID {
			t.Fatalf("container was created from %q, want the resolved ID %s", created, imageID)
		}
		attempt := fixture.releaseAndFinish(job, state.AttemptPassed)
		if !strings.Contains(attempt.Results[0].OutputExcerpt, "Container image: "+imageID+" (from "+tag+")") {
			t.Fatalf("output does not record the resolved image: %q", attempt.Results[0].OutputExcerpt)
		}
	})

	t.Run("missing_image_is_downloaded_without_stored_credentials", func(t *testing.T) {
		registry := os.Getenv(realDockerRegistryEnv)
		if registry == "" {
			t.Skipf("set %s to a local registry to test downloads", realDockerRegistryEnv)
		}
		reference := registry + "/owngit-pull-test:" + unique
		mustDocker(t, config, "tag", imageID, reference)
		mustDocker(t, config, "push", "--quiet", reference)
		removeDockerImageTag(t, config, reference)
		t.Cleanup(func() { removeDockerImageTag(t, config, reference) })
		fixture := newRealDockerFixtureWith(t, config, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = reference
			settings.ContainerAllowTags = true
			settings.ContainerPullMissing = true
		})
		job := fixture.waitForTerminalJob(fixture.waitForAnyJob().ID)
		attempt := fixture.attempt(job, state.AttemptPassed)
		if !strings.Contains(attempt.Results[0].OutputExcerpt, "downloaded for this job") {
			t.Fatalf("output does not record the download: %q", attempt.Results[0].OutputExcerpt)
		}

		absent := registry + "/owngit-absent:" + unique
		failed := newRealDockerFixtureWith(t, config, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = absent
			settings.ContainerAllowTags = true
			settings.ContainerPullMissing = true
		})
		job = failed.waitForTerminalJob(failed.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || job.AttemptID != "" || !strings.Contains(job.Summary, "download container image") {
			t.Fatalf("failed download job status=%s attempt=%s summary=%s", job.Status, job.AttemptID, job.Summary)
		}
	})

	t.Run("image_volumes_get_disposable_mounts", func(t *testing.T) {
		source := "owngit-check-volume-source-" + unique
		volumeImage := "owngit-check-test/volumes:" + unique
		mustDocker(t, config, "create", "--name", source, imageID, "/bin/true")
		mustDocker(t, config, "commit", "--change", "VOLUME /data", source, volumeImage)
		mustDocker(t, config, "rm", source)
		t.Cleanup(func() { removeDockerImageTag(t, config, volumeImage) })
		volumesBefore := dockerVolumes(t, config)

		refused := newRealDockerFixtureWith(t, config, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = volumeImage
			settings.ContainerAllowTags = true
		})
		job := refused.waitForTerminalJob(refused.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || !strings.Contains(job.Summary, "declares volumes") {
			t.Fatalf("volume image without the option: status=%s summary=%s", job.Status, job.Summary)
		}

		fixture := newRealDockerFixtureWith(t, config, heldCommand(`grep -q ' /data tmpfs ' /proc/mounts
	printf data > /data/probe`), func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = volumeImage
			settings.ContainerAllowTags = true
			settings.ContainerImageVolumes = true
		})
		job, ownership := fixture.waitForRunningContainer()
		inspection := inspectOwnedContainer(t, config, ownership.ContainerID)
		if tmpfs := inspection.HostConfig.Tmpfs["/data"]; !strings.Contains(tmpfs, "size="+strconv.FormatInt(testContainerTmpfs, 10)) {
			t.Fatalf("/data mount=%q", tmpfs)
		}
		for _, mount := range inspection.Mounts {
			if mount.Type == "volume" {
				t.Fatalf("Docker made a volume: %+v", mount)
			}
		}
		fixture.releaseAndFinish(job, state.AttemptPassed)
		fixture.assertContainerRemoved(ownership.ContainerID)
		if after := dockerVolumes(t, config); !slices.Equal(after, volumesBefore) {
			t.Fatalf("Docker volumes changed from %v to %v", volumesBefore, after)
		}
	})

	// A declared volume that is a link in the image would put the disposable
	// mount wherever the link points, here the workspace. It is refused.
	t.Run("image_volume_through_a_link_is_refused", func(t *testing.T) {
		source := "owngit-check-link-source-" + unique
		linkImage := "owngit-check-test/volume-link:" + unique
		mustDocker(t, config, "create", "--name", source, imageID, "/bin/sh", "-c", "ln -s /workspace /data")
		mustDocker(t, config, "start", "--attach", source)
		mustDocker(t, config, "commit", "--change", "VOLUME /data", source, linkImage)
		mustDocker(t, config, "rm", source)
		t.Cleanup(func() { removeDockerImageTag(t, config, linkImage) })
		fixture := newRealDockerFixtureWith(t, config, `printf data > /data/escaped`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerImage = linkImage
			settings.ContainerAllowTags = true
			settings.ContainerImageVolumes = true
		})
		job := fixture.waitForTerminalJob(fixture.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || job.AttemptID != "" || !strings.Contains(job.Summary, "/data is a link in the image") {
			t.Fatalf("volume through a link: status=%s attempt=%s summary=%s", job.Status, job.AttemptID, job.Summary)
		}
	})

	// Commands still run as OwnGit's own user, so they can change the files
	// of the image that user may change, such as /var/tmp.
	t.Run("writable_root_keeps_the_other_restrictions", func(t *testing.T) {
		fixture := newRealDockerFixtureWith(t, config, heldCommand(`test "$(id -u)" -ne 0
	grep -Eq '^CapEff:[[:space:]]*0+$' /proc/self/status
	grep -Eq '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
	touch /var/tmp/owngit-root-probe`), func(settings *state.CheckExecutionSettings) {
			settings.ContainerNetwork = state.ContainerNetworkNone
			settings.ContainerWritableRoot = true
		})
		job, ownership := fixture.waitForRunningContainer()
		inspection := inspectOwnedContainer(t, config, ownership.ContainerID)
		if inspection.HostConfig.ReadonlyRootfs || inspection.HostConfig.NetworkMode != state.ContainerNetworkNone {
			t.Fatalf("readonly=%v network=%s", inspection.HostConfig.ReadonlyRootfs, inspection.HostConfig.NetworkMode)
		}
		fixture.releaseAndFinish(job, state.AttemptPassed)
	})

	t.Run("named_network", func(t *testing.T) {
		network := "owngit-check-test-" + unique
		mustDocker(t, config, "network", "create", "--internal", network)
		t.Cleanup(func() {
			if _, err := dockerCommand(context.Background(), config, "network", "rm", network); err != nil {
				t.Errorf("remove test network: %v", err)
			}
		})
		fixture := newRealDockerFixtureWith(t, config, heldCommand(`true`), func(settings *state.CheckExecutionSettings) {
			settings.ContainerNetwork = network
		})
		job, ownership := fixture.waitForRunningContainer()
		if mode := inspectOwnedContainer(t, config, ownership.ContainerID).HostConfig.NetworkMode; mode != network {
			t.Fatalf("network mode=%q", mode)
		}
		fixture.releaseAndFinish(job, state.AttemptPassed)

		// Docker accepts a network ID where a name is expected; the host
		// network's ID must not stand in for the refused host network.
		hostID := strings.TrimSpace(mustDocker(t, config, "network", "inspect", "--format", "{{.Id}}", "host"))
		refused := newRealDockerFixtureWith(t, config, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerNetwork = hostID
		})
		job = refused.waitForTerminalJob(refused.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || job.AttemptID != "" || !strings.Contains(job.Summary, "not a network of that name") {
			t.Fatalf("host network by ID: status=%s attempt=%s summary=%s", job.Status, job.AttemptID, job.Summary)
		}
	})

	t.Run("a_missing_limit_runs_only_when_accepted", func(t *testing.T) {
		// The daemon is real; only its report of swap enforcement is changed,
		// as on a computer whose kernel does not account swap.
		wrapper := filepath.Join(t.TempDir(), "docker")
		script := "#!/bin/sh\ncase \"$*\" in *memory_limit*) out=$(" + shellQuoteDockerTest(config.docker) + " \"$@\") || exit $?\n" +
			"printf '%s\\n' \"$out\" | sed 's/\"swap_limit\":true/\"swap_limit\":false/' ;;\n*) exec " + shellQuoteDockerTest(config.docker) + " \"$@\" ;;\nesac\n"
		noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
		noSwap := realDockerConfig{docker: wrapper, dockerHost: config.dockerHost, image: config.image}

		refused := newRealDockerFixtureWith(t, noSwap, `true`, func(settings *state.CheckExecutionSettings) {})
		job := refused.waitForTerminalJob(refused.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || !strings.Contains(job.Summary, "does not enforce the swap limit") {
			t.Fatalf("unaccepted missing limit: status=%s summary=%s", job.Status, job.Summary)
		}

		accepted := newRealDockerFixtureWith(t, noSwap, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerMissingEnforcement = []string{state.ContainerLimitSwap}
		})
		job = accepted.waitForTerminalJob(accepted.waitForAnyJob().ID)
		attempt := accepted.attempt(job, state.AttemptPassed)
		if !strings.Contains(attempt.Results[0].OutputExcerpt, "Limits this Docker does not enforce: swap") {
			t.Fatalf("output does not record the unenforced limit: %q", attempt.Results[0].OutputExcerpt)
		}
	})

	t.Run("an_accepted_missing_CPU_limit_is_not_sent", func(t *testing.T) {
		// The daemon is real. The wrapper reports no CPU enforcement and, as
		// such a daemon does, refuses to create a container with a CPU limit.
		wrapper := filepath.Join(t.TempDir(), "docker")
		script := "#!/bin/sh\ncase \"$*\" in *memory_limit*) out=$(" + shellQuoteDockerTest(config.docker) + " \"$@\") || exit $?\n" +
			"printf '%s\\n' \"$out\" | sed 's/\"cpu_cfs_quota\":true/\"cpu_cfs_quota\":false/' ;;\n" +
			"*' create '*--cpus*) echo 'NanoCPUs can not be set, as your kernel does not support CPU CFS scheduler' >&2; exit 1 ;;\n" +
			"*) exec " + shellQuoteDockerTest(config.docker) + " \"$@\" ;;\nesac\n"
		noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
		noCPU := realDockerConfig{docker: wrapper, dockerHost: config.dockerHost, image: config.image}

		refused := newRealDockerFixtureWith(t, noCPU, `true`, func(settings *state.CheckExecutionSettings) {})
		job := refused.waitForTerminalJob(refused.waitForAnyJob().ID)
		if job.Status != state.CheckJobUnavailable || !strings.Contains(job.Summary, "does not enforce the cpu limit") {
			t.Fatalf("unaccepted missing CPU limit: status=%s summary=%s", job.Status, job.Summary)
		}
		accepted := newRealDockerFixtureWith(t, noCPU, `true`, func(settings *state.CheckExecutionSettings) {
			settings.ContainerMissingEnforcement = []string{state.ContainerLimitCPU}
		})
		job = accepted.waitForTerminalJob(accepted.waitForAnyJob().ID)
		attempt := accepted.attempt(job, state.AttemptPassed)
		if !strings.Contains(attempt.Results[0].OutputExcerpt, "Limits this Docker does not enforce: cpu") {
			t.Fatalf("output does not record the unenforced limit: %q", attempt.Results[0].OutputExcerpt)
		}
	})
}

// heldCommand runs command, then waits until the test removes .owngit-held,
// so the running container can be inspected.
func heldCommand(command string) string {
	return "set -eu\n\t" + command + "\n\tprintf 'ready\\n' > .owngit-held\n\twhile [ -f .owngit-held ]; do sleep 1; done"
}

func (fixture *realDockerFixture) releaseAndFinish(job state.CheckJob, want string) state.CheckAttempt {
	fixture.t.Helper()
	noErr(fixture.t, os.Remove(filepath.Join(fixture.sourcePath(job.ID), ".owngit-held")))
	return fixture.attempt(fixture.waitForTerminalJob(job.ID), want)
}

func (fixture *realDockerFixture) attempt(job state.CheckJob, want string) state.CheckAttempt {
	fixture.t.Helper()
	if job.AttemptID == "" {
		fixture.t.Fatalf("job status=%s summary=%s has no attempt", job.Status, job.Summary)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, job.AttemptID)
	if err != nil || !exists || attempt.Status != want || len(attempt.Results) != 1 {
		fixture.t.Fatalf("attempt status=%s want=%s exists=%v err=%v diagnostic=%s", attempt.Status, want, exists, err, fixture.attemptDiagnostic(job))
	}
	return attempt
}

func mustDocker(t *testing.T, config realDockerConfig, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := dockerCommand(ctx, config, arguments...)
	noErr(t, err)
	return output
}

// removeDockerImageTag removes one test tag. The image it named stays when
// another name still refers to it.
func removeDockerImageTag(t *testing.T, config realDockerConfig, tag string) {
	t.Helper()
	if _, err := dockerCommand(context.Background(), config, "image", "rm", tag); err != nil && !strings.Contains(err.Error(), "No such image") {
		t.Errorf("remove test image tag %s: %v", tag, err)
	}
}

func dockerVolumes(t *testing.T, config realDockerConfig) []string {
	t.Helper()
	volumes := strings.Fields(mustDocker(t, config, "volume", "ls", "--quiet"))
	slices.Sort(volumes)
	return volumes
}

package checkrun

import (
	"slices"
	"strings"
	"testing"

	"owngit/internal/state"
)

// A limit is passed to Docker exactly when this Docker enforces it. Docker
// refuses to create a container with a CPU limit it cannot enforce, so an
// accepted missing CPU limit must not be sent; an accepted limit that the
// daemon does enforce still is.
func TestContainerCreateArgumentsPassOnlyEnforcedLimits(t *testing.T) {
	const allEnforced = `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":true}`
	for name, test := range map[string]struct {
		info     string
		accepted []string
		absent   []string
	}{
		"missing CPU accepted": {
			info:     `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":false,"cpu_cfs_quota":false,"pids_limit":true}`,
			accepted: []string{"cpu"}, absent: []string{"--cpus"},
		},
		"CPU accepted but enforced": {info: allEnforced, accepted: []string{"cpu"}},
		"all accepted and enforced": {info: allEnforced, accepted: []string{"cpu", "memory", "pids", "swap"}},
		"missing swap accepted": {
			info:     `{"id":"daemon","memory_limit":true,"swap_limit":false,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":true}`,
			accepted: []string{"swap"}, absent: []string{"--memory-swap"},
		},
		"missing memory and swap accepted": {
			info:     `{"id":"daemon","memory_limit":false,"swap_limit":false,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":true}`,
			accepted: []string{"memory", "swap"}, absent: []string{"--memory", "--memory-swap"},
		},
		"missing PIDs accepted": {
			info:     `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":false}`,
			accepted: []string{"pids"}, absent: []string{"--pids-limit"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, unenforced, err := validateContainerRuntimeInfo(test.info, test.accepted)
			noErr(t, err)
			job := state.CheckJob{ID: "job", Execution: state.CheckExecutionSettings{
				ContainerNetwork: "none", ContainerCPUMillis: 1000, ContainerMemoryBytes: 64 << 20, ContainerPIDs: 16,
				ContainerScratchBytes: 1 << 20, ContainerMissingEnforcement: test.accepted,
			}}
			prepared := preparedContainer{imageID: "sha256:" + strings.Repeat("a", 64), unenforced: unenforced}
			arguments, err := (&Coordinator{}).containerCreateArguments(job, prepared, t.TempDir(), "name", "probe", "/bin/true")
			noErr(t, err)
			for _, flag := range []string{"--cpus", "--memory", "--memory-swap", "--pids-limit"} {
				if want := !slices.Contains(test.absent, flag); slices.Contains(arguments, flag) != want {
					t.Fatalf("%s passed=%v, want %v: %v", flag, !want, want, arguments)
				}
			}
			for _, forced := range []string{"--read-only", "--cap-drop", "--security-opt", "--pull=never"} {
				if !slices.Contains(arguments, forced) {
					t.Fatalf("%s is missing: %v", forced, arguments)
				}
			}
		})
	}
	// A missing limit the policy did not accept still refuses the run.
	if _, _, err := validateContainerRuntimeInfo(`{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":false,"cpu_cfs_quota":true,"pids_limit":true}`, []string{"swap"}); err == nil || !strings.Contains(err.Error(), "cpu") {
		t.Fatalf("an unaccepted missing CPU limit: %v", err)
	}
}

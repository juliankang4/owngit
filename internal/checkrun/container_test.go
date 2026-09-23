package checkrun

import (
	"strings"
	"testing"

	"owngit/internal/state"
)

func TestContainerCreateArgumentsEnforceRestrictions(t *testing.T) {
	job := state.CheckJob{
		ID: "0123456789abcdef",
		Execution: state.CheckExecutionSettings{
			ContainerImage:        "example.invalid/checks@sha256:" + strings.Repeat("a", 64),
			ContainerNetwork:      "none",
			ContainerCPUMillis:    750,
			ContainerMemoryBytes:  256 << 20,
			ContainerPIDs:         64,
			ContainerScratchBytes: 32 << 20,
		},
	}
	arguments, err := (&Coordinator{}).containerCreateArguments(job, t.TempDir(), "owned-name", "0", "/bin/sh", "-c", "true")
	noErr(t, err)
	joined := " " + strings.Join(arguments, " ") + " "
	for _, required := range []string{
		" --pull=never ", " --log-driver none ", " --network none ", " --read-only ",
		" --cap-drop ALL ", " --security-opt no-new-privileges=true ", " --cpus 0.750 ",
		" --memory 268435456 ", " --memory-swap 268435456 ", " --pids-limit 64 ",
		" --tmpfs /tmp:rw,exec,nosuid,nodev,size=33554432 ",
		" --env HOME=/tmp ", " --env TMPDIR=/tmp ", " --env GOCACHE=/tmp/go-build ", " --env GOTMPDIR=/tmp ",
		" --label com.owngit.check-job=0123456789abcdef ",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing restriction %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, " /var/run/docker.sock ") {
		t.Fatalf("Docker socket was exposed in %q", joined)
	}
	if strings.Contains(joined, "noexec") {
		t.Fatalf("bounded scratch unexpectedly prevents temporary executables: %q", joined)
	}
}

func TestContainerImageInspectionRequiresConfiguredImmutableIdentity(t *testing.T) {
	digest := strings.Repeat("a", 64)
	bare := "sha256:" + digest
	repository := "example.invalid/checks@sha256:" + digest
	noErr(t, verifyContainerImageIdentity(bare, `{"id":"`+bare+`","repo_digests":[],"os":"linux","volumes":null}`))
	noErr(t, verifyContainerImageIdentity(repository, `{"id":"sha256:`+strings.Repeat("b", 64)+`","repo_digests":["normalized/checks@sha256:`+digest+`"],"os":"linux","volumes":{}}`))
	if err := verifyContainerImageIdentity(bare, `{"id":"sha256:`+strings.Repeat("b", 64)+`","repo_digests":[],"os":"linux","volumes":null}`); err == nil {
		t.Fatal("mismatched image ID was accepted")
	}
	if err := verifyContainerImageIdentity(repository, `{"id":"sha256:`+digest+`","repo_digests":[],"os":"linux","volumes":null}`); err == nil {
		t.Fatal("unreported repository digest was accepted")
	}
}

func TestContainerImageInspectionRejectsDeclaredOrMalformedVolumesBeforeCreate(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	for name, inspection := range map[string]string{
		"missing metadata":          `{"id":"` + image + `","repo_digests":[],"os":"linux"}`,
		"declared volume":           `{"id":"` + image + `","repo_digests":[],"os":"linux","volumes":{"/data":{}}}`,
		"malformed volume metadata": `{"id":"` + image + `","repo_digests":[],"os":"linux","volumes":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyContainerImageIdentity(image, inspection); err == nil {
				t.Fatalf("unsafe image inspection was accepted: %s", inspection)
			}
		})
	}
}

func TestContainerRuntimeInfoRequiresResourceEnforcement(t *testing.T) {
	complete := `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":true}`
	if id, err := validateContainerRuntimeInfo(complete); err != nil || id != "daemon" {
		t.Fatalf("runtime id=%q err=%v", id, err)
	}
	missingPIDs := `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":false}`
	if _, err := validateContainerRuntimeInfo(missingPIDs); err == nil {
		t.Fatal("runtime without PID enforcement was accepted")
	}
}

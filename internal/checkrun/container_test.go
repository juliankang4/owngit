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
	prepared := preparedContainer{imageID: "sha256:" + strings.Repeat("c", 64)}
	arguments, err := (&Coordinator{}).containerCreateArguments(job, prepared, t.TempDir(), "owned-name", "0", "/bin/sh", "-c", "true")
	noErr(t, err)
	joined := " " + strings.Join(arguments, " ") + " "
	if !strings.HasSuffix(joined, " --entrypoint /bin/sh "+prepared.imageID+" -c true ") {
		t.Fatalf("container does not run the resolved image: %q", joined)
	}
	for _, required := range []string{
		" --pull=never ", " --log-driver none ", " --network none ", " --read-only ",
		" --cap-drop ALL ", " --security-opt no-new-privileges=true ", " --cpus 0.750 ",
		" --memory 268435456 ", " --memory-swap 268435456 ", " --pids-limit 64 ",
		" --tmpfs /tmp:rw,exec,nosuid,nodev,size=33554432 ",
		" --env HOME=/tmp ", " --env TMPDIR=/tmp ", " --env GOCACHE=/tmp/go-build ", " --env GOTMPDIR=/tmp ",
		" --env HTTP_PROXY= ", " --env http_proxy= ", " --env HTTPS_PROXY= ", " --env https_proxy= ",
		" --env NO_PROXY= ", " --env no_proxy= ", " --env FTP_PROXY= ", " --env ftp_proxy= ",
		" --env ALL_PROXY= ", " --env all_proxy= ",
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
	bare := state.CheckExecutionSettings{ContainerImage: "sha256:" + digest}
	repository := state.CheckExecutionSettings{ContainerImage: "example.invalid/checks@sha256:" + digest}
	other := "sha256:" + strings.Repeat("b", 64)
	if id, _, err := verifyContainerImageIdentity(bare, `{"id":"`+bare.ContainerImage+`","repo_digests":[],"os":"linux","volumes":null}`); err != nil || id != bare.ContainerImage {
		t.Fatalf("image ID id=%q err=%v", id, err)
	}
	if id, _, err := verifyContainerImageIdentity(repository, `{"id":"`+other+`","repo_digests":["normalized/checks@sha256:`+digest+`"],"os":"linux","volumes":{}}`); err != nil || id != other {
		t.Fatalf("repository digest id=%q err=%v", id, err)
	}
	if _, _, err := verifyContainerImageIdentity(bare, `{"id":"`+other+`","repo_digests":[],"os":"linux","volumes":null}`); err == nil {
		t.Fatal("mismatched image ID was accepted")
	}
	if _, _, err := verifyContainerImageIdentity(repository, `{"id":"sha256:`+digest+`","repo_digests":[],"os":"linux","volumes":null}`); err == nil {
		t.Fatal("unreported repository digest was accepted")
	}
}

// A tag is accepted only when the policy allows tags, and resolves to the
// image ID that every container of the job then runs.
func TestContainerImageTagResolvesToTheInspectedID(t *testing.T) {
	id := "sha256:" + strings.Repeat("d", 64)
	inspection := `{"id":"` + id + `","repo_digests":[],"os":"linux","volumes":null}`
	tagged := state.CheckExecutionSettings{ContainerImage: "example.invalid/checks:1"}
	if _, _, err := verifyContainerImageIdentity(tagged, inspection); err == nil {
		t.Fatal("a tag was accepted without allowing tags")
	}
	tagged.ContainerAllowTags = true
	resolved, _, err := verifyContainerImageIdentity(tagged, inspection)
	if err != nil || resolved != id {
		t.Fatalf("tag resolved to %q err=%v", resolved, err)
	}
	if _, _, err := verifyContainerImageIdentity(tagged, `{"id":"latest","repo_digests":[],"os":"linux","volumes":null}`); err == nil {
		t.Fatal("a tag resolved to something other than an image ID")
	}
	header := preparedContainer{imageID: resolved, unenforced: []string{"swap"}}.header(tagged.ContainerImage)
	if !strings.Contains(header, "Container image: "+id+" (from example.invalid/checks:1)") || !strings.Contains(header, "does not enforce: swap") {
		t.Fatalf("header=%q", header)
	}
	if header := (preparedContainer{imageID: id}).header(id); header != "" {
		t.Fatalf("an exact image with every limit enforced has a header %q", header)
	}
}

func TestContainerImageInspectionRejectsDeclaredOrMalformedVolumesBeforeCreate(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	settings := state.CheckExecutionSettings{ContainerImage: image}
	for name, inspection := range map[string]string{
		"missing metadata":          `{"id":"` + image + `","repo_digests":[],"os":"linux"}`,
		"declared volume":           `{"id":"` + image + `","repo_digests":[],"os":"linux","volumes":{"/data":{}}}`,
		"malformed volume metadata": `{"id":"` + image + `","repo_digests":[],"os":"linux","volumes":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := verifyContainerImageIdentity(settings, inspection); err == nil {
				t.Fatalf("unsafe image inspection was accepted: %s", inspection)
			}
		})
	}
}

// With image volumes allowed, each declared path gets its own bounded
// in-memory mount, and paths that would touch what the container runs on
// keep the image refused.
func TestImageVolumesGetDisposableMounts(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	settings := state.CheckExecutionSettings{ContainerImage: image, ContainerImageVolumes: true, ContainerScratchBytes: 1 << 20, ContainerNetwork: "none"}
	_, volumes, err := verifyContainerImageIdentity(settings, `{"id":"`+image+`","repo_digests":[],"os":"linux","volumes":{"/var/lib/data":{},"/cache":{}}}`)
	noErr(t, err)
	if strings.Join(volumes, " ") != "/cache /var/lib/data" {
		t.Fatalf("volumes=%v", volumes)
	}
	arguments, err := (&Coordinator{}).containerCreateArguments(state.CheckJob{ID: "job", Execution: settings},
		preparedContainer{imageID: image, volumes: volumes}, t.TempDir(), "name", "0", "/bin/true")
	noErr(t, err)
	joined := " " + strings.Join(arguments, " ") + " "
	for _, volume := range volumes {
		if !strings.Contains(joined, " --tmpfs "+volume+":rw,exec,nosuid,nodev,size=1048576 ") {
			t.Fatalf("volume %s has no bounded mount in %q", volume, joined)
		}
	}
	for _, volume := range []string{"/workspace", "/workspace/out", "/tmp", "/", "/etc", "/dev/shm", "/data/../etc", "/a,b", "/a:b", "relative"} {
		inspection := `{"id":"` + image + `","repo_digests":[],"os":"linux","volumes":{"` + volume + `":{}}}`
		if _, _, err := verifyContainerImageIdentity(settings, inspection); err == nil {
			t.Errorf("volume %q was accepted", volume)
		}
	}
}

func TestWritableRootDropsOnlyTheReadOnlyRoot(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	settings := state.CheckExecutionSettings{ContainerImage: image, ContainerWritableRoot: true, ContainerNetwork: "checks-net",
		ContainerCPUMillis: 1000, ContainerMemoryBytes: 64 << 20, ContainerPIDs: 16, ContainerScratchBytes: 1 << 20}
	arguments, err := (&Coordinator{}).containerCreateArguments(state.CheckJob{ID: "job", Execution: settings},
		preparedContainer{imageID: image}, t.TempDir(), "name", "0", "/bin/true")
	noErr(t, err)
	joined := " " + strings.Join(arguments, " ") + " "
	if strings.Contains(joined, " --read-only ") {
		t.Fatalf("writable root still read-only: %q", joined)
	}
	for _, required := range []string{" --network checks-net ", " --cap-drop ALL ", " --security-opt no-new-privileges=true ", " --pull=never ", " --user "} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %q", required, joined)
		}
	}
}

// Each resource limit Docker does not enforce refuses the run unless the
// policy names that limit; the accepted ones are reported.
func TestContainerRuntimeInfoAcceptsOnlyNamedMissingLimits(t *testing.T) {
	complete := `{"id":"daemon","memory_limit":true,"swap_limit":true,"cpu_cfs_period":true,"cpu_cfs_quota":true,"pids_limit":true}`
	if id, unenforced, err := validateContainerRuntimeInfo(complete, nil); err != nil || id != "daemon" || len(unenforced) != 0 {
		t.Fatalf("runtime id=%q unenforced=%v err=%v", id, unenforced, err)
	}
	noSwap := `{"id":"daemon","memory_limit":true,"swap_limit":false,"cpu_cfs_period":true,"cpu_cfs_quota":false,"pids_limit":true}`
	if _, _, err := validateContainerRuntimeInfo(noSwap, nil); err == nil || !strings.Contains(err.Error(), "swap, cpu") {
		t.Fatalf("missing limits error=%v", err)
	}
	if _, _, err := validateContainerRuntimeInfo(noSwap, []string{"swap"}); err == nil || !strings.Contains(err.Error(), "cpu") || strings.Contains(err.Error(), "swap") {
		t.Fatalf("partly accepted limits error=%v", err)
	}
	if _, unenforced, err := validateContainerRuntimeInfo(noSwap, []string{"cpu", "pids", "swap"}); err != nil || strings.Join(unenforced, ",") != "swap,cpu" {
		t.Fatalf("accepted limits unenforced=%v err=%v", unenforced, err)
	}
}

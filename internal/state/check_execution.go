package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	RunnerRoleServer   = "server"
	RunnerRoleExternal = "external_runner"

	ContainerNetworkNone   = "none"
	ContainerNetworkBridge = "bridge"
	// ContainerNetworkHost is Docker's host network. It is never accepted:
	// it would give repository code the host's loopback services, including
	// OwnGit itself.
	ContainerNetworkHost = "host"

	// Resource limits a policy may accept as not enforced.
	ContainerLimitMemory = "memory"
	ContainerLimitSwap   = "swap"
	ContainerLimitCPU    = "cpu"
	ContainerLimitPIDs   = "pids"

	defaultSourceMaxEntries    = 20000
	defaultSourceMaxFileBytes  = int64(64 << 20)
	defaultSourceMaxTotalBytes = int64(256 << 20)
	defaultSourceMaxPathDepth  = 64
	defaultSourceMaxPathBytes  = 1024
	defaultSourceMaxNameBytes  = 255
	defaultSourceMetadataBytes = int64(16 << 20)
)

// CheckSourceLimits are the operator-owned bounds for one exact source
// snapshot. They intentionally mirror checksource.Limits without introducing a
// state -> repository import cycle.
type CheckSourceLimits struct {
	MaxEntries    int   `json:"max_entries"`
	MaxFileBytes  int64 `json:"max_file_bytes"`
	MaxTotalBytes int64 `json:"max_total_bytes"`
	MaxPathDepth  int   `json:"max_path_depth"`
	MaxPathBytes  int   `json:"max_path_bytes"`
	MaxNameBytes  int   `json:"max_name_bytes"`
	MetadataLimit int64 `json:"metadata_limit_bytes"`
}

// CheckExecutionSettings are effective operator settings captured by policy
// and copied into every admitted job. Repository workflow bytes cannot change
// any of these fields.
type CheckExecutionSettings struct {
	Source CheckSourceLimits `json:"source"`

	ContainerImage        string `json:"container_image,omitempty"`
	ContainerRuntime      string `json:"container_runtime,omitempty"`
	ContainerNetwork      string `json:"container_network,omitempty"`
	ContainerCPUMillis    int64  `json:"container_cpu_millis,omitempty"`
	ContainerMemoryBytes  int64  `json:"container_memory_bytes,omitempty"`
	ContainerPIDs         int64  `json:"container_pids,omitempty"`
	ContainerScratchBytes int64  `json:"container_scratch_bytes,omitempty"`

	// The container options below are each off when absent, which is the
	// behavior of a policy that predates them. Each one is the owner's
	// explicit choice, shown with its own warning, and part of the policy
	// digest, so changing one withdraws consent.

	// ContainerAllowTags accepts an image named by a tag. Each job resolves
	// the tag once to an image ID and runs every command from that ID.
	ContainerAllowTags bool `json:"container_allow_tags,omitempty"`
	// ContainerPullMissing downloads a missing image before the job, with no
	// stored registry credentials.
	ContainerPullMissing bool `json:"container_pull_missing,omitempty"`
	// ContainerMissingEnforcement lists the resource limits the owner accepts
	// this computer's Docker may not enforce. It is sorted and each entry is
	// one of ContainerLimitMemory, ContainerLimitSwap, ContainerLimitCPU and
	// ContainerLimitPIDs. A missing limit that is not listed still refuses.
	ContainerMissingEnforcement []string `json:"container_missing_enforcement,omitempty"`
	// ContainerImageVolumes gives each volume the image declares a disposable
	// in-memory mount instead of refusing the image.
	ContainerImageVolumes bool `json:"container_image_volumes,omitempty"`
	// ContainerWritableRoot lets commands change the container's own files.
	// They are discarded with the container.
	ContainerWritableRoot bool `json:"container_writable_root,omitempty"`

	// Legacy marks a schema 9 policy or job whose execution conditions were not
	// recorded. It remains portable history but cannot receive fresh consent or
	// execution until the operator stores a complete current policy.
	Legacy bool `json:"legacy,omitempty"`
}

// HasContainerOptions reports whether any container option beyond an
// immutable image on the none or bridge network is chosen. Backups holding
// one need format 11.
func (settings CheckExecutionSettings) HasContainerOptions() bool {
	return settings.ContainerAllowTags || settings.ContainerPullMissing || len(settings.ContainerMissingEnforcement) != 0 ||
		settings.ContainerImageVolumes || settings.ContainerWritableRoot ||
		(settings.ContainerNetwork != "" && settings.ContainerNetwork != ContainerNetworkNone && settings.ContainerNetwork != ContainerNetworkBridge)
}

// DefaultCheckSourceLimits returns the bounded source snapshot defaults used
// when the operator leaves individual source limits unset.
func DefaultCheckSourceLimits() CheckSourceLimits {
	return defaultCheckSourceLimits()
}

// CheckContainerLimits are the container resource settings a policy can
// leave unset.
type CheckContainerLimits struct {
	CPUMillis    int64
	MemoryBytes  int64
	PIDs         int64
	ScratchBytes int64
}

// DefaultCheckContainerLimits returns the container resources used when the
// operator leaves individual container limits unset.
func DefaultCheckContainerLimits() CheckContainerLimits {
	return CheckContainerLimits{CPUMillis: 1000, MemoryBytes: 512 << 20, PIDs: 256, ScratchBytes: 512 << 20}
}

func defaultCheckSourceLimits() CheckSourceLimits {
	return CheckSourceLimits{
		MaxEntries: defaultSourceMaxEntries, MaxFileBytes: defaultSourceMaxFileBytes,
		MaxTotalBytes: defaultSourceMaxTotalBytes, MaxPathDepth: defaultSourceMaxPathDepth,
		MaxPathBytes: defaultSourceMaxPathBytes, MaxNameBytes: defaultSourceMaxNameBytes,
		MetadataLimit: defaultSourceMetadataBytes,
	}
}

func normalizeCheckExecutionSettings(executor string, settings CheckExecutionSettings) (CheckExecutionSettings, error) {
	if settings.Legacy {
		return CheckExecutionSettings{}, errors.New("legacy execution settings cannot be selected")
	}
	if len(settings.ContainerMissingEnforcement) == 0 {
		settings.ContainerMissingEnforcement = nil
	}
	defaults := defaultCheckSourceLimits()
	if settings.Source.MaxEntries == 0 {
		settings.Source.MaxEntries = defaults.MaxEntries
	}
	if settings.Source.MaxFileBytes == 0 {
		settings.Source.MaxFileBytes = defaults.MaxFileBytes
	}
	if settings.Source.MaxTotalBytes == 0 {
		settings.Source.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if settings.Source.MaxPathDepth == 0 {
		settings.Source.MaxPathDepth = defaults.MaxPathDepth
	}
	if settings.Source.MaxPathBytes == 0 {
		settings.Source.MaxPathBytes = defaults.MaxPathBytes
	}
	if settings.Source.MaxNameBytes == 0 {
		settings.Source.MaxNameBytes = defaults.MaxNameBytes
	}
	if settings.Source.MetadataLimit == 0 {
		settings.Source.MetadataLimit = defaults.MetadataLimit
	}
	if err := validateCheckSourceLimits(settings.Source); err != nil {
		return CheckExecutionSettings{}, err
	}

	switch executor {
	case CheckExecutorHost, CheckExecutorExternalRunner:
		// These values may be perfectly valid; they simply do not apply to the
		// selected executor. Naming the offending field lets a caller say so
		// rather than implying the value itself is wrong.
		for _, supplied := range []struct {
			field string
			set   bool
		}{
			{FieldContainerImage, settings.ContainerImage != ""},
			{FieldContainerRuntime, settings.ContainerRuntime != ""},
			{FieldContainerNetwork, settings.ContainerNetwork != ""},
			{FieldContainerCPUMillis, settings.ContainerCPUMillis != 0},
			{FieldContainerMemoryBytes, settings.ContainerMemoryBytes != 0},
			{FieldContainerPIDs, settings.ContainerPIDs != 0},
			{FieldContainerScratchBytes, settings.ContainerScratchBytes != 0},
			{FieldContainerAllowTags, settings.ContainerAllowTags},
			{FieldContainerPullMissing, settings.ContainerPullMissing},
			{FieldContainerMissingEnforcement, len(settings.ContainerMissingEnforcement) != 0},
			{FieldContainerImageVolumes, settings.ContainerImageVolumes},
			{FieldContainerWritableRoot, settings.ContainerWritableRoot},
		} {
			if supplied.set {
				return CheckExecutionSettings{}, notApplicableError(supplied.field)
			}
		}
	case CheckExecutorContainer:
		switch {
		case settings.ContainerImage == "":
			return CheckExecutionSettings{}, &CheckPolicyFieldError{Field: FieldContainerImage, Rule: RuleRequired}
		case validImmutableContainerImage(settings.ContainerImage):
		case settings.ContainerAllowTags && validContainerImageReference(settings.ContainerImage):
		default:
			return CheckExecutionSettings{}, &CheckPolicyFieldError{Field: FieldContainerImage, Rule: RuleFormat}
		}
		// A bare image ID names no repository to download from.
		if settings.ContainerPullMissing && strings.HasPrefix(settings.ContainerImage, "sha256:") {
			return CheckExecutionSettings{}, &CheckPolicyFieldError{Field: FieldContainerPullMissing, Rule: RuleNeedsRepository}
		}
		if settings.ContainerRuntime == "" {
			settings.ContainerRuntime = "docker-local"
		}
		if settings.ContainerRuntime != "docker-local" {
			return CheckExecutionSettings{}, unknownValueError(FieldContainerRuntime, settings.ContainerRuntime)
		}
		if settings.ContainerNetwork == "" {
			settings.ContainerNetwork = ContainerNetworkNone
		}
		if err := validateContainerNetwork(settings.ContainerNetwork); err != nil {
			return CheckExecutionSettings{}, err
		}
		missing, err := normalizeContainerLimitNames(settings.ContainerMissingEnforcement)
		if err != nil {
			return CheckExecutionSettings{}, err
		}
		settings.ContainerMissingEnforcement = missing
		containerDefaults := DefaultCheckContainerLimits()
		if settings.ContainerCPUMillis == 0 {
			settings.ContainerCPUMillis = containerDefaults.CPUMillis
		}
		if settings.ContainerMemoryBytes == 0 {
			settings.ContainerMemoryBytes = containerDefaults.MemoryBytes
		}
		if settings.ContainerPIDs == 0 {
			settings.ContainerPIDs = containerDefaults.PIDs
		}
		if settings.ContainerScratchBytes == 0 {
			settings.ContainerScratchBytes = containerDefaults.ScratchBytes
		}
		// The same four bounds as before, now read from the published table
		// and reported one field at a time.
		for _, field := range []struct {
			name  string
			value int64
		}{
			{FieldContainerCPUMillis, settings.ContainerCPUMillis},
			{FieldContainerMemoryBytes, settings.ContainerMemoryBytes},
			{FieldContainerPIDs, settings.ContainerPIDs},
			{FieldContainerScratchBytes, settings.ContainerScratchBytes},
		} {
			bounds, _ := CheckPolicyBoundsFor(field.name)
			if field.value < bounds.Min || field.value > bounds.Max {
				return CheckExecutionSettings{}, boundedRangeError(field.name, field.value)
			}
		}
	default:
		return CheckExecutionSettings{}, unknownValueError(FieldExecutor, executor)
	}
	return settings, nil
}

// validateCheckSourceLimits enforces the source snapshot bounds.
//
// The bounds are exactly the ones this function has always enforced. What
// changed is that each is reported against the field it belongs to, so a
// caller can say which box is wrong instead of refusing the whole group.
// MaxTotalBytes is checked against MaxFileBytes because a total below the
// per-file allowance could never be satisfied.
func validateCheckSourceLimits(limits CheckSourceLimits) error {
	// Fields with a fixed range are checked against the published table, so
	// the range an interface shows is the range enforced here.
	for _, field := range []struct {
		name  string
		value int64
	}{
		{FieldSourceMaxEntries, int64(limits.MaxEntries)},
		{FieldSourceMaxFileBytes, limits.MaxFileBytes},
		{FieldSourceMaxPathDepth, int64(limits.MaxPathDepth)},
		{FieldSourceMaxPathBytes, int64(limits.MaxPathBytes)},
		{FieldSourceMaxNameBytes, int64(limits.MaxNameBytes)},
		{FieldSourceMetadataLimit, limits.MetadataLimit},
	} {
		bounds, _ := CheckPolicyBoundsFor(field.name)
		if field.value < bounds.Min || field.value > bounds.Max {
			return boundedRangeError(field.name, field.value)
		}
	}
	// The total has a moving floor and a fixed ceiling. The floor is the
	// operator's own file limit, named by the descriptor rather than assumed
	// here; the ceiling comes from the same published table as every other
	// bound, so the maximum this refuses is the maximum an interface shows.
	total, _ := CheckPolicyBoundsFor(FieldSourceMaxTotalBytes)
	if limits.MaxTotalBytes < limits.MaxFileBytes || limits.MaxTotalBytes > total.Max {
		return rangeError(FieldSourceMaxTotalBytes, limits.MaxTotalBytes, limits.MaxFileBytes, total.Max)
	}
	return nil
}

func validImmutableContainerImage(value string) bool {
	if value == "" || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	digestStart := 0
	if strings.HasPrefix(value, "sha256:") {
		digestStart = len("sha256:")
		if digestStart+64 != len(value) {
			return false
		}
	} else {
		marker := strings.LastIndex(value, "@sha256:")
		if marker <= 0 || marker+len("@sha256:")+64 != len(value) {
			return false
		}
		digestStart = marker + len("@sha256:")
	}
	for _, character := range value[digestStart:] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// validContainerImageReference accepts a Docker image reference: an optional
// registry host, a lowercase repository path, and an optional tag or digest.
// It never begins with a dash, so it cannot be read as a Docker option.
func validContainerImageReference(value string) bool {
	return len(value) <= 512 && containerImageReference.MatchString(value)
}

var containerImageReference = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*(?::[0-9]+)?/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[0-9a-f]{64})?$`)

// ContainerImageRegistry names the registry an image reference downloads
// from, so a warning can say where OwnGit will connect.
func ContainerImageRegistry(image string) string {
	name := image
	if marker := strings.Index(name, "@"); marker >= 0 {
		name = name[:marker]
	}
	first, _, nested := strings.Cut(name, "/")
	if nested && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}

// validateContainerNetwork accepts none, bridge, or the name of a Docker
// network the owner created. Host networking is refused outright, and
// Docker's other reserved spellings are not names.
func validateContainerNetwork(network string) error {
	switch {
	case network == ContainerNetworkNone || network == ContainerNetworkBridge:
		return nil
	case network == ContainerNetworkHost:
		return &CheckPolicyFieldError{Field: FieldContainerNetwork, Rule: RuleForbidden, Value: network}
	case network == "default" || !containerNetworkName.MatchString(network):
		return unknownValueError(FieldContainerNetwork, network)
	}
	return nil
}

// Docker's own network name rule, bounded. A colon never appears, so
// "container:NAME" cannot be spelled.
var containerNetworkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func normalizeContainerLimitNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	sorted := make([]string, 0, len(names))
	for _, name := range names {
		switch name {
		case ContainerLimitMemory, ContainerLimitSwap, ContainerLimitCPU, ContainerLimitPIDs:
		default:
			return nil, unknownValueError(FieldContainerMissingEnforcement, name)
		}
		if slices.Contains(sorted, name) {
			return nil, &CheckPolicyFieldError{Field: FieldContainerMissingEnforcement, Rule: RuleDuplicate, Value: name}
		}
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	return sorted, nil
}

func checkExecutionSettingsJSON(settings CheckExecutionSettings) (string, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	if len(encoded) > 4096 {
		return "", fmt.Errorf("execution settings exceed their storage bound")
	}
	return string(encoded), nil
}

func validRunnerRole(role string) bool {
	return role == RunnerRoleServer || role == RunnerRoleExternal
}

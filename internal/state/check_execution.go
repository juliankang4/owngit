package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RunnerRoleServer   = "server"
	RunnerRoleExternal = "external_runner"

	ContainerNetworkNone   = "none"
	ContainerNetworkBridge = "bridge"

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

	// Legacy marks a schema 9 policy or job whose execution conditions were not
	// recorded. It remains portable history but cannot receive fresh consent or
	// execution until the operator stores a complete current policy.
	Legacy bool `json:"legacy,omitempty"`
}

// DefaultCheckSourceLimits returns the bounded source snapshot defaults used
// when the operator leaves individual source limits unset.
func DefaultCheckSourceLimits() CheckSourceLimits {
	return defaultCheckSourceLimits()
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
		} {
			if supplied.set {
				return CheckExecutionSettings{}, notApplicableError(supplied.field)
			}
		}
	case CheckExecutorContainer:
		if !validImmutableContainerImage(settings.ContainerImage) {
			if settings.ContainerImage == "" {
				return CheckExecutionSettings{}, &CheckPolicyFieldError{Field: FieldContainerImage, Rule: RuleRequired}
			}
			return CheckExecutionSettings{}, &CheckPolicyFieldError{Field: FieldContainerImage, Rule: RuleFormat}
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
		if settings.ContainerNetwork != ContainerNetworkNone && settings.ContainerNetwork != ContainerNetworkBridge {
			return CheckExecutionSettings{}, unknownValueError(FieldContainerNetwork, settings.ContainerNetwork)
		}
		if settings.ContainerCPUMillis == 0 {
			settings.ContainerCPUMillis = 1000
		}
		if settings.ContainerMemoryBytes == 0 {
			settings.ContainerMemoryBytes = 512 << 20
		}
		if settings.ContainerPIDs == 0 {
			settings.ContainerPIDs = 256
		}
		if settings.ContainerScratchBytes == 0 {
			settings.ContainerScratchBytes = 512 << 20
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

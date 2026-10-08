package actions

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxEnvironmentNames = 200
	maxEnvironmentValue = 48 << 10
	maxEnvironmentBytes = 256 << 10
)

// Evaluator expands templates and evaluates scalar expressions for the given
// workflow key. For step.if it evaluates the condition without implicit success.
// It must enforce the expression bounds and per-key context rules. The extra
// success, failure and cancelled booleans supply the job's status functions.
type Evaluator func(key, text string, contexts map[string]any) (any, error)

func evaluate(evaluator Evaluator, key, text string, contexts map[string]any) (any, error) {
	if evaluator != nil {
		return evaluator(key, text, contexts)
	}
	if strings.Contains(text, "${{") {
		return nil, fmt.Errorf("workflow.expression: no evaluator for %s", key)
	}
	return text, nil
}

func evaluateText(evaluator Evaluator, key, text string, contexts map[string]any) (string, error) {
	value, err := evaluate(evaluator, key, text, contexts)
	if err != nil {
		return "", err
	}
	switch value := value.(type) {
	case string:
		if len(value) > 64<<10 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("workflow.limit: invalid or oversized %s", key)
		}
		return value, nil
	case nil:
		return "", nil
	case bool:
		return strconv.FormatBool(value), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case int:
		return strconv.Itoa(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	default:
		return "", fmt.Errorf("workflow.expression: %s must be scalar", key)
	}
}

func environmentMap(entries []string, windows bool) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			setEnvironment(values, name, value, windows)
		}
	}
	return values
}

func setEnvironment(values map[string]string, name, value string, windows bool) {
	if windows {
		for existing := range values {
			if strings.EqualFold(existing, name) {
				delete(values, existing)
			}
		}
	}
	values[name] = value
}

func environmentValue(values map[string]string, name string, windows bool) string {
	for key, value := range values {
		if key == name || windows && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func environmentEntries(values map[string]string, windows bool) ([]string, error) {
	if len(values) > maxEnvironmentNames {
		return nil, fmt.Errorf("workflow.limit: environment exceeds %d variables", maxEnvironmentNames)
	}
	keys := make([]string, 0, len(values))
	total := 0
	for name, value := range values {
		characters := 0
		for _, char := range value {
			characters++
			if char > 0xffff {
				characters++
			}
		}
		if len(value) > maxEnvironmentValue || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || windows && characters > 32767 {
			return nil, fmt.Errorf("workflow.limit: invalid or oversized environment variable %s", name)
		}
		total += len(name) + len(value) + 2
		keys = append(keys, name)
	}
	if total > maxEnvironmentBytes {
		return nil, fmt.Errorf("workflow.limit: environment exceeds %d bytes", maxEnvironmentBytes)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, name := range keys {
		entries = append(entries, name+"="+values[name])
	}
	return entries, nil
}

func validEnvironmentName(name string) bool {
	for index, char := range name {
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return name != ""
}

func applyEnvironment(values map[string]string, layer map[string]string, evaluator Evaluator, key string, contexts map[string]any, windows bool) error {
	keys := make([]string, 0, len(layer))
	for name := range layer {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if !validEnvironmentName(name) {
			return fmt.Errorf("workflow.environment: invalid variable %s", name)
		}
		value, err := evaluateText(evaluator, key, layer[name], contexts)
		if err != nil {
			return err
		}
		setEnvironment(values, name, value, windows)
	}
	return nil
}

func runtimeContexts(plan JobPlan, identity RunIdentity, workspace, temporary, osName, arch, name string) (map[string]any, error) {
	github := plan.Context.GitHub
	github.RunID, github.RunNumber, github.RunAttempt = identity.ID, identity.Number, identity.Attempt
	github.Job = plan.JobKey
	encoded, err := json.Marshal(github)
	if err != nil {
		return nil, fmt.Errorf("workflow.context: %w", err)
	}
	var githubValues map[string]any
	if err := json.Unmarshal(encoded, &githubValues); err != nil {
		return nil, err
	}
	githubValues["workspace"] = workspace
	needs := make(map[string]any, len(plan.Context.Needs))
	for key, value := range plan.Context.Needs {
		needs[key] = map[string]any{"result": value}
	}
	return map[string]any{
		"github": githubValues, "inputs": plan.Context.Inputs, "matrix": plan.Context.Matrix,
		"needs": needs, "strategy": map[string]any{"job-index": plan.Context.Strategy.JobIndex, "job-total": plan.Context.Strategy.JobTotal},
		"runner": map[string]any{"os": osName, "arch": arch, "temp": temporary, "name": name},
		"job":    map[string]any{"status": ResultSuccess}, "steps": map[string]any{},
	}, nil
}

func actionsEnvironment(plan JobPlan, identity RunIdentity, workspace, temporary, event, osName, arch string) map[string]string {
	github := plan.Context.GitHub
	work := filepath.Dir(temporary)
	values := map[string]string{
		"CI": "true", "GITHUB_ACTIONS": "true", "GITHUB_WORKSPACE": workspace,
		"GITHUB_SHA": github.SHA, "GITHUB_REF": github.Ref, "GITHUB_REF_NAME": github.RefName,
		"GITHUB_REF_TYPE": github.RefType, "GITHUB_HEAD_REF": github.HeadRef, "GITHUB_BASE_REF": github.BaseRef,
		"GITHUB_EVENT_NAME": github.EventName, "GITHUB_EVENT_PATH": event, "GITHUB_REPOSITORY": github.Repository,
		"GITHUB_RUN_ID": identity.ID, "GITHUB_RUN_NUMBER": strconv.FormatInt(identity.Number, 10),
		"GITHUB_RUN_ATTEMPT": strconv.FormatInt(identity.Attempt, 10), "GITHUB_JOB": plan.JobKey,
		"GITHUB_WORKFLOW": github.Workflow, "RUNNER_OS": osName, "RUNNER_ARCH": arch, "RUNNER_TEMP": temporary,
		"HOME": filepath.Join(work, "home"), "XDG_CACHE_HOME": filepath.Join(work, "cache"),
		"GOCACHE": filepath.Join(work, "cache", "go-build"), "GOTMPDIR": temporary,
		"TMPDIR": temporary, "TEMP": temporary, "TMP": temporary,
	}
	if osName == "Windows" {
		values["USERPROFILE"] = values["HOME"]
	}
	return values
}

func runnerPlatform() (string, string) {
	osName := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows"}[runtime.GOOS]
	arch := map[string]string{"amd64": "X64", "arm64": "ARM64", "386": "X86", "arm": "ARM"}[runtime.GOARCH]
	if arch == "" {
		arch = strings.ToUpper(runtime.GOARCH)
	}
	return osName, arch
}

func prependPaths(values map[string]string, paths []string, windows bool) {
	if len(paths) == 0 {
		return
	}
	parts := make([]string, 0, len(paths)+1)
	for index := len(paths) - 1; index >= 0; index-- {
		parts = append(parts, paths[index])
	}
	parts = append(parts, environmentValue(values, "PATH", windows))
	separator := ":"
	if windows {
		separator = ";"
	}
	setEnvironment(values, "PATH", strings.Join(parts, separator), windows)
}

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"owngit/internal/actions"
)

// Executor provides a policy-prepared container and its execution boundary.
type Executor interface {
	Metadata() (environment []string, architecture, description string)
	Limits() (time.Duration, int64)
	RunScript(context.Context, actions.Script, ...string) actions.ScriptResult
	CheckInterpreter(context.Context, actions.Script, ...string) actions.ScriptResult
}

func missingExecutor(executor Executor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// RunJob executes with a container prepared before the start grant. The caller
// owns the job envelope and removes it after the engine and adapter return.
func RunJob(ctx context.Context, plan actions.JobPlan, options actions.RunOptions, executor Executor) actions.JobResult {
	if missingExecutor(executor) {
		return actions.JobResult{Status: actions.StatusError, Error: "workflow.executor: prepared container is required"}
	}
	environment, architecture, description := executor.Metadata()
	options.RunnerOS, options.RunnerArch = "Linux", architecture
	if options.BaseEnvironment == nil && options.Environment == nil {
		base := slices.Clone(environment)
		imagePath, pathIndex := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", -1
		for index, entry := range base {
			if value, found := strings.CutPrefix(entry, "PATH="); found {
				imagePath, pathIndex = value, index
			}
		}
		entry := "PATH=/owngit/work/home/.local/bin:" + imagePath
		if pathIndex >= 0 {
			base[pathIndex] = entry
		} else {
			base = append(base, entry)
		}
		options.BaseEnvironment = base
	}
	timeout, outputLimit := executor.Limits()
	if options.MaxTimeout <= 0 || options.MaxTimeout > timeout {
		options.MaxTimeout = timeout
	}
	if options.OutputLimit <= 0 || options.OutputLimit > outputLimit {
		options.OutputLimit = outputLimit
	}
	if options.Evaluator != nil {
		options.Evaluator = containerEvaluator(options)
	}
	if options.RunScript == nil {
		options.RunScript = Runner(executor)
	}
	if description != "" {
		plan.Notes = append(slices.Clone(plan.Notes), actions.Message{Code: "note.container", Detail: description})
	}
	return actions.RunJob(ctx, plan, options)
}

type interpreterLookup struct {
	name, path, directory string
}

func Runner(executor Executor) actions.ScriptRunner {
	availability := make(map[interpreterLookup]bool)
	return func(ctx context.Context, script actions.Script) actions.ScriptResult {
		fail := func(err error) actions.ScriptResult {
			return actions.ScriptResult{Status: actions.StatusError, Output: err.Error()}
		}
		if missingExecutor(executor) || script.ActionsRoot == nil {
			return fail(fmt.Errorf("workflow.executor: prepared container and actions root are required"))
		}
		command, implicit, err := shellCommand(script)
		if err != nil {
			result := fail(err)
			if errors.Is(err, exec.ErrNotFound) {
				result.Status = actions.StatusUnavailable
			}
			return result
		}
		lookup := interpreterLookup{directory: scriptPaths(script).translate(script.Directory)}
		for _, entry := range script.Environment {
			if value, found := strings.CutPrefix(entry, "PATH="); found {
				lookup.path = value
			}
		}
		interpreters := []string{command[0]}
		if implicit {
			interpreters = []string{"bash", "sh"}
		}
		found := false
		for _, interpreter := range interpreters {
			lookup.name = interpreter
			if strings.ContainsRune(lookup.name+lookup.path+lookup.directory, 0) {
				return fail(fmt.Errorf("workflow.shell: NUL is not supported"))
			}
			available := availability[lookup]
			if !available {
				payload := "export PATH=" + shellQuote(lookup.path) + "\ncd " + shellQuote(lookup.directory) + " || exit 126\ncommand -v " + shellQuote(lookup.name) + " >/dev/null 2>&1 || exit 127\n"
				checked := runPayload(ctx, executor, script, payload, true)
				switch checked.Status {
				case actions.StatusPassed:
					available = true
					availability[lookup] = true
				case actions.StatusUnavailable:
					if checked.ExitCode == nil || *checked.ExitCode != 127 || checked.CleanupError != "" {
						return checked
					}
				default:
					return checked
				}
			}
			if available {
				if implicit {
					command = append([]string{interpreter, "-e"}, command...)
				}
				found = true
				break
			}
		}
		if !found {
			exitCode := 127
			return actions.ScriptResult{Status: actions.StatusUnavailable, ExitCode: &exitCode, Output: "workflow.shell_unavailable: " + lookup.name}
		}
		payload, err := stepEnvironment(script, command)
		if err != nil {
			return fail(err)
		}
		result := runPayload(ctx, executor, script, payload, false)
		if result.Status == actions.StatusUnavailable && result.ExitCode != nil {
			result.Status = actions.StatusFailed
		}
		return result
	}
}

func runPayload(ctx context.Context, executor Executor, script actions.Script, payload string, lookup bool) (result actions.ScriptResult) {
	result.Status = actions.StatusError
	if missingExecutor(executor) || script.ActionsRoot == nil {
		result.Output = "workflow.executor: prepared container and actions root are required"
		return result
	}
	remove := func(relative string) {
		if err := script.ActionsRoot.Remove(relative); err != nil {
			result.Status = actions.StatusError
			result.CleanupError = strings.TrimSpace(result.CleanupError + "\nremove private step payload: " + err.Error())
		}
	}
	name := strings.TrimSuffix(filepath.Base(script.Path), ".script")
	directory := "scripts"
	if lookup {
		name += ".lookup"
		directory = filepath.Join(directory, name)
		if err := script.ActionsRoot.Mkdir(directory, 0o700); err != nil {
			result.Output = err.Error()
			return result
		}
		defer remove(directory)
	}
	name += ".env"
	relative := filepath.Join(directory, name)
	file, err := script.ActionsRoot.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		result.Output = err.Error()
		return result
	}
	defer remove(relative)
	_, err = file.WriteString(payload)
	if err = errors.Join(err, file.Close()); err != nil {
		result.Output = err.Error()
		return result
	}
	launcher := ". /owngit/scripts/" + name
	if lookup {
		return executor.CheckInterpreter(ctx, script, "/bin/sh", "-c", launcher, "sh")
	}
	return executor.RunScript(ctx, script, "/bin/sh", "-c", launcher+` && exec "$@"`, "sh")
}

func stepEnvironment(script actions.Script, command []string) (string, error) {
	var payload strings.Builder
	paths := scriptPaths(script)
	for _, entry := range script.Environment {
		name, value, found := strings.Cut(entry, "=")
		if !found || !environmentName(name) || strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("workflow.environment: invalid step environment")
		}
		payload.WriteString("export " + name + "=" + shellQuote(paths.environmentValue(name, value)) + "\n")
	}
	payload.WriteString("cd " + shellQuote(paths.translate(script.Directory)) + " || exit 126\nset --")
	for _, word := range command {
		if strings.ContainsRune(word, 0) {
			return "", fmt.Errorf("workflow.shell: NUL is not supported")
		}
		payload.WriteString(" " + shellQuote(word))
	}
	payload.WriteByte('\n')
	return payload.String(), nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func environmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char != '_' && !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(index > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

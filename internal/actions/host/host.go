package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"owngit/internal/actions"
	"owngit/internal/checkexec"
)

// RunJob supplies the host environment and script adapter. GitPath is the Git
// executable selected by OwnGit, used to locate Git for Windows bash.
func RunJob(ctx context.Context, plan actions.JobPlan, options actions.RunOptions, gitPath string) actions.JobResult {
	if options.Environment == nil {
		options.Environment = checkexec.HostEnvironment
	}
	if options.RunScript == nil {
		options.RunScript = Runner(gitPath)
	}
	return actions.RunJob(ctx, plan, options)
}

// Runner reuses the owned process lifecycle and raw byte limits of checks.
func Runner(gitPath string) actions.ScriptRunner {
	return func(ctx context.Context, script actions.Script) actions.ScriptResult {
		definition, err := shellInvocation(script, gitPath)
		if err != nil {
			status := actions.StatusError
			if errors.Is(err, exec.ErrNotFound) {
				status = actions.StatusUnavailable
			}
			return actions.ScriptResult{Status: status, Output: err.Error()}
		}
		results, _ := checkexec.Run(ctx, []checkexec.Definition{definition}, checkexec.Options{
			Dir: script.Directory, Env: script.Environment, Timeout: script.Timeout,
			OutputLimit: script.OutputLimit, OutputTap: script.Output,
		})
		result := results[0]
		return actions.ScriptResult{Status: result.Status, ExitCode: result.ExitCode, Duration: result.Duration,
			Output: result.Output, OutputGap: result.OutputGap, Truncated: result.Truncated, CleanupError: result.CleanupError,
			ExceededOutputLimit: result.ExceededOutputLimit}
	}
}

func shellInvocation(script actions.Script, gitPath string) (checkexec.Definition, error) {
	values := environmentMap(script.Environment, runtime.GOOS == "windows")
	shell := script.Shell
	implicit := shell == ""
	if implicit {
		if runtime.GOOS == "windows" {
			if prefersBash(script.RunsOn, gitPath, script.Directory, script.Environment) {
				shell = "bash"
			} else if findExecutable("pwsh", script.Directory, values) != "" {
				shell = "pwsh"
			} else {
				shell = "powershell"
			}
		} else if findExecutable("bash", script.Directory, values) != "" {
			shell = "bash"
		} else {
			shell = "sh"
		}
	}
	words, err := actions.ShellWords(shell)
	if err != nil {
		return checkexec.Definition{}, err
	}
	language := strings.TrimSuffix(strings.ToLower(filepath.Base(words[0])), ".exe")
	path, err := prepareShellScript(script, language)
	if err != nil {
		return checkexec.Definition{}, err
	}
	script.Path = path
	command, arguments := shell, []string{}
	switch shell {
	case "bash":
		arguments = []string{"--noprofile", "--norc", "-eo", "pipefail", script.Path}
		if implicit {
			arguments = []string{"-e", script.Path}
		}
	case "sh":
		if runtime.GOOS == "windows" {
			return checkexec.Definition{}, fmt.Errorf("workflow.shell_unavailable: sh is not supported on Windows: %w", exec.ErrNotFound)
		}
		arguments = []string{"-e", script.Path}
	case "pwsh", "powershell":
		arguments = []string{"-ExecutionPolicy", "Bypass", "-command", ". '" + strings.ReplaceAll(script.Path, "'", "''") + "'"}
	case "python":
		arguments = []string{script.Path}
	case "cmd":
		if runtime.GOOS != "windows" {
			return checkexec.Definition{}, fmt.Errorf("workflow.shell_unavailable: cmd requires Windows: %w", exec.ErrNotFound)
		}
		return cmdScriptDefinition(script)
	default:
		command = words[0]
		placeholder := false
		for _, word := range words[1:] {
			placeholder = placeholder || strings.Contains(word, "{0}")
			arguments = append(arguments, strings.ReplaceAll(word, "{0}", script.Path))
		}
		if !placeholder {
			return checkexec.Definition{}, fmt.Errorf("workflow.shell: custom shell must contain {0}")
		}
	}
	executable := findExecutable(command, script.Directory, values)
	if language == "bash" && runtime.GOOS == "windows" {
		executable = gitBash(gitPath, script.Directory, values)
	}
	if executable == "" {
		return checkexec.Definition{}, fmt.Errorf("workflow.shell_unavailable: %s: %w", shell, exec.ErrNotFound)
	}
	return checkexec.Definition{Executable: executable, Arguments: arguments}, nil
}

func cmdScriptDefinition(script actions.Script) (checkexec.Definition, error) {
	environment := environmentMap(script.BaseEnvironment, true)
	command := environmentValue(environment, "ComSpec", true)
	if systemRoot := environmentValue(environment, "SystemRoot", true); command == "" && filepath.IsAbs(systemRoot) {
		command = filepath.Join(systemRoot, "System32", "cmd.exe")
	}
	executable := ""
	if command != "" {
		executable = findExecutable(command, script.Directory, environment)
	}
	if executable == "" {
		return checkexec.Definition{}, fmt.Errorf("workflow.shell_unavailable: cmd: %w", exec.ErrNotFound)
	}
	return checkexec.Definition{Executable: executable,
		CommandLine: `"` + executable + `" /D /E:ON /V:OFF /S /C "CALL "` + script.Path + `""`}, nil
}

func findExecutable(name, stepDirectory string, environment map[string]string) string {
	windows := runtime.GOOS == "windows"
	extensions := []string{""}
	if windows && filepath.Ext(name) == "" {
		extensions = strings.Split(environmentValue(environment, "PATHEXT", true), ";")
		if len(extensions) == 1 && extensions[0] == "" {
			extensions = []string{".exe", ".com", ".bat", ".cmd"}
		}
	}
	directories := filepath.SplitList(environmentValue(environment, "PATH", windows))
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		directories = []string{""}
	}
	for _, directory := range directories {
		for _, extension := range extensions {
			candidate := filepath.Join(directory, name+extension)
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(stepDirectory, candidate)
			}
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && (windows || info.Mode()&0o111 != 0) {
				absolute, err := filepath.Abs(candidate)
				if err == nil {
					return absolute
				}
			}
		}
	}
	return ""
}

func gitBash(gitPath, stepDirectory string, environment map[string]string) string {
	if gitPath == "" {
		gitPath = findExecutable("git", stepDirectory, environment)
	}
	if !filepath.IsAbs(gitPath) || !strings.EqualFold(filepath.Base(gitPath), "git.exe") {
		return ""
	}
	directory := filepath.Dir(gitPath)
	for range 3 {
		for _, relative := range []string{"bin/bash.exe", "usr/bin/bash.exe"} {
			candidate := filepath.Join(directory, filepath.FromSlash(relative))
			if strings.Contains(strings.ToLower(candidate), `\windows\system32\`) {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
		directory = filepath.Dir(directory)
	}
	return ""
}

func prefersBash(runsOn []string, gitPath, stepDirectory string, environment []string) bool {
	if runtime.GOOS != "windows" || gitBash(gitPath, stepDirectory, environmentMap(environment, true)) == "" {
		return false
	}
	for _, label := range runsOn {
		label = strings.ToLower(label)
		if label == "linux" || label == "macos" || strings.HasPrefix(label, "ubuntu-") || strings.HasPrefix(label, "macos-") {
			return true
		}
	}
	return false
}

func prepareShellScript(script actions.Script, shell string) (string, error) {
	if shell != "pwsh" && shell != "powershell" && shell != "cmd" {
		return script.Path, nil
	}
	root := script.ActionsRoot
	if root == nil {
		return "", fmt.Errorf("workflow.workspace: actions root is required for script preparation")
	}
	name := filepath.Join("scripts", filepath.Base(script.Path))
	input, err := root.Open(name)
	if err != nil {
		return "", err
	}
	value, readErr := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if err := errors.Join(readErr, input.Close()); err != nil {
		return "", err
	}
	if len(value) > 64<<10 {
		return "", fmt.Errorf("workflow.limit: script exceeds 64 KiB")
	}
	extension := ".cmd"
	if shell != "cmd" {
		extension = ".ps1"
		// Windows PowerShell reads BOM-less source as ANSI.
		value = []byte("\uFEFF$ErrorActionPreference = 'stop'\n" + string(value) + "\nif (Test-Path -LiteralPath variable:\\LASTEXITCODE) { exit $LASTEXITCODE }\n")
	}
	name += extension
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := output.Write(value)
	return filepath.Join(root.Name(), name), errors.Join(writeErr, output.Close())
}

func environmentMap(entries []string, windows bool) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			if windows {
				name = strings.ToUpper(name)
			}
			values[name] = value
		}
	}
	return values
}

func environmentValue(values map[string]string, name string, windows bool) string {
	if windows {
		name = strings.ToUpper(name)
	}
	return values[name]
}

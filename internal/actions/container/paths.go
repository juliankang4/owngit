package container

import (
	"maps"
	"path/filepath"
	"strings"

	"owngit/internal/actions"
)

type mountedPaths struct {
	workspace, directory string
}

func scriptPaths(script actions.Script) mountedPaths {
	return mountedPaths{workspace: script.Workspace, directory: script.ActionsRoot.Name()}
}

func (paths mountedPaths) translate(value string) string {
	if !filepath.IsAbs(value) {
		return value
	}
	for _, mount := range []struct{ source, target string }{
		{paths.workspace, "/workspace"},
		{filepath.Join(paths.directory, "scripts"), "/owngit/scripts"},
		{filepath.Join(paths.directory, "work"), "/owngit/work"},
	} {
		relative, err := filepath.Rel(mount.source, value)
		if err == nil && filepath.IsLocal(relative) {
			if relative == "." {
				return mount.target
			}
			return mount.target + "/" + filepath.ToSlash(relative)
		}
	}
	return value
}

func (paths mountedPaths) environmentValue(name, value string) string {
	switch name {
	case "GITHUB_ENV", "GITHUB_PATH", "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY":
		relative, err := filepath.Rel(filepath.Join(paths.directory, "files"), value)
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if err == nil && filepath.IsLocal(relative) && len(parts) == 2 && strings.HasPrefix(parts[0], "step-") && parts[1] == name {
			return "/owngit/files/" + name
		}
	case "GITHUB_WORKSPACE", "GITHUB_EVENT_PATH", "RUNNER_TEMP", "HOME", "XDG_CACHE_HOME", "GOCACHE", "GOTMPDIR", "TMPDIR", "TEMP", "TMP":
		return paths.translate(value)
	}
	return value
}

func containerEvaluator(options actions.RunOptions) actions.Evaluator {
	workspace, _ := filepath.Abs(options.Workspace)
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	directory := options.Directory
	if directory == "" {
		directory = filepath.Join(filepath.Dir(workspace), "actions")
	}
	directory, _ = filepath.Abs(directory)
	paths := mountedPaths{workspace: workspace, directory: directory}
	return func(key, text string, contexts map[string]any) (any, error) {
		translated := maps.Clone(contexts)
		for _, context := range []struct{ name, field string }{{"github", "workspace"}, {"runner", "temp"}} {
			values := maps.Clone(contexts[context.name].(map[string]any))
			if value, ok := values[context.field].(string); ok {
				values[context.field] = paths.translate(value)
			}
			translated[context.name] = values
		}
		if values, ok := contexts["env"].(map[string]string); ok {
			environment := maps.Clone(values)
			for name, value := range environment {
				environment[name] = paths.environmentValue(name, value)
			}
			translated["env"] = environment
		}
		return options.Evaluator(key, text, translated)
	}
}

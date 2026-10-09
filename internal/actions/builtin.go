package actions

import (
	"fmt"
	"sort"
	"strings"
)

func runBuiltin(step Step, evaluator Evaluator, contexts map[string]any, result *StepResult, mask *masker) error {
	action, version, ok := strings.Cut(strings.ToLower(step.Uses), "@")
	if !ok || version == "" {
		return fmt.Errorf("workflow.action: unsupported action %s", step.Uses)
	}
	note := Message{}
	result.Status = StatusNotRun
	switch action {
	case "actions/checkout":
		result.Status = StatusPassed
		note = Message{Code: "note.checkout", Detail: "OwnGit already prepared this commit. The workspace has no .git folder."}
		if github, ok := contexts["github"].(map[string]any); ok {
			note.Args = map[string]string{"sha": mask.text(fmt.Sprint(github["sha"]))}
		}
	case "actions/setup-go", "actions/setup-node", "actions/setup-python", "actions/setup-java":
		note = Message{Code: "note.setup", Detail: "OwnGit does not install tools. Provide them on the host or in the executor image."}
		tool := map[string]string{"actions/setup-go": "Go", "actions/setup-node": "Node.js", "actions/setup-python": "Python", "actions/setup-java": "Java"}[action]
		var versions []string
		keys := make([]string, 0, len(step.With))
		for key := range step.With {
			if strings.HasSuffix(key, "-version") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := evaluateText(evaluator, "step.with", step.With[key], contexts)
			if err != nil {
				return err
			}
			note.Detail += " " + key + "=" + value + "."
			versions = append(versions, value)
		}
		note.Args = map[string]string{"tool": tool, "version": mask.text(strings.Join(versions, ", "))}
	case "actions/cache", "actions/cache/restore", "actions/cache/save":
		note = Message{Code: "note.cache", Detail: "OwnGit does not restore or save this cache."}
		contexts["step_outputs"] = map[string]string{"cache-hit": "false"}
		result.Outputs = map[string]string{"cache-hit": "false"}
	case "actions/upload-artifact":
		note = Message{Code: "note.artifact", Detail: "OwnGit does not upload artifacts. Keep the files through a run step instead."}
	default:
		return fmt.Errorf("workflow.action: unsupported action %s; replace it with run steps", step.Uses)
	}
	note.Detail = mask.text(note.Detail)
	result.Notes = append(result.Notes, note)
	return nil
}

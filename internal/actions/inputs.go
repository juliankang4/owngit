package actions

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type WorkflowFile struct {
	Path string
	Mode string
	Size int64
	Data []byte
}

type ReadWorkflow struct {
	Path     string
	Workflow *Workflow
	Refusal  *Message
}

// ParseFiles applies the commit-wide file bounds in path order. Discovery and
// blob reads stay with the repository adapter; subdirectories are not workflows.
func ParseFiles(files []WorkflowFile) []ReadWorkflow {
	files = append([]WorkflowFile(nil), files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out, total, count := []ReadWorkflow{}, int64(0), 0
	for _, file := range files {
		if !strings.HasPrefix(file.Path, ".github/workflows/") {
			continue
		}
		name := strings.TrimPrefix(file.Path, ".github/workflows/")
		if strings.Contains(name, "/") || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		count++
		entry := ReadWorkflow{Path: file.Path}
		var err error
		switch {
		case count > 32:
			err = limitError("workflow files per commit", 32)
		case file.Size < 0 || file.Size > MaxWorkflowBytes:
			err = limitError(file.Path, MaxWorkflowBytes)
		case file.Size > (1<<20)-total:
			err = limitError("workflow bytes per commit", 1<<20)
		case file.Mode != "100644" && file.Mode != "100755":
			err = expressionError(file.Path, "a regular Git blob, not a symbolic link")
		case int64(len(file.Data)) != file.Size:
			err = expressionError(file.Path, "the exact recorded blob size")
		default:
			entry.Workflow, err = Parse(file.Path, file.Data)
		}
		if file.Size >= 0 && file.Size <= MaxWorkflowBytes {
			total += file.Size
		}
		if err != nil {
			message := refusedJob(JobPlan{}, err).Reason
			entry.Refusal = &message
		}
		out = append(out, entry)
	}
	return out
}

// DispatchInputs resolves defaults and returns typed inputs. The github event
// input map is separately stringified, as GitHub's event payload does.
func DispatchInputs(definitions map[string]DispatchInput, supplied map[string]any) (map[string]any, map[string]any, error) {
	if len(definitions) > 25 {
		return nil, nil, limitError("dispatch inputs", 25)
	}
	for _, name := range sortedKeys(supplied) {
		if _, ok := definitions[name]; !ok {
			return nil, nil, dispatchError(name, "it is not declared")
		}
	}
	typed, event, total := map[string]any{}, map[string]any{}, 0
	for _, name := range sortedKeys(definitions) {
		definition := definitions[name]
		value, exists := supplied[name]
		if !exists {
			value = definition.Default
		}
		if value == nil {
			if definition.Required {
				return nil, nil, dispatchError(name, "it is required")
			}
			switch definition.Type {
			case "boolean":
				value = false
			case "number":
				value = float64(0)
			default:
				value = ""
			}
		}
		text, err := ScalarString(value)
		if err != nil || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return nil, nil, dispatchError(name, "use a UTF-8 scalar without NUL")
		}
		if len(text) > 1024 {
			return nil, nil, dispatchError(name, "the value is longer than 1024 bytes")
		}
		total += len(name) + len(text)
		if total > 64<<10 {
			return nil, nil, dispatchError(name, "the inputs are larger than 64 KiB")
		}
		switch definition.Type {
		case "boolean":
			boolean, err := strconv.ParseBool(text)
			if err != nil || text != "true" && text != "false" {
				return nil, nil, dispatchError(name, "use true or false")
			}
			value = boolean
		case "number":
			n, err := parseNumber(text)
			if err != nil {
				return nil, nil, dispatchError(name, "use a finite number")
			}
			value = n
		case "choice":
			found := false
			for _, option := range definition.Options {
				found = found || text == option
			}
			if !found && (definition.Required || text != "") {
				return nil, nil, dispatchError(name, "choose one of the declared options")
			}
			value = text
		case "string", "":
			if definition.Required && text == "" {
				return nil, nil, dispatchError(name, "it is required")
			}
			value = text
		default:
			return nil, nil, dispatchError(name, "the input type is not supported")
		}
		typed[name], event[name] = value, text
	}
	return typed, event, nil
}

func dispatchError(name, reason string) error {
	return refuse("workflow.dispatch_input", "inputs."+name, 0, fmt.Sprintf("Input %s is not valid: %s. The workflow declares its inputs under on.workflow_dispatch.inputs.", name, reason), map[string]string{"input": name, "reason": reason})
}

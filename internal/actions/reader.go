package actions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	MaxWorkflowBytes = 128 << 10
	MaxYAMLNodes     = 100000
	MaxYAMLDepth     = 64
	MaxSteps         = 50
	MaxRunBytes      = 24000
	MaxPlanBytes     = 2 << 20
)

type Workflow struct {
	Path        string
	Name        string
	RunName     string
	Digest      string
	Events      map[string]Trigger
	Env         map[string]string
	Defaults    RunDefaults
	Concurrency Concurrency
	Jobs        []JobDefinition
	Notes       []Message
}

type JobDefinition struct {
	JobPlan
	Matrix      any
	MatrixOrder []string
	FailFast    string
	MaxParallel string
	Refusal     *Message
}

type Trigger struct {
	Branches, BranchesIgnore []string
	Tags, TagsIgnore         []string
	Paths, PathsIgnore       []string
	Types                    []string
	Inputs                   map[string]DispatchInput
	Schedules                []string
	RefusedSchedules         []Message
}

type DispatchInput struct {
	Description string
	Type        string
	Required    bool
	Default     any
	Options     []string
}

// Parse reads one top-level workflow blob. Unknown keys and YAML structural
// errors refuse the file; unsupported job features refuse only their job.
func Parse(filename string, data []byte) (*Workflow, error) {
	if path.Dir(filename) != ".github/workflows" || !(strings.HasSuffix(filename, ".yml") || strings.HasSuffix(filename, ".yaml")) {
		return nil, expressionError(filename, "a top-level .github/workflows/*.yml or *.yaml file")
	}
	if len(path.Base(filename)) > 100 {
		return nil, limitError("workflow filename", 100)
	}
	if len(data) > MaxWorkflowBytes {
		return nil, limitError(filename, MaxWorkflowBytes)
	}
	if !utf8.Valid(data) {
		return nil, refuse("workflow.yaml", filename, 1, "Line 1 is not valid YAML: use UTF-8.", map[string]string{"line": "1", "detail": "use UTF-8"})
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document, second yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, invalidYAML(filename, err)
	}
	if err := decoder.Decode(&second); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, invalidYAML(filename, err)
		}
		return nil, yamlFeature(&second, filename, "a second document")
	}
	count := 0
	root, err := expandYAML(&document, filename, 0, &count)
	if err != nil {
		return nil, err
	}
	if len(root.Content) != 1 {
		return nil, wrongType(root, filename, "a workflow mapping")
	}
	fields, err := mapping(root.Content[0], "workflow", "name run-name on env defaults concurrency jobs permissions cache-mode")
	if err != nil {
		return nil, err
	}
	w := &Workflow{Path: filename, Events: map[string]Trigger{}}
	digest := sha256.Sum256(data)
	w.Digest = hex.EncodeToString(digest[:])
	if w.Name, err = scalar(fields["name"], "name"); err != nil {
		return nil, err
	}
	if w.RunName, err = scalar(fields["run-name"], "run-name"); err != nil {
		return nil, err
	}
	if _, err = validateAt(fields["run-name"], w.RunName, "run-name"); err != nil {
		return nil, err
	}
	if err = readEvents(w, fields["on"]); err != nil {
		return nil, err
	}
	if w.Env, err = readEnv(fields["env"], "env"); err != nil {
		return nil, err
	}
	if w.Defaults, err = readDefaults(fields["defaults"], "defaults"); err != nil {
		return nil, err
	}
	if w.Concurrency, err = readConcurrency(fields["concurrency"], "concurrency"); err != nil {
		return nil, err
	}
	for _, key := range []string{"permissions", "cache-mode"} {
		if n := fields[key]; n != nil {
			if err := validateNoted(n, key); err != nil {
				return nil, err
			}
			w.Notes = append(w.Notes, notApplied(key))
		}
	}
	jobs, err := mapping(fields["jobs"], "jobs", "")
	if err != nil || len(jobs) == 0 {
		if err == nil {
			err = wrongType(fields["jobs"], "jobs", "a nonempty mapping of jobs")
		}
		return nil, err
	}
	for i := 0; i < len(fields["jobs"].Content); i += 2 {
		id := fields["jobs"].Content[i].Value
		if len(id) > 100 || !validIdentifier(id) {
			return nil, wrongType(fields["jobs"].Content[i], "jobs."+id, "a job identifier up to 100 bytes, starting with a letter or underscore")
		}
		job, err := readJob(id, jobs[id])
		if err != nil {
			return nil, err
		}
		w.Jobs = append(w.Jobs, job)
	}
	if err := validateNeeds(w.Jobs); err != nil {
		return nil, err
	}
	return w, nil
}

func expandYAML(n *yaml.Node, key string, depth int, count *int) (*yaml.Node, error) {
	*count++
	if depth > MaxYAMLDepth || *count > MaxYAMLNodes {
		return nil, refuse("workflow.limit", key, n.Line, "YAML expansion is over OwnGit's limit of 100000 nodes or depth 64.", map[string]string{"what": "YAML expansion", "limit": "100000 nodes or depth 64"})
	}
	if n.Tag == "!!merge" {
		return nil, yamlFeature(n, key, "a merge key (<<)")
	}
	if n.Style&yaml.TaggedStyle != 0 || n.Tag != "" && !containsWord("!!map !!seq !!str !!int !!float !!bool !!null", n.Tag) {
		return nil, yamlFeature(n, key, "a tag")
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return nil, wrongType(n, key, "an existing YAML anchor")
		}
		expanded, err := expandYAML(n.Alias, key, depth+1, count)
		if err == nil {
			expanded.Line, expanded.Column = n.Line, n.Column
		}
		return expanded, err
	}
	copy := *n
	copy.Content = make([]*yaml.Node, 0, len(n.Content))
	seen := map[string]*yaml.Node{}
	for i, child := range n.Content {
		childKey := key
		if n.Kind == yaml.MappingNode && i%2 == 1 {
			childKey += "." + copy.Content[i-1].Value
		}
		expanded, err := expandYAML(child, childKey, depth+1, count)
		if err != nil {
			return nil, err
		}
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			if expanded.Value == "<<" {
				return nil, yamlFeature(expanded, key, "a merge key (<<)")
			}
			if expanded.Kind != yaml.ScalarNode || expanded.Tag != "!!str" {
				return nil, wrongType(expanded, key, "string mapping keys")
			}
			if first, exists := seen[expanded.Value]; exists {
				return nil, duplicateKey(first, expanded, expanded.Value, key)
			}
			seen[expanded.Value] = expanded
		}
		copy.Content = append(copy.Content, expanded)
	}
	return &copy, nil
}

func mapping(n *yaml.Node, key, allowed string) (map[string]*yaml.Node, error) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, wrongType(n, key, "a mapping")
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		name := n.Content[i].Value
		if allowed != "" && !containsWord(allowed, name) {
			return nil, unknownKey(n.Content[i], key+"."+name)
		}
		fields[name] = n.Content[i+1]
	}
	return fields, nil
}

func scalar(n *yaml.Node, key string) (string, error) {
	if n == nil {
		return "", nil
	}
	if n.Kind != yaml.ScalarNode {
		return "", wrongType(n, key, "a scalar")
	}
	v, err := yamlValue(n)
	if err != nil {
		return "", wrongType(n, key, "a JSON scalar")
	}
	return ScalarString(v)
}

func stringList(n *yaml.Node, key string, scalarAllowed bool) ([]string, error) {
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.ScalarNode && scalarAllowed {
		s, err := scalar(n, key)
		return []string{s}, err
	}
	if n.Kind != yaml.SequenceNode {
		return nil, wrongType(n, key, "a list")
	}
	out := make([]string, 0, len(n.Content))
	for _, child := range n.Content {
		s, err := scalar(child, key)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			value, err := yamlValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[n.Content[i].Value] = value
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, child := range n.Content {
			value, err := yamlValue(child)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str":
			return n.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool":
			return strconv.ParseBool(n.Value)
		case "!!int", "!!float":
			var value any
			if err := n.Decode(&value); err != nil {
				return nil, err
			}
			switch value := value.(type) {
			case int:
				return float64(value), nil
			case int64:
				return float64(value), nil
			case uint64:
				return float64(value), nil
			case float64:
				if _, err := parseNumber(strconv.FormatFloat(value, 'g', -1, 64)); err != nil {
					return nil, err
				}
				return value, nil
			}
		}
	}
	return nil, fmt.Errorf("not a JSON value")
}

func readEnv(n *yaml.Node, key string) (map[string]string, error) {
	if n == nil {
		return nil, nil
	}
	fields, err := mapping(n, key, "")
	if err != nil {
		return nil, err
	}
	if len(fields) > 200 {
		return nil, limitError(key+" variables", 200)
	}
	out, total := map[string]string{}, 0
	for _, name := range sortedKeys(fields) {
		if !validEnvName(name) {
			return nil, refuse("workflow.env_name", key+"."+name, fields[name].Line, fmt.Sprintf("Variable name %s is not supported. Use letters, digits and underscores, and do not start with a digit.", name), map[string]string{"name": name})
		}
		text, err := scalar(fields[name], key+"."+name)
		if err != nil {
			return nil, err
		}
		if strings.ContainsRune(text, 0) {
			return nil, wrongType(fields[name], key+"."+name, "a value without NUL")
		}
		if len(text) > 48<<10 {
			return nil, limitError(key+"."+name, 48<<10)
		}
		total += len(name) + len(text) + 2
		out[name] = text
	}
	if total > 256<<10 {
		return nil, limitError(key, 256<<10)
	}
	return out, nil
}

func readDefaults(n *yaml.Node, key string) (RunDefaults, error) {
	var out RunDefaults
	if n == nil {
		return out, nil
	}
	fields, err := mapping(n, key, "run")
	if err != nil {
		return out, err
	}
	fields, err = mapping(fields["run"], key+".run", "shell working-directory")
	if err != nil {
		return out, err
	}
	if out.Shell, err = scalar(fields["shell"], key+".run.shell"); err != nil {
		return out, err
	}
	out.WorkingDirectory, err = scalar(fields["working-directory"], key+".run.working-directory")
	if err == nil && key == "defaults" && (strings.Contains(out.Shell, "${{") || strings.Contains(out.WorkingDirectory, "${{")) {
		err = contextError("expression", key, "Workflow defaults.run cannot use expressions.")
	}
	return out, err
}

func readConcurrency(n *yaml.Node, key string) (Concurrency, error) {
	var out Concurrency
	if n == nil {
		return out, nil
	}
	if n.Kind == yaml.ScalarNode {
		var err error
		out.Group, err = scalar(n, key)
		if err == nil && out.Group == "" {
			err = wrongType(n, key, "a nonempty group")
		}
		return out, err
	}
	fields, err := mapping(n, key, "group cancel-in-progress queue")
	if err != nil {
		return out, err
	}
	if out.Group, err = scalar(fields["group"], key+".group"); err != nil {
		return out, err
	}
	if out.Group == "" {
		return out, wrongType(n, key+".group", "a nonempty group")
	}
	if out.CancelInProgress, err = boolExpression(fields["cancel-in-progress"], key+".cancel-in-progress"); err != nil {
		return out, err
	}
	if out.Queue, err = scalar(fields["queue"], key+".queue"); err != nil {
		return out, err
	}
	if out.Queue != "" && out.Queue != "single" && out.Queue != "max" {
		return out, wrongType(fields["queue"], key+".queue", "single or max")
	}
	if out.Queue == "max" && out.CancelInProgress == "true" {
		return out, wrongType(n, key, "queue: max without cancel-in-progress: true")
	}
	return out, nil
}

func boolExpression(n *yaml.Node, key string) (string, error) {
	if n == nil {
		return "", nil
	}
	if n.Tag != "!!bool" && !(n.Tag == "!!str" && strings.Contains(n.Value, "${{")) {
		return "", wrongType(n, key, "a boolean (use true or false), or an expression")
	}
	return scalar(n, key)
}

func wrongType(n *yaml.Node, key, expected string) error {
	line := 0
	if n != nil {
		line = n.Line
	}
	var args map[string]string
	if line > 0 {
		args = map[string]string{"path": key, "line": fmt.Sprint(line), "expected": expected}
	}
	return refuse("workflow.wrong_type", key, line, fmt.Sprintf("%s (line %d) must be %s.", key, line, expected), args)
}

func validateAt(n *yaml.Node, text, key string) ([]string, error) {
	secrets, err := ValidateTemplate(text, key)
	var refusal *Refusal
	if errors.As(err, &refusal) && n != nil {
		refusal.Line = n.Line
	}
	return secrets, err
}

func validIdentifier(s string) bool {
	if len(s) == 0 || !asciiLetter(s[0]) && s[0] != '_' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !asciiLetter(s[i]) && !asciiDigit(s[i]) && s[i] != '_' && s[i] != '-' {
			return false
		}
	}
	return true
}

func validEnvName(s string) bool {
	return validIdentifier(s) && !strings.Contains(s, "-")
}

func invalidYAML(key string, err error) error {
	line := 1
	_, _ = fmt.Sscanf(err.Error(), "yaml: line %d:", &line)
	return refuse("workflow.yaml", key, line, fmt.Sprintf("Line %d is not valid YAML: %s", line, err), map[string]string{"line": fmt.Sprint(line), "detail": err.Error()})
}

func yamlFeature(n *yaml.Node, key, feature string) error {
	return refuse("workflow.yaml_feature", key, n.Line, fmt.Sprintf("Line %d uses %s, which OwnGit does not read in workflow files. Write the values out in full.", n.Line, feature), map[string]string{"line": fmt.Sprint(n.Line), "feature": feature})
}

func duplicateKey(first, second *yaml.Node, name, key string) error {
	return refuse("workflow.duplicate_key", key, second.Line, fmt.Sprintf("`%s` appears twice in `%s` (lines %d and %d). Keep one.", name, key, first.Line, second.Line), map[string]string{"key": name, "path": key, "a": fmt.Sprint(first.Line), "b": fmt.Sprint(second.Line)})
}

func unknownKey(n *yaml.Node, key string) error {
	return refuse("workflow.unknown_key", key, n.Line, fmt.Sprintf("`%s` (line %d) is not a workflow key OwnGit knows. Check the spelling against GitHub's workflow syntax, or remove it.", key, n.Line), map[string]string{"path": key, "line": fmt.Sprint(n.Line)})
}

func notApplied(key string) Message {
	effect := "OwnGit does not use this GitHub setting."
	switch key {
	case "permissions":
		effect = "OwnGit gives workflows no GitHub token."
	case "cache-mode":
		effect = "OwnGit keeps no cache."
	case "outputs":
		effect = "OwnGit does not pass outputs between jobs."
	}
	return Message{Code: "note.not_applied", Path: key, Detail: key + " is not applied: " + effect, Args: map[string]string{"key": key, "effect": effect}}
}

func validateNoted(n *yaml.Node, key string) error {
	if strings.HasSuffix(key, "permissions") && n.Kind == yaml.MappingNode {
		_, err := mapping(n, key, "actions artifact-metadata attestations checks code-quality contents deployments discussions id-token issues models packages pages pull-requests repository-projects security-events statuses vulnerability-alerts")
		return err
	}
	if n.Kind != yaml.ScalarNode {
		return wrongType(n, key, "a scalar")
	}
	return nil
}

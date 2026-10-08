package actions

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

func supportedEvent(name string) bool {
	return containsWord("push pull_request workflow_dispatch schedule", name)
}

func eventMessage(name string) Message {
	if name == "pull_request_target" {
		return Message{Code: "workflow.event_pr_target", Path: "on." + name, Detail: "pull_request_target does not run. Use pull_request; OwnGit runs a pull request from its own commit."}
	}
	return Message{Code: "workflow.event", Path: "on." + name, Detail: fmt.Sprintf("OwnGit does not run workflows on %s. It runs push, pull_request, workflow_dispatch and schedule.", name)}
}

func readEvents(w *Workflow, n *yaml.Node) error {
	known := "branch_protection_rule check_run check_suite create delete deployment deployment_status discussion discussion_comment fork gollum issue_comment issues label merge_group milestone page_build project project_card project_column public pull_request pull_request_review pull_request_review_comment pull_request_target push registry_package release repository_dispatch schedule status watch workflow_call workflow_dispatch workflow_run"
	var events map[string]*yaml.Node
	if n == nil {
		return wrongType(n, "on", "an event name, list or mapping")
	}
	if n.Kind == yaml.MappingNode {
		var err error
		events, err = mapping(n, "on", known)
		if err != nil {
			return err
		}
	} else {
		list, err := stringList(n, "on", true)
		if err != nil {
			return err
		}
		events = map[string]*yaml.Node{}
		for i, name := range list {
			node := n
			if n.Kind == yaml.SequenceNode {
				node = n.Content[i]
			}
			if !containsWord(known, name) {
				return unknownKey(node, "on."+name)
			}
			if first, exists := events[name]; exists {
				return duplicateKey(first, node, name, "on")
			}
			events[name] = node
		}
	}
	if len(events) == 0 {
		return wrongType(n, "on", "at least one event")
	}
	for _, name := range sortedKeys(events) {
		value := events[name]
		if n.Kind != yaml.MappingNode {
			value = nil
		}
		trigger := Trigger{}
		if !supportedEvent(name) {
			if value != nil && value.Tag != "!!null" {
				if _, err := mapping(value, "on."+name, "types branches branches-ignore tags tags-ignore paths paths-ignore workflows inputs outputs secrets"); err != nil {
					return err
				}
			}
			w.Notes = append(w.Notes, eventMessage(name))
		} else if value != nil && value.Tag != "!!null" {
			var err error
			switch name {
			case "schedule":
				trigger.Schedules, trigger.RefusedSchedules, err = readSchedules(value)
				w.Notes = append(w.Notes, trigger.RefusedSchedules...)
			case "workflow_dispatch":
				trigger.Inputs, err = readInputs(value)
			default:
				trigger, err = readFilters(value, name)
			}
			if err != nil {
				return err
			}
		}
		w.Events[name] = trigger
	}
	return nil
}

func readFilters(n *yaml.Node, event string) (Trigger, error) {
	out := Trigger{}
	allowed := "branches branches-ignore paths paths-ignore types"
	if event == "push" {
		allowed = "branches branches-ignore tags tags-ignore paths paths-ignore"
	}
	key := "on." + event
	fields, err := mapping(n, key, allowed)
	if err != nil {
		return out, err
	}
	for _, pair := range []string{"branches", "tags", "paths"} {
		if fields[pair] != nil && fields[pair+"-ignore"] != nil {
			return out, wrongType(n, key, "only "+pair+" or "+pair+"-ignore, not both")
		}
	}
	for _, filter := range []struct {
		key string
		out *[]string
	}{
		{"branches", &out.Branches}, {"branches-ignore", &out.BranchesIgnore},
		{"tags", &out.Tags}, {"tags-ignore", &out.TagsIgnore},
		{"paths", &out.Paths}, {"paths-ignore", &out.PathsIgnore}, {"types", &out.Types},
	} {
		node := fields[filter.key]
		list, err := stringList(node, key+"."+filter.key, false)
		if err != nil {
			return out, err
		}
		if node != nil && len(list) == 0 {
			return out, wrongType(node, key+"."+filter.key, "a nonempty list")
		}
		if filter.key != "types" {
			if strings.HasSuffix(filter.key, "-ignore") {
				for _, pattern := range list {
					if strings.HasPrefix(pattern, "!") {
						return out, wrongType(fields[filter.key], key+"."+filter.key, "patterns without ! in an ignore filter")
					}
					if _, err := compilePattern(pattern); err != nil {
						return out, err
					}
				}
			} else if _, err := MatchPatterns(list, ""); err != nil {
				return out, err
			}
		}
		*filter.out = list
	}
	return out, nil
}

func readSchedules(n *yaml.Node) ([]string, []Message, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, nil, wrongType(n, "on.schedule", "a list of cron entries")
	}
	var schedules []string
	var refused []Message
	for i, entry := range n.Content {
		fields, err := mapping(entry, "on.schedule", "cron timezone")
		if err != nil {
			return nil, nil, err
		}
		if i >= 10 {
			refused = append(refused, Message{Code: "workflow.limit", Path: fmt.Sprintf("on.schedule[%d]", i), Line: entry.Line, Detail: "Schedules per workflow is over OwnGit's limit of 10."})
			continue
		}
		if fields["timezone"] != nil {
			return nil, nil, refuse("workflow.timezone", "on.schedule.timezone", fields["timezone"].Line, "OwnGit runs schedules in UTC only. Remove timezone and write the cron time in UTC.")
		}
		cron, err := scalar(fields["cron"], "on.schedule.cron")
		if err != nil {
			return nil, nil, err
		}
		if len(strings.Fields(cron)) != 5 {
			return nil, nil, wrongType(entry, "on.schedule.cron", "a five-field UTC cron expression")
		}
		schedules = append(schedules, cron)
	}
	return schedules, refused, nil
}

func readInputs(n *yaml.Node) (map[string]DispatchInput, error) {
	fields, err := mapping(n, "on.workflow_dispatch", "inputs")
	if err != nil || fields["inputs"] == nil {
		return nil, err
	}
	fields, err = mapping(fields["inputs"], "on.workflow_dispatch.inputs", "")
	if err != nil {
		return nil, err
	}
	if len(fields) > 25 {
		return nil, limitError("dispatch inputs", 25)
	}
	out := map[string]DispatchInput{}
	for _, name := range sortedKeys(fields) {
		key := "on.workflow_dispatch.inputs." + name
		if !validIdentifier(name) {
			return nil, wrongType(fields[name], key, "an input identifier")
		}
		items, err := mapping(fields[name], key, "description required default type options")
		if err != nil {
			return nil, err
		}
		input := DispatchInput{Type: "string"}
		if input.Description, err = scalar(items["description"], key+".description"); err != nil {
			return nil, err
		}
		if items["type"] != nil {
			if input.Type, err = scalar(items["type"], key+".type"); err != nil {
				return nil, err
			}
		}
		if !containsWord("string boolean choice number", input.Type) {
			return nil, wrongType(items["type"], key+".type", "string, boolean, choice or number (environment is not supported)")
		}
		if items["required"] != nil {
			if items["required"].Tag != "!!bool" {
				return nil, wrongType(items["required"], key+".required", "a boolean (use true or false)")
			}
			input.Required = strings.EqualFold(items["required"].Value, "true")
		}
		if input.Options, err = stringList(items["options"], key+".options", false); err != nil {
			return nil, err
		}
		if input.Type == "choice" && len(input.Options) == 0 {
			return nil, wrongType(items["options"], key+".options", "a nonempty list of choices")
		}
		if items["default"] != nil {
			if items["default"].Kind != yaml.ScalarNode {
				return nil, wrongType(items["default"], key+".default", "a scalar")
			}
			input.Default, err = yamlValue(items["default"])
			if err != nil {
				return nil, wrongType(items["default"], key+".default", "a valid input value")
			}
		}
		out[name] = input
	}
	return out, nil
}

func readJob(id string, n *yaml.Node) (JobDefinition, error) {
	job := JobDefinition{JobPlan: JobPlan{JobKey: id}, FailFast: "true"}
	key := "jobs." + id
	fields, err := mapping(n, key, "name runs-on needs if env defaults steps strategy concurrency timeout-minutes continue-on-error permissions cache-mode outputs uses with secrets container services environment snapshot")
	if err != nil {
		return job, err
	}
	for _, feature := range []string{"uses", "with", "secrets", "container", "services", "environment", "snapshot"} {
		if node := fields[feature]; node != nil && job.Refusal == nil {
			message := refusedFeature(feature, node.Value)
			message.Path, message.Line = key+"."+feature, node.Line
			job.Refusal = &message
		}
	}
	for _, item := range []struct {
		name string
		out  *string
	}{
		{"name", &job.Name}, {"if", &job.If}, {"timeout-minutes", &job.TimeoutMinutes},
	} {
		if *item.out, err = scalar(fields[item.name], key+"."+item.name); err != nil {
			return job, err
		}
	}
	if job.ContinueOnError, err = boolExpression(fields["continue-on-error"], key+".continue-on-error"); err != nil {
		return job, err
	}
	if fields["runs-on"] != nil && fields["runs-on"].Kind == yaml.MappingNode {
		labels, err := mapping(fields["runs-on"], key+".runs-on", "group labels")
		if err != nil {
			return job, err
		}
		if job.RunsOn, err = stringList(labels["labels"], key+".runs-on.labels", true); err != nil {
			return job, err
		}
		group, err := scalar(labels["group"], key+".runs-on.group")
		if err != nil {
			return job, err
		}
		job.RunsOn = append(job.RunsOn, group)
	} else if job.RunsOn, err = stringList(fields["runs-on"], key+".runs-on", true); err != nil {
		return job, err
	}
	if len(job.RunsOn) == 0 && job.Refusal == nil {
		return job, wrongType(fields["runs-on"], key+".runs-on", "a runner label or list of labels")
	}
	if job.Needs, err = stringList(fields["needs"], key+".needs", true); err != nil {
		return job, err
	}
	if job.Env, err = readEnv(fields["env"], key+".env"); err != nil {
		return job, err
	}
	if job.Defaults, err = readDefaults(fields["defaults"], key+".defaults"); err != nil {
		return job, err
	}
	if job.Concurrency, err = readConcurrency(fields["concurrency"], key+".concurrency"); err != nil {
		return job, err
	}
	if strategy := fields["strategy"]; strategy != nil {
		items, err := mapping(strategy, key+".strategy", "matrix fail-fast max-parallel")
		if err != nil {
			return job, err
		}
		if items["matrix"] != nil {
			job.Matrix, err = yamlValue(items["matrix"])
			if err != nil {
				return job, wrongType(items["matrix"], key+".strategy.matrix", "a mapping or expression")
			}
			if items["matrix"].Kind == yaml.MappingNode {
				for i := 0; i < len(items["matrix"].Content); i += 2 {
					name := items["matrix"].Content[i].Value
					if name != "include" && name != "exclude" {
						job.MatrixOrder = append(job.MatrixOrder, name)
					}
				}
			}
		}
		if items["fail-fast"] != nil {
			if job.FailFast, err = boolExpression(items["fail-fast"], key+".strategy.fail-fast"); err != nil {
				return job, err
			}
		}
		if job.MaxParallel, err = scalar(items["max-parallel"], key+".strategy.max-parallel"); err != nil {
			return job, err
		}
	}
	for _, feature := range []string{"permissions", "cache-mode", "outputs"} {
		if node := fields[feature]; node != nil {
			if feature == "outputs" {
				if _, err := mapping(node, key+".outputs", ""); err != nil {
					return job, err
				}
			} else if err := validateNoted(node, key+"."+feature); err != nil {
				return job, err
			}
			job.Notes = append(job.Notes, notApplied(feature))
		}
	}
	steps := fields["steps"]
	if steps == nil && job.Refusal != nil {
		return job, nil
	}
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) == 0 {
		return job, wrongType(steps, key+".steps", "a nonempty list of steps")
	}
	if len(steps.Content) > MaxSteps && job.Refusal == nil {
		job.Refusal = &Message{Code: "workflow.limit", Path: key + ".steps", Line: steps.Line, Detail: "Steps per job is over OwnGit's limit of 50."}
	}
	ids := map[string]bool{}
	for i, n := range steps.Content {
		step, refusal, err := readStep(n, fmt.Sprintf("%s.steps[%d]", key, i))
		if err != nil {
			return job, err
		}
		if step.ID != "" {
			if !validIdentifier(step.ID) || ids[step.ID] {
				return job, wrongType(n, key+".steps.id", "a unique step identifier")
			}
			ids[step.ID] = true
		}
		if job.Refusal == nil {
			job.Refusal = refusal
		}
		job.Steps = append(job.Steps, step)
	}
	return job, nil
}

func readStep(n *yaml.Node, key string) (Step, *Message, error) {
	step := Step{}
	fields, err := mapping(n, key, "id name if run shell working-directory env continue-on-error timeout-minutes uses with background wait wait-all cancel parallel")
	if err != nil {
		return step, nil, err
	}
	var refusal *Message
	for _, feature := range []string{"background", "wait", "wait-all", "cancel", "parallel"} {
		if node := fields[feature]; node != nil && refusal == nil {
			message := refusedFeature(feature, node.Value)
			message.Path, message.Line = key+"."+feature, node.Line
			refusal = &message
		}
	}
	for _, item := range []struct {
		name string
		out  *string
	}{
		{"id", &step.ID}, {"name", &step.Name}, {"if", &step.If}, {"run", &step.Run},
		{"uses", &step.Uses}, {"shell", &step.Shell}, {"working-directory", &step.WorkingDirectory}, {"timeout-minutes", &step.TimeoutMinutes},
	} {
		if *item.out, err = scalar(fields[item.name], key+"."+item.name); err != nil {
			return step, refusal, err
		}
	}
	if (fields["run"] == nil) == (fields["uses"] == nil) || step.Run == "" && step.Uses == "" {
		return step, refusal, wrongType(n, key, "one nonempty run or uses value")
	}
	if fields["with"] != nil && step.Uses == "" {
		return step, refusal, wrongType(fields["with"], key+".with", "inputs only on a uses step")
	}
	if step.ContinueOnError, err = boolExpression(fields["continue-on-error"], key+".continue-on-error"); err != nil {
		return step, refusal, err
	}
	if step.Env, err = readEnv(fields["env"], key+".env"); err != nil {
		return step, refusal, err
	}
	if fields["with"] != nil {
		items, err := mapping(fields["with"], key+".with", "")
		if err != nil {
			return step, refusal, err
		}
		step.With = map[string]string{}
		for _, name := range sortedKeys(items) {
			if step.With[name], err = scalar(items[name], key+".with."+name); err != nil {
				return step, refusal, err
			}
		}
	}
	if len(step.Run) > MaxRunBytes {
		refusal = &Message{Code: "workflow.run_too_long", Path: key + ".run", Line: n.Line, Detail: "The run script of this step is longer than 24000 bytes. Put it in a file in the repository and run that file."}
	}
	return step, refusal, nil
}

func refusedFeature(feature, value string) Message {
	code, detail := "workflow."+feature, ""
	switch feature {
	case "uses", "with", "secrets":
		code, detail = "workflow.reusable", fmt.Sprintf("Reusable workflows such as %s do not run. Copy its jobs into this file.", value)
	case "container":
		detail = "container is not used, because the administrator's policy chooses where jobs run. Remove it. For a container, the administrator sets the container executor and image."
	case "services":
		detail = "Service containers do not run. Use a service the job can reach over the network the check policy allows, or start the service and use it inside one run script."
	case "environment":
		detail = "OwnGit has no deployment environments or approval gates, and anyone with general access can start a workflow by hand, so a manual start is not an approval. Remove environment only if running this job without approval is acceptable."
	case "snapshot":
		detail = "snapshot builds a GitHub runner image, which OwnGit does not do. Keep this workflow on GitHub, or remove the key."
	default:
		code, detail = "workflow.background", fmt.Sprintf("Background and parallel steps (%s) do not run in OwnGit. Start the process and use it inside one run script.", feature)
	}
	return Message{Code: code, Detail: detail}
}

func validateNeeds(jobs []JobDefinition) error {
	byID := map[string]JobDefinition{}
	for _, job := range jobs {
		byID[job.JobKey] = job
	}
	visited, active := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return expressionError("jobs."+id+".needs", "an acyclic dependency graph")
		}
		if visited[id] {
			return nil
		}
		job, exists := byID[id]
		if !exists {
			return expressionError("needs", "a declared job, not "+id)
		}
		active[id] = true
		seen := map[string]bool{}
		for _, needed := range job.Needs {
			if seen[needed] {
				return expressionError("jobs."+id+".needs", "unique dependency names")
			}
			seen[needed] = true
			if err := visit(needed); err != nil {
				return err
			}
		}
		active[id], visited[id] = false, true
		return nil
	}
	for _, job := range jobs {
		if err := visit(job.JobKey); err != nil {
			return err
		}
	}
	return nil
}

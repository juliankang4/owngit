package actions

import (
	"fmt"
	"regexp"
	"strings"
)

// MatchPatterns applies ordered inclusions and ! exclusions to the whole name.
// GitHub's ? and + repeat the preceding character, unlike filepath.Match.
func MatchPatterns(patterns []string, name string) (bool, error) {
	set, err := compilePatternSet(patterns, true)
	return set.matches(name), err
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > MaxExpressionBytes {
		return nil, limitError("filter pattern", MaxExpressionBytes)
	}
	var out strings.Builder
	out.WriteString("(?s)^")
	repeatable := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
			if i == len(pattern) {
				return nil, expressionError("filter", "a character after an escape")
			}
			out.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			repeatable = true
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
			repeatable = false
		case '?', '+':
			if !repeatable {
				return nil, expressionError("filter", "a character or character class before ? or +")
			}
			out.WriteByte(pattern[i])
			repeatable = false
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 1 {
				return nil, expressionError("filter", "a closed, nonempty character class")
			}
			end += i + 1
			if !validPatternClass(pattern[i+1 : end]) {
				return nil, expressionError("filter", "alphanumeric characters or ranges within a-z, A-Z or 0-9")
			}
			out.WriteString(pattern[i : end+1])
			i = end
			repeatable = true
		default:
			out.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			repeatable = true
		}
	}
	out.WriteByte('$')
	re, err := regexp.Compile(out.String())
	if err != nil {
		return nil, expressionError("filter", "a valid GitHub filter pattern")
	}
	return re, nil
}

func validPatternClass(class string) bool {
	for i := 0; i < len(class); i++ {
		start := class[i]
		if !asciiLetter(start) && !asciiDigit(start) {
			return false
		}
		if i+1 == len(class) || class[i+1] != '-' {
			continue
		}
		if i+2 >= len(class) {
			return false
		}
		end := class[i+2]
		if start > end || !(start >= 'a' && end <= 'z' || start >= 'A' && end <= 'Z' || start >= '0' && end <= '9') {
			return false
		}
		i += 2
	}
	return true
}

// ChangedPaths is supplied by the coordinator's bounded Git diff reader. An
// incomplete or unavailable list never becomes an assumed filter match.
type ChangedPaths struct {
	Files    []string
	Complete bool
	Reason   string
}

type Event struct {
	Name        string
	RefName     string
	RefType     string
	BaseRef     string
	Action      string
	Changed     ChangedPaths
	BypassPaths bool
}

type FilterDecision struct {
	Matched bool
	Unknown bool
	Note    *Message
}

// MatchEvent handles only filtering. Reading Git diffs and recording refused
// runs belong to admission, not to this pure library.
func MatchEvent(workflow *Workflow, event Event) (FilterDecision, error) {
	trigger, ok := workflow.Events[event.Name]
	if !ok {
		return FilterDecision{}, nil
	}
	if !supportedEvent(event.Name) {
		message := eventMessage(event.Name)
		return FilterDecision{Note: &message}, nil
	}
	if event.Name == "workflow_dispatch" || event.Name == "schedule" {
		return FilterDecision{Matched: true}, nil
	}
	if event.Name == "push" && event.RefType == "tag" {
		message := Message{Code: "workflow.tags", Detail: "OwnGit does not run workflows for tag pushes, so this push trigger never starts a run."}
		return FilterDecision{Note: &message}, nil
	}
	name := event.RefName
	if event.Name == "pull_request" {
		name = event.BaseRef
		if len(trigger.Types) > 0 && !containsWord(strings.Join(trigger.Types, " "), event.Action) {
			return FilterDecision{}, nil
		}
	}
	if event.Name == "push" && len(trigger.Branches) == 0 && len(trigger.BranchesIgnore) == 0 && (len(trigger.Tags) > 0 || len(trigger.TagsIgnore) > 0) {
		message := Message{Code: "workflow.tags", Detail: "OwnGit does not run workflows for tag pushes, so this push trigger never starts a run."}
		return FilterDecision{Note: &message}, nil
	}
	matched, err := MatchPatterns(trigger.Branches, name)
	if err != nil || !matched {
		return FilterDecision{}, err
	}
	if ignored, err := matchesIgnore(trigger.BranchesIgnore, name); err != nil || ignored {
		return FilterDecision{}, err
	}
	if event.BypassPaths || len(trigger.Paths) == 0 && len(trigger.PathsIgnore) == 0 {
		return FilterDecision{Matched: true}, nil
	}
	paths, err := compilePatternSet(trigger.Paths, true)
	if err != nil {
		return FilterDecision{}, err
	}
	ignoredPaths, err := compilePatternSet(trigger.PathsIgnore, false)
	if err != nil {
		return FilterDecision{}, err
	}
	for _, path := range event.Changed.Files {
		if paths.matches(path) && !ignoredPaths.matches(path) {
			return FilterDecision{Matched: true}, nil
		}
	}
	if !event.Changed.Complete {
		message := Message{Code: "note.paths_unknown", Detail: fmt.Sprintf("Not run: OwnGit could not list the changed files (%s), so the paths filter could not be decided. Rerun to run it anyway.", event.Changed.Reason)}
		return FilterDecision{Unknown: true, Note: &message}, nil
	}
	return FilterDecision{}, nil
}

type compiledPattern struct {
	regex    *regexp.Regexp
	negative bool
}

type patternSet struct {
	patterns []compiledPattern
	initial  bool
}

func compilePatternSet(patterns []string, ordered bool) (patternSet, error) {
	set := patternSet{initial: ordered && len(patterns) == 0}
	positive := len(patterns) == 0
	for _, pattern := range patterns {
		negative := ordered && strings.HasPrefix(pattern, "!")
		if negative {
			pattern = pattern[1:]
		} else {
			positive = true
		}
		re, err := compilePattern(pattern)
		if err != nil {
			return patternSet{}, err
		}
		set.patterns = append(set.patterns, compiledPattern{regex: re, negative: negative})
	}
	if !positive {
		return patternSet{}, expressionError("filter", "at least one positive pattern, or an ignore filter")
	}
	return set, nil
}

func (set patternSet) matches(name string) bool {
	matched := set.initial
	for _, pattern := range set.patterns {
		if pattern.regex.MatchString(name) {
			matched = !pattern.negative
		}
	}
	return matched
}

func matchesIgnore(patterns []string, name string) (bool, error) {
	set, err := compilePatternSet(patterns, false)
	return set.matches(name), err
}

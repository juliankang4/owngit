// Package checkworkflow decodes the exact bytes of a repository check
// workflow (".owngit/checks.json") and tightens its requests against the
// operator policy.
//
// The package is deliberately small and dependency-free. It performs no Git
// access, no file discovery, and no execution. Version 1 is strict: unknown
// fields, duplicate fields, malformed patterns, unsupported versions, and
// oversized input are rejected instead of being guessed.
package checkworkflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// Path is the repository-relative workflow file. Parsing remains independent
// of Git discovery and source materialization, so callers supply exact bytes.
const Path = ".owngit/checks.json"

// Version is the only supported workflow format.
const Version = 1

const (
	// MaximumBytes bounds one workflow file before decoding.
	MaximumBytes = 64 << 10
	// MaximumChecks bounds one accepted command list.
	MaximumChecks = 50
	// MaximumCheckNameBytes and MaximumCheckCommandBytes bound one command.
	MaximumCheckNameBytes    = 100
	MaximumCheckCommandBytes = 24000
	// MaximumBranchPatterns and MaximumBranchPatternBytes bound one event's
	// optional branch selection.
	MaximumBranchPatterns     = 64
	MaximumBranchPatternBytes = 200
)

// Requested limit bounds. The durable job contract uses the same numbers, so
// an accepted request is never silently truncated to a different range.
const (
	MinimumTimeoutMS       = 1000
	DefaultTimeoutMS int64 = 10 * 60 * 1000
	MaximumTimeoutMS int64 = 24 * 60 * 60 * 1000

	MinimumOutputLimitBytes = 1024
	DefaultOutputLimitBytes = 64 << 10
	MaximumOutputLimitBytes = 64 << 20
)

// Event names. A document enables at least one explicitly.
const (
	EventPush        = "push"
	EventPullRequest = "pull_request"
)

// ErrTooLarge reports a workflow file over MaximumBytes.
var ErrTooLarge = errors.New("check workflow is too large")

// Check is one named shell command. Commands run through the platform shell,
// and no dependency installation or language detection is implied.
type Check struct {
	Name    string
	Command string
}

// Event is one explicitly enabled trigger. An empty Branches list means every
// branch; otherwise at least one pattern must match the observed branch.
type Event struct {
	Branches []string
}

// Match reports whether the event selects the branch name (without the
// refs/heads/ prefix). A pattern ending in "*" matches by prefix; every other
// pattern matches exactly.
func (event *Event) Match(branch string) bool {
	if event == nil {
		return false
	}
	if len(event.Branches) == 0 {
		return true
	}
	for _, pattern := range event.Branches {
		if matchPattern(pattern, branch) {
			return true
		}
	}
	return false
}

// Limits are the limits the repository requests. Zero means unspecified, so
// the effective limit comes from the default or the operator cap.
type Limits struct {
	TimeoutMS        int64
	OutputLimitBytes int64
}

// Events holds the explicitly enabled triggers. A nil event is disabled.
type Events struct {
	Push        *Event
	PullRequest *Event
}

// Event returns the selected event by its canonical name.
func (events Events) Event(name string) *Event {
	switch name {
	case EventPush:
		return events.Push
	case EventPullRequest:
		return events.PullRequest
	default:
		return nil
	}
}

// Document is one decoded version 1 workflow.
type Document struct {
	Version int
	Events  Events
	Checks  []Check
	Limits  Limits
}

// EventNames returns the enabled event names in canonical sorted order.
func (document Document) EventNames() []string {
	var names []string
	if document.Events.Push != nil {
		names = append(names, EventPush)
	}
	if document.Events.PullRequest != nil {
		names = append(names, EventPullRequest)
	}
	sort.Strings(names)
	return names
}

type rawDocument struct {
	Version *int        `json:"version"`
	Events  *rawEvents  `json:"events"`
	Checks  *[]rawCheck `json:"checks"`
	Limits  *rawLimits  `json:"limits"`
}

type rawEvents struct {
	Push        *rawEvent `json:"push"`
	PullRequest *rawEvent `json:"pull_request"`
}

type rawEvent struct {
	Branches *[]string `json:"branches"`
}

type rawCheck struct {
	Name    *string `json:"name"`
	Command *string `json:"command"`
}

type rawLimits struct {
	TimeoutMS        *int64 `json:"timeout_ms"`
	OutputLimitBytes *int64 `json:"output_limit_bytes"`
}

// Parse decodes and validates one exact workflow file.
func Parse(content []byte) (Document, error) {
	if len(content) > MaximumBytes {
		return Document{}, ErrTooLarge
	}
	if !utf8.Valid(content) {
		return Document{}, errors.New("check workflow is not valid UTF-8")
	}
	if err := rejectDuplicateFields(content); err != nil {
		return Document{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var raw rawDocument
	if err := decoder.Decode(&raw); err != nil {
		return Document{}, fmt.Errorf("decode check workflow: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Document{}, err
	}
	document, err := validateDocument(raw)
	if err != nil {
		return Document{}, err
	}
	return document, nil
}

func requireEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("check workflow contains trailing data")
	}
	return nil
}

// rejectDuplicateFields walks the raw JSON tokens and rejects a repeated object
// key at any depth. encoding/json otherwise keeps the last value silently.
func rejectDuplicateFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode check workflow: %w", err)
	}
	if err := scanJSONValue(decoder, token, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("check workflow contains trailing data")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, token json.Token, path string) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode check workflow: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("check workflow has a non-string field name in %s", path)
			}
			if seen[key] {
				return fmt.Errorf("check workflow has a duplicate field %q in %s", key, path)
			}
			seen[key] = true
			valueToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode check workflow: %w", err)
			}
			if err := scanJSONValue(decoder, valueToken, path+"."+key); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode check workflow: %w", err)
		}
		if end != json.Delim('}') {
			return fmt.Errorf("check workflow has a malformed object in %s", path)
		}
	case '[':
		index := 0
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode check workflow: %w", err)
			}
			if err := scanJSONValue(decoder, valueToken, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode check workflow: %w", err)
		}
		if end != json.Delim(']') {
			return fmt.Errorf("check workflow has a malformed array in %s", path)
		}
	default:
		return fmt.Errorf("check workflow has an unexpected delimiter %q in %s", delim, path)
	}
	return nil
}

func validateDocument(raw rawDocument) (Document, error) {
	document := Document{}
	if raw.Version == nil {
		return Document{}, errors.New("check workflow has no version")
	}
	if *raw.Version != Version {
		return Document{}, fmt.Errorf("unsupported check workflow version %d", *raw.Version)
	}
	document.Version = *raw.Version
	if raw.Events == nil {
		return Document{}, errors.New("check workflow has no events")
	}
	var err error
	if document.Events.Push, err = validateEvent(raw.Events.Push, EventPush); err != nil {
		return Document{}, err
	}
	if document.Events.PullRequest, err = validateEvent(raw.Events.PullRequest, EventPullRequest); err != nil {
		return Document{}, err
	}
	if document.Events.Push == nil && document.Events.PullRequest == nil {
		return Document{}, errors.New("check workflow enables no event")
	}
	if raw.Checks == nil || len(*raw.Checks) == 0 {
		return Document{}, errors.New("check workflow has no checks")
	}
	if len(*raw.Checks) > MaximumChecks {
		return Document{}, fmt.Errorf("check workflow has %d checks, the maximum is %d", len(*raw.Checks), MaximumChecks)
	}
	names := make(map[string]bool, len(*raw.Checks))
	for index, item := range *raw.Checks {
		if item.Name == nil || item.Command == nil {
			return Document{}, fmt.Errorf("check %d needs a name and a command", index)
		}
		if err := validateCheckName(*item.Name); err != nil {
			return Document{}, fmt.Errorf("check %d: %w", index, err)
		}
		if err := validateCheckCommand(*item.Command); err != nil {
			return Document{}, fmt.Errorf("check %d: %w", index, err)
		}
		if names[*item.Name] {
			return Document{}, fmt.Errorf("check workflow repeats the check name %q", *item.Name)
		}
		names[*item.Name] = true
		document.Checks = append(document.Checks, Check{Name: *item.Name, Command: *item.Command})
	}
	if raw.Limits != nil {
		if raw.Limits.TimeoutMS != nil {
			if err := validateLimit("timeout_ms", *raw.Limits.TimeoutMS, MinimumTimeoutMS, MaximumTimeoutMS); err != nil {
				return Document{}, err
			}
			document.Limits.TimeoutMS = *raw.Limits.TimeoutMS
		}
		if raw.Limits.OutputLimitBytes != nil {
			if err := validateLimit("output_limit_bytes", *raw.Limits.OutputLimitBytes, MinimumOutputLimitBytes, MaximumOutputLimitBytes); err != nil {
				return Document{}, err
			}
			document.Limits.OutputLimitBytes = *raw.Limits.OutputLimitBytes
		}
	}
	return document, nil
}

func validateEvent(raw *rawEvent, name string) (*Event, error) {
	if raw == nil {
		return nil, nil
	}
	event := &Event{}
	if raw.Branches == nil {
		return event, nil
	}
	branches := *raw.Branches
	if len(branches) == 0 {
		return nil, fmt.Errorf("event %q has an empty branch list", name)
	}
	if len(branches) > MaximumBranchPatterns {
		return nil, fmt.Errorf("event %q has %d branch patterns, the maximum is %d", name, len(branches), MaximumBranchPatterns)
	}
	for _, pattern := range branches {
		if err := validateBranchPattern(pattern); err != nil {
			return nil, fmt.Errorf("event %q: %w", name, err)
		}
	}
	event.Branches = branches
	return event, nil
}

// validateBranchPattern accepts a Git branch name or a single trailing "*"
// prefix pattern. "*" alone selects every branch.
func validateBranchPattern(pattern string) error {
	if pattern == "" {
		return errors.New("branch pattern is empty")
	}
	if len(pattern) > MaximumBranchPatternBytes {
		return fmt.Errorf("branch pattern %q exceeds %d bytes", pattern, MaximumBranchPatternBytes)
	}
	if !utf8.ValidString(pattern) || pattern != strings.TrimSpace(pattern) {
		return fmt.Errorf("branch pattern %q is not valid branch text", pattern)
	}
	if strings.Count(pattern, "*") > 1 {
		return fmt.Errorf("branch pattern %q must contain at most one wildcard", pattern)
	}
	literal := pattern
	hasWildcard := strings.HasSuffix(pattern, "*")
	if star := strings.IndexByte(pattern, '*'); star >= 0 {
		if !hasWildcard {
			return fmt.Errorf("branch pattern %q must end with its wildcard", pattern)
		}
		literal = pattern[:star]
	}
	if literal == "" {
		return nil
	}
	if literal == "@" || literal[0] == '/' || (!hasWildcard && strings.HasSuffix(literal, "/")) ||
		strings.Contains(literal, "//") || strings.Contains(literal, "..") || strings.Contains(literal, "@{") {
		return fmt.Errorf("branch pattern %q is not valid Git branch syntax", pattern)
	}
	for _, character := range literal {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("branch pattern %q contains a control character", pattern)
		}
	}
	if strings.ContainsAny(literal, " \\~^:?[") {
		return fmt.Errorf("branch pattern %q contains a character that is not valid in a ref name", pattern)
	}
	components := strings.Split(strings.TrimSuffix(literal, "/"), "/")
	for _, component := range components {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("branch pattern %q has an invalid ref component", pattern)
		}
	}
	return nil
}

func matchPattern(pattern, branch string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(branch, pattern[:len(pattern)-1])
	}
	return pattern == branch
}

func validateCheckName(name string) error {
	if name == "" {
		return errors.New("check name is empty")
	}
	if len(name) > MaximumCheckNameBytes {
		return fmt.Errorf("check name exceeds %d bytes", MaximumCheckNameBytes)
	}
	if name != strings.TrimSpace(name) || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("check name %q contains whitespace or a control character", name)
	}
	return nil
}

func validateCheckCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("check command is empty")
	}
	if len(command) > MaximumCheckCommandBytes {
		return fmt.Errorf("check command exceeds %d bytes", MaximumCheckCommandBytes)
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return errors.New("check command contains a control character")
	}
	return nil
}

func validateLimit(name string, value, minimum, maximum int64) error {
	if value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %d and %d, got %d", name, minimum, maximum, value)
	}
	return nil
}

// OperatorPolicy is the administrator-selected check policy. It can restrict
// which events may run and cap the requested limits. It is never read from the
// committed workflow, so a repository cannot elevate itself.
type OperatorPolicy struct {
	// AllowedEvents is a nonempty subset of push and pull_request.
	AllowedEvents []string
	// MaxTimeoutMS and MaxOutputLimitBytes cap requested values.
	MaxTimeoutMS        int64
	MaxOutputLimitBytes int64
}

// Effective is the tightened plan that event reconciliation admits as jobs.
type Effective struct {
	// Events are the enabled event names in sorted order.
	Events []string
	// Branches carries the selected patterns per event. A missing or empty
	// list means every branch.
	Branches map[string][]string
	// TimeoutMS and OutputLimitBytes are the effective limits.
	TimeoutMS        int64
	OutputLimitBytes int64
}

// Enabled reports whether the event survived the policy intersection.
func (effective Effective) Enabled(event string) bool {
	for _, name := range effective.Events {
		if name == event {
			return true
		}
	}
	return false
}

// MatchesBranch reports whether the enabled event selects the branch. A nil or
// empty pattern list matches every branch.
func (effective Effective) MatchesBranch(event, branch string) bool {
	if !effective.Enabled(event) {
		return false
	}
	patterns := effective.Branches[event]
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matchPattern(pattern, branch) {
			return true
		}
	}
	return false
}

// Tighten intersects the workflow events with the operator policy and lowers
// requested limits to the policy caps. Unspecified limits use the bounded
// defaults. An empty intersection is a valid plan that runs nothing.
func Tighten(document Document, policy OperatorPolicy) (Effective, error) {
	allowed, err := normalizeAllowedEvents(policy.AllowedEvents)
	if err != nil {
		return Effective{}, err
	}
	if derived := validatePolicyLimit("max_timeout_ms", policy.MaxTimeoutMS, MinimumTimeoutMS, MaximumTimeoutMS); derived != nil {
		return Effective{}, derived
	}
	if derived := validatePolicyLimit("max_output_limit_bytes", policy.MaxOutputLimitBytes, MinimumOutputLimitBytes, MaximumOutputLimitBytes); derived != nil {
		return Effective{}, derived
	}
	effective := Effective{
		TimeoutMS:        effectiveLimit(document.Limits.TimeoutMS, DefaultTimeoutMS, policy.MaxTimeoutMS),
		OutputLimitBytes: effectiveLimit(document.Limits.OutputLimitBytes, DefaultOutputLimitBytes, policy.MaxOutputLimitBytes),
		Branches:         make(map[string][]string),
	}
	for _, name := range allowed {
		event := document.Events.Event(name)
		if event == nil {
			continue
		}
		effective.Events = append(effective.Events, name)
		if len(event.Branches) != 0 {
			effective.Branches[name] = event.Branches
		}
	}
	sort.Strings(effective.Events)
	return effective, nil
}

func normalizeAllowedEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, errors.New("operator policy allows no event")
	}
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		if event != EventPush && event != EventPullRequest {
			return nil, fmt.Errorf("operator policy allows the unknown event %q", event)
		}
		if seen[event] {
			return nil, fmt.Errorf("operator policy repeats the event %q", event)
		}
		seen[event] = true
	}
	normalized := make([]string, 0, len(seen))
	for event := range seen {
		normalized = append(normalized, event)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func validatePolicyLimit(name string, value, minimum, maximum int64) error {
	if value <= 0 {
		return fmt.Errorf("operator policy %s is not set", name)
	}
	return validateLimit(name, value, minimum, maximum)
}

func effectiveLimit(requested, fallback, cap int64) int64 {
	value := requested
	if value <= 0 {
		value = fallback
	}
	if value > cap {
		value = cap
	}
	return value
}

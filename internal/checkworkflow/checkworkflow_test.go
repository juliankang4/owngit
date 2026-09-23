package checkworkflow

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const validDocument = `{
  "version": 1,
  "events": {
    "push": {"branches": ["main", "release/*"]},
    "pull_request": {}
  },
  "checks": [
    {"name": "unit", "command": "go test ./..."}
  ],
  "limits": {"timeout_ms": 120000, "output_limit_bytes": 4096}
}`

func TestParseValidDocument(t *testing.T) {
	document, err := Parse([]byte(validDocument))
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != Version || len(document.Checks) != 1 || document.Checks[0].Name != "unit" {
		t.Fatalf("parsed document=%+v", document)
	}
	if document.Limits.TimeoutMS != 120000 || document.Limits.OutputLimitBytes != 4096 {
		t.Fatalf("parsed limits=%+v", document.Limits)
	}
	if names := document.EventNames(); len(names) != 2 || names[0] != EventPullRequest || names[1] != EventPush {
		t.Fatalf("enabled events=%v", names)
	}
	if !document.Events.Push.Match("main") || !document.Events.Push.Match("release/1.2") || document.Events.Push.Match("dev") {
		t.Fatal("push branch selection is wrong")
	}
	if !document.Events.PullRequest.Match("anything") || len(document.Events.PullRequest.Branches) != 0 {
		t.Fatal("an empty branch selection must match every branch")
	}
}

func TestParseMinimalDocumentUsesDefaults(t *testing.T) {
	document, err := Parse([]byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"true"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if document.Limits.TimeoutMS != 0 || document.Limits.OutputLimitBytes != 0 {
		t.Fatalf("unspecified limits=%+v", document.Limits)
	}
}

func TestParseRejectsDuplicateFields(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		content string
	}{
		{name: "top level", field: "version", content: `{"version":1,"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "nested check", field: "name", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","name":"m","command":"c"}]}`},
		{name: "nested event", field: "branches", content: `{"version":1,"events":{"push":{"branches":["a"],"branches":["b"]}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "nested limit", field: "timeout_ms", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}],"limits":{"timeout_ms":1000,"timeout_ms":2000}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), "duplicate field") || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("duplicate %s error=%v", test.field, err)
			}
		})
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		content string
	}{
		{name: "top level", field: "secrets", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}],"secrets":{}}`},
		{name: "event", field: "paths", content: `{"version":1,"events":{"push":{"paths":["a"]}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "merge event", field: "merge", content: `{"version":1,"events":{"merge":{}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "check", field: "shell", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c","shell":"bash"}]}`},
		{name: "limits", field: "cpu", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}],"limits":{"cpu":2}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("unknown %s error=%v", test.field, err)
			}
		})
	}
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		content  string
	}{
		{name: "array root", fragment: "cannot unmarshal", content: `[]`},
		{name: "trailing data", fragment: "trailing", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]} {}`},
		{name: "missing version", fragment: "no version", content: `{"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "unsupported version", fragment: "unsupported check workflow version", content: `{"version":2,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "fractional version", fragment: "cannot unmarshal", content: `{"version":1.5,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "missing events", fragment: "no events", content: `{"version":1,"checks":[{"name":"n","command":"c"}]}`},
		{name: "no enabled event", fragment: "enables no event", content: `{"version":1,"events":{},"checks":[{"name":"n","command":"c"}]}`},
		{name: "missing checks", fragment: "no checks", content: `{"version":1,"events":{"push":{}}}`},
		{name: "empty checks", fragment: "no checks", content: `{"version":1,"events":{"push":{}},"checks":[]}`},
		{name: "check without name", fragment: "needs a name", content: `{"version":1,"events":{"push":{}},"checks":[{"command":"c"}]}`},
		{name: "check without command", fragment: "needs a name", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n"}]}`},
		{name: "duplicate check name", fragment: "repeats the check name", content: `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"a"},{"name":"n","command":"b"}]}`},
		{name: "empty branch list", fragment: "empty branch list", content: `{"version":1,"events":{"push":{"branches":[]}},"checks":[{"name":"n","command":"c"}]}`},
		{name: "trimmed check name", fragment: "whitespace", content: `{"version":1,"events":{"push":{}},"checks":[{"name":" n ","command":"c"}]}`},
		{name: "newline in command", fragment: "control character", content: "{\"version\":1,\"events\":{\"push\":{}},\"checks\":[{\"name\":\"n\",\"command\":\"a\\nb\"}]}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("error=%v want %q", err, test.fragment)
			}
		})
	}
}

func TestParseRejectsMalformedBranchPatterns(t *testing.T) {
	for _, pattern := range []string{
		"", "a*b", "**", "a**", "/main", "main/", "a//b", "a..b", "a@{b}", "@",
		"main 1", "main~1", "main^", "main?", "main[", "main.", ".hidden", "a/.hidden", "a.lock", "a/a.lock",
		"a.lock/*", "\x01main", "main\x7f", strings.Repeat("x", MaximumBranchPatternBytes+1),
	} {
		t.Run(pattern, func(t *testing.T) {
			content := fmt.Sprintf(`{"version":1,"events":{"push":{"branches":[%q]}},"checks":[{"name":"n","command":"c"}]}`, pattern)
			_, err := Parse([]byte(content))
			if err == nil {
				t.Fatalf("accepted branch pattern %q", pattern)
			}
		})
	}
	for _, pattern := range []string{"*", "main", "release/*", "feature/nested/*", "release/v1.0", "feature/a.locked"} {
		t.Run("valid "+pattern, func(t *testing.T) {
			content := fmt.Sprintf(`{"version":1,"events":{"push":{"branches":[%q]}},"checks":[{"name":"n","command":"c"}]}`, pattern)
			if _, err := Parse([]byte(content)); err != nil {
				t.Fatalf("rejected branch pattern %q: %v", pattern, err)
			}
		})
	}
}

func TestParseRejectsInvalidUTF8BeforeJSONDecode(t *testing.T) {
	content := []byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`)
	content[len(content)-2] = 0xff
	if _, err := Parse(content); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error=%v", err)
	}
}

func TestParseRejectsOutOfRangeLimits(t *testing.T) {
	for _, test := range []struct {
		limits   string
		fragment string
	}{
		{`{"timeout_ms":0}`, "timeout_ms must be between"},
		{`{"timeout_ms":999}`, "timeout_ms must be between"},
		{`{"timeout_ms":86400001}`, "timeout_ms must be between"},
		{`{"output_limit_bytes":0}`, "output_limit_bytes must be between"},
		{`{"output_limit_bytes":1023}`, "output_limit_bytes must be between"},
		{`{"output_limit_bytes":67108865}`, "output_limit_bytes must be between"},
		{`{"timeout_ms":-1}`, "timeout_ms must be between"},
		{`{"timeout_ms":1000,"output_limit_bytes":1024}`, ""},
	} {
		t.Run(test.limits, func(t *testing.T) {
			content := fmt.Sprintf(`{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}],"limits":%s}`, test.limits)
			_, err := Parse([]byte(content))
			if test.fragment == "" {
				if err != nil {
					t.Fatalf("valid limits rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("limits error=%v want %q", err, test.fragment)
			}
		})
	}
}

func TestParseRejectsOversizedInput(t *testing.T) {
	content := make([]byte, MaximumBytes+1)
	for index := range content {
		content[index] = ' '
	}
	if _, err := Parse(content); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized error=%v", err)
	}
	checks := make([]string, 0, MaximumChecks)
	for index := 0; index <= MaximumChecks; index++ {
		checks = append(checks, fmt.Sprintf(`{"name":"n%d","command":"c"}`, index))
	}
	content = []byte(fmt.Sprintf(`{"version":1,"events":{"push":{}},"checks":[%s]}`, strings.Join(checks, ",")))
	if _, err := Parse(content); err == nil || !strings.Contains(err.Error(), "the maximum is") {
		t.Fatalf("too many checks error=%v", err)
	}
}

func TestTightenUsesRequestedValuesBelowCaps(t *testing.T) {
	document, err := Parse([]byte(validDocument))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := Tighten(document, OperatorPolicy{
		AllowedEvents: []string{EventPush, EventPullRequest},
		MaxTimeoutMS:  MaximumTimeoutMS, MaxOutputLimitBytes: MaximumOutputLimitBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if effective.TimeoutMS != 120000 || effective.OutputLimitBytes != 4096 {
		t.Fatalf("effective limits=%d/%d", effective.TimeoutMS, effective.OutputLimitBytes)
	}
	if !effective.Enabled(EventPush) || !effective.MatchesBranch(EventPush, "release/x") || effective.MatchesBranch(EventPush, "dev") {
		t.Fatal("effective push selection is wrong")
	}
	if !effective.MatchesBranch(EventPullRequest, "any") {
		t.Fatal("effective pull request selection is wrong")
	}
}

func TestTightenAppliesDefaultsAndCaps(t *testing.T) {
	document, err := Parse([]byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := Tighten(document, OperatorPolicy{
		AllowedEvents: []string{EventPush}, MaxTimeoutMS: 60000, MaxOutputLimitBytes: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if effective.TimeoutMS != 60000 || effective.OutputLimitBytes != 2048 {
		t.Fatalf("capped defaults=%d/%d", effective.TimeoutMS, effective.OutputLimitBytes)
	}
	if effective.Enabled(EventPullRequest) {
		t.Fatal("pull request must be excluded by the policy")
	}
}

func TestTightenIntersectsEventsCanonically(t *testing.T) {
	document, err := Parse([]byte(`{"version":1,"events":{"push":{"branches":["main"]},"pull_request":{"branches":["main"]}},"checks":[{"name":"n","command":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := Tighten(document, OperatorPolicy{
		AllowedEvents: []string{EventPullRequest, EventPush},
		MaxTimeoutMS:  MaximumTimeoutMS, MaxOutputLimitBytes: MaximumOutputLimitBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective.Events) != 2 || effective.Events[0] != EventPullRequest || effective.Events[1] != EventPush {
		t.Fatalf("effective events=%v", effective.Events)
	}
	empty, err := Tighten(document, OperatorPolicy{
		AllowedEvents: []string{EventPush}, MaxTimeoutMS: MaximumTimeoutMS, MaxOutputLimitBytes: MaximumOutputLimitBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Enabled(EventPullRequest) || !empty.Enabled(EventPush) {
		t.Fatalf("single-event intersection=%v", empty.Events)
	}
}

func TestTightenRejectsInvalidPolicy(t *testing.T) {
	document, err := Parse([]byte(validDocument))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		policy   OperatorPolicy
		fragment string
	}{
		{name: "no event", policy: OperatorPolicy{MaxTimeoutMS: 1000, MaxOutputLimitBytes: 1024}, fragment: "allows no event"},
		{name: "unknown event", policy: OperatorPolicy{AllowedEvents: []string{"merge"}, MaxTimeoutMS: 1000, MaxOutputLimitBytes: 1024}, fragment: "unknown event"},
		{name: "repeated event", policy: OperatorPolicy{AllowedEvents: []string{"push", "push"}, MaxTimeoutMS: 1000, MaxOutputLimitBytes: 1024}, fragment: "repeats the event"},
		{name: "unset timeout", policy: OperatorPolicy{AllowedEvents: []string{"push"}, MaxOutputLimitBytes: 1024}, fragment: "max_timeout_ms"},
		{name: "small timeout", policy: OperatorPolicy{AllowedEvents: []string{"push"}, MaxTimeoutMS: 500, MaxOutputLimitBytes: 1024}, fragment: "max_timeout_ms"},
		{name: "unset output", policy: OperatorPolicy{AllowedEvents: []string{"push"}, MaxTimeoutMS: 1000}, fragment: "max_output_limit_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Tighten(document, test.policy)
			if err == nil || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("policy error=%v want %q", err, test.fragment)
			}
		})
	}
}

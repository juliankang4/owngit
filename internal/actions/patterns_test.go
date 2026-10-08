package actions

import (
	"reflect"
	"testing"
)

func TestPatterns(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		value    string
		match    bool
		code     string
	}{
		{"no filter", nil, "any/ref", true, ""},
		{"single star", []string{"feature/*"}, "feature/one", true, ""},
		{"star excludes slash", []string{"feature/*"}, "feature/one/two", false, ""},
		{"double star", []string{"feature/**"}, "feature/one/two", true, ""},
		{"globstar empty directory", []string{"**/README.md"}, "README.md", true, ""},
		{"globstar nested", []string{"src/**/*.go"}, "src/a/b/test.go", true, ""},
		{"question mark optional", []string{"*.jsx?"}, "page.js", true, ""},
		{"question mark present", []string{"*.jsx?"}, "page.jsx", true, ""},
		{"question mark not wildcard", []string{"*.jsx?"}, "page.jsy", false, ""},
		{"plus", []string{"v[12].[0-9]+.[0-9]+"}, "v2.10.22", true, ""},
		{"plus requires one", []string{"v[12].[0-9]+"}, "v2.", false, ""},
		{"case sensitive Git names", []string{"Main"}, "main", false, ""},
		{"escaped special", []string{`release/\*`}, "release/*", true, ""},
		{"Unicode bytes", []string{"릴리스/**"}, "릴리스/테스트", true, ""},
		{"ordered exclusion", []string{"release/**", "!release/old/**"}, "release/old/v1", false, ""},
		{"reinclude", []string{"release/**", "!release/old/**", "release/old/current"}, "release/old/current", true, ""},
		{"whole path", []string{"test.go"}, "src/test.go", false, ""},
		{"all negative refused", []string{"!main"}, "main", false, "workflow.wrong_type"},
		{"invalid range", []string{"[z-a]"}, "a", false, "workflow.wrong_type"},
		{"uppercase range", []string{"[A-Z]"}, "A", true, ""},
		{"uppercase excludes punctuation", []string{"[A-Z]"}, "_", false, ""},
		{"lowercase range", []string{"[a-z]"}, "m", true, ""},
		{"digit range", []string{"[0-9]"}, "5", true, ""},
		{"separate category ranges", []string{"[A-Z0-9a-z]"}, "z", true, ""},
		{"cross-case range", []string{"[A-z]"}, "_", false, "workflow.wrong_type"},
		{"digit to letter range", []string{"[0-z]"}, "_", false, "workflow.wrong_type"},
		{"trailing range marker", []string{"[a-]"}, "a", false, "workflow.wrong_type"},
		{"chained range marker", []string{"[a-b-c]"}, "-", false, "workflow.wrong_type"},
		{"unclosed class", []string{"[abc"}, "a", false, "workflow.wrong_type"},
		{"dangling escape", []string{`abc\`}, "abc", false, "workflow.wrong_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchPatterns(tc.patterns, tc.value)
			if got != tc.match || errorCode(err) != tc.code {
				t.Fatalf("got %t, %v; want %t, %s", got, err, tc.match, tc.code)
			}
		})
	}
}

func TestMatchEvent(t *testing.T) {
	cases := []struct {
		name    string
		trigger Trigger
		event   Event
		want    FilterDecision
	}{
		{name: "matching branch", trigger: Trigger{Branches: []string{"main"}}, event: Event{Name: "push", RefName: "main"}, want: FilterDecision{Matched: true}},
		{name: "branch excludes", trigger: Trigger{BranchesIgnore: []string{"docs/**"}}, event: Event{Name: "push", RefName: "docs/a"}},
		{name: "tag only", trigger: Trigger{Tags: []string{"v*"}}, event: Event{Name: "push", RefName: "main"}, want: FilterDecision{Note: &Message{Code: "workflow.tags"}}},
		{name: "tag event", event: Event{Name: "push", RefType: "tag"}, want: FilterDecision{Note: &Message{Code: "workflow.tags"}}},
		{name: "PR target branch", trigger: Trigger{Branches: []string{"main"}, Types: []string{"opened"}}, event: Event{Name: "pull_request", RefName: "feature", BaseRef: "main", Action: "opened"}, want: FilterDecision{Matched: true}},
		{name: "PR type excludes", trigger: Trigger{Types: []string{"reopened"}}, event: Event{Name: "pull_request", Action: "synchronize"}},
		{name: "empty changed list", trigger: Trigger{Paths: []string{"**"}}, event: Event{Name: "push", Changed: ChangedPaths{Complete: true}}},
		{name: "complete matching path", trigger: Trigger{Paths: []string{"**.go"}}, event: Event{Name: "push", Changed: ChangedPaths{Complete: true, Files: []string{"README.md", "src/a.go"}}}, want: FilterDecision{Matched: true}},
		{name: "branches and paths both required", trigger: Trigger{Branches: []string{"main"}, Paths: []string{"**.go"}}, event: Event{Name: "push", RefName: "other", Changed: ChangedPaths{Complete: true, Files: []string{"a.go"}}}},
		{name: "all paths ignored", trigger: Trigger{PathsIgnore: []string{"docs/**"}}, event: Event{Name: "push", Changed: ChangedPaths{Complete: true, Files: []string{"docs/a", "docs/b"}}}},
		{name: "one path outside ignore", trigger: Trigger{PathsIgnore: []string{"docs/**"}}, event: Event{Name: "push", Changed: ChangedPaths{Complete: true, Files: []string{"docs/a", "a.go"}}}, want: FilterDecision{Matched: true}},
		{name: "cut list decides include", trigger: Trigger{Paths: []string{"**.go"}}, event: Event{Name: "push", Changed: ChangedPaths{Files: []string{"a.go"}}}, want: FilterDecision{Matched: true}},
		{name: "cut list decides ignore", trigger: Trigger{PathsIgnore: []string{"docs/**"}}, event: Event{Name: "push", Changed: ChangedPaths{Files: []string{"a.go"}}}, want: FilterDecision{Matched: true}},
		{name: "cut list unknown", trigger: Trigger{Paths: []string{"**.go"}}, event: Event{Name: "push", Changed: ChangedPaths{Files: []string{"README.md"}, Reason: "file limit"}}, want: FilterDecision{Unknown: true, Note: &Message{Code: "note.paths_unknown"}}},
		{name: "missing base unknown", trigger: Trigger{PathsIgnore: []string{"docs/**"}}, event: Event{Name: "push", Changed: ChangedPaths{Reason: "base unavailable"}}, want: FilterDecision{Unknown: true, Note: &Message{Code: "note.paths_unknown"}}},
		{name: "rerun bypasses paths", trigger: Trigger{Paths: []string{"**.go"}}, event: Event{Name: "push", BypassPaths: true}, want: FilterDecision{Matched: true}},
		{name: "dispatch", event: Event{Name: "workflow_dispatch"}, want: FilterDecision{Matched: true}},
		{name: "schedule", event: Event{Name: "schedule"}, want: FilterDecision{Matched: true}},
		{name: "unobserved GitHub event", event: Event{Name: "pull_request_target"}, want: FilterDecision{Note: &Message{Code: "workflow.event_pr_target"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &Workflow{Events: map[string]Trigger{tc.event.Name: tc.trigger}}
			got, err := MatchEvent(w, tc.event)
			if err != nil {
				t.Fatal(err)
			}
			if got.Note != nil {
				if got.Note.Detail == "" {
					t.Fatal("note has no remedy")
				}
				got.Note = &Message{Code: got.Note.Code}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

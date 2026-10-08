package actions

import (
	"reflect"
	"testing"
)

func TestExpandMatrix(t *testing.T) {
	cases := []struct {
		name   string
		matrix map[string]any
		order  []string
		limit  int
		want   []map[string]any
		code   string
	}{
		{name: "no matrix", limit: 16, want: []map[string]any{{}}},
		{name: "axis order", matrix: map[string]any{"os": []any{"linux", "macos"}, "version": []any{float64(1), float64(2)}}, order: []string{"version", "os"}, limit: 16, want: []map[string]any{{"version": float64(1), "os": "linux"}, {"version": float64(1), "os": "macos"}, {"version": float64(2), "os": "linux"}, {"version": float64(2), "os": "macos"}}},
		{name: "exclude subset", matrix: map[string]any{"a": []any{float64(1), float64(2)}, "b": []any{"x", "y"}, "exclude": []any{map[string]any{"a": float64(1)}}}, limit: 16, want: []map[string]any{{"a": float64(2), "b": "x"}, {"a": float64(2), "b": "y"}}},
		{name: "include overlays nonaxis", matrix: map[string]any{"a": []any{"one", "two"}, "include": []any{map[string]any{"color": "green"}, map[string]any{"a": "one", "color": "pink"}, map[string]any{"a": "three", "color": "blue"}}}, limit: 16, want: []map[string]any{{"a": "one", "color": "pink"}, {"a": "two", "color": "green"}, {"a": "three", "color": "blue"}}},
		{name: "include does not combine additions", matrix: map[string]any{"a": []any{"one"}, "include": []any{map[string]any{"a": "two"}, map[string]any{"a": "two", "b": "extra"}}}, limit: 16, want: []map[string]any{{"a": "one"}, {"a": "two"}, {"a": "two", "b": "extra"}}},
		{name: "include restores excluded", matrix: map[string]any{"a": []any{"one", "two"}, "exclude": []any{map[string]any{"a": "two"}}, "include": []any{map[string]any{"a": "two", "experimental": true}}}, limit: 16, want: []map[string]any{{"a": "one"}, {"a": "two", "experimental": true}}},
		{name: "include only", matrix: map[string]any{"include": []any{map[string]any{"a": "one"}, map[string]any{"a": "two"}}}, limit: 16, want: []map[string]any{{"a": "one"}, {"a": "two"}}},
		{name: "mapping axis", matrix: map[string]any{"node": []any{map[string]any{"version": float64(20), "env": "test"}}}, limit: 16, want: []map[string]any{{"node": map[string]any{"version": float64(20), "env": "test"}}}},
		{name: "null scalar axis", matrix: map[string]any{"a": []any{nil}}, limit: 16, want: []map[string]any{{"a": nil}}},
		{name: "exclude all", matrix: map[string]any{"a": []any{"one"}, "exclude": []any{map[string]any{"a": "one"}}}, limit: 16},
		{name: "sixteen jobs", matrix: map[string]any{"a": matrixNumbers(16)}, limit: 16, want: matrixCombinations(16)},
		{name: "seventeenth stops", matrix: map[string]any{"a": matrixNumbers(1000)}, limit: 16, code: "workflow.too_many_jobs"},
		{name: "remaining workflow capacity", matrix: map[string]any{"a": []any{float64(1), float64(2)}}, limit: 1, code: "workflow.too_many_jobs"},
		{name: "include bound", matrix: map[string]any{"include": []any{map[string]any{"a": "one"}, map[string]any{"a": "two"}}}, limit: 1, code: "workflow.too_many_jobs"},
		{name: "empty axis", matrix: map[string]any{"a": []any{}}, limit: 16, code: "workflow.wrong_type"},
		{name: "scalar axis", matrix: map[string]any{"a": "one"}, limit: 16, code: "workflow.wrong_type"},
		{name: "nested array axis", matrix: map[string]any{"a": []any{[]any{"one"}}}, limit: 16, code: "workflow.wrong_type"},
		{name: "bad include", matrix: map[string]any{"include": []any{"one"}}, limit: 16, code: "workflow.wrong_type"},
		{name: "unknown exclude key", matrix: map[string]any{"a": []any{"one"}, "exclude": []any{map[string]any{"absent": "x"}}}, limit: 16, code: "workflow.wrong_type"},
		{name: "invalid order", matrix: map[string]any{"a": []any{"one"}}, order: []string{"absent"}, limit: 16, code: "workflow.wrong_type"},
		{name: "large excluded subtree pruned", matrix: map[string]any{"a": matrixNumbers(1000), "b": matrixNumbers(1000), "exclude": []any{map[string]any{}}}, order: []string{"a", "b"}, limit: 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := cloneTestJSON(tc.matrix)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ExpandMatrix(tc.matrix, tc.order, tc.limit)
			if errorCode(err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
			if err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			after, _ := cloneTestJSON(tc.matrix)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("matrix input mutated")
			}
		})
	}
}

func matrixNumbers(count int) []any {
	out := make([]any, count)
	for i := range out {
		out[i] = float64(i)
	}
	return out
}

func matrixCombinations(count int) []map[string]any {
	out := make([]map[string]any, count)
	for i := range out {
		out[i] = map[string]any{"a": float64(i)}
	}
	return out
}

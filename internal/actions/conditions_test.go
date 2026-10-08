package actions

import (
	"fmt"
	"math"
	"testing"
)

func TestStepCondition(t *testing.T) {
	type conditionCase struct {
		name      string
		condition string
		success   bool
		value     any
		custom    bool
		want      bool
		wantError bool
	}
	tests := []conditionCase{
		{name: "empty before failure", success: true, want: true},
		{name: "empty after failure"},
		{name: "true before failure", condition: "true", success: true, want: true},
		{name: "true after failure", condition: "true"},
		{name: "ordinary comparison after failure", condition: "github.ref == 'refs/heads/main'"},
		{name: "success after failure", condition: "success()"},
		{name: "failure after failure", condition: "failure()", want: true},
		{name: "always after failure", condition: "always()", want: true},
		{name: "cancelled false", condition: "cancelled()"},
		{name: "quoted function does not bypass success", condition: "'always()'"},
		{name: "escaped quote does not bypass success", condition: "'it''s failure()'"},
		{name: "case insensitive function", condition: "ALWAYS ()", custom: true, value: true, want: true},
		{name: "unknown function error", condition: "unknown()", success: true, wantError: true},
		{name: "null is false", condition: "value", success: true, custom: true},
		{name: "empty string is false", condition: "value", success: true, custom: true, value: ""},
		{name: "nonempty string is true", condition: "value", success: true, custom: true, value: "false", want: true},
		{name: "zero is false", condition: "value", success: true, custom: true, value: float64(0)},
		{name: "nonzero is true", condition: "value", success: true, custom: true, value: float64(1), want: true},
		{name: "negative zero is false", condition: "value", success: true, custom: true, value: math.Copysign(0, -1)},
		{name: "float32 negative zero is false", condition: "value", success: true, custom: true, value: float32(math.Copysign(0, -1))},
		{name: "NaN is false", condition: "value", success: true, custom: true, value: math.NaN()},
		{name: "float32 NaN is false", condition: "value", success: true, custom: true, value: float32(math.NaN())},
		{name: "object is true", condition: "value", success: true, custom: true, value: map[string]any{}, want: true},
	}
	type integerValue int64
	type decimalValue float32
	for _, numbers := range []struct{ zero, nonzero any }{
		{int(0), int(-1)}, {int8(0), int8(-1)}, {int16(0), int16(-1)}, {int32(0), int32(-1)}, {int64(0), int64(-1)},
		{uint(0), uint(1)}, {uint8(0), uint8(1)}, {uint16(0), uint16(1)}, {uint32(0), uint32(1)}, {uint64(0), uint64(1)}, {uintptr(0), uintptr(1)},
		{float32(0), float32(-1)}, {integerValue(0), integerValue(-1)}, {decimalValue(0), decimalValue(-1)},
	} {
		tests = append(tests,
			conditionCase{name: fmt.Sprintf("%T zero is false", numbers.zero), condition: "value", success: true, custom: true, value: numbers.zero},
			conditionCase{name: fmt.Sprintf("%T nonzero is true", numbers.nonzero), condition: "value", success: true, custom: true, value: numbers.nonzero, want: true})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contexts := map[string]any{"success": test.success, "failure": !test.success, "cancelled": false}
			evaluator := Evaluator(tinyEvaluator)
			if test.custom {
				evaluator = func(string, string, map[string]any) (any, error) { return test.value, nil }
			}
			actual, err := stepCondition(test.condition, test.success, evaluator, contexts)
			if actual != test.want || (err != nil) != test.wantError {
				t.Fatalf("got %v, %v", actual, err)
			}
		})
	}
}

func TestEvaluatorBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		evaluator Evaluator
		want      string
		wantError bool
	}{
		{name: "literal without evaluator", input: "literal", want: "literal"},
		{name: "template needs evaluator", input: "${{ env.VALUE }}", wantError: true},
		{name: "bound error preserved", input: "x", evaluator: func(string, string, map[string]any) (any, error) { return nil, fmt.Errorf("expression bound") }, wantError: true},
		{name: "non scalar refused", input: "x", evaluator: func(string, string, map[string]any) (any, error) { return map[string]any{}, nil }, wantError: true},
		{name: "integer identity", input: "x", evaluator: func(string, string, map[string]any) (any, error) { return int64(17), nil }, want: "17"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := evaluateText(test.evaluator, "step.run", test.input, nil)
			if value != test.want || (err != nil) != test.wantError {
				t.Fatal(value, err)
			}
		})
	}
}

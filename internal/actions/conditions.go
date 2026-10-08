package actions

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func hasStatusFunction(text string) bool {
	quoted := false
	for index := 0; index < len(text); {
		if text[index] == '\'' {
			if quoted && index+1 < len(text) && text[index+1] == '\'' {
				index += 2
				continue
			}
			quoted = !quoted
			index++
			continue
		}
		if quoted || !identifierByte(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && identifierByte(text[index]) {
			index++
		}
		name := strings.ToLower(text[start:index])
		end := index
		for end < len(text) && unicode.IsSpace(rune(text[end])) {
			end++
		}
		if end < len(text) && text[end] == '(' && (name == "success" || name == "failure" || name == "always" || name == "cancelled") {
			return true
		}
	}
	return false
}

func identifierByte(char byte) bool {
	return char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func stepCondition(text string, successful bool, evaluator Evaluator, contexts map[string]any) (bool, error) {
	if !hasStatusFunction(text) && !successful {
		return false, nil
	}
	if strings.TrimSpace(text) == "" {
		return successful, nil
	}
	if evaluator == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return false, fmt.Errorf("workflow.expression: no evaluator for step.if")
		}
	}
	value, err := evaluator("step.if", text, contexts)
	if err != nil {
		return false, err
	}
	switch value := value.(type) {
	case nil:
		return false, nil
	case bool:
		return value, nil
	case string:
		return value != "", nil
	default:
		numeric := reflect.ValueOf(value)
		switch numeric.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return numeric.Int() != 0, nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return numeric.Uint() != 0, nil
		case reflect.Float32, reflect.Float64:
			number := numeric.Float()
			return number != 0 && !math.IsNaN(number), nil
		default:
			return true, nil
		}
	}
}

func evaluateBool(evaluator Evaluator, key, text string, contexts map[string]any) (bool, error) {
	if text == "" {
		return false, nil
	}
	value, err := evaluate(evaluator, key, text, contexts)
	if err != nil {
		return false, err
	}
	if flag, ok := value.(bool); ok {
		return flag, nil
	}
	if value == "true" {
		return true, nil
	}
	if value == "false" {
		return false, nil
	}
	return false, fmt.Errorf("workflow.expression: %s must be true or false", key)
}

func evaluateTimeout(evaluator Evaluator, key, text string, contexts map[string]any, fallback time.Duration) (time.Duration, error) {
	if text == "" {
		return fallback, nil
	}
	value, err := evaluateText(evaluator, key, text, contexts)
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 || minutes >= float64(math.MaxInt64/int64(time.Minute)) {
		return 0, fmt.Errorf("workflow.expression: %s must be a positive minute count", key)
	}
	return max(time.Nanosecond, time.Duration(minutes*float64(time.Minute))), nil
}

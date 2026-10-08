package actions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func callExpression(name string, args []any, ctx EvalContext, steps *int) (any, error) {
	switch name {
	case "success":
		return ctx.Success, nil
	case "failure":
		return ctx.Failure, nil
	case "cancelled":
		return ctx.Cancelled, nil
	case "always":
		return true, nil
	case "fromjson":
		s, err := ScalarString(args[0])
		if err != nil {
			return nil, err
		}
		if len(s) > MaxValueBytes {
			return nil, limitError("fromJSON input", MaxValueBytes)
		}
		depth, quoted, escaped := 0, false, false
		for _, c := range s {
			if quoted {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					quoted = false
				}
				continue
			}
			switch c {
			case '"':
				quoted = true
			case '[', '{':
				depth++
				if depth > MaxExpressionDepth {
					return nil, limitError("fromJSON depth", MaxExpressionDepth)
				}
			case ']', '}':
				depth--
			}
		}
		var result any
		if err := json.Unmarshal([]byte(s), &result); err != nil {
			// Do not include runtime input, which can hold a secret.
			return nil, expressionError("fromJSON", "valid JSON")
		}
		if _, err := checkedValue(result); err != nil {
			return nil, err
		}
		value, err := expressionValues(result, map[string]any{}, 0, steps)
		if _, ok := value.(map[string]any); ok && ctx.matrixOrders != nil && err == nil {
			var order []string
			order, err = jsonMatrixKeys(s)
			ctx.matrixOrders[fmt.Sprintf("%p", value)] = order
		}
		return value, err
	case "tojson":
		size, err := preflightJSONSize(args[0], 0)
		if err != nil {
			return nil, err
		}
		if size > MaxValueBytes {
			return nil, limitError("toJSON output", MaxValueBytes)
		}
		out := &boundedBuffer{limit: MaxValueBytes + 1}
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(nativeValue(args[0])); err != nil {
			return nil, expressionError("toJSON", "a JSON value within 64 KiB")
		}
		return strings.TrimSuffix(out.String(), "\n"), nil
	case "contains":
		if values, ok := arrayValues(args[0]); ok {
			for _, value := range values {
				if equalValue(value, args[1]) {
					return true, nil
				}
			}
			return false, nil
		}
	}
	if name == "contains" || name == "startswith" || name == "endswith" {
		a, err := ScalarString(args[0])
		if err != nil {
			return nil, err
		}
		b, err := ScalarString(args[1])
		if err != nil {
			return nil, err
		}
		a, b = strings.ToLower(a), strings.ToLower(b)
		switch name {
		case "contains":
			return strings.Contains(a, b), nil
		case "startswith":
			return strings.HasPrefix(a, b), nil
		default:
			return strings.HasSuffix(a, b), nil
		}
	}
	if name == "join" {
		separator := ","
		if len(args) == 2 {
			var err error
			separator, err = ScalarString(args[1])
			if err != nil {
				return nil, err
			}
		}
		values, ok := arrayValues(args[0])
		if !ok {
			return ScalarString(args[0])
		}
		out := &boundedBuffer{limit: MaxValueBytes}
		for i, value := range values {
			text, err := ScalarString(value)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				if _, err := out.Write([]byte(separator)); err != nil {
					return nil, err
				}
			}
			if _, err := out.Write([]byte(text)); err != nil {
				return nil, err
			}
		}
		return out.String(), nil
	}
	if name == "format" {
		text, err := ScalarString(args[0])
		if err != nil {
			return nil, err
		}
		out := &boundedBuffer{limit: MaxValueBytes}
		for i := 0; i < len(text); {
			value := text[i : i+1]
			i++
			if value == "{" || value == "}" {
				if i < len(text) && text[i:i+1] == value {
					i++
				} else if value == "{" {
					end := strings.IndexByte(text[i:], '}')
					if end < 0 {
						return nil, expressionError("format", "a closed placeholder")
					}
					index, err := strconv.Atoi(text[i : i+end])
					if err != nil || index < 0 || index >= len(args)-1 {
						return nil, expressionError("format", "a placeholder with a supplied argument")
					}
					value, err = ScalarString(args[index+1])
					if err != nil {
						return nil, err
					}
					i += end + 1
				} else {
					return nil, expressionError("format", "escaped braces")
				}
			}
			if _, err := out.Write([]byte(value)); err != nil {
				return nil, err
			}
		}
		return out.String(), nil
	}
	return nil, expressionError("expression", "a supported function")
}

func jsonMatrixKeys(text string) (keys []string, err error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	_, err = decoder.Token()
	seen := map[string]bool{}
	for err == nil && decoder.More() {
		var key json.Token
		key, err = decoder.Token()
		if err != nil {
			break
		}
		var value json.RawMessage
		err = decoder.Decode(&value)
		name := key.(string)
		if name != "include" && name != "exclude" && !seen[name] {
			keys = append(keys, name)
			seen[name] = true
		}
	}
	return keys, err
}

func checkedValue(v any) (any, error) {
	steps := 0
	return checkedExpressionValue(v, &steps)
}

func checkedExpressionValue(v any, steps *int) (any, error) {
	remaining := MaxValueBytes
	var check func(any, int) error
	check = func(v any, depth int) error {
		*steps++
		if *steps > MaxEvaluationSteps || depth > MaxExpressionDepth {
			return limitError("expression value depth or nodes", MaxEvaluationSteps)
		}
		switch value := v.(type) {
		case *expressionEnvironment:
			return check(value.values, depth)
		case *expressionArray:
			return check(value.values, depth)
		case map[string]any:
			remaining -= 2
			for key, item := range value {
				remaining -= len(key)
				if err := check(item, depth+1); err != nil {
					return err
				}
			}
		case []any:
			remaining -= 2
			for _, item := range value {
				remaining--
				if err := check(item, depth+1); err != nil {
					return err
				}
			}
		default:
			text, err := ScalarString(v)
			if err != nil {
				return err
			}
			remaining -= len(text)
			if v == nil {
				remaining -= 4
			}
		}
		if remaining < 0 {
			return limitError("expression value", MaxValueBytes)
		}
		return nil
	}
	return v, check(v, 0)
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, limitError("expression value", b.limit)
	}
	return b.Buffer.Write(data)
}

// Refusal is a stable, structured workflow diagnostic, not a successful empty plan.
type Refusal struct{ Message }

func (r *Refusal) Error() string { return r.Code + ": " + r.Detail }

func refuse(code, path string, line int, detail string) error {
	return &Refusal{Message{Code: code, Path: path, Line: line, Detail: detail}}
}

func expressionError(key, expected string) error {
	return refuse("workflow.wrong_type", key, 0, fmt.Sprintf("%s must be %s.", key, expected))
}

func limitError(what string, limit int) error {
	return refuse("workflow.limit", what, 0, fmt.Sprintf("%s is over OwnGit's limit of %d.", what, limit))
}

func contextError(context, key, hint string) error {
	return refuse("workflow.context", key, 0, fmt.Sprintf("%s is not available in %s in OwnGit workflows. %s", context, key, hint))
}

func secretReferenceError(key string) error {
	return refuse("workflow.secret_ref", key, 0, "Name each secret directly, as `secrets.NAME`, and do not use secrets in `if`. To test a secret, put it in `env` and test the variable, for example `if: env.TOKEN != ''`.")
}

func tokenError(expr, key string) error {
	return refuse("workflow.token", key, 0, fmt.Sprintf("OwnGit gives workflows no GitHub token. Remove %s, or store a token of your own as a repository secret under another name.", expr))
}

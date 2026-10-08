package actions

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	MaxExpressionBytes = 4096
	MaxExpressionDepth = 32
	MaxValueBytes      = 64 << 10
	MaxEvaluationSteps = 10000
)

// EvalContext contains only contexts allowed by the workflow contract. Runtime
// secrets and step outputs belong here, never in a persisted PlanContext.
// Values.runner.os selects Windows' case-insensitive env dictionary; otherwise
// env names are case-sensitive.
type EvalContext struct {
	Values       map[string]any
	Success      bool
	Failure      bool
	Cancelled    bool
	matrixOrders map[string][]string
	matrixOrder  *[]string
}

type expression struct {
	op       string
	value    any
	children []*expression
}

type expressionParser struct {
	text, token string
	value       any
	pos         int
	key         string
	status      bool
	secrets     map[string]bool
}

// Eval evaluates an expression, with or without the ${{ }} delimiters.
func Eval(text, key string, ctx EvalContext) (any, error) {
	e, _, err := compileExpression(text, key)
	if err != nil {
		return nil, err
	}
	steps := 0
	ctx, err = prepareContext(ctx, &steps)
	if err != nil {
		return nil, err
	}
	value, err := e.evaluate(ctx, &steps)
	if ctx.matrixOrder != nil {
		*ctx.matrixOrder = ctx.matrixOrders[fmt.Sprintf("%p", value)]
	}
	return nativeValue(value), err
}

// EvalIf applies GitHub's implicit success() unless a status function is present.
func EvalIf(text, key string, ctx EvalContext) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return ctx.Success, nil
	}
	e, p, err := compileExpression(text, key)
	if err != nil {
		return false, err
	}
	if !p.status && !ctx.Success {
		return false, nil
	}
	steps := 0
	ctx, err = prepareContext(ctx, &steps)
	if err != nil {
		return false, err
	}
	v, err := e.evaluate(ctx, &steps)
	return Truthy(v), err
}

// EvalTemplate preserves a sole expression's JSON type. Mixed text converts
// scalars as GitHub does; arrays and objects cannot be interpolated as strings.
func EvalTemplate(text, key string, ctx EvalContext) (any, error) {
	parts, err := templateParts(text)
	if err != nil {
		return nil, err
	}
	if len(parts) == 1 && parts[0].expr {
		return Eval(parts[0].text, key, ctx)
	}
	var out strings.Builder
	for _, part := range parts {
		value := part.text
		if part.expr {
			v, err := Eval(part.text, key, ctx)
			if err != nil {
				return nil, err
			}
			value, err = ScalarString(v)
			if err != nil {
				return nil, err
			}
		}
		if len(value) > MaxValueBytes-out.Len() {
			return nil, limitError(key, MaxValueBytes)
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

// ValidateTemplate checks even expressions in branches that would not execute.
// It returns the directly named secrets for delivery at job start.
func ValidateTemplate(text, key string) ([]string, error) {
	parts, err := templateParts(text)
	if err != nil {
		return nil, err
	}
	secrets := map[string]bool{}
	if isIfKey(key) {
		parts = []templatePart{{text: text, expr: true}}
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
	}
	for _, part := range parts {
		if !part.expr {
			continue
		}
		_, p, err := compileExpression(part.text, key)
		if err != nil {
			return nil, err
		}
		for name := range p.secrets {
			secrets[name] = true
		}
	}
	return sortedKeys(secrets), nil
}

type templatePart struct {
	text string
	expr bool
}

func templateParts(text string) ([]templatePart, error) {
	var parts []templatePart
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			if text != "" || len(parts) == 0 {
				parts = append(parts, templatePart{text: text})
			}
			return parts, nil
		}
		if start > 0 {
			parts = append(parts, templatePart{text: text[:start]})
		}
		quoted, end := false, -1
		for i := start + 3; i < len(text)-1; i++ {
			if text[i] == '\'' {
				if quoted && text[i+1] == '\'' {
					i++
					continue
				}
				quoted = !quoted
			}
			if !quoted && text[i:i+2] == "}}" {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, expressionError("expression", "a closing }}")
		}
		parts = append(parts, templatePart{text: text[start+3 : end], expr: true})
		text = text[end+2:]
	}
}

func compileExpression(text, key string) (*expression, *expressionParser, error) {
	if len(text) > MaxExpressionBytes {
		return nil, nil, limitError("expression", MaxExpressionBytes)
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		text = strings.TrimSpace(text[3 : len(text)-2])
	}
	p := &expressionParser{text: text, key: key, secrets: map[string]bool{}}
	if err := p.next(); err != nil {
		return nil, p, err
	}
	e, err := p.parse(1, 0)
	if err == nil && p.token != "" {
		err = expressionError(key, "a valid expression")
	}
	if err == nil && expressionDepth(e) > MaxExpressionDepth {
		err = limitError("expression nesting", MaxExpressionDepth)
	}
	if err == nil {
		err = p.validate(e)
	}
	return e, p, err
}

func (p *expressionParser) next() error {
	for p.pos < len(p.text) && strings.ContainsRune(" \t\r\n", rune(p.text[p.pos])) {
		p.pos++
	}
	p.token, p.value = "", nil
	if p.pos == len(p.text) {
		return nil
	}
	start, b := p.pos, p.text[p.pos]
	p.pos++
	if b == '\'' {
		var value strings.Builder
		for p.pos < len(p.text) {
			c := p.text[p.pos]
			p.pos++
			if c == '\'' {
				if p.pos < len(p.text) && p.text[p.pos] == '\'' {
					p.pos++
				} else {
					p.token, p.value = "literal", value.String()
					return nil
				}
			}
			value.WriteByte(c)
		}
		return expressionError(p.key, "a closed single-quoted string")
	}
	if asciiLetter(b) || b == '_' {
		for p.pos < len(p.text) && (asciiLetter(p.text[p.pos]) || asciiDigit(p.text[p.pos]) || strings.ContainsRune("_-", rune(p.text[p.pos]))) {
			p.pos++
		}
		p.token, p.value = "identifier", p.text[start:p.pos]
		switch strings.ToLower(p.value.(string)) {
		case "true":
			p.token, p.value = "literal", true
		case "false":
			p.token, p.value = "literal", false
		case "null":
			p.token, p.value = "literal", nil
		}
		return nil
	}
	if asciiDigit(b) || b == '-' {
		for p.pos < len(p.text) && strings.ContainsRune("0123456789abcdefABCDEFxX.eE+-", rune(p.text[p.pos])) {
			p.pos++
		}
		v, err := parseNumber(p.text[start:p.pos])
		if err != nil {
			return expressionError(p.key, "a number")
		}
		p.token, p.value = "literal", v
		return nil
	}
	if p.pos < len(p.text) {
		pair := p.text[start : p.pos+1]
		if containsWord("== != <= >= && ||", pair) {
			p.pos++
			p.token = pair
			return nil
		}
	}
	if strings.ContainsRune("!<>()[],.", rune(b)) {
		p.token = string(b)
		return nil
	}
	return expressionError(p.key, "a supported expression operator (object filters are not supported)")
}

func precedence(op string) int {
	switch op {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=":
		return 3
	case "<", "<=", ">", ">=":
		return 4
	}
	return 0
}

func (p *expressionParser) parse(min, depth int) (*expression, error) {
	if depth > MaxExpressionDepth {
		return nil, limitError("expression nesting", MaxExpressionDepth)
	}
	var e *expression
	switch p.token {
	case "!":
		if err := p.next(); err != nil {
			return nil, err
		}
		child, err := p.parse(5, depth+1)
		if err != nil {
			return nil, err
		}
		e = &expression{op: "!", children: []*expression{child}}
	case "(":
		if err := p.next(); err != nil {
			return nil, err
		}
		var err error
		e, err = p.parse(1, depth+1)
		if err != nil {
			return nil, err
		}
		if err := p.take(")"); err != nil {
			return nil, err
		}
	case "literal":
		e = &expression{op: "literal", value: p.value}
		if err := p.next(); err != nil {
			return nil, err
		}
	case "identifier":
		name := p.value.(string)
		if err := p.next(); err != nil {
			return nil, err
		}
		e = &expression{op: "context", value: strings.ToLower(name)}
		if p.token == "(" {
			e.op, e.value = "call", strings.ToLower(name)
			if err := p.next(); err != nil {
				return nil, err
			}
			for p.token != ")" {
				arg, err := p.parse(1, depth+1)
				if err != nil {
					return nil, err
				}
				e.children = append(e.children, arg)
				if p.token != "," {
					break
				}
				if err := p.next(); err != nil {
					return nil, err
				}
			}
			if err := p.take(")"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, expressionError(p.key, "a valid expression")
	}
	for p.token == "." || p.token == "[" {
		kind := p.token
		if err := p.next(); err != nil {
			return nil, err
		}
		var property *expression
		if kind == "." {
			if p.token != "identifier" {
				return nil, expressionError(p.key, "a property name")
			}
			property = &expression{op: "literal", value: p.value}
			if err := p.next(); err != nil {
				return nil, err
			}
		} else {
			var err error
			property, err = p.parse(1, depth+1)
			if err != nil {
				return nil, err
			}
			if err := p.take("]"); err != nil {
				return nil, err
			}
		}
		e = &expression{op: kind, children: []*expression{e, property}}
		depth++
		if depth > MaxExpressionDepth {
			return nil, limitError("expression nesting", MaxExpressionDepth)
		}
	}
	for precedence(p.token) >= min {
		op := p.token
		if err := p.next(); err != nil {
			return nil, err
		}
		right, err := p.parse(precedence(op)+1, depth+1)
		if err != nil {
			return nil, err
		}
		e = &expression{op: op, children: []*expression{e, right}}
	}
	return e, nil
}

func (p *expressionParser) take(token string) error {
	if p.token != token {
		return expressionError(p.key, token)
	}
	return p.next()
}

func isIfKey(key string) bool { return key == "job.if" || key == "step.if" }

func allowedContexts(key string) string {
	switch key {
	case "run-name", "workflow.concurrency", "job.strategy":
		return "github inputs"
	case "workflow.env":
		return "github secrets inputs"
	case "job.if":
		return "github needs inputs"
	case "job.name", "job.runs-on", "job.timeout-minutes", "job.continue-on-error", "job.concurrency":
		return "github needs strategy matrix inputs"
	case "job.env":
		return "github needs strategy matrix secrets inputs"
	case "job.defaults.run":
		return "github needs strategy matrix env inputs"
	case "step.if":
		return "github needs strategy matrix job runner env steps inputs"
	case "step.name", "step.run", "step.env", "step.with", "step.working-directory", "step.timeout-minutes", "step.continue-on-error":
		return "github needs strategy matrix job runner env steps inputs secrets"
	case "builtin.with":
		return "github strategy matrix inputs"
	}
	return ""
}

func (p *expressionParser) validate(e *expression) error {
	if e.op == "call" {
		name := e.value.(string)
		min, max := 0, 0
		switch name {
		case "success", "failure", "always", "cancelled":
			if !isIfKey(p.key) {
				return contextError(name+"()", p.key, "Use status functions in if.")
			}
			p.status = true
		case "contains", "startswith", "endswith":
			min, max = 2, 2
		case "join":
			min, max = 1, 2
		case "format":
			min, max = 1, MaxExpressionBytes
		case "tojson", "fromjson":
			min, max = 1, 1
		default:
			hint := "Replace it with a supported expression function."
			if name == "hashfiles" {
				hint = "Compute the hash in a run step, for example with sha256sum."
			}
			return refuse("workflow.function", p.key, 0, fmt.Sprintf("%s() is not supported. %s", name, hint))
		}
		if len(e.children) < min || len(e.children) > max {
			return expressionError(p.key, "the documented function arguments")
		}
	}
	if e.op == "context" || e.op == "." || e.op == "[" {
		return p.validateAccess(e, nil, true)
	}
	for _, child := range e.children {
		if err := p.validate(child); err != nil {
			return err
		}
	}
	return nil
}

func (p *expressionParser) validateAccess(e *expression, path []string, direct bool) error {
	switch e.op {
	case ".", "[":
		property := e.children[1]
		if err := p.validate(property); err != nil {
			return err
		}
		name, literal := property.value.(string)
		if property.op != "literal" || !literal {
			name = "*"
		}
		return p.validateAccess(e.children[0], append([]string{name}, path...), direct && e.op == ".")
	case "&&", "||":
		for _, child := range e.children {
			if err := p.validateAccess(child, path, false); err != nil {
				return err
			}
		}
		return nil
	case "context":
		root := e.value.(string)
		if root == "secrets" && isIfKey(p.key) {
			return secretReferenceError(p.key)
		}
		if !containsWord(allowedContexts(p.key), root) {
			hint := "Use a context available at this key."
			if root == "vars" {
				hint = "Use env in the workflow, or a secret."
			}
			return contextError(root, p.key, hint)
		}
		if root == "secrets" {
			if len(path) != 1 || !direct || !validEnvName(path[0]) {
				return secretReferenceError(p.key)
			}
			name := strings.ToUpper(path[0])
			if name == "GITHUB_TOKEN" {
				return tokenError("secrets.GITHUB_TOKEN", p.key)
			}
			p.secrets[name] = true
		}
		return validateContextPath(root, path, p.key)
	default:
		return p.validate(e)
	}
}

func directContextAccess(e *expression) bool {
	for e.op == "." || e.op == "[" {
		e = e.children[0]
	}
	return e.op == "context"
}

func validateContextPath(root string, path []string, key string) error {
	joined := strings.ToLower(strings.Join(path, "."))
	if root == "github" {
		if joined == "token" {
			return tokenError("github.token", key)
		}
		allowed := "sha ref ref_name ref_type head_ref base_ref event_name repository actor triggering_actor run_id run_number run_attempt workflow job event event.action event.inputs event.number event.pull_request event.pull_request.number event.pull_request.head event.pull_request.head.ref event.pull_request.head.sha event.pull_request.base event.pull_request.base.ref event.pull_request.base.sha"
		if strings.HasPrefix(key, "step.") || containsWord("workflow.env job.env job.defaults.run", key) {
			allowed += " workspace"
		}
		if joined != "" && !containsWord(allowed, joined) && !strings.HasPrefix(joined, "event.inputs.") {
			return contextError("github."+joined, key, "OwnGit fills only the event fields listed in its workflow documentation.")
		}
	}
	if root == "needs" && len(path) > 1 && (strings.ToLower(path[1]) != "result" || len(path) > 2) {
		return contextError("needs."+joined, key, "OwnGit does not pass outputs between jobs. Compute the value in the job that uses it, or merge the two jobs.")
	}
	closed := map[string]string{"runner": "os arch temp name", "job": "status", "strategy": "job-index job-total fail-fast max-parallel"}
	if allowed, ok := closed[root]; ok && len(path) > 0 && (len(path) > 1 || !containsWord(allowed, joined)) {
		return contextError(root+"."+joined, key, "Use a documented context field.")
	}
	if root == "steps" && len(path) > 1 && (strings.ToLower(path[1]) != "outputs" && strings.ToLower(path[1]) != "outcome" && strings.ToLower(path[1]) != "conclusion" || strings.ToLower(path[1]) != "outputs" && len(path) > 2) {
		return contextError(root+"."+joined, key, "Use step outputs, outcome or conclusion.")
	}
	return nil
}

func (e *expression) evaluate(ctx EvalContext, steps *int) (any, error) {
	*steps++
	if *steps > MaxEvaluationSteps {
		return nil, limitError("expression evaluation steps", MaxEvaluationSteps)
	}
	if e.op == "literal" {
		return e.value, nil
	}
	if e.op == "context" {
		return checkedExpressionValue(ctx.Values[e.value.(string)], steps)
	}
	if e.op == "call" {
		args := make([]any, len(e.children))
		for i, child := range e.children {
			v, err := child.evaluate(ctx, steps)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return callExpression(e.value.(string), args, ctx, steps)
	}
	if e.op == "." || e.op == "[" {
		if directContextAccess(e) {
			return e.access(ctx, steps)
		}
	}
	left, err := e.children[0].evaluate(ctx, steps)
	if err != nil {
		return nil, err
	}
	if e.op == "!" {
		return !Truthy(left), nil
	}
	if e.op == "&&" && !Truthy(left) || e.op == "||" && Truthy(left) {
		return left, nil
	}
	right, err := e.children[1].evaluate(ctx, steps)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case ".", "[":
		return checkedExpressionValue(propertyValue(left, right), steps)
	case "&&", "||":
		return right, nil
	case "==":
		return equalValue(left, right), nil
	case "!=":
		return !equalValue(left, right), nil
	case "<", "<=", ">", ">=":
		return compareValues(left, right, e.op), nil
	}
	return nil, expressionError("expression", "a supported operator")
}

func expressionDepth(e *expression) int {
	depth := 1
	for _, child := range e.children {
		if n := 1 + expressionDepth(child); n > depth {
			depth = n
		}
	}
	return depth
}

func objectProperty(object map[string]any, name string) any {
	if value, exists := object[name]; exists {
		return value
	}
	for _, key := range sortedKeys(object) {
		if strings.EqualFold(key, name) {
			return object[key]
		}
	}
	return nil
}

func (e *expression) access(ctx EvalContext, steps *int) (any, error) {
	var properties []*expression
	node := e
	for node.op == "." || node.op == "[" {
		properties = append(properties, node.children[1])
		node = node.children[0]
	}
	value := ctx.Values[node.value.(string)]
	for i := len(properties) - 1; i >= 0; i-- {
		property, err := properties[i].evaluate(ctx, steps)
		if err != nil {
			return nil, err
		}
		value = propertyValue(value, property)
	}
	return checkedExpressionValue(value, steps)
}

// Truthy implements GitHub's expression truthiness, including false null and NaN.
func Truthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0 && !math.IsNaN(value)
	case int:
		return value != 0
	case int64:
		return value != 0
	case json.Number:
		n, _ := value.Float64()
		return n != 0 && !math.IsNaN(n)
	}
	return true
}

// ScalarString uses GitHub's interpolation coercion, not Go's debug formatting.
func ScalarString(v any) (string, error) {
	switch value := v.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case int:
		return strconv.Itoa(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case json.Number:
		return value.String(), nil
	}
	return "", expressionError("expression", "a scalar value (arrays and objects are not strings)")
}

func parseNumber(s string) (float64, error) {
	if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(s, "-")), "0x") {
		n, err := strconv.ParseInt(s, 0, 64)
		return float64(n), err
	}
	n, err := strconv.ParseFloat(s, 64)
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, fmt.Errorf("non-finite number")
	}
	return n, err
}

func number(v any) float64 {
	if v == nil {
		return 0
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1
		}
		return 0
	}
	s, err := ScalarString(v)
	if err != nil {
		return math.NaN()
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// String-to-number conversion accepts JSON numbers only.
	if !json.Valid([]byte(s)) {
		return math.NaN()
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return n
}

func equalValue(a, b any) bool {
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.EqualFold(x, y)
		}
	}
	if equal, collection := collectionEqual(a, b); collection {
		return equal
	}
	x, y := number(a), number(b)
	return !math.IsNaN(x) && !math.IsNaN(y) && x == y
}

func compareValues(a, b any, op string) bool {
	compare := 0
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			compare = strings.Compare(strings.ToLower(x), strings.ToLower(y))
			return ordered(compare, op)
		}
	}
	x, y := number(a), number(b)
	if math.IsNaN(x) || math.IsNaN(y) {
		return false
	}
	if x < y {
		compare = -1
	} else if x > y {
		compare = 1
	}
	return ordered(compare, op)
}

func ordered(c int, op string) bool {
	return op == "<" && c < 0 || op == "<=" && c <= 0 || op == ">" && c > 0 || op == ">=" && c >= 0
}

func containsWord(words, word string) bool {
	for _, candidate := range strings.Fields(words) {
		if candidate == word {
			return true
		}
	}
	return false
}

func asciiLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
func asciiDigit(b byte) bool  { return b >= '0' && b <= '9' }

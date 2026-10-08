package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

type EvaluatedConcurrency struct {
	Group            string
	CancelInProgress bool
	Queue            string
}

type PlannedWorkflow struct {
	Name        string
	RunName     string
	Concurrency EvaluatedConcurrency
	Jobs        []PlannedJob
	Facts       RunFacts
}

type PlannedJob struct {
	Name           string
	RunsOn         []string
	Notes          []Message
	Plan           JobPlan
	Encoded        []byte
	Digest         string
	Status         string
	Tolerated      bool
	TimeoutMinutes float64
	Concurrency    EvaluatedConcurrency
}

// Plan expands one workflow at admission. Runtime environment expressions stay
// in JobPlan, which contains secret names but never runtime secret values.
func Plan(w *Workflow, context PlanContext) (PlannedWorkflow, error) {
	name := w.Name
	if name == "" {
		name = w.Path
	}
	out := PlannedWorkflow{Name: name, Facts: RunFacts{WorkflowName: name, WorkflowDigest: w.Digest, Notes: append([]Message(nil), w.Notes...)}}
	context.GitHub.Workflow = name
	ctx, err := ContextFromPlan(context)
	if err != nil {
		return PlannedWorkflow{}, err
	}
	workflowSecrets := map[string]bool{}
	for _, name := range sortedKeys(w.Env) {
		names, err := ValidateTemplate(w.Env[name], "workflow.env")
		if err != nil {
			return PlannedWorkflow{}, err
		}
		for _, name := range names {
			workflowSecrets[name] = true
		}
	}
	commonSecretNames := sortedKeys(workflowSecrets)
	if out.RunName, err = evaluatedString(w.RunName, "run-name", ctx); err != nil {
		return PlannedWorkflow{}, err
	}
	if out.Concurrency, err = EvaluateConcurrency(w.Concurrency, "workflow.concurrency", ctx); err != nil {
		return PlannedWorkflow{}, err
	}
	if err := validateNeeds(w.Jobs); err != nil {
		return PlannedWorkflow{}, err
	}
	count, allSecrets := 0, map[string]bool{}
	for _, definition := range w.Jobs {
		plan := definition.JobPlan
		plan.Context = context
		plan.Context.GitHub.Job = plan.JobKey
		plan.WorkflowEnv, plan.WorkflowDefaults = w.Env, w.Defaults
		secrets, validation := validateJob(definition, commonSecretNames)
		plan.SecretNames = secrets
		for _, name := range secrets {
			allSecrets[name] = true
		}
		var combinations []map[string]any
		matrixOrder, matrixContext := definition.MatrixOrder, ctx
		if _, expression := definition.Matrix.(string); expression {
			matrixContext.matrixOrders = map[string][]string{}
			matrixContext.matrixOrder = &matrixOrder
		}
		var matrixValue any = map[string]any{}
		var matrixErr error
		if definition.Matrix != nil {
			matrixValue, matrixErr = evaluateMatrix(definition.Matrix, matrixContext)
		}
		if matrixErr == nil {
			matrix, ok := matrixValue.(map[string]any)
			if !ok {
				matrixErr = expressionError("job.strategy.matrix", "a matrix mapping")
			} else {
				combinations, matrixErr = ExpandMatrix(matrix, matrixOrder, MaxJobs-count)
			}
		}
		var refusal *Refusal
		if errors.As(matrixErr, &refusal) && refusal.Code == "workflow.too_many_jobs" {
			return PlannedWorkflow{}, tooManyJobs(MaxJobs + 1)
		}
		if matrixErr == nil && len(combinations) == 0 {
			matrixErr = expressionError("job.strategy.matrix", "at least one combination after exclusions")
		}
		if matrixErr != nil {
			if validation == nil {
				validation = matrixErr
			}
			combinations = []map[string]any{{}}
		}
		count += len(combinations)
		if count > MaxJobs {
			return PlannedWorkflow{}, tooManyJobs(count)
		}
		failFast, strategyErr := evaluatedBool(definition.FailFast, "job.strategy", ctx, true)
		maxParallel := len(combinations)
		if maxParallel == 0 {
			maxParallel = 1
		}
		if strategyErr == nil && definition.MaxParallel != "" {
			maxParallel, strategyErr = evaluatedPositiveInt(definition.MaxParallel, "job.strategy", ctx)
		}
		if strategyErr != nil && validation == nil {
			validation = strategyErr
		}
		for i, matrix := range combinations {
			plan.MatrixIndex = i
			plan.Context.Matrix = matrix
			if len(matrix) == 0 {
				plan.Context.Matrix = nil
			}
			plan.Context.Strategy = StrategyContext{JobIndex: i, JobTotal: len(combinations), FailFast: failFast, MaxParallel: maxParallel}
			if definition.Refusal != nil {
				out.Facts.RefusedJobs = append(out.Facts.RefusedJobs, RefusedJob{JobKey: plan.JobKey, MatrixIndex: i, Reason: *definition.Refusal})
				continue
			}
			if validation != nil {
				out.Facts.RefusedJobs = append(out.Facts.RefusedJobs, refusedJob(plan, validation))
				continue
			}
			plan.Notes = append([]Message(nil), definition.Notes...)
			job, err := ResolveJob(plan, nil)
			if err != nil {
				out.Facts.RefusedJobs = append(out.Facts.RefusedJobs, refusedJob(plan, err))
				continue
			}
			out.Jobs = append(out.Jobs, job)
		}
	}
	out.Facts.SecretNames = sortedKeys(allSecrets)
	return out, nil
}

func validateJob(definition JobDefinition, workflowSecrets []string) ([]string, error) {
	secrets := map[string]bool{}
	for _, name := range workflowSecrets {
		secrets[name] = true
	}
	check := func(text, key string) error {
		names, err := ValidateTemplate(text, key)
		for _, name := range names {
			secrets[name] = true
		}
		return err
	}
	plan := definition.JobPlan
	for _, field := range []struct{ text, key string }{
		{plan.Name, "job.name"}, {plan.If, "job.if"}, {plan.TimeoutMinutes, "job.timeout-minutes"},
		{plan.ContinueOnError, "job.continue-on-error"}, {plan.Concurrency.Group, "job.concurrency"},
		{plan.Concurrency.CancelInProgress, "job.concurrency"}, {plan.Defaults.Shell, "job.defaults.run"},
		{plan.Defaults.WorkingDirectory, "job.defaults.run"}, {definition.FailFast, "job.strategy"}, {definition.MaxParallel, "job.strategy"},
	} {
		if err := check(field.text, field.key); err != nil {
			return sortedKeys(secrets), err
		}
	}
	for _, label := range plan.RunsOn {
		if err := check(label, "job.runs-on"); err != nil {
			return sortedKeys(secrets), err
		}
	}
	for _, name := range sortedKeys(plan.Env) {
		if err := check(plan.Env[name], "job.env"); err != nil {
			return sortedKeys(secrets), err
		}
	}
	for _, step := range plan.Steps {
		for _, field := range []struct{ text, key string }{
			{step.Name, "step.name"}, {step.If, "step.if"}, {step.Run, "step.run"},
			{step.WorkingDirectory, "step.working-directory"}, {step.TimeoutMinutes, "step.timeout-minutes"},
			{step.ContinueOnError, "step.continue-on-error"}, {step.Shell, "step.shell"},
		} {
			if err := check(field.text, field.key); err != nil {
				return sortedKeys(secrets), err
			}
		}
		for _, name := range sortedKeys(step.Env) {
			if err := check(step.Env[name], "step.env"); err != nil {
				return sortedKeys(secrets), err
			}
		}
		if step.Uses != "" {
			if _, err := LookupBuiltin(step.Uses); err != nil {
				return sortedKeys(secrets), err
			}
		}
	}
	return sortedKeys(secrets), nil
}

// Dependency includes ancestor failure so failure() works through skipped jobs.
// The coordinator supplies all combinations of each directly needed job.
type Dependency struct {
	Status         string
	Tolerated      bool
	AncestorFailed bool
}

// ResolveJob releases only fully finished dependency sets. An ambiguous needed
// job skips even always(); a missing or unfinished dependency leaves it waiting.
func ResolveJob(plan JobPlan, dependencies map[string][]Dependency) (PlannedJob, error) {
	job := PlannedJob{Plan: plan, Status: StatusPending, TimeoutMinutes: 360, Notes: append([]Message(nil), plan.Notes...)}
	ctx, err := ContextFromPlan(plan.Context)
	if err != nil {
		return job, err
	}
	for _, step := range plan.Steps {
		if step.Uses != "" {
			_, notes, err := EvaluateBuiltin(step, ctx)
			if err != nil {
				return job, err
			}
			job.Notes = append(job.Notes, notes...)
		}
	}
	if len(plan.Needs) > 0 {
		results := map[string]string{}
		uncertain, ready := false, true
		for _, name := range plan.Needs {
			items := dependencies[name]
			if len(items) == 0 {
				ready = false
			}
			result := ResultSuccess
			for _, item := range items {
				if containsWord("pending waiting claimed started running queued", item.Status) {
					ready = false
				}
				if item.Status == StatusAmbiguous {
					uncertain = true
				}
				mapped := NeedsResult(item.Status, item.Tolerated)
				if mapped == ResultFailure || mapped == ResultCancelled && result != ResultFailure || mapped == ResultSkipped && result == ResultSuccess {
					result = mapped
				}
				ctx.Failure = ctx.Failure || mapped == ResultFailure || item.AncestorFailed
				ctx.Cancelled = ctx.Cancelled || mapped == ResultCancelled
			}
			results[name] = result
			ctx.Success = ctx.Success && result == ResultSuccess
		}
		if !ready {
			job.Status = StatusWaiting
			return canonicalizePlannedJob(job)
		}
		job.Plan.Context.Needs = results
		ctx.Values["needs"] = needsValues(results)
		if uncertain {
			job.Status = StatusSkipped
			return canonicalizePlannedJob(job)
		}
	}
	run, err := EvalIf(plan.If, "job.if", ctx)
	if err != nil {
		return job, err
	}
	if !run {
		job.Status = StatusSkipped
		return canonicalizePlannedJob(job)
	}
	if job.Name, err = evaluatedString(plan.Name, "job.name", ctx); err != nil {
		return job, err
	}
	for _, raw := range plan.RunsOn {
		value, err := EvalTemplate(raw, "job.runs-on", ctx)
		if err != nil {
			return job, err
		}
		if labels, ok := value.([]any); ok {
			for _, label := range labels {
				text, err := ScalarString(label)
				if err != nil {
					return job, err
				}
				job.RunsOn = append(job.RunsOn, text)
			}
		} else {
			text, err := ScalarString(value)
			if err != nil {
				return job, err
			}
			job.RunsOn = append(job.RunsOn, text)
		}
	}
	if job.Tolerated, err = evaluatedBool(plan.ContinueOnError, "job.continue-on-error", ctx, false); err != nil {
		return job, err
	}
	if plan.TimeoutMinutes != "" {
		n, err := evaluatedPositiveNumber(plan.TimeoutMinutes, "job.timeout-minutes", ctx)
		if err != nil {
			return job, err
		}
		job.TimeoutMinutes = n
	}
	if job.Concurrency, err = EvaluateConcurrency(plan.Concurrency, "job.concurrency", ctx); err != nil {
		return job, err
	}
	return canonicalizePlannedJob(job)
}

func ContextFromPlan(context PlanContext) (EvalContext, error) {
	encoded, err := json.Marshal(context)
	if err != nil {
		return EvalContext{}, expressionError("plan context", "JSON-compatible values")
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil {
		return EvalContext{}, expressionError("plan context", "valid JSON values")
	}
	values["needs"] = needsValues(context.Needs)
	return EvalContext{Values: values, Success: true}, nil
}

func needsValues(results map[string]string) map[string]any {
	values := make(map[string]any, len(results))
	for name, result := range results {
		values[name] = map[string]any{"result": result}
	}
	return values
}

func EvaluateConcurrency(raw Concurrency, key string, ctx EvalContext) (EvaluatedConcurrency, error) {
	out := EvaluatedConcurrency{Queue: raw.Queue}
	if out.Queue == "" {
		out.Queue = "single"
	}
	if raw.Group == "" {
		return out, nil
	}
	group, err := evaluatedString(raw.Group, key, ctx)
	if err != nil {
		return out, err
	}
	if group == "" || strings.ContainsRune(group, 0) {
		return out, expressionError(key+".group", "a nonempty group without NUL")
	}
	normalized := strings.ToLower(group)
	if len(group) > 200 || len(normalized) > 200 {
		return out, limitError("concurrency group", 200)
	}
	out.Group = normalized
	out.CancelInProgress, err = evaluatedBool(raw.CancelInProgress, key, ctx, false)
	if err == nil && out.Queue != "single" && out.Queue != "max" {
		err = expressionError(key+".queue", "single or max")
	}
	if err == nil && out.Queue == "max" && out.CancelInProgress {
		err = expressionError(key, "queue: max without cancel-in-progress: true")
	}
	return out, err
}

func evaluateMatrix(value any, ctx EvalContext) (any, error) {
	switch value := value.(type) {
	case string:
		return EvalTemplate(value, "job.strategy", ctx)
	case map[string]any:
		out := map[string]any{}
		for _, key := range sortedKeys(value) {
			item, err := evaluateMatrix(value[key], ctx)
			if err != nil {
				return nil, err
			}
			out[key] = item
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, value := range value {
			item, err := evaluateMatrix(value, ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	}
	return value, nil
}

func evaluatedString(text, key string, ctx EvalContext) (string, error) {
	v, err := EvalTemplate(text, key, ctx)
	if err != nil {
		return "", err
	}
	return ScalarString(v)
}

func evaluatedBool(text, key string, ctx EvalContext, fallback bool) (bool, error) {
	if text == "" {
		return fallback, nil
	}
	v, err := EvalTemplate(text, key, ctx)
	if err != nil {
		return false, err
	}
	if !strings.Contains(text, "${{") {
		if text == "true" {
			v = true
		} else if text == "false" {
			v = false
		}
	}
	b, ok := v.(bool)
	if !ok {
		return false, expressionError(key, "a boolean (use true or false)")
	}
	return b, nil
}

func evaluatedPositiveNumber(text, key string, ctx EvalContext) (float64, error) {
	v, err := EvalTemplate(text, key, ctx)
	if err != nil {
		return 0, err
	}
	if strings.Contains(text, "${{") {
		if _, ok := v.(float64); !ok {
			return 0, expressionError(key, "a positive number, not a string")
		}
	}
	s, err := ScalarString(v)
	if err != nil {
		return 0, err
	}
	n, err := parseNumber(s)
	if err != nil || n <= 0 {
		return 0, expressionError(key, "a positive number")
	}
	return n, nil
}

func evaluatedPositiveInt(text, key string, ctx EvalContext) (int, error) {
	n, err := evaluatedPositiveNumber(text, key, ctx)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt32 || n != math.Trunc(n) {
		return 0, expressionError(key, "a positive integer")
	}
	return int(n), nil
}

func refusedJob(plan JobPlan, err error) RefusedJob {
	var refusal *Refusal
	message := Message{Code: "workflow.wrong_type", Detail: "Job cannot be planned."}
	if errors.As(err, &refusal) {
		message = refusal.Message
	}
	return RefusedJob{JobKey: plan.JobKey, MatrixIndex: plan.MatrixIndex, Reason: message}
}

// EncodePlan returns canonical JSON without per-run identity and its exact
// SHA-256 digest. Admission and the step runner supply run identity from the
// authoritative run row when they evaluate expressions.
func EncodePlan(plan JobPlan) ([]byte, string, error) {
	plan = planWithoutRunIdentity(plan)
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, "", expressionError("plan", "JSON-compatible values")
	}
	if len(encoded) > MaxPlanBytes {
		return nil, "", limitError("plan per job", MaxPlanBytes)
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func planWithoutRunIdentity(plan JobPlan) JobPlan {
	plan.Context.GitHub.RunID = ""
	plan.Context.GitHub.RunNumber = 0
	plan.Context.GitHub.RunAttempt = 0
	return plan
}

func DecodePlan(encoded []byte, digest string) (JobPlan, error) {
	var plan JobPlan
	if len(encoded) > MaxPlanBytes {
		return plan, limitError("plan per job", MaxPlanBytes)
	}
	hash := sha256.Sum256(encoded)
	if digest != hex.EncodeToString(hash[:]) {
		return plan, expressionError("plan", "an unchanged SHA-256 digest")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return JobPlan{}, expressionError("plan", "a valid job plan")
	}
	canonical, _, err := EncodePlan(plan)
	if err != nil || string(canonical) != string(encoded) {
		return JobPlan{}, expressionError("plan", "canonical job plan JSON")
	}
	return plan, nil
}

func canonicalizePlannedJob(job PlannedJob) (PlannedJob, error) {
	var err error
	job.Encoded, job.Digest, err = EncodePlan(job.Plan)
	if err != nil {
		return job, err
	}
	var canonical JobPlan
	if err := json.Unmarshal(job.Encoded, &canonical); err != nil {
		return job, expressionError("plan", "a valid job plan")
	}
	job.Plan = canonical
	return job, nil
}

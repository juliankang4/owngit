package state

import (
	"errors"
	"fmt"
	"strconv"

	"owngit/internal/checkworkflow"
)

// Structured policy rejections.
//
// The backend stays the sole authority on what a policy may contain. What this
// file adds is a machine-readable account of which field was refused and by
// which rule, so a caller can attach the refusal to the control the operator
// used instead of showing one sentence for the whole form.
//
// Two properties are deliberate. Every rejection still satisfies
// errors.Is(err, ErrInvalidCheckPolicy), so existing callers and tests keep
// working unchanged. And the bounds travel as numbers rather than as prose, so
// a caller never has to parse a formatted message to learn them, and no second
// copy of a range can drift from the one enforced here.

// Policy field names. They are the backend's own vocabulary for the policy, and
// a caller maps them to whatever its interface calls the same thing.
const (
	FieldExecutor            = "executor"
	FieldAllowedEvents       = "allowed_events"
	FieldMaxTimeoutMS        = "max_timeout_ms"
	FieldMaxOutputLimitBytes = "max_output_limit_bytes"
	FieldQueueLimit          = "queue_limit"
	FieldMaxActiveJobs       = "max_active_jobs"
	FieldMaxLeaseMS          = "max_lease_ms"

	FieldContainerImage        = "container_image"
	FieldContainerRuntime      = "container_runtime"
	FieldContainerNetwork      = "container_network"
	FieldContainerCPUMillis    = "container_cpu_millis"
	FieldContainerMemoryBytes  = "container_memory_bytes"
	FieldContainerPIDs         = "container_pids"
	FieldContainerScratchBytes = "container_scratch_bytes"

	FieldSourceMaxEntries    = "source_max_entries"
	FieldSourceMaxFileBytes  = "source_max_file_bytes"
	FieldSourceMaxTotalBytes = "source_max_total_bytes"
	FieldSourceMaxPathDepth  = "source_max_path_depth"
	FieldSourceMaxPathBytes  = "source_max_path_bytes"
	FieldSourceMaxNameBytes  = "source_max_name_bytes"
	FieldSourceMetadataLimit = "source_metadata_limit_bytes"
)

// Rules a field can break. A caller chooses its wording from the rule, not from
// the sentence this package formats.
const (
	// RuleRange means the value is outside Min and Max, inclusive.
	RuleRange = "range"
	// RuleRequired means the field was empty and has no default.
	RuleRequired = "required"
	// RuleUnknown means the value is not one this release accepts.
	RuleUnknown = "unknown"
	// RuleFormat means the value is the right kind of thing written wrongly.
	RuleFormat = "format"
	// RuleNotApplicable means the field was supplied for an executor that does
	// not use it. The value itself may be perfectly valid.
	RuleNotApplicable = "not_applicable"
	// RuleDuplicate means the value repeats an earlier entry.
	RuleDuplicate = "duplicate"
)

// CheckPolicyFieldError is one refused policy field.
//
// It wraps ErrInvalidCheckPolicy, so a caller that only wants to know the
// policy was refused keeps working without change.
type CheckPolicyFieldError struct {
	// Field is one of the Field* names.
	Field string
	// Rule is one of the Rule* names.
	Rule string
	// Min and Max carry the accepted bounds when Rule is RuleRange. They are
	// the exact numbers enforced here, so a caller can state them without
	// keeping its own copy.
	Min int64
	Max int64
	// Value is the refused value when stating it helps and it is not
	// sensitive. Policy fields are settings, never secrets.
	Value string
}

func (e *CheckPolicyFieldError) Error() string {
	switch e.Rule {
	case RuleRange:
		return fmt.Sprintf("invalid check policy: %s must be between %d and %d", e.Field, e.Min, e.Max)
	case RuleRequired:
		return "invalid check policy: " + e.Field + " is required"
	case RuleUnknown:
		return fmt.Sprintf("invalid check policy: %s value %q is not recognised", e.Field, e.Value)
	case RuleFormat:
		return "invalid check policy: " + e.Field + " is not in an accepted form"
	case RuleNotApplicable:
		return "invalid check policy: " + e.Field + " does not apply to this executor"
	case RuleDuplicate:
		return fmt.Sprintf("invalid check policy: %s repeats %q", e.Field, e.Value)
	default:
		return "invalid check policy: " + e.Field + " was refused"
	}
}

// Unwrap keeps errors.Is(err, ErrInvalidCheckPolicy) true, which is what every
// existing caller and test relies on.
func (e *CheckPolicyFieldError) Unwrap() error { return ErrInvalidCheckPolicy }

// PolicyFieldErrors extracts the structured refusals from an error.
//
// It returns nil for an error that is not a field refusal, including a plain
// ErrInvalidCheckPolicy, so a caller can fall back to a single message.
func PolicyFieldErrors(err error) []*CheckPolicyFieldError {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		var all []*CheckPolicyFieldError
		for _, one := range joined.Unwrap() {
			all = append(all, PolicyFieldErrors(one)...)
		}
		return all
	}
	var field *CheckPolicyFieldError
	if errors.As(err, &field) {
		return []*CheckPolicyFieldError{field}
	}
	return nil
}

// CheckPolicyBounds is the accepted range of one numeric policy field.
//
// It exists so the range a caller shows an operator and the range this package
// enforces are the same numbers. An interface that printed its own copy would
// eventually disagree with the rule that actually refuses the value, and the
// operator would be told a range that is not true.
type CheckPolicyBounds struct {
	// Min is the fixed lower bound. It is meaningful only when MinField is
	// empty; otherwise the floor is whatever that other field holds.
	Min int64
	// Max is the fixed upper bound. Every numeric policy field has one.
	Max int64
	// MinField names the field this one cannot fall below, for a limit whose
	// floor is another operator setting rather than a constant. The source
	// total is the only such field: it cannot be smaller than a single file.
	//
	// A dynamic floor does not make the ceiling unknowable, so a caller shows
	// the relationship and the maximum together rather than showing nothing.
	MinField string
}

// HasFixedMinimum reports whether Min is a constant the caller can print.
func (b CheckPolicyBounds) HasFixedMinimum() bool { return b.MinField == "" }

// checkPolicyBounds holds every numeric bound in the policy.
//
// Every numeric field appears here, including the source total. That one
// carries a dynamic floor through MinField and a fixed ceiling in Max, because
// "the floor depends on another field" is not a reason to leave the maximum
// undocumented and enforced only in private.
var checkPolicyBounds = map[string]CheckPolicyBounds{
	FieldMaxTimeoutMS:        {Min: checkworkflow.MinimumTimeoutMS, Max: checkworkflow.MaximumTimeoutMS},
	FieldMaxOutputLimitBytes: {Min: checkworkflow.MinimumOutputLimitBytes, Max: checkworkflow.MaximumOutputLimitBytes},
	FieldQueueLimit:          {Min: 1, Max: MaximumCheckQueueLimit},
	FieldMaxActiveJobs:       {Min: 1, Max: MaximumCheckActiveJobs},
	FieldMaxLeaseMS:          {Min: MinimumCheckLeaseMS, Max: MaximumCheckLeaseMS},

	FieldContainerCPUMillis:    {Min: 100, Max: 64000},
	FieldContainerMemoryBytes:  {Min: 64 << 20, Max: 64 << 30},
	FieldContainerPIDs:         {Min: 16, Max: 4096},
	FieldContainerScratchBytes: {Min: 1 << 20, Max: 16 << 30},

	FieldSourceMaxEntries:    {Min: 1, Max: 100000},
	FieldSourceMaxFileBytes:  {Min: 1, Max: 1 << 30},
	FieldSourceMaxPathDepth:  {Min: 1, Max: 256},
	FieldSourceMaxPathBytes:  {Min: 1, Max: 4096},
	FieldSourceMaxNameBytes:  {Min: 1, Max: 1024},
	FieldSourceMetadataLimit: {Min: 1024, Max: 64 << 20},

	// The total cannot be smaller than one file, and cannot exceed 4 GiB. The
	// floor moves with the operator's own file limit; the ceiling does not.
	FieldSourceMaxTotalBytes: {MinField: FieldSourceMaxFileBytes, Max: 4 << 30},
}

// PublishedPolicyFields lists every field that publishes bounds.
//
// A caller uses it to enumerate what can be shown, and a test uses it to
// confirm that nothing published to operators escapes a boundary check. The
// order is not defined.
func PublishedPolicyFields() []string {
	fields := make([]string, 0, len(checkPolicyBounds))
	for field := range checkPolicyBounds {
		fields = append(fields, field)
	}
	return fields
}

// CheckPolicyBoundsFor reports the accepted range of a numeric field.
//
// The second result is false only for a field that is not numeric. A numeric
// field always publishes its ceiling, and publishes its floor either as a
// number or as the name of the field it cannot fall below.
func CheckPolicyBoundsFor(field string) (CheckPolicyBounds, bool) {
	bounds, known := checkPolicyBounds[field]
	return bounds, known
}

// boundedRangeError refuses a field using its published range, so the refusal
// and the range shown beside the control cannot disagree.
func boundedRangeError(field string, value int64) error {
	bounds, known := checkPolicyBounds[field]
	if !known {
		// A caller asking to refuse a field with no published range is a
		// programming mistake, not operator input. Refuse it as a plain range
		// error rather than silently claiming bounds of zero.
		return &CheckPolicyFieldError{Field: field, Rule: RuleRange, Value: strconv.FormatInt(value, 10)}
	}
	return rangeError(field, value, bounds.Min, bounds.Max)
}

func rangeError(field string, value, minimum, maximum int64) error {
	return &CheckPolicyFieldError{
		Field: field, Rule: RuleRange, Min: minimum, Max: maximum,
		Value: strconv.FormatInt(value, 10),
	}
}

func unknownValueError(field, value string) error {
	return &CheckPolicyFieldError{Field: field, Rule: RuleUnknown, Value: value}
}

func notApplicableError(field string) error {
	return &CheckPolicyFieldError{Field: field, Rule: RuleNotApplicable}
}

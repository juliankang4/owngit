package state

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Check ceilings.
//
// A repository's check policy chooses its own limits. The ceilings are this
// computer's upper bounds for those choices, set by the owner for the whole
// server. Each ceiling bounds one policy field and can be anywhere within
// that field's record bound (checkPolicyBounds), the largest value any
// stored or restored policy may hold. Defaults are the record bounds of
// earlier releases.
//
// A ceiling is applied when a policy is saved and when a job is admitted.
// Nothing else reads it: a backup, a restore and the check file parser use
// the record bounds, so a policy saved under higher ceilings elsewhere
// restores here and waits, without running, until the owner raises the
// ceiling or lowers the policy. Lowering a ceiling rewrites no policy and
// stops no admitted job, which runs under the limits it was admitted with.

const checkCeilingsKey = "check_ceilings"

// ErrCheckCeilingExceeded reports a policy above this computer's check
// ceilings: no new job is admitted for it.
var ErrCheckCeilingExceeded = errors.New("the check policy is above this computer's check ceilings")

// CheckCeilings are the upper bounds of repository check policies on this
// computer. Each field bounds the policy field of the same meaning.
type CheckCeilings struct {
	TimeoutMS             int64
	OutputLimitBytes      int64
	QueueLimit            int64
	ActiveJobs            int64
	ContainerCPUMillis    int64
	ContainerMemoryBytes  int64
	ContainerPIDs         int64
	ContainerScratchBytes int64
	SourceTotalBytes      int64
}

// DefaultCheckCeilings apply while nothing was saved.
var DefaultCheckCeilings = CheckCeilings{
	TimeoutMS: 24 * 60 * 60 * 1000, OutputLimitBytes: 64 << 20, QueueLimit: 1000, ActiveJobs: 100,
	ContainerCPUMillis: 64000, ContainerMemoryBytes: 64 << 30, ContainerPIDs: 4096, ContainerScratchBytes: 16 << 30,
	SourceTotalBytes: 4 << 30,
}

// bounded pairs each ceiling with the policy field it bounds.
func (c *CheckCeilings) bounded() []struct {
	field   string
	ceiling *int64
} {
	return []struct {
		field   string
		ceiling *int64
	}{
		{FieldMaxTimeoutMS, &c.TimeoutMS}, {FieldMaxOutputLimitBytes, &c.OutputLimitBytes},
		{FieldQueueLimit, &c.QueueLimit}, {FieldMaxActiveJobs, &c.ActiveJobs},
		{FieldContainerCPUMillis, &c.ContainerCPUMillis}, {FieldContainerMemoryBytes, &c.ContainerMemoryBytes},
		{FieldContainerPIDs, &c.ContainerPIDs}, {FieldContainerScratchBytes, &c.ContainerScratchBytes},
		{FieldSourceMaxTotalBytes, &c.SourceTotalBytes},
	}
}

// CheckCeilingRange is the range a ceiling may be set to: the record bounds
// of the policy field it bounds. The source total's own floor is another
// policy field, so its ceiling may go down to one byte.
func CheckCeilingRange(field string) (minimum, maximum int64) {
	bounds := checkPolicyBounds[field]
	if !bounds.HasFixedMinimum() {
		return 1, bounds.Max
	}
	return bounds.Min, bounds.Max
}

// Validate checks each ceiling against its range. The timeout is a whole
// number of seconds, as it is set.
func (c CheckCeilings) Validate() error {
	for _, bound := range c.bounded() {
		minimum, maximum := CheckCeilingRange(bound.field)
		if *bound.ceiling < minimum || *bound.ceiling > maximum {
			return fmt.Errorf("the check ceiling for %s is from %d to %d", bound.field, minimum, maximum)
		}
	}
	if c.TimeoutMS%1000 != 0 {
		return errors.New("the check time ceiling is a whole number of seconds")
	}
	return nil
}

// Looser reports whether any ceiling is above its default.
func (c CheckCeilings) Looser() bool {
	defaults := DefaultCheckCeilings
	current := c.bounded()
	for index, bound := range defaults.bounded() {
		if *current[index].ceiling > *bound.ceiling {
			return true
		}
	}
	return false
}

// Bounds reports the range a policy field accepts under these ceilings: its
// record bounds with the maximum lowered to the ceiling, if it has one.
func (c CheckCeilings) Bounds(field string) (CheckPolicyBounds, bool) {
	bounds, known := checkPolicyBounds[field]
	for _, bound := range c.bounded() {
		if bound.field == field && *bound.ceiling < bounds.Max {
			bounds.Max = *bound.ceiling
		}
	}
	return bounds, known
}

// CheckCeilingValues are the values of a policy or a job that a ceiling
// bounds. A job has no queue or active-job value; zero is within any
// ceiling.
type CheckCeilingValues struct {
	TimeoutMS        int64
	OutputLimitBytes int64
	QueueLimit       int
	MaxActiveJobs    int
	Execution        CheckExecutionSettings
}

// CeilingValues are the values of p that a ceiling bounds.
func (p CheckPolicy) CeilingValues() CheckCeilingValues {
	return CheckCeilingValues{
		TimeoutMS: p.MaxTimeoutMS, OutputLimitBytes: p.MaxOutputLimitBytes,
		QueueLimit: p.QueueLimit, MaxActiveJobs: p.MaxActiveJobs, Execution: p.Execution,
	}
}

// Exceeded lists each value above its ceiling as a field refusal with the
// rule RuleCeiling and the ceiling as Max. It is empty when every value is
// within the ceilings.
func (c CheckCeilings) Exceeded(values CheckCeilingValues) []*CheckPolicyFieldError {
	value := map[string]int64{
		FieldMaxTimeoutMS: values.TimeoutMS, FieldMaxOutputLimitBytes: values.OutputLimitBytes,
		FieldQueueLimit: int64(values.QueueLimit), FieldMaxActiveJobs: int64(values.MaxActiveJobs),
		FieldContainerCPUMillis: values.Execution.ContainerCPUMillis, FieldContainerMemoryBytes: values.Execution.ContainerMemoryBytes,
		FieldContainerPIDs: values.Execution.ContainerPIDs, FieldContainerScratchBytes: values.Execution.ContainerScratchBytes,
		FieldSourceMaxTotalBytes: values.Execution.Source.MaxTotalBytes,
	}
	var exceeded []*CheckPolicyFieldError
	for _, bound := range c.bounded() {
		if value[bound.field] > *bound.ceiling {
			exceeded = append(exceeded, &CheckPolicyFieldError{
				Field: bound.field, Rule: RuleCeiling, Max: *bound.ceiling, Value: strconv.FormatInt(value[bound.field], 10),
			})
		}
	}
	return exceeded
}

// admissionError is ErrCheckCeilingExceeded naming the fields above their
// ceilings, or nil when there is none.
func admissionError(exceeded []*CheckPolicyFieldError) error {
	if len(exceeded) == 0 {
		return nil
	}
	names := make([]string, 0, len(exceeded))
	for _, field := range exceeded {
		names = append(names, field.Field)
	}
	return fmt.Errorf("%w: %s", ErrCheckCeilingExceeded, strings.Join(names, ", "))
}

// CheckCeilingFields is the JSON form of CheckCeilings, stored and in the
// settings API. A missing field keeps the value it is applied to.
type CheckCeilingFields struct {
	TimeoutSeconds        *int64 `json:"timeout_seconds,omitempty"`
	OutputBytes           *int64 `json:"output_bytes,omitempty"`
	QueueLimit            *int64 `json:"queue_limit,omitempty"`
	ActiveJobs            *int64 `json:"active_jobs,omitempty"`
	ContainerCPUMillis    *int64 `json:"container_cpu_millis,omitempty"`
	ContainerMemoryBytes  *int64 `json:"container_memory_bytes,omitempty"`
	ContainerPIDs         *int64 `json:"container_pids,omitempty"`
	ContainerScratchBytes *int64 `json:"container_scratch_bytes,omitempty"`
	SourceTotalBytes      *int64 `json:"source_total_bytes,omitempty"`
}

// Apply returns c with the fields f names, checked.
func (f CheckCeilingFields) Apply(c CheckCeilings) (CheckCeilings, error) {
	if f.TimeoutSeconds != nil {
		minimum, maximum := CheckCeilingRange(FieldMaxTimeoutMS)
		if *f.TimeoutSeconds < minimum/1000 || *f.TimeoutSeconds > maximum/1000 {
			return CheckCeilings{}, fmt.Errorf("timeout_seconds is from %d to %d seconds", minimum/1000, maximum/1000)
		}
		c.TimeoutMS = *f.TimeoutSeconds * 1000
	}
	for _, field := range []struct {
		value  *int64
		target *int64
	}{
		{f.OutputBytes, &c.OutputLimitBytes}, {f.QueueLimit, &c.QueueLimit}, {f.ActiveJobs, &c.ActiveJobs},
		{f.ContainerCPUMillis, &c.ContainerCPUMillis}, {f.ContainerMemoryBytes, &c.ContainerMemoryBytes},
		{f.ContainerPIDs, &c.ContainerPIDs}, {f.ContainerScratchBytes, &c.ContainerScratchBytes},
		{f.SourceTotalBytes, &c.SourceTotalBytes},
	} {
		if field.value != nil {
			*field.target = *field.value
		}
	}
	if err := c.Validate(); err != nil {
		return CheckCeilings{}, err
	}
	return c, nil
}

// Fields is the JSON form of c, naming every field.
func (c CheckCeilings) Fields() CheckCeilingFields {
	timeout := c.TimeoutMS / 1000
	return CheckCeilingFields{
		TimeoutSeconds: &timeout, OutputBytes: &c.OutputLimitBytes, QueueLimit: &c.QueueLimit, ActiveJobs: &c.ActiveJobs,
		ContainerCPUMillis: &c.ContainerCPUMillis, ContainerMemoryBytes: &c.ContainerMemoryBytes,
		ContainerPIDs: &c.ContainerPIDs, ContainerScratchBytes: &c.ContainerScratchBytes, SourceTotalBytes: &c.SourceTotalBytes,
	}
}

// CheckCeilings returns the ceilings that apply now.
func (s *Store) CheckCeilings(ctx context.Context) (CheckCeilings, error) {
	return checkCeilings(ctx, s.db)
}

func checkCeilings(ctx context.Context, query querier) (CheckCeilings, error) {
	return readGroup(ctx, query, checkCeilingsKey, DefaultCheckCeilings, CheckCeilingFields.Apply)
}

// RepositoriesAboveCheckCeilings lists, in order, the repositories whose
// saved check policy is above the ceilings that apply now.
func (s *Store) RepositoriesAboveCheckCeilings(ctx context.Context) ([]string, error) {
	ceilings, err := s.CheckCeilings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, checkPolicySelect+` ORDER BY repository_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repositories []string
	for rows.Next() {
		policy, err := scanCheckPolicy(rows)
		if err != nil {
			return nil, err
		}
		if len(ceilings.Exceeded(policy.CeilingValues())) != 0 {
			repositories = append(repositories, policy.RepositoryID)
		}
	}
	return repositories, rows.Err()
}

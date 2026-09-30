package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Git and storage policies: the Git transfer limits, the budgets of file,
// diff and comparison views, repository maintenance and unused object
// cleanup. Like the other server-wide policies each is one metadata row
// holding a JSON object, machine-local and read where it applies.
//
// Each group has one JSON form, used both for the stored row and in the
// settings API (for example GitTransferFields). Its Apply sets the fields
// it names on a value and checks the result, so a stored row is read by
// applying it to the defaults, and a partial change is saved by applying
// it to the saved value. Every value, however it arrives, passes the same
// checks.

const (
	gitTransferLimitsKey   = "git_transfer_limits"
	browseLimitsKey        = "browse_limits"
	maintenanceKey         = "maintenance"
	unusedObjectCleanupKey = "unused_object_cleanup"
)

// readGroup reads the JSON row key into fields, a pointer to a group's
// JSON form, and applies it to the defaults with apply. A missing row is
// the defaults; a row that does not parse or apply is a PolicyError.
func readGroup[T any, F any](ctx context.Context, query querier, key string, defaults T, apply func(F, T) (T, error)) (T, error) {
	raw, found, err := policyValue(ctx, query, key)
	if err != nil || !found {
		return defaults, err
	}
	var fields F
	value := defaults
	if err = decodeStoredJSON(raw, &fields); err == nil {
		value, err = apply(fields, defaults)
	}
	if err != nil {
		var zero T
		return zero, &PolicyError{Key: key, Value: raw, Cause: err}
	}
	return value, nil
}

// storedGroup is the row text of a group's JSON form.
func storedGroup(fields any) (string, error) {
	encoded, err := json.Marshal(fields)
	return string(encoded), err
}

// secondsWithin converts a number of seconds named field to a duration
// from minimum to maximum. It compares the number with both bounds before
// converting it, so no value, however large or negative, can overflow into
// a valid one.
func secondsWithin(field string, seconds int64, minimum, maximum time.Duration) (time.Duration, error) {
	if seconds < int64(minimum/time.Second) || seconds > int64(maximum/time.Second) {
		return 0, fmt.Errorf("%s is from %d to %d seconds", field, int64(minimum/time.Second), int64(maximum/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}

// wholeSecondsWithin reports whether d is a whole number of seconds from
// minimum to maximum.
func wholeSecondsWithin(d, minimum, maximum time.Duration) bool {
	return d >= minimum && d <= maximum && d%time.Second == 0
}

// GitTransferLimits bound each Git transfer: a clone, fetch or push, or an
// archive download, and how many run at once.
type GitTransferLimits struct {
	// MaximumBytes bounds what a transfer receives and, apart, what it
	// sends.
	MaximumBytes int64
	// Operation bounds how long a transfer takes.
	Operation time.Duration
	// PerRepository is how many transfers of one repository run at once.
	PerRepository int
	// ExtraSlots are slots beyond PerRepository that only a repository
	// with no transfer running may take, so one busy repository never
	// makes the others wait. The server runs at most PerRepository plus
	// ExtraSlots transfers.
	ExtraSlots int
	// Idle stops a transfer whose client moves no data for this long.
	Idle time.Duration
	// QueueWait bounds how long a transfer waits for a slot.
	QueueWait time.Duration
}

// The bounds of the Git transfer limits. Each is finite: the largest keep
// one transfer from filling a disk or holding a slot for more than a day,
// and the server from starting more Git processes than a small computer
// runs well.
const (
	MinimumTransferBytes     = 1 << 20
	MaximumTransferBytes     = 64 << 30
	MinimumTransferOperation = time.Minute
	MaximumTransferOperation = 24 * time.Hour
	MaximumTransfersAtOnce   = 32
	MinimumTransferIdle      = 10 * time.Second
	MaximumTransferIdle      = time.Hour
	MinimumTransferQueue     = 5 * time.Second
	MaximumTransferQueue     = 10 * time.Minute
)

// DefaultGitTransferLimits apply while nothing was saved.
var DefaultGitTransferLimits = GitTransferLimits{
	MaximumBytes: 4 << 30, Operation: 30 * time.Minute,
	PerRepository: 4, ExtraSlots: 1, Idle: time.Minute, QueueWait: 90 * time.Second,
}

// Validate checks the limits against their bounds.
func (l GitTransferLimits) Validate() error {
	switch {
	case l.MaximumBytes < MinimumTransferBytes || l.MaximumBytes > MaximumTransferBytes:
		return errors.New("the largest transfer is from 1 MB to 64 GB (1 GB is 1024 MB)")
	case !wholeSecondsWithin(l.Operation, MinimumTransferOperation, MaximumTransferOperation):
		return errors.New("the longest transfer is a whole number of seconds from 1 minute to 24 hours")
	case l.PerRepository < 1 || l.PerRepository > MaximumTransfersAtOnce:
		return fmt.Errorf("transfers per repository are from 1 to %d", MaximumTransfersAtOnce)
	case l.ExtraSlots < 0 || l.ExtraSlots > MaximumTransfersAtOnce:
		return fmt.Errorf("extra slots are from 0 to %d", MaximumTransfersAtOnce)
	case !wholeSecondsWithin(l.Idle, MinimumTransferIdle, MaximumTransferIdle):
		return errors.New("the idle limit is a whole number of seconds from 10 seconds to 1 hour")
	case !wholeSecondsWithin(l.QueueWait, MinimumTransferQueue, MaximumTransferQueue):
		return errors.New("the queue wait is a whole number of seconds from 5 seconds to 10 minutes")
	}
	return nil
}

// Looser reports whether l lets transfers hold slots longer or run more
// at once than the defaults, so other clients may wait longer.
func (l GitTransferLimits) Looser() bool {
	d := DefaultGitTransferLimits
	return l.PerRepository > d.PerRepository || l.ExtraSlots > d.ExtraSlots || l.Idle > d.Idle || l.QueueWait > d.QueueWait
}

// GitTransferFields is the JSON form of GitTransferLimits, stored and in
// the settings API. A missing field keeps the value it is applied to.
type GitTransferFields struct {
	MaximumBytes     *int64 `json:"maximum_bytes,omitempty"`
	OperationSeconds *int64 `json:"operation_seconds,omitempty"`
	PerRepository    *int   `json:"per_repository,omitempty"`
	ExtraSlots       *int   `json:"extra_slots,omitempty"`
	IdleSeconds      *int64 `json:"idle_seconds,omitempty"`
	QueueSeconds     *int64 `json:"queue_seconds,omitempty"`
}

// Empty reports whether f names no field.
func (f GitTransferFields) Empty() bool { return f == GitTransferFields{} }

// Apply returns limits with the fields f names, checked.
func (f GitTransferFields) Apply(limits GitTransferLimits) (GitTransferLimits, error) {
	var err error
	if f.MaximumBytes != nil {
		limits.MaximumBytes = *f.MaximumBytes
	}
	if f.PerRepository != nil {
		limits.PerRepository = *f.PerRepository
	}
	if f.ExtraSlots != nil {
		limits.ExtraSlots = *f.ExtraSlots
	}
	for _, field := range []struct {
		name     string
		seconds  *int64
		target   *time.Duration
		min, max time.Duration
	}{
		{"operation_seconds", f.OperationSeconds, &limits.Operation, MinimumTransferOperation, MaximumTransferOperation},
		{"idle_seconds", f.IdleSeconds, &limits.Idle, MinimumTransferIdle, MaximumTransferIdle},
		{"queue_seconds", f.QueueSeconds, &limits.QueueWait, MinimumTransferQueue, MaximumTransferQueue},
	} {
		if field.seconds != nil {
			if *field.target, err = secondsWithin(field.name, *field.seconds, field.min, field.max); err != nil {
				return GitTransferLimits{}, err
			}
		}
	}
	if err := limits.Validate(); err != nil {
		return GitTransferLimits{}, err
	}
	return limits, nil
}

// Fields is the JSON form of l, naming every field.
func (l GitTransferLimits) Fields() GitTransferFields {
	return GitTransferFields{
		MaximumBytes: &l.MaximumBytes, OperationSeconds: seconds(l.Operation),
		PerRepository: &l.PerRepository, ExtraSlots: &l.ExtraSlots,
		IdleSeconds: seconds(l.Idle), QueueSeconds: seconds(l.QueueWait),
	}
}

func seconds(d time.Duration) *int64 {
	value := int64(d / time.Second)
	return &value
}

// GitTransferLimits returns the limits of a transfer that starts now.
func (s *Store) GitTransferLimits(ctx context.Context) (GitTransferLimits, error) {
	return readGroup(ctx, s.db, gitTransferLimitsKey, DefaultGitTransferLimits, GitTransferFields.Apply)
}

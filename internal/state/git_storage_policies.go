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

// addGroup checks value, a group to save or nil, and adds the row text of
// its JSON form to values under key.
func addGroup[T interface {
	Validate() error
	Fields() F
}, F any](values map[string]string, key string, value *T) error {
	if value == nil {
		return nil
	}
	if err := (*value).Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal((*value).Fields())
	values[key] = string(encoded)
	return err
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

// Maintenance decides when OwnGit packs repositories: small maintenance
// once a repository it wrote to has been idle for Idle, and a nightly
// consolidation of a repository with more than PackThreshold packs inside
// the window from WindowStart to WindowEnd, whole hours of local time that
// may pass midnight. CommandTime bounds each command and FullRepackTime the
// consolidating repack. While Enabled is false nothing is maintained.
type Maintenance struct {
	Enabled                bool
	WindowStart, WindowEnd int
	Idle                   time.Duration
	CommandTime            time.Duration
	FullRepackTime         time.Duration
	PackThreshold          int
}

// The bounds of the maintenance times and pack threshold.
const (
	MinimumMaintenanceTime  = time.Minute
	MaximumMaintenanceTime  = 24 * time.Hour
	MinimumPackThreshold    = 2
	MaximumPackThreshold    = 1000
	defaultMaintenanceHours = 2
)

// DefaultMaintenance applies while nothing was saved.
var DefaultMaintenance = Maintenance{
	Enabled: true, WindowStart: 3, WindowEnd: 5, Idle: 5 * time.Minute,
	CommandTime: 30 * time.Minute, FullRepackTime: 2 * time.Hour, PackThreshold: 20,
}

// WindowHours is how many hours the nightly window lasts.
func (m Maintenance) WindowHours() int { return (m.WindowEnd - m.WindowStart + 24) % 24 }

// Validate checks the maintenance choices against their bounds.
func (m Maintenance) Validate() error {
	switch {
	case m.WindowStart < 0 || m.WindowStart > 23 || m.WindowEnd < 0 || m.WindowEnd > 23 || m.WindowStart == m.WindowEnd:
		return errors.New("the window starts and ends at different whole hours from 0 to 23")
	case !wholeSecondsWithin(m.Idle, MinimumMaintenanceTime, MaximumMaintenanceTime):
		return errors.New("the idle time is a whole number of seconds from 1 minute to 24 hours")
	case !wholeSecondsWithin(m.CommandTime, MinimumMaintenanceTime, MaximumMaintenanceTime),
		!wholeSecondsWithin(m.FullRepackTime, MinimumMaintenanceTime, MaximumMaintenanceTime):
		return errors.New("a command budget is a whole number of seconds from 1 minute to 24 hours")
	case m.PackThreshold < MinimumPackThreshold || m.PackThreshold > MaximumPackThreshold:
		return fmt.Errorf("the pack threshold is from %d to %d", MinimumPackThreshold, MaximumPackThreshold)
	}
	return nil
}

// Looser reports whether m turns maintenance off or widens the window
// beyond the default two hours.
func (m Maintenance) Looser() bool {
	return !m.Enabled || m.WindowHours() > defaultMaintenanceHours
}

// MaintenanceFields is the JSON form of Maintenance, stored and in the
// settings API. A missing field keeps the value it is applied to.
type MaintenanceFields struct {
	Enabled           *bool  `json:"enabled,omitempty"`
	WindowStartHour   *int   `json:"window_start_hour,omitempty"`
	WindowEndHour     *int   `json:"window_end_hour,omitempty"`
	IdleSeconds       *int64 `json:"idle_seconds,omitempty"`
	CommandSeconds    *int64 `json:"command_seconds,omitempty"`
	FullRepackSeconds *int64 `json:"full_repack_seconds,omitempty"`
	PackThreshold     *int   `json:"pack_threshold,omitempty"`
}

// Apply returns m with the fields f names, checked.
func (f MaintenanceFields) Apply(m Maintenance) (Maintenance, error) {
	var err error
	if f.Enabled != nil {
		m.Enabled = *f.Enabled
	}
	if f.WindowStartHour != nil {
		m.WindowStart = *f.WindowStartHour
	}
	if f.WindowEndHour != nil {
		m.WindowEnd = *f.WindowEndHour
	}
	if f.PackThreshold != nil {
		m.PackThreshold = *f.PackThreshold
	}
	for _, field := range []struct {
		name    string
		seconds *int64
		target  *time.Duration
	}{{"idle_seconds", f.IdleSeconds, &m.Idle}, {"command_seconds", f.CommandSeconds, &m.CommandTime}, {"full_repack_seconds", f.FullRepackSeconds, &m.FullRepackTime}} {
		if field.seconds != nil {
			if *field.target, err = secondsWithin(field.name, *field.seconds, MinimumMaintenanceTime, MaximumMaintenanceTime); err != nil {
				return Maintenance{}, err
			}
		}
	}
	if err := m.Validate(); err != nil {
		return Maintenance{}, err
	}
	return m, nil
}

// Fields is the JSON form of m, naming every field.
func (m Maintenance) Fields() MaintenanceFields {
	return MaintenanceFields{
		Enabled: &m.Enabled, WindowStartHour: &m.WindowStart, WindowEndHour: &m.WindowEnd,
		IdleSeconds: seconds(m.Idle), CommandSeconds: seconds(m.CommandTime), FullRepackSeconds: seconds(m.FullRepackTime),
		PackThreshold: &m.PackThreshold,
	}
}

// Maintenance returns the maintenance choices the next job follows.
func (s *Store) Maintenance(ctx context.Context) (Maintenance, error) {
	return readGroup(ctx, s.db, maintenanceKey, DefaultMaintenance, MaintenanceFields.Apply)
}

// BrowseLimits bound what one page or API answer reads to show a file, a
// diff or a comparison. A larger budget never outlasts the page's own time
// limit, and a cached result is kept apart for each budget, so a changed
// budget is never answered with a result cut at the old one.
type BrowseLimits struct {
	// RawBytes bounds a raw file download; a larger file is refused.
	RawBytes int64
	// FileBytes bounds the text shown for one file.
	FileBytes int64
	// CommitPatchBytes bounds the diff read for a commit page, and
	// FilePatchBytes the diff of one file of it.
	CommitPatchBytes, FilePatchBytes int64
	// CommitFileBytes bounds the diff of one file shown within a commit
	// page; a larger file is listed with a link to its diff alone.
	CommitFileBytes int64
	// CompareBytes and CompareTime bound the diff of a pull request.
	CompareBytes int64
	CompareTime  time.Duration
}

// DefaultBrowseLimits apply while nothing was saved.
var DefaultBrowseLimits = BrowseLimits{
	RawBytes: 10 << 20, FileBytes: 2 << 20, CommitPatchBytes: 2 << 20, FilePatchBytes: 8 << 20,
	CommitFileBytes: 256 << 10, CompareBytes: 8 << 20, CompareTime: 20 * time.Second,
}

// The bounds of the browsing limits. Each view is held in memory while it
// is built, so the largest stay well below what a small computer holds for
// several pages at once.
const (
	MinimumBrowseBytes     = 64 << 10
	MaximumBrowseBytes     = 64 << 20
	MinimumRawBytes        = 1 << 20
	MaximumRawBytes        = 256 << 20
	MinimumCommitFileBytes = 16 << 10
	MaximumCommitFileBytes = 16 << 20
	MinimumCompareTime     = 5 * time.Second
	MaximumCompareTime     = time.Minute
)

// Validate checks the limits against their bounds.
func (l BrowseLimits) Validate() error {
	within := func(value, minimum, maximum int64) bool { return value >= minimum && value <= maximum }
	switch {
	case !within(l.RawBytes, MinimumRawBytes, MaximumRawBytes):
		return errors.New("the raw file size is from 1 MB to 256 MB")
	case !within(l.FileBytes, MinimumBrowseBytes, MaximumBrowseBytes), !within(l.CommitPatchBytes, MinimumBrowseBytes, MaximumBrowseBytes),
		!within(l.FilePatchBytes, MinimumBrowseBytes, MaximumBrowseBytes), !within(l.CompareBytes, MinimumBrowseBytes, MaximumBrowseBytes):
		return errors.New("a file, diff or comparison size is from 64 KB to 64 MB")
	case !within(l.CommitFileBytes, MinimumCommitFileBytes, MaximumCommitFileBytes):
		return errors.New("the size of one file's diff on a commit page is from 16 KB to 16 MB")
	case !wholeSecondsWithin(l.CompareTime, MinimumCompareTime, MaximumCompareTime):
		return errors.New("the comparison time is a whole number of seconds from 5 seconds to 1 minute")
	}
	return nil
}

// Looser reports whether l lets any view read more or longer than the
// defaults.
func (l BrowseLimits) Looser() bool {
	d := DefaultBrowseLimits
	return l.RawBytes > d.RawBytes || l.FileBytes > d.FileBytes || l.CommitPatchBytes > d.CommitPatchBytes || l.FilePatchBytes > d.FilePatchBytes ||
		l.CommitFileBytes > d.CommitFileBytes || l.CompareBytes > d.CompareBytes || l.CompareTime > d.CompareTime
}

// BrowseFields is the JSON form of BrowseLimits, stored and in the
// settings API. A missing field keeps the value it is applied to.
type BrowseFields struct {
	RawBytes         *int64 `json:"raw_bytes,omitempty"`
	FileBytes        *int64 `json:"file_bytes,omitempty"`
	CommitPatchBytes *int64 `json:"commit_patch_bytes,omitempty"`
	FilePatchBytes   *int64 `json:"file_patch_bytes,omitempty"`
	CommitFileBytes  *int64 `json:"commit_file_bytes,omitempty"`
	CompareBytes     *int64 `json:"compare_bytes,omitempty"`
	CompareSeconds   *int64 `json:"compare_seconds,omitempty"`
}

// Apply returns l with the fields f names, checked.
func (f BrowseFields) Apply(l BrowseLimits) (BrowseLimits, error) {
	for _, field := range []struct {
		value  *int64
		target *int64
	}{
		{f.RawBytes, &l.RawBytes}, {f.FileBytes, &l.FileBytes}, {f.CommitPatchBytes, &l.CommitPatchBytes},
		{f.FilePatchBytes, &l.FilePatchBytes}, {f.CommitFileBytes, &l.CommitFileBytes}, {f.CompareBytes, &l.CompareBytes},
	} {
		if field.value != nil {
			*field.target = *field.value
		}
	}
	if f.CompareSeconds != nil {
		var err error
		if l.CompareTime, err = secondsWithin("compare_seconds", *f.CompareSeconds, MinimumCompareTime, MaximumCompareTime); err != nil {
			return BrowseLimits{}, err
		}
	}
	if err := l.Validate(); err != nil {
		return BrowseLimits{}, err
	}
	return l, nil
}

// Fields is the JSON form of l, naming every field.
func (l BrowseLimits) Fields() BrowseFields {
	return BrowseFields{
		RawBytes: &l.RawBytes, FileBytes: &l.FileBytes, CommitPatchBytes: &l.CommitPatchBytes, FilePatchBytes: &l.FilePatchBytes,
		CommitFileBytes: &l.CommitFileBytes, CompareBytes: &l.CompareBytes, CompareSeconds: seconds(l.CompareTime),
	}
}

// BrowseLimits returns the limits of a view read now.
func (s *Store) BrowseLimits(ctx context.Context) (BrowseLimits, error) {
	return readGroup(ctx, s.db, browseLimitsKey, DefaultBrowseLimits, BrowseFields.Apply)
}

// UnusedObjectCleanup decides whether nightly maintenance removes Git
// objects that no ref reaches, once they are older than Grace. It is off
// by default. Objects that any ref reaches, kept history included, stay.
type UnusedObjectCleanup struct {
	Enabled bool
	Grace   time.Duration
}

// The bounds of the cleanup grace period, in whole days. The shortest
// outlasts any transfer, import or check that may still use a new object
// before a ref reaches it.
const (
	MinimumCleanupGrace = 2 * 24 * time.Hour
	MaximumCleanupGrace = 365 * 24 * time.Hour
)

// DefaultUnusedObjectCleanup applies while nothing was saved.
var DefaultUnusedObjectCleanup = UnusedObjectCleanup{Grace: 14 * 24 * time.Hour}

// Validate checks the grace period against its bounds.
func (c UnusedObjectCleanup) Validate() error {
	if c.Grace < MinimumCleanupGrace || c.Grace > MaximumCleanupGrace || c.Grace%(24*time.Hour) != 0 {
		return errors.New("the grace period is a whole number of days from 2 to 365")
	}
	return nil
}

// CleanupFields is the JSON form of UnusedObjectCleanup, stored and in the
// settings API. A missing field keeps the value it is applied to.
type CleanupFields struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	GraceDays *int64 `json:"grace_days,omitempty"`
}

// Apply returns c with the fields f names, checked.
func (f CleanupFields) Apply(c UnusedObjectCleanup) (UnusedObjectCleanup, error) {
	if f.Enabled != nil {
		c.Enabled = *f.Enabled
	}
	if f.GraceDays != nil {
		days := *f.GraceDays
		if days < int64(MinimumCleanupGrace/(24*time.Hour)) || days > int64(MaximumCleanupGrace/(24*time.Hour)) {
			return UnusedObjectCleanup{}, errors.New("grace_days is from 2 to 365")
		}
		c.Grace = time.Duration(days) * 24 * time.Hour
	}
	if err := c.Validate(); err != nil {
		return UnusedObjectCleanup{}, err
	}
	return c, nil
}

// Fields is the JSON form of c, naming every field.
func (c UnusedObjectCleanup) Fields() CleanupFields {
	days := int64(c.Grace / (24 * time.Hour))
	return CleanupFields{Enabled: &c.Enabled, GraceDays: &days}
}

// UnusedObjectCleanup returns the cleanup choice the next maintenance
// follows.
func (s *Store) UnusedObjectCleanup(ctx context.Context) (UnusedObjectCleanup, error) {
	return readGroup(ctx, s.db, unusedObjectCleanupKey, DefaultUnusedObjectCleanup, CleanupFields.Apply)
}

// RepositoryObjectsInUse reports whether repository id has a check job
// that has not finished (pending, claimed or started) or an import run in
// progress. Either may still need objects that no ref reaches, so unused
// object cleanup waits for them.
func (s *Store) RepositoryObjectsInUse(ctx context.Context, id string) (bool, error) {
	var busy bool
	err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM check_jobs WHERE repository_id=? AND status IN ('pending','claimed','started'))
		OR EXISTS(SELECT 1 FROM import_runs WHERE repository_id=? AND status IN ('preparing','fetching','indexing','inspecting','publishing'))`, id, id).Scan(&busy)
	return busy, err
}

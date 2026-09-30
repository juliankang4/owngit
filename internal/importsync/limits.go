package importsync

import (
	"errors"
	"fmt"
	"math"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/importfetch"
	"owngit/internal/state"
)

// Limits bound every import stage. Zero fields take their default value.
//
// The transport limits describe HTTP entity bytes after transfer decoding, not
// TLS, TCP, or chunk-framing bytes. AllocationGuard is passed to Git as
// GIT_ALLOC_LIMIT, which the Git source defines as a per-allocation guard on
// xmalloc/xrealloc/xcalloc; it is not an OS resident-memory quota.
type Limits struct {
	Fetch              importfetch.Limits
	RunTimeout         time.Duration
	IndexTimeout       time.Duration
	VerifyTimeout      time.Duration
	InspectTimeout     time.Duration
	PublishTimeout     time.Duration
	CommandOutputBytes int64
	AllocationGuard    string
	LFS                LFSLimits
}

// LFSLimits bound Git LFS pointer inspection. If MaxPointerBytes is below the
// shared parser's maximum serialized pointer size, skipped candidate blobs make
// the inspection incomplete. Larger blobs cannot match the pointer grammar.
type LFSLimits struct {
	MaxObjects         int
	MaxCandidateBlobs  int
	MaxCandidateBytes  int64
	MaxPointerBytes    int64
	MaxObjectListBytes int64
	MaxTypeListBytes   int64
	Timeout            time.Duration
}

// DefaultLimits returns conservative finite bounds.
func DefaultLimits() Limits {
	return Limits{
		Fetch:              importfetch.DefaultLimits(),
		RunTimeout:         60 * time.Minute,
		IndexTimeout:       20 * time.Minute,
		VerifyTimeout:      10 * time.Minute,
		InspectTimeout:     5 * time.Minute,
		PublishTimeout:     5 * time.Minute,
		CommandOutputBytes: 1 << 20,
		LFS: LFSLimits{
			MaxObjects:         200_000,
			MaxCandidateBlobs:  100_000,
			MaxCandidateBytes:  32 << 20,
			MaxPointerBytes:    1024,
			MaxObjectListBytes: 48 << 20,
			MaxTypeListBytes:   64 << 20,
			Timeout:            3 * time.Minute,
		},
	}
}

func (l Limits) effective() (Limits, error) {
	defaults := DefaultLimits()
	limits := l
	if limits.RunTimeout == 0 {
		limits.RunTimeout = defaults.RunTimeout
	}
	if limits.IndexTimeout == 0 {
		limits.IndexTimeout = defaults.IndexTimeout
	}
	if limits.VerifyTimeout == 0 {
		limits.VerifyTimeout = defaults.VerifyTimeout
	}
	if limits.InspectTimeout == 0 {
		limits.InspectTimeout = defaults.InspectTimeout
	}
	if limits.PublishTimeout == 0 {
		limits.PublishTimeout = defaults.PublishTimeout
	}
	if limits.CommandOutputBytes == 0 {
		limits.CommandOutputBytes = defaults.CommandOutputBytes
	}
	if limits.LFS.MaxObjects == 0 {
		limits.LFS.MaxObjects = defaults.LFS.MaxObjects
	}
	if limits.LFS.MaxCandidateBlobs == 0 {
		limits.LFS.MaxCandidateBlobs = defaults.LFS.MaxCandidateBlobs
	}
	if limits.LFS.MaxCandidateBytes == 0 {
		limits.LFS.MaxCandidateBytes = defaults.LFS.MaxCandidateBytes
	}
	if limits.LFS.MaxPointerBytes == 0 {
		limits.LFS.MaxPointerBytes = defaults.LFS.MaxPointerBytes
	}
	if limits.LFS.Timeout == 0 {
		limits.LFS.Timeout = defaults.LFS.Timeout
	}
	if limits.LFS.MaxObjectListBytes == 0 {
		limits.LFS.MaxObjectListBytes = defaults.LFS.MaxObjectListBytes
	}
	if limits.LFS.MaxTypeListBytes == 0 {
		limits.LFS.MaxTypeListBytes = defaults.LFS.MaxTypeListBytes
	}
	for name, value := range map[string]time.Duration{
		"run timeout": limits.RunTimeout, "index timeout": limits.IndexTimeout,
		"verify timeout": limits.VerifyTimeout, "inspection timeout": limits.InspectTimeout,
		"publication timeout": limits.PublishTimeout, "LFS timeout": limits.LFS.Timeout,
	} {
		if value < 0 {
			return Limits{}, fmt.Errorf("import %s is negative", name)
		}
	}
	if limits.RunTimeout > 24*time.Hour {
		return Limits{}, errors.New("import run timeout exceeds 24h")
	}
	if limits.CommandOutputBytes < 64<<10 || limits.CommandOutputBytes > 64<<20 {
		return Limits{}, errors.New("import command output bound must be between 64 KiB and 64 MiB")
	}
	if limits.LFS.MaxObjects < 1 || limits.LFS.MaxCandidateBlobs < 1 || limits.LFS.MaxCandidateBytes < 1 || limits.LFS.MaxPointerBytes < 16 {
		return Limits{}, errors.New("import LFS bounds are too small")
	}
	if limits.LFS.MaxObjectListBytes < 64<<10 || limits.LFS.MaxTypeListBytes < 64<<10 {
		return Limits{}, errors.New("import LFS output bounds are too small")
	}
	fetch, err := importfetch.EffectiveLimits(limits.Fetch)
	if err != nil {
		return Limits{}, fmt.Errorf("import transfer limits are invalid: %w", err)
	}
	limits.Fetch = fetch
	return limits, nil
}

// withSource fills the limits a caller left unset from a source's saved
// limits; effective fills the rest with the defaults. A caller's own limit
// wins, so a server that bounds its request by a run time keeps that bound.
// The bounds that follow from a saved limit are derived here unless the
// caller set them: the LFS listing output grows with the objects scanned,
// and the transport derives its request and total body bounds.
func (l Limits) withSource(saved state.ImportLimits) (Limits, error) {
	if err := saved.Validate(); err != nil {
		return Limits{}, err
	}
	seconds := func(target *time.Duration, value int64) error {
		if value == 0 || *target != 0 {
			return nil
		}
		if value > int64(math.MaxInt64/time.Second) {
			return errors.New("import limit is too long")
		}
		*target = time.Duration(value) * time.Second
		return nil
	}
	for _, item := range []struct {
		target *time.Duration
		value  int64
	}{
		{&l.RunTimeout, saved.RunSeconds}, {&l.Fetch.TotalTimeout, saved.FetchSeconds},
		{&l.IndexTimeout, saved.IndexSeconds}, {&l.VerifyTimeout, saved.VerifySeconds},
		{&l.Fetch.TLSHandshakeTimeout, saved.TLSHandshakeSeconds}, {&l.Fetch.ResponseHeaderTimeout, saved.ResponseHeaderSeconds},
	} {
		if err := seconds(item.target, item.value); err != nil {
			return Limits{}, err
		}
	}
	if saved.PackBytes != 0 && l.Fetch.MaxPackBytes == 0 {
		l.Fetch.MaxPackBytes = saved.PackBytes
	}
	if saved.AdvertisementBytes != 0 && l.Fetch.Advertisement.MaxTotalBytes == 0 {
		l.Fetch.Advertisement.MaxTotalBytes = saved.AdvertisementBytes
	}
	if saved.Refs != 0 && l.Fetch.Advertisement.MaxRefRecords == 0 {
		l.Fetch.Advertisement.MaxRefRecords = int(saved.Refs)
	}
	if saved.LFSObjects != 0 && l.LFS.MaxObjects == 0 {
		l.LFS.MaxObjects = int(saved.LFSObjects)
		defaults := DefaultLimits().LFS
		if saved.LFSObjects > int64(defaults.MaxObjects) {
			// The listings scale with the objects they name; the defaults are
			// sized for the default object count. Bounded by the object
			// ceiling, the product cannot overflow.
			scale := func(target *int64, value int64) {
				if *target == 0 {
					*target = (value*saved.LFSObjects + int64(defaults.MaxObjects) - 1) / int64(defaults.MaxObjects)
				}
			}
			scale(&l.LFS.MaxObjectListBytes, defaults.MaxObjectListBytes)
			scale(&l.LFS.MaxTypeListBytes, defaults.MaxTypeListBytes)
		}
	}
	return l, nil
}

// checkSourceLimits refuses saved limits whose stages cannot finish inside
// the run: the fetch, verification and indexing each within the run time, and
// indexing, which reads the pack while it arrives, within the fetch time.
func checkSourceLimits(saved state.ImportLimits) error {
	limits, err := Limits{}.withSource(saved)
	if err != nil {
		return err
	}
	if limits, err = limits.effective(); err != nil {
		return err
	}
	switch {
	case limits.Fetch.TotalTimeout > limits.RunTimeout:
		return fmt.Errorf("the fetch time (%s) is longer than the run time (%s); raise the run time too", limits.Fetch.TotalTimeout, limits.RunTimeout)
	case limits.VerifyTimeout > limits.RunTimeout:
		return fmt.Errorf("the verification time (%s) is longer than the run time (%s); raise the run time too", limits.VerifyTimeout, limits.RunTimeout)
	case limits.IndexTimeout > limits.Fetch.TotalTimeout:
		return fmt.Errorf("the indexing time (%s) is longer than the fetch time (%s), which includes indexing; raise the fetch time too", limits.IndexTimeout, limits.Fetch.TotalTimeout)
	}
	return nil
}

// limitValues states effective limits in the units a source saves them.
func limitValues(l Limits) state.ImportLimits {
	return state.ImportLimits{
		PackBytes:             l.Fetch.MaxPackBytes,
		AdvertisementBytes:    l.Fetch.Advertisement.MaxTotalBytes,
		Refs:                  int64(l.Fetch.Advertisement.MaxRefRecords),
		RunSeconds:            int64(l.RunTimeout / time.Second),
		FetchSeconds:          int64(l.Fetch.TotalTimeout / time.Second),
		IndexSeconds:          int64(l.IndexTimeout / time.Second),
		VerifySeconds:         int64(l.VerifyTimeout / time.Second),
		TLSHandshakeSeconds:   int64(l.Fetch.TLSHandshakeTimeout / time.Second),
		ResponseHeaderSeconds: int64(l.Fetch.ResponseHeaderTimeout / time.Second),
		LFSObjects:            int64(l.LFS.MaxObjects),
	}
}

// SourceLimits returns the limits a run of a source with these saved limits
// uses, for showing them and for bounding a request that waits for the run.
func SourceLimits(saved state.ImportLimits) (Limits, error) {
	limits, err := Limits{}.withSource(saved)
	if err != nil {
		return Limits{}, err
	}
	return limits.effective()
}

// commandLimits is the per-command bound used for every import-owned Git
// subprocess. Replacement refs stay inert so object identity is always raw.
func (l Limits) commandLimits(timeout time.Duration) gitexec.CommandLimits {
	limits := gitexec.CommandLimits{
		Timeout: timeout, OutputLimit: l.CommandOutputBytes,
		Environment: []string{"GIT_NO_REPLACE_OBJECTS=1"},
	}
	if l.AllocationGuard != "" {
		limits.Environment = append(limits.Environment, "GIT_ALLOC_LIMIT="+l.AllocationGuard)
	}
	return limits
}

// commandLimitsFor overrides only the output bound for commands that
// legitimately emit more than the default capture size.
func (l Limits) commandLimitsFor(timeout time.Duration, outputLimit int64) gitexec.CommandLimits {
	limits := l.commandLimits(timeout)
	if outputLimit > limits.OutputLimit {
		limits.OutputLimit = outputLimit
	}
	return limits
}

// LimitsView reports the effective limits so callers never have to guess what
// was actually applied.
type LimitsView struct {
	MaxPackBytes          int64         `json:"max_pack_bytes"`
	MaxTotalBodyBytes     int64         `json:"max_total_body_bytes"`
	MaxRequestBytes       int64         `json:"max_request_bytes"`
	FetchTimeout          time.Duration `json:"fetch_timeout"`
	RunTimeout            time.Duration `json:"run_timeout"`
	IndexTimeout          time.Duration `json:"index_timeout"`
	VerifyTimeout         time.Duration `json:"verify_timeout"`
	InspectTimeout        time.Duration `json:"inspect_timeout"`
	PublishTimeout        time.Duration `json:"publish_timeout"`
	CommandOutputBytes    int64         `json:"command_output_bytes"`
	AllocationGuard       string        `json:"allocation_guard,omitempty"`
	LFSMaxObjects         int           `json:"lfs_max_objects"`
	LFSMaxCandidateBlobs  int           `json:"lfs_max_candidate_blobs"`
	LFSMaxCandidateBytes  int64         `json:"lfs_max_candidate_bytes"`
	LFSMaxPointerBytes    int64         `json:"lfs_max_pointer_bytes"`
	LFSMaxObjectListBytes int64         `json:"lfs_max_object_list_bytes"`
	LFSMaxTypeListBytes   int64         `json:"lfs_max_type_list_bytes"`
	LFSTimeout            time.Duration `json:"lfs_timeout"`
}

func (l Limits) view() LimitsView {
	return LimitsView{
		MaxPackBytes:          l.Fetch.MaxPackBytes,
		MaxTotalBodyBytes:     l.Fetch.MaxTotalBodyBytes,
		MaxRequestBytes:       l.Fetch.MaxRequestBytes,
		FetchTimeout:          l.Fetch.TotalTimeout,
		RunTimeout:            l.RunTimeout,
		IndexTimeout:          l.IndexTimeout,
		VerifyTimeout:         l.VerifyTimeout,
		InspectTimeout:        l.InspectTimeout,
		PublishTimeout:        l.PublishTimeout,
		CommandOutputBytes:    l.CommandOutputBytes,
		AllocationGuard:       l.AllocationGuard,
		LFSMaxObjects:         l.LFS.MaxObjects,
		LFSMaxCandidateBlobs:  l.LFS.MaxCandidateBlobs,
		LFSMaxCandidateBytes:  l.LFS.MaxCandidateBytes,
		LFSMaxPointerBytes:    l.LFS.MaxPointerBytes,
		LFSMaxObjectListBytes: l.LFS.MaxObjectListBytes,
		LFSMaxTypeListBytes:   l.LFS.MaxTypeListBytes,
		LFSTimeout:            l.LFS.Timeout,
	}
}

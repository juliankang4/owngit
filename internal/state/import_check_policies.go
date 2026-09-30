package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Import source options.
//
// Each import source has machine-local connection choices and capacity
// limits beside its URL. They are off, or at OwnGit's built-in values, until
// the owner changes them for that source, and an offline restore resets them.

// Redirect policies of an import source, as stored in redirect_policy.
const (
	ImportRedirectRefuse     = "refuse"
	ImportRedirectSameOrigin = "same_origin"
	ImportRedirectApproved   = "approved"
)

// ImportOptions are one source's connection choices and limits.
type ImportOptions struct {
	// AllowPlainHTTP lets the source, and a redirect, use http://.
	AllowPlainHTTP bool
	// Redirects is one of the ImportRedirect* policies.
	// ApprovedRedirectOrigin is the one other origin ImportRedirectApproved
	// follows, such as https://mirror.example.
	Redirects              string
	ApprovedRedirectOrigin string
	// AllowReservedAddresses lets the source reach usable special-purpose
	// unicast addresses. Private addresses keep their own consent.
	AllowReservedAddresses bool
	Limits                 ImportLimits
}

// DefaultImportOptions are the options of a source nobody changed.
func DefaultImportOptions() ImportOptions {
	return ImportOptions{Redirects: ImportRedirectRefuse}
}

// SameTransport reports whether two option sets reach the source the same
// way. A transport change is new execution authority; a limits change only
// applies to later runs.
func (o ImportOptions) SameTransport(other ImportOptions) bool {
	return o.AllowPlainHTTP == other.AllowPlainHTTP && o.Redirects == other.Redirects &&
		o.ApprovedRedirectOrigin == other.ApprovedRedirectOrigin && o.AllowReservedAddresses == other.AllowReservedAddresses
}

// WithoutTransport keeps only the limits. A changed source URL starts from
// this, so a consent given for one address never carries over to another.
func (o ImportOptions) WithoutTransport() ImportOptions {
	reset := DefaultImportOptions()
	reset.Limits = o.Limits
	return reset
}

// ImportLimits are the capacity and time limits the owner set for one
// source. A zero field is not set and uses OwnGit's built-in limit, which the
// import service owns; only changed limits are stored.
type ImportLimits struct {
	PackBytes             int64 `json:"pack_bytes,omitempty"`
	AdvertisementBytes    int64 `json:"advertisement_bytes,omitempty"`
	Refs                  int64 `json:"refs,omitempty"`
	RunSeconds            int64 `json:"run_seconds,omitempty"`
	FetchSeconds          int64 `json:"fetch_seconds,omitempty"`
	IndexSeconds          int64 `json:"index_seconds,omitempty"`
	VerifySeconds         int64 `json:"verify_seconds,omitempty"`
	TLSHandshakeSeconds   int64 `json:"tls_handshake_seconds,omitempty"`
	ResponseHeaderSeconds int64 `json:"response_header_seconds,omitempty"`
	LFSObjects            int64 `json:"lfs_objects,omitempty"`
}

// ImportLimitField is one adjustable import limit and the range a set value
// must be in. Seconds and bytes are the stored units.
type ImportLimitField struct {
	Name string
	Min  int64
	Max  int64
}

// ImportLimitFields lists every adjustable import limit, in the order an
// interface shows them. The last four are the deeper transport and scan
// limits.
var ImportLimitFields = []ImportLimitField{
	{Name: "pack_bytes", Min: 1 << 20, Max: 1 << 40},
	{Name: "run_seconds", Min: 60, Max: 24 * 60 * 60},
	{Name: "fetch_seconds", Min: 60, Max: 24 * 60 * 60},
	{Name: "index_seconds", Min: 60, Max: 24 * 60 * 60},
	{Name: "verify_seconds", Min: 60, Max: 24 * 60 * 60},
	{Name: "refs", Min: 1, Max: 200_000},
	{Name: "advertisement_bytes", Min: 64 << 10, Max: 64 << 20},
	{Name: "tls_handshake_seconds", Min: 1, Max: 10 * 60},
	{Name: "response_header_seconds", Min: 1, Max: 60 * 60},
	{Name: "lfs_objects", Min: 1, Max: 1_000_000},
}

// CheckImportLimit refuses a value the named limit cannot be set to.
func CheckImportLimit(name string, value int64) error {
	for _, field := range ImportLimitFields {
		if field.Name == name {
			if value < field.Min || value > field.Max {
				return &ImportLimitRangeError{Field: field, Value: value}
			}
			return nil
		}
	}
	return fmt.Errorf("unknown import limit %q", name)
}

// Field returns the named limit for reading or setting.
func (l *ImportLimits) Field(name string) (*int64, bool) {
	switch name {
	case "pack_bytes":
		return &l.PackBytes, true
	case "advertisement_bytes":
		return &l.AdvertisementBytes, true
	case "refs":
		return &l.Refs, true
	case "run_seconds":
		return &l.RunSeconds, true
	case "fetch_seconds":
		return &l.FetchSeconds, true
	case "index_seconds":
		return &l.IndexSeconds, true
	case "verify_seconds":
		return &l.VerifySeconds, true
	case "tls_handshake_seconds":
		return &l.TLSHandshakeSeconds, true
	case "response_header_seconds":
		return &l.ResponseHeaderSeconds, true
	case "lfs_objects":
		return &l.LFSObjects, true
	}
	return nil, false
}

// ImportLimitRangeError refuses a limit outside its range.
type ImportLimitRangeError struct {
	Field ImportLimitField
	Value int64
}

func (e *ImportLimitRangeError) Error() string {
	return fmt.Sprintf("import limit %s must be between %d and %d, not %d", e.Field.Name, e.Field.Min, e.Field.Max, e.Value)
}

// Validate refuses a set limit outside its range. Unset limits are valid.
func (l ImportLimits) Validate() error {
	for _, field := range ImportLimitFields {
		value, _ := l.Field(field.Name)
		if *value != 0 && (*value < field.Min || *value > field.Max) {
			return &ImportLimitRangeError{Field: field, Value: *value}
		}
	}
	return nil
}

func encodeImportLimits(limits ImportLimits) (string, error) {
	if err := limits.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(limits)
	return string(encoded), err
}

// decodeImportLimits reads column text strictly: anything but an object of
// known limits, each set to a value in its range, is an error, never a
// default. An unset limit is absent, not zero.
func decodeImportLimits(text string) (ImportLimits, error) {
	var values map[string]int64
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return ImportLimits{}, fmt.Errorf("stored limits are not readable: %w", err)
	}
	if values == nil {
		return ImportLimits{}, errors.New("stored limits are not an object")
	}
	var limits ImportLimits
	for name, value := range values {
		if err := CheckImportLimit(name, value); err != nil {
			return ImportLimits{}, err
		}
		field, _ := limits.Field(name)
		*field = value
	}
	return limits, nil
}

// ImportSourceSettingError is a saved option of one import source that
// cannot be used. The source's other fields were read.
type ImportSourceSettingError struct {
	RepositoryID string
	// Setting is "limits" or "redirects".
	Setting string
	Cause   error
}

func (e *ImportSourceSettingError) Error() string {
	return fmt.Sprintf("import source %q: the saved %s cannot be used (%v)", e.RepositoryID, e.Setting, e.Cause)
}

func (e *ImportSourceSettingError) Unwrap() error { return e.Cause }

// Advice says which setting to save again and where.
func (e *ImportSourceSettingError) Advice() string {
	if e.Setting == "limits" {
		return "This source's saved import limits cannot be used. Set them again on the repository's Import tab, or with owngit import configure --limit."
	}
	return "This source's saved redirect choice cannot be used. Set it again on the repository's Import tab, or with owngit import configure --redirects."
}

// decodeImportOptions reads the option columns of one source row.
func decodeImportOptions(repositoryID string, plainHTTP bool, redirects, approved string, reserved bool, limitsText string) (ImportOptions, error) {
	options := ImportOptions{AllowPlainHTTP: plainHTTP, Redirects: redirects, ApprovedRedirectOrigin: approved, AllowReservedAddresses: reserved}
	var problems []error
	if limits, err := decodeImportLimits(limitsText); err != nil {
		problems = append(problems, &ImportSourceSettingError{RepositoryID: repositoryID, Setting: "limits", Cause: err})
	} else {
		options.Limits = limits
	}
	if err := validateImportRedirects(redirects, approved); err != nil {
		problems = append(problems, &ImportSourceSettingError{RepositoryID: repositoryID, Setting: "redirects", Cause: err})
	}
	return options, errors.Join(problems...)
}

// validateImportRedirects checks the policy and that an origin is stored
// exactly when the policy uses one. The import service checks the origin's
// form, as it does a source URL.
func validateImportRedirects(policy, origin string) error {
	switch policy {
	case ImportRedirectRefuse, ImportRedirectSameOrigin:
		if origin != "" {
			return errors.New("an approved redirect origin is stored without the approved-origin policy")
		}
	case ImportRedirectApproved:
		if origin == "" || len(origin) > 2048 {
			return errors.New("the approved-origin policy has no valid origin")
		}
	default:
		return fmt.Errorf("unknown redirect policy %s", strconv.Quote(policy))
	}
	return nil
}

func (o ImportOptions) validate() error {
	if err := validateImportRedirects(o.Redirects, o.ApprovedRedirectOrigin); err != nil {
		return err
	}
	return o.Limits.Validate()
}

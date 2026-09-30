// Package importfetch performs one confined Git smart-HTTP upload-pack fetch.
//
// It owns HTTPS and protocol framing, but it does not own staging, object
// indexing, ref verification, publication, or synchronization policy. Git is
// never given network authority by this package.
package importfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"owngit/internal/importgit"
)

var (
	ErrInvalidRequest       = errors.New("invalid import fetch request")
	ErrNameResolution       = errors.New("source name resolution failed")
	ErrAddressPolicy        = errors.New("source address is forbidden by import policy")
	ErrConnection           = errors.New("source HTTPS request failed")
	ErrRedirect             = errors.New("source redirect was refused")
	ErrHTTPStatus           = errors.New("source returned an unexpected HTTP status")
	ErrMediaType            = errors.New("source returned an unexpected media type")
	ErrContentEncoding      = errors.New("source returned an unsupported content encoding")
	ErrResponseHeaders      = errors.New("source returned unsupported response headers")
	ErrRequestTooLarge      = errors.New("upload-pack request exceeds its byte limit")
	ErrResponseTooLarge     = errors.New("source response exceeds its byte limit")
	ErrAdvertisement        = errors.New("source returned an unsupported Git advertisement")
	ErrUploadPackProtocol   = errors.New("source returned an unsupported upload-pack response")
	ErrConsumer             = errors.New("pack consumer failed")
	ErrConsumerStoppedEarly = errors.New("pack consumer returned before consuming the complete pack")
)

// Error reports a fetch stage and a stable error kind. Its text deliberately
// omits remote response bytes, credentials, and consumer-provided text.
type Error struct {
	Op         string
	Kind       error
	StatusCode int
	// Address, AddressRange and Consent describe a refused destination: the
	// resolved address, the special-purpose range it belongs to, and the
	// consent that would allow it (ConsentPrivateNetwork,
	// ConsentExceptionalDestination, or empty when nothing can).
	Address      string
	AddressRange string
	Consent      string
	// RedirectOrigin is the checked scheme and host a refused redirect
	// pointed to, when it is not an origin this source may follow.
	RedirectOrigin string
	cause          error
}

func (e *Error) Error() string {
	prefix := "import fetch"
	if e.Op != "" {
		prefix += " " + e.Op
	}
	switch {
	case e.StatusCode != 0:
		return fmt.Sprintf("%s: %v (HTTP %d)", prefix, e.Kind, e.StatusCode)
	case e.Address != "":
		return fmt.Sprintf("%s: %v (%s, %s)", prefix, e.Kind, e.Address, e.AddressRange)
	case e.RedirectOrigin != "":
		return fmt.Sprintf("%s: %v (to %s)", prefix, e.Kind, e.RedirectOrigin)
	}
	return fmt.Sprintf("%s: %v", prefix, e.Kind)
}

// Unwrap preserves stable classification and safe local causes such as
// context cancellation. Remote parser and consumer errors are reduced to
// stable sentinels before they are attached.
func (e *Error) Unwrap() []error {
	if e.cause == nil || e.cause == e.Kind {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.cause}
}

// BasicAuth holds HTTP Basic authentication in memory. The source URL may not
// contain user information.
type BasicAuth struct {
	Username string
	Password string
}

// Authentication selects at most one Authorization header form.
type Authentication struct {
	Basic       *BasicAuth
	BearerToken string
}

// Limits bound one complete fetch. Zero fields use DefaultLimits. Pack and
// total-body limits count HTTP entity bytes after transfer decoding, not TLS,
// TCP, or chunk-framing bytes.
type Limits struct {
	Advertisement         importgit.Limits
	MaxRequestBytes       int64
	MaxPackBytes          int64
	MaxTotalBodyBytes     int64
	MaxHeaderBytes        int64
	MaxURLBytes           int
	MaxCredentialBytes    int
	MaxCABundleBytes      int
	TotalTimeout          time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

// DefaultLimits returns conservative finite bounds for a full snapshot fetch.
func DefaultLimits() Limits {
	advertisement := importgit.DefaultLimits()
	const maxPack = int64(16 << 30)
	return Limits{
		Advertisement:         advertisement,
		MaxRequestBytes:       defaultMaxRequestBytes,
		MaxPackBytes:          maxPack,
		MaxTotalBodyBytes:     maxPack + sidebandOverhead(maxPack) + 2*advertisement.MaxTotalBytes + 64,
		MaxHeaderBytes:        64 << 10,
		MaxURLBytes:           8 << 10,
		MaxCredentialBytes:    16 << 10,
		MaxCABundleBytes:      1 << 20,
		TotalTimeout:          30 * time.Minute,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// Request describes one immutable source snapshot request. URL is the
// repository URL, without an info/refs suffix.
type Request struct {
	URL                 string
	Authentication      Authentication
	AllowPrivateNetwork bool
	// AllowPlainHTTP permits an http:// URL, and a redirect to one. The
	// source's code and credentials then travel unencrypted.
	AllowPlainHTTP bool
	// AllowReservedAddresses permits usable special-purpose unicast
	// addresses, such as documentation or benchmarking ranges, for this
	// source. Private addresses still need AllowPrivateNetwork.
	AllowReservedAddresses bool
	// Redirects is RedirectRefuse (also when empty), RedirectSameOrigin or
	// RedirectApproved. ApprovedRedirectOrigin is the one other origin that
	// RedirectApproved follows. Credentials and RootCAPEM stay with the
	// source origin.
	Redirects              string
	ApprovedRedirectOrigin string
	// RootCAPEM optionally adds source-specific trust anchors to the system
	// roots. Normal certificate chain and original-hostname checks still run.
	RootCAPEM []byte
	// ExtraRefPrefixes are ref namespaces, such as refs/notes/, that a
	// protocol v2 server is asked for beside HEAD, branches and tags.
	ExtraRefPrefixes []string
	Limits           Limits
}

// PackConsumer synchronously consumes one validated raw PACK stream. It must
// honor ctx, consume reader to EOF, and return before ctx expires. The same
// fetch deadline covers HTTPS and the callback.
type PackConsumer func(ctx context.Context, advertisement *importgit.Advertisement, reader io.Reader) error

// Result records the validated advertisement and successful entity-byte
// counts. Empty advertisements have PackBytes zero and do not invoke a
// PackConsumer.
type Result struct {
	Advertisement *importgit.Advertisement
	PackBytes     int64
	HTTPBodyBytes int64
}

package importgit

import (
	"errors"
	"fmt"
)

// Typed failures a caller can branch on with errors.Is. Every parse failure
// wraps exactly one of these in a *ParseError, so a caller can distinguish a
// refusal it may retry or report from one that means the response was not a
// supported smart advertisement at all.
var (
	// ErrInvalidLimits reports limits this parser will not run with, such as a
	// negative bound or a packet limit above the protocol maximum. No parsing
	// is attempted.
	ErrInvalidLimits = errors.New("invalid advertisement parse limits")
	// ErrInvalidOptions reports a caller mistake other than a limit, such as a
	// missing response body or an unsupported service name.
	ErrInvalidOptions = errors.New("invalid advertisement parse options")
	// ErrLimitExceeded reports that the response is larger than the effective
	// limits allow. It is a local bound, not a protocol violation.
	ErrLimitExceeded = errors.New("advertisement exceeds a parse limit")
	// ErrMalformedPacket reports pkt-line framing this parser cannot trust,
	// including a non-hexadecimal length, an empty data packet, and a length
	// beyond the protocol maximum.
	ErrMalformedPacket = errors.New("malformed advertisement packet framing")
	// ErrTruncated reports a response that ended inside a packet or before the
	// terminating flush packet.
	ErrTruncated = errors.New("advertisement ended before the terminating flush packet")
	// ErrTrailingContent reports bytes after the terminating flush packet.
	ErrTrailingContent = errors.New("advertisement has content after the terminating flush packet")
	// ErrMalformedService reports a missing or unexpected "# service=" packet
	// or its flush packet.
	ErrMalformedService = errors.New("malformed advertisement service announcement")
	// ErrUnsupportedVersion reports a protocol version this parser does not
	// implement, including protocol v2 and the v2-only delimiter and
	// response-end packets. It never means the repository is empty.
	ErrUnsupportedVersion = errors.New("unsupported Git protocol version in advertisement")
	// ErrInvalidRecord reports a ref record whose structure is not
	// "obj-id SP refname", or a peeled record with no preceding ref.
	ErrInvalidRecord = errors.New("invalid advertisement ref record")
	// ErrInvalidObjectID reports an object ID that is not hexadecimal, or a
	// zero object ID used as an ordinary ref tip. Uppercase and lowercase hex
	// are canonicalized, so case alone is never a failure; a width that
	// contradicts the advertised object format is reported as ErrObjectFormat.
	ErrInvalidObjectID = errors.New("invalid advertisement object ID")
	// ErrInvalidName reports a ref name that is not "HEAD" or a valid
	// "refs/"-rooted name, or that this parser refuses to represent.
	ErrInvalidName = errors.New("invalid advertisement ref name")
	// ErrConflictingRefs reports duplicate or contradictory records, such as a
	// repeated name, a repeated peeled value, or contradicting symrefs.
	ErrConflictingRefs = errors.New("conflicting advertisement ref records")
	// ErrInvalidCapability reports a capability name or value outside the
	// documented grammar. An unknown but well-formed capability is accepted.
	ErrInvalidCapability = errors.New("invalid advertisement capability")
	// ErrObjectFormat reports an unsupported hash algorithm, or object IDs
	// whose width contradicts the advertised object-format.
	ErrObjectFormat = errors.New("unsupported or inconsistent advertisement object format")
	// ErrRemoteError reports an "ERR <text>" packet sent by the server in
	// place of an advertisement. The response was well framed; the server
	// refused the request.
	ErrRemoteError = errors.New("the Git server returned an error packet")
	// ErrIncompleteHistory reports a shallow advertisement. The source history
	// is truncated, so this parser refuses it instead of letting a caller
	// describe it as a complete copy.
	ErrIncompleteHistory = errors.New("advertisement declares shallow, incomplete history")
)

// ParseError locates one advertisement failure. Offset is the byte offset of
// the packet that failed, counted from the first byte of the response body.
type ParseError struct {
	// Offset is the start of the offending packet, or the bytes consumed when
	// no packet boundary applies.
	Offset int64
	// Detail explains the failure without repeating advertised bytes that the
	// caller has not validated.
	Detail string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%v (at byte %d)", e.Err, e.Offset)
	}
	return fmt.Sprintf("%v: %s (at byte %d)", e.Err, e.Detail, e.Offset)
}

func (e *ParseError) Unwrap() error { return e.Err }

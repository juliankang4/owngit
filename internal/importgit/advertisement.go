// Package importgit parses a Git smart HTTP reference advertisement.
//
// It implements only the protocol v0 and v1 upload-pack advertisement that a
// server returns from "$GIT_URL/info/refs?service=git-upload-pack". It is a
// pure parser: it opens no connection, performs no authentication, reads no
// repository, and publishes nothing. The caller owns the HTTP request, the
// status and content-type checks, deadlines, and the decision of what to do
// with the parsed facts.
//
// The parser is deliberately strict. Protocol v2, the v2-only delimiter and
// response-end packets, shallow advertisements, malformed framing, and
// unexpected trailing bytes are reported as distinct typed failures, because
// an importer must not read any of them as "the source repository is empty".
// A genuinely empty repository has its own representation on the wire, the
// zero-id "capabilities^{}" record, and is reported as Advertisement.Empty.
//
// Nothing here decides that an advertised ref may be imported or published.
// The result preserves advertised names, peeled tag identities and symref
// statements exactly as received so a later caller can select refs; it applies
// no defaults and never invents a HEAD the server did not state. Object IDs
// are the one exception: the protocol requires receivers to treat them
// case-insensitively, so they are lowercased to a single canonical form. That
// changes representation, never identity.
//
// Protocol references: gitprotocol-common(5) for pkt-line framing,
// gitprotocol-http(5) for the smart reply, gitprotocol-pack(5) for the
// advertised-refs grammar, and gitprotocol-capabilities(5) for object-format
// and symref.
package importgit

import (
	"fmt"
	"io"
	"strings"
)

// Supported repository hash algorithms, as advertised by the object-format
// capability. gitprotocol-capabilities(5) states that SHA-1 is assumed when
// the capability is absent.
const (
	FormatSHA1   = "sha1"
	FormatSHA256 = "sha256"
)

// DefaultService is the only service this package parses.
const DefaultService = "git-upload-pack"

// capabilitiesSentinel is the ref name a server uses to carry capabilities
// when it has no refs to advertise. See the empty_list rule in
// gitprotocol-http(5).
const capabilitiesSentinel = "capabilities^{}"

// peeledSuffix marks the peeled value of the preceding ref.
const peeledSuffix = "^{}"

// shallowPrefix introduces a shallow boundary record.
const shallowPrefix = "shallow"

// Limits bound one parse. Every bound is enforced before the matching
// allocation, so a hostile response cannot force unbounded memory use.
type Limits struct {
	// MaxPacketBytes bounds one pkt-line including its four length bytes. It
	// may not exceed the 65520-byte protocol maximum.
	MaxPacketBytes int
	// MaxTotalBytes bounds the whole response body.
	MaxTotalBytes int64
	// MaxRefRecords bounds the number of data packets in the ref list. Every
	// record is counted once, including peeled and shallow records. The
	// optional version packet is not a ref-list record and is not counted.
	MaxRefRecords int
	// MaxNameBytes bounds one advertised ref name, including any "^{}" suffix.
	MaxNameBytes int
	// MaxCapabilities bounds the number of advertised capabilities.
	MaxCapabilities int
	// MaxCapabilityBytes bounds one capability token including its value.
	MaxCapabilityBytes int
}

// DefaultLimits are sized for an ordinary repository advertisement rather than
// for the largest response the protocol permits.
func DefaultLimits() Limits {
	return Limits{
		MaxPacketBytes:     protocolMaxPacket,
		MaxTotalBytes:      16 << 20,
		MaxRefRecords:      50000,
		MaxNameBytes:       1024,
		MaxCapabilities:    256,
		MaxCapabilityBytes: 1024,
	}
}

// effective fills unset limits with defaults and refuses values this parser
// will not run with.
func (l Limits) effective() (Limits, error) {
	defaults := DefaultLimits()
	limits := l
	for name, value := range map[string]int{
		"MaxPacketBytes": l.MaxPacketBytes, "MaxRefRecords": l.MaxRefRecords,
		"MaxNameBytes": l.MaxNameBytes, "MaxCapabilities": l.MaxCapabilities,
		"MaxCapabilityBytes": l.MaxCapabilityBytes,
	} {
		if value < 0 {
			return Limits{}, fmt.Errorf("%w: %s is negative", ErrInvalidLimits, name)
		}
	}
	if l.MaxTotalBytes < 0 {
		return Limits{}, fmt.Errorf("%w: MaxTotalBytes is negative", ErrInvalidLimits)
	}
	if limits.MaxPacketBytes == 0 {
		limits.MaxPacketBytes = defaults.MaxPacketBytes
	}
	if limits.MaxPacketBytes > protocolMaxPacket {
		return Limits{}, fmt.Errorf("%w: MaxPacketBytes %d exceeds the %d-byte protocol maximum",
			ErrInvalidLimits, limits.MaxPacketBytes, protocolMaxPacket)
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxRefRecords == 0 {
		limits.MaxRefRecords = defaults.MaxRefRecords
	}
	if limits.MaxNameBytes == 0 {
		limits.MaxNameBytes = defaults.MaxNameBytes
	}
	if limits.MaxCapabilities == 0 {
		limits.MaxCapabilities = defaults.MaxCapabilities
	}
	if limits.MaxCapabilityBytes == 0 {
		limits.MaxCapabilityBytes = defaults.MaxCapabilityBytes
	}
	return limits, nil
}

// Options configure one parse.
type Options struct {
	// Limits bound the parse. Zero fields take their default value.
	Limits Limits
	// Service is the service name the caller requested. The advertised name
	// must match it exactly. An empty value means DefaultService.
	Service string
}

// Ref is one advertised reference. Names are preserved exactly as received;
// object IDs are canonicalized to lowercase.
type Ref struct {
	// Name is the advertised ref name, either "HEAD" or a "refs/"-rooted name.
	// Its case is exactly what the server advertised.
	Name string
	// OID is the advertised object ID, lowercased. The protocol requires
	// receivers to treat object IDs case-insensitively, so an uppercase
	// advertisement is accepted and canonicalized rather than refused.
	OID string
	// PeeledOID is the object the ref resolves to after peeling, lowercased,
	// set only when the server advertised a "<name>^{}" record. It is empty for
	// a ref that was not peeled, which is not evidence that the ref is not a
	// tag.
	PeeledOID string
	// SymrefTarget is the target named by a matching symref capability. It is
	// empty when the server made no symref statement about this ref.
	SymrefTarget string
}

// Peeled reports whether the server advertised a peeled value for this ref.
func (r Ref) Peeled() bool { return r.PeeledOID != "" }

// Symref is one "symref=<name>:<target>" statement. The target is recorded as
// advertised; the server is not required to advertise the target ref, and an
// unborn branch legitimately has no ref record.
type Symref struct {
	Name   string
	Target string
}

// Head collects what the advertisement actually said about HEAD. Nothing here
// is inferred: a server that advertises no HEAD ref and no HEAD symref leaves
// every field at its zero value.
type Head struct {
	// Advertised reports that a ref named HEAD appeared in the ref list.
	Advertised bool
	// OID is HEAD's advertised object ID, lowercased, set only when Advertised
	// is true.
	OID string
	// PeeledOID is HEAD's peeled value, lowercased, when the server advertised
	// one.
	PeeledOID string
	// SymrefTarget is the ref HEAD points at, taken from the symref
	// capability. It is empty when the server did not say, which happens for a
	// detached HEAD and for servers that omit the capability. A stated target
	// without an advertised HEAD object does not prove the branch is unborn:
	// server visibility rules may also omit refs.
	SymrefTarget string
}

// Advertisement is the complete set of facts one advertisement stated.
type Advertisement struct {
	// Service is the advertised service name, always the requested one.
	Service string
	// ProtocolVersion is 0 when no version packet was sent and 1 when the
	// server sent "version 1". Protocol v2 is never reported here; it is
	// refused with ErrUnsupportedVersion.
	ProtocolVersion int
	// ObjectFormat is FormatSHA1 or FormatSHA256. It is FormatSHA1 when the
	// server advertised no object-format capability, as the capability
	// documentation requires.
	ObjectFormat string
	// ObjectFormatAdvertised reports whether the server stated the format
	// rather than this parser applying the documented SHA-1 default.
	ObjectFormatAdvertised bool
	// Capabilities are the first ref's capability tokens in advertised order,
	// including values such as "agent=git/2.54.0". Unknown but well-formed
	// tokens are kept rather than refused.
	Capabilities []string
	// Refs are the advertised refs in advertised order, excluding the
	// capabilities sentinel. Peeled records are folded into the ref they
	// belong to instead of appearing separately.
	Refs []Ref
	// Head is what the server stated about HEAD.
	Head Head
	// Symrefs are the symref capability statements in advertised order.
	Symrefs []Symref
	// Empty reports a valid zero-id "capabilities^{}" advertisement with no
	// advertised refs. Hidden refs may still exist in the repository. It is
	// never set for a malformed, unsupported or truncated response.
	Empty bool
	// EffectiveLimits are the limits this parse actually ran with.
	EffectiveLimits Limits
	// TotalBytes is the number of response bytes consumed.
	TotalBytes int64
}

// Parse reads one smart HTTP upload-pack advertisement from source.
//
// The caller must pass the response body only, after checking the HTTP status
// and content type. Parse reads to the end of the body: it requires the
// terminating flush packet and refuses any byte after it, so a response that
// appends a second message is rejected rather than half-accepted.
//
// Parse returns a *ParseError wrapping one of this package's typed errors for
// every protocol failure. A nil error means every advertised record was
// validated and preserved.
func Parse(source io.Reader, options Options) (*Advertisement, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: no response body was supplied", ErrInvalidOptions)
	}
	limits, err := options.Limits.effective()
	if err != nil {
		return nil, err
	}
	service := options.Service
	if service == "" {
		service = DefaultService
	}
	if service != DefaultService {
		return nil, fmt.Errorf("%w: only %s advertisements are parsed", ErrInvalidOptions, DefaultService)
	}
	state := &parseState{
		reader: &packetReader{source: source, maxPacket: limits.MaxPacketBytes, maxTotal: limits.MaxTotalBytes},
		limits: limits,
		result: &Advertisement{Service: service, ObjectFormat: FormatSHA1, EffectiveLimits: limits},
		names:  map[string]int{},
	}
	if err := state.run(); err != nil {
		return nil, err
	}
	state.result.TotalBytes = state.reader.consumed
	return state.result, nil
}

// parseState holds one in-progress parse.
type parseState struct {
	reader *packetReader
	limits Limits
	result *Advertisement
	// names maps an advertised ref name to its index in result.Refs, so
	// duplicates and misplaced peeled records are detected without rescanning.
	names map[string]int
	// oidWidth is the hexadecimal length required by the object format.
	oidWidth int
	// records counts data packets consumed in the ref list.
	records int
}

func (s *parseState) fail(offset int64, err error, detail string) error {
	return &ParseError{Offset: offset, Err: err, Detail: detail}
}

// run parses the complete response.
func (s *parseState) run() error {
	if err := s.readServiceAnnouncement(); err != nil {
		return err
	}
	if err := s.readRefList(); err != nil {
		return err
	}
	return s.requireEndOfResponse()
}

// readServiceAnnouncement consumes the "# service=<name>" packet and the flush
// packet that follows it, as required by gitprotocol-http(5).
func (s *parseState) readServiceAnnouncement() error {
	kind, payload, err := s.reader.next()
	offset := s.reader.offset
	switch {
	case err == io.EOF:
		return s.fail(offset, ErrTruncated, "the response body was empty")
	case err != nil:
		return err
	}
	if kind != packetData {
		return s.unsupportedControl(offset, kind, "before the service announcement")
	}
	line := string(trimLF(payload))
	if err := s.checkErrorPacket(offset, line); err != nil {
		return err
	}
	if !strings.HasPrefix(line, "# service=") {
		// A v2 server answers the same request with "version 2" as its first
		// packet, so naming the version is more useful than calling the
		// response malformed. Version 1 is different: smart_reply in
		// gitprotocol-http(5) puts the service announcement before any version
		// packet, so a leading "version 1" is a broken HTTP response from a
		// version this parser supports, not an unsupported version.
		if version, ok := parseVersionLine(line); ok {
			if version > 1 {
				return s.fail(offset, ErrUnsupportedVersion,
					fmt.Sprintf("the response begins with a protocol version %d capability advertisement", version))
			}
			return s.fail(offset, ErrMalformedService,
				fmt.Sprintf("a protocol version %d packet precedes the service announcement", version))
		}
		return s.fail(offset, ErrMalformedService, "the first packet is not a service announcement")
	}
	if advertised := strings.TrimPrefix(line, "# service="); advertised != s.result.Service {
		return s.fail(offset, ErrMalformedService,
			fmt.Sprintf("the advertised service is not the requested %s", s.result.Service))
	}
	kind, payload, err = s.reader.next()
	offset = s.reader.offset
	switch {
	case err == io.EOF:
		return s.fail(offset, ErrTruncated, "the service announcement has no flush packet")
	case err != nil:
		return err
	}
	switch kind {
	case packetFlush:
		return nil
	case packetData:
		if err := s.checkErrorPacket(offset, string(trimLF(payload))); err != nil {
			return err
		}
		return s.fail(offset, ErrMalformedService, "the service announcement is not followed by a flush packet")
	default:
		return s.unsupportedControl(offset, kind, "after the service announcement")
	}
}

// readRefList consumes the optional version packet, every ref record, and the
// terminating flush packet.
func (s *parseState) readRefList() error {
	s.oidWidth = oidWidth(s.result.ObjectFormat)
	first := true
	sawVersion := false
	sawSentinel := false
	for {
		kind, payload, err := s.reader.next()
		offset := s.reader.offset
		switch {
		case err == io.EOF:
			return s.fail(offset, ErrTruncated, "the ref list has no terminating flush packet")
		case err != nil:
			return err
		}
		switch kind {
		case packetFlush:
			if first {
				// The grammar has no advertisement without a ref list: an
				// empty repository still sends the capabilities sentinel.
				// Reporting this as emptiness would hide a broken server.
				return s.fail(offset, ErrInvalidRecord,
					"the ref list is absent; an empty repository still advertises the capabilities record")
			}
			return nil
		case packetData:
		default:
			return s.unsupportedControl(offset, kind, "inside the ref list")
		}
		line := string(trimLF(payload))
		if err := s.checkErrorPacket(offset, line); err != nil {
			return err
		}
		if version, ok := parseVersionLine(line); ok {
			// An unsupported version is unsupported wherever it appears; a v2
			// capability advertisement in a v1 response body is the case Main's
			// matrix exercised.
			if version > 1 {
				return s.fail(offset, ErrUnsupportedVersion,
					fmt.Sprintf("the server announced protocol version %d", version))
			}
			// A supported version in the wrong place is broken framing rather
			// than an unsupported version. gitprotocol-pack(5) writes the
			// packet as *1("version 1"), before the ref list.
			if !first || sawVersion {
				return s.fail(offset, ErrInvalidRecord,
					fmt.Sprintf("a protocol version %d packet appears inside the ref list", version))
			}
			if version != 1 {
				return s.fail(offset, ErrInvalidRecord,
					fmt.Sprintf("the server announced protocol version %d, which no server sends", version))
			}
			s.result.ProtocolVersion = 1
			sawVersion = true
			continue
		}
		// Every ref-list data record is counted exactly once, before it is
		// dispatched, so the documented bound covers ref, peeled and shallow
		// records alike. The version packet is not a ref-list record and is
		// handled above.
		s.records++
		if s.records > s.limits.MaxRefRecords {
			return s.fail(offset, ErrLimitExceeded,
				fmt.Sprintf("the ref list exceeds the %d-record limit", s.limits.MaxRefRecords))
		}
		if line == shallowPrefix || strings.HasPrefix(line, shallowPrefix+" ") {
			return s.readShallow(offset, line, first)
		}
		if first {
			first = false
			sentinel, err := s.readFirstRecord(offset, line)
			if err != nil {
				return err
			}
			sawSentinel = sentinel
			continue
		}
		if sawSentinel {
			return s.fail(offset, ErrConflictingRefs,
				"the capabilities record states that no refs exist, but another record follows")
		}
		if err := s.readRecord(offset, line); err != nil {
			return err
		}
	}
}

// readShallow validates one shallow boundary record before deciding what it
// proves. gitprotocol-pack(5) defines it as PKT-LINE("shallow" SP obj-id) and
// places *shallow after (no-refs / list-of-refs), so it may follow either the
// zero-OID sentinel or an ordinary ref list.
//
// The distinction matters: ErrIncompleteHistory asserts that the source really
// does have truncated history. A record that only looks shallow must not carry
// that assertion, so a malformed one is reported as an invalid record instead.
func (s *parseState) readShallow(offset int64, line string, first bool) error {
	oid, ok := strings.CutPrefix(line, shallowPrefix+" ")
	if !ok {
		return s.fail(offset, ErrInvalidRecord, "a shallow record carries no object ID")
	}
	if first {
		// The grammar is (no-refs / list-of-refs) *shallow, and the capability
		// list lives on whichever record comes first. A leading shallow record
		// means the capabilities were never advertised.
		return s.fail(offset, ErrInvalidRecord, "a shallow record precedes the ref list")
	}
	// A shallow record after the zero-OID sentinel is valid: the grammar
	// permits *shallow after no-refs as well as after list-of-refs.
	if len(oid) != s.oidWidth {
		return s.fail(offset, ErrObjectFormat,
			fmt.Sprintf("a shallow record has a %d-character object ID, but %s requires %d",
				len(oid), s.result.ObjectFormat, s.oidWidth))
	}
	if !isHexOID(oid) {
		return s.fail(offset, ErrInvalidObjectID, "a shallow object ID is not hexadecimal")
	}
	// Canonicalized before the zero test so an uppercase form cannot slip past
	// a comparison. Zero has no uppercase form, but the ordering keeps every
	// object-ID check working on one representation.
	oid = canonicalOID(oid)
	if isZeroOID(oid) {
		return s.fail(offset, ErrInvalidObjectID, "a shallow record has the zero object ID")
	}
	// The record is a valid shallow boundary, so the source history really is
	// incomplete. Copying it would not reproduce the source.
	return s.fail(offset, ErrIncompleteHistory,
		"the advertisement declares a shallow boundary, so the source history is incomplete")
}

// readFirstRecord parses the ref record that carries the capability list. Its
// NUL separator is required: without it the capabilities sentinel cannot be
// told apart from a ref literally named "capabilities^{}".
func (s *parseState) readFirstRecord(offset int64, line string) (bool, error) {
	separator := strings.IndexByte(line, 0)
	if separator < 0 {
		return false, s.fail(offset, ErrInvalidRecord,
			"the first ref record carries no NUL capability separator")
	}
	if err := s.readCapabilities(offset, line[separator+1:]); err != nil {
		return false, err
	}
	// The object format is known only after the capabilities are read, so ref
	// widths are validated against the advertised algorithm and not a guess.
	s.oidWidth = oidWidth(s.result.ObjectFormat)
	oid, name, err := s.splitRecord(offset, line[:separator])
	if err != nil {
		return false, err
	}
	if name == capabilitiesSentinel {
		if !isZeroOID(oid) {
			return false, s.fail(offset, ErrInvalidRecord,
				"the capabilities record does not carry the zero object ID")
		}
		s.result.Empty = true
		return true, nil
	}
	// Any other "^{}" name in first position is a peeled record with no ref to
	// attach to. Saying so is more accurate than calling "^" a forbidden
	// character in the name.
	if strings.HasSuffix(name, peeledSuffix) {
		return false, s.fail(offset, ErrInvalidRecord, "a peeled record precedes every ref")
	}
	if err := s.addRef(offset, oid, name); err != nil {
		return false, err
	}
	return false, nil
}

// readRecord parses one ordinary ref record or peeled record.
func (s *parseState) readRecord(offset int64, line string) error {
	if strings.IndexByte(line, 0) >= 0 {
		return s.fail(offset, ErrInvalidRecord,
			"only the first ref record may carry a capability list")
	}
	oid, name, err := s.splitRecord(offset, line)
	if err != nil {
		return err
	}
	if base, ok := strings.CutSuffix(name, peeledSuffix); ok {
		return s.addPeeled(offset, oid, base)
	}
	return s.addRef(offset, oid, name)
}

// splitRecord separates "obj-id SP refname" and validates both halves.
func (s *parseState) splitRecord(offset int64, line string) (string, string, error) {
	space := strings.IndexByte(line, ' ')
	if space < 0 {
		return "", "", s.fail(offset, ErrInvalidRecord, "the record is not an object ID followed by a name")
	}
	oid, name := line[:space], line[space+1:]
	if len(oid) != s.oidWidth {
		return "", "", s.fail(offset, ErrObjectFormat,
			fmt.Sprintf("the record has a %d-character object ID, but %s requires %d",
				len(oid), s.result.ObjectFormat, s.oidWidth))
	}
	if !isHexOID(oid) {
		return "", "", s.fail(offset, ErrInvalidObjectID, "the object ID is not hexadecimal")
	}
	// Every caller receives the canonical form, so stored IDs and the
	// comparisons built on them never depend on the advertised case. The ref
	// name is returned exactly as advertised.
	oid = canonicalOID(oid)
	if name == "" {
		return "", "", s.fail(offset, ErrInvalidName, "the record has an empty name")
	}
	if len(name) > s.limits.MaxNameBytes {
		return "", "", s.fail(offset, ErrLimitExceeded,
			fmt.Sprintf("a ref name exceeds the %d-byte limit", s.limits.MaxNameBytes))
	}
	return oid, name, nil
}

// addRef records one ref tip.
// A later "capabilities^{}" record cannot reach here: readRecord strips the
// "^{}" suffix first, so it is refused as a peeled record that does not follow
// its own ref.
func (s *parseState) addRef(offset int64, oid, name string) error {
	if isZeroOID(oid) {
		// The zero ID means "no object". Keeping it as a ref would let a later
		// caller create or compare against an object that cannot exist.
		return s.fail(offset, ErrInvalidObjectID, "a ref other than the capabilities record has the zero object ID")
	}
	if err := validateRefName(name); err != nil {
		return s.fail(offset, ErrInvalidName, err.Error())
	}
	if _, duplicate := s.names[name]; duplicate {
		return s.fail(offset, ErrConflictingRefs, "the advertisement repeats a ref name")
	}
	s.names[name] = len(s.result.Refs)
	s.result.Refs = append(s.result.Refs, Ref{Name: name, OID: oid})
	if name == "HEAD" {
		s.result.Head.Advertised = true
		s.result.Head.OID = oid
	}
	return nil
}

// addPeeled attaches a "<name>^{}" record to the ref it belongs to.
// gitprotocol-pack(5) requires a peeled value to follow its ref immediately.
func (s *parseState) addPeeled(offset int64, oid, base string) error {
	if len(s.result.Refs) == 0 {
		return s.fail(offset, ErrInvalidRecord, "a peeled record precedes every ref")
	}
	last := len(s.result.Refs) - 1
	if s.result.Refs[last].Name != base {
		return s.fail(offset, ErrInvalidRecord, "a peeled record does not follow its own ref")
	}
	if s.result.Refs[last].PeeledOID != "" {
		return s.fail(offset, ErrConflictingRefs, "a ref has more than one peeled record")
	}
	if isZeroOID(oid) {
		return s.fail(offset, ErrInvalidObjectID, "a peeled record has the zero object ID")
	}
	s.result.Refs[last].PeeledOID = oid
	if base == "HEAD" {
		s.result.Head.PeeledOID = oid
	}
	return nil
}

// readCapabilities validates the capability list and extracts object-format
// and symref. Unknown well-formed capabilities are preserved, not refused:
// gitprotocol-capabilities(5) lets servers advertise capabilities this parser
// has never heard of, and an unknown name alone is not evidence of protocol v2.
//
// The list is scanned one token at a time rather than split up front, so the
// capability limit is enforced before the parser allocates per-token storage
// for a hostile list.
func (s *parseState) readCapabilities(offset int64, list string) error {
	// cap-list is "capability *(SP capability)" in both gitprotocol-http(5)
	// and gitprotocol-pack(5), so at least one capability is required. An
	// empty list is a malformed record, including on the empty-repository
	// sentinel, and must not be reported as a successful empty advertisement.
	if list == "" {
		return s.fail(offset, ErrInvalidCapability,
			"the ref record carries an empty capability list, but at least one capability is required")
	}
	seenFormat := false
	count := 0
	for remaining := list; ; {
		token, rest, more := strings.Cut(remaining, " ")
		remaining = rest
		count++
		if count > s.limits.MaxCapabilities {
			return s.fail(offset, ErrLimitExceeded,
				fmt.Sprintf("the capability list exceeds the %d-capability limit", s.limits.MaxCapabilities))
		}
		if token == "" {
			return s.fail(offset, ErrInvalidCapability, "the capability list has an empty entry")
		}
		if len(token) > s.limits.MaxCapabilityBytes {
			return s.fail(offset, ErrLimitExceeded,
				fmt.Sprintf("a capability exceeds the %d-byte limit", s.limits.MaxCapabilityBytes))
		}
		key, value, hasValue := strings.Cut(token, "=")
		if !validCapabilityKey(key) {
			return s.fail(offset, ErrInvalidCapability, "a capability name is outside the documented grammar")
		}
		if hasValue && !validCapabilityValue(value) {
			return s.fail(offset, ErrInvalidCapability, "a capability value contains a space or a non-printable byte")
		}
		switch key {
		case "object-format":
			if !hasValue {
				return s.fail(offset, ErrObjectFormat, "the object-format capability has no value")
			}
			// The capability may repeat; the first value is the one used in
			// the ref advertisement, so later values must not change it.
			if !seenFormat {
				if value != FormatSHA1 && value != FormatSHA256 {
					return s.fail(offset, ErrObjectFormat, "the advertised hash algorithm is not supported")
				}
				s.result.ObjectFormat = value
				s.result.ObjectFormatAdvertised = true
				seenFormat = true
			}
		case "symref":
			name, target, ok := strings.Cut(value, ":")
			if !hasValue || !ok {
				return s.fail(offset, ErrInvalidCapability, "a symref capability is not <name>:<target>")
			}
			if err := validateRefName(name); err != nil {
				return s.fail(offset, ErrInvalidName, "symref source: "+err.Error())
			}
			if err := validateRefName(target); err != nil {
				return s.fail(offset, ErrInvalidName, "symref target: "+err.Error())
			}
			for _, existing := range s.result.Symrefs {
				if existing.Name != name {
					continue
				}
				if existing.Target != target {
					return s.fail(offset, ErrConflictingRefs, "a symref is advertised with two different targets")
				}
				return s.fail(offset, ErrConflictingRefs, "a symref capability is repeated")
			}
			s.result.Symrefs = append(s.result.Symrefs, Symref{Name: name, Target: target})
		}
		s.result.Capabilities = append(s.result.Capabilities, token)
		if !more {
			return nil
		}
	}
}

// applySymrefs copies symref statements onto the refs they describe. It runs
// after the whole ref list so a symref may name a ref advertised later, and it
// never creates a ref the server did not advertise.
//
// When both the symbolic ref and its target were advertised, they name the
// same object by definition, so differing object IDs are a contradiction the
// caller must not resolve by guessing. A target the server did not advertise
// is left alone: a hidden or unborn target is a legitimate statement about a
// ref this response does not carry.
func (s *parseState) applySymrefs(offset int64) error {
	for _, symref := range s.result.Symrefs {
		index, advertised := s.names[symref.Name]
		if advertised {
			s.result.Refs[index].SymrefTarget = symref.Target
			if target, ok := s.names[symref.Target]; ok &&
				s.result.Refs[target].OID != s.result.Refs[index].OID {
				return s.fail(offset, ErrConflictingRefs,
					"a symbolic ref and its advertised target point at different objects")
			}
		}
		if symref.Name == "HEAD" {
			s.result.Head.SymrefTarget = symref.Target
		}
	}
	return nil
}

// requireEndOfResponse refuses any byte after the terminating flush packet. A
// second message would mean the body was not the advertisement alone.
func (s *parseState) requireEndOfResponse() error {
	if err := s.applySymrefs(s.reader.consumed); err != nil {
		return err
	}
	var probe [1]byte
	// ReadFull loops over a reader that legitimately returns (0, nil), so a
	// short read is not mistaken for the end of the body.
	read, err := io.ReadFull(s.reader.source, probe[:])
	if read > 0 {
		return s.fail(s.reader.consumed, ErrTrailingContent, "the response continues past the terminating flush packet")
	}
	if err == nil || err == io.EOF {
		return nil
	}
	return err
}

// unsupportedControl reports a v2-only control packet where v0 or v1 framing
// was expected.
func (s *parseState) unsupportedControl(offset int64, kind packetKind, where string) error {
	switch kind {
	case packetDelimiter, packetResponseEnd:
		return s.fail(offset, ErrUnsupportedVersion,
			fmt.Sprintf("a %s appeared %s; it exists only in protocol v2", kind, where))
	case packetFlush:
		return s.fail(offset, ErrMalformedService, "an unexpected flush packet appeared "+where)
	default:
		return s.fail(offset, ErrMalformedPacket, "an unexpected "+kind.String()+" appeared "+where)
	}
}

// checkErrorPacket turns an "ERR <text>" packet into a typed failure.
// gitprotocol-pack(5) allows one wherever a pkt-line is expected.
func (s *parseState) checkErrorPacket(offset int64, line string) error {
	text, ok := strings.CutPrefix(line, "ERR ")
	if !ok {
		return nil
	}
	return s.fail(offset, ErrRemoteError, "the server reported: "+sanitizeRemoteText(text))
}

// parseVersionLine recognizes the "version <n>" packet of both protocol v1 and
// the protocol v2 capability advertisement.
func parseVersionLine(line string) (int, bool) {
	digits, ok := strings.CutPrefix(line, "version ")
	if !ok || digits == "" || len(digits) > 3 {
		return 0, false
	}
	version := 0
	for index := 0; index < len(digits); index++ {
		if digits[index] < '0' || digits[index] > '9' {
			return 0, false
		}
		version = version*10 + int(digits[index]-'0')
	}
	return version, true
}

func oidWidth(format string) int {
	if format == FormatSHA256 {
		return 64
	}
	return 40
}

func isZeroOID(oid string) bool {
	return strings.Count(oid, "0") == len(oid) && len(oid) > 0
}

// isHexOID reports whether value is hexadecimal in either case.
//
// gitprotocol-pack(5) says both peers "MUST use lowercase for obj-id" and both
// "MUST treat obj-id as case-insensitive". Git's own receiver follows the
// second rule: hex-ll.c maps A-F and a-f to the same values, so an uppercase
// advertisement is accepted upstream. Refusing it here would reject a source
// real Git reads, so the case is normalized instead, by canonicalOID.
//
// This is specific to object IDs. The pkt-len header keeps its lowercase rule,
// which gitprotocol-http(5) states as the regex "^[0-9a-f]{4}#".
func isHexOID(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		case character >= 'A' && character <= 'F':
		default:
			return false
		}
	}
	return true
}

// canonicalOID lowercases a validated object ID so every stored value and
// every comparison uses the one form the protocol prefers. Only the object ID
// is canonicalized; ref names keep the exact case the server advertised.
func canonicalOID(oid string) string { return strings.ToLower(oid) }

// validCapabilityKey applies the capability rule of gitprotocol-capabilities(5):
// lowercase letters, digits, "-" and "_".
func validCapabilityKey(key string) bool {
	if key == "" {
		return false
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_':
		default:
			return false
		}
	}
	return true
}

// validCapabilityValue accepts the printable ASCII range the agent capability
// documents, which also covers object-format and symref values.
func validCapabilityValue(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '!' || value[index] > '~' {
			return false
		}
	}
	return true
}

// sanitizeRemoteText makes a server-supplied message safe to place in an error
// string. The text is remote input, so control bytes are replaced and the
// length is bounded rather than trusted.
func sanitizeRemoteText(text string) string {
	const maximum = 200
	truncated := false
	if len(text) > maximum {
		text, truncated = text[:maximum], true
	}
	cleaned := make([]byte, 0, len(text))
	for index := 0; index < len(text); index++ {
		character := text[index]
		if character < 0x20 || character == 0x7f {
			character = '?'
		}
		cleaned = append(cleaned, character)
	}
	if truncated {
		cleaned = append(cleaned, "..."...)
	}
	return string(cleaned)
}

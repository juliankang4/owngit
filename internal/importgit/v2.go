package importgit

import (
	"fmt"
	"io"
	"strings"
)

// LsRefsPrefixes returns the ref prefixes an importer asks a protocol v2
// server for: HEAD, branches, tags and the source's extra namespaces, such
// as refs/notes/, the refs an import can use. Refs in other namespaces, such
// as pull request refs, are then never listed, so they neither count toward
// MaxRefRecords nor enter a fetch. An extra namespace must be a ref name
// followed by a slash.
func LsRefsPrefixes(extra []string) ([]string, error) {
	prefixes := []string{"HEAD", "refs/heads/", "refs/tags/"}
	for _, prefix := range extra {
		name, isNamespace := strings.CutSuffix(prefix, "/")
		if !isNamespace || name == "HEAD" || validateRefName(name) != nil {
			return nil, fmt.Errorf("%w: extra ref namespace %q is not a ref name followed by a slash", ErrInvalidOptions, prefix)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// hasRequestedPrefix reports whether name is one of prefixes or, for a
// prefix that ends with a slash, starts with it.
func hasRequestedPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if name == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(name, prefix)) {
			return true
		}
	}
	return false
}

// readV2Capabilities reads a protocol v2 capability advertisement after its
// "version 2" packet, up to its flush packet. Its lines are bounded like the
// v0 capability list: MaxCapabilities lines of at most MaxCapabilityBytes.
// The server must offer the ls-refs and fetch commands.
func (s *parseState) readV2Capabilities() error {
	s.result.ProtocolVersion = 2
	seen := map[string]bool{}
	for {
		kind, payload, err := s.reader.next()
		offset := s.reader.offset
		switch {
		case err == io.EOF:
			return s.fail(offset, ErrTruncated, "the capability advertisement has no terminating flush packet")
		case err != nil:
			return err
		}
		if kind == packetFlush {
			break
		}
		if kind != packetData {
			return s.fail(offset, ErrMalformedPacket, "an unexpected "+kind.String()+" appeared in the capability advertisement")
		}
		line := string(trimLF(payload))
		if err := s.checkErrorPacket(offset, line); err != nil {
			return err
		}
		if len(s.result.Capabilities) >= s.limits.MaxCapabilities {
			return s.fail(offset, ErrLimitExceeded,
				fmt.Sprintf("the capability list exceeds the %d-capability limit", s.limits.MaxCapabilities))
		}
		if len(line) > s.limits.MaxCapabilityBytes {
			return s.fail(offset, ErrLimitExceeded,
				fmt.Sprintf("a capability exceeds the %d-byte limit", s.limits.MaxCapabilityBytes))
		}
		key, value, hasValue := strings.Cut(line, "=")
		if !validCapabilityKey(key) {
			return s.fail(offset, ErrInvalidCapability, "a capability name is outside the documented grammar")
		}
		// A v2 value may hold spaces, such as "fetch=shallow filter".
		if hasValue && (value == "" || strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r > '~' })) {
			return s.fail(offset, ErrInvalidCapability, "a capability value is empty or holds a non-printable byte")
		}
		if seen[key] {
			return s.fail(offset, ErrInvalidCapability, "a capability is repeated")
		}
		seen[key] = true
		if key == "object-format" {
			if value != FormatSHA1 && value != FormatSHA256 {
				return s.fail(offset, ErrObjectFormat, "the advertised hash algorithm is not supported")
			}
			s.result.ObjectFormat = value
			s.result.ObjectFormatAdvertised = true
		}
		s.result.Capabilities = append(s.result.Capabilities, line)
	}
	if !seen["ls-refs"] || !seen["fetch"] {
		return s.fail(s.reader.consumed, ErrInvalidCapability, "the protocol v2 server offers no ls-refs or fetch command")
	}
	return nil
}

// ParseLsRefs reads the answer to an ls-refs command that asked a protocol
// v2 server for prefixes (from LsRefsPrefixes) with the symrefs and peel
// arguments, after capabilities, the Advertisement Parse returned for that
// server. The result describes the source as Parse does for a v0 server: the
// refs, HEAD, their peeled values and symrefs, validated by the same rules
// and bounded by the same limits. A ref outside prefixes, which a server may
// list, is validated and counted, then left out. No refs at all is Empty.
func ParseLsRefs(source io.Reader, capabilities *Advertisement, prefixes []string, options Options) (*Advertisement, error) {
	if source == nil || capabilities == nil || capabilities.ProtocolVersion != 2 || len(prefixes) == 0 {
		return nil, fmt.Errorf("%w: ls-refs needs a protocol v2 capability advertisement, the requested prefixes and a response body", ErrInvalidOptions)
	}
	limits, err := options.Limits.Effective()
	if err != nil {
		return nil, err
	}
	s := &parseState{
		reader: &packetReader{source: source, maxPacket: limits.MaxPacketBytes, maxTotal: limits.MaxTotalBytes},
		limits: limits,
		result: &Advertisement{
			Service: capabilities.Service, ProtocolVersion: 2, ObjectFormat: capabilities.ObjectFormat,
			ObjectFormatAdvertised: capabilities.ObjectFormatAdvertised,
			Capabilities:           append([]string(nil), capabilities.Capabilities...), EffectiveLimits: limits,
		},
		names:    map[string]int{},
		oidWidth: oidWidth(capabilities.ObjectFormat),
		prefixes: prefixes,
	}
	if err := s.readLsRefs(); err != nil {
		return nil, err
	}
	s.result.Empty = len(s.result.Refs) == 0
	if err := s.requireEndOfResponse(); err != nil {
		return nil, err
	}
	s.result.TotalBytes = s.reader.consumed
	return s.result, nil
}

// readLsRefs reads ls-refs records, "obj-id SP refname *(SP attribute)", up
// to the flush packet. Every record counts toward MaxRefRecords once.
func (s *parseState) readLsRefs() error {
	for {
		kind, payload, err := s.reader.next()
		offset := s.reader.offset
		switch {
		case err == io.EOF:
			return s.fail(offset, ErrTruncated, "the ref list has no terminating flush packet")
		case err != nil:
			return err
		}
		if kind == packetFlush {
			return nil
		}
		if kind != packetData {
			return s.fail(offset, ErrMalformedPacket, "an unexpected "+kind.String()+" appeared in the ref list")
		}
		line := string(trimLF(payload))
		if err := s.checkErrorPacket(offset, line); err != nil {
			return err
		}
		s.records++
		if s.records > s.limits.MaxRefRecords {
			return s.fail(offset, ErrTooManyRefs,
				fmt.Sprintf("the ref list exceeds the %d-record limit", s.limits.MaxRefRecords))
		}
		if err := s.readLsRefsRecord(offset, line); err != nil {
			return err
		}
	}
}

func (s *parseState) readLsRefsRecord(offset int64, line string) error {
	fields := strings.Split(line, " ")
	if len(fields) < 2 {
		return s.fail(offset, ErrInvalidRecord, "the record is not an object ID followed by a name")
	}
	if fields[0] == "unborn" {
		return s.fail(offset, ErrInvalidRecord, "the server sent an unborn record, which ls-refs did not ask for")
	}
	oid, name, err := s.splitRecord(offset, fields[0]+" "+fields[1])
	if err != nil {
		return err
	}
	symrefTarget, peeled := "", ""
	for _, attribute := range fields[2:] {
		switch key, value, _ := strings.Cut(attribute, ":"); key {
		case "symref-target":
			if symrefTarget != "" {
				return s.fail(offset, ErrConflictingRefs, "a ref has more than one symref target")
			}
			if err := validateRefName(value); err != nil {
				return s.fail(offset, ErrInvalidName, "symref target: "+err.Error())
			}
			symrefTarget = value
		case "peeled":
			if peeled != "" {
				return s.fail(offset, ErrConflictingRefs, "a ref has more than one peeled value")
			}
			if len(value) != s.oidWidth || !isHexOID(value) {
				return s.fail(offset, ErrInvalidObjectID, "a peeled value is not an object ID of the advertised format")
			}
			peeled = canonicalOID(value)
			if isZeroOID(peeled) {
				return s.fail(offset, ErrInvalidObjectID, "a peeled value is the zero object ID")
			}
		default:
			return s.fail(offset, ErrInvalidRecord, "the record carries an attribute that was not requested")
		}
	}
	if !hasRequestedPrefix(name, s.prefixes) {
		// ref-prefix only narrows the answer: gitprotocol-v2 lets a server
		// list other refs and has the client filter them. Such a record is
		// validated and counted like any other, then left out, so it is
		// neither fetched nor published.
		if isZeroOID(oid) {
			return s.fail(offset, ErrInvalidObjectID, "a ref has the zero object ID")
		}
		if err := validateRefName(name); err != nil {
			return s.fail(offset, ErrInvalidName, err.Error())
		}
		if s.unrequested[name] {
			return s.fail(offset, ErrConflictingRefs, "the advertisement repeats a ref name")
		}
		if s.unrequested == nil {
			s.unrequested = map[string]bool{}
		}
		s.unrequested[name] = true
		return nil
	}
	if err := s.addRef(offset, oid, name); err != nil {
		return err
	}
	if symrefTarget != "" {
		// applySymrefs copies the statement onto the ref and HEAD and checks
		// it against an advertised target.
		s.result.Symrefs = append(s.result.Symrefs, Symref{Name: name, Target: symrefTarget})
	}
	if peeled != "" {
		s.result.Refs[len(s.result.Refs)-1].PeeledOID = peeled
		if name == "HEAD" {
			s.result.Head.PeeledOID = peeled
		}
	}
	return nil
}

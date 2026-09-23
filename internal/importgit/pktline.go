package importgit

import (
	"errors"
	"fmt"
	"io"
)

// Framing constants from gitprotocol-common(5). A pkt-line begins with its
// total length as four hexadecimal digits, and that length includes the four
// length bytes themselves. No implementation may send a packet longer than
// 65520 bytes, so a longer declared length is a protocol violation rather than
// a local limit.
const (
	packetLengthBytes = 4
	protocolMaxPacket = 65520
)

// packetKind distinguishes the pkt-line forms this parser accepts from the
// protocol v2 control packets it refuses.
type packetKind int

const (
	packetNone packetKind = iota
	// packetData is an ordinary data packet.
	packetData
	// packetFlush is the "0000" flush-pkt.
	packetFlush
	// packetDelimiter is the v2-only "0001" delim-pkt.
	packetDelimiter
	// packetResponseEnd is the v2-only "0002" response-end-pkt.
	packetResponseEnd
)

func (k packetKind) String() string {
	switch k {
	case packetData:
		return "data packet"
	case packetFlush:
		return "flush packet"
	case packetDelimiter:
		return "delimiter packet"
	case packetResponseEnd:
		return "response-end packet"
	default:
		return "no packet"
	}
}

// packetReader reads pkt-lines from a response body under fixed byte limits.
//
// It never allocates a payload buffer before the declared length has been
// checked against both the protocol ceiling and the caller's packet limit, and
// it never reads more than the total byte budget. The payload returned by next
// is only valid until the following call, because the buffer is reused; the
// advertisement parser copies every fact it keeps.
type packetReader struct {
	source    io.Reader
	maxPacket int
	maxTotal  int64
	// consumed counts every byte read from source.
	consumed int64
	// offset is the start of the packet most recently returned by next.
	offset  int64
	length  [packetLengthBytes]byte
	payload []byte
}

// read fills buffer exactly. It reports io.EOF only when the source ended on a
// packet boundary with no bytes read, so a partial packet is always a
// truncation rather than a clean end of response.
func (r *packetReader) read(buffer []byte) error {
	if int64(len(buffer)) > r.maxTotal-r.consumed {
		return &ParseError{Offset: r.consumed, Err: ErrLimitExceeded,
			Detail: fmt.Sprintf("the response exceeds the %d-byte total limit", r.maxTotal)}
	}
	read, err := io.ReadFull(r.source, buffer)
	r.consumed += int64(read)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF) && read == 0:
		return io.EOF
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &ParseError{Offset: r.consumed, Err: ErrTruncated,
			Detail: fmt.Sprintf("the response ended mid-packet after %d bytes", r.consumed)}
	default:
		return err
	}
}

// next returns the following pkt-line. It returns io.EOF only when the source
// ended exactly on a packet boundary.
func (r *packetReader) next() (packetKind, []byte, error) {
	r.offset = r.consumed
	if err := r.read(r.length[:]); err != nil {
		return packetNone, nil, err
	}
	length, ok := parseHexLength(r.length)
	if !ok {
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrMalformedPacket,
			Detail: fmt.Sprintf("packet length %q is not four lowercase hexadecimal digits", string(r.length[:]))}
	}
	switch length {
	case 0:
		return packetFlush, nil, nil
	case 1:
		return packetDelimiter, nil, nil
	case 2:
		return packetResponseEnd, nil, nil
	case 3:
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrMalformedPacket,
			Detail: "packet length 3 is shorter than the four-byte length header"}
	case packetLengthBytes:
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrMalformedPacket,
			Detail: "an empty data packet is not part of a reference advertisement"}
	}
	if length > protocolMaxPacket {
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrMalformedPacket,
			Detail: fmt.Sprintf("packet length %d exceeds the %d-byte protocol maximum", length, protocolMaxPacket)}
	}
	if length > r.maxPacket {
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrLimitExceeded,
			Detail: fmt.Sprintf("packet length %d exceeds the %d-byte packet limit", length, r.maxPacket)}
	}
	size := length - packetLengthBytes
	// The remaining byte budget is checked before the buffer is allocated, so a
	// response that is already at its total limit cannot force one more
	// allocation for a payload it may never send.
	if int64(size) > r.maxTotal-r.consumed {
		return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrLimitExceeded,
			Detail: fmt.Sprintf("the response exceeds the %d-byte total limit", r.maxTotal)}
	}
	if cap(r.payload) < size {
		r.payload = make([]byte, size)
	}
	buffer := r.payload[:size]
	if err := r.read(buffer); err != nil {
		if errors.Is(err, io.EOF) {
			return packetNone, nil, &ParseError{Offset: r.offset, Err: ErrTruncated,
				Detail: fmt.Sprintf("the response ended before the %d-byte packet payload", size)}
		}
		return packetNone, nil, err
	}
	return packetData, buffer, nil
}

// parseHexLength decodes the four-digit pkt-len. gitprotocol-common(5) defines
// HEXDIG as digits and lowercase a-f, and gitprotocol-http(5) requires the
// first four bytes of a smart response to match "[0-9a-f]{4}", so uppercase is
// refused instead of being accepted silently.
func parseHexLength(digits [packetLengthBytes]byte) (int, bool) {
	value := 0
	for _, digit := range digits {
		switch {
		case digit >= '0' && digit <= '9':
			value = value<<4 | int(digit-'0')
		case digit >= 'a' && digit <= 'f':
			value = value<<4 | int(digit-'a'+10)
		default:
			return 0, false
		}
	}
	return value, true
}

// trimLF drops the single optional trailing LF of a non-binary pkt-line.
// gitprotocol-common(5) requires receivers to accept the line with or without
// it, and gitprotocol-http(5) repeats that for the service announcement.
func trimLF(payload []byte) []byte {
	if len(payload) > 0 && payload[len(payload)-1] == '\n' {
		return payload[:len(payload)-1]
	}
	return payload
}

package importfetch

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"owngit/internal/importgit"
)

var (
	errTotalBodyExceeded = errors.New("total HTTP entity-byte limit exceeded")
	errPackExceeded      = errors.New("pack byte limit exceeded")
)

type bodyBudget struct {
	remaining int64
	consumed  int64
	exceeded  bool
}

func (b *bodyBudget) reader(source io.Reader) io.Reader {
	return &budgetReader{source: source, budget: b}
}

type budgetReader struct {
	source io.Reader
	budget *bodyBudget
}

func (r *budgetReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.budget.remaining == 0 {
		var overflow [1]byte
		read, err := r.source.Read(overflow[:])
		if read > 0 {
			r.budget.consumed++
			r.budget.exceeded = true
			return 0, errTotalBodyExceeded
		}
		return 0, err
	}
	if int64(len(buffer)) > r.budget.remaining {
		buffer = buffer[:r.budget.remaining]
	}
	read, err := r.source.Read(buffer)
	r.budget.remaining -= int64(read)
	r.budget.consumed += int64(read)
	return read, err
}

type packReader struct {
	source      io.Reader
	remaining   int64
	consumed    int64
	exceeded    bool
	terminalErr error
}

func (r *packReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var overflow [1]byte
		read, err := r.source.Read(overflow[:])
		if read > 0 {
			r.consumed++
			r.exceeded = true
			r.terminalErr = errPackExceeded
			return 0, errPackExceeded
		}
		if err != nil {
			r.terminalErr = err
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	read, err := r.source.Read(buffer)
	r.remaining -= int64(read)
	r.consumed += int64(read)
	if err != nil {
		r.terminalErr = err
	}
	return read, err
}

type observedReader struct {
	source io.Reader
	eof    bool
}

func (r *observedReader) Read(buffer []byte) (int, error) {
	read, err := r.source.Read(buffer)
	if errors.Is(err, io.EOF) {
		r.eof = true
	}
	return read, err
}

// buildUploadRequest encodes a v0 or v1 upload-pack request for every
// advertised object.
func buildUploadRequest(advertisement *importgit.Advertisement, maximum int64) ([]byte, error) {
	capability, ok := requestCapability(advertisement)
	if !ok {
		return nil, fetchError("select upload-pack capability", ErrUploadPackProtocol, nil)
	}
	wants, err := selectWants(advertisement)
	if err != nil {
		return nil, err
	}
	var request bytes.Buffer
	for index, oid := range wants {
		line := "want " + oid
		if index == 0 {
			line += " " + capability
		}
		line += "\n"
		if err := appendPacket(&request, line, maximum); err != nil {
			return nil, err
		}
	}
	if err := appendBounded(&request, "0000", maximum); err != nil {
		return nil, err
	}
	if err := appendPacket(&request, "done\n", maximum); err != nil {
		return nil, err
	}
	return request.Bytes(), nil
}

// buildLsRefsCommand encodes the protocol v2 ls-refs command for HEAD,
// branches and tags, with their symref targets and peeled values.
func buildLsRefsCommand(capabilities *importgit.Advertisement, maximum int64) ([]byte, error) {
	arguments := []string{"symrefs", "peel"}
	for _, prefix := range importgit.LsRefsPrefixes {
		arguments = append(arguments, "ref-prefix "+prefix)
	}
	return buildCommand("ls-refs", capabilities, arguments, maximum)
}

// buildFetchCommand encodes the protocol v2 fetch command for every listed
// object, the same wants as buildUploadRequest. It asks for no progress and
// no thin pack, and sends done, so the answer is one packfile section.
func buildFetchCommand(advertisement *importgit.Advertisement, maximum int64) ([]byte, error) {
	wants, err := selectWants(advertisement)
	if err != nil {
		return nil, err
	}
	arguments := []string{"ofs-delta", "no-progress"}
	for _, oid := range wants {
		arguments = append(arguments, "want "+oid)
	}
	return buildCommand("fetch", advertisement, append(arguments, "done"), maximum)
}

// buildCommand encodes a protocol v2 command request: the command, the object
// format when the server advertised one, a delimiter, the arguments and a
// flush packet.
func buildCommand(command string, capabilities *importgit.Advertisement, arguments []string, maximum int64) ([]byte, error) {
	var request bytes.Buffer
	lines := []string{"command=" + command}
	if capabilities.ObjectFormatAdvertised {
		lines = append(lines, "object-format="+capabilities.ObjectFormat)
	}
	for _, line := range lines {
		if err := appendPacket(&request, line+"\n", maximum); err != nil {
			return nil, err
		}
	}
	if err := appendBounded(&request, "0001", maximum); err != nil {
		return nil, err
	}
	for _, argument := range arguments {
		if err := appendPacket(&request, argument+"\n", maximum); err != nil {
			return nil, err
		}
	}
	if err := appendBounded(&request, "0000", maximum); err != nil {
		return nil, err
	}
	return request.Bytes(), nil
}

// selectWants lists each advertised object once: every ref and HEAD.
func selectWants(advertisement *importgit.Advertisement) ([]string, error) {
	seen := make(map[string]struct{}, len(advertisement.Refs))
	wants := make([]string, 0, len(advertisement.Refs))
	for _, reference := range advertisement.Refs {
		if _, exists := seen[reference.OID]; exists {
			continue
		}
		seen[reference.OID] = struct{}{}
		wants = append(wants, reference.OID)
	}
	if advertisement.Head.Advertised {
		if _, exists := seen[advertisement.Head.OID]; !exists {
			seen[advertisement.Head.OID] = struct{}{}
			wants = append(wants, advertisement.Head.OID)
		}
	}
	if len(wants) == 0 {
		return nil, fetchError("select wants", ErrUploadPackProtocol, nil)
	}
	return wants, nil
}

func requestCapability(advertisement *importgit.Advertisement) (string, bool) {
	if advertisement.ObjectFormatAdvertised {
		return "object-format=" + advertisement.ObjectFormat, true
	}
	if advertisement.ObjectFormat != importgit.FormatSHA1 {
		return "", false
	}
	for _, capability := range advertisement.Capabilities {
		if capability == "ofs-delta" {
			return "ofs-delta", true
		}
	}
	return "", false
}

func appendPacket(destination *bytes.Buffer, payload string, maximum int64) error {
	length := len(payload) + 4
	if length > 65520 {
		return fetchError("encode upload-pack request", ErrRequestTooLarge, nil)
	}
	return appendBounded(destination, fmt.Sprintf("%04x%s", length, payload), maximum)
}

func appendBounded(destination *bytes.Buffer, value string, maximum int64) error {
	if int64(destination.Len())+int64(len(value)) > maximum {
		return fetchError("encode upload-pack request", ErrRequestTooLarge, nil)
	}
	_, _ = destination.WriteString(value)
	return nil
}

func readNAK(source io.Reader) error {
	var header [4]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return uploadProtocolReadError(err)
	}
	length, ok := strictHexLength(header)
	if !ok || (length != 7 && length != 8) {
		return fetchError("read upload-pack acknowledgement", ErrUploadPackProtocol, nil)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(source, payload); err != nil {
		return uploadProtocolReadError(err)
	}
	if string(payload) != "NAK" && string(payload) != "NAK\n" {
		return fetchError("read upload-pack acknowledgement", ErrUploadPackProtocol, nil)
	}
	return nil
}

// maxSidebandPacket is the largest pkt-line, LARGE_PACKET_MAX in Git, and
// sidebandHeader is its length prefix and band byte.
const (
	maxSidebandPacket = 65520
	sidebandHeader    = 5
)

// sidebandPackChunk is the smallest share of the pack a side-band packet is
// expected to carry. Git before 2.32 relayed the pack 8 KiB at a time; newer
// Git fills packets up to maxSidebandPacket.
const sidebandPackChunk = 8192

// sidebandOverhead is the framing allowed around maxPack bytes of pack data
// in a protocol v2 fetch answer: the packfile section header, a band header
// per packet of at least sidebandPackChunk bytes, and the final flush packet.
// It is about 10 MB for a 16 GiB pack. A pack framed in still smaller
// packets can reach the response bound slightly below MaxPackBytes.
func sidebandOverhead(maxPack int64) int64 {
	packets := maxPack/sidebandPackChunk + 1
	return int64(len("000dpackfile\n")) + packets*sidebandHeader + 4
}

// readPackfileSection reads the section header that starts a protocol v2
// fetch answer sent after done. Another section, such as shallow-info, is
// refused: this request asked for none, and a shallow pack is not a full
// snapshot.
func readPackfileSection(source io.Reader) error {
	var header [4]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return uploadProtocolReadError(err)
	}
	length, ok := strictHexLength(header)
	if !ok || (length != 12 && length != 13) {
		return fetchError("read upload-pack section", ErrUploadPackProtocol, nil)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(source, payload); err != nil {
		return uploadProtocolReadError(err)
	}
	if string(payload) != "packfile" && string(payload) != "packfile\n" {
		return fetchError("read upload-pack section", ErrUploadPackProtocol, nil)
	}
	return nil
}

// sidebandReader returns the pack data of a protocol v2 packfile section:
// the payloads of band 1. Band 2 carries progress text, which is discarded.
// Band 3 is the server reporting failure. The section ends with a flush
// packet, which must end the response. A protocol failure is kept in failure
// and reported as ErrUploadPackProtocol; the remote text is never kept. A
// read error from the response itself, such as the body budget or a broken
// connection, is returned unchanged for the caller to classify.
type sidebandReader struct {
	source  io.Reader
	pending []byte
	buffer  [maxSidebandPacket]byte
	done    bool
	failure error
}

func (r *sidebandReader) Read(buffer []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.failure != nil {
			return 0, r.failure
		}
		if r.done {
			return 0, io.EOF
		}
		if err := r.next(); err != nil {
			return 0, err
		}
	}
	read := copy(buffer, r.pending)
	r.pending = r.pending[read:]
	return read, nil
}

// next reads one packet.
func (r *sidebandReader) next() error {
	header := r.buffer[:4]
	if _, err := io.ReadFull(r.source, header); err != nil {
		return r.readError(err)
	}
	length, ok := strictHexLength([4]byte(header))
	switch {
	case !ok || (length > 0 && length <= 4) || length > maxSidebandPacket:
		return r.fail("read side-band packet")
	case length == 0:
		// The flush packet ends the section and the response.
		var probe [1]byte
		read, err := io.ReadFull(r.source, probe[:])
		if read > 0 {
			return r.fail("read side-band trailer")
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		r.done = true
		return nil
	}
	packet := r.buffer[:length-4]
	if _, err := io.ReadFull(r.source, packet); err != nil {
		return r.readError(err)
	}
	switch packet[0] {
	case 1:
		r.pending = packet[1:]
	case 2:
	case 3:
		return r.fail("read side-band remote error")
	default:
		return r.fail("read side-band band")
	}
	return nil
}

// readError keeps a response that ended early as a protocol failure, since
// the section had not ended, and returns other read errors unchanged.
func (r *sidebandReader) readError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return r.fail("read side-band packet")
	}
	return err
}

func (r *sidebandReader) fail(op string) error {
	r.failure = fetchError(op, ErrUploadPackProtocol, nil)
	return r.failure
}

func strictHexLength(header [4]byte) (int, bool) {
	value := 0
	for _, digit := range header {
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

func uploadProtocolReadError(err error) error {
	if errors.Is(err, errTotalBodyExceeded) || errors.Is(err, errPackExceeded) {
		return fetchError("read upload-pack response", ErrResponseTooLarge, nil)
	}
	return fetchError("read upload-pack response", ErrUploadPackProtocol, safeReadCause(err))
}

func safeReadCause(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

func advertisementCause(err error) error {
	known := []error{
		importgit.ErrInvalidLimits,
		importgit.ErrInvalidOptions,
		// Before ErrLimitExceeded, which it wraps.
		importgit.ErrTooManyRefs,
		importgit.ErrLimitExceeded,
		importgit.ErrMalformedPacket,
		importgit.ErrTruncated,
		importgit.ErrTrailingContent,
		importgit.ErrMalformedService,
		importgit.ErrUnsupportedVersion,
		importgit.ErrInvalidRecord,
		importgit.ErrInvalidObjectID,
		importgit.ErrInvalidName,
		importgit.ErrConflictingRefs,
		importgit.ErrInvalidCapability,
		importgit.ErrObjectFormat,
		importgit.ErrRemoteError,
		importgit.ErrIncompleteHistory,
	}
	for _, candidate := range known {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return nil
}

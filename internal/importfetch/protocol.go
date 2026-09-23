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

func buildUploadRequest(advertisement *importgit.Advertisement, maximum int64) ([]byte, error) {
	capability, ok := requestCapability(advertisement)
	if !ok {
		return nil, fetchError("select upload-pack capability", ErrUploadPackProtocol, nil)
	}
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

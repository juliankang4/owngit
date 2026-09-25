package githttp

import (
	"bytes"
	"strconv"
)

// maximumPacket is the largest pkt-line, including its 4-byte length.
const maximumPacket = 65520

// pushReport reads a receive-pack response as it is copied to the client and
// notes the failures Git reports inside the protocol: git-http-backend exits
// 0 when objects cannot be stored or a ref update is refused. It keeps at
// most one packet and never stores request bytes or backend output; reason
// returns a fixed description.
type pushReport struct {
	pending []byte
	inner   []byte
	// sideband is 0 until the first packet shows whether the report is
	// multiplexed, then 1 or -1.
	sideband     int
	stopped      bool
	unpackFailed bool
	refusedRefs  bool
	fatal        bool
	hookDeclined bool
	storageFull  bool
	notWritable  bool
}

func (report *pushReport) Write(content []byte) (int, error) {
	if !report.stopped {
		report.pending = report.packets(append(report.pending, content...), report.packet)
	}
	return len(content), nil
}

// packets calls handle for each complete pkt-line in buffer and returns the
// incomplete rest. A malformed length stops the report.
func (report *pushReport) packets(buffer []byte, handle func([]byte)) []byte {
	for len(buffer) >= 4 && !report.stopped {
		length, err := strconv.ParseUint(string(buffer[:4]), 16, 16)
		switch {
		case err != nil || length == 3 || length > maximumPacket:
			report.stopped = true
			return nil
		case length < 4:
			// Flush, delimiter and response-end packets carry no payload.
			buffer = buffer[4:]
			continue
		case len(buffer) < int(length):
			return buffer
		}
		handle(buffer[4:length])
		buffer = buffer[length:]
	}
	return buffer
}

func (report *pushReport) packet(payload []byte) {
	if report.sideband == 0 {
		report.sideband = -1
		if len(payload) > 0 && payload[0] >= 1 && payload[0] <= 3 {
			report.sideband = 1
		}
	}
	if report.sideband < 0 {
		report.status(payload)
		return
	}
	if len(payload) == 0 {
		return
	}
	switch data := payload[1:]; payload[0] {
	case 1:
		report.inner = report.packets(append(report.inner, data...), report.status)
	case 2:
		report.scan(data)
	case 3:
		report.fatal = true
		report.scan(data)
	}
}

// status reads one report-status line: "unpack ok", "unpack <error>",
// "ok <ref>" or "ng <ref> <reason>".
func (report *pushReport) status(line []byte) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	switch {
	case bytes.HasPrefix(line, []byte("unpack ")):
		if !bytes.Equal(line, []byte("unpack ok")) {
			report.unpackFailed = true
			report.scan(line)
		}
	case bytes.HasPrefix(line, []byte("ng ")):
		report.refusedRefs = true
		report.scan(line)
	}
}

// scan notes known causes in Git's messages.
func (report *pushReport) scan(message []byte) {
	report.storageFull = report.storageFull || bytes.Contains(message, []byte("No space left on device"))
	report.notWritable = report.notWritable || bytes.Contains(message, []byte("Read-only file system")) ||
		bytes.Contains(message, []byte("Permission denied")) ||
		bytes.Contains(message, []byte("unable to create temporary object directory"))
	report.hookDeclined = report.hookDeclined || bytes.Contains(message, []byte("hook declined"))
}

// reason describes a refused push for the server log, or returns "" when Git
// reported no failure.
func (report *pushReport) reason() string {
	if !report.unpackFailed && !report.refusedRefs && !report.fatal {
		return ""
	}
	switch {
	case report.storageFull:
		return "push refused: repository storage is full"
	case report.notWritable:
		return "push refused: repository storage is not writable"
	case report.unpackFailed:
		return "push refused: Git could not store the pushed objects"
	case report.hookDeclined:
		return "push refused: a server hook declined a ref update"
	case report.refusedRefs:
		return "push refused: Git refused a ref update"
	default:
		return "Git backend reported a fatal error"
	}
}

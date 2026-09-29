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
// returns a fixed description, and refsUnchanged tells whether the report
// proves that the push changed no ref.
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
	// unpackSeen and ended mark the report-status list from its unpack line
	// to its closing flush. accepted is set by an "ok" line, and
	// mayHaveWritten by any other line or refusal after which the update
	// hook may already have preserved history.
	unpackSeen     bool
	ended          bool
	accepted       bool
	mayHaveWritten bool
	// acceptedRefs names each ref of an "ok" line, in the report's order.
	acceptedRefs []string
}

func (report *pushReport) Write(content []byte) (int, error) {
	if !report.stopped {
		report.pending = report.packets(append(report.pending, content...), report.packet, func() {
			if report.sideband < 0 {
				report.statusEnd()
			}
		})
	}
	return len(content), nil
}

// packets calls handle for each complete pkt-line in buffer, and flush for
// each flush packet, and returns the incomplete rest. A malformed length stops
// the report.
func (report *pushReport) packets(buffer []byte, handle func([]byte), flush func()) []byte {
	for len(buffer) >= 4 && !report.stopped {
		length, err := strconv.ParseUint(string(buffer[:4]), 16, 16)
		switch {
		case err != nil || length == 3 || length > maximumPacket:
			report.stopped = true
			return nil
		case length < 4:
			// Flush, delimiter and response-end packets carry no payload.
			if length == 0 && flush != nil {
				flush()
			}
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
		report.inner = report.packets(append(report.inner, data...), report.status, report.statusEnd)
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
	case report.ended:
		report.mayHaveWritten = true
	case bytes.HasPrefix(line, []byte("unpack ")):
		report.unpackSeen = true
		if !bytes.Equal(line, []byte("unpack ok")) {
			report.unpackFailed = true
			report.scan(line)
		}
	case bytes.HasPrefix(line, []byte("ok ")):
		report.accepted = true
		report.acceptedRefs = append(report.acceptedRefs, string(line[len("ok "):]))
	case bytes.HasPrefix(line, []byte("ng ")):
		report.refusedRefs = true
		report.scan(line)
		if !refusedBeforeWriting(line) {
			report.mayHaveWritten = true
		}
	default:
		report.mayHaveWritten = true
	}
}

// statusEnd notes the flush that closes the report-status list.
func (report *pushReport) statusEnd() {
	if report.unpackSeen {
		report.ended = true
	}
}

// refusedBeforeWriting reports whether an "ng <ref> <reason>" line names a
// refusal after which neither Git nor OwnGit's update hook wrote a ref. Git
// runs no hook after an unpack failure. OwnGit's update hook preserves history
// only as its last step and then succeeds, so a declined update wrote nothing,
// unless the hook reported that preserving history failed (see scan). Every
// other reason, such as an atomic push failure after another ref's hook ran,
// may follow a write.
func refusedBeforeWriting(line []byte) bool {
	_, reason, found := bytes.Cut(bytes.TrimPrefix(line, []byte("ng ")), []byte(" "))
	if !found {
		return false
	}
	switch string(reason) {
	case "hook declined", "unpacker error", "n/a (unpacker error)":
		return true
	}
	return false
}

// refsUnchanged reports whether the complete report proves that the push
// changed no ref: it listed every command, accepted none, and refused each
// one before any write. The caller also requires that the backend exited
// normally after the whole response was read.
func (report *pushReport) refsUnchanged() bool {
	return report.unpackSeen && report.ended && !report.stopped && !report.fatal &&
		!report.accepted && !report.mayHaveWritten
}

// scan notes known causes in Git's messages.
func (report *pushReport) scan(message []byte) {
	report.storageFull = report.storageFull || bytes.Contains(message, []byte("No space left on device"))
	report.notWritable = report.notWritable || bytes.Contains(message, []byte("Read-only file system")) ||
		bytes.Contains(message, []byte("Permission denied")) ||
		bytes.Contains(message, []byte("unable to create temporary object directory"))
	report.hookDeclined = report.hookDeclined || bytes.Contains(message, []byte("hook declined"))
	// The retention hook's message when its own ref transaction failed, which
	// may have written part of the retained refs.
	report.mayHaveWritten = report.mayHaveWritten || bytes.Contains(message, []byte("could not preserve previous history"))
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

// RefUpdate is a ref that a push updated. Old is "" when the push created
// the ref, and New is "" when it deleted it.
type RefUpdate struct {
	Ref, Old, New string
}

// pushCommands reads the ref update commands at the start of a receive-pack
// request, "<old> <new> <ref>" packets up to the first flush packet, as the
// backend reads the request. It keeps at most one incomplete packet and the
// commands themselves, which receive-pack also holds for the same request.
type pushCommands struct {
	pending []byte
	done    bool
	byRef   map[string]RefUpdate
}

func (commands *pushCommands) Write(content []byte) (int, error) {
	if commands.done {
		return len(content), nil
	}
	buffer := append(commands.pending, content...)
	for !commands.done && len(buffer) >= 4 {
		length, err := strconv.ParseUint(string(buffer[:4]), 16, 16)
		switch {
		case err != nil || length < 4 || length > maximumPacket:
			// A flush packet ends the commands; anything else is not a
			// command list.
			commands.done = true
		case len(buffer) < int(length):
			commands.pending = append([]byte(nil), buffer...)
			return len(content), nil
		default:
			commands.command(buffer[4:length])
			buffer = buffer[length:]
		}
	}
	commands.pending = nil
	if !commands.done {
		commands.pending = append([]byte(nil), buffer...)
	}
	return len(content), nil
}

// command notes one packet that is a ref update command. The first command
// carries the capabilities after a NUL byte. Other packets, such as
// "shallow" lines and the header of a push certificate, are not commands.
func (commands *pushCommands) command(payload []byte) {
	payload, _, _ = bytes.Cut(payload, []byte{0})
	fields := bytes.SplitN(bytes.TrimSuffix(payload, []byte("\n")), []byte(" "), 3)
	if len(fields) != 3 || !objectID(fields[0]) || !objectID(fields[1]) || len(fields[2]) == 0 {
		return
	}
	// A command that names the value the ref already has changes nothing,
	// even when Git answers it with "ok".
	if bytes.Equal(fields[0], fields[1]) {
		return
	}
	ref := string(fields[2])
	if _, seen := commands.byRef[ref]; seen {
		return
	}
	if commands.byRef == nil {
		commands.byRef = map[string]RefUpdate{}
	}
	commands.byRef[ref] = RefUpdate{Ref: ref, Old: presentObject(fields[0]), New: presentObject(fields[1])}
}

// objectID reports whether value is a SHA-1 or SHA-256 object ID as Git
// writes it.
func objectID(value []byte) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// presentObject returns the object ID, or "" for the all-zero ID with which
// a command names a ref that does not exist.
func presentObject(value []byte) string {
	if len(bytes.Trim(value, "0")) == 0 {
		return ""
	}
	return string(value)
}

// updates returns the refs the push changed: each ref that a complete
// report accepted, with the command the request gave for it, in the
// report's order. receive-pack reports only the refs it was asked to
// update, so every accepted ref has a command unless that command changed
// nothing (see command).
func (report *pushReport) updates(commands *pushCommands) []RefUpdate {
	if !report.unpackSeen || !report.ended || report.stopped {
		return nil
	}
	var updates []RefUpdate
	for _, ref := range report.acceptedRefs {
		if update, found := commands.byRef[ref]; found {
			updates = append(updates, update)
		}
	}
	return updates
}

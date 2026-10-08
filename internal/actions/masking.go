package actions

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"owngit/internal/checkoutput"
)

const (
	maxAddedMasks = 256
	maxMaskBytes  = 8 << 10
	maskCommand   = "::add-mask::"
)

var errMaskLimit = errors.New("workflow.mask_limit: masking limit exceeded; remaining output withheld")

type masker struct {
	values  []string
	added   int
	stopped bool
}

func newMasker(secrets map[string]string) *masker {
	mask := &masker{}
	for _, value := range secrets {
		mask.remember(value)
		if strings.ContainsAny(value, "\r\n") {
			for _, line := range strings.FieldsFunc(value, func(char rune) bool { return char == '\r' || char == '\n' }) {
				if len(line) >= 8 {
					mask.remember(line)
				}
			}
		}
	}
	return mask
}

func (mask *masker) remember(value string) bool {
	if value == "" {
		return false
	}
	for _, existing := range mask.values {
		if existing == value {
			return false
		}
	}
	mask.values = append(mask.values, value)
	return true
}

func (mask *masker) add(value string) error {
	if len(value) > maxMaskBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		mask.stopped = true
		return errMaskLimit
	}
	for _, existing := range mask.values {
		if existing == value {
			return nil
		}
	}
	if value != "" {
		if mask.added >= maxAddedMasks {
			mask.stopped = true
			return errMaskLimit
		}
		mask.remember(value)
		mask.added++
	}
	return nil
}

func (mask *masker) text(value string) string {
	if value == "" {
		return ""
	}
	if mask.stopped {
		return "[output withheld]"
	}
	var output strings.Builder
	redactor := literalRedactor{mask: mask, emit: func(text string) { output.WriteString(text) }}
	redactor.write(value, true)
	return output.String()
}

type literalRedactor struct {
	mask              *masker
	pendingMaskPrefix string
	coveredMaskBytes  int
	invalid           bool
	emit              func(string)
}

func (redactor *literalRedactor) write(value string, final bool) {
	redactor.pendingMaskPrefix += value
	var output strings.Builder
	index := 0
	for index < len(redactor.pendingMaskPrefix) {
		rest := redactor.pendingMaskPrefix[index:]
		size, partial := 0, false
		for _, mask := range redactor.mask.values {
			if strings.HasPrefix(rest, mask) {
				size = max(size, len(mask))
			} else if len(rest) < len(mask) && strings.HasPrefix(mask, rest) {
				partial = true
			}
		}
		if !final && (partial || !utf8.FullRuneInString(rest)) {
			break
		}
		if size > 0 {
			if redactor.coveredMaskBytes == 0 {
				output.WriteString("[redacted]")
			}
			redactor.coveredMaskBytes = max(redactor.coveredMaskBytes, size)
		}
		if redactor.coveredMaskBytes > 0 {
			redactor.coveredMaskBytes--
			index++
			continue
		}
		char, width := utf8.DecodeRuneInString(rest)
		if char == utf8.RuneError && width == 1 {
			if !redactor.invalid {
				output.WriteRune(utf8.RuneError)
			}
			redactor.invalid = true
		} else {
			output.WriteString(rest[:width])
			redactor.invalid = false
		}
		index += width
	}
	redactor.pendingMaskPrefix = strings.Clone(redactor.pendingMaskPrefix[index:])
	redactor.emit(output.String())
}

type commandMaskingWriter struct {
	mu      sync.Mutex
	mask    *masker
	pending []byte
	kept    checkoutput.LogBuffer
	literal literalRedactor
	err     error
}

func newCommandMaskingWriter(mask *masker) *commandMaskingWriter {
	stream := &commandMaskingWriter{mask: mask, kept: checkoutput.LogBuffer{Limit: maximumStepOutput}}
	stream.literal = literalRedactor{mask: mask, emit: stream.kept.Add}
	return stream
}

func (stream *commandMaskingWriter) Write(value []byte) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.err == nil {
		stream.pending = append(stream.pending, value...)
		stream.consumeMaskCommands(false)
	}
	return len(value), stream.err
}

func (stream *commandMaskingWriter) consumeMaskCommands(final bool) {
	for len(stream.pending) != 0 && stream.err == nil {
		marker := bytes.Index(stream.pending, []byte(maskCommand))
		newline := bytes.IndexByte(stream.pending, '\n')
		if marker >= 0 && (newline < 0 || marker < newline) {
			stream.literal.write(string(stream.pending[:marker]), false)
			stream.pending = stream.pending[marker:]
			end := bytes.IndexByte(stream.pending, '\n')
			if end < 0 && !final {
				if len(stream.pending) > len(maskCommand)+3*maxMaskBytes+1 {
					stream.mask.stopped, stream.err = true, errMaskLimit
				}
				return
			}
			if end < 0 {
				end = len(stream.pending)
			}
			encoded := strings.TrimSuffix(string(stream.pending[len(maskCommand):end]), "\r")
			value := strings.NewReplacer("%0D", "\r", "%0A", "\n", "%25", "%").Replace(encoded)
			stream.err = stream.mask.add(value)
			stream.pending = stream.pending[min(end+1, len(stream.pending)):]
			continue
		}
		if newline >= 0 {
			stream.literal.write(string(stream.pending[:newline+1]), false)
			stream.pending = stream.pending[newline+1:]
			continue
		}
		keep := min(len(stream.pending), len(maskCommand)-1)
		if final {
			keep = 0
		}
		stream.literal.write(string(stream.pending[:len(stream.pending)-keep]), false)
		stream.pending = append(stream.pending[:0], stream.pending[len(stream.pending)-keep:]...)
		return
	}
}

func (stream *commandMaskingWriter) finish() (string, checkoutput.Gap, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.consumeMaskCommands(true)
	if stream.err == nil {
		stream.literal.write("", true)
	} else {
		stream.kept.Add("\n" + errMaskLimit.Error() + "\n")
	}
	value, gap, _ := stream.kept.ResultWithGap()
	return value, gap, stream.err
}

package firstrun

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// errStopped means the owner pressed Ctrl-C, the terminal closed, or the
	// server was asked to stop while setup questions were open.
	errStopped = errors.New("setup stopped")
	// errFinishedElsewhere means setup was completed by another path, such as
	// an approved browser, while the terminal was waiting for an answer.
	errFinishedElsewhere = errors.New("setup finished elsewhere")
)

type keyKind int

const (
	keyRune keyKind = iota
	keyEnter
	keyBackspace
	keyKillLine
	keyInterrupt
	keyOther
)

type key struct {
	kind keyKind
	r    rune
}

// maxAnswerRunes bounds a typed answer. The longest accepted password is 1024
// characters, so a longer answer is still refused by the password rules.
const maxAnswerRunes = 4096

// console reads keys from the terminal. The terminal is in a mode without
// echo, line editing or signal keys, so every key, Ctrl-C included, arrives
// here, and hidden answers are never shown.
type console struct {
	ctx context.Context
	// input delivers what the terminal sends; it is closed when the terminal
	// can no longer be read.
	input <-chan []byte
	// finished is closed when setup completes by another path.
	finished <-chan struct{}
	pending  []byte
	echo     func(string)
}

// errInputTimeout is returned by byteWithin when nothing arrived in time.
var errInputTimeout = errors.New("no input")

// nextByte waits for the next byte of input. extra, when not nil, ends the
// wait early with ok false.
func (c *console) nextByte(extra <-chan struct{}, timeout <-chan time.Time) (byte, error) {
	for len(c.pending) == 0 {
		select {
		case chunk, ok := <-c.input:
			if !ok {
				return 0, errStopped
			}
			c.pending = append(c.pending, chunk...)
			clear(chunk)
		case <-c.ctx.Done():
			return 0, errStopped
		case <-c.finished:
			return 0, errFinishedElsewhere
		case <-extra:
			return 0, errWoken
		case <-timeout:
			return 0, errInputTimeout
		}
	}
	// Consumed bytes are cleared, so a typed password does not linger in
	// the buffer.
	b := c.pending[0]
	c.pending[0] = 0
	c.pending = c.pending[1:]
	return b, nil
}

// errWoken is returned when the extra channel of a wait fired.
var errWoken = errors.New("woken")

func (c *console) byteWithin(d time.Duration) (byte, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	return c.nextByte(nil, timer.C)
}

// readKey returns the next key. Escape sequences, including late replies to
// the background color query, are read whole and reported as keyOther, so
// they never become typed text.
func (c *console) readKey(extra <-chan struct{}, timeout <-chan time.Time) (key, error) {
	b, err := c.nextByte(extra, timeout)
	if err != nil {
		return key{}, err
	}
	switch {
	case b == 0x03:
		return key{kind: keyInterrupt}, nil
	case b == '\r' || b == '\n':
		return key{kind: keyEnter}, nil
	case b == 0x7F || b == 0x08:
		return key{kind: keyBackspace}, nil
	case b == 0x15:
		return key{kind: keyKillLine}, nil
	case b == 0x1B:
		return c.escape()
	case b < 0x80:
		// Other control characters, such as a pasted tab, are part of the
		// answer, as they are in the web form.
		return key{kind: keyRune, r: rune(b)}, nil
	}
	// A UTF-8 sequence: collect its continuation bytes.
	sequence := []byte{b}
	for !utf8.FullRune(sequence) && len(sequence) < utf8.UTFMax {
		next, err := c.byteWithin(50 * time.Millisecond)
		if errors.Is(err, errInputTimeout) {
			break
		}
		if err != nil {
			return key{}, err
		}
		sequence = append(sequence, next)
	}
	r, _ := utf8.DecodeRune(sequence)
	if r == utf8.RuneError {
		return key{kind: keyOther}, nil
	}
	return key{kind: keyRune, r: r}, nil
}

// escape reads the rest of an escape sequence: a control sequence such as an
// arrow key or a device-attributes reply, or an operating system command
// such as a background color reply.
func (c *console) escape() (key, error) {
	b, err := c.byteWithin(50 * time.Millisecond)
	if errors.Is(err, errInputTimeout) {
		return key{kind: keyOther}, nil
	}
	if err != nil {
		return key{}, err
	}
	switch b {
	case '[', 'O':
		for i := 0; i < 64; i++ {
			next, err := c.byteWithin(50 * time.Millisecond)
			if errors.Is(err, errInputTimeout) {
				break
			}
			if err != nil {
				return key{}, err
			}
			if next >= 0x40 && next <= 0x7E && !(b == '[' && i == 0 && next == '[') {
				break
			}
		}
	case ']', 'P', '_', '^':
		// Ends with BEL or ESC \.
		previous := byte(0)
		for i := 0; i < 1024; i++ {
			next, err := c.byteWithin(200 * time.Millisecond)
			if errors.Is(err, errInputTimeout) {
				break
			}
			if err != nil {
				return key{}, err
			}
			if next == 0x07 || (previous == 0x1B && next == '\\') {
				break
			}
			previous = next
		}
	}
	return key{kind: keyOther}, nil
}

// readLine reads one answer. It keeps every character the web form keeps:
// only the keys that edit or end the line (Enter, Backspace, Ctrl-U, Ctrl-C
// and Escape sequences) are not part of it, and nothing is normalized. So a
// pasted password with a no-break space, a zero-width space, combining marks
// or emoji is saved exactly as the web form would save it. Visible answers
// echo their graphic characters; hidden answers echo nothing at all, not
// even a placeholder. The returned string is the only copy; the edit buffer
// is cleared.
func (c *console) readLine(hidden bool) (string, error) {
	// Full capacity up front: append never moves the answer to a new array
	// and leaves an uncleared copy behind.
	buffer := make([]rune, 0, maxAnswerRunes)
	defer func() { clear(buffer[:cap(buffer)]) }()
	for {
		k, err := c.readKey(nil, nil)
		if err != nil {
			return "", err
		}
		switch k.kind {
		case keyInterrupt:
			return "", errStopped
		case keyEnter:
			c.echo("\n")
			return string(buffer), nil
		case keyBackspace, keyKillLine:
			for len(buffer) > 0 {
				last := buffer[len(buffer)-1]
				buffer[len(buffer)-1] = 0
				buffer = buffer[:len(buffer)-1]
				if !hidden {
					width := echoWidth(last)
					c.echo(strings.Repeat("\b", width) + strings.Repeat(" ", width) + strings.Repeat("\b", width))
				}
				if k.kind == keyBackspace {
					break
				}
			}
		case keyRune:
			if len(buffer) >= maxAnswerRunes {
				continue
			}
			buffer = append(buffer, k.r)
			if !hidden && echoWidth(k.r) > 0 {
				c.echo(string(k.r))
			}
		}
	}
}

// echoWidth is the width a typed character takes when echoed. Controls and
// format characters, such as a tab or a direction override, are kept in the
// answer but not echoed, so they cannot move the cursor; the review card
// shows them as escapes.
func echoWidth(r rune) int {
	if !unicode.IsGraphic(r) {
		return 0
	}
	return runeWidth(r)
}

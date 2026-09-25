//go:build windows

package firstrun

import (
	"encoding/binary"
	"os"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stopSignals is empty: Ctrl-Break, a closed console, logoff and shutdown
// already stop the server through its context.
var stopSignals []os.Signal

// continueSignals is empty: a console process is not stopped and continued.
var continueSignals []os.Signal

// foreground is always true: a console has no background process groups,
// so a console start keeps the terminal check alone.
func foreground(*os.File) bool { return true }

func isTerminal(file *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil
}

// enterSetupMode turns off echo, line input and Ctrl-C processing on the
// console input, so hidden answers are never shown and Ctrl-C arrives as a
// key, and asks for escape sequences on input and output.
func enterSetupMode(input, output *os.File) (setupMode, error) {
	in, out := windows.Handle(input.Fd()), windows.Handle(output.Fd())
	var previousIn, previousOut uint32
	if err := windows.GetConsoleMode(in, &previousIn); err != nil {
		return setupMode{}, err
	}
	if err := windows.GetConsoleMode(out, &previousOut); err != nil {
		return setupMode{}, err
	}
	mode := previousIn &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(in, mode|windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		if err := windows.SetConsoleMode(in, mode); err != nil {
			return setupMode{}, err
		}
	}
	escapes := windows.SetConsoleMode(out, previousOut|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	restore := func() error {
		errOut := windows.SetConsoleMode(out, previousOut)
		if err := windows.SetConsoleMode(in, previousIn); err != nil {
			return err
		}
		return errOut
	}
	return setupMode{restore: restore, resume: func() error { return nil }, escapes: escapes}, nil
}

func terminalColumns(output *os.File) (int, bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(output.Fd()), &info); err != nil {
		return 0, false
	}
	columns := int(info.Window.Right-info.Window.Left) + 1
	return columns, columns > 0
}

// readConsoleInput reads console input records without waiting for more.
var readConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

// inputRecord is INPUT_RECORD. For a KEY_EVENT, event holds
// KEY_EVENT_RECORD: bKeyDown at 0, wRepeatCount at 4, wVirtualKeyCode at 6,
// uChar at 10.
type inputRecord struct {
	eventType uint16
	_         uint16
	event     [16]byte
}

// virtualKeyAlt is VK_MENU. A character typed with Alt and the number pad
// arrives on its key-up record.
const virtualKeyAlt = 0x12

// readTerminal copies what the console sends to input until done is closed
// or the console can no longer be read, then closes input. It waits on the
// console handle with a timeout and takes only the records already there,
// so it never stays blocked after setup ends. A console has no background,
// so resume is never needed.
func readTerminal(done <-chan struct{}, file *os.File, input chan<- []byte, _ func()) {
	defer close(input)
	handle := windows.Handle(file.Fd())
	records := make([]inputRecord, 64)
	defer clear(records)
	var high uint16 // the first half of a surrogate pair
	for {
		select {
		case <-done:
			return
		default:
		}
		event, err := windows.WaitForSingleObject(handle, uint32(readTick/time.Millisecond))
		if err != nil || (event != windows.WAIT_OBJECT_0 && event != uint32(windows.WAIT_TIMEOUT)) {
			return
		}
		if event == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		var count uint32
		if ok, _, _ := readConsoleInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&count))); ok == 0 {
			return
		}
		var chunk []byte
		for _, record := range records[:count] {
			if record.eventType != windows.KEY_EVENT {
				continue
			}
			down := binary.LittleEndian.Uint32(record.event[0:4]) != 0
			repeat := int(binary.LittleEndian.Uint16(record.event[4:6]))
			virtualKey := binary.LittleEndian.Uint16(record.event[6:8])
			char := rune(binary.LittleEndian.Uint16(record.event[10:12]))
			if char == 0 || (!down && virtualKey != virtualKeyAlt) {
				continue
			}
			switch {
			case utf16.IsSurrogate(char) && char < 0xDC00:
				high = uint16(char)
				continue
			case utf16.IsSurrogate(char):
				char = utf16.DecodeRune(rune(high), char)
				high = 0
			}
			for i := 0; i < max(repeat, 1); i++ {
				chunk = utf8.AppendRune(chunk, char)
			}
		}
		clear(records[:count])
		if len(chunk) == 0 {
			continue
		}
		select {
		case input <- chunk:
		case <-done:
			clear(chunk)
			return
		}
	}
}

//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
)

// hiddenInputSignals is unused here because echo is never turned off.
var hiddenInputSignals = []os.Signal{os.Interrupt}

// disableStdinEcho is unsupported here, so a secret prompt is refused rather
// than echoed.
func disableStdinEcho() (func() error, error) {
	return nil, errors.New("hiding terminal input is unsupported on this platform")
}

//go:build !darwin && !linux && !windows

package firstrun

import (
	"errors"
	"os"
)

var stopSignals, continueSignals []os.Signal

func foreground(*os.File) bool { return false }

// isTerminal is false here, so these platforms keep the setup file flow.
func isTerminal(*os.File) bool { return false }

func enterSetupMode(_, _ *os.File) (setupMode, error) {
	return setupMode{}, errors.New("terminal setup is unsupported on this platform")
}

func terminalColumns(*os.File) (int, bool) { return 0, false }

// readTerminal is never reached here, since setup mode is refused.
func readTerminal(_ <-chan struct{}, _ *os.File, input chan<- []byte, _ func()) { close(input) }

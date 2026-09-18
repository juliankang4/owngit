//go:build !darwin && !linux && !windows

package state

import (
	"errors"
	"os"
)

func tryOperationLock(*os.File) (func(), error) {
	return nil, errors.New("offline operation locking is unsupported on this platform")
}

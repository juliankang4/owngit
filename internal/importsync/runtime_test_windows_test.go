//go:build windows

package importsync

import (
	"errors"

	"golang.org/x/sys/windows"
)

func runtimeSharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

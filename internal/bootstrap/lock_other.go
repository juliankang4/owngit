//go:build !darwin && !linux && !windows

package bootstrap

import (
	"context"
	"errors"
	"os"
)

func lockFile(context.Context, *os.File) (func(), error) {
	return nil, errors.New("cross-process setup issuance locking is unsupported on this platform")
}

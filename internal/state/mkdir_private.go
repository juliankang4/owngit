package state

import (
	"errors"
	"fmt"
	"io/fs"
)

// ErrPrivateDirectoryExists identifies only a collision reported by the
// operation that attempted to create the directory name. Later inspection or
// cleanup failures deliberately do not unwrap to filesystem errors, because
// ENOTEMPTY can alias ErrExist on Unix without meaning the name pre-existed.
var ErrPrivateDirectoryExists = errors.New("private directory already exists")

type privateDirectoryExistsError struct {
	path  string
	cause error
}

func (e *privateDirectoryExistsError) Error() string {
	return fmt.Sprintf("create private directory %s: %v", e.path, e.cause)
}

func (e *privateDirectoryExistsError) Unwrap() error { return ErrPrivateDirectoryExists }

type privateDirectoryPostCreateError struct{ cause error }

func (e *privateDirectoryPostCreateError) Error() string { return e.cause.Error() }

func privateDirectoryMkdirError(path string, err error) error {
	if errors.Is(err, fs.ErrExist) {
		return &privateDirectoryExistsError{path: path, cause: err}
	}
	return err
}

func privateDirectoryPostCreateFailure(err error) error {
	if err == nil {
		return nil
	}
	return &privateDirectoryPostCreateError{cause: err}
}

package state

import (
	"errors"
	"fmt"
)

// NotPrivateError reports why a file that must be private is not, and a
// one-line command that fixes it. Neither part holds the file's content.
type NotPrivateError struct {
	// Problem says what is wrong, such as which accounts can also read the
	// file.
	Problem string
	// Fix is a command for the current platform that makes the file private.
	Fix string
	// Shell names the shell that Fix is written for, such as "PowerShell",
	// or is empty for a POSIX shell.
	Shell string
}

func (e *NotPrivateError) Error() string { return e.Problem }

// ExplainPrivateFileError keeps a private-file refusal wrapped while adding
// the affected path and its repair command. Other errors keep their original
// text because they already carry their operation and path where applicable.
func ExplainPrivateFileError(path string, err error) error {
	if err == nil {
		return nil
	}
	var notPrivate *NotPrivateError
	if !errors.As(err, &notPrivate) {
		return err
	}
	if notPrivate.Fix == "" {
		return fmt.Errorf("%s: %w", path, err)
	}
	return fmt.Errorf("%s: %w; to fix it, run: %s", path, err, notPrivate.Fix)
}

package state

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

package repository

import (
	"errors"
	"regexp"
	"strings"
)

var (
	validRepositoryID       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	windowsReservedBasename = regexp.MustCompile(`^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)
)

// ValidateID applies the portable repository identifier rules used by storage
// paths and offline recovery on every supported operating system.
func ValidateID(id string) error {
	if !validRepositoryID.MatchString(id) || strings.HasSuffix(id, ".git") {
		return errors.New("invalid repository ID: use 1-100 lowercase letters, numbers, dots, underscores, or hyphens and do not end in .git")
	}
	basename := strings.SplitN(strings.ToLower(id), ".", 2)[0]
	if windowsReservedBasename.MatchString(basename) {
		return errors.New("invalid repository ID: Windows device names are not portable")
	}
	return nil
}

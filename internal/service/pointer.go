package service

import (
	"fmt"
	"os"
	"strings"
)

// ReadPointer returns the state directory that the pointer file at path
// names. It returns "" and no error when the file does not exist.
func ReadPointer(path string) (string, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(content))
	if err := checkAbsolute("state directory in "+path, dir); err != nil {
		return "", fmt.Errorf("%w; run \"owngit service install\" as root again to rewrite it", err)
	}
	return dir, nil
}

package state

import "path/filepath"

// ProtectedProgram returns the file that the program at path leads to,
// after RequireProtectedPath found that no other account can change it,
// a folder above it or a link on the way. Running the returned file runs
// what was checked, whatever the path names later.
func ProtectedProgram(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := RequireProtectedPath(absolute); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

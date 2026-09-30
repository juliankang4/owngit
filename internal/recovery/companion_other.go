//go:build !darwin

package recovery

// requireAttributesInPlace accepts every folder: only macOS keeps
// attributes in companion files.
func requireAttributesInPlace(string) error { return nil }

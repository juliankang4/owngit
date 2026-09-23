//go:build !windows

package importsync

func runtimeSharingViolation(_ error) bool {
	return false
}

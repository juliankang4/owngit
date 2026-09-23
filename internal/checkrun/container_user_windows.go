//go:build windows

package checkrun

// Docker Desktop translates bind mounts independently of the Windows account.
// The fixed numeric identity keeps the Linux container process nonroot.
func containerUserForWorkspace(workspace string) (string, error) { return "65532:65532", nil }

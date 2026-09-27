//go:build !windows

package main

import "owngit/internal/state"

// requireProtectedPath checks the binary and the PATH folders of the macOS
// agent. Tests replace it, since their binaries do not exist.
var requireProtectedPath = state.RequireProtectedPath

package main

import "os"

// fileOwner has no user ID to report on Windows, where install.sh does not run.
func fileOwner(os.FileInfo) int { return -1 }

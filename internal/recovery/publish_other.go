//go:build !darwin && !linux

package recovery

// publishBackup publishes the backup stage at output with the rename that
// never replaces what exists.
func publishBackup(stage, output string) error {
	return renameNoReplace(stage, output)
}

// requireExclusiveRename accepts every folder: the rename of this system
// never replaces what exists.
func requireExclusiveRename(string) error { return nil }

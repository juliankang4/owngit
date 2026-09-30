package recovery

import "strings"

// isCompanion reports whether name, an entry of a folder, is the file
// system's companion of the entry base in the same folder, for which
// exists reports. macOS keeps the extended attributes of a file on a
// volume that cannot hold them, such as exFAT or FAT, in a companion file
// named "._" and the file's name (AppleDouble). A companion is the file
// system's, not OwnGit's: it moves and goes with its file, and a backup
// ignores it. A "._" entry without its file is not a companion.
func isCompanion(name string, exists func(base string) bool) bool {
	base, found := strings.CutPrefix(name, "._")
	return found && base != "" && exists(base)
}

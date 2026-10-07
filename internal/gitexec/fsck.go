package gitexec

// HistoricFormatWarnings are the fsck message IDs that object checks report as
// warnings instead of refusing the objects. Old Git versions wrote these
// spelling faults into public repositories; they change neither a path, a file
// type, a link nor a submodule. All other strict checks stay enabled.
//
//   - badTimezone: an author or committer offset such as +051800 (rails/rails
//     commit 4cf94979, psf/requests commit 5e6ecdad).
//   - missingSpaceBeforeDate: a tagger line without a date (30 tags in
//     coreutils/coreutils, such as v4.5.1).
//   - zeroPaddedFilemode: a directory mode written 040000 instead of 40000,
//     which still means a directory (141 trees in rails/rails). Git itself
//     only warns about it; --strict alone would refuse it.
func HistoricFormatWarnings() []string {
	return []string{"badTimezone", "missingSpaceBeforeDate", "zeroPaddedFilemode"}
}

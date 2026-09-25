package webui

// ArchiveLink downloads the selected branch, tag, or commit as one archive
// format. Label is the format's name, which is not translated.
type ArchiveLink struct {
	Label string
	URL   string
}

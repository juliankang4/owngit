package webui

// Archive download wording.
const (
	MsgArchiveDownload MessageCode = "archive.download"
	MsgArchiveLabel    MessageCode = "archive.label"
)

var archiveCatalog = map[MessageCode]message{
	MsgArchiveDownload: {en: "Download", ko: "내려받기"},
	MsgArchiveLabel:    {en: "Download these files as an archive", ko: "이 파일들을 압축 파일로 내려받기"},
}

func init() {
	for code, entry := range archiveCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

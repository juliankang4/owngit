package webui

// Archive download wording.
const (
	MsgArchiveDownload MessageCode = "archive.download"
	MsgArchiveLabel    MessageCode = "archive.label"
	// MsgArchiveRepeatedPath heads a refused download of a tree that names
	// one path twice, MsgArchiveManyFiles one whose files cannot be checked,
	// MsgArchiveDeepChain one whose packing holds a delta chain deeper than
	// Git builds, and MsgArchiveMemory one that holds a file too large to
	// rebuild here. The English wording of the same reasons is in the package
	// githttp, for the API route.
	MsgArchiveRepeatedPath MessageCode = "archive.refused.repeated_path"
	MsgArchiveManyFiles    MessageCode = "archive.refused.too_many_files"
	MsgArchiveDeepChain    MessageCode = "archive.refused.deep_chain"
	MsgArchiveMemory       MessageCode = "archive.refused.memory"
)

var archiveCatalog = map[MessageCode]message{
	MsgArchiveDownload: {en: "Download", ko: "내려받기"},
	MsgArchiveLabel:    {en: "Download these files as an archive", ko: "이 파일들을 압축 파일로 내려받기"},
	MsgArchiveRepeatedPath: {
		en: "OwnGit did not create the archive: this commit holds two entries with one path, so an archive of it would not be faithful.",
		ko: "OwnGit이 압축 파일을 만들지 않았습니다. 이 커밋에 같은 경로의 항목이 둘 있어 압축 파일이 내용을 정확히 담지 못합니다.",
	},
	MsgArchiveManyFiles: {
		en: "OwnGit did not create the archive: this commit has too many files for OwnGit to check its paths.",
		ko: "OwnGit이 압축 파일을 만들지 않았습니다. 이 커밋의 파일이 너무 많아 경로를 확인할 수 없습니다.",
	},
	MsgArchiveDeepChain: {
		en: "OwnGit did not create the archive: one object of this commit is stored in a delta chain deeper than Git builds, so the commit's files cannot be checked. Pack the repository again on a computer with enough memory, or clone it with Git instead.",
		ko: "OwnGit이 압축 파일을 만들지 않았습니다. 이 커밋의 객체 하나가 Git이 만드는 것보다 깊은 델타 체인에 저장되어 파일을 확인할 수 없습니다. 메모리가 충분한 컴퓨터에서 저장소를 다시 압축하거나 대신 Git으로 복제하세요.",
	},
	MsgArchiveMemory: {
		en: "OwnGit did not create the archive: one of its files needs more memory to rebuild than this computer gives Git. Clone the repository with Git instead, or run OwnGit on a computer with more memory.",
		ko: "OwnGit이 압축 파일을 만들지 않았습니다. 파일 하나를 다시 만드는 데 이 컴퓨터가 Git에 주는 메모리보다 더 많이 필요합니다. 대신 Git으로 저장소를 복제하거나 메모리가 더 많은 컴퓨터에서 OwnGit을 실행하세요.",
	},
}

func init() {
	for code, entry := range archiveCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

package webui

// Sidebar, file view, and diff wording.
const (
	MsgNavHome         MessageCode = "app.nav.home"
	MsgNavAllRepos     MessageCode = "app.nav.all_repositories"
	MsgNavRepoSettings MessageCode = "app.nav.repo_settings"
	MsgNavFilter       MessageCode = "app.nav.filter"
	MsgNavNoMatch      MessageCode = "app.nav.no_match"
	MsgNavMenu         MessageCode = "app.nav.menu"
	MsgNavImport       MessageCode = "app.nav.import"
	// MsgNavNewShort is the visible sidebar label of New repository. Korean
	// keeps the full name, which already fits.
	MsgNavNewShort MessageCode = "app.nav.new_short"
	MsgNavAdminOn  MessageCode = "app.nav.admin_on"
	MsgNavAdminEnd MessageCode = "app.nav.admin_end"

	MsgCodeFiles         MessageCode = "code.files"
	MsgCodePreview       MessageCode = "code.preview"
	MsgCodeSource        MessageCode = "code.source"
	MsgCodeViewAs        MessageCode = "code.view_as"
	MsgCodeWrap          MessageCode = "code.wrap"
	MsgCodeLocation      MessageCode = "code.location"
	MsgCodeNotShown      MessageCode = "code.not_rendered"
	MsgCodeBusy          MessageCode = "code.render_busy"
	MsgReadmeNotShown    MessageCode = "code.readme_not_rendered"
	MsgReadmeBusy        MessageCode = "code.readme_busy"
	MsgCodeUnavailable   MessageCode = "code.render_unavailable"
	MsgReadmeUnavailable MessageCode = "code.readme_unavailable"
	MsgCodeRawTooLarge   MessageCode = "code.raw_too_large"
	MsgCommitsToList     MessageCode = "commits.back_to_list"

	MsgDiffFiles       MessageCode = "diff.files"
	MsgDiffCollapseAll MessageCode = "diff.collapse_all"
	MsgDiffExpandAll   MessageCode = "diff.expand_all"
	MsgDiffRenamedFrom MessageCode = "diff.renamed_from"
	MsgDiffNoLines     MessageCode = "diff.no_lines"
	MsgDiffNotLoaded   MessageCode = "diff.not_loaded"
	MsgDiffOpenOne     MessageCode = "diff.open_one"
	MsgDiffMoreFiles   MessageCode = "diff.more_files"
	MsgDiffOneFile     MessageCode = "diff.one_file"
	MsgDiffShowAll     MessageCode = "diff.show_all"
)

var navigationCatalog = map[MessageCode]message{
	MsgNavHome:         {en: "Home", ko: "홈"},
	MsgNavAllRepos:     {en: "All repositories", ko: "모든 저장소"},
	MsgNavRepoSettings: {en: "Repository settings", ko: "저장소 설정"},
	MsgNavFilter:       {en: "Find a repository", ko: "저장소 찾기"},
	MsgNavNoMatch:      {en: "No repository matches.", ko: "일치하는 저장소가 없습니다."},
	MsgNavMenu:         {en: "Menu", ko: "메뉴"},
	MsgNavImport:       {en: "Import", ko: "가져오기"},
	MsgNavNewShort:     {en: "New", ko: "새 저장소"},
	MsgNavAdminOn:      {en: "Confirmed as administrator", ko: "관리자로 확인됨"},
	MsgNavAdminEnd:     {en: "End", ko: "종료"},

	MsgCodeFiles:    {en: "Files", ko: "파일 목록"},
	MsgCodePreview:  {en: "Preview", ko: "미리보기"},
	MsgCodeSource:   {en: "Source", ko: "원문"},
	MsgCodeViewAs:   {en: "Show the document as", ko: "문서 보기 방식"},
	MsgCodeWrap:     {en: "Wrap lines", ko: "줄바꿈"},
	MsgCodeLocation: {en: "Location", ko: "위치"},
	MsgCodeNotShown: {
		en: "This document is too large or too complex to show formatted, so its source is shown.",
		ko: "문서가 너무 크거나 복잡해서 서식 없이 원문으로 보여 줍니다.",
	},
	MsgCodeBusy: {
		en: "Formatted views are busy right now, so the source is shown. Reload the page to try again.",
		ko: "지금은 서식 보기가 밀려 있어 원문으로 보여 줍니다. 잠시 뒤 페이지를 새로 고치세요.",
	},
	MsgReadmeNotShown: {
		en: "This README is too large or too complex to show here. Open it to read its source.",
		ko: "이 README는 너무 크거나 복잡해서 여기에 보여 줄 수 없습니다. 파일을 열어 원문을 읽으세요.",
	},
	MsgReadmeBusy: {
		en: "Formatted views are busy right now. Reload the page to see this README, or open it to read its source.",
		ko: "지금은 서식 보기가 밀려 있습니다. 페이지를 새로 고치거나 파일을 열어 원문을 읽으세요.",
	},
	MsgCodeUnavailable: {
		en: "Formatted view is unavailable on this server, so the source is shown.",
		ko: "이 서버에서는 서식 보기를 쓸 수 없어 원문으로 보여 줍니다.",
	},
	MsgReadmeUnavailable: {
		en: "Formatted view is unavailable on this server. Open the README to read its source.",
		ko: "이 서버에서는 서식 보기를 쓸 수 없습니다. README를 열어 원문을 읽으세요.",
	},
	MsgCodeRawTooLarge: {
		en: "Files over 10 MB cannot be downloaded from the browser. Clone the repository to get this file.",
		ko: "10MB가 넘는 파일은 브라우저에서 내려받을 수 없습니다. 저장소를 복제해서 받으세요.",
	},
	MsgCommitsToList: {en: "Commits", ko: "커밋 목록"},

	MsgDiffFiles:       {en: "Changed files", ko: "변경 파일"},
	MsgDiffCollapseAll: {en: "Collapse all", ko: "모두 접기"},
	MsgDiffExpandAll:   {en: "Expand all", ko: "모두 펼치기"},
	MsgDiffRenamedFrom: {en: "Previously", ko: "이전 이름"},
	MsgDiffNoLines:     {en: "No line changes to show.", ko: "보여 줄 줄 변경이 없습니다."},
	MsgDiffNotLoaded: {
		en: "This file's changes were not loaded, because the change is too large to show on one page.",
		ko: "변경이 너무 커서 이 파일의 변경 내용은 한 페이지에 불러오지 않았습니다.",
	},
	MsgDiffOpenOne: {en: "Show this file's changes", ko: "이 파일의 변경 내용 보기"},
	MsgDiffOneFile: {en: "Only this file's changes are shown.", ko: "이 파일의 변경 내용만 보여 줍니다."},
	MsgDiffShowAll: {en: "Show every changed file", ko: "변경된 파일 모두 보기"},
	// %d is the number of files not listed yet.
	MsgDiffMoreFiles: {en: "Show %d more files", ko: "나머지 %d개 파일 보기"},
}

func init() {
	for code, entry := range navigationCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

package webui

// Pull request description, edit and review note wording.
const (
	MsgPRBodyField       MessageCode = "pr.text.body_field"
	MsgPRBodyHelp        MessageCode = "pr.text.body_help"
	MsgPRNoDescription   MessageCode = "pr.text.no_description"
	MsgPRTextPlain       MessageCode = "pr.text.plain"
	MsgPRInvalidBody     MessageCode = "pr.text.invalid_body"
	MsgPREditedAt        MessageCode = "pr.edit.edited_at"
	MsgPREditOpen        MessageCode = "pr.edit.open"
	MsgPREditSave        MessageCode = "pr.edit.save"
	MsgPREditSaved       MessageCode = "pr.edit.saved"
	MsgPREditStale       MessageCode = "pr.edit.stale"
	MsgPRNotesTitle      MessageCode = "pr.notes.title"
	MsgPRNotesEarlier    MessageCode = "pr.notes.earlier"
	MsgPRNotesTruncated  MessageCode = "pr.notes.truncated"
	MsgPRReviewOpen      MessageCode = "pr.review.open"
	MsgPRReviewDecision  MessageCode = "pr.review.decision"
	MsgPRReviewApprove   MessageCode = "pr.review.approve"
	MsgPRReviewChanges   MessageCode = "pr.review.request_changes"
	MsgPRReviewerHelp    MessageCode = "pr.review.reviewer_help"
	MsgPRReviewNote      MessageCode = "pr.review.note"
	MsgPRReviewNoteHelp  MessageCode = "pr.review.note_help"
	MsgPRReviewSubmit    MessageCode = "pr.review.submit"
	MsgPRReviewRecorded  MessageCode = "pr.review.recorded"
	MsgPRInvalidNote     MessageCode = "pr.review.invalid_note"
	MsgPRInvalidReviewer MessageCode = "pr.review.invalid_reviewer"
	MsgPRInvalidDecision MessageCode = "pr.review.invalid_decision"
)

var pullRequestTextCatalog = map[MessageCode]message{
	MsgPRBodyField: {en: "Description", ko: "설명"},
	MsgPRBodyHelp: {
		en: "Optional. Markdown formatting works; images and HTML are not shown. Up to 64 KiB.",
		ko: "선택 사항입니다. 마크다운 서식을 쓸 수 있으며 이미지와 HTML은 표시하지 않습니다. 최대 64KiB까지 쓸 수 있습니다.",
	},
	MsgPRNoDescription: {en: "No description.", ko: "설명이 없습니다."},
	MsgPRTextPlain: {
		en: "Shown as written, because OwnGit could not format it right now.",
		ko: "지금은 서식을 적용하지 못해 입력한 그대로 보여 줍니다.",
	},
	MsgPRInvalidBody: {
		en: "The description can hold up to 64 KiB of text.",
		ko: "설명은 최대 64KiB까지 쓸 수 있습니다.",
	},
	MsgPREditedAt:  {en: "Edited", ko: "수정"},
	MsgPREditOpen:  {en: "Edit title and description", ko: "제목과 설명 수정"},
	MsgPREditSave:  {en: "Save", ko: "저장"},
	MsgPREditSaved: {en: "Title and description saved.", ko: "제목과 설명을 저장했습니다."},
	MsgPREditStale: {
		en: "Someone edited this pull request after you opened it. Their text is shown above and yours is kept in the form. Save again to replace theirs.",
		ko: "이 페이지를 연 뒤에 다른 곳에서 풀 리퀘스트를 수정했습니다. 수정된 내용은 위에 있고, 입력한 내용은 양식에 그대로 두었습니다. 다시 저장하면 입력한 내용으로 바뀝니다.",
	},
	MsgPRNotesTitle:   {en: "Review notes", ko: "리뷰 메모"},
	MsgPRNotesEarlier: {en: "About earlier commits", ko: "이전 커밋 기준"},
	MsgPRNotesTruncated: {
		en: "Older review notes are kept but not shown here.",
		ko: "이전 리뷰 메모는 보관되어 있지만 여기에는 표시하지 않습니다.",
	},
	MsgPRReviewOpen:     {en: "Record a review", ko: "리뷰 결과 기록"},
	MsgPRReviewDecision: {en: "Result", ko: "결과"},
	MsgPRReviewApprove:  {en: "Approve", ko: "승인"},
	MsgPRReviewChanges:  {en: "Request changes", ko: "변경 요청"},
	MsgPRReviewerHelp: {
		en: "Who reviewed, for example your name or a tool. OwnGit records it as you enter it.",
		ko: "리뷰한 사람이나 도구의 이름입니다. OwnGit은 입력한 그대로 기록합니다.",
	},
	MsgPRReviewNote: {en: "Note", ko: "메모"},
	MsgPRReviewNoteHelp: {
		en: "Optional. Markdown, up to 64 KiB. It stays tied to the current commits of both branches.",
		ko: "선택 사항입니다. 마크다운으로 최대 64KiB까지 쓸 수 있으며, 두 브랜치의 현재 커밋에 묶여 기록됩니다.",
	},
	MsgPRReviewSubmit:   {en: "Record review", ko: "리뷰 기록"},
	MsgPRReviewRecorded: {en: "Review recorded.", ko: "리뷰를 기록했습니다."},
	MsgPRInvalidNote: {
		en: "The note can hold up to 64 KiB of text.",
		ko: "메모는 최대 64KiB까지 쓸 수 있습니다.",
	},
	MsgPRInvalidReviewer: {
		en: "Enter who reviewed, 1 to 200 characters on one line.",
		ko: "리뷰한 사람이나 도구를 한 줄로 1자에서 200자 사이로 입력하세요.",
	},
	MsgPRInvalidDecision: {en: "Choose Approve or Request changes.", ko: "승인이나 변경 요청 중 하나를 고르세요."},
}

func init() {
	for code, entry := range pullRequestTextCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

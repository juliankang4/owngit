package webui

// Strings for share links: the administrator's Share links screen and the
// pages a visitor with a link sees.

const (
	MsgShareTitle          MessageCode = "share.title"
	MsgShareIntro          MessageCode = "share.intro"
	MsgShareSettingsLine   MessageCode = "repoadmin.pages.share"
	MsgShareCreate         MessageCode = "share.create"
	MsgShareLabel          MessageCode = "share.label"
	MsgShareLabelHelp      MessageCode = "share.label_help"
	MsgShareScope          MessageCode = "share.scope"
	MsgShareScopeBrowse    MessageCode = "share.scope.browse"
	MsgShareScopeClone     MessageCode = "share.scope.clone"
	MsgShareExpiry         MessageCode = "share.expiry"
	MsgShareExpiry1        MessageCode = "share.expiry.1"
	MsgShareExpiry7        MessageCode = "share.expiry.7"
	MsgShareExpiry30       MessageCode = "share.expiry.30"
	MsgShareExpiry90       MessageCode = "share.expiry.90"
	MsgShareExpiryNever    MessageCode = "share.expiry.never"
	MsgSharePassword       MessageCode = "share.password"
	MsgSharePasswordHelp   MessageCode = "share.password_help"
	MsgShareSubmit         MessageCode = "share.submit"
	MsgShareCreatedTitle   MessageCode = "share.created.title"
	MsgShareCreatedOnce    MessageCode = "share.created.once"
	MsgShareCreatedLink    MessageCode = "share.created.link"
	MsgShareCreatedClone   MessageCode = "share.created.clone"
	MsgShareWarnNever      MessageCode = "share.warn.never"
	MsgShareWarnClone      MessageCode = "share.warn.clone"
	MsgShareWarnNoPassword MessageCode = "share.warn.no_password"
	MsgShareListTitle      MessageCode = "share.list.title"
	MsgShareNone           MessageCode = "share.none"
	MsgShareActive         MessageCode = "share.state.active"
	MsgShareExpired        MessageCode = "share.state.expired"
	MsgShareRevoked        MessageCode = "share.state.revoked"
	MsgShareID             MessageCode = "share.id"
	MsgShareCreatedAt      MessageCode = "share.created_at"
	MsgShareExpiresAt      MessageCode = "share.expires_at"
	MsgShareUntilRevoked   MessageCode = "share.until_revoked"
	MsgShareLastUsed       MessageCode = "share.last_used"
	MsgShareNeverUsed      MessageCode = "share.never_used"
	MsgShareRevokedAt      MessageCode = "share.revoked_at"
	MsgShareHasPassword    MessageCode = "share.has_password"
	MsgShareRevoke         MessageCode = "share.revoke"
	MsgShareRevokeHelp     MessageCode = "share.revoke_help"
	MsgShareCreatedNotice  MessageCode = "share.result.created"
	MsgShareRevokedNotice  MessageCode = "share.result.revoked"
	MsgShareLabelInvalid   MessageCode = "share.error.label"
	MsgSharePasswordRule   MessageCode = "share.error.password"
	MsgShareChoiceInvalid  MessageCode = "share.error.choice"
	MsgShareNotActive      MessageCode = "share.error.not_active"
	MsgShareFailed         MessageCode = "share.error.failed"
	MsgShareUnreadable     MessageCode = "share.error.unreadable"

	// A visitor's pages.
	MsgShareLinkNotFound      MessageCode = "share.visitor.not_found"
	MsgShareCloneHelp         MessageCode = "share.clone_help"
	MsgShareCloneHelpPassword MessageCode = "share.clone_help_password"
	MsgSharePasswordTitle     MessageCode = "share.visitor.password_title"
	MsgSharePasswordBody      MessageCode = "share.visitor.password_body"
	MsgSharePasswordField     MessageCode = "share.visitor.password_field"
	MsgSharePasswordSubmit    MessageCode = "share.visitor.password_submit"
	MsgSharePasswordWrong     MessageCode = "share.visitor.password_wrong"
	MsgSharePasswordLocked    MessageCode = "share.visitor.password_locked"
)

var shareCatalog = map[MessageCode]message{
	MsgShareTitle: {en: "Share links", ko: "공유 링크"},
	MsgShareIntro: {
		en: "A share link lets someone without an account read this repository: its files, the history of its branches and tags, and its README. Anyone who has the link can use it until it expires or you revoke it. Pull requests, checks, kept history, settings and other repositories are never shown.",
		ko: "공유 링크가 있으면 계정 없이도 이 저장소를 읽을 수 있습니다. 파일, 브랜치와 태그의 기록, README를 볼 수 있습니다. 링크가 만료되거나 폐기하기 전까지는 링크를 가진 누구나 쓸 수 있습니다. 풀 리퀘스트, 체크, 보관된 기록, 설정, 다른 저장소는 보이지 않습니다.",
	},
	MsgShareSettingsLine: {
		en: "Links that let someone without an account read this repository.",
		ko: "계정 없이 이 저장소를 읽을 수 있게 하는 링크입니다.",
	},
	MsgShareCreate:    {en: "Create a share link", ko: "공유 링크 만들기"},
	MsgShareLabel:     {en: "Name", ko: "이름"},
	MsgShareLabelHelp: {en: "Who the link is for, such as a person or a company. Only administrators see it.", ko: "누구에게 주는 링크인지 적습니다. 예를 들어 사람이나 회사 이름을 씁니다. 관리자만 볼 수 있습니다."},
	MsgShareScope:     {en: "Access", ko: "권한"},
	MsgShareScopeBrowse: {
		en: "Browse files and history",
		ko: "파일과 기록 보기",
	},
	MsgShareScopeClone: {
		en: "Browse, and clone with Git",
		ko: "보기와 Git 복제",
	},
	MsgShareExpiry:       {en: "Expires", ko: "만료"},
	MsgShareExpiry1:      {en: "After 1 day", ko: "1일 뒤"},
	MsgShareExpiry7:      {en: "After 7 days", ko: "7일 뒤"},
	MsgShareExpiry30:     {en: "After 30 days", ko: "30일 뒤"},
	MsgShareExpiry90:     {en: "After 90 days", ko: "90일 뒤"},
	MsgShareExpiryNever:  {en: "Until revoked", ko: "폐기할 때까지"},
	MsgSharePassword:     {en: "Extra password (optional)", ko: "추가 비밀번호 (선택)"},
	MsgSharePasswordHelp: {en: "Visitors type it before they see anything. Leave it empty for a link that opens without one.", ko: "방문자는 이 비밀번호를 입력해야 내용을 볼 수 있습니다. 비워 두면 비밀번호 없이 열리는 링크가 됩니다."},
	MsgShareSubmit:       {en: "Create link", ko: "링크 만들기"},
	MsgShareCreatedTitle: {en: "Copy the link now", ko: "지금 링크를 복사하세요"},
	MsgShareCreatedOnce: {
		en: "This is the only time OwnGit shows this link. It keeps only a fingerprint of the link's secret, so it cannot show the link again. If it is lost, create a new link and revoke this one.",
		ko: "이 링크는 지금 한 번만 볼 수 있습니다. OwnGit은 링크 비밀값의 지문만 보관하므로 다시 보여 줄 수 없습니다. 링크를 잃어버리면 새 링크를 만들고 이 링크는 폐기하세요.",
	},
	MsgShareCreatedLink:  {en: "Share link", ko: "공유 링크"},
	MsgShareCreatedClone: {en: "Git address", ko: "Git 주소"},
	MsgShareWarnNever: {
		en: "This link works until you revoke it.",
		ko: "이 링크는 폐기할 때까지 계속 작동합니다.",
	},
	MsgShareWarnClone: {
		en: "Anyone with this link can copy the whole history of the branches and tags with Git. Revoking the link later does not take back a copy.",
		ko: "이 링크를 가진 사람은 브랜치와 태그의 전체 기록을 Git으로 복사할 수 있습니다. 나중에 링크를 폐기해도 이미 가져간 복사본은 되돌릴 수 없습니다.",
	},
	MsgShareWarnNoPassword: {
		en: "Anyone who gets this link can open it. It has no extra password.",
		ko: "이 링크를 받은 사람은 누구나 열 수 있습니다. 추가 비밀번호가 없습니다.",
	},
	MsgShareListTitle:    {en: "Links", ko: "링크"},
	MsgShareNone:         {en: "No share links yet.", ko: "아직 공유 링크가 없습니다."},
	MsgShareActive:       {en: "Active", ko: "사용 중"},
	MsgShareExpired:      {en: "Expired", ko: "만료됨"},
	MsgShareRevoked:      {en: "Revoked", ko: "폐기됨"},
	MsgShareID:           {en: "Visitor pages", ko: "방문자 페이지"},
	MsgShareCreatedAt:    {en: "Created", ko: "만든 때"},
	MsgShareExpiresAt:    {en: "Expires", ko: "만료"},
	MsgShareUntilRevoked: {en: "Until revoked", ko: "폐기할 때까지"},
	MsgShareLastUsed:     {en: "Last used", ko: "마지막 사용"},
	MsgShareNeverUsed:    {en: "Never used", ko: "사용한 적 없음"},
	MsgShareRevokedAt:    {en: "Revoked", ko: "폐기한 때"},
	MsgShareHasPassword:  {en: "Extra password", ko: "추가 비밀번호"},
	MsgShareRevoke:       {en: "Revoke", ko: "폐기"},
	MsgShareRevokeHelp:   {en: "The link stops working at once.", ko: "링크가 바로 작동을 멈춥니다."},
	MsgShareCreatedNotice: {
		en: "Share link created.",
		ko: "공유 링크를 만들었습니다.",
	},
	MsgShareRevokedNotice: {
		en: "Share link revoked. It no longer opens this repository.",
		ko: "공유 링크를 폐기했습니다. 이제 이 링크로는 저장소를 열 수 없습니다.",
	},
	MsgShareLabelInvalid: {
		en: "Give the link a name on one line, at most 100 bytes.",
		ko: "링크 이름을 한 줄로, 100바이트 이하로 적으세요.",
	},
	MsgSharePasswordRule: {
		en: "An extra password needs at least 8 characters and at most 1024.",
		ko: "추가 비밀번호는 8자 이상 1024자 이하여야 합니다.",
	},
	MsgShareChoiceInvalid: {
		en: "Choose an access and an expiry from the lists.",
		ko: "목록에서 권한과 만료를 고르세요.",
	},
	MsgShareNotActive: {
		en: "This link was not found or is already revoked. The list shows the links as they are now.",
		ko: "이 링크를 찾지 못했거나 이미 폐기되었습니다. 목록은 지금 상태를 보여 줍니다.",
	},
	MsgShareFailed: {
		en: "The share link could not be saved. The OwnGit log says why. Try again.",
		ko: "공유 링크를 저장하지 못했습니다. 이유는 OwnGit 로그에 있습니다. 다시 시도하세요.",
	},
	MsgShareUnreadable: {
		en: "The share links could not be read. The OwnGit log says why.",
		ko: "공유 링크를 읽지 못했습니다. 이유는 OwnGit 로그에 있습니다.",
	},
	MsgShareLinkNotFound: {
		en: "This share link does not open anything. It may have expired or been revoked, or the address may be incomplete.",
		ko: "이 공유 링크로는 열 수 있는 것이 없습니다. 만료되었거나 폐기되었을 수 있고, 주소가 잘렸을 수도 있습니다.",
	},
	MsgShareCloneHelp: {
		en: "When Git asks for a password, enter the part of your share link after /share/. Any user name works.",
		ko: "Git이 비밀번호를 물으면 공유 링크에서 /share/ 뒤의 부분을 입력하세요. 사용자 이름은 아무거나 써도 됩니다.",
	},
	MsgShareCloneHelpPassword: {
		en: "When Git asks for a user name, enter the part of your share link after /share/. For the password, enter the link's password.",
		ko: "Git이 사용자 이름을 물으면 공유 링크에서 /share/ 뒤의 부분을 입력하세요. 비밀번호에는 링크의 비밀번호를 입력하세요.",
	},
	MsgSharePasswordTitle: {en: "This link needs a password", ko: "이 링크에는 비밀번호가 필요합니다"},
	MsgSharePasswordBody: {
		en: "The person who shared this repository gave the link a password.",
		ko: "저장소를 공유한 사람이 이 링크에 비밀번호를 걸어 두었습니다.",
	},
	MsgSharePasswordField:  {en: "Password", ko: "비밀번호"},
	MsgSharePasswordSubmit: {en: "Open", ko: "열기"},
	MsgSharePasswordWrong:  {en: "Wrong password.", ko: "비밀번호가 틀렸습니다."},
	MsgSharePasswordLocked: {
		en: "Too many wrong passwords from this address. Try again after",
		ko: "이 주소에서 틀린 비밀번호가 너무 많았습니다. 다음 시각 이후에 다시 시도하세요:",
	},
}

func init() {
	for code, entry := range shareCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

package webui

// Strings for the repository overview (variant A), the administrator Settings
// tab, and the delete confirmation. They live in their own catalog so they
// merge without touching the shared list in messages.go.

const (
	// Tab strip.
	MsgRepoSettingsTab MessageCode = "repoadmin.settings_tab"
	MsgRepoDeleteTab   MessageCode = "repoadmin.delete_tab"
	// MsgRepoAdminNeeded is the hidden words beside the lock shape on an
	// entry point that opens the administrator login first.
	MsgRepoAdminNeeded     MessageCode = "repoadmin.admin_needed"
	MsgRepoAdminNeededHint MessageCode = "repoadmin.admin_needed_hint"

	// Overview.
	MsgRepoCloneAddress   MessageCode = "repo.overview.clone_address"
	MsgRepoCopy           MessageCode = "repo.overview.copy"
	MsgRepoCopied         MessageCode = "repo.overview.copied"
	MsgRepoCopyFailed     MessageCode = "repo.overview.copy_failed"
	MsgRepoSummaryLabel   MessageCode = "repo.overview.summary_label"
	MsgRepoFactDefault    MessageCode = "repo.overview.default_branch"
	MsgRepoFactNoDefault  MessageCode = "repo.overview.no_default_branch"
	MsgRepoFactLatest     MessageCode = "repo.overview.last_commit"
	MsgRepoFactOpenPRs    MessageCode = "repo.overview.open_pull_requests"
	MsgRepoFactCheck      MessageCode = "repo.overview.default_check"
	MsgRepoFactUnreadable MessageCode = "repo.overview.unreadable"
	MsgRepoRecentCommits  MessageCode = "repo.overview.recent_commits"
	MsgRepoAllCommits     MessageCode = "repo.overview.all_commits"
	MsgRepoSideLabel      MessageCode = "repo.overview.side_label"
	MsgRepoShowAllRefs    MessageCode = "repo.overview.show_all_refs"
	MsgRepoShowFewerRefs  MessageCode = "repo.overview.show_fewer_refs"
	MsgRepoNewestShown    MessageCode = "repo.overview.newest_shown"
	MsgRepoTipsUnreadable MessageCode = "repo.overview.tips_unreadable"
	MsgRepoNoTagsYet      MessageCode = "repo.overview.no_tags_yet"
	// Languages panel.
	MsgRepoLanguagesTitle       MessageCode = "repo.languages.title"
	MsgRepoLanguagesBarLabel    MessageCode = "repo.languages.bar_label"
	MsgRepoLanguagesOther       MessageCode = "repo.languages.other"
	MsgRepoLanguagesNone        MessageCode = "repo.languages.none"
	MsgRepoLanguagesTooLarge    MessageCode = "repo.languages.too_large"
	MsgRepoLanguagesUnavailable MessageCode = "repo.languages.unavailable"
	MsgRepoLanguagesNoAttrs     MessageCode = "repo.languages.attributes_ignored"
	MsgRepoLanguagesAttrsFailed MessageCode = "repo.languages.attributes_unreadable"
	MsgRepoLanguagesSlow        MessageCode = "repo.languages.slow"
	MsgRepoLanguagesBusy        MessageCode = "repo.languages.busy"
	MsgRepoRestoreLead          MessageCode = "repo.overview.restore_lead"
	MsgRepoRestoreStart         MessageCode = "repo.overview.restore_start"

	// Settings tab.
	MsgRepoSettingsTitle           MessageCode = "repoadmin.settings.title"
	MsgRepoSettingsIntro           MessageCode = "repoadmin.settings.intro"
	MsgRepoDefaultBranchTitle      MessageCode = "repoadmin.default_branch.title"
	MsgRepoDefaultBranchHelp       MessageCode = "repoadmin.default_branch.help"
	MsgRepoDefaultBranchLabel      MessageCode = "repoadmin.default_branch.label"
	MsgRepoDefaultBranchCurrent    MessageCode = "repoadmin.default_branch.current"
	MsgRepoDefaultBranchMissing    MessageCode = "repoadmin.default_branch.missing"
	MsgRepoDefaultBranchChoose     MessageCode = "repoadmin.default_branch.choose"
	MsgRepoDefaultBranchNone       MessageCode = "repoadmin.default_branch.none"
	MsgRepoDefaultBranchSave       MessageCode = "repoadmin.default_branch.save"
	MsgRepoDefaultBranchSaved      MessageCode = "repoadmin.default_branch.saved"
	MsgRepoDefaultBranchUnknown    MessageCode = "repoadmin.default_branch.unknown"
	MsgRepoDefaultBranchFailed     MessageCode = "repoadmin.default_branch.failed"
	MsgRepoHistoryTitle            MessageCode = "repoadmin.history.title"
	MsgRepoHistoryHelp             MessageCode = "repoadmin.history.help"
	MsgRepoHistoryUnreadable       MessageCode = "repoadmin.history.unreadable"
	MsgRepoHistorySave             MessageCode = "repoadmin.history.save"
	MsgRepoHistorySaved            MessageCode = "repoadmin.history.saved"
	MsgRepoHistoryKeptOff          MessageCode = "repoadmin.history.kept_off"
	MsgRepoHistoryProtectOff       MessageCode = "repoadmin.history.protect_off"
	MsgRepoHistoryFailed           MessageCode = "repoadmin.history.failed"
	MsgRepoHistoryServerUnreadable MessageCode = "repoadmin.history.server_unreadable"
	MsgRepoKeptLabel               MessageCode = "repoadmin.kept.label"
	MsgRepoKeptHelp                MessageCode = "repoadmin.kept.help"
	MsgRepoKeptDefaultOn           MessageCode = "repoadmin.kept.default_on"
	MsgRepoKeptDefaultOff          MessageCode = "repoadmin.kept.default_off"
	MsgRepoKeptDefaultUnknown      MessageCode = "repoadmin.kept.default_unknown"
	MsgRepoProtectLabel            MessageCode = "repoadmin.protect.label"
	MsgRepoProtectHelp             MessageCode = "repoadmin.protect.help"
	MsgRepoProtectOffWarning       MessageCode = "repoadmin.protect.off_warning"
	MsgRepoBusy                    MessageCode = "repoadmin.busy"
	MsgRepoBusyImport              MessageCode = "repoadmin.busy.import"
	MsgRepoBusyCheck               MessageCode = "repoadmin.busy.check"
	MsgRepoBusyCheckCleanup        MessageCode = "repoadmin.busy.check_cleanup"
	MsgRepoBusyInUse               MessageCode = "repoadmin.busy.in_use"
	MsgRepoBusyPreparing           MessageCode = "repoadmin.busy.preparing"
	MsgRepoBusyBackup              MessageCode = "repoadmin.busy.backup"
	MsgRepoRenameTitle             MessageCode = "repoadmin.rename.title"
	MsgRepoRenameHelp              MessageCode = "repoadmin.rename.help"
	MsgRepoRenameAddress           MessageCode = "repoadmin.rename.address"
	MsgRepoRenameLabel             MessageCode = "repoadmin.rename.label"
	MsgRepoRenameAliasNote         MessageCode = "repoadmin.rename.alias_note"
	MsgRepoRenameAliases           MessageCode = "repoadmin.rename.aliases"
	MsgRepoRenameAliasUntil        MessageCode = "repoadmin.rename.alias_until"
	MsgRepoRenameSubmit            MessageCode = "repoadmin.rename.submit"
	MsgRepoRenamed                 MessageCode = "repoadmin.rename.done"
	MsgRepoRenameTaken             MessageCode = "repoadmin.rename.taken"
	MsgRepoRenameFailed            MessageCode = "repoadmin.rename.failed"
	MsgRepoAdminPagesTitle         MessageCode = "repoadmin.pages.title"
	MsgRepoAdminChecksLine         MessageCode = "repoadmin.pages.checks"
	MsgRepoAdminRunnersLine        MessageCode = "repoadmin.pages.runners"
	MsgRepoAdminHelpersLine        MessageCode = "repoadmin.pages.helpers"
	MsgRepoAdminImportLine         MessageCode = "repoadmin.pages.import"
	MsgRepoAdminDeleteLine         MessageCode = "repoadmin.pages.delete"

	// Delete confirmation.
	MsgRepoDeleteTitle         MessageCode = "repoadmin.delete.title"
	MsgRepoDeleteLead          MessageCode = "repoadmin.delete.lead"
	MsgRepoDeleteChoose        MessageCode = "repoadmin.delete.choose"
	MsgRepoDeleteKeepTitle     MessageCode = "repoadmin.delete.keep.title"
	MsgRepoDeleteKeepDesc      MessageCode = "repoadmin.delete.keep.desc"
	MsgRepoDeleteKeepBack      MessageCode = "repoadmin.delete.keep.back"
	MsgRepoDeleteFilesTitle    MessageCode = "repoadmin.delete.files.title"
	MsgRepoDeleteFilesDesc     MessageCode = "repoadmin.delete.files.desc"
	MsgRepoDeleteFilesWarn     MessageCode = "repoadmin.delete.files.warn"
	MsgRepoDeleteGitPath       MessageCode = "repoadmin.delete.git_path"
	MsgRepoDeleteRemovedPath   MessageCode = "repoadmin.delete.removed_path"
	MsgRepoDeleteNameHelp      MessageCode = "repoadmin.delete.name.help"
	MsgRepoDeleteSubmit        MessageCode = "repoadmin.delete.submit"
	MsgRepoDeleteModeRequired  MessageCode = "repoadmin.delete.mode_required"
	MsgRepoDeleteNameMismatch  MessageCode = "repoadmin.delete.name_mismatch"
	MsgRepoDeleteGone          MessageCode = "repoadmin.delete.gone"
	MsgRepoDeleteFailed        MessageCode = "repoadmin.delete.failed"
	MsgRepoRemovedKept         MessageCode = "repoadmin.removed.kept"
	MsgRepoRemovedKeptAt       MessageCode = "repoadmin.removed.kept_at"
	MsgRepoRemovedKeptRecovery MessageCode = "repoadmin.removed.kept_recovery"
	MsgRepoRemovedKeptCommand  MessageCode = "repoadmin.removed.kept_command"
	MsgRepoRemovedKeptWhere    MessageCode = "repoadmin.removed.kept_where"
	MsgRepoRemovedDeleted      MessageCode = "repoadmin.removed.deleted"
	MsgRepoRemovedIncomplete   MessageCode = "repoadmin.removed.incomplete"
	MsgRepoRemovedCleanupLater MessageCode = "repoadmin.removed.cleanup_later"
	MsgRepoRemovedGeneric      MessageCode = "repoadmin.removed.generic"

	// Pages the Settings tab lists, pointing back to it.
	MsgRepoSettingsListed MessageCode = "repoadmin.settings.listed"
)

var repositoryAdminCatalog = map[MessageCode]message{
	MsgRepoSettingsTab:     {en: "Settings", ko: "설정"},
	MsgRepoDeleteTab:       {en: "Delete repository", ko: "저장소 삭제"},
	MsgRepoAdminNeeded:     {en: "(asks for the administrator password)", ko: "(관리자 비밀번호를 묻습니다)"},
	MsgRepoAdminNeededHint: {en: "Asks for the administrator password", ko: "관리자 비밀번호를 묻습니다"},

	MsgRepoCloneAddress:      {en: "Clone address", ko: "클론 주소"},
	MsgRepoCopy:              {en: "Copy", ko: "복사"},
	MsgRepoCopied:            {en: "Copied", ko: "복사함"},
	MsgRepoCopyFailed:        {en: "Select the address to copy it", ko: "주소를 선택해 복사하세요"},
	MsgRepoSummaryLabel:      {en: "Repository summary", ko: "저장소 요약"},
	MsgRepoFactDefault:       {en: "Default branch", ko: "기본 브랜치"},
	MsgRepoFactNoDefault:     {en: "Not set", ko: "정해지지 않음"},
	MsgRepoFactLatest:        {en: "Last commit", ko: "마지막 커밋"},
	MsgRepoFactOpenPRs:       {en: "Open pull requests", ko: "열린 풀 리퀘스트"},
	MsgRepoFactCheck:         {en: "Latest check on the default branch", ko: "기본 브랜치 최근 체크"},
	MsgRepoFactUnreadable:    {en: "Could not be read", ko: "읽지 못함"},
	MsgRepoRecentCommits:     {en: "Recent commits", ko: "최근 커밋"},
	MsgRepoAllCommits:        {en: "All commits", ko: "커밋 전체 보기"},
	MsgRepoSideLabel:         {en: "Branches and history", ko: "브랜치와 기록"},
	MsgRepoShowAllRefs:       {en: "Show all", ko: "모두 보기"},
	MsgRepoShowFewerRefs:     {en: "Show newest only", ko: "최신만 보기"},
	MsgRepoNewestShown:       {en: "Newest first", ko: "최신순"},
	MsgRepoTipsUnreadable:    {en: "Latest commits could not be read, so the list is by name", ko: "최근 커밋을 읽지 못해 이름순으로 보여 줍니다"},
	MsgRepoNoTagsYet:         {en: "None yet", ko: "아직 없음"},
	MsgRepoLanguagesTitle:    {en: "Languages", ko: "언어"},
	MsgRepoLanguagesBarLabel: {en: "Language shares", ko: "언어 비율"},
	MsgRepoLanguagesOther:    {en: "Other", ko: "기타"},
	// The short notes sit beside the heading, like the Tags panel's empty
	// state, instead of a share that could be wrong.
	MsgRepoLanguagesNone:        {en: "No languages detected", ko: "감지된 언어가 없습니다"},
	MsgRepoLanguagesTooLarge:    {en: "Too many files to count", ko: "파일이 너무 많아 세지 않았습니다"},
	MsgRepoLanguagesUnavailable: {en: "Not counted right now", ko: "지금은 세지 못했습니다"},
	MsgRepoLanguagesBusy:        {en: "The repository is in use. It will be counted on a later visit.", ko: "저장소가 사용 중이라 다음 방문 때 셉니다"},
	MsgRepoLanguagesSlow:        {en: "Counting took too long. It will be tried again in a few minutes.", ko: "세는 데 시간이 오래 걸려 몇 분 뒤 다시 셉니다"},
	MsgRepoLanguagesAttrsFailed: {
		en: "Language settings in .gitattributes could not be read, so they were not applied.",
		ko: ".gitattributes의 언어 설정을 읽지 못해 반영하지 않았습니다.",
	},
	MsgRepoLanguagesNoAttrs: {
		en: "Language settings in .gitattributes were not applied. They need Git 2.40 or newer on the OwnGit host.",
		ko: ".gitattributes의 언어 설정은 반영하지 않았습니다. OwnGit 호스트에 Git 2.40 이상이 필요합니다.",
	},
	MsgRepoRestoreLead: {
		en: "Bring back deleted or overwritten work from an earlier commit.",
		ko: "지운 파일이나 덮어쓴 작업을 예전 커밋에서 되살립니다.",
	},
	MsgRepoRestoreStart: {en: "Start a restore", ko: "되돌리기 시작"},

	MsgRepoSettingsTitle: {en: "Repository settings", ko: "저장소 설정"},
	MsgRepoSettingsListed: {
		en: "The default branch and this repository's other administrator pages are in its Settings tab:",
		ko: "기본 브랜치와 이 저장소의 다른 관리자 화면은 설정 탭에 있습니다:",
	},
	MsgRepoSettingsIntro: {
		en: "Settings for this repository. Only administrators can open this tab.",
		ko: "이 저장소의 설정입니다. 관리자만 이 탭을 열 수 있습니다.",
	},
	MsgRepoDefaultBranchTitle: {en: "Default branch", ko: "기본 브랜치"},
	MsgRepoDefaultBranchHelp: {
		en: "OwnGit shows this branch first, and a new clone checks it out. Only existing branches can be chosen.",
		ko: "OwnGit이 먼저 보여 주고 새로 클론하면 받게 되는 브랜치입니다. 이미 있는 브랜치만 고를 수 있습니다.",
	},
	MsgRepoDefaultBranchLabel:   {en: "Branch", ko: "브랜치"},
	MsgRepoDefaultBranchCurrent: {en: "Now:", ko: "지금:"},
	MsgRepoDefaultBranchMissing: {
		en: "The default branch is set to a branch that does not exist. Choose an existing branch below. Currently set to:",
		ko: "기본 브랜치로 정한 브랜치가 저장소에 없습니다. 아래에서 기존 브랜치를 고르세요. 지금 정해진 이름:",
	},
	MsgRepoDefaultBranchChoose: {en: "Choose a branch", ko: "브랜치를 고르세요"},
	MsgRepoDefaultBranchNone: {
		en: "This repository has no branches yet. Push a branch first.",
		ko: "아직 브랜치가 없습니다. 먼저 브랜치를 푸시하세요.",
	},
	MsgRepoDefaultBranchSave:  {en: "Set as default branch", ko: "기본 브랜치로 정하기"},
	MsgRepoDefaultBranchSaved: {en: "Default branch changed.", ko: "기본 브랜치를 바꿨습니다."},
	MsgRepoDefaultBranchUnknown: {
		en: "That branch does not exist. Choose one from the list.",
		ko: "그런 브랜치가 없습니다. 목록에서 고르세요.",
	},
	MsgRepoDefaultBranchFailed: {
		en: "The default branch could not be changed. Nothing was changed.",
		ko: "기본 브랜치를 바꾸지 못했습니다. 바뀐 것은 없습니다.",
	},
	MsgRepoHistoryTitle: {en: "History and default branch protection", ko: "기록 보관과 기본 브랜치 보호"},
	MsgRepoHistoryHelp: {
		en: "What OwnGit does when a push or an import overwrites or deletes a branch or tag in this repository. A change applies to pushes and imports that start after you save.",
		ko: "이 저장소에서 푸시나 가져오기로 브랜치나 태그를 덮어쓰거나 지울 때 OwnGit이 어떻게 할지 정합니다. 저장한 뒤 시작하는 푸시와 가져오기부터 적용됩니다.",
	},
	MsgRepoHistoryUnreadable: {
		en: "The saved choices of this repository cannot be read, so pushes and imports to it are refused until you save them again. The form shows the defaults.",
		ko: "이 저장소에 저장된 선택을 읽을 수 없어, 다시 저장할 때까지 이 저장소로 오는 푸시와 가져오기를 거부합니다. 양식에는 기본값이 보입니다.",
	},
	MsgRepoHistorySave:  {en: "Save", ko: "저장"},
	MsgRepoHistorySaved: {en: "Saved. The next push or import follows these choices.", ko: "저장했습니다. 다음 푸시나 가져오기부터 이 선택을 따릅니다."},
	MsgRepoHistoryKeptOff: {
		en: "This repository no longer keeps history that is overwritten or deleted from now on. History already kept stays.",
		ko: "이제부터 이 저장소에서 덮어쓰거나 지운 기록은 보관하지 않습니다. 이미 보관된 기록은 그대로 남습니다.",
	},
	MsgRepoHistoryProtectOff: {
		en: "The default branch is no longer protected. Pushes that rewrite or delete it are accepted again.",
		ko: "이제 기본 브랜치를 보호하지 않습니다. 기본 브랜치를 다시 쓰거나 지우는 푸시도 다시 받아들입니다.",
	},
	MsgRepoHistoryFailed: {
		en: "The choices could not be saved. Nothing was changed.",
		ko: "선택을 저장하지 못했습니다. 바뀐 것은 없습니다.",
	},
	MsgRepoHistoryServerUnreadable: {
		en: "Nothing was saved: the server-wide kept history choice cannot be read, so following it cannot be decided. Choose Keep or Do not keep here, or set the server choice again under Settings, Repositories.",
		ko: "저장하지 않았습니다. 서버 전체의 기록 보관 설정을 읽을 수 없어 서버 설정을 따를 수 없습니다. 여기서 보관이나 보관하지 않음을 고르거나, 설정의 저장소 탭에서 서버 설정을 다시 정해 주세요.",
	},
	MsgRepoKeptLabel: {en: "Kept history", ko: "보관된 기록"},
	MsgRepoKeptHelp: {
		en: "When a force push, an import or a deletion replaces the commits of a branch or tag, OwnGit can keep them as kept history, which you can browse and restore. The server setting is under Settings, Repositories.",
		ko: "강제 푸시나 가져오기, 삭제로 브랜치나 태그의 커밋이 바뀌면 OwnGit은 이전 커밋을 보관된 기록으로 남겨 둘 수 있고, 이 기록은 둘러보거나 되돌릴 수 있습니다. 서버 설정은 설정의 저장소 탭에 있습니다.",
	},
	MsgRepoKeptDefaultOn:      {en: "Follow the server setting (now Keep)", ko: "서버 설정 따르기 (지금은 보관)"},
	MsgRepoKeptDefaultOff:     {en: "Follow the server setting (now Do not keep)", ko: "서버 설정 따르기 (지금은 보관하지 않음)"},
	MsgRepoKeptDefaultUnknown: {en: "Follow the server setting (cannot be read now)", ko: "서버 설정 따르기 (지금은 읽을 수 없음)"},
	MsgRepoProtectLabel:       {en: "Protect the default branch", ko: "기본 브랜치 보호"},
	MsgRepoProtectHelp: {
		en: "Refuses a push that rewrites the default branch (one that is not a fast-forward) or deletes it. Pushes that add commits to it, and every other branch and tag, work as before. When you change the default branch, the protection moves to the new one.",
		ko: "기본 브랜치를 다시 쓰는 푸시(fast-forward가 아닌 푸시)나 지우는 푸시를 거부합니다. 커밋을 더하는 푸시와 다른 브랜치, 태그는 전과 같이 동작합니다. 기본 브랜치를 바꾸면 보호도 새 기본 브랜치로 옮겨 갑니다.",
	},
	MsgRepoProtectOffWarning: {
		en: "Turning the protection off lets anyone who can push rewrite or delete the default branch again.",
		ko: "보호를 끄면 푸시할 수 있는 사람은 누구나 기본 브랜치를 다시 쓰거나 지울 수 있게 됩니다.",
	},
	MsgRepoBusy: {
		en: "The repository is busy with another operation, such as an import, a check, or a push. Try again when it finishes.",
		ko: "저장소가 가져오기, 체크, 푸시 같은 다른 작업을 하고 있습니다. 끝난 뒤 다시 시도하세요.",
	},
	MsgRepoBusyImport: {
		en: "An import is running for this repository. Wait for it to finish, or cancel it on the Import tab, then try again.",
		ko: "이 저장소의 가져오기가 진행 중입니다. 끝날 때까지 기다리거나 가져오기 탭에서 취소한 뒤 다시 시도하세요.",
	},
	MsgRepoBusyCheck: {
		en: "A check is running for this repository. Wait for it to finish, or cancel it, then try again.",
		ko: "이 저장소의 체크가 실행 중입니다. 끝날 때까지 기다리거나 취소한 뒤 다시 시도하세요.",
	},
	MsgRepoBusyCheckCleanup: {
		en: "A check's container cleanup is still pending for this repository. Try again after it finishes, or after the next start of OwnGit. If the repository's Automatic checks page lists a leftover container, remove that container and forget it there, or run owngit forget-check-container on the OwnGit computer.",
		ko: "이 저장소의 체크 컨테이너 정리가 아직 끝나지 않았습니다. 정리가 끝난 뒤나 OwnGit을 다음에 시작한 뒤 다시 시도하세요. 저장소의 자동 체크 화면에 남은 컨테이너가 나오면 그 컨테이너를 지운 뒤 거기에서 기록을 지우거나, OwnGit이 실행되는 컴퓨터에서 owngit forget-check-container를 실행하세요.",
	},
	MsgRepoBusyInUse: {
		en: "Another Git operation, such as a push or a clone, is using the repository. Try again in a moment.",
		ko: "푸시나 클론 같은 다른 Git 작업이 이 저장소를 쓰고 있습니다. 잠시 뒤 다시 시도하세요.",
	},
	MsgRepoBusyPreparing: {
		en: "OwnGit is preparing this repository right now. Try again in a moment. Between preparation attempts the repository can be deleted.",
		ko: "OwnGit이 지금 이 저장소를 준비하고 있습니다. 잠시 뒤 다시 시도하세요. 준비 시도 사이에는 저장소를 삭제할 수 있습니다.",
	},
	MsgRepoBusyBackup: {
		en: "A backup is reading this repository. Try again when the backup finishes.",
		ko: "백업이 이 저장소를 읽고 있습니다. 백업이 끝난 뒤 다시 시도하세요.",
	},
	MsgRepoRenameTitle: {en: "Name and address", ko: "이름과 주소"},
	MsgRepoRenameHelp: {
		en: "A new name also changes the address of the repository's pages and its clone address. Files, history, pull requests and checks stay as they are.",
		ko: "이름을 바꾸면 저장소 화면 주소와 클론 주소도 바뀝니다. 파일, 기록, 풀 리퀘스트, 체크는 그대로 남습니다.",
	},
	MsgRepoRenameAddress: {en: "Address now:", ko: "지금 주소:"},
	MsgRepoRenameLabel:   {en: "New name", ko: "새 이름"},
	MsgRepoRenameAliasNote: {
		en: "For 90 days the old address sends pages, the API and Git to the new one, so existing clones keep fetching and pushing. After that the old address stops working, and another repository can take the name unless it is this repository's first name, which stays its ID. Point clones at the new address with git remote set-url before then.",
		ko: "90일 동안은 예전 주소로 들어온 화면, API, Git 요청을 새 주소로 보내므로 기존 클론에서 계속 가져오고 푸시할 수 있습니다. 그 뒤로는 예전 주소가 더 이상 동작하지 않고, 저장소 ID로 남는 처음 이름이 아니라면 다른 저장소가 그 이름을 쓸 수 있습니다. 그 전에 git remote set-url로 클론의 주소를 새 주소로 바꾸세요.",
	},
	MsgRepoRenameAliases: {en: "Earlier addresses that still lead here", ko: "아직 이곳으로 안내하는 예전 주소"},
	// Value: the time the alias stops leading here.
	MsgRepoRenameAliasUntil: {en: "until %s", ko: "%s까지"},
	MsgRepoRenameSubmit:     {en: "Rename", ko: "이름 바꾸기"},
	MsgRepoRenamed: {
		en: "Renamed. The old address leads here for 90 days.",
		ko: "이름을 바꿨습니다. 예전 주소는 90일 동안 이곳으로 안내합니다.",
	},
	MsgRepoRenameTaken: {
		en: "Another repository uses this name, as its name, its ID, or an earlier name that still leads to it. Choose another name.",
		ko: "다른 저장소가 이 이름을 쓰고 있습니다. 그 저장소의 이름이나 ID이거나, 아직 그 저장소로 안내하는 예전 이름입니다. 다른 이름을 입력하세요.",
	},
	MsgRepoRenameFailed: {
		en: "The repository could not be renamed. Nothing was changed.",
		ko: "저장소 이름을 바꾸지 못했습니다. 바뀐 것은 없습니다.",
	},
	MsgRepoAdminPagesTitle: {en: "Other administrator pages", ko: "다른 관리자 화면"},
	MsgRepoAdminChecksLine: {
		en: "Decide whether OwnGit runs this repository's checks, and how.",
		ko: "OwnGit이 이 저장소의 체크를 실행할지, 어떻게 실행할지 정합니다.",
	},
	MsgRepoAdminRunnersLine: {
		en: "Tokens for a runner on another machine that takes this repository's check work.",
		ko: "다른 컴퓨터의 러너가 이 저장소의 체크 작업을 가져갈 때 쓰는 토큰입니다.",
	},
	MsgRepoAdminHelpersLine: {
		en: "Tokens a check helper uses to report results for this repository.",
		ko: "체크 에이전트가 이 저장소의 결과를 보고할 때 쓰는 토큰입니다.",
	},
	MsgRepoAdminImportLine: {
		en: "Change the source this repository copies from, refresh it, or set a schedule.",
		ko: "이 저장소가 복사해 오는 원본을 바꾸거나, 새로 받거나, 일정을 정합니다.",
	},
	MsgRepoAdminDeleteLine: {
		en: "Remove this repository from OwnGit. You choose whether its files stay on disk.",
		ko: "이 저장소를 OwnGit에서 지웁니다. 디스크의 파일을 남길지 고를 수 있습니다.",
	},

	MsgRepoDeleteTitle: {en: "Delete repository", ko: "저장소 삭제"},
	MsgRepoDeleteLead: {
		en: "Either choice removes this repository from OwnGit, together with its pull requests, check records, and import records.",
		ko: "어느 쪽을 고르든 이 저장소는 OwnGit에서 사라지고, 풀 리퀘스트와 체크 기록, 가져오기 기록도 함께 지워집니다.",
	},
	MsgRepoDeleteChoose:    {en: "What happens to the Git files?", ko: "Git 파일은 어떻게 할까요?"},
	MsgRepoDeleteKeepTitle: {en: "Remove from OwnGit and keep the files", ko: "OwnGit에서만 제거하고 파일은 남기기"},
	MsgRepoDeleteKeepDesc: {
		en: "The Git folder moves into a hidden .owngit-removed folder in the repository storage.",
		ko: "Git 폴더를 저장소 폴더 안의 숨은 폴더 .owngit-removed로 옮깁니다.",
	},
	MsgRepoDeleteKeepBack: {
		en: "To bring it back, create a new repository and push from that folder. After the removal, OwnGit shows the command to run.",
		ko: "되살리려면 새 저장소를 만들고 그 폴더에서 푸시하면 됩니다. 제거한 뒤 OwnGit이 실행할 명령을 보여 줍니다.",
	},
	MsgRepoDeleteFilesTitle: {en: "Delete the files too", ko: "파일까지 영구 삭제"},
	MsgRepoDeleteFilesDesc: {
		en: "The Git folder is deleted from disk with all of its history, including kept history.",
		ko: "Git 폴더를 디스크에서 지웁니다. 보관된 기록을 포함해 모든 기록이 사라집니다.",
	},
	MsgRepoDeleteFilesWarn: {
		en: "This cannot be undone. Without a copy elsewhere, this history is gone for good.",
		ko: "되돌릴 수 없습니다. 다른 곳에 복사본이 없으면 이 기록은 영영 사라집니다.",
	},
	MsgRepoDeleteGitPath:     {en: "Git folder now", ko: "지금 Git 폴더"},
	MsgRepoDeleteRemovedPath: {en: "A kept folder moves into", ko: "남긴 폴더를 옮길 곳"},
	MsgRepoDeleteNameHelp: {
		en: "To confirm, type the name exactly as shown:",
		ko: "확인하려면 표시된 이름을 그대로 입력하세요:",
	},
	MsgRepoDeleteSubmit:       {en: "Delete repository", ko: "저장소 삭제"},
	MsgRepoDeleteModeRequired: {en: "Choose what happens to the Git files.", ko: "Git 파일을 어떻게 할지 고르세요."},
	MsgRepoDeleteNameMismatch: {
		en: "The name does not match. Type the repository name exactly as shown.",
		ko: "이름이 맞지 않습니다. 저장소 이름을 표시된 그대로 입력하세요.",
	},
	MsgRepoDeleteGone: {en: "This repository no longer exists.", ko: "이 저장소는 이미 없습니다."},
	MsgRepoDeleteFailed: {
		en: "The repository could not be deleted. Check the server log before trying again.",
		ko: "저장소를 삭제하지 못했습니다. 다시 시도하기 전에 서버 로그를 확인하세요.",
	},
	MsgRepoRemovedKept:   {en: "Removed from OwnGit, files kept:", ko: "OwnGit에서 제거하고 파일은 남겼습니다:"},
	MsgRepoRemovedKeptAt: {en: "The Git folder is now at", ko: "Git 폴더는 지금 이곳에 있습니다:"},
	MsgRepoRemovedKeptRecovery: {
		en: "To bring it back, create a new repository and push from that folder.",
		ko: "되살리려면 새 저장소를 만들고 그 폴더에서 푸시하면 됩니다.",
	},
	MsgRepoRemovedKeptCommand: {
		en: "To bring it back, create a new repository with the same name, then run:",
		ko: "되살리려면 같은 이름으로 새 저장소를 만든 뒤 다음 명령을 실행하세요:",
	},
	MsgRepoRemovedKeptWhere: {
		en: "Run this on the computer where OwnGit runs, because the folder is on that computer.",
		ko: "이 명령은 OwnGit이 실행 중인 컴퓨터에서 실행하세요. 폴더가 그 컴퓨터에 있습니다.",
	},
	MsgRepoRemovedDeleted: {en: "Deleted with its files:", ko: "파일까지 삭제했습니다:"},
	MsgRepoRemovedIncomplete: {
		en: "Removed from OwnGit, but its files are not fully moved or deleted yet:",
		ko: "OwnGit에서는 제거했지만 파일을 아직 다 옮기거나 지우지 못했습니다:",
	},
	MsgRepoRemovedCleanupLater: {
		en: "The file cleanup finishes at the next start of OwnGit. Until then, a new repository cannot use this name.",
		ko: "파일 정리는 OwnGit을 다음에 시작할 때 끝납니다. 그때까지는 이 이름으로 새 저장소를 만들 수 없습니다.",
	},
	MsgRepoRemovedGeneric: {en: "The repository was removed.", ko: "저장소를 제거했습니다."},
}

func init() {
	for code, entry := range repositoryAdminCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

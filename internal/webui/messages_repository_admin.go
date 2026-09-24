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
	MsgRepoNoTagsYet      MessageCode = "repo.overview.no_tags_yet"
	MsgRepoRestoreLead    MessageCode = "repo.overview.restore_lead"
	MsgRepoRestoreStart   MessageCode = "repo.overview.restore_start"

	// Settings tab.
	MsgRepoSettingsTitle        MessageCode = "repoadmin.settings.title"
	MsgRepoSettingsIntro        MessageCode = "repoadmin.settings.intro"
	MsgRepoDefaultBranchTitle   MessageCode = "repoadmin.default_branch.title"
	MsgRepoDefaultBranchHelp    MessageCode = "repoadmin.default_branch.help"
	MsgRepoDefaultBranchLabel   MessageCode = "repoadmin.default_branch.label"
	MsgRepoDefaultBranchCurrent MessageCode = "repoadmin.default_branch.current"
	MsgRepoDefaultBranchMissing MessageCode = "repoadmin.default_branch.missing"
	MsgRepoDefaultBranchChoose  MessageCode = "repoadmin.default_branch.choose"
	MsgRepoDefaultBranchNone    MessageCode = "repoadmin.default_branch.none"
	MsgRepoDefaultBranchSave    MessageCode = "repoadmin.default_branch.save"
	MsgRepoDefaultBranchSaved   MessageCode = "repoadmin.default_branch.saved"
	MsgRepoDefaultBranchUnknown MessageCode = "repoadmin.default_branch.unknown"
	MsgRepoDefaultBranchFailed  MessageCode = "repoadmin.default_branch.failed"
	MsgRepoBusy                 MessageCode = "repoadmin.busy"
	MsgRepoBusyImport           MessageCode = "repoadmin.busy.import"
	MsgRepoBusyCheck            MessageCode = "repoadmin.busy.check"
	MsgRepoBusyCheckCleanup     MessageCode = "repoadmin.busy.check_cleanup"
	MsgRepoBusyInUse            MessageCode = "repoadmin.busy.in_use"
	MsgRepoBusyPreparing        MessageCode = "repoadmin.busy.preparing"
	MsgRepoAdminPagesTitle      MessageCode = "repoadmin.pages.title"
	MsgRepoAdminChecksLine      MessageCode = "repoadmin.pages.checks"
	MsgRepoAdminRunnersLine     MessageCode = "repoadmin.pages.runners"
	MsgRepoAdminHelpersLine     MessageCode = "repoadmin.pages.helpers"
	MsgRepoAdminImportLine      MessageCode = "repoadmin.pages.import"
	MsgRepoAdminDeleteLine      MessageCode = "repoadmin.pages.delete"

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
	MsgRepoDeletePasswordHelp  MessageCode = "repoadmin.delete.password.help"
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

	MsgRepoCloneAddress:   {en: "Clone address", ko: "클론 주소"},
	MsgRepoCopy:           {en: "Copy", ko: "복사"},
	MsgRepoCopied:         {en: "Copied", ko: "복사함"},
	MsgRepoCopyFailed:     {en: "Select the address to copy it", ko: "주소를 선택해 복사하세요"},
	MsgRepoSummaryLabel:   {en: "Repository summary", ko: "저장소 요약"},
	MsgRepoFactDefault:    {en: "Default branch", ko: "기본 브랜치"},
	MsgRepoFactNoDefault:  {en: "Not set", ko: "정해지지 않음"},
	MsgRepoFactLatest:     {en: "Last commit", ko: "마지막 커밋"},
	MsgRepoFactOpenPRs:    {en: "Open pull requests", ko: "열린 풀 리퀘스트"},
	MsgRepoFactCheck:      {en: "Latest check on the default branch", ko: "기본 브랜치 최근 체크"},
	MsgRepoFactUnreadable: {en: "Could not be read", ko: "읽지 못함"},
	MsgRepoRecentCommits:  {en: "Recent commits", ko: "최근 커밋"},
	MsgRepoAllCommits:     {en: "All commits", ko: "커밋 전체 보기"},
	MsgRepoSideLabel:      {en: "Branches and history", ko: "브랜치와 기록"},
	MsgRepoShowAllRefs:    {en: "Show all", ko: "모두 보기"},
	MsgRepoShowFewerRefs:  {en: "Show newest only", ko: "최신만 보기"},
	MsgRepoNewestShown:    {en: "Newest first", ko: "최신순"},
	MsgRepoNoTagsYet:      {en: "None yet", ko: "아직 없음"},
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
		ko: "OwnGit가 먼저 보여 주고, 새로 클론하면 받게 되는 브랜치입니다. 이미 있는 브랜치만 고를 수 있습니다.",
	},
	MsgRepoDefaultBranchLabel:   {en: "Branch", ko: "브랜치"},
	MsgRepoDefaultBranchCurrent: {en: "Now:", ko: "지금:"},
	MsgRepoDefaultBranchMissing: {
		en: "The default branch is set to a branch that does not exist. Choose an existing branch below. Currently set to:",
		ko: "기본 브랜치로 정해진 브랜치가 없습니다. 아래에서 있는 브랜치를 고르세요. 지금 정해진 이름:",
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
		en: "A check's container cleanup is still pending for this repository. Try again after it finishes, or after the next start of OwnGit. If the server log says the job belongs to another Docker daemon, remove its leftover container and run owngit forget-check-container on the OwnGit computer.",
		ko: "이 저장소의 체크 컨테이너 정리가 아직 끝나지 않았습니다. 정리가 끝난 뒤나 OwnGit을 다음에 시작한 뒤 다시 시도하세요. 서버 로그에 이 작업이 다른 Docker 데몬에 속한다고 나오면 남은 컨테이너를 지운 뒤 OwnGit이 실행되는 컴퓨터에서 owngit forget-check-container를 실행하세요.",
	},
	MsgRepoBusyInUse: {
		en: "Another Git operation, such as a push or a clone, is using the repository. Try again in a moment.",
		ko: "푸시나 클론 같은 다른 Git 작업이 이 저장소를 쓰고 있습니다. 잠시 뒤 다시 시도하세요.",
	},
	MsgRepoBusyPreparing: {
		en: "OwnGit is preparing this repository right now. Try again in a moment. Between preparation attempts the repository can be deleted.",
		ko: "OwnGit이 지금 이 저장소를 준비하고 있습니다. 잠시 뒤 다시 시도하세요. 준비 시도 사이에는 저장소를 삭제할 수 있습니다.",
	},
	MsgRepoAdminPagesTitle: {en: "Other administrator pages", ko: "다른 관리자 화면"},
	MsgRepoAdminChecksLine: {
		en: "Decide whether OwnGit runs this repository's checks, and how.",
		ko: "OwnGit가 이 저장소의 체크를 실행할지, 어떻게 실행할지 정합니다.",
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
		ko: "Git 폴더를 저장소 보관 위치 안의 숨은 폴더 .owngit-removed로 옮깁니다.",
	},
	MsgRepoDeleteKeepBack: {
		en: "To bring it back, create a new repository and push from that folder. After the removal, OwnGit shows the command to run.",
		ko: "되살리려면 새 저장소를 만들고 그 폴더에서 푸시하면 됩니다. 제거한 뒤 실행할 명령을 보여 드립니다.",
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
	MsgRepoDeleteRemovedPath: {en: "A kept folder moves into", ko: "남긴 폴더가 옮겨질 곳"},
	MsgRepoDeleteNameHelp: {
		en: "To confirm, type the name exactly as shown:",
		ko: "확인을 위해 표시된 이름을 그대로 입력하세요:",
	},
	MsgRepoDeletePasswordHelp: {
		en: "Deleting asks for the administrator password again, even in an administrator session.",
		ko: "관리자로 로그인해 있어도 삭제할 때는 관리자 비밀번호를 다시 묻습니다.",
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
		ko: "이 명령은 OwnGit가 실행 중인 컴퓨터에서 실행하세요. 폴더가 그 컴퓨터에 있습니다.",
	},
	MsgRepoRemovedDeleted: {en: "Deleted with its files:", ko: "파일까지 삭제했습니다:"},
	MsgRepoRemovedIncomplete: {
		en: "Removed from OwnGit, but its files are not fully moved or deleted yet:",
		ko: "OwnGit에서는 제거했지만 파일을 아직 다 옮기거나 지우지 못했습니다:",
	},
	MsgRepoRemovedCleanupLater: {
		en: "The file cleanup finishes at the next start of OwnGit. Until then, a new repository cannot use this name.",
		ko: "파일 정리는 OwnGit를 다음에 시작할 때 끝납니다. 그때까지는 이 이름으로 새 저장소를 만들 수 없습니다.",
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

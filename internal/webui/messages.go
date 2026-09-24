package webui

import "encoding/json"

// MessageCode names one localized sentence. The backend reports outcomes with
// these codes instead of English strings, so both languages stay complete and
// the wording can change without touching handler code.
//
// An unknown code renders as a generic message and is reported by
// MissingMessages, which the package tests use to keep the catalog complete.
type MessageCode string

// Shared interface chrome. These live in the same catalog as everything else
// so the client-side language switch has a single source of text.
const (
	MsgAppName        MessageCode = "app.name"
	MsgSkipToContent  MessageCode = "app.skip_to_content"
	MsgNavPrimary     MessageCode = "app.nav.primary"
	MsgNavRepos       MessageCode = "app.nav.repositories"
	MsgNavAll         MessageCode = "app.nav.all"
	MsgNavNoRepos     MessageCode = "app.nav.no_repositories"
	MsgSearchRepos    MessageCode = "app.search.repositories"
	MsgAppearance     MessageCode = "app.appearance"
	MsgAppearLight    MessageCode = "app.appearance.light"
	MsgAppearDark     MessageCode = "app.appearance.dark"
	MsgAppearSystem   MessageCode = "app.appearance.system"
	MsgLanguage       MessageCode = "app.language"
	MsgSignOut        MessageCode = "app.sign_out"
	MsgEndAdmin       MessageCode = "app.end_admin"
	MsgBackToRepos    MessageCode = "app.back_to_repositories"
	MsgBackToRepo     MessageCode = "app.back_to_repository"
	MsgOlder          MessageCode = "app.older"
	MsgNewer          MessageCode = "app.newer"
	MsgCancel         MessageCode = "app.cancel"
	MsgSelectYear     MessageCode = "app.select_year"
	MsgGraphRegion    MessageCode = "app.graph_region"
	MsgGraphLess      MessageCode = "app.graph_less"
	MsgGraphMore      MessageCode = "app.graph_more"
	MsgGraphHint      MessageCode = "app.graph_hint"
	MsgGraphTitle     MessageCode = "app.graph_title"
	MsgGraphKicker    MessageCode = "app.graph_kicker"
	MsgRepoTabOver    MessageCode = "app.repo_tab.overview"
	MsgRepoTabCode    MessageCode = "app.repo_tab.code"
	MsgRepoTabCommits MessageCode = "app.repo_tab.commits"
	MsgRepoTabsLabel  MessageCode = "app.repo_tabs_label"
	MsgBranchLabel    MessageCode = "app.branch_label"
	MsgRefLabel       MessageCode = "app.ref_label"
	MsgBranches       MessageCode = "app.branches"
	MsgTags           MessageCode = "app.tags"
	MsgRootFolder     MessageCode = "app.root_folder"
	MsgUpOneLevel     MessageCode = "app.up_one_level"
	MsgFilesLabel     MessageCode = "app.files_label"
	MsgLatestCommit   MessageCode = "app.latest_commit"
	MsgLatestActivity MessageCode = "app.latest_activity"
	MsgSeeAll         MessageCode = "app.see_all"
	MsgChangedFiles   MessageCode = "app.changed_files"
	MsgCommitHistory  MessageCode = "app.commit_history"
	MsgAuthoredBy     MessageCode = "app.authored_by"
	MsgNoDescription  MessageCode = "app.no_description"
	MsgOverviewEmpty  MessageCode = "app.overview_empty"
	MsgOverviewStart  MessageCode = "app.overview_start"
	MsgNoMatches      MessageCode = "app.no_matches"
	MsgClearSearch    MessageCode = "app.clear_search"
	MsgShow           MessageCode = "app.show"
)

// Setup and bootstrap.
const (
	MsgSetupWelcomeTitle      MessageCode = "setup.welcome.title"
	MsgSetupWelcomeBody       MessageCode = "setup.welcome.body"
	MsgSetupWelcomeSecretHeld MessageCode = "setup.welcome.secret_held"
	MsgSetupStart             MessageCode = "setup.welcome.start"
	MsgSetupMissingToken      MessageCode = "setup.welcome.missing_token"
	MsgSetupNeedsScript       MessageCode = "setup.welcome.needs_script"

	MsgSetupWizardTitle MessageCode = "setup.wizard.title"
	MsgSetupWizardBody  MessageCode = "setup.wizard.body"

	MsgSetupStorageLabel   MessageCode = "setup.storage.label"
	MsgSetupStorageHelp    MessageCode = "setup.storage.help"
	MsgSetupStorageLocal   MessageCode = "setup.storage.local_only"
	MsgSetupStorageMissing MessageCode = "setup.storage.missing"
	MsgSetupStorageInvalid MessageCode = "setup.storage.invalid"
	MsgSetupStorageDenied  MessageCode = "setup.storage.denied"
	MsgSetupStorageNotDir  MessageCode = "setup.storage.not_directory"
	MsgSetupStorageInUse   MessageCode = "setup.storage.in_use"
	MsgSetupStorageRemote  MessageCode = "setup.storage.network_share"

	MsgSetupAccessLabel     MessageCode = "setup.access.label"
	MsgSetupAccessHelp      MessageCode = "setup.access.help"
	MsgSetupAccessOpen      MessageCode = "setup.access.open"
	MsgSetupAccessOpenHelp  MessageCode = "setup.access.open_help"
	MsgSetupAccessPassword  MessageCode = "setup.access.password"
	MsgSetupAccessPassHelp  MessageCode = "setup.access.password_help"
	MsgSetupAccessPassEmpty MessageCode = "setup.access.password_empty"
	MsgSetupAccessPassShort MessageCode = "setup.access.password_short"
	MsgSetupAccessPassSaved MessageCode = "setup.access.password_saved"

	MsgSetupAdminLabel     MessageCode = "setup.admin.label"
	MsgSetupAdminHelp      MessageCode = "setup.admin.help"
	MsgSetupAdminEmpty     MessageCode = "setup.admin.empty"
	MsgSetupAdminShort     MessageCode = "setup.admin.short"
	MsgSetupAdminSaved     MessageCode = "setup.admin.saved"
	MsgSetupAdminSameAsGen MessageCode = "setup.admin.same_as_general"

	MsgSetupInsecureLabel MessageCode = "setup.insecure.label"
	MsgSetupInsecureHelp  MessageCode = "setup.insecure.help"
	MsgSetupInsecureNeed  MessageCode = "setup.insecure.required"

	MsgSetupSubmit    MessageCode = "setup.submit"
	MsgSetupFailed    MessageCode = "setup.failed"
	MsgSetupCompleted MessageCode = "setup.completed"

	MsgSetupLinkExpired  MessageCode = "setup.unavailable.expired"
	MsgSetupLinkUsed     MessageCode = "setup.unavailable.used"
	MsgSetupLinkInvalid  MessageCode = "setup.unavailable.invalid"
	MsgSetupAlreadyDone  MessageCode = "setup.unavailable.already_configured"
	MsgSetupRaceLost     MessageCode = "setup.unavailable.race_lost"
	MsgSetupSessionEnded MessageCode = "setup.unavailable.session_ended"
	MsgSetupReissueHint  MessageCode = "setup.unavailable.reissue_hint"

	MsgPrereqGitFound   MessageCode = "setup.prereq.git_found"
	MsgPrereqGitMissing MessageCode = "setup.prereq.git_missing"
	MsgPrereqGitOld     MessageCode = "setup.prereq.git_too_old"
	MsgPrereqHTTPFound  MessageCode = "setup.prereq.http_backend_found"
	MsgPrereqHTTPMiss   MessageCode = "setup.prereq.http_backend_missing"
)

// Authentication.
const (
	MsgLoginTitle       MessageCode = "login.title"
	MsgLoginBody        MessageCode = "login.body"
	MsgLoginField       MessageCode = "login.field"
	MsgLoginSubmit      MessageCode = "login.submit"
	MsgLoginFailed      MessageCode = "login.failed"
	MsgLoginEmpty       MessageCode = "login.empty"
	MsgLoginLocked      MessageCode = "login.locked"
	MsgLoginNotRequired MessageCode = "login.not_required"
	MsgLogoutDone       MessageCode = "login.logged_out"

	MsgAdminTitle     MessageCode = "admin.title"
	MsgAdminBody      MessageCode = "admin.body"
	MsgAdminField     MessageCode = "admin.field"
	MsgAdminSubmit    MessageCode = "admin.submit"
	MsgAdminFailed    MessageCode = "admin.failed"
	MsgAdminEmpty     MessageCode = "admin.empty"
	MsgAdminLocked    MessageCode = "admin.locked"
	MsgAdminConfirmed MessageCode = "admin.confirmed"
	MsgAdminEnded     MessageCode = "admin.ended"
	MsgAdminTemporary MessageCode = "admin.temporary_notice"
	MsgAdminForgot    MessageCode = "admin.forgot"
)

// Settings.
const (
	MsgSettingsTitle    MessageCode = "settings.title"
	MsgSettingsSaved    MessageCode = "settings.saved"
	MsgSettingsAdminReq MessageCode = "settings.admin_required"

	MsgSettingsAccessTitle    MessageCode = "settings.access.title"
	MsgSettingsAccessOpenNow  MessageCode = "settings.access.open_now"
	MsgSettingsAccessPassNow  MessageCode = "settings.access.password_now"
	MsgSettingsEnablePass     MessageCode = "settings.access.enable"
	MsgSettingsChangePass     MessageCode = "settings.access.change"
	MsgSettingsDisablePass    MessageCode = "settings.access.disable"
	MsgSettingsDisableWarning MessageCode = "settings.access.disable_warning"
	MsgSettingsAccessEnabled  MessageCode = "settings.access.enabled"
	MsgSettingsAccessChanged  MessageCode = "settings.access.changed"
	MsgSettingsAccessDisabled MessageCode = "settings.access.disabled"

	MsgSettingsAdminTitle   MessageCode = "settings.admin.title"
	MsgSettingsAdminCurrent MessageCode = "settings.admin.current"
	MsgSettingsAdminNew     MessageCode = "settings.admin.new"
	MsgSettingsAdminChange  MessageCode = "settings.admin.change"
	MsgSettingsAdminChanged MessageCode = "settings.admin.changed"
	MsgSettingsAdminReset   MessageCode = "settings.admin.reset_hint"

	MsgSettingsStorageTitle MessageCode = "settings.storage.title"
	MsgSettingsStorageHelp  MessageCode = "settings.storage.help"
	MsgSettingsCloneTitle   MessageCode = "settings.clone.title"
	MsgSettingsCloneHelp    MessageCode = "settings.clone.help"

	MsgSettingsConnTitle  MessageCode = "settings.connection.title"
	MsgSettingsAckSubmit  MessageCode = "settings.connection.acknowledge"
	MsgSettingsAckDone    MessageCode = "settings.connection.acknowledged"
	MsgSettingsUnknownAct MessageCode = "settings.unknown_action"
)

// Connection indicator.
const (
	MsgConnEncrypted   MessageCode = "connection.encrypted"
	MsgConnPlain       MessageCode = "connection.plain"
	MsgConnPlainDetail MessageCode = "connection.plain_detail"
	MsgConnTailscale   MessageCode = "connection.tailscale_hint"
	MsgConnNoProof     MessageCode = "connection.no_proof"
)

// Repositories.
const (
	MsgRepoNewTitle     MessageCode = "repo.new.title"
	MsgRepoNameLabel    MessageCode = "repo.new.name"
	MsgRepoNameRules    MessageCode = "repo.new.name_rules"
	MsgRepoDescLabel    MessageCode = "repo.new.description"
	MsgRepoDescHelp     MessageCode = "repo.new.description_help"
	MsgRepoCreate       MessageCode = "repo.new.submit"
	MsgRepoNameEmpty    MessageCode = "repo.new.name_empty"
	MsgRepoNameInvalid  MessageCode = "repo.new.name_invalid"
	MsgRepoNameReserved MessageCode = "repo.new.name_reserved"
	MsgRepoNameTaken    MessageCode = "repo.new.name_taken"
	MsgRepoNameLong     MessageCode = "repo.new.name_long"
	MsgRepoCreateFail   MessageCode = "repo.new.failed"
	MsgRepoCreated      MessageCode = "repo.new.created"

	MsgRepoEmpty            MessageCode = "repo.empty"
	MsgRepoEmptyPush        MessageCode = "repo.empty.push_hint"
	MsgRepoCloneTitle       MessageCode = "repo.clone.title"
	MsgRepoUnreadable       MessageCode = "repo.unreadable"
	MsgRepoPreparing        MessageCode = "repo.preparing"
	MsgRepoPreparingDetail  MessageCode = "repo.preparing.detail"
	MsgRepoPreparingShort   MessageCode = "repo.preparing.short"
	MsgActivityPreparing    MessageCode = "activity.preparing"
	MsgRepoNoBranches       MessageCode = "repo.no_branches"
	MsgRepoNoTags           MessageCode = "repo.no_tags"
	MsgRepoDefaultGone      MessageCode = "repo.default_branch_missing"
	MsgRepoDefaultGoneShort MessageCode = "repo.default_branch_missing.short"
	MsgRepoRefMissing       MessageCode = "repo.ref_missing"
	MsgRepoRefRetained      MessageCode = "repo.ref_retained"
	MsgRepoDetached         MessageCode = "repo.detached"
	MsgRepoRetainTitle      MessageCode = "repo.retained.title"
	MsgRepoRetainHelp       MessageCode = "repo.retained.help"
	MsgRepoNotFound         MessageCode = "repo.not_found"
)

// Code browsing and commits.
const (
	MsgCodeEmptyDir    MessageCode = "code.empty_directory"
	MsgCodePathMissing MessageCode = "code.path_missing"
	MsgCodeBinary      MessageCode = "code.binary"
	MsgCodeTruncated   MessageCode = "code.truncated"
	MsgCodeRawLink     MessageCode = "code.raw_link"
	MsgCodeSubmodule   MessageCode = "code.submodule"
	MsgCodeSymlink     MessageCode = "code.symlink"

	MsgCommitsEmpty     MessageCode = "commits.empty"
	MsgCommitNotFound   MessageCode = "commits.not_found"
	MsgCommitDiffBig    MessageCode = "commits.diff_truncated"
	MsgCommitDiffNone   MessageCode = "commits.diff_unavailable"
	MsgCommitDiffMerge  MessageCode = "commits.diff_merge"
	MsgCommitBinaryFile MessageCode = "commits.binary_file"
	MsgCommitCommitter  MessageCode = "commits.committer"
)

// Restoring files from an earlier commit.
//
// The first seven codes are the shared outcomes the backend reports. The rest
// are the screen's own labels and help text, which the renderer owns.
const (
	MsgRestoreInvalid     MessageCode = "restore.invalid"
	MsgRestoreConflict    MessageCode = "restore.conflict"
	MsgRestoreNoChanges   MessageCode = "restore.no_changes"
	MsgRestoreUnsupported MessageCode = "restore.unsupported"
	MsgRestoreFailed      MessageCode = "restore.failed"
	MsgRestoreReady       MessageCode = "restore.ready"
	MsgRestoreSuccess     MessageCode = "restore.success"

	MsgRestoreTitle    MessageCode = "restore.title"
	MsgRestoreIntro    MessageCode = "restore.intro"
	MsgRestoreOpen     MessageCode = "restore.open"
	MsgRestoreOpenFile MessageCode = "restore.open_file"

	MsgRestoreSourceLabel  MessageCode = "restore.source.label"
	MsgRestoreSourceHelp   MessageCode = "restore.source.help"
	MsgRestoreTargetLabel  MessageCode = "restore.target.label"
	MsgRestoreTargetHelp   MessageCode = "restore.target.help"
	MsgRestoreTargetChoose MessageCode = "restore.target.choose"
	MsgRestoreTargetNew    MessageCode = "restore.target.recreated"
	MsgRestoreTargetEmpty  MessageCode = "restore.target.empty"

	MsgRestoreScopeLabel     MessageCode = "restore.scope.label"
	MsgRestoreModeAll        MessageCode = "restore.scope.all"
	MsgRestoreModeAllHelp    MessageCode = "restore.scope.all_help"
	MsgRestoreModeFiles      MessageCode = "restore.scope.files"
	MsgRestoreModeFilesHelp  MessageCode = "restore.scope.files_help"
	MsgRestoreFilesLabel     MessageCode = "restore.files.label"
	MsgRestoreFilesHelp      MessageCode = "restore.files.help"
	MsgRestoreFilesInactive  MessageCode = "restore.files.inactive"
	MsgRestoreFilesNone      MessageCode = "restore.files.none"
	MsgRestoreFilesEmpty     MessageCode = "restore.files.empty"
	MsgRestoreStatusAdded    MessageCode = "restore.status.added"
	MsgRestoreStatusModified MessageCode = "restore.status.modified"
	MsgRestoreStatusDeleted  MessageCode = "restore.status.deleted"

	MsgRestorePreviewSubmit MessageCode = "restore.preview.submit"
	MsgRestorePreviewTitle  MessageCode = "restore.preview.title"
	MsgRestorePreviewHelp   MessageCode = "restore.preview.help"
	MsgRestorePreviewStale  MessageCode = "restore.preview.stale"
	MsgRestoreDeletesLabel  MessageCode = "restore.preview.deletes"
	MsgRestoreDiffTruncated MessageCode = "restore.preview.diff_truncated"
	MsgRestoreBinaryFile    MessageCode = "restore.preview.binary_file"

	MsgRestoreConfirmLabel MessageCode = "restore.confirm.label"
	MsgRestoreConfirmHelp  MessageCode = "restore.confirm.help"
	MsgRestoreApplySubmit  MessageCode = "restore.apply.submit"
	MsgRestoreChangeChoice MessageCode = "restore.apply.change_choice"
	MsgRestoreChangeHelp   MessageCode = "restore.apply.change_help"
)

// Activity.
const (
	MsgActivityTitle      MessageCode = "activity.title"
	MsgActivityEmpty      MessageCode = "activity.empty"
	MsgActivityIncomplete MessageCode = "activity.incomplete"
	MsgActivityLimit      MessageCode = "activity.incomplete.limit"
	MsgActivityCounting   MessageCode = "activity.incomplete.counting"
	MsgActivityCountRepo  MessageCode = "activity.incomplete.counting_repository"
	MsgActivityScanFail   MessageCode = "activity.incomplete.scan_failed"
	MsgActivityUnavail    MessageCode = "activity.unavailable"
	MsgActivityNotBuilt   MessageCode = "activity.unavailable.not_built"
	MsgActivityNoChecks   MessageCode = "activity.no_check_claim"
)

// Generic and HTTP errors.
const (
	MsgErrNotFound     MessageCode = "error.not_found"
	MsgErrForbidden    MessageCode = "error.forbidden"
	MsgErrBadRequest   MessageCode = "error.bad_request"
	MsgErrCSRF         MessageCode = "error.csrf"
	MsgErrHostRejected MessageCode = "error.host_rejected"
	MsgErrMethod       MessageCode = "error.method_not_allowed"
	MsgErrTooLarge     MessageCode = "error.payload_too_large"
	MsgErrRateLimited  MessageCode = "error.rate_limited"
	MsgErrInternal     MessageCode = "error.internal"
	MsgErrUnavailable  MessageCode = "error.unavailable"
	MsgErrGeneric      MessageCode = "error.generic"
	MsgErrBackHome     MessageCode = "error.back_home"
)

// message is one catalog entry.
type message struct {
	en string
	ko string
}

// catalog holds every localized sentence. Korean uses the established Git
// vocabulary: 브랜치, 커밋, 태그, 저장소.
var catalog = map[MessageCode]message{
	// -- chrome --------------------------------------------------------
	MsgAppName:        {en: "OwnGit", ko: "OwnGit"},
	MsgSkipToContent:  {en: "Skip to content", ko: "본문으로 건너뛰기"},
	MsgNavPrimary:     {en: "Primary", ko: "주 메뉴"},
	MsgNavRepos:       {en: "Repositories", ko: "저장소"},
	MsgNavAll:         {en: "All", ko: "전체"},
	MsgNavNoRepos:     {en: "No repositories yet", ko: "아직 저장소가 없습니다"},
	MsgSearchRepos:    {en: "Search repositories", ko: "저장소 검색"},
	MsgAppearance:     {en: "Appearance", ko: "화면 모드"},
	MsgAppearLight:    {en: "Light", ko: "라이트"},
	MsgAppearDark:     {en: "Dark", ko: "다크"},
	MsgAppearSystem:   {en: "System", ko: "시스템"},
	MsgLanguage:       {en: "Language", ko: "언어"},
	MsgSignOut:        {en: "Sign out", ko: "로그아웃"},
	MsgEndAdmin:       {en: "End admin confirmation", ko: "관리자 확인 종료"},
	MsgBackToRepos:    {en: "All repositories", ko: "저장소 목록"},
	MsgBackToRepo:     {en: "Back to repository", ko: "저장소로 돌아가기"},
	MsgOlder:          {en: "Older", ko: "이전"},
	MsgNewer:          {en: "Newer", ko: "다음"},
	MsgCancel:         {en: "Cancel", ko: "취소"},
	MsgSelectYear:     {en: "Select year", ko: "연도 선택"},
	MsgGraphRegion:    {en: "Daily commit activity. Scroll horizontally to see the whole year.", ko: "일별 커밋 활동 표입니다. 가로로 스크롤하면 한 해 전체를 볼 수 있습니다."},
	MsgGraphLess:      {en: "Less", ko: "적음"},
	MsgGraphMore:      {en: "More", ko: "많음"},
	MsgGraphHint:      {en: "Move between dates with the arrow keys.", ko: "방향키로 날짜 사이를 이동할 수 있습니다."},
	MsgGraphTitle:     {en: "Commit activity", ko: "커밋 활동"},
	MsgGraphKicker:    {en: "Commit activity", ko: "커밋 활동"},
	MsgRepoTabOver:    {en: "Overview", ko: "개요"},
	MsgRepoTabCode:    {en: "Code", ko: "코드"},
	MsgRepoTabCommits: {en: "Commits", ko: "커밋"},
	MsgRepoTabsLabel:  {en: "Repository sections", ko: "저장소 메뉴"},
	MsgBranchLabel:    {en: "Branch", ko: "브랜치"},
	// The picker lists branches and tags together, and its closed state shows
	// only the chosen short name, so the label cannot promise a branch. This
	// is separate from MsgBranchLabel, which names the default-branch pill.
	MsgRefLabel:       {en: "Branch or tag", ko: "브랜치 또는 태그"},
	MsgBranches:       {en: "Branches", ko: "브랜치"},
	MsgTags:           {en: "Tags", ko: "태그"},
	MsgRootFolder:     {en: "Root folder", ko: "최상위 폴더"},
	MsgUpOneLevel:     {en: "Up one level", ko: "상위 폴더로"},
	MsgFilesLabel:     {en: "Files", ko: "파일"},
	MsgLatestCommit:   {en: "Latest commit", ko: "최근 커밋"},
	MsgLatestActivity: {en: "Latest activity", ko: "최근 활동"},
	MsgSeeAll:         {en: "See all activity", ko: "전체 활동 보기"},
	MsgChangedFiles:   {en: "Changed files", ko: "변경된 파일"},
	MsgCommitHistory:  {en: "Commit history", ko: "커밋 기록"},
	MsgAuthoredBy:     {en: "Written by", ko: "작성자"},
	MsgNoDescription:  {en: "No description", ko: "설명 없음"},
	MsgOverviewEmpty:  {en: "No repositories yet.", ko: "아직 저장소가 없습니다."},
	MsgOverviewStart:  {en: "Create one, then push an existing project into it.", ko: "저장소를 만든 뒤 기존 프로젝트를 푸시하세요."},
	MsgNoMatches:      {en: "No repository matches that search.", ko: "검색과 일치하는 저장소가 없습니다."},
	MsgClearSearch:    {en: "Clear search", ko: "검색 지우기"},
	MsgShow:           {en: "Show", ko: "보기"},

	// -- setup ---------------------------------------------------------
	MsgSetupWelcomeTitle: {
		en: "Set up OwnGit",
		ko: "OwnGit 설치 시작",
	},
	// This is the page's static introduction, rendered before anything has
	// been checked. The code lives in the URL fragment, which the browser
	// never sends, so the server cannot tell whether this visitor arrived by
	// the one-time link or simply typed the address. It therefore explains
	// what the link is for instead of claiming the reader used one. The
	// held-code and missing-code notices below say which case applies.
	MsgSetupWelcomeBody: {
		en: "Use the one-time setup link to configure this installation. Starting setup uses the link so it cannot be reused.",
		ko: "이 설치를 구성하려면 1회용 설치 링크를 사용하세요. 설치를 시작하면 링크가 사용 처리되어 다시 쓸 수 없습니다.",
	},
	MsgSetupWelcomeSecretHeld: {
		en: "The link code was removed from the address bar and is kept only in this page until you start.",
		ko: "링크의 코드는 주소창에서 지웠고, 설치를 시작할 때까지 이 페이지 안에만 보관합니다.",
	},
	MsgSetupStart: {
		en: "Start setup",
		ko: "설치 시작",
	},
	MsgSetupMissingToken: {
		en: "This page has no setup code. Open the one-time link the installer gave you.",
		ko: "이 페이지에 설치 코드가 없습니다. 설치할 때 받은 1회용 링크로 다시 열어 주세요.",
	},
	// The code lives in the URL fragment, which a browser never sends to the
	// server. Only a script in the page can read it, so the server cannot know
	// whether one is present, and setup genuinely cannot proceed without
	// scripting.
	MsgSetupNeedsScript: {
		en: "Setup needs JavaScript in this browser, because the one-time code stays in the address bar and is never sent to the server.",
		ko: "설정에는 이 브라우저의 JavaScript가 필요합니다. 1회용 코드는 주소창에만 있어 서버로 전달되지 않기 때문입니다.",
	},
	MsgSetupWizardTitle: {
		en: "Configure this installation",
		ko: "설치 설정",
	},
	MsgSetupWizardBody: {
		en: "Choose where repositories are stored and how people reach them. You can change access settings later.",
		ko: "저장소를 어디에 보관할지, 누가 어떻게 접근할지 정합니다. 접근 설정은 나중에 바꿀 수 있습니다.",
	},
	MsgSetupStorageLabel: {
		en: "Repository folder",
		ko: "저장소 폴더",
	},
	MsgSetupStorageHelp: {
		en: "Repositories are created inside this folder. Existing files in it are left alone.",
		ko: "저장소는 이 폴더 안에 만들어집니다. 폴더에 이미 있던 파일은 건드리지 않습니다.",
	},
	MsgSetupStorageLocal: {
		en: "The folder can be on this computer's disk or on a mounted SMB or NFS share, as long as only one OwnGit uses it at a time. Settings and the database always stay on this computer.",
		ko: "이 컴퓨터의 디스크나 마운트한 SMB 또는 NFS 공유 폴더를 쓸 수 있습니다. 단, 한 번에 하나의 OwnGit만 그 폴더를 써야 합니다. 설정과 데이터베이스는 항상 이 컴퓨터에 보관합니다.",
	},
	MsgSetupStorageMissing: {
		en: "Enter the folder where repositories should be stored.",
		ko: "저장소를 보관할 폴더를 입력하세요.",
	},
	MsgSetupStorageInvalid: {
		en: "Enter a full path, for example /Users/you/git.",
		ko: "전체 경로를 입력하세요. 예: /Users/you/git",
	},
	MsgSetupStorageDenied: {
		en: "This account cannot write to that folder. Choose another folder or change its permissions.",
		ko: "이 계정으로는 그 폴더에 쓸 수 없습니다. 다른 폴더를 고르거나 권한을 바꿔 주세요.",
	},
	MsgSetupStorageNotDir: {
		en: "That path is a file. Choose a folder.",
		ko: "그 경로는 파일입니다. 폴더를 선택하세요.",
	},
	MsgSetupStorageInUse: {
		en: "That folder already holds another installation's data.",
		ko: "그 폴더에는 이미 다른 설치의 데이터가 있습니다.",
	},
	MsgSetupStorageRemote: {
		en: "That folder looks like a network share. Repositories can be kept there while only one OwnGit uses it at a time. The database always stays on this computer.",
		ko: "그 폴더는 네트워크 공유로 보입니다. 한 번에 하나의 OwnGit만 쓴다면 저장소를 그곳에 둘 수 있습니다. 데이터베이스는 항상 이 컴퓨터에 남습니다.",
	},
	MsgSetupAccessLabel: {
		en: "Who can read and write repositories",
		ko: "저장소를 읽고 쓸 수 있는 사람",
	},
	MsgSetupAccessHelp: {
		en: "This covers everyday repository use on your network. Security settings always need the administrator password.",
		ko: "네트워크 안에서의 평소 저장소 사용에 적용됩니다. 보안 설정은 언제나 관리자 비밀번호가 필요합니다.",
	},
	MsgSetupAccessOpen: {
		en: "Anyone on this network",
		ko: "이 네트워크의 모든 사람",
	},
	MsgSetupAccessOpenHelp: {
		en: "No password for reading or pushing. Suitable for a private LAN or a Tailscale network you control.",
		ko: "읽기와 푸시에 비밀번호가 없습니다. 직접 관리하는 사설 LAN이나 Tailscale 네트워크에 적합합니다.",
	},
	MsgSetupAccessPassword: {
		en: "People with the shared password",
		ko: "공용 비밀번호를 아는 사람",
	},
	MsgSetupAccessPassHelp: {
		en: "One shared password for everyone. There are no individual accounts.",
		ko: "모두가 같은 비밀번호 하나를 사용합니다. 개인 계정은 없습니다.",
	},
	MsgSetupAccessPassEmpty: {
		en: "Enter the shared access password.",
		ko: "공용 접근 비밀번호를 입력하세요.",
	},
	MsgSetupAccessPassShort: {
		en: "Use at least 8 characters.",
		ko: "8자 이상으로 입력하세요.",
	},
	MsgSetupAccessPassSaved: {
		en: "Shared password entered.",
		ko: "공용 비밀번호를 입력했습니다.",
	},
	MsgSetupAdminLabel: {
		en: "Administrator password",
		ko: "관리자 비밀번호",
	},
	MsgSetupAdminHelp: {
		en: "Required to change security settings from any browser. Keep it separate from the shared password.",
		ko: "어느 브라우저에서든 보안 설정을 바꿀 때 필요합니다. 공용 비밀번호와는 다르게 정하세요.",
	},
	MsgSetupAdminEmpty: {
		en: "Enter an administrator password.",
		ko: "관리자 비밀번호를 입력하세요.",
	},
	MsgSetupAdminShort: {
		en: "Use at least 8 characters.",
		ko: "8자 이상으로 입력하세요.",
	},
	MsgSetupAdminSaved: {
		en: "Administrator password entered.",
		ko: "관리자 비밀번호를 입력했습니다.",
	},
	MsgSetupAdminSameAsGen: {
		en: "Use a different password from the shared access password.",
		ko: "공용 접근 비밀번호와 다른 비밀번호를 사용하세요.",
	},
	MsgSetupInsecureLabel: {
		en: "Continue with a connection OwnGit does not encrypt",
		ko: "OwnGit이 암호화하지 않는 연결로 계속하기",
	},
	// Say exactly what the application provides and what it cannot see. OwnGit
	// serves plain HTTP and adds no encryption of its own. Whether the
	// path is protected by a VPN or similar is outside what it can observe, so
	// the notice must neither promise safety nor declare every HTTP setup
	// wholly exposed.
	MsgSetupInsecureHelp: {
		en: "OwnGit serves this page over plain HTTP and adds no encryption of its own. On an ordinary LAN with nothing else protecting the connection, passwords, sessions, and repository contents can be read by others on the network. A VPN or similar protection may already cover this path, but OwnGit cannot check that.",
		ko: "OwnGit은 이 페이지를 일반 HTTP로 제공하며 자체 암호화를 더하지 않습니다. 다른 보호 장치가 없는 일반 LAN에서는 비밀번호와 세션, 저장소 내용을 같은 네트워크의 다른 사람이 읽을 수 있습니다. VPN 같은 보호가 이미 적용되어 있을 수도 있지만 OwnGit은 그 여부를 확인할 수 없습니다.",
	},
	MsgSetupInsecureNeed: {
		en: "Confirm that you understand OwnGit is not encrypting this connection.",
		ko: "OwnGit이 이 연결을 암호화하지 않는다는 점을 확인해 주세요.",
	},
	MsgSetupSubmit: {
		en: "Finish setup",
		ko: "설치 완료",
	},
	MsgSetupFailed: {
		en: "Setup could not be completed. Nothing was changed.",
		ko: "설치를 완료하지 못했습니다. 변경된 내용은 없습니다.",
	},
	MsgSetupCompleted: {
		en: "Setup finished. Create your first repository when you are ready.",
		ko: "설치를 마쳤습니다. 준비되면 첫 저장소를 만드세요.",
	},
	MsgSetupLinkExpired: {
		en: "This setup link expired.",
		ko: "이 설치 링크는 기한이 지났습니다.",
	},
	MsgSetupLinkUsed: {
		en: "This setup link was already used.",
		ko: "이 설치 링크는 이미 사용되었습니다.",
	},
	MsgSetupLinkInvalid: {
		en: "This setup link is not valid for this installation.",
		ko: "이 설치 링크는 이 설치에서 사용할 수 없습니다.",
	},
	MsgSetupAlreadyDone: {
		en: "This installation is already set up.",
		ko: "이 설치는 이미 설정을 마쳤습니다.",
	},
	MsgSetupRaceLost: {
		en: "Another browser finished setup first.",
		ko: "다른 브라우저에서 먼저 설치를 마쳤습니다.",
	},
	MsgSetupSessionEnded: {
		en: "The setup session ended. Open the setup link again to continue.",
		ko: "설치 세션이 끝났습니다. 설치 링크를 다시 열어 계속하세요.",
	},
	MsgSetupReissueHint: {
		en: "Issue a new link from the computer running OwnGit. There is no email or account recovery.",
		ko: "OwnGit을 실행 중인 컴퓨터에서 새 링크를 발급하세요. 이메일이나 계정 복구 절차는 없습니다.",
	},
	MsgPrereqGitFound: {
		en: "Git is installed.",
		ko: "Git이 설치되어 있습니다.",
	},
	MsgPrereqGitMissing: {
		en: "Git was not found. Install Git on this computer, then reload this page.",
		ko: "Git을 찾지 못했습니다. 이 컴퓨터에 Git을 설치한 뒤 이 페이지를 새로 고치세요.",
	},
	MsgPrereqGitOld: {
		en: "The installed Git is older than OwnGit needs.",
		ko: "설치된 Git이 OwnGit에서 필요한 버전보다 오래되었습니다.",
	},
	MsgPrereqHTTPFound: {
		en: "Git's HTTP service was found.",
		ko: "Git의 HTTP 서비스를 찾았습니다.",
	},
	MsgPrereqHTTPMiss: {
		en: "Git's HTTP service was not found, so cloning and pushing over HTTP will not work.",
		ko: "Git의 HTTP 서비스를 찾지 못했습니다. HTTP로 클론하거나 푸시할 수 없습니다.",
	},

	// -- authentication ------------------------------------------------
	MsgLoginTitle: {
		en: "Enter the shared password",
		ko: "공용 비밀번호 입력",
	},
	MsgLoginBody: {
		en: "This installation asks for one shared password before showing repositories.",
		ko: "이 설치는 저장소를 보기 전에 공용 비밀번호 하나를 요구합니다.",
	},
	MsgLoginField: {
		en: "Shared password",
		ko: "공용 비밀번호",
	},
	MsgLoginSubmit: {
		en: "Open repositories",
		ko: "저장소 열기",
	},
	MsgLoginFailed: {
		en: "That password did not match.",
		ko: "비밀번호가 맞지 않습니다.",
	},
	MsgLoginEmpty: {
		en: "Enter the shared password.",
		ko: "공용 비밀번호를 입력하세요.",
	},
	MsgLoginLocked: {
		en: "Too many attempts. Try again shortly.",
		ko: "시도가 너무 많았습니다. 잠시 후 다시 시도하세요.",
	},
	MsgLoginNotRequired: {
		en: "This installation does not use a shared password.",
		ko: "이 설치는 공용 비밀번호를 사용하지 않습니다.",
	},
	MsgLogoutDone: {
		en: "Signed out of shared access.",
		ko: "공용 접근에서 나왔습니다.",
	},
	MsgAdminTitle: {
		en: "Confirm as administrator",
		ko: "관리자 확인",
	},
	MsgAdminBody: {
		en: "Security settings need the administrator password. Confirmation lasts for this short session only.",
		ko: "보안 설정에는 관리자 비밀번호가 필요합니다. 확인 상태는 이번 짧은 세션 동안만 유지됩니다.",
	},
	MsgAdminField: {
		en: "Administrator password",
		ko: "관리자 비밀번호",
	},
	MsgAdminSubmit: {
		en: "Confirm",
		ko: "확인",
	},
	MsgAdminFailed: {
		en: "That administrator password did not match.",
		ko: "관리자 비밀번호가 맞지 않습니다.",
	},
	MsgAdminEmpty: {
		en: "Enter the administrator password.",
		ko: "관리자 비밀번호를 입력하세요.",
	},
	MsgAdminLocked: {
		en: "Too many attempts. Try again shortly.",
		ko: "시도가 너무 많았습니다. 잠시 후 다시 시도하세요.",
	},
	MsgAdminConfirmed: {
		en: "Confirmed as administrator.",
		ko: "관리자로 확인되었습니다.",
	},
	MsgAdminEnded: {
		en: "Administrator confirmation ended.",
		ko: "관리자 확인을 종료했습니다.",
	},
	MsgAdminTemporary: {
		en: "This browser is not remembered as an administrator. Confirmation is asked again when it lapses.",
		ko: "이 브라우저를 관리자로 기억하지 않습니다. 확인 상태가 끝나면 다시 물어봅니다.",
	},
	MsgAdminForgot: {
		en: "Forgot it? Reset the administrator password from the computer running OwnGit. Repositories are not touched.",
		ko: "잊으셨나요? OwnGit을 실행 중인 컴퓨터에서 관리자 비밀번호를 다시 설정하세요. 저장소는 그대로 유지됩니다.",
	},

	// -- settings ------------------------------------------------------
	MsgSettingsTitle: {
		en: "Settings",
		ko: "설정",
	},
	MsgSettingsSaved: {
		en: "Settings saved.",
		ko: "설정을 저장했습니다.",
	},
	MsgSettingsAdminReq: {
		en: "Enter the administrator password to apply a change.",
		ko: "변경을 적용하려면 관리자 비밀번호를 입력하세요.",
	},
	MsgSettingsAccessTitle: {
		en: "Repository access",
		ko: "저장소 접근",
	},
	MsgSettingsAccessOpenNow: {
		en: "Anyone on this network can read and push without a password.",
		ko: "지금은 이 네트워크의 누구나 비밀번호 없이 읽고 푸시할 수 있습니다.",
	},
	MsgSettingsAccessPassNow: {
		en: "A shared password is required to read and push.",
		ko: "지금은 읽기와 푸시에 공용 비밀번호가 필요합니다.",
	},
	MsgSettingsEnablePass: {
		en: "Require a shared password",
		ko: "공용 비밀번호 사용",
	},
	MsgSettingsChangePass: {
		en: "Change the shared password",
		ko: "공용 비밀번호 변경",
	},
	MsgSettingsDisablePass: {
		en: "Stop requiring a shared password",
		ko: "공용 비밀번호 해제",
	},
	MsgSettingsDisableWarning: {
		en: "Everyone who can reach this address will be able to read and push.",
		ko: "이 주소에 접속할 수 있는 모든 사람이 읽고 푸시할 수 있게 됩니다.",
	},
	MsgSettingsAccessEnabled: {
		en: "Shared password turned on. Open sessions were signed out.",
		ko: "공용 비밀번호를 켰습니다. 기존 세션은 로그아웃되었습니다.",
	},
	MsgSettingsAccessChanged: {
		en: "Shared password changed. Open sessions were signed out.",
		ko: "공용 비밀번호를 바꿨습니다. 기존 세션은 로그아웃되었습니다.",
	},
	MsgSettingsAccessDisabled: {
		en: "Shared password turned off.",
		ko: "공용 비밀번호를 껐습니다.",
	},
	MsgSettingsAdminTitle: {
		en: "Administrator password",
		ko: "관리자 비밀번호",
	},
	MsgSettingsAdminCurrent: {
		en: "Current administrator password",
		ko: "현재 관리자 비밀번호",
	},
	MsgSettingsAdminNew: {
		en: "New administrator password",
		ko: "새 관리자 비밀번호",
	},
	MsgSettingsAdminChange: {
		en: "Change administrator password",
		ko: "관리자 비밀번호 변경",
	},
	MsgSettingsAdminChanged: {
		en: "Administrator password changed.",
		ko: "관리자 비밀번호를 바꿨습니다.",
	},
	MsgSettingsAdminReset: {
		en: "If it is lost, reset it from the computer running OwnGit. Repository data is preserved.",
		ko: "잊었다면 OwnGit을 실행 중인 컴퓨터에서 다시 설정하세요. 저장소 데이터는 그대로 유지됩니다.",
	},
	MsgSettingsStorageTitle: {
		en: "Repository folder",
		ko: "저장소 폴더",
	},
	MsgSettingsStorageHelp: {
		en: "Only administrators see this location.",
		ko: "이 위치는 관리자에게만 보입니다.",
	},
	MsgSettingsCloneTitle: {
		en: "Clone address",
		ko: "클론 주소",
	},
	MsgSettingsCloneHelp: {
		en: "Add the repository name to this address when cloning.",
		ko: "클론할 때 이 주소 뒤에 저장소 이름을 붙이세요.",
	},
	MsgSettingsConnTitle: {
		en: "Connection",
		ko: "연결",
	},
	MsgSettingsAckSubmit: {
		en: "Keep using this connection",
		ko: "이 연결로 계속 사용",
	},
	// Acknowledging stops the prompt. It does not change the connection, so
	// the indicator stays visible.
	MsgSettingsAckDone: {
		en: "Noted. Nothing about the connection changed; the header keeps showing its status instead of asking again.",
		ko: "확인했습니다. 연결 자체가 바뀜지지는 않으며, 다시 묻지 않고 상단에 상태만 계속 표시합니다.",
	},
	MsgSettingsUnknownAct: {
		en: "That action is not available.",
		ko: "사용할 수 없는 동작입니다.",
	},

	// -- connection ----------------------------------------------------
	// The indicator describes this request's transport only. Encrypted means
	// the request itself arrived over TLS; it says nothing about later hops.
	MsgConnEncrypted: {
		en: "Encrypted by OwnGit",
		ko: "OwnGit이 암호화함",
	},
	MsgConnPlain: {
		en: "Not encrypted by OwnGit",
		ko: "OwnGit이 암호화하지 않음",
	},
	MsgConnPlainDetail: {
		en: "This page arrived over plain HTTP, so OwnGit is not encrypting it.",
		ko: "이 페이지는 일반 HTTP로 전달되었으며 OwnGit이 암호화하지 않습니다.",
	},
	MsgConnTailscale: {
		en: "Tailscale is a straightforward way to protect access from another device. OwnGit does not detect or configure it for you.",
		ko: "다른 기기에서 접속할 때는 Tailscale로 보호하는 방법이 간단합니다. OwnGit이 자동으로 감지하거나 설정하지는 않습니다.",
	},
	// A host name is not evidence. A Tailscale-style name can be served over
	// plain HTTP, and a plain name can sit inside a protected network.
	MsgConnNoProof: {
		en: "OwnGit reports only its own connection. It cannot tell whether a VPN or other protection covers the rest of the path, and a host name alone does not prove one.",
		ko: "OwnGit은 자신이 맺은 연결만 알려줍니다. VPN 같은 다른 보호가 나머지 구간을 감싸는지는 알 수 없으며, 호스트 이름만으로는 증명되지 않습니다.",
	},

	// -- repositories --------------------------------------------------
	MsgRepoNewTitle: {
		en: "New repository",
		ko: "새 저장소",
	},
	MsgRepoNameLabel: {
		en: "Name",
		ko: "이름",
	},
	MsgRepoNameRules: {
		en: "Letters, numbers, dots, dashes, and underscores. This becomes the clone address.",
		ko: "영문자, 숫자, 점, 하이픈, 밑줄을 사용합니다. 이 이름이 클론 주소가 됩니다.",
	},
	MsgRepoDescLabel: {
		en: "Description",
		ko: "설명",
	},
	MsgRepoDescHelp: {
		en: "Optional. Shown next to the repository name.",
		ko: "선택 사항입니다. 저장소 이름 옆에 표시됩니다.",
	},
	MsgRepoCreate: {
		en: "Create repository",
		ko: "저장소 만들기",
	},
	MsgRepoNameEmpty: {
		en: "Enter a repository name.",
		ko: "저장소 이름을 입력하세요.",
	},
	MsgRepoNameInvalid: {
		en: "Use letters, numbers, dots, dashes, and underscores only.",
		ko: "영문자, 숫자, 점, 하이픈, 밑줄만 사용하세요.",
	},
	MsgRepoNameReserved: {
		en: "The names new and new-import are reserved. Choose another name.",
		ko: "new와 new-import는 예약된 이름입니다. 다른 이름을 입력하세요.",
	},
	MsgRepoNameTaken: {
		en: "A repository with that name already exists.",
		ko: "같은 이름의 저장소가 이미 있습니다.",
	},
	MsgRepoNameLong: {
		en: "That name is too long.",
		ko: "이름이 너무 깁니다.",
	},
	MsgRepoCreateFail: {
		en: "The repository could not be created. Nothing was stored.",
		ko: "저장소를 만들지 못했습니다. 저장된 내용은 없습니다.",
	},
	MsgRepoCreated: {
		en: "Repository created.",
		ko: "저장소를 만들었습니다.",
	},
	MsgRepoEmpty: {
		en: "This repository has no commits yet.",
		ko: "이 저장소에는 아직 커밋이 없습니다.",
	},
	MsgRepoEmptyPush: {
		en: "Push an existing project to fill it:",
		ko: "기존 프로젝트를 푸시해서 채우세요.",
	},
	MsgRepoCloneTitle: {
		en: "Clone address",
		ko: "클론 주소",
	},
	MsgRepoUnreadable: {
		en: "This repository's Git data could not be read.",
		ko: "이 저장소의 Git 데이터를 읽지 못했습니다.",
	},
	MsgRepoPreparing: {
		en: "This repository is being prepared.",
		ko: "이 저장소를 준비하는 중입니다.",
	},
	MsgRepoPreparingDetail: {
		en: "After starting, OwnGit checks each repository's safety settings before serving it. This one is not ready yet, so pushes, clones, and changes are refused for now. OwnGit keeps retrying on its own. The server log explains what went wrong.",
		ko: "OwnGit은 시작할 때 저장소마다 안전 설정을 확인한 뒤에 제공합니다. 이 저장소는 아직 준비되지 않아 지금은 푸시, 클론, 변경을 받지 않습니다. OwnGit이 알아서 계속 다시 시도합니다. 원인은 서버 로그에 있습니다.",
	},
	MsgRepoPreparingShort: {
		en: "Preparing",
		ko: "준비 중",
	},
	MsgRepoNoBranches: {
		en: "No branches.",
		ko: "브랜치가 없습니다.",
	},
	MsgRepoNoTags: {
		en: "No tags.",
		ko: "태그가 없습니다.",
	},
	MsgRepoDefaultGone: {
		en: "The default branch no longer exists. Choose another branch, or push one with this name again.",
		ko: "기본 브랜치가 더 이상 없습니다. 다른 브랜치를 고르거나 같은 이름으로 다시 푸시하세요.",
	},
	// The repository list shows this state as a badge, which is a narrow
	// fixed-width element on one line. The full sentence above cannot fit and
	// would be cut off mid-word, so the row states the status only. The
	// repository's own pages keep the full explanation, including what to do
	// about it.
	MsgRepoDefaultGoneShort: {
		en: "Default branch missing",
		ko: "기본 브랜치 없음",
	},
	MsgRepoRefMissing: {
		en: "That branch or tag does not exist in this repository.",
		ko: "그 브랜치나 태그는 이 저장소에 없습니다.",
	},
	MsgRepoRefRetained: {
		en: "This history was kept after the branch was replaced or deleted. It is not a current branch.",
		ko: "브랜치가 교체되거나 삭제된 뒤 보관된 기록입니다. 현재 브랜치가 아닙니다.",
	},
	MsgRepoDetached: {
		en: "Showing a specific revision, not a branch.",
		ko: "브랜치가 아니라 특정 커밋을 보고 있습니다.",
	},
	MsgRepoRetainTitle: {
		en: "Kept history",
		ko: "보관된 기록",
	},
	// Whether a particular entry can be opened or restored depends on what the
	// backend can address, so this says what the list is rather than promising
	// a control that may not be there. The entries carry their own links.
	MsgRepoRetainHelp: {
		en: "Commits kept after a force push or a deleted branch or tag. They are records of past work, not current branches.",
		ko: "강제 푸시나 브랜치, 태그 삭제 뒤에 보관된 커밋입니다. 현재 브랜치가 아니라 지난 작업의 기록입니다.",
	},
	MsgRepoNotFound: {
		en: "That repository does not exist.",
		ko: "그 저장소는 없습니다.",
	},

	// -- code and commits ----------------------------------------------
	MsgCodeEmptyDir: {
		en: "This folder is empty.",
		ko: "이 폴더는 비어 있습니다.",
	},
	MsgCodePathMissing: {
		en: "That path does not exist at this revision.",
		ko: "이 커밋에는 그 경로가 없습니다.",
	},
	MsgCodeBinary: {
		en: "This file is not text, so it is not shown here.",
		ko: "이 파일은 텍스트가 아니어서 여기에 표시하지 않습니다.",
	},
	MsgCodeTruncated: {
		en: "Only the beginning of this file is shown.",
		ko: "이 파일의 앞부분만 표시했습니다.",
	},
	MsgCodeRawLink: {
		en: "Download this file",
		ko: "이 파일 내려받기",
	},
	MsgCodeSubmodule: {
		en: "Submodule",
		ko: "서브모듈",
	},
	MsgCodeSymlink: {
		en: "Symbolic link",
		ko: "심볼릭 링크",
	},
	MsgCommitsEmpty: {
		en: "No commits on this branch.",
		ko: "이 브랜치에는 커밋이 없습니다.",
	},
	MsgCommitNotFound: {
		en: "That commit does not exist in this repository.",
		ko: "그 커밋은 이 저장소에 없습니다.",
	},
	MsgCommitDiffBig: {
		en: "This change is too large to show completely.",
		ko: "변경 내용이 너무 커서 전부 표시하지 못했습니다.",
	},
	MsgCommitDiffNone: {
		en: "The changes for this commit could not be read.",
		ko: "이 커밋의 변경 내용을 읽지 못했습니다.",
	},
	MsgCommitDiffMerge: {
		en: "This is a merge commit. Open a parent commit to see its changes.",
		ko: "병합 커밋입니다. 변경 내용은 부모 커밋에서 확인하세요.",
	},
	MsgCommitBinaryFile: {
		en: "Binary file, no line changes shown.",
		ko: "바이너리 파일이라 줄 단위 변경을 표시하지 않습니다.",
	},
	// A label, not a conclusion. Git's author and committer fields are
	// metadata a client may set freely: the committer date can be earlier than
	// the author date, and two names can share one instant. Naming the field
	// and showing its value lets the reader judge; claiming an amend, a rebase,
	// or "later" would assert more than the data supports.
	// Parallel to MsgAuthoredBy ("Written by" / "작성자"), naming the other
	// identity Git stores. "커밋 기록" is already the commit history label, so
	// the Korean names the person instead.
	MsgCommitCommitter: {
		en: "Committed by",
		ko: "커밋한 사람",
	},

	// -- restore -------------------------------------------------------
	//
	// Restoring writes to a repository, so the wording says plainly what will
	// change, what will be deleted, and what stays untouched. It never claims
	// anything about other people's computers, which OwnGit cannot reach.
	MsgRestoreInvalid: {
		en: "That selection cannot be restored. Check the commit and the branch, then preview again.",
		ko: "그 선택은 되돌릴 수 없습니다. 커밋과 브랜치를 확인한 뒤 다시 미리 보세요.",
	},
	MsgRestoreConflict: {
		// The check compares the branch against the tip that was previewed. Any
		// difference trips it: a new commit, a deletion, a branch recreated
		// elsewhere, or one moved back to an older commit. Naming only the
		// first would describe the wrong event for the others.
		en: "The branch changed after the preview. Preview again before restoring.",
		ko: "미리 보기 뒤에 브랜치가 바뀌었습니다. 다시 미리 본 뒤 되돌리세요.",
	},
	MsgRestoreNoChanges: {
		en: "The branch already matches this selection, so there is nothing to restore.",
		ko: "브랜치가 이미 선택한 내용과 같아서 되돌릴 것이 없습니다.",
	},
	MsgRestoreUnsupported: {
		// The third case is reachable from this screen: choosing selected files
		// while the target is a branch that does not exist yet. Picking files
		// out of a commit is a comparison against the branch's current state,
		// and there is nothing to compare against when the branch is new.
		en: "Part of this selection cannot be restored from the browser. Submodule entries, and paths that would replace files you did not select, have to be handled with Git. Restoring selected files also needs a branch that already exists; choose the whole project to start a new one.",
		ko: "선택한 항목 가운데 일부는 브라우저에서 되돌릴 수 없습니다. 서브모듈 항목과, 선택하지 않은 파일까지 바꾸게 되는 경로는 Git으로 처리해야 합니다. 또한 파일을 골라 되돌리려면 이미 있는 브랜치여야 하니, 새 브랜치를 만들 때는 프로젝트 전체를 고르세요.",
	},
	MsgRestoreFailed: {
		// This is the outcome the backend could not establish. It is reported
		// when publishing the change failed and reading the branch back failed
		// too, so whether the branch moved is unknown. Saying it was left alone
		// would be a promise nothing verified; the reader is told to look.
		en: "Could not confirm the restore. Check the branch before trying again.",
		ko: "되돌리기 결과를 확인하지 못했습니다. 다시 시도하기 전에 브랜치를 확인하세요.",
	},
	MsgRestoreReady: {
		en: "Nothing has changed yet. Read the list below, then restore.",
		ko: "아직 바뀐 것은 없습니다. 아래 목록을 확인한 뒤 되돌리세요.",
	},
	// Restoring onto an existing branch writes a commit. Restoring to a name
	// that is not a branch yet creates it at the selected commit and writes no
	// commit at all, so the generic result states the outcome both cases
	// actually share.
	MsgRestoreSuccess: {
		en: "Restored. The earlier history is still there.",
		ko: "되돌렸습니다. 이전 기록도 그대로 있습니다.",
	},

	MsgRestoreTitle: {
		en: "Restore files",
		ko: "파일 되돌리기",
	},
	MsgRestoreIntro: {
		en: "Choose the commit to take files from and the branch to put them on. Earlier commits are never removed or rewritten.",
		ko: "파일을 가져올 커밋과 그 파일을 올릴 브랜치를 고르세요. 이전 커밋은 지우거나 고쳐 쓰지 않습니다.",
	},
	MsgRestoreOpen: {
		en: "Restore files from here",
		ko: "여기서 파일 되돌리기",
	},
	MsgRestoreOpenFile: {
		en: "Restore this file",
		ko: "이 파일 되돌리기",
	},

	MsgRestoreSourceLabel: {
		en: "Take files from",
		ko: "파일을 가져올 커밋",
	},
	MsgRestoreSourceHelp: {
		en: "Files are read from this commit. The commit itself does not change.",
		ko: "이 커밋에서 파일을 읽습니다. 커밋 자체는 바뀌지 않습니다.",
	},
	MsgRestoreTargetLabel: {
		en: "Put them on",
		ko: "파일을 올릴 브랜치",
	},
	// The last sentence is the boundary users most often get wrong, so it is
	// stated where the branch is chosen rather than only in the result.
	// The two targets behave differently, and the earlier wording described
	// only one of them. An existing branch keeps its own history: the restore
	// commit's parent is that branch's current tip, not the selected commit. A
	// name that does not exist yet is created at the selected commit, so it
	// continues that history instead.
	//
	// It also claimed nothing is removed, which is false. Restoring changes
	// and deletes files in the tree on purpose; what it preserves is the
	// history, and that promise is made where the reader confirms the write.
	MsgRestoreTargetChoose: {
		en: "Choose an existing branch or enter a new name. The restore adds to an existing branch's history; a new branch continues from the selected commit.",
		ko: "기존 브랜치를 고르거나 새 이름을 입력하세요. 기존 브랜치는 현재 기록 뒤에 새 커밋을 추가하고, 새 브랜치는 선택한 커밋의 기록을 이어갑니다.",
	},
	// What this branch choice does is stated by the two messages above it.
	// This one covers the boundary a reader is most likely to get wrong, so it
	// says only what is true either way.
	MsgRestoreTargetHelp: {
		en: "Only this repository changes. Working copies on other computers are not touched; pull to receive the change there.",
		ko: "이 저장소만 바뀝니다. 다른 컴퓨터의 작업 폴더는 건드리지 않으며, 그쪽에서 받으려면 pull 하세요.",
	},
	// "Recreates" was wrong for the general case. This notice appears for any
	// name that is not currently a branch, including a suggested one that
	// never existed, so it says the branch is created rather than implying
	// something is being brought back under its former name.
	MsgRestoreTargetNew: {
		en: "This branch does not exist yet. Restoring creates it from the selected commit's history.",
		ko: "아직 없는 브랜치입니다. 선택한 커밋의 기록을 이어 새로 만듭니다.",
	},
	MsgRestoreTargetEmpty: {
		en: "Choose the branch to restore onto.",
		ko: "되돌릴 브랜치를 고르세요.",
	},

	MsgRestoreScopeLabel: {
		en: "What to restore",
		ko: "되돌릴 범위",
	},
	MsgRestoreModeAll: {
		en: "The whole project",
		ko: "프로젝트 전체",
	},
	MsgRestoreModeAllHelp: {
		en: "Make the branch match this commit. Files the commit does not have are deleted.",
		ko: "브랜치를 이 커밋과 같은 상태로 만듭니다. 이 커밋에 없는 파일은 삭제됩니다.",
	},
	MsgRestoreModeFiles: {
		en: "Selected files",
		ko: "선택한 파일",
	},
	MsgRestoreModeFilesHelp: {
		en: "Change only the files you tick. The rest of the branch stays as it is.",
		ko: "체크한 파일만 바꿉니다. 브랜치의 나머지는 그대로 둡니다.",
	},
	MsgRestoreFilesLabel: {
		en: "Files",
		ko: "파일",
	},
	MsgRestoreFilesInactive: {
		en: "The whole project is selected, so these ticks are not used. They are kept if you switch back.",
		ko: "프로젝트 전체를 선택했기 때문에 이 체크는 사용하지 않습니다. 다시 바꾸면 그대로 남아 있습니다.",
	},
	MsgRestoreFilesHelp: {
		en: "Each row says what restoring that path would do to the branch.",
		ko: "각 줄은 그 경로를 되돌렸을 때 브랜치가 어떻게 바뀌는지 알려 줍니다.",
	},
	MsgRestoreFilesNone: {
		en: "Tick at least one file, or restore the whole project.",
		ko: "파일을 하나 이상 체크하거나 프로젝트 전체를 되돌리세요.",
	},
	MsgRestoreFilesEmpty: {
		en: "There are no files to choose from for this selection.",
		ko: "이 선택으로 고를 수 있는 파일이 없습니다.",
	},
	// The status is what restoring would do to the branch, not what the source
	// commit did. "Deleted" alone would read as a fact about the past.
	MsgRestoreStatusAdded: {
		en: "Will be added",
		ko: "추가 예정",
	},
	MsgRestoreStatusModified: {
		en: "Will be changed",
		ko: "변경 예정",
	},
	MsgRestoreStatusDeleted: {
		en: "Will be deleted",
		ko: "삭제 예정",
	},

	MsgRestorePreviewSubmit: {
		en: "Preview changes",
		ko: "바뀔 내용 미리 보기",
	},
	MsgRestorePreviewTitle: {
		en: "What will change",
		ko: "바뀔 내용",
	},
	MsgRestorePreviewHelp: {
		en: "Read this list before restoring. Only the changes listed here are applied.",
		ko: "되돌리기 전에 이 목록을 확인하세요. 여기 있는 변경만 적용됩니다.",
	},
	MsgRestorePreviewStale: {
		en: "The selection changed after this preview. Preview again before restoring.",
		ko: "미리 보기 뒤에 선택이 바뀌었습니다. 되돌리기 전에 다시 미리 보세요.",
	},
	MsgRestoreDeletesLabel: {
		en: "Files that will be deleted",
		ko: "삭제될 파일",
	},
	// The changed-path list is always complete; only the line by line view is
	// cut, and the sentence says which is which.
	MsgRestoreDiffTruncated: {
		en: "Some file contents were too large to show line by line. The list of changed files is complete.",
		ko: "일부 파일 내용은 너무 커서 줄 단위로는 다 보여주지 못했습니다. 변경되는 파일 목록은 전체입니다.",
	},
	MsgRestoreBinaryFile: {
		en: "Not text, so there is no line by line view",
		ko: "텍스트가 아니어서 줄 단위로 보여주지 않습니다",
	},

	MsgRestoreConfirmLabel: {
		en: "I have read these changes and want to restore them",
		ko: "이 변경 내용을 확인했고 되돌리겠습니다",
	},
	MsgRestoreConfirmHelp: {
		en: "Restoring does not remove or rewrite any commit that is already in this repository.",
		ko: "되돌려도 이 저장소에 이미 있는 커밋은 지우거나 고쳐 쓰지 않습니다.",
	},
	MsgRestoreApplySubmit: {
		en: "Restore",
		ko: "되돌리기",
	},
	MsgRestoreChangeChoice: {
		en: "Change the selection",
		ko: "선택 바꾸기",
	},
	MsgRestoreChangeHelp: {
		en: "Your choices are kept, and OwnGit shows the changes again before anything is restored.",
		ko: "선택한 내용은 그대로 두고, 되돌리기 전에 바뀔 내용을 다시 보여 줍니다.",
	},

	// -- activity ------------------------------------------------------
	MsgActivityTitle: {
		en: "All activity",
		ko: "전체 활동",
	},
	MsgActivityEmpty: {
		en: "No commits recorded yet.",
		ko: "아직 기록된 커밋이 없습니다.",
	},
	MsgActivityIncomplete: {
		en: "This count is incomplete.",
		ko: "이 집계는 완전하지 않습니다.",
	},
	MsgActivityLimit: {
		en: "Counting stopped at this installation's limit, so the real total is higher.",
		ko: "이 설치의 한도에서 집계를 멈췄습니다. 실제 합계는 이보다 많습니다.",
	},
	MsgActivityCounting: {
		en: "Some repositories are still being counted. Reload the page to see the full count.",
		ko: "일부 저장소를 아직 집계하고 있습니다. 전체 집계를 보려면 페이지를 새로 고치세요.",
	},
	MsgActivityCountRepo: {
		en: "This repository is still being counted. Reload the page to see the full count.",
		ko: "이 저장소를 아직 집계하고 있습니다. 전체 집계를 보려면 페이지를 새로 고치세요.",
	},
	MsgActivityPreparing: {
		en: "Repositories that are still being prepared are not counted yet.",
		ko: "아직 준비 중인 저장소는 집계하지 않았습니다.",
	},
	MsgActivityScanFail: {
		en: "Some repositories could not be read while counting.",
		ko: "집계하는 동안 일부 저장소를 읽지 못했습니다.",
	},
	MsgActivityUnavail: {
		en: "Activity is not available.",
		ko: "활동 기록을 표시할 수 없습니다.",
	},
	MsgActivityNotBuilt: {
		en: "The activity index has not been built yet.",
		ko: "활동 색인이 아직 만들어지지 않았습니다.",
	},
	// Two separate statements a reader needs: which day a commit is counted
	// on, and that activity is not a check result. The date is the author's
	// own, so a commit made just after midnight abroad stays on that date here
	// rather than moving to the server's calendar.
	MsgActivityNoChecks: {
		en: "Activity counts commits by their author date, in the time zone the author recorded. It does not mean checks ran or passed.",
		ko: "활동은 커밋의 작성 날짜를 작성자가 기록한 시간대 기준으로 집계합니다. 체크를 실행했거나 통과했다는 뜻은 아닙니다.",
	},

	// -- errors --------------------------------------------------------
	MsgErrNotFound: {
		en: "That page does not exist.",
		ko: "그 페이지는 없습니다.",
	},
	MsgErrForbidden: {
		en: "You do not have access to that.",
		ko: "그 항목에 접근할 수 없습니다.",
	},
	MsgErrBadRequest: {
		en: "That request could not be understood.",
		ko: "요청을 이해할 수 없습니다.",
	},
	MsgErrCSRF: {
		en: "This form expired. Open the page again and resubmit.",
		ko: "이 양식이 만료되었습니다. 페이지를 다시 열고 제출하세요.",
	},
	MsgErrHostRejected: {
		en: "This address is not approved for this installation. Open it from an already approved address.",
		ko: "이 주소는 이 설치에서 승인되지 않았습니다. 이미 승인된 주소로 열어 주세요.",
	},
	MsgErrMethod: {
		en: "That action is not allowed here.",
		ko: "여기서는 그 동작을 사용할 수 없습니다.",
	},
	MsgErrTooLarge: {
		en: "That upload is larger than this installation accepts.",
		ko: "이 설치에서 받을 수 있는 크기를 넘었습니다.",
	},
	MsgErrRateLimited: {
		en: "Too many requests. Try again shortly.",
		ko: "요청이 너무 많습니다. 잠시 후 다시 시도하세요.",
	},
	MsgErrInternal: {
		en: "Something went wrong on the server.",
		ko: "서버에서 문제가 발생했습니다.",
	},
	MsgErrUnavailable: {
		en: "This is temporarily unavailable.",
		ko: "지금은 사용할 수 없습니다.",
	},
	MsgErrGeneric: {
		en: "Something went wrong.",
		ko: "문제가 발생했습니다.",
	},
	MsgErrBackHome: {
		en: "Back to repositories",
		ko: "저장소 목록으로",
	},
}

// Text returns the localized sentence for code. An unknown or empty code
// resolves to the generic error text so a template never renders a raw key.
func Text(lang Lang, code MessageCode) string {
	entry, ok := catalog[code]
	if !ok {
		entry = catalog[MsgErrGeneric]
	}
	if lang == LangKO {
		return entry.ko
	}
	return entry.en
}

// Has reports whether code names a catalog entry.
func Has(code MessageCode) bool {
	_, ok := catalog[code]
	return ok
}

// MissingMessages lists catalog entries without text in one of the languages.
// The package test uses it; it exists so an incomplete translation fails a
// check instead of silently shipping English to Korean readers.
func MissingMessages() []MessageCode {
	var missing []MessageCode
	for code, entry := range catalog {
		if entry.en == "" || entry.ko == "" {
			missing = append(missing, code)
		}
	}
	return missing
}

// bundle renders the whole catalog as the JSON the browser downloads once to
// switch language without leaving the page. It is generated from the same
// catalog the server renders from, so the two can never drift apart.
func bundle() ([]byte, error) {
	byLang := map[string]map[string]string{
		string(LangEN): make(map[string]string, len(catalog)),
		string(LangKO): make(map[string]string, len(catalog)),
	}
	for code, entry := range catalog {
		byLang[string(LangEN)][string(code)] = entry.en
		byLang[string(LangKO)][string(code)] = entry.ko
	}
	return json.Marshal(byLang)
}

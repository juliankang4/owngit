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
	MsgRepoNewTitle    MessageCode = "repo.new.title"
	MsgRepoNameLabel   MessageCode = "repo.new.name"
	MsgRepoNameRules   MessageCode = "repo.new.name_rules"
	MsgRepoDescLabel   MessageCode = "repo.new.description"
	MsgRepoDescHelp    MessageCode = "repo.new.description_help"
	MsgRepoCreate      MessageCode = "repo.new.submit"
	MsgRepoNameEmpty   MessageCode = "repo.new.name_empty"
	MsgRepoNameInvalid MessageCode = "repo.new.name_invalid"
	MsgRepoNameTaken   MessageCode = "repo.new.name_taken"
	MsgRepoNameLong    MessageCode = "repo.new.name_long"
	MsgRepoCreateFail  MessageCode = "repo.new.failed"
	MsgRepoCreated     MessageCode = "repo.new.created"

	MsgRepoEmpty            MessageCode = "repo.empty"
	MsgRepoEmptyPush        MessageCode = "repo.empty.push_hint"
	MsgRepoCloneTitle       MessageCode = "repo.clone.title"
	MsgRepoUnreadable       MessageCode = "repo.unreadable"
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

// Activity.
const (
	MsgActivityTitle      MessageCode = "activity.title"
	MsgActivityEmpty      MessageCode = "activity.empty"
	MsgActivityIncomplete MessageCode = "activity.incomplete"
	MsgActivityLimit      MessageCode = "activity.incomplete.limit"
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
	MsgRepoTabsLabel:  {en: "Repository sections", ko: "저장소 구역"},
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
		en: "Use a folder on this computer's own disk. Settings and the index are stored beside the server, not on a network share.",
		ko: "이 컴퓨터의 디스크에 있는 폴더를 사용하세요. 설정과 색인은 네트워크 공유가 아니라 서버가 있는 컴퓨터에 보관합니다.",
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
		en: "That folder looks like a network share. Repository storage there is not runtime-verified yet, and the database always stays on this computer.",
		ko: "그 폴더는 네트워크 공유로 보입니다. 그곳의 저장소 보관은 아직 실행 환경에서 검증되지 않았으며, 데이터베이스는 항상 이 컴퓨터에 남습니다.",
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
		ko: "Git의 HTTP 서비스를 찾지 못했습니다. HTTP로 복제하거나 푸시할 수 없습니다.",
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
		ko: "복제 주소",
	},
	MsgSettingsCloneHelp: {
		en: "Add the repository name to this address when cloning.",
		ko: "복제할 때 이 주소 뒤에 저장소 이름을 붙이세요.",
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
		ko: "영문자, 숫자, 점, 붙임표, 밑줄을 사용합니다. 이 이름이 복제 주소가 됩니다.",
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
		ko: "영문자, 숫자, 점, 붙임표, 밑줄만 사용하세요.",
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
		ko: "복제 주소",
	},
	MsgRepoUnreadable: {
		en: "This repository's Git data could not be read.",
		ko: "이 저장소의 Git 데이터를 읽지 못했습니다.",
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
		ko: "브랜치가 아니라 특정 리비전을 보고 있습니다.",
	},
	MsgRepoRetainTitle: {
		en: "Kept history",
		ko: "보관된 기록",
	},
	MsgRepoRetainHelp: {
		en: "Commits kept after a force push or a deleted branch or tag. Restoring them from the browser is not available yet.",
		ko: "강제 푸시나 브랜치, 태그 삭제 뒤에 보관된 커밋입니다. 브라우저에서 되돌리는 기능은 아직 없습니다.",
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
		ko: "이 리비전에는 그 경로가 없습니다.",
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
		ko: "활동은 커밋의 작성 날짜를 작성자가 기록한 시간대 기준으로 집계합니다. 검사를 실행했거나 통과했다는 뜻은 아닙니다.",
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

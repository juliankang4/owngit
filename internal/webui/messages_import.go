package webui

const (
	MsgImportTab                MessageCode = "import.tab"
	MsgImportTitle              MessageCode = "import.title"
	MsgImportIntro              MessageCode = "import.intro"
	MsgImportUnavailable        MessageCode = "import.unavailable"
	MsgImportNotConfigured      MessageCode = "import.not_configured"
	MsgImportURL                MessageCode = "import.url"
	MsgImportMode               MessageCode = "import.mode"
	MsgImportModeStandalone     MessageCode = "import.mode.standalone"
	MsgImportModeCoexistence    MessageCode = "import.mode.coexistence"
	MsgImportGitOnly            MessageCode = "import.git_only"
	MsgImportGitOnlyHelp        MessageCode = "import.git_only_help"
	MsgImportPrivateNetwork     MessageCode = "import.private_network"
	MsgImportPrivateNetworkHelp MessageCode = "import.private_network_help"
	MsgImportSaveSource         MessageCode = "import.save_source"
	MsgImportCredentials        MessageCode = "import.credentials"
	MsgImportCredentialsHelp    MessageCode = "import.credentials_help"
	MsgImportCredentialForm     MessageCode = "import.credential_form"
	MsgImportCredentialNone     MessageCode = "import.credential_none"
	MsgImportCredentialBasic    MessageCode = "import.credential_basic"
	MsgImportCredentialBearer   MessageCode = "import.credential_bearer"
	MsgImportUsername           MessageCode = "import.username"
	MsgImportPassword           MessageCode = "import.password"
	MsgImportToken              MessageCode = "import.token"
	MsgImportCA                 MessageCode = "import.ca"
	MsgImportSaveCredentials    MessageCode = "import.save_credentials"
	MsgImportClearCredentials   MessageCode = "import.clear_credentials"
	MsgImportNothingToSave      MessageCode = "import.nothing_to_save"
	MsgImportBound              MessageCode = "import.bound"
	MsgImportUnbound            MessageCode = "import.unbound"
	MsgImportCAPresent          MessageCode = "import.ca_present"
	MsgImportLastRun            MessageCode = "import.last_run"
	MsgImportActiveRun          MessageCode = "import.active_run"
	MsgImportNoRun              MessageCode = "import.no_run"
	MsgImportRefresh            MessageCode = "import.refresh"
	MsgImportCancel             MessageCode = "import.cancel"
	MsgImportSchedule           MessageCode = "import.schedule"
	MsgImportScheduleEnabled    MessageCode = "import.schedule_enabled"
	MsgImportScheduleDisabled   MessageCode = "import.schedule_disabled"
	MsgImportInterval           MessageCode = "import.interval"
	MsgImportIntervalHelp       MessageCode = "import.interval_help"
	MsgImportSaveSchedule       MessageCode = "import.save_schedule"
	MsgImportHistory            MessageCode = "import.history"
	MsgImportOlder              MessageCode = "import.older"
	MsgImportRefs               MessageCode = "import.refs"
	MsgImportRefsTruncated      MessageCode = "import.refs_truncated"
	MsgImportUnresolved         MessageCode = "import.unresolved"
	MsgImportStaging            MessageCode = "import.staging"
	MsgImportIncomplete         MessageCode = "import.incomplete"
	MsgImportNewTitle           MessageCode = "import.new.title"
	MsgImportNewIntro           MessageCode = "import.new.intro"
	MsgImportNewSubmit          MessageCode = "import.new.submit"
	MsgImportPasswordEachTime   MessageCode = "import.password_each_time"
	MsgImportSaved              MessageCode = "import.saved"
	MsgImportRefreshed          MessageCode = "import.refreshed"
	MsgImportCancelled          MessageCode = "import.cancelled"
	MsgImportCancelNone         MessageCode = "import.cancel_none"
	MsgImportCredentialsSaved   MessageCode = "import.credentials_saved"
	MsgImportCredentialsCleared MessageCode = "import.credentials_cleared"
	MsgImportScheduleSaved      MessageCode = "import.schedule_saved"
	MsgImportStarted            MessageCode = "import.started"
	MsgImportFailed             MessageCode = "import.failed"
	MsgImportName               MessageCode = "import.name"
	MsgImportDescription        MessageCode = "import.description"
	MsgImportListLink           MessageCode = "import.list_link"

	MsgImportResolveHelp      MessageCode = "import.resolve_help"
	MsgImportResolve          MessageCode = "import.resolve"
	MsgImportResolved         MessageCode = "import.resolved"
	MsgImportSchedulerFailed  MessageCode = "import.scheduler_failed"
	MsgImportReconcileFailed  MessageCode = "import.reconcile_failed"
	MsgImportErrorNothing     MessageCode = "import.error.nothing_to_resolve"
	MsgImportRunCancelled     MessageCode = "import.run_cancelled"
	MsgImportCancelledNoRepo  MessageCode = "import.cancelled_no_repository"
	MsgImportTechnicalDetails MessageCode = "import.technical_details"

	MsgImportErrorInvalidSource MessageCode = "import.error.invalid_source"
	MsgImportErrorBusy          MessageCode = "import.error.busy"
	MsgImportErrorRepoMissing   MessageCode = "import.error.repository_missing"
	MsgImportErrorRepoTaken     MessageCode = "import.error.repository_taken"
	MsgImportErrorObjectFormat  MessageCode = "import.error.unsupported_object_format"
	MsgImportErrorRefs          MessageCode = "import.error.unsupported_refs"
	MsgImportErrorNetwork       MessageCode = "import.error.network"
	MsgImportErrorProtocol      MessageCode = "import.error.protocol"
	MsgImportErrorTooLarge      MessageCode = "import.error.too_large"
	MsgImportErrorIndex         MessageCode = "import.error.index_failed"
	MsgImportErrorVerify        MessageCode = "import.error.verify_failed"
	MsgImportErrorPublish       MessageCode = "import.error.publish_failed"
	MsgImportErrorLFS           MessageCode = "import.error.git_lfs_required"
	MsgImportErrorDestination   MessageCode = "import.error.destination_changed"
	MsgImportErrorUnresolved    MessageCode = "import.error.publication_unresolved"
	MsgImportErrorSuperseded    MessageCode = "import.error.superseded"
	MsgImportErrorInterrupted   MessageCode = "import.error.interrupted"
	MsgImportErrorState         MessageCode = "import.error.state_unavailable"
	MsgImportErrorRuntime       MessageCode = "import.error.runtime"
	MsgImportErrorUnsupported   MessageCode = "import.error.unsupported"
	MsgImportErrorLimit         MessageCode = "import.error.limit"

	MsgImportKindInitial       MessageCode = "import.kind.initial"
	MsgImportKindRefresh       MessageCode = "import.kind.refresh"
	MsgImportKindScheduled     MessageCode = "import.kind.scheduled"
	MsgImportStatusPreparing   MessageCode = "import.status.preparing"
	MsgImportStatusFetching    MessageCode = "import.status.fetching"
	MsgImportStatusIndexing    MessageCode = "import.status.indexing"
	MsgImportStatusInspecting  MessageCode = "import.status.inspecting"
	MsgImportStatusPublishing  MessageCode = "import.status.publishing"
	MsgImportStatusComplete    MessageCode = "import.status.complete"
	MsgImportStatusFailed      MessageCode = "import.status.failed"
	MsgImportStatusCancelled   MessageCode = "import.status.cancelled"
	MsgImportStatusSuperseded  MessageCode = "import.status.superseded"
	MsgImportStatusInterrupted MessageCode = "import.status.interrupted"
	MsgImportStatusUnresolved  MessageCode = "import.status.unresolved"
	MsgImportRefTracked        MessageCode = "import.ref.tracked"
	MsgImportRefDiverged       MessageCode = "import.ref.diverged"
	MsgImportRefAbsent         MessageCode = "import.ref.absent_locally"
	MsgImportRefEarlier        MessageCode = "import.ref.earlier_source"
	MsgImportRefUnknown        MessageCode = "import.ref.unknown_local"
	MsgImportRefDeleted        MessageCode = "import.ref.deleted_at_source"
)

var importCatalog = map[MessageCode]message{
	MsgImportTab:                {en: "Import", ko: "가져오기"},
	MsgImportTitle:              {en: "Import", ko: "가져오기"},
	MsgImportIntro:              {en: "OwnGit keeps its own copy. It never writes to the source.", ko: "OwnGit은 자기 복사본을 둡니다. 원본에는 쓰지 않습니다."},
	MsgImportUnavailable:        {en: "Import is not available in this process.", ko: "이 프로세스에서는 가져오기를 사용할 수 없습니다."},
	MsgImportNotConfigured:      {en: "This repository has no import source.", ko: "이 저장소에는 가져오기 원본이 없습니다."},
	MsgImportURL:                {en: "Source URL", ko: "원본 주소"},
	MsgImportMode:               {en: "Mode", ko: "방식"},
	MsgImportModeStandalone:     {en: "Standalone", ko: "독립"},
	MsgImportModeCoexistence:    {en: "Coexistence", ko: "공존"},
	MsgImportGitOnly:            {en: "Accept Git-only content", ko: "Git 내용만 받기"},
	MsgImportGitOnlyHelp:        {en: "Git LFS pointers stay in the copy and the content is marked incomplete. OwnGit does not download LFS objects.", ko: "Git LFS 포인터는 복사본에 남고 내용은 불완전으로 표시됩니다. OwnGit은 LFS 객체를 받지 않습니다."},
	MsgImportPrivateNetwork:     {en: "Allow a private-network source", ko: "사설망 원본 허용"},
	MsgImportPrivateNetworkHelp: {en: "Required only when the source address is on a private network.", ko: "원본 주소가 사설망일 때만 필요합니다."},
	MsgImportSaveSource:         {en: "Save source", ko: "원본 저장"},
	MsgImportCredentials:        {en: "Credentials", ko: "인증 정보"},
	MsgImportCredentialsHelp:    {en: "The password or token is sent once and is not shown again.", ko: "비밀번호나 토큰은 한 번만 전송되고 다시 표시되지 않습니다."},
	MsgImportCredentialForm:     {en: "Credential form", ko: "인증 형식"},
	MsgImportCredentialNone:     {en: "None", ko: "없음"},
	MsgImportCredentialBasic:    {en: "Username and password", ko: "사용자 이름과 비밀번호"},
	MsgImportCredentialBearer:   {en: "Access token", ko: "액세스 토큰"},
	MsgImportUsername:           {en: "Username", ko: "사용자 이름"},
	MsgImportPassword:           {en: "Password", ko: "비밀번호"},
	MsgImportToken:              {en: "Token", ko: "토큰"},
	MsgImportCA:                 {en: "Source CA PEM", ko: "원본 CA PEM"},
	MsgImportSaveCredentials:    {en: "Save credentials", ko: "인증 정보 저장"},
	MsgImportClearCredentials:   {en: "Clear credentials", ko: "인증 정보 지우기"},
	MsgImportNothingToSave:      {en: "Nothing was saved because no credential or CA was entered. The stored credential is unchanged. To remove it, use Clear credentials.", ko: "인증 정보나 CA를 입력하지 않아 아무것도 저장하지 않았습니다. 저장된 인증 정보는 그대로입니다. 지우려면 인증 정보 지우기를 사용하세요."},
	MsgImportBound:              {en: "A credential is bound to this source.", ko: "인증 정보가 이 원본에 연결되어 있습니다."},
	MsgImportUnbound:            {en: "No credential is bound.", ko: "연결된 인증 정보가 없습니다."},
	MsgImportCAPresent:          {en: "A source CA is stored.", ko: "원본 CA가 저장되어 있습니다."},
	MsgImportLastRun:            {en: "Last run", ko: "마지막 실행"},
	MsgImportActiveRun:          {en: "Active run", ko: "진행 중인 실행"},
	MsgImportNoRun:              {en: "No import run is recorded.", ko: "기록된 가져오기 실행이 없습니다."},
	MsgImportRefresh:            {en: "Refresh", ko: "새로고침"},
	MsgImportCancel:             {en: "Cancel", ko: "취소"},
	MsgImportSchedule:           {en: "Schedule", ko: "예약"},
	MsgImportScheduleEnabled:    {en: "Enabled", ko: "사용"},
	MsgImportScheduleDisabled:   {en: "Disabled", ko: "중지"},
	MsgImportInterval:           {en: "Interval", ko: "간격"},
	MsgImportIntervalHelp:       {en: "Use a duration from 60s to 168h, for example 1h.", ko: "60s에서 168h 사이의 간격을 입력합니다. 예: 1h."},
	MsgImportSaveSchedule:       {en: "Save schedule", ko: "예약 저장"},
	MsgImportHistory:            {en: "History", ko: "기록"},
	MsgImportOlder:              {en: "Older runs", ko: "이전 실행"},
	MsgImportRefs:               {en: "Observed refs", ko: "관측된 ref"},
	MsgImportRefsTruncated:      {en: "The ref list is truncated.", ko: "ref 목록이 잘렸습니다."},
	MsgImportUnresolved:         {en: "Unresolved publication intents need attention.", ko: "해결되지 않은 게시 의도를 확인해야 합니다."},
	MsgImportStaging:            {en: "Staging issues are preserved.", ko: "스테이징 문제가 보존되어 있습니다."},
	MsgImportIncomplete:         {en: "Accepted content is incomplete.", ko: "받은 내용이 불완전합니다."},
	MsgImportNewTitle:           {en: "Import a repository", ko: "저장소 가져오기"},
	MsgImportNewIntro:           {en: "Create a new OwnGit repository from an HTTPS Git source.", ko: "HTTPS Git 원본에서 새 OwnGit 저장소를 만듭니다."},
	MsgImportNewSubmit:          {en: "Start import", ko: "가져오기 시작"},
	MsgImportPasswordEachTime:   {en: "Enter the administrator password for this change.", ko: "이 변경에는 관리자 비밀번호가 필요합니다."},
	MsgImportSaved:              {en: "Import source saved.", ko: "가져오기 원본을 저장했습니다."},
	MsgImportRefreshed:          {en: "Refresh finished.", ko: "새로고침이 끝났습니다."},
	MsgImportCancelled:          {en: "Cancellation requested.", ko: "취소를 요청했습니다."},
	MsgImportCancelNone:         {en: "No active import to cancel.", ko: "취소할 진행 중인 가져오기가 없습니다."},
	MsgImportCredentialsSaved:   {en: "Credential state saved. The secret is not shown.", ko: "인증 상태를 저장했습니다. 비밀 값은 표시하지 않습니다."},
	MsgImportCredentialsCleared: {en: "Credentials cleared.", ko: "인증 정보를 지웠습니다."},
	MsgImportScheduleSaved:      {en: "Schedule saved.", ko: "예약을 저장했습니다."},
	MsgImportStarted:            {en: "Import started.", ko: "가져오기를 시작했습니다."},
	MsgImportFailed:             {en: "Import did not finish successfully.", ko: "가져오기가 성공적으로 끝나지 않았습니다."},
	MsgImportResolveHelp:        {en: "OwnGit could not confirm how an earlier publication ended, so refreshes are paused. Check the repository. If its current branches, tags, and HEAD are acceptable, accept them as they are. OwnGit does not change any ref for this. The next refresh plans from the repository as it is now, and it leaves refs that differ from both the source and the last confirmed import alone.", ko: "이전 게시가 어떻게 끝났는지 OwnGit이 확인하지 못해 새로고침을 멈췄습니다. 저장소를 확인하세요. 현재 브랜치, 태그, HEAD를 그대로 써도 된다면 현재 상태로 인정하세요. 이 작업은 ref를 바꾸지 않습니다. 다음 새로고침은 지금 저장소 상태를 기준으로 계획하며, 원본과도 마지막으로 확인된 가져오기와도 다른 ref는 그대로 둡니다."},
	MsgImportResolve:            {en: "Accept the current repository state", ko: "현재 저장소 상태 인정"},
	MsgImportResolved:           {en: "The current repository state was accepted. Refresh is available again.", ko: "현재 저장소 상태를 인정했습니다. 다시 새로고침할 수 있습니다."},
	MsgImportSchedulerFailed:    {en: "The import scheduler did not start. Scheduled refreshes do not run until OwnGit restarts. Ordinary Git service is available.", ko: "가져오기 예약 실행기가 시작되지 않았습니다. OwnGit을 다시 시작할 때까지 예약 새로고침은 실행되지 않습니다. 일반 Git 서비스는 사용할 수 있습니다."},
	MsgImportReconcileFailed:    {en: "Import startup checks did not finish. Ordinary Git service is available.", ko: "가져오기 시작 점검이 끝나지 않았습니다. 일반 Git 서비스는 사용할 수 있습니다."},
	MsgImportErrorNothing:       {en: "There is no unresolved publication to accept.", ko: "인정할 미해결 게시가 없습니다."},
	MsgImportRunCancelled:       {en: "The import run was cancelled.", ko: "가져오기 실행이 취소되었습니다."},
	MsgImportCancelledNoRepo:    {en: "The import was cancelled before the repository was created. No repository was added.", ko: "저장소가 만들어지기 전에 가져오기가 취소되었습니다. 추가된 저장소는 없습니다."},
	MsgImportTechnicalDetails:   {en: "Technical details", ko: "기술 세부 정보"},

	MsgImportErrorInvalidSource: {en: "The source address or settings are not valid.", ko: "원본 주소나 설정이 올바르지 않습니다."},
	MsgImportErrorBusy:          {en: "Another import run is active for this repository.", ko: "이 저장소에서 다른 가져오기가 진행 중입니다."},
	MsgImportErrorRepoMissing:   {en: "The destination repository could not be found.", ko: "대상 저장소를 찾을 수 없습니다."},
	MsgImportErrorRepoTaken:     {en: "A repository with this name already exists.", ko: "같은 이름의 저장소가 이미 있습니다."},
	MsgImportErrorObjectFormat:  {en: "The source uses an object format that this installation cannot import.", ko: "원본이 이 설치에서 가져올 수 없는 객체 형식을 사용합니다."},
	MsgImportErrorRefs:          {en: "The source has refs that cannot be imported safely.", ko: "원본에 안전하게 가져올 수 없는 ref가 있습니다."},
	MsgImportErrorNetwork:       {en: "The source could not be reached.", ko: "원본에 연결할 수 없었습니다."},
	MsgImportErrorProtocol:      {en: "The source sent a response that OwnGit could not accept.", ko: "원본이 OwnGit이 받아들일 수 없는 응답을 보냈습니다."},
	MsgImportErrorTooLarge:      {en: "The source is larger than the import limits.", ko: "원본이 가져오기 한도보다 큽니다."},
	MsgImportErrorIndex:         {en: "The received pack could not be indexed.", ko: "받은 팩을 색인하지 못했습니다."},
	MsgImportErrorVerify:        {en: "The received content did not pass verification.", ko: "받은 내용이 검증을 통과하지 못했습니다."},
	MsgImportErrorPublish:       {en: "The imported refs could not be published.", ko: "가져온 ref를 게시하지 못했습니다."},
	MsgImportErrorLFS:           {en: "The source uses Git LFS. Accept Git-only content to import it.", ko: "원본이 Git LFS를 사용합니다. 가져오려면 Git 내용만 받기를 선택하세요."},
	MsgImportErrorDestination:   {en: "The destination repository changed during the import.", ko: "가져오는 동안 대상 저장소가 바뀌었습니다."},
	MsgImportErrorUnresolved:    {en: "The publication result is uncertain and needs attention.", ko: "게시 결과가 확실하지 않아 확인이 필요합니다."},
	MsgImportErrorSuperseded:    {en: "A newer source setting replaced this run.", ko: "새 원본 설정이 이 실행을 대신했습니다."},
	MsgImportErrorInterrupted:   {en: "The run was interrupted when OwnGit stopped.", ko: "OwnGit이 멈추면서 실행이 중단되었습니다."},
	MsgImportErrorState:         {en: "OwnGit could not read or record import state.", ko: "OwnGit이 가져오기 상태를 읽거나 기록하지 못했습니다."},
	MsgImportErrorRuntime:       {en: "The import workspace is not available.", ko: "가져오기 작업 공간을 사용할 수 없습니다."},
	MsgImportErrorUnsupported:   {en: "The source or destination uses a feature that import does not support.", ko: "원본이나 대상이 가져오기에서 지원하지 않는 기능을 사용합니다."},
	MsgImportErrorLimit:         {en: "The import reached its time limit.", ko: "가져오기가 시간 제한에 도달했습니다."},
	MsgImportName:               {en: "Name", ko: "이름"},
	MsgImportDescription:        {en: "Description", ko: "설명"},
	MsgImportListLink:           {en: "Import a repository", ko: "저장소 가져오기"},
	MsgImportKindInitial:        {en: "Initial", ko: "처음 가져오기"},
	MsgImportKindRefresh:        {en: "Refresh", ko: "새로고침"},
	MsgImportKindScheduled:      {en: "Scheduled", ko: "예약"},
	MsgImportStatusPreparing:    {en: "Preparing", ko: "준비 중"},
	MsgImportStatusFetching:     {en: "Fetching", ko: "받는 중"},
	MsgImportStatusIndexing:     {en: "Indexing", ko: "색인 중"},
	MsgImportStatusInspecting:   {en: "Inspecting", ko: "검사 중"},
	MsgImportStatusPublishing:   {en: "Publishing", ko: "게시 중"},
	MsgImportStatusComplete:     {en: "Complete", ko: "완료"},
	MsgImportStatusFailed:       {en: "Failed", ko: "실패"},
	MsgImportStatusCancelled:    {en: "Cancelled", ko: "취소됨"},
	MsgImportStatusSuperseded:   {en: "Superseded", ko: "대체됨"},
	MsgImportStatusInterrupted:  {en: "Interrupted", ko: "중단됨"},
	MsgImportStatusUnresolved:   {en: "Unresolved", ko: "미해결"},
	MsgImportRefTracked:         {en: "Tracked", ko: "원본과 같음"},
	MsgImportRefDiverged:        {en: "Diverged", ko: "원본과 다름"},
	MsgImportRefAbsent:          {en: "Absent locally", ko: "OwnGit에 없음"},
	MsgImportRefEarlier:         {en: "Earlier source", ko: "예전 원본 기록"},
	MsgImportRefUnknown:         {en: "Local unknown", ko: "OwnGit 쪽 확인 못 함"},
	MsgImportRefDeleted:         {en: "Deleted at source", ko: "원본에서 삭제됨"},
}

func importToken(lang Lang, token string) string {
	code, ok := importTokenCode(token)
	if !ok {
		return token
	}
	return Text(lang, code)
}

// ImportErrorCode maps a stable import error class to its localized
// explanation. An unknown class falls back to the generic failure text, so
// the page never shows a raw internal token as the only explanation.
func ImportErrorCode(class string) MessageCode {
	switch class {
	case "invalid_source":
		return MsgImportErrorInvalidSource
	case "not_configured":
		return MsgImportNotConfigured
	case "busy":
		return MsgImportErrorBusy
	case "repository_missing":
		return MsgImportErrorRepoMissing
	case "repository_taken":
		return MsgImportErrorRepoTaken
	case "unsupported_object_format":
		return MsgImportErrorObjectFormat
	case "unsupported_refs":
		return MsgImportErrorRefs
	case "network":
		return MsgImportErrorNetwork
	case "protocol":
		return MsgImportErrorProtocol
	case "too_large":
		return MsgImportErrorTooLarge
	case "index_failed":
		return MsgImportErrorIndex
	case "verify_failed":
		return MsgImportErrorVerify
	case "publish_failed":
		return MsgImportErrorPublish
	case "git_lfs_required":
		return MsgImportErrorLFS
	case "destination_changed":
		return MsgImportErrorDestination
	case "publication_unresolved":
		return MsgImportErrorUnresolved
	case "cancelled":
		return MsgImportRunCancelled
	case "superseded":
		return MsgImportErrorSuperseded
	case "interrupted":
		return MsgImportErrorInterrupted
	case "state_unavailable":
		return MsgImportErrorState
	case "runtime_unavailable", "runtime_unsafe", "staging_unsafe":
		return MsgImportErrorRuntime
	case "unsupported":
		return MsgImportErrorUnsupported
	case "limit":
		return MsgImportErrorLimit
	case "nothing_to_resolve":
		return MsgImportErrorNothing
	default:
		return MsgImportFailed
	}
}

func importTokenCode(token string) (MessageCode, bool) {
	switch token {
	case "initial":
		return MsgImportKindInitial, true
	case "refresh":
		return MsgImportKindRefresh, true
	case "scheduled":
		return MsgImportKindScheduled, true
	case "preparing":
		return MsgImportStatusPreparing, true
	case "fetching":
		return MsgImportStatusFetching, true
	case "indexing":
		return MsgImportStatusIndexing, true
	case "inspecting":
		return MsgImportStatusInspecting, true
	case "publishing":
		return MsgImportStatusPublishing, true
	case "complete":
		return MsgImportStatusComplete, true
	case "failed":
		return MsgImportStatusFailed, true
	case "cancelled":
		return MsgImportStatusCancelled, true
	case "superseded":
		return MsgImportStatusSuperseded, true
	case "interrupted":
		return MsgImportStatusInterrupted, true
	case "unresolved":
		return MsgImportStatusUnresolved, true
	case "tracked":
		return MsgImportRefTracked, true
	case "diverged":
		return MsgImportRefDiverged, true
	case "absent_locally":
		return MsgImportRefAbsent, true
	case "earlier_source":
		return MsgImportRefEarlier, true
	case "unknown_local":
		return MsgImportRefUnknown, true
	case "deleted_at_source":
		return MsgImportRefDeleted, true
	case "basic":
		return MsgImportCredentialBasic, true
	case "bearer":
		return MsgImportCredentialBearer, true
	case "none":
		return MsgImportCredentialNone, true
	case "standalone":
		return MsgImportModeStandalone, true
	case "coexistence":
		return MsgImportModeCoexistence, true
	default:
		return "", false
	}
}

func init() {
	for code, entry := range importCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

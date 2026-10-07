package webui

const (
	MsgImportTab                MessageCode = "import.tab"
	MsgImportTitle              MessageCode = "import.title"
	MsgImportIntro              MessageCode = "import.intro"
	MsgImportStatusUnreadable   MessageCode = "import.status_unreadable"
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
	MsgImportHistoryUnavailable MessageCode = "import.history_unavailable"
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
	MsgImportCancelledUnsure  MessageCode = "import.cancelled_unconfirmed"
	MsgImportTechnicalDetails MessageCode = "import.technical_details"

	// The Import tab's read-only status and its set-up step.
	MsgImportStatusHeading         MessageCode = "import.status_heading"
	MsgImportSource                MessageCode = "import.source"
	MsgImportAdminOnly             MessageCode = "import.admin_only"
	MsgImportAutoRefresh           MessageCode = "import.auto_refresh"
	MsgImportEvery                 MessageCode = "import.every"
	MsgImportSetUp                 MessageCode = "import.set_up"
	MsgImportSetUpIntro            MessageCode = "import.set_up_intro"
	MsgImportNotConfiguredHelp     MessageCode = "import.not_configured_help"
	MsgImportChangeSource          MessageCode = "import.change_source"
	MsgImportChangeSourceIntro     MessageCode = "import.change_source_intro"
	MsgImportChangeSettings        MessageCode = "import.change_settings"
	MsgImportModeHelp              MessageCode = "import.mode.help"
	MsgImportModeStandaloneHelp    MessageCode = "import.mode.standalone_help"
	MsgImportModeCoexistHelp       MessageCode = "import.mode.coexistence_help"
	MsgImportURLHelp               MessageCode = "import.url_help"
	MsgImportURLRequired           MessageCode = "import.url_required"
	MsgImportURLHTTPS              MessageCode = "import.url_https"
	MsgImportURLUser               MessageCode = "import.url_user"
	MsgImportURLQuery              MessageCode = "import.url_query"
	MsgImportNameHelp              MessageCode = "import.name_help"
	MsgImportCAHelp                MessageCode = "import.ca_help"
	MsgImportBasicNeedsBoth        MessageCode = "import.basic_needs_both"
	MsgImportTokenRequired         MessageCode = "import.token_required"
	MsgImportCredentialFormUnknown MessageCode = "import.credential_form_unknown"
	MsgImportCredentialKeep        MessageCode = "import.credential_keep"
	MsgImportCredentialSaveHelp    MessageCode = "import.credential_save_help"
	MsgImportCAOnly                MessageCode = "import.ca_only"
	MsgImportNoRefs                MessageCode = "import.no_refs"

	MsgImportErrorInvalidSource MessageCode = "import.error.invalid_source"
	MsgImportErrorBusy          MessageCode = "import.error.busy"
	MsgImportErrorRepoMissing   MessageCode = "import.error.repository_missing"
	MsgImportErrorRepoTaken     MessageCode = "import.error.repository_taken"
	MsgImportErrorObjectFormat  MessageCode = "import.error.unsupported_object_format"
	MsgImportErrorRefs          MessageCode = "import.error.unsupported_refs"
	MsgImportErrorNetwork       MessageCode = "import.error.network"
	MsgImportErrorProtocol      MessageCode = "import.error.protocol"
	MsgImportErrorTooLarge      MessageCode = "import.error.too_large"
	MsgImportErrorTooManyRefs   MessageCode = "import.error.too_many_refs"
	MsgImportErrorIndex         MessageCode = "import.error.index_failed"
	MsgImportErrorVerify        MessageCode = "import.error.verify_failed"
	MsgImportErrorPublish       MessageCode = "import.error.publish_failed"
	MsgImportErrorLFS           MessageCode = "import.error.git_lfs_required"
	MsgImportErrorDestination   MessageCode = "import.error.destination_changed"
	MsgImportErrorProtected     MessageCode = "import.error.protected_default_branch"
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

	MsgImportOptions            MessageCode = "import.options"
	MsgImportOptionsHelp        MessageCode = "import.options_help"
	MsgImportOptionsUnreadable  MessageCode = "import.options_unreadable"
	MsgImportPlainHTTP          MessageCode = "import.plain_http"
	MsgImportPlainHTTPHelp      MessageCode = "import.plain_http_help"
	MsgImportRedirects          MessageCode = "import.redirects"
	MsgImportRedirectsHelp      MessageCode = "import.redirects_help"
	MsgImportRedirectRefuse     MessageCode = "import.redirect.refuse"
	MsgImportRedirectSame       MessageCode = "import.redirect.same_origin"
	MsgImportRedirectApproved   MessageCode = "import.redirect.approved"
	MsgImportRedirectOrigin     MessageCode = "import.redirect.origin"
	MsgImportRedirectOriginHelp MessageCode = "import.redirect.origin_help"
	MsgImportReserved           MessageCode = "import.reserved"
	MsgImportReservedHelp       MessageCode = "import.reserved_help"
	MsgImportLimits             MessageCode = "import.limits"
	MsgImportLimitsHelp         MessageCode = "import.limits_help"
	MsgImportLimitsMore         MessageCode = "import.limits_more"
	MsgImportLimitPack          MessageCode = "import.limit.pack"
	MsgImportLimitRun           MessageCode = "import.limit.run"
	MsgImportLimitFetch         MessageCode = "import.limit.fetch"
	MsgImportLimitIndex         MessageCode = "import.limit.index"
	MsgImportLimitVerify        MessageCode = "import.limit.verify"
	MsgImportLimitRefs          MessageCode = "import.limit.refs"
	MsgImportLimitAdvertisement MessageCode = "import.limit.advertisement"
	MsgImportLimitTLS           MessageCode = "import.limit.tls"
	MsgImportLimitHeaders       MessageCode = "import.limit.headers"
	MsgImportLimitLFS           MessageCode = "import.limit.lfs"
	MsgImportLimitWholeSeconds  MessageCode = "import.limit.whole_seconds"
	MsgImportLimitRange         MessageCode = "import.limit.range"
	MsgImportFactConnection     MessageCode = "import.fact.connection"
	MsgImportFactDefaults       MessageCode = "import.fact.defaults"
	MsgImportFactPlainHTTP      MessageCode = "import.fact.plain_http"
	MsgImportFactSameOrigin     MessageCode = "import.fact.same_origin"
	MsgImportFactApproved       MessageCode = "import.fact.approved"
	MsgImportFactReserved       MessageCode = "import.fact.reserved"
	MsgImportFactLimits         MessageCode = "import.fact.limits"
	MsgImportOriginInvalid      MessageCode = "import.origin_invalid"
	MsgImportURLPlainHTTP       MessageCode = "import.url_plain_http"

	MsgImportRefusedPrivate   MessageCode = "import.error.address_needs_private_network"
	MsgImportRefusedException MessageCode = "import.error.address_needs_exceptional_destination"
	MsgImportRefusedAddress   MessageCode = "import.error.address_refused"
	MsgImportRefusedRedirect  MessageCode = "import.error.redirect_not_allowed"
	MsgImportRefusedPlain     MessageCode = "import.error.redirect_needs_plain_http"
	MsgImportRefusalDetails   MessageCode = "import.refusal_details"
	MsgImportTransportReset   MessageCode = "import.transport_reset"

	MsgImportRefreshChoices       MessageCode = "import.refresh_choices"
	MsgImportRefreshChoicesHelp   MessageCode = "import.refresh_choices_help"
	MsgImportExtraRefs            MessageCode = "import.extra_refs"
	MsgImportExtraRefsHelp        MessageCode = "import.extra_refs_help"
	MsgImportExtraRefsWarning     MessageCode = "import.extra_refs_warning"
	MsgImportExtraRefsInvalid     MessageCode = "import.extra_refs_invalid"
	MsgImportOverwrite            MessageCode = "import.overwrite_diverged"
	MsgImportOverwriteHelp        MessageCode = "import.overwrite_diverged_help"
	MsgImportFollowDeletions      MessageCode = "import.follow_deletions"
	MsgImportFollowDeletionsHelp  MessageCode = "import.follow_deletions_help"
	MsgImportRefreshWarning       MessageCode = "import.refresh_warning"
	MsgImportEffectsHeading       MessageCode = "import.effects_heading"
	MsgImportEffectsNone          MessageCode = "import.effects_none"
	MsgImportEffectReplace        MessageCode = "import.effect.replace"
	MsgImportEffectDelete         MessageCode = "import.effect.delete"
	MsgImportEffectNeedsOverwrite MessageCode = "import.effect.needs_overwrite"
	MsgImportEffectNeedsFollow    MessageCode = "import.effect.needs_follow"
	MsgImportEffectNeedsBoth      MessageCode = "import.effect.needs_both"
	MsgImportEffectKept           MessageCode = "import.effect.kept"
	MsgImportEffectNotKept        MessageCode = "import.effect.not_kept"
	MsgImportEffectsUnknown       MessageCode = "import.effects_unknown"
	MsgImportEffectRefused        MessageCode = "import.effect.refused"
	MsgImportEffectRefusedHelp    MessageCode = "import.effect.refused_help"
	MsgImportFactRefresh          MessageCode = "import.fact.refresh"
	MsgImportFactExtraRefs        MessageCode = "import.fact.extra_refs"
	MsgImportFactOverwrite        MessageCode = "import.fact.overwrite"
	MsgImportFactFollowDeletions  MessageCode = "import.fact.follow_deletions"
	MsgImportRefNotImported       MessageCode = "import.ref.not_imported"
)

var importOptionsCatalog = map[MessageCode]message{
	MsgImportRefreshChoices: {en: "Refs and refresh", ko: "ref와 새로고침"},
	MsgImportRefreshChoicesHelp: {
		en: "By default a refresh brings in branches and tags, keeps a branch or tag you changed here, and only counts refs the source deleted. These choices apply from the next refresh.",
		ko: "기본적으로 새로고침은 브랜치와 태그를 가져오고, 여기서 바꾼 브랜치나 태그는 그대로 두며, 원본에서 삭제된 ref는 개수만 셉니다. 이 설정은 다음 새로고침부터 적용됩니다.",
	},
	MsgImportExtraRefs: {en: "Extra ref namespaces", ko: "추가 ref 네임스페이스"},
	MsgImportExtraRefsHelp: {
		en: "One per line, each ending with a slash, such as refs/notes/. Refs under these namespaces are imported with branches and tags. Pushes to this repository are not affected.",
		ko: "한 줄에 하나씩, refs/notes/처럼 슬래시로 끝나게 적습니다. 이 네임스페이스의 ref를 브랜치, 태그와 함께 가져옵니다. 이 저장소로의 푸시에는 영향을 주지 않습니다.",
	},
	MsgImportExtraRefsWarning: {en: "Overwritten or deleted refs in these namespaces have no kept history.", ko: "이 네임스페이스에서 덮어쓰거나 삭제한 ref는 보관된 기록에 남지 않습니다."},
	MsgImportExtraRefsInvalid: {
		en: "Enter ref namespaces such as refs/notes/, one per line, each once. Branches, tags and refs/owngit/ are not allowed.",
		ko: "refs/notes/ 같은 ref 네임스페이스를 한 줄에 하나씩, 중복 없이 적으세요. 브랜치, 태그, refs/owngit/은 쓸 수 없습니다.",
	},
	MsgImportOverwrite: {en: "Overwrite diverged branches", ko: "원본과 달라진 브랜치 덮어쓰기"},
	MsgImportOverwriteHelp: {
		en: "A branch, tag or extra ref that changed here since the source was last seen is replaced with the source's.",
		ko: "원본을 마지막으로 확인한 뒤 여기서 바뀐 브랜치, 태그, 추가 ref를 원본의 것으로 바꿉니다.",
	},
	MsgImportFollowDeletions: {en: "Follow upstream deletions", ko: "원본의 삭제 따르기"},
	MsgImportFollowDeletionsHelp: {
		en: "A ref the source deleted is deleted here too, unless it changed here since, the default branch uses it, or it is a symbolic ref.",
		ko: "원본에서 삭제된 ref를 여기서도 삭제합니다. 그 뒤 여기서 바뀌었거나, 기본 브랜치이거나, 심볼릭 ref라면 남겨 둡니다.",
	},
	MsgImportRefreshWarning:       {en: "Local work may be replaced; upstream deletions will remove these local refs.", ko: "여기서 한 작업이 바뀔 수 있고, 원본에서 삭제된 ref는 여기서도 삭제됩니다."},
	MsgImportEffectsHeading:       {en: "Refs these choices would change now", ko: "지금 이 설정이 바꿀 ref"},
	MsgImportEffectsNone:          {en: "As of the last refresh, no local ref would change.", ko: "마지막 새로고침 기준으로 바뀔 ref가 없습니다."},
	MsgImportEffectReplace:        {en: "Replaced with the source's", ko: "원본의 것으로 바뀜"},
	MsgImportEffectDelete:         {en: "Deleted", ko: "삭제됨"},
	MsgImportEffectNeedsOverwrite: {en: "Changes when Overwrite diverged branches is on.", ko: "‘원본과 달라진 브랜치 덮어쓰기’를 켜면 바뀝니다."},
	MsgImportEffectNeedsFollow:    {en: "Changes when Follow upstream deletions is on.", ko: "‘원본의 삭제 따르기’를 켜면 바뀝니다."},
	MsgImportEffectNeedsBoth:      {en: "Changed here since, so it changes only when both choices are on.", ko: "여기서 바뀐 ref라서 두 설정을 모두 켜야 바뀝니다."},
	MsgImportEffectKept:           {en: "Kept history keeps its current commit.", ko: "지금 커밋은 보관된 기록에 남습니다."},
	MsgImportEffectNotKept:        {en: "Its current commit is not kept.", ko: "지금 커밋은 보관된 기록에 남지 않습니다."},
	MsgImportEffectsUnknown:       {en: "Which refs would change could not be worked out now, for example because the repository is being written. Reload the page to check again.", ko: "지금은 바뀔 ref를 알아내지 못했습니다. 저장소에 쓰는 중일 수 있습니다. 페이지를 다시 불러와 확인하세요."},
	MsgImportEffectRefused:        {en: "Refresh stops", ko: "새로고침 멈춤"},
	MsgImportEffectRefusedHelp:    {en: "This protected default branch differs here. With Overwrite diverged branches on, a refresh stops at it and changes nothing until its protection is turned off.", ko: "보호된 기본 브랜치가 원본과 다릅니다. ‘원본과 달라진 브랜치 덮어쓰기’를 켜면 보호를 끌 때까지 새로고침이 여기서 멈추고 아무것도 바꾸지 않습니다."},
	MsgImportFactRefresh:          {en: "Refresh", ko: "새로고침"},
	MsgImportFactExtraRefs:        {en: "Extra refs", ko: "추가 ref"},
	MsgImportFactOverwrite:        {en: "Overwrites diverged branches", ko: "달라진 브랜치 덮어씀"},
	MsgImportFactFollowDeletions:  {en: "Follows upstream deletions", ko: "원본의 삭제 따름"},
	MsgImportRefNotImported:       {en: "Namespace not imported", ko: "가져오지 않는 네임스페이스"},
	MsgImportRefusedPrivate: {
		en: "OwnGit did not connect: the source address is on a private network, which this source does not allow. To connect, turn on “Allow a private-network source” in this source's settings.",
		ko: "원본 주소가 사설망에 있고 이 원본은 사설망 연결을 허용하지 않아 OwnGit이 연결하지 않았습니다. 연결하려면 이 원본 설정에서 ‘사설망 원본 허용’을 켜세요.",
	},
	MsgImportRefusedException: {
		en: "OwnGit did not connect: the source address is a special-purpose address, which this source does not allow. To connect, turn on “Allow this exceptional destination” under Connection and limits.",
		ko: "원본 주소가 특수 용도 주소이고 이 원본은 그런 주소를 허용하지 않아 OwnGit이 연결하지 않았습니다. 연결하려면 ‘연결과 한도’에서 ‘예외 대상 주소 허용’을 켜세요.",
	},
	MsgImportRefusedAddress: {
		en: "OwnGit did not connect: the source address is in a range that imports never connect to, such as link-local or multicast. No setting allows it, so use another source address.",
		ko: "원본 주소가 링크 로컬이나 멀티캐스트처럼 가져오기가 절대 연결하지 않는 범위에 있어 OwnGit이 연결하지 않았습니다. 이를 허용하는 설정은 없으니 다른 원본 주소를 쓰세요.",
	},
	MsgImportRefusedRedirect: {
		en: "OwnGit did not follow the source's redirect, because this source's Redirects setting does not allow it. To follow it, choose a redirect option under Connection and limits; a redirect to another origin also needs that origin approved.",
		ko: "이 원본의 리디렉션 설정이 허용하지 않아 OwnGit이 원본의 리디렉션을 따르지 않았습니다. 따르려면 ‘연결과 한도’에서 리디렉션 방식을 고르세요. 다른 출처로의 리디렉션은 그 출처를 승인해야 합니다.",
	},
	MsgImportRefusedPlain: {
		en: "OwnGit did not follow the source's redirect from HTTPS to plain HTTP, which this source does not allow. To follow it, turn on “Allow plain HTTP for this source” under Connection and limits.",
		ko: "원본이 HTTPS에서 암호화되지 않은 HTTP로 리디렉션했고 이 원본은 이를 허용하지 않아 OwnGit이 따르지 않았습니다. 따르려면 ‘연결과 한도’에서 ‘이 원본에 암호화되지 않은 HTTP 허용’을 켜세요.",
	},
	MsgImportTransportReset: {
		en: "The address changed, so the plain HTTP, redirect, exceptional destination, overwrite and upstream deletion choices were reset. Choose again any that the new address needs.",
		ko: "주소가 바뀌어 암호화되지 않은 HTTP, 리디렉션, 예외 대상 주소, 덮어쓰기, 삭제 따르기 설정을 초기화했습니다. 새 주소에 필요한 설정을 다시 고르세요.",
	},
	MsgImportRefusalDetails: {
		en: "The technical details name the address and its range, or the origin the redirect leads to.",
		ko: "기술 세부 정보에 해당 주소와 범위, 또는 리디렉션이 향하는 출처가 적혀 있습니다.",
	},
	MsgImportOptions:            {en: "Connection and limits", ko: "연결과 한도"},
	MsgImportOptionsHelp:        {en: "These choices apply to this source only. The defaults suit most sources.", ko: "이 원본에만 적용됩니다. 대부분의 원본은 기본값으로 충분합니다."},
	MsgImportOptionsUnreadable:  {en: "A saved connection choice or limit of this source cannot be read, so imports from it stop. Set it again under Connection and limits.", ko: "이 원본에 저장된 연결 설정이나 한도를 읽을 수 없어 가져오기가 멈췄습니다. ‘연결과 한도’에서 다시 설정하세요."},
	MsgImportPlainHTTP:          {en: "Allow plain HTTP for this source", ko: "이 원본에 암호화되지 않은 HTTP 허용"},
	MsgImportPlainHTTPHelp:      {en: "This source's code and credentials can be read or changed in transit. Use it only on a network you trust.", ko: "전송 중에 이 원본의 코드와 인증 정보가 노출되거나 바뀔 수 있습니다. 믿을 수 있는 네트워크에서만 사용하세요."},
	MsgImportRedirects:          {en: "Redirects", ko: "리디렉션"},
	MsgImportRedirectsHelp:      {en: "OwnGit follows a redirect only for the first request, and never sends this source's sign-in or CA to another origin.", ko: "OwnGit은 첫 요청의 리디렉션만 따르고, 이 원본의 로그인 정보나 CA를 다른 출처로 보내지 않습니다."},
	MsgImportRedirectRefuse:     {en: "Refuse redirects", ko: "리디렉션 거부"},
	MsgImportRedirectSame:       {en: "Follow redirects within the same origin", ko: "같은 출처 안의 리디렉션만 따르기"},
	MsgImportRedirectApproved:   {en: "Also follow redirects to one approved origin", ko: "승인한 출처 한 곳으로의 리디렉션도 따르기"},
	MsgImportRedirectOrigin:     {en: "Approved origin", ko: "승인한 출처"},
	MsgImportRedirectOriginHelp: {en: "Used only with the approved-origin choice. Scheme and host only, for example https://mirror.example. The source's sign-in is not sent there; if that origin needs one, change the source address to it instead.", ko: "승인한 출처를 고른 경우에만 씁니다. https://mirror.example처럼 스킴과 호스트만 입력합니다. 원본의 로그인 정보는 이 출처로 보내지 않으므로, 로그인이 필요하면 원본 주소를 그 출처로 바꾸세요."},
	MsgImportReserved:           {en: "Allow this exceptional destination", ko: "예외 대상 주소 허용"},
	MsgImportReservedHelp:       {en: "This source can connect to a special-purpose address that is normally blocked, such as a documentation or benchmarking address. Private addresses still need the private-network choice. Link-local, multicast and unspecified addresses stay blocked.", ko: "평소 차단되는 특수 용도 주소(문서용, 벤치마크용 주소 등)에 이 원본이 연결할 수 있습니다. 사설 주소는 여전히 사설망 원본 허용이 필요하고, 링크 로컬, 멀티캐스트, 지정되지 않은 주소는 계속 차단됩니다."},
	MsgImportLimits:             {en: "Capacity and time", ko: "용량과 시간"},
	MsgImportLimitsHelp:         {en: "Leave a field empty to use its default. Higher limits let this import use more disk and keep the server busy longer. A change applies from the next run.", ko: "비워 두면 기본값을 씁니다. 한도를 높이면 이 가져오기가 디스크를 더 많이 쓰고 서버가 더 오래 바쁠 수 있습니다. 바꾼 값은 다음 실행부터 적용됩니다."},
	MsgImportLimitsMore:         {en: "Transfer and scan limits", ko: "전송과 검사 한도"},
	MsgImportLimitPack:          {en: "Largest pack", ko: "최대 팩 크기"},
	MsgImportLimitRun:           {en: "Run time", ko: "전체 실행 시간"},
	MsgImportLimitFetch:         {en: "Download time, including indexing", ko: "내려받기 시간(색인 포함)"},
	MsgImportLimitIndex:         {en: "Indexing time", ko: "색인 시간"},
	MsgImportLimitVerify:        {en: "Verification time", ko: "검증 시간"},
	MsgImportLimitRefs:          {en: "Most refs listed", ko: "목록에 올 수 있는 ref 수"},
	MsgImportLimitAdvertisement: {en: "Largest ref list", ko: "ref 목록 최대 크기"},
	MsgImportLimitTLS:           {en: "TLS handshake time", ko: "TLS 핸드셰이크 시간"},
	MsgImportLimitHeaders:       {en: "Wait for response headers", ko: "응답 헤더 대기 시간"},
	MsgImportLimitLFS:           {en: "Objects checked for Git LFS", ko: "Git LFS 확인 대상 객체 수"},
	MsgImportLimitWholeSeconds:  {en: "Enter a whole number of seconds.", ko: "초 단위의 정수로 입력하세요."},
	MsgImportLimitRange:         {en: "This value is outside the allowed range, or the times do not fit inside the run time.", ko: "허용 범위를 벗어났거나 각 단계 시간이 전체 실행 시간 안에 들어가지 않습니다."},
	MsgImportFactConnection:     {en: "Connection", ko: "연결"},
	MsgImportFactDefaults:       {en: "Defaults", ko: "기본값"},
	MsgImportFactPlainHTTP:      {en: "Plain HTTP allowed", ko: "암호화되지 않은 HTTP 허용"},
	MsgImportFactSameOrigin:     {en: "Redirects within the origin", ko: "같은 출처 안 리디렉션"},
	MsgImportFactApproved:       {en: "Redirects to", ko: "리디렉션 허용 출처"},
	MsgImportFactReserved:       {en: "Exceptional destination allowed", ko: "예외 대상 주소 허용"},
	MsgImportFactLimits:         {en: "Changed limits:", ko: "바꾼 한도:"},
	MsgImportURLPlainHTTP:       {en: "This address uses plain HTTP. Allow plain HTTP for this source under Connection and limits, or enter an address that starts with https://.", ko: "이 주소는 암호화되지 않은 HTTP를 씁니다. ‘연결과 한도’에서 이 원본에 HTTP를 허용하거나 https://로 시작하는 주소를 입력하세요."},
	MsgImportOriginInvalid:      {en: "Enter a scheme and host such as https://mirror.example, without a path. A plain HTTP origin also needs the plain HTTP choice.", ko: "https://mirror.example처럼 경로 없이 스킴과 호스트를 입력하세요. HTTP 출처는 암호화되지 않은 HTTP 허용도 필요합니다."},
}

var importCatalog = map[MessageCode]message{
	MsgImportTab:                {en: "Import", ko: "가져오기"},
	MsgImportTitle:              {en: "Import", ko: "가져오기"},
	MsgImportIntro:              {en: "OwnGit keeps its own copy. It never writes to the source.", ko: "OwnGit은 복사본을 따로 두고 원본에는 아무것도 쓰지 않습니다."},
	MsgImportStatusUnreadable:   {en: "The import status of this repository could not be read. This does not mean that import is off or not configured.", ko: "이 저장소의 가져오기 상태를 읽지 못했습니다. 가져오기가 꺼져 있거나 설정되지 않았다는 뜻은 아닙니다."},
	MsgImportNotConfigured:      {en: "This repository has no import source.", ko: "이 저장소에는 가져오기 원본이 없습니다."},
	MsgImportURL:                {en: "Source URL", ko: "원본 주소"},
	MsgImportMode:               {en: "Mode", ko: "방식"},
	MsgImportModeStandalone:     {en: "Standalone", ko: "독립"},
	MsgImportModeCoexistence:    {en: "Coexistence", ko: "공존"},
	MsgImportGitOnly:            {en: "Accept Git-only content", ko: "Git 내용만 받기"},
	MsgImportGitOnlyHelp:        {en: "Git LFS pointers stay in the copy and the content is marked incomplete. OwnGit does not download LFS objects.", ko: "Git LFS 포인터는 복사본에 그대로 남고 내용은 불완전한 것으로 표시됩니다. OwnGit은 LFS 객체를 받지 않습니다."},
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
	MsgImportHistoryUnavailable: {en: "The import history could not be read.", ko: "가져오기 기록을 읽지 못했습니다."},
	MsgImportOlder:              {en: "Older runs", ko: "이전 실행"},
	MsgImportRefs:               {en: "Observed refs", ko: "원본에서 확인한 ref"},
	MsgImportRefsTruncated:      {en: "The ref list is truncated.", ko: "ref 목록이 잘렸습니다."},
	MsgImportUnresolved:         {en: "Unresolved publication intents need attention.", ko: "미해결 게시가 있어 확인이 필요합니다."},
	MsgImportStaging:            {en: "Staging issues are preserved.", ko: "스테이징 문제를 그대로 남겨 두었습니다."},
	MsgImportIncomplete:         {en: "Accepted content is incomplete.", ko: "받은 내용이 불완전합니다."},
	MsgImportNewTitle:           {en: "Import a repository", ko: "저장소 가져오기"},
	MsgImportNewIntro:           {en: "Create a new OwnGit repository from an HTTPS Git source.", ko: "HTTPS Git 원본에서 새 OwnGit 저장소를 만듭니다."},
	MsgImportNewSubmit:          {en: "Start import", ko: "가져오기 시작"},
	MsgImportPasswordEachTime:   {en: "Enter the administrator password for this change.", ko: "이 변경에는 관리자 비밀번호가 필요합니다."},
	MsgImportSaved:              {en: "Import source saved.", ko: "가져오기 원본을 저장했습니다."},
	MsgImportRefreshed:          {en: "Refresh finished.", ko: "새로고침이 끝났습니다."},
	MsgImportCancelled:          {en: "Cancellation requested.", ko: "취소를 요청했습니다."},
	MsgImportCancelNone:         {en: "No active import to cancel.", ko: "취소할 진행 중인 가져오기가 없습니다."},
	MsgImportCredentialsSaved:   {en: "Credential state saved. The secret is not shown.", ko: "인증 정보를 저장했습니다. 비밀번호나 토큰 값은 표시하지 않습니다."},
	MsgImportCredentialsCleared: {en: "Credentials cleared.", ko: "인증 정보를 지웠습니다."},
	MsgImportScheduleSaved:      {en: "Schedule saved.", ko: "예약을 저장했습니다."},
	MsgImportStarted:            {en: "Import started.", ko: "가져오기를 시작했습니다."},
	MsgImportFailed:             {en: "Import did not finish successfully.", ko: "가져오기를 마치지 못했습니다."},
	MsgImportResolveHelp:        {en: "OwnGit could not confirm how an earlier publication ended, so refreshes are paused. Check the repository. If its current branches, tags, and HEAD are acceptable, accept them as they are. OwnGit does not change any ref for this. The next refresh plans from the repository as it is now, and it leaves refs that differ from both the source and the last confirmed import alone.", ko: "이전 게시가 어떻게 끝났는지 OwnGit이 확인하지 못해 새로고침을 멈췄습니다. 저장소를 확인한 뒤 현재 브랜치, 태그, HEAD를 그대로 써도 된다면 현재 상태를 인정하세요. 인정해도 ref는 바뀌지 않습니다. 다음 새로고침은 지금 저장소 상태를 기준으로 하며, 원본과도 다르고 마지막으로 확인된 가져오기와도 다른 ref는 그대로 둡니다."},
	MsgImportResolve:            {en: "Accept the current repository state", ko: "현재 저장소 상태 인정"},
	MsgImportResolved:           {en: "The current repository state was accepted. Refresh is available again.", ko: "현재 저장소 상태를 인정했습니다. 다시 새로고침할 수 있습니다."},
	MsgImportSchedulerFailed:    {en: "The import scheduler did not start. Scheduled refreshes do not run until OwnGit restarts. Ordinary Git service is available.", ko: "가져오기 예약 실행기가 시작되지 않았습니다. OwnGit을 다시 시작할 때까지 예약 새로고침은 실행되지 않습니다. 일반 Git 서비스는 사용할 수 있습니다."},
	MsgImportReconcileFailed:    {en: "Import startup checks did not finish. Ordinary Git service is available.", ko: "OwnGit을 시작할 때 하는 가져오기 점검이 끝나지 않았습니다. 일반 Git 서비스는 사용할 수 있습니다."},
	MsgImportErrorNothing:       {en: "There is no unresolved publication to accept.", ko: "인정할 미해결 게시가 없습니다."},
	MsgImportRunCancelled:       {en: "The import run was cancelled.", ko: "가져오기 실행이 취소되었습니다."},
	MsgImportCancelledNoRepo:    {en: "The import was cancelled before the repository was created. No repository was added.", ko: "저장소가 만들어지기 전에 가져오기가 취소되었습니다. 추가된 저장소는 없습니다."},
	MsgImportCancelledUnsure:    {en: "The import was cancelled, but OwnGit could not check whether the repository was added. Check the repository list before you import it again.", ko: "가져오기가 취소되었지만 저장소가 추가되었는지 확인하지 못했습니다. 다시 가져오기 전에 저장소 목록을 확인하세요."},
	MsgImportTechnicalDetails:   {en: "Technical details", ko: "기술 세부 정보"},

	MsgImportStatusHeading:         {en: "Status", ko: "상태"},
	MsgImportSource:                {en: "Source", ko: "원본"},
	MsgImportAdminOnly:             {en: "Shown to administrators", ko: "관리자에게만 표시"},
	MsgImportAutoRefresh:           {en: "Automatic refresh", ko: "자동 새로고침"},
	MsgImportEvery:                 {en: "Every %s", ko: "%s마다"},
	MsgImportSetUp:                 {en: "Set up import", ko: "가져오기 설정"},
	MsgImportSetUpIntro:            {en: "Choose the Git host to copy from. Each refresh copies its branches and tags into this repository. Branches and tags that are already here are never removed.", ko: "복사해 올 Git 호스트를 고르세요. 새로고침할 때마다 그 브랜치와 태그를 이 저장소로 복사합니다. 이미 있는 브랜치와 태그는 지우지 않습니다."},
	MsgImportNotConfiguredHelp:     {en: "Set up an import to keep this repository updated from another Git host.", ko: "다른 Git 호스트의 내용으로 이 저장소를 계속 갱신하려면 가져오기를 설정하세요."},
	MsgImportChangeSource:          {en: "Change source", ko: "원본 바꾸기"},
	MsgImportChangeSourceIntro:     {en: "The next refresh copies from the new source. Branches and tags already in this repository are not removed.", ko: "다음 새로고침부터 새 원본에서 복사합니다. 이 저장소에 이미 있는 브랜치와 태그는 지우지 않습니다."},
	MsgImportChangeSettings:        {en: "Change import settings", ko: "가져오기 설정 바꾸기"},
	MsgImportModeHelp:              {en: "The mode records how you use this copy. Imports and refreshes work the same way in both modes.", ko: "방식에는 이 복사본을 어떻게 쓰는지 기록해 둡니다. 가져오기와 새로고침은 어느 방식이든 똑같이 동작합니다."},
	MsgImportModeStandaloneHelp:    {en: "You use OwnGit as the main copy from now on.", ko: "앞으로 OwnGit을 주 저장소로 씁니다."},
	MsgImportModeCoexistHelp:       {en: "The other host stays the main copy, and you refresh this copy from it.", ko: "다른 호스트가 계속 주 저장소이고 이 복사본은 거기서 새로고침합니다."},
	MsgImportURLHelp:               {en: "The HTTPS clone address, for example https://example.com/team/project.git. A plain HTTP address needs the plain HTTP choice under Connection and limits.", ko: "HTTPS 클론 주소를 입력합니다. 예: https://example.com/team/project.git. 암호화되지 않은 HTTP 주소는 ‘연결과 한도’에서 HTTP를 허용해야 합니다."},
	MsgImportURLRequired:           {en: "Enter the source address.", ko: "원본 주소를 입력하세요."},
	MsgImportURLHTTPS:              {en: "Only HTTPS addresses can be imported, and HTTP addresses when plain HTTP is allowed. Enter an address that starts with https://.", ko: "HTTPS 주소만 가져올 수 있으며, HTTP 주소는 암호화되지 않은 HTTP를 허용한 경우에만 가져올 수 있습니다. https://로 시작하는 주소를 입력하세요."},
	MsgImportURLUser:               {en: "Leave the username and password out of the address. Enter them under Credentials instead.", ko: "주소에서 사용자 이름과 비밀번호를 빼세요. 인증 정보에 따로 입력하세요."},
	MsgImportURLQuery:              {en: "Remove the part of the address after ? or #.", ko: "주소에서 ? 또는 # 뒤의 부분을 지우세요."},
	MsgImportNameHelp:              {en: "Optional. Without a name, OwnGit uses the last part of the address. Letters, numbers, dots, dashes, and underscores.", ko: "선택 사항입니다. 비워 두면 주소의 마지막 부분을 이름으로 씁니다. 영문자, 숫자, 점, 하이픈, 밑줄을 사용합니다."},
	MsgImportCAHelp:                {en: "Optional. Needed only when the source uses a certificate that this computer does not trust. Paste the PEM text.", ko: "선택 사항입니다. 원본이 이 컴퓨터가 신뢰하지 않는 인증서를 쓸 때만 필요합니다. PEM 텍스트를 붙여 넣으세요."},
	MsgImportBasicNeedsBoth:        {en: "Username and password sign-in needs both a username and a password.", ko: "사용자 이름과 비밀번호 방식에는 둘 다 입력해야 합니다."},
	MsgImportTokenRequired:         {en: "Enter the access token.", ko: "액세스 토큰을 입력하세요."},
	MsgImportCredentialFormUnknown: {en: "Choose one of the listed credential forms.", ko: "목록에 있는 인증 형식 중 하나를 고르세요."},
	MsgImportCredentialKeep:        {en: "No new sign-in (CA only)", ko: "새 로그인 정보 없음 (CA만)"},
	MsgImportCredentialSaveHelp:    {en: "Saving changes only what you enter. With “No new sign-in (CA only)”, only the CA below is saved, and a stored username and password or token stays. A new sign-in replaces the stored one and keeps the stored CA unless you enter a new CA. To remove the stored sign-in and CA, use Clear credentials below.", ko: "저장하면 입력한 것만 바뀝니다. “새 로그인 정보 없음 (CA만)”을 고르면 아래 CA만 저장되고, 저장된 사용자 이름과 비밀번호 또는 토큰은 그대로 남습니다. 새 로그인 정보를 저장하면 기존 것을 대신하며, 새 CA를 입력하지 않으면 저장된 CA는 남습니다. 저장된 로그인 정보와 CA를 지우려면 아래의 인증 정보 지우기를 사용하세요."},
	MsgImportCAOnly:                {en: "CA only", ko: "CA만"},
	MsgImportNoRefs:                {en: "No branches or tags have been observed at the source yet.", ko: "아직 원본에서 확인한 브랜치나 태그가 없습니다."},

	MsgImportErrorInvalidSource: {en: "The source address or settings are not valid.", ko: "원본 주소나 설정이 올바르지 않습니다."},
	MsgImportErrorBusy:          {en: "Another import run is active for this repository.", ko: "이 저장소에서 다른 가져오기가 진행 중입니다."},
	MsgImportErrorRepoMissing:   {en: "The destination repository could not be found.", ko: "대상 저장소를 찾을 수 없습니다."},
	MsgImportErrorRepoTaken:     {en: "A repository with this name already exists.", ko: "같은 이름의 저장소가 이미 있습니다."},
	MsgImportErrorObjectFormat:  {en: "The source uses an object format that this installation cannot import.", ko: "원본이 이 서버에서 가져올 수 없는 객체 형식을 사용합니다."},
	MsgImportErrorRefs:          {en: "The source has refs that cannot be imported safely.", ko: "원본에 안전하게 가져올 수 없는 ref가 있습니다."},
	MsgImportErrorNetwork:       {en: "The source could not be reached.", ko: "원본에 연결할 수 없었습니다."},
	MsgImportErrorProtocol:      {en: "The source sent a response that OwnGit could not accept.", ko: "원본이 OwnGit이 받아들일 수 없는 응답을 보냈습니다."},
	MsgImportErrorTooLarge:      {en: "The source is larger than this import's limits. You can raise them under Connection and limits.", ko: "원본이 이 가져오기의 한도보다 큽니다. ‘연결과 한도’에서 한도를 높일 수 있습니다."},
	MsgImportErrorTooManyRefs:   {en: "The source lists more refs than this import accepts (50,000 unless you changed it). If the source does not support Git protocol v2, pull request refs count too, although they are not imported. Raise the ref limit under Connection and limits, or clone the source and push its branches and tags to a new repository.", ko: "원본의 ref가 이 가져오기의 한도(바꾸지 않았다면 50,000개)를 넘습니다. 원본이 Git 프로토콜 v2를 지원하지 않으면 가져오지 않는 풀 리퀘스트 ref도 개수에 들어갑니다. ‘연결과 한도’에서 ref 한도를 높이거나, 원본을 clone한 뒤 브랜치와 태그를 새 저장소에 푸시하세요."},
	MsgImportErrorIndex:         {en: "The received pack could not be indexed.", ko: "받은 팩을 색인하지 못했습니다."},
	MsgImportErrorVerify:        {en: "The received content did not pass verification.", ko: "받은 내용이 검증을 통과하지 못했습니다."},
	MsgImportErrorPublish:       {en: "The imported refs could not be published.", ko: "가져온 ref를 게시하지 못했습니다."},
	MsgImportErrorLFS:           {en: "The source uses Git LFS. Accept Git-only content to import it.", ko: "원본이 Git LFS를 사용합니다. 가져오려면 Git 내용만 받기를 선택하세요."},
	MsgImportErrorDestination:   {en: "The destination repository changed during the import.", ko: "가져오는 동안 대상 저장소가 바뀌었습니다."},
	MsgImportErrorProtected: {
		en: "The source rewrote the protected default branch, so nothing was changed. To follow the source, turn off the protection in the repository's Settings tab and refresh again.",
		ko: "원본이 보호된 기본 브랜치를 다시 써서 아무것도 바꾸지 않았습니다. 원본을 따르려면 저장소 설정 탭에서 보호를 끄고 다시 새로고침하세요.",
	},
	MsgImportErrorUnresolved:   {en: "The publication result is uncertain and needs attention.", ko: "게시 결과가 확실하지 않아 확인이 필요합니다."},
	MsgImportErrorSuperseded:   {en: "A newer source setting replaced this run.", ko: "새 원본 설정이 이 실행을 대신했습니다."},
	MsgImportErrorInterrupted:  {en: "The run was interrupted when OwnGit stopped.", ko: "OwnGit이 멈추면서 실행이 중단되었습니다."},
	MsgImportErrorState:        {en: "OwnGit could not read or record import state.", ko: "OwnGit이 가져오기 상태를 읽거나 기록하지 못했습니다."},
	MsgImportErrorRuntime:      {en: "The import workspace is not available.", ko: "가져오기 작업 공간을 사용할 수 없습니다."},
	MsgImportErrorUnsupported:  {en: "The source or destination uses a feature that import does not support.", ko: "원본이나 대상이 가져오기에서 지원하지 않는 기능을 사용합니다."},
	MsgImportErrorLimit:        {en: "The import reached its time limit.", ko: "가져오기가 시간 제한에 도달했습니다."},
	MsgImportName:              {en: "Name", ko: "이름"},
	MsgImportDescription:       {en: "Description", ko: "설명"},
	MsgImportListLink:          {en: "Import a repository", ko: "저장소 가져오기"},
	MsgImportKindInitial:       {en: "Initial", ko: "처음 가져오기"},
	MsgImportKindRefresh:       {en: "Refresh", ko: "새로고침"},
	MsgImportKindScheduled:     {en: "Scheduled", ko: "예약"},
	MsgImportStatusPreparing:   {en: "Preparing", ko: "준비 중"},
	MsgImportStatusFetching:    {en: "Fetching", ko: "받는 중"},
	MsgImportStatusIndexing:    {en: "Indexing", ko: "색인 중"},
	MsgImportStatusInspecting:  {en: "Inspecting", ko: "검사 중"},
	MsgImportStatusPublishing:  {en: "Publishing", ko: "게시 중"},
	MsgImportStatusComplete:    {en: "Complete", ko: "완료"},
	MsgImportStatusFailed:      {en: "Failed", ko: "실패"},
	MsgImportStatusCancelled:   {en: "Cancelled", ko: "취소됨"},
	MsgImportStatusSuperseded:  {en: "Superseded", ko: "대체됨"},
	MsgImportStatusInterrupted: {en: "Interrupted", ko: "중단됨"},
	MsgImportStatusUnresolved:  {en: "Unresolved", ko: "미해결"},
	MsgImportRefTracked:        {en: "Tracked", ko: "원본과 같음"},
	MsgImportRefDiverged:       {en: "Diverged", ko: "원본과 다름"},
	MsgImportRefAbsent:         {en: "Absent locally", ko: "OwnGit에 없음"},
	MsgImportRefEarlier:        {en: "Earlier source", ko: "예전 원본 기록"},
	MsgImportRefUnknown:        {en: "Local unknown", ko: "OwnGit 쪽 확인 못 함"},
	MsgImportRefDeleted:        {en: "Deleted at source", ko: "원본에서 삭제됨"},
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
	case "repository_create_failed":
		return MsgRepoCreateFail
	case "unsupported_object_format":
		return MsgImportErrorObjectFormat
	case "unsupported_refs":
		return MsgImportErrorRefs
	case "network":
		return MsgImportErrorNetwork
	case "address_needs_private_network":
		return MsgImportRefusedPrivate
	case "address_needs_exceptional_destination":
		return MsgImportRefusedException
	case "address_refused":
		return MsgImportRefusedAddress
	case "redirect_not_allowed":
		return MsgImportRefusedRedirect
	case "redirect_needs_plain_http":
		return MsgImportRefusedPlain
	case "protocol":
		return MsgImportErrorProtocol
	case "too_large":
		return MsgImportErrorTooLarge
	case "too_many_refs":
		return MsgImportErrorTooManyRefs
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
	case "protected_default_branch":
		return MsgImportErrorProtected
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
	case "unclassified":
		return MsgImportFailed
	case "limit":
		return MsgImportErrorLimit
	case "nothing_to_resolve":
		return MsgImportErrorNothing
	case "invalid_schedule":
		return MsgImportIntervalHelp
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
	case "not_imported":
		return MsgImportRefNotImported, true
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
	for _, source := range []map[MessageCode]message{importCatalog, importOptionsCatalog} {
		registerMessages(source)
	}
}

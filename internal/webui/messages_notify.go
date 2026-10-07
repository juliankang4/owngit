package webui

// Desktop notifications of the OwnGit icon. The operating system shows
// them as plain text: a title, an optional subtitle and a body.
const (
	MsgNotifyCommit           MessageCode = "notify.commit"
	MsgNotifyCommits          MessageCode = "notify.commits"
	MsgNotifyAndMore          MessageCode = "notify.and_more"
	MsgNotifyNewBranch        MessageCode = "notify.new_branch"
	MsgNotifyNewTag           MessageCode = "notify.new_tag"
	MsgNotifyNewRef           MessageCode = "notify.new_ref"
	MsgNotifyRefDeleted       MessageCode = "notify.ref_deleted"
	MsgNotifyRefUpdated       MessageCode = "notify.ref_updated"
	MsgNotifyPushes           MessageCode = "notify.pushes"
	MsgNotifyRepositoryPushes MessageCode = "notify.repository_pushes"
	MsgNotifyLatest           MessageCode = "notify.latest"
	MsgNotifyPushedElsewhere  MessageCode = "notify.pushed_elsewhere"
	MsgNotifyPullRequest      MessageCode = "notify.pull_request"
	MsgNotifyPullRequests     MessageCode = "notify.pull_requests"
	MsgNotifyOpenedElsewhere  MessageCode = "notify.opened_elsewhere"
	MsgNotifyCheckFailed      MessageCode = "notify.check_failed"
	MsgNotifyChecksFailed     MessageCode = "notify.checks_failed"
	MsgNotifyForPullRequest   MessageCode = "notify.for_pull_request"
	MsgNotifyImportFailed     MessageCode = "notify.import_failed"
	MsgNotifyImportsFailed    MessageCode = "notify.imports_failed"
	MsgNotifyBackupFailed     MessageCode = "notify.backup_failed"
	MsgNotifyBackupsFailed    MessageCode = "notify.backups_failed"
	MsgNotifyUpdate           MessageCode = "notify.update"
	MsgNotifyOpen             MessageCode = "notify.open"
	MsgNotifyMany             MessageCode = "notify.many"
	MsgNotifyManyHint         MessageCode = "notify.many_hint"
	MsgNotifyUnavailable      MessageCode = "notify.unavailable"

	// The notification settings of the icon's panel.
	MsgNotifySettings       MessageCode = "notify.settings"
	MsgNotifySettingsHint   MessageCode = "notify.settings_hint"
	MsgNotifySettingsFailed MessageCode = "notify.settings_failed"
	MsgNotifySettingAll     MessageCode = "notify.setting.all"
	MsgNotifySettingOthers  MessageCode = "notify.setting.only_others"
	MsgNotifySettingPush    MessageCode = "notify.setting.push"
	MsgNotifySettingPR      MessageCode = "notify.setting.pull_request"
	MsgNotifySettingCheck   MessageCode = "notify.setting.check_failed"
	MsgNotifySettingImport  MessageCode = "notify.setting.import_failed"
	MsgNotifySettingBackup  MessageCode = "notify.setting.backup_failed"
	MsgNotifySettingUpdate  MessageCode = "notify.setting.update"
)

var notifyCatalog = map[MessageCode]message{
	MsgNotifyCommit:           {en: "1 new commit in %s", ko: "%s에 새 커밋 1개"},
	MsgNotifyCommits:          {en: "%d new commits in %s", ko: "%[2]s에 새 커밋 %[1]d개"},
	MsgNotifyAndMore:          {en: "%s and %d more", ko: "%s 외 %d개"},
	MsgNotifyNewBranch:        {en: "New branch %s in %s", ko: "%[2]s에 새 브랜치 %[1]s"},
	MsgNotifyNewTag:           {en: "New tag %s in %s", ko: "%[2]s에 새 태그 %[1]s"},
	MsgNotifyNewRef:           {en: "New ref %s in %s", ko: "%[2]s에 새 참조 %[1]s"},
	MsgNotifyRefDeleted:       {en: "%s deleted in %s", ko: "%[2]s에서 %[1]s 삭제됨"},
	MsgNotifyRefUpdated:       {en: "%s updated in %s", ko: "%[2]s의 %[1]s 변경됨"},
	MsgNotifyPushes:           {en: "%d pushes", ko: "푸시 %d번"},
	MsgNotifyRepositoryPushes: {en: "%s %d", ko: "%s %d번"},
	MsgNotifyLatest:           {en: "Latest: %s", ko: "마지막: %s"},
	MsgNotifyPushedElsewhere:  {en: "Pushed from another computer", ko: "다른 컴퓨터에서 푸시"},
	MsgNotifyPullRequest:      {en: "Pull request #%d opened in %s", ko: "%[2]s에 풀 리퀘스트 #%[1]d 열림"},
	MsgNotifyPullRequests:     {en: "%d pull requests opened", ko: "풀 리퀘스트 %d개 열림"},
	MsgNotifyOpenedElsewhere:  {en: "Opened from another computer", ko: "다른 컴퓨터에서 열림"},
	MsgNotifyCheckFailed:      {en: "Checks failed in %s", ko: "%s 체크 실패"},
	MsgNotifyChecksFailed:     {en: "Checks failed %d times", ko: "체크 실패 %d건"},
	MsgNotifyForPullRequest:   {en: "Pull request #%d", ko: "풀 리퀘스트 #%d"},
	MsgNotifyImportFailed:     {en: "Import did not finish in %s", ko: "%s 가져오기가 끝나지 않음"},
	MsgNotifyImportsFailed:    {en: "%d imports did not finish", ko: "끝나지 않은 가져오기 %d건"},
	MsgNotifyBackupFailed:     {en: "Backup did not finish", ko: "백업이 끝나지 않음"},
	MsgNotifyBackupsFailed:    {en: "%d backups did not finish", ko: "끝나지 않은 백업 %d건"},
	MsgNotifyUpdate:           {en: "OwnGit %s is available", ko: "OwnGit %s 버전이 나왔습니다"},
	MsgNotifyOpen:             {en: "Open", ko: "열기"},
	MsgNotifyMany:             {en: "%d new OwnGit notifications", ko: "OwnGit 새 알림 %d개"},
	MsgNotifyManyHint:         {en: "Open OwnGit to see them.", ko: "OwnGit을 열어 확인하세요."},
	MsgNotifyUnavailable: {
		en: "This desktop has no notification service, so OwnGit cannot show notifications. Results still appear on the dashboard.",
		ko: "이 데스크톱에는 알림 서비스가 없어 OwnGit 알림을 표시할 수 없습니다. 결과는 대시보드에서 계속 볼 수 있습니다.",
	},
	MsgNotifySettings: {en: "Notifications", ko: "알림"},
	MsgNotifySettingsHint: {
		en: "The notification settings of this computer apply too.",
		ko: "이 컴퓨터의 알림 설정도 함께 적용됩니다.",
	},
	MsgNotifySettingsFailed: {en: "The notification settings could not be read or saved: %s", ko: "알림 설정을 읽거나 저장하지 못했습니다: %s"},
	MsgNotifySettingAll:     {en: "Show notifications", ko: "알림 보기"},
	MsgNotifySettingOthers:  {en: "Only what I did not do", ko: "내가 하지 않은 일만 알림"},
	MsgNotifySettingPush:    {en: "Pushes", ko: "푸시"},
	MsgNotifySettingPR:      {en: "Pull requests opened", ko: "새 풀 리퀘스트"},
	MsgNotifySettingCheck:   {en: "Failed checks", ko: "실패한 체크"},
	MsgNotifySettingImport:  {en: "Imports that did not finish", ko: "끝나지 않은 가져오기"},
	MsgNotifySettingBackup:  {en: "Backups that did not finish", ko: "끝나지 않은 백업"},
	MsgNotifySettingUpdate:  {en: "New OwnGit versions", ko: "새 OwnGit 버전"},
}

func init() {
	registerMessages(notifyCatalog)
}

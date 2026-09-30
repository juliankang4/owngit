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
}

func init() {
	for code, entry := range notifyCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

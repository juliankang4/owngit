package webui

// Backups that OwnGit makes while it serves: the warnings shown when an
// owner turns a part of them off.
const (
	MsgBackupScheduleOffWarning MessageCode = "backup.schedule_off_warning"
	MsgBackupVerifyOffWarning   MessageCode = "backup.verify_off_warning"
)

var backupsCatalog = map[MessageCode]message{
	MsgBackupScheduleOffWarning: {
		en: "OwnGit will stop creating scheduled recovery copies. Back up now still works, and the backups already made stay.",
		ko: "OwnGit이 예약된 복구용 백업을 더 이상 만들지 않습니다. 지금 백업하기는 계속 쓸 수 있고, 이미 만든 백업은 그대로 남습니다.",
	},
	MsgBackupVerifyOffWarning: {
		en: "New backups will not be checked by rehearsing a restore, so a damaged backup may go unnoticed until you need it. OwnGit never removes the last verified backup.",
		ko: "새 백업을 복원 연습으로 검사하지 않으므로, 손상된 백업을 실제로 필요할 때까지 알아차리지 못할 수 있습니다. 마지막으로 검사를 통과한 백업은 지우지 않습니다.",
	},
}

func init() {
	for code, entry := range backupsCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

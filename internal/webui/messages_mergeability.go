package webui

// Check mergeability wording.
const (
	MsgMergeabilityCheck            MessageCode = "pr.mergeability.check"
	MsgMergeabilityHelp             MessageCode = "pr.mergeability.help"
	MsgMergeabilityFastForward      MessageCode = "pr.mergeability.fast_forward"
	MsgMergeabilityMergeCommit      MessageCode = "pr.mergeability.merge_commit"
	MsgMergeabilityUpToDate         MessageCode = "pr.mergeability.up_to_date"
	MsgMergeabilityConflict         MessageCode = "pr.mergeability.conflict"
	MsgMergeabilityMorePaths        MessageCode = "pr.mergeability.more_paths"
	MsgMergeabilityConflictUnlisted MessageCode = "pr.mergeability.conflict_unlisted"
	MsgMergeabilityNoBase           MessageCode = "pr.mergeability.no_base"
	MsgMergeabilityStale            MessageCode = "pr.mergeability.stale"
	MsgMergeabilityOldGit           MessageCode = "pr.mergeability.old_git"
	MsgMergeabilityFailed           MessageCode = "pr.mergeability.failed"
)

var mergeabilityCatalog = map[MessageCode]message{
	MsgMergeabilityCheck: {en: "Check mergeability", ko: "병합 가능 여부 확인"},
	MsgMergeabilityHelp: {
		en: "Works out whether the current commits merge, without changing anything. OwnGit checks only when you ask.",
		ko: "아무것도 바꾸지 않고 현재 커밋을 병합할 수 있는지 확인합니다. 요청할 때만 확인합니다.",
	},
	MsgMergeabilityFastForward: {
		en: "Can merge: the target branch moves forward to the source commit.",
		ko: "병합할 수 있습니다. 대상 브랜치가 가져올 브랜치의 커밋으로 앞당겨집니다.",
	},
	MsgMergeabilityMergeCommit: {
		en: "Can merge without conflicts: OwnGit creates a merge commit.",
		ko: "충돌 없이 병합할 수 있습니다. OwnGit이 병합 커밋을 만듭니다.",
	},
	MsgMergeabilityUpToDate: {
		en: "Can merge: the target branch already contains the source, so it stays as it is.",
		ko: "병합할 수 있습니다. 대상 브랜치에 가져올 브랜치의 커밋이 이미 들어 있어 대상은 그대로입니다.",
	},
	MsgMergeabilityConflict: {
		en: "These files conflict. Resolve them on a branch and push, then check again.",
		ko: "다음 파일이 충돌합니다. 브랜치에서 충돌을 해결해 푸시한 뒤 다시 확인하세요.",
	},
	MsgMergeabilityConflictUnlisted: {
		en: "Merging now would conflict, but Git named no file. Change a branch and push, then check again.",
		ko: "지금 병합하면 충돌하지만 Git이 충돌한 파일을 알려 주지 않았습니다. 브랜치를 고쳐 푸시한 뒤 다시 확인하세요.",
	},
	MsgMergeabilityMorePaths: {
		en: "More files conflict than are listed here.",
		ko: "여기 표시한 파일 말고도 충돌하는 파일이 더 있습니다.",
	},
	MsgMergeabilityNoBase: {
		en: "The branches share no history, so they cannot be merged.",
		ko: "두 브랜치에 공통 기록이 없어 병합할 수 없습니다.",
	},
	MsgMergeabilityStale: {
		en: "A branch moved after this page was opened, so nothing was checked. The page now shows the current commits; check again.",
		ko: "이 페이지를 연 뒤에 브랜치가 움직여 확인하지 않았습니다. 페이지에는 이제 현재 커밋이 보입니다. 다시 확인하세요.",
	},
	MsgMergeabilityOldGit: {
		en: "Checking needs Git 2.38 or newer on the server, as merging does.",
		ko: "병합과 마찬가지로 확인하려면 서버에 Git 2.38 이상이 필요합니다.",
	},
	MsgMergeabilityFailed: {
		en: "OwnGit could not work out whether this merges. Try again; the server log has the details.",
		ko: "병합할 수 있는지 알아내지 못했습니다. 다시 시도해 보세요. 자세한 내용은 서버 로그에 있습니다.",
	},
}

func init() {
	registerMessages(mergeabilityCatalog)
}

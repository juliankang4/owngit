package webui

import "time"

// Leftover check containers: the container cleanup records of finished jobs
// that OwnGit could not remove itself, shown on the Checks page with a form
// that forgets one once the owner removed its container.

// ActionForgetCheckContainer forgets the cleanup record of one finished job.
// Fields: csrf, action, admin_password, job_id, container_removed.
const ActionForgetCheckContainer = "forget_container"

// CheckContainerRow is one leftover container cleanup record.
type CheckContainerRow struct {
	JobID string
	// JobURL opens the job on the Checks page.
	JobURL        string
	ContainerName string
	// ContainerID is "" when Docker never reported the container's ID.
	ContainerID string
	DaemonID    string
	CreatedAt   time.Time
}

func forgetJobAction() string { return ActionForgetCheckContainer }

const (
	MsgCCLeftoverTitle       MessageCode = "cc.leftover.title"
	MsgCCLeftoverHelp        MessageCode = "cc.leftover.help"
	MsgCCLeftoverUnavailable MessageCode = "cc.leftover.unavailable"
	MsgCCLeftoverJob         MessageCode = "cc.leftover.job"
	MsgCCLeftoverName        MessageCode = "cc.leftover.name"
	MsgCCLeftoverID          MessageCode = "cc.leftover.id"
	MsgCCLeftoverNoID        MessageCode = "cc.leftover.no_id"
	MsgCCLeftoverDaemon      MessageCode = "cc.leftover.daemon"
	MsgCCLeftoverLabel       MessageCode = "cc.leftover.label"
	MsgCCLeftoverCreated     MessageCode = "cc.leftover.created"
	MsgCCLeftoverRemoved     MessageCode = "cc.leftover.removed"
	MsgCCLeftoverForget      MessageCode = "cc.leftover.forget"

	MsgCCContainerForgotten     MessageCode = "cc.result.container_forgotten"
	MsgCCContainerRemovedNeeded MessageCode = "cc.result.container_removed_needed"
	MsgCCContainerMissing       MessageCode = "cc.result.container_missing"
	MsgCCContainerCurrentDaemon MessageCode = "cc.result.container_current_daemon"
	MsgCCContainerJobActive     MessageCode = "cc.result.container_job_active"
	MsgCCContainerChanged       MessageCode = "cc.result.container_changed"
)

var checkContainerCatalog = map[MessageCode]message{
	MsgCCLeftoverTitle: {en: "Leftover check containers", ko: "남은 체크 컨테이너"},
	MsgCCLeftoverHelp: {
		en: "OwnGit could not remove these containers of finished jobs: they were created on another Docker daemon, or Docker was unavailable. While one is listed, this repository cannot be deleted. Remove the container yourself on the Docker daemon that ran it, or make sure that daemon no longer exists, then forget it here. OwnGit removes no container.",
		ko: "끝난 작업의 컨테이너 가운데 OwnGit이 지우지 못한 것입니다. 다른 Docker 데몬에서 만들어졌거나 Docker를 쓸 수 없었습니다. 여기에 남아 있는 동안에는 이 저장소를 삭제할 수 없습니다. 그 작업을 실행한 Docker 데몬에서 컨테이너를 직접 지우거나 그 데몬이 더 이상 없는지 확인한 뒤 여기에서 기록을 지우세요. OwnGit은 컨테이너를 지우지 않습니다.",
	},
	MsgCCLeftoverUnavailable: {
		en: "The leftover container records could not be read. Reload the page to try again.",
		ko: "남은 컨테이너 기록을 읽지 못했습니다. 페이지를 새로 고쳐 다시 시도하세요.",
	},
	MsgCCLeftoverJob:     {en: "Job", ko: "작업"},
	MsgCCLeftoverName:    {en: "Container name", ko: "컨테이너 이름"},
	MsgCCLeftoverID:      {en: "Container ID", ko: "컨테이너 ID"},
	MsgCCLeftoverNoID:    {en: "Not assigned yet", ko: "아직 없음"},
	MsgCCLeftoverDaemon:  {en: "Docker daemon", ko: "Docker 데몬"},
	MsgCCLeftoverLabel:   {en: "Label", ko: "레이블"},
	MsgCCLeftoverCreated: {en: "Created", ko: "만든 시각"},
	MsgCCLeftoverRemoved: {
		en: "I removed this container, or its Docker daemon no longer exists.",
		ko: "이 컨테이너를 지웠거나 그 Docker 데몬이 더 이상 없습니다.",
	},
	MsgCCLeftoverForget: {en: "Forget container", ko: "컨테이너 기록 지우기"},

	MsgCCContainerForgotten: {
		en: "OwnGit forgot the container record and removed no container. If that Docker daemon comes back, remove any container with the job's label yourself. Restart OwnGit to clean up the job's check workspace.",
		ko: "컨테이너 기록을 지웠고 컨테이너는 지우지 않았습니다. 그 Docker 데몬이 다시 나타나면 이 작업의 레이블이 붙은 컨테이너를 직접 지우세요. 작업의 체크 작업 공간은 OwnGit을 다시 시작하면 정리됩니다.",
	},
	MsgCCContainerRemovedNeeded: {
		en: "Tick the box to confirm that the container was removed or its Docker daemon no longer exists.",
		ko: "컨테이너를 지웠거나 그 Docker 데몬이 없다는 것을 확인하는 칸을 선택하세요.",
	},
	MsgCCContainerMissing: {
		en: "This repository has no container record for that job. It may have been forgotten or removed already.",
		ko: "이 저장소에는 그 작업의 컨테이너 기록이 없습니다. 이미 지워졌을 수 있습니다.",
	},
	MsgCCContainerCurrentDaemon: {
		en: "This container belongs to the Docker daemon OwnGit uses now, so nothing was forgotten. Restart OwnGit and it removes the container itself.",
		ko: "이 컨테이너는 OwnGit이 지금 쓰는 Docker 데몬에 있어서 기록을 지우지 않았습니다. OwnGit을 다시 시작하면 컨테이너를 직접 지웁니다.",
	},
	MsgCCContainerJobActive: {
		en: "The job has not finished, so nothing was forgotten. Wait for it to finish or cancel it, then try again.",
		ko: "작업이 아직 끝나지 않아 기록을 지우지 않았습니다. 작업이 끝나거나 취소한 뒤 다시 시도하세요.",
	},
	MsgCCContainerChanged: {
		en: "The container record changed while this page was open, so nothing was forgotten. Check it again before forgetting it.",
		ko: "이 화면을 열어 둔 사이에 컨테이너 기록이 바뀌어 지우지 않았습니다. 다시 확인한 뒤 지우세요.",
	},
}

func init() {
	registerMessages(checkContainerCatalog)
}

package webui

const (
	MsgFolderChoose            MessageCode = "folder.choose"
	MsgFolderHost              MessageCode = "folder.host"
	MsgFolderCurrent           MessageCode = "folder.current"
	MsgFolderParent            MessageCode = "folder.parent"
	MsgFolderHidden            MessageCode = "folder.hidden"
	MsgFolderList              MessageCode = "folder.list"
	MsgFolderEmpty             MessageCode = "folder.empty"
	MsgFolderLoading           MessageCode = "folder.loading"
	MsgFolderLimit             MessageCode = "folder.limit"
	MsgFolderParentOpened      MessageCode = "folder.parent_opened"
	MsgFolderSkippedFolders    MessageCode = "folder.skipped_folders"
	MsgFolderName              MessageCode = "folder.name"
	MsgFolderCreate            MessageCode = "folder.create"
	MsgFolderCreated           MessageCode = "folder.created"
	MsgFolderCancel            MessageCode = "folder.cancel"
	MsgFolderUse               MessageCode = "folder.use"
	MsgFolderDrives            MessageCode = "folder.drives"
	MsgFolderRetry             MessageCode = "folder.retry"
	MsgFolderDenied            MessageCode = "folder.denied"
	MsgFolderMissing           MessageCode = "folder.missing"
	MsgFolderNotDirectory      MessageCode = "folder.not_directory"
	MsgFolderTimeout           MessageCode = "folder.timeout"
	MsgFolderBusy              MessageCode = "folder.busy"
	MsgFolderInvalidPath       MessageCode = "folder.invalid_path"
	MsgFolderInvalidName       MessageCode = "folder.invalid_name"
	MsgFolderExists            MessageCode = "folder.exists"
	MsgFolderFailed            MessageCode = "folder.failed"
	MsgFolderCreateUnconfirmed MessageCode = "folder.create_unconfirmed"
)

var folderCatalog = map[MessageCode]message{
	MsgFolderChoose:            {en: "Choose folder", ko: "폴더 선택"},
	MsgFolderHost:              {en: "Folders on the computer running OwnGit, not this browser's device. You can also type a path in the setup form.", ko: "브라우저를 연 기기가 아니라 OwnGit이 실행되는 컴퓨터의 폴더입니다. 설치 화면에서 경로를 직접 입력해도 됩니다."},
	MsgFolderCurrent:           {en: "Current folder", ko: "현재 폴더"},
	MsgFolderParent:            {en: "Parent folder", ko: "상위 폴더"},
	MsgFolderHidden:            {en: "Show hidden folders", ko: "숨김 폴더 표시"},
	MsgFolderList:              {en: "Subfolders. Press Enter to open a folder.", ko: "하위 폴더. Enter를 누르면 폴더를 엽니다."},
	MsgFolderEmpty:             {en: "No subfolders to show. You can use this folder or create one here.", ko: "표시할 하위 폴더가 없습니다. 현재 폴더를 선택하거나 새 폴더를 만드세요."},
	MsgFolderLoading:           {en: "Loading folders…", ko: "폴더를 불러오는 중…"},
	MsgFolderLimit:             {en: "This folder is large. Only folders from the first 2,000 entries are shown, sorted by name. You can type a path in the setup form.", ko: "항목이 많은 폴더입니다. 처음 2,000개 항목에 있는 폴더만 이름순으로 표시합니다. 설치 화면에서 경로를 직접 입력해도 됩니다."},
	MsgFolderParentOpened:      {en: "That folder does not exist yet. Its nearest existing parent is open. Create a folder here or choose another.", ko: "아직 없는 폴더라 가장 가까운 상위 폴더를 열었습니다. 여기에서 새 폴더를 만들거나 다른 폴더를 선택하세요."},
	MsgFolderSkippedFolders:    {en: "Some folders could not be shown. The other folders are listed.", ko: "일부 폴더를 표시하지 못했습니다. 나머지 폴더는 표시합니다."},
	MsgFolderName:              {en: "New folder name", ko: "새 폴더 이름"},
	MsgFolderCreate:            {en: "Create folder", ko: "폴더 만들기"},
	MsgFolderCreated:           {en: "Folder created and opened.", ko: "폴더를 만들고 열었습니다."},
	MsgFolderCancel:            {en: "Cancel", ko: "취소"},
	MsgFolderUse:               {en: "Use this folder", ko: "이 폴더 선택"},
	MsgFolderDrives:            {en: "Drives", ko: "드라이브"},
	MsgFolderRetry:             {en: "Try again", ko: "다시 시도"},
	MsgFolderDenied:            {en: "The account running OwnGit cannot access this folder. Choose another folder or change its permissions on that computer.", ko: "OwnGit을 실행하는 계정이 이 폴더에 접근할 수 없습니다. 다른 폴더를 선택하거나 해당 컴퓨터에서 폴더 권한을 바꾸세요."},
	MsgFolderMissing:           {en: "This folder does not exist. Go to its parent or type another path in the setup form.", ko: "이 폴더는 없습니다. 상위 폴더로 이동하거나 설치 화면에서 다른 경로를 입력하세요."},
	MsgFolderNotDirectory:      {en: "This path is not a folder. Go to its parent or type a folder path in the setup form.", ko: "폴더가 아닌 경로입니다. 상위 폴더로 이동하거나 설치 화면에서 폴더 경로를 입력하세요."},
	MsgFolderTimeout:           {en: "The folder did not respond within 3 seconds. A filesystem operation may still be running. Choose another path or try again later.", ko: "폴더가 3초 안에 응답하지 않았습니다. 파일 시스템 작업이 아직 진행 중일 수 있습니다. 다른 경로를 입력하거나 나중에 다시 시도하세요."},
	MsgFolderBusy:              {en: "A folder operation is still running. Try again after it finishes, or type a path in the setup form.", ko: "폴더 작업이 아직 진행 중입니다. 작업이 끝난 뒤 다시 시도하거나 설치 화면에서 경로를 직접 입력하세요."},
	MsgFolderInvalidPath:       {en: "Enter a full folder path in the setup form, then choose again.", ko: "설치 화면에 전체 폴더 경로를 입력한 뒤 다시 선택하세요."},
	MsgFolderInvalidName:       {en: "Enter one folder name, not a path. Do not use . or .., path separators, control characters, or a name reserved by the operating system.", ko: "경로가 아닌 폴더 이름 하나를 입력하세요. .이나 .., 경로 구분 문자, 줄바꿈 같은 제어 문자, 운영체제에서 예약한 이름은 사용할 수 없습니다."},
	MsgFolderExists:            {en: "An entry with this name already exists. Choose another name; nothing was replaced.", ko: "이 이름의 항목이 이미 있습니다. 다른 이름을 입력하세요. 기존 항목은 바꾸지 않았습니다."},
	MsgFolderFailed:            {en: "The folder operation failed. Check the folder on the computer running OwnGit, or type another path in the setup form.", ko: "폴더 작업에 실패했습니다. OwnGit이 실행되는 컴퓨터에서 폴더를 확인하거나 설치 화면에서 다른 경로를 입력하세요."},
	MsgFolderCreateUnconfirmed: {en: "Folder creation could not be confirmed within 3 seconds and may still finish. Refresh this folder before trying to create it again.", ko: "3초 안에 폴더 생성 결과를 확인하지 못했습니다. 작업이 나중에 끝날 수 있으니 다시 만들기 전에 폴더 목록을 새로 불러와 확인하세요."},
}

func init() {
	for code, entry := range folderCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

// FolderMessages are the server-rendered status texts used by the chooser.
func (SetupPage) FolderMessages() []MessageCode {
	return []MessageCode{MsgFolderEmpty, MsgFolderLoading, MsgFolderCreated, MsgFolderParentOpened, MsgFolderDenied, MsgFolderMissing, MsgFolderNotDirectory, MsgFolderTimeout, MsgFolderBusy, MsgFolderInvalidPath, MsgFolderInvalidName, MsgFolderExists, MsgFolderFailed, MsgFolderCreateUnconfirmed, MsgSetupAlreadyDone, MsgSetupSessionEnded, MsgErrCSRF, MsgErrUnavailable}
}

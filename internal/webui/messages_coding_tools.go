package webui

// The Coding tools page: connecting a coding tool to this server, the
// helper credentials of every repository, and the recent check tasks.
const (
	MsgCodingTitle          MessageCode = "coding.title"
	MsgCodingIntro          MessageCode = "coding.intro"
	MsgCodingServer         MessageCode = "coding.server"
	MsgCodingPlainHTTP      MessageCode = "coding.plain_http"
	MsgCodingPasswordFile   MessageCode = "coding.password_file"
	MsgCodingSkillTitle     MessageCode = "coding.skill.title"
	MsgCodingSkillHelp      MessageCode = "coding.skill.help"
	MsgCodingSkillCommand   MessageCode = "coding.skill.command"
	MsgCodingMCPTitle       MessageCode = "coding.mcp.title"
	MsgCodingMCPHelp        MessageCode = "coding.mcp.help"
	MsgCodingClaude         MessageCode = "coding.mcp.claude"
	MsgCodingCodex          MessageCode = "coding.mcp.codex"
	MsgCodingOther          MessageCode = "coding.mcp.other"
	MsgCodingOtherHelp      MessageCode = "coding.mcp.other_help"
	MsgCodingChecksHelp     MessageCode = "coding.mcp.checks"
	MsgCodingPathHelp       MessageCode = "coding.path"
	MsgCodingCredsTitle     MessageCode = "coding.credentials.title"
	MsgCodingCredsHelp      MessageCode = "coding.credentials.help"
	MsgCodingCredsNone      MessageCode = "coding.credentials.none"
	MsgCodingCredsAdmin     MessageCode = "coding.credentials.admin"
	MsgCodingCredsUnreadble MessageCode = "coding.credentials.unavailable"
	MsgCodingTasksTitle     MessageCode = "coding.tasks.title"
	MsgCodingTasksNone      MessageCode = "coding.tasks.none"
	MsgCodingTasksCut       MessageCode = "coding.tasks.truncated"
	MsgCodingTasksUnread    MessageCode = "coding.tasks.unavailable"
	MsgCodingRepository     MessageCode = "coding.repository"
)

var codingToolsCatalog = map[MessageCode]message{
	MsgCodingTitle: {en: "Coding tools", ko: "코딩 도구"},
	MsgCodingIntro: {
		en: "Connect a coding tool such as Claude Code or Codex to this server. The tool runs the owngit command on its own computer, so install OwnGit there too.",
		ko: "Claude Code나 Codex 같은 코딩 도구를 이 서버에 연결합니다. 코딩 도구는 자기 컴퓨터에서 owngit 명령을 실행하므로 그 컴퓨터에도 OwnGit을 설치하세요.",
	},
	MsgCodingServer: {en: "Server address", ko: "서버 주소"},
	MsgCodingPlainHTTP: {
		en: "This address uses plain HTTP, which does not encrypt passwords, so the commands include --accept-insecure-http. Use them only on a network you trust.",
		ko: "이 주소는 비밀번호를 암호화하지 않는 HTTP를 쓰므로 명령에 --accept-insecure-http가 들어 있습니다. 믿을 수 있는 네트워크에서만 쓰세요.",
	},
	MsgCodingPasswordFile: {
		en: "This server asks for the shared password. Save it in a file only you can read, and replace PASSWORD_FILE in the commands with that file's path.",
		ko: "이 서버는 공용 비밀번호를 요구합니다. 비밀번호를 나만 읽을 수 있는 파일에 저장하고, 명령의 PASSWORD_FILE을 그 파일 경로로 바꾸세요.",
	},
	MsgCodingSkillTitle: {en: "Skill", ko: "스킬"},
	MsgCodingSkillHelp: {
		en: "The skill tells a coding tool which owngit commands to use. This command writes it to ~/.agents/skills/owngit-checks, where Codex and Pi look for skills.",
		ko: "스킬은 코딩 도구에 어떤 owngit 명령을 쓸지 알려 줍니다. 이 명령은 Codex와 Pi가 스킬을 찾는 ~/.agents/skills/owngit-checks에 스킬을 씁니다.",
	},
	MsgCodingSkillCommand: {en: "Install the skill", ko: "스킬 설치"},
	MsgCodingMCPTitle:     {en: "MCP server", ko: "MCP 서버"},
	MsgCodingMCPHelp: {
		en: "A coding tool that supports MCP starts owngit mcp itself. Run one of these commands once on the computer where the tool runs.",
		ko: "MCP를 지원하는 코딩 도구는 owngit mcp를 직접 실행합니다. 코딩 도구가 실행되는 컴퓨터에서 아래 명령 중 하나를 한 번 실행하세요.",
	},
	MsgCodingClaude:    {en: "Claude Code", ko: "Claude Code"},
	MsgCodingCodex:     {en: "Codex", ko: "Codex"},
	MsgCodingOther:     {en: "Other MCP clients", ko: "다른 MCP 클라이언트"},
	MsgCodingOtherHelp: {en: "Use the stdio transport with this command.", ko: "stdio 방식으로 이 명령을 실행하게 설정하세요."},
	MsgCodingChecksHelp: {
		en: "The check tools need a helper credential for one repository. Issue one on that repository's Helper credentials page, then add --credential-file with the token file's path, and --repository or --workdir with a clone of it.",
		ko: "체크 도구를 쓰려면 저장소 하나의 체크 에이전트 토큰이 필요합니다. 그 저장소의 체크 에이전트 토큰 화면에서 발급한 뒤, 토큰 파일 경로와 함께 --credential-file을 붙이고 --repository 또는 그 저장소 클론을 가리키는 --workdir를 붙이세요.",
	},
	MsgCodingPathHelp: {
		en: "If owngit is not on the tool's PATH, write the full path to the program instead of owngit after --.",
		ko: "코딩 도구의 PATH에 owngit이 없다면 -- 뒤의 owngit 대신 프로그램의 전체 경로를 쓰세요.",
	},
	MsgCodingCredsTitle: {en: "Helper credentials", ko: "체크 에이전트 토큰"},
	MsgCodingCredsHelp: {
		en: "Every repository's helper credentials, with when each was last used. A label is the name given when the credential was issued; it does not prove who used it.",
		ko: "모든 저장소의 체크 에이전트 토큰과 각각 마지막으로 쓰인 때입니다. 이름은 발급할 때 붙인 것이며 누가 썼는지를 증명하지는 않습니다.",
	},
	MsgCodingCredsNone: {en: "No helper credential has been issued.", ko: "발급된 체크 에이전트 토큰이 없습니다."},
	MsgCodingCredsAdmin: {
		en: "Helper credentials are shown to the administrator.",
		ko: "체크 에이전트 토큰은 관리자에게만 보입니다.",
	},
	MsgCodingCredsUnreadble: {
		en: "Helper credentials could not be read. Reload the page to try again.",
		ko: "체크 에이전트 토큰을 읽지 못했습니다. 페이지를 새로 고쳐 다시 시도하세요.",
	},
	MsgCodingTasksTitle: {en: "Recent tasks", ko: "최근 체크 에이전트 작업"},
	MsgCodingTasksNone:  {en: "No task has been recorded yet.", ko: "아직 기록된 작업이 없습니다."},
	MsgCodingTasksCut: {
		en: "Only the %s most recently updated tasks are shown. Each repository's Checks tab lists all of its tasks.",
		ko: "가장 최근에 바뀐 작업 %s개만 보여 줍니다. 저장소마다 체크 탭에서 모든 작업을 볼 수 있습니다.",
	},
	MsgCodingTasksUnread: {
		en: "Task records could not be read. Reload the page to try again.",
		ko: "작업 기록을 읽지 못했습니다. 페이지를 새로 고쳐 다시 시도하세요.",
	},
	MsgCodingRepository: {en: "Repository", ko: "저장소"},
}

func init() {
	registerMessages(codingToolsCatalog)
}

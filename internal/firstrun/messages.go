package firstrun

import (
	"strings"

	"owngit/internal/webui"
)

// Terminal wording. Sentences that the web setup page also shows come from
// the web catalog (webui.Text) so the two stay the same; these are the
// sentences only the terminal needs. Korean uses 설치 for the process and 설정
// for the state (아직 설정되지 않았습니다).
type phrase struct{ en, ko string }

var phrases = map[string]phrase{
	"subtitle":      {"First-run setup", "처음 설정"},
	"start_title":   {"Set up OwnGit", "OwnGit 설치"},
	"start_help":    {"OwnGit is running but has not been set up yet. Choose where to answer the setup questions.", "OwnGit은 실행 중이지만 아직 설정되지 않았습니다. 설치 질문에 어디서 답할지 고르세요."},
	"opt_term":      {"Continue in this terminal", "이 터미널에서 계속"},
	"opt_term_help": {"Answer a few questions here. Passwords stay hidden as you type.", "여기서 몇 가지 질문에 답합니다. 비밀번호는 입력해도 화면에 나타나지 않습니다."},
	"opt_web":       {"Open the web dashboard", "웹 대시보드 열기"},
	"opt_web_help":  {"Approve your browser here, then continue setup in the browser.", "이 터미널에서 브라우저를 승인한 뒤 브라우저에서 설치를 이어 갑니다."},
	"lang_other":    {"한국어", "English"},
	"choice":        {"Choice", "선택"},
	"choice_bad":    {"Enter a number from 1 to {n}.", "1부터 {n}까지의 번호를 입력하세요."},
	"yn_bad":        {"Enter y or n.", "y 또는 n을 입력하세요."},
	"suggested":     {"Suggested", "제안"},

	"storage_help":    {"OwnGit creates repositories inside this folder and leaves existing files alone.", "OwnGit은 이 폴더 안에 저장소를 만들고, 폴더에 있던 파일은 그대로 둡니다."},
	"storage_default": {"Type a full path, or press Enter to use the suggested folder.", "전체 경로를 입력하세요. Enter만 누르면 제안한 폴더를 씁니다."},
	"storage_field":   {"Folder", "폴더"},
	"storage_ok":      {"OwnGit can use this folder.", "이 폴더를 쓸 수 있습니다."},

	"shared_title": {"Shared password", "공용 비밀번호"},
	"pw_rule":      {"Use 8 to 1024 characters. What you type stays hidden.", "8자 이상 1024자 이하로 입력하세요. 입력한 글자는 화면에 나타나지 않습니다."},
	"again_field":  {"Type it again", "한 번 더 입력"},
	"pw_mismatch":  {"The two entries are different. Type the password again.", "두 번 입력한 비밀번호가 서로 다릅니다. 다시 입력하세요."},

	"conn_label":  {"Unencrypted connection", "암호화되지 않는 연결"},
	"conn_listen": {"OwnGit is listening on {addr}, so other devices on the network can connect.", "OwnGit이 {addr}에서 연결을 받으므로 네트워크의 다른 기기에서 접속할 수 있습니다."},
	"conn_q":      {"Continue without encryption?", "암호화 없이 계속할까요?"},
	"conn_need":   {"Confirm that you understand OwnGit is not encrypting this connection. To keep OwnGit on this computer only, press Ctrl-C and start it again with --listen 127.0.0.1:{port}.", "OwnGit이 이 연결을 암호화하지 않는다는 점을 확인해 주세요. 이 컴퓨터에서만 쓰려면 Ctrl-C로 멈춘 뒤 --listen 127.0.0.1:{port} 옵션으로 다시 시작하세요."},

	"conn_need_saved": {"Confirm that you understand OwnGit is not encrypting this connection. This address comes from the saved network settings. To keep OwnGit on this computer only, press Ctrl-C, run the command below, and start OwnGit again.", "OwnGit이 이 연결을 암호화하지 않는다는 점을 확인해 주세요. 이 주소는 저장된 네트워크 설정에서 왔습니다. 이 컴퓨터에서만 쓰려면 Ctrl-C로 멈춘 뒤 아래 명령을 실행하고 OwnGit을 다시 시작하세요."},

	"review_title": {"Review", "설정 확인"},
	"review_help":  {"Nothing is saved until you choose Finish setup.", "설치 완료를 고르기 전에는 아무것도 저장하지 않습니다."},
	"row_access":   {"Access", "접근"},
	"row_conn":     {"Connection", "연결"},
	"val_entered":  {"entered", "입력함"},
	"val_plain":    {"plain HTTP, not encrypted", "일반 HTTP, 암호화 안 됨"},
	"val_local":    {"this computer only", "이 컴퓨터에서만"},
	"start_over":   {"Start over", "처음부터 다시"},

	"done_title": {"Setup complete", "설치를 마쳤습니다"},
	"done_dash":  {"Dashboard", "대시보드"},
	"done_other": {"Other devices", "다른 기기에서"},
	"done_next":  {"Create your first repository when you are ready.", "준비되면 첫 저장소를 만드세요."},
	"done_log":   {"OwnGit keeps running. The server log continues below.", "OwnGit은 계속 실행됩니다. 아래로 서버 로그가 이어집니다."},

	"web_title":      {"Browser setup", "브라우저에서 설치"},
	"web_open":       {"Opening {url} in your browser. If nothing opens, visit that address on this computer.", "브라우저에서 {url} 주소를 엽니다. 열리지 않으면 이 컴퓨터에서 그 주소로 접속하세요."},
	"web_visit":      {"Visit {url} in a browser on this computer.", "이 컴퓨터의 브라우저에서 {url} 주소로 접속하세요."},
	"web_wait":       {"Waiting for the browser.", "브라우저를 기다리는 중입니다."},
	"web_switch":     {"Press T to set up here instead.", "여기서 설치하려면 T를 누르세요."},
	"web_req":        {"A browser wants to set up OwnGit", "브라우저가 OwnGit 설치를 요청했습니다"},
	"web_from":       {"From", "보낸 곳"},
	"web_this":       {"this computer", "이 컴퓨터"},
	"web_other_dev":  {"another device", "다른 기기"},
	"web_remote":     {"This request comes from another device. Approve it only if you are using that device yourself.", "다른 기기에서 온 요청입니다. 그 기기를 직접 쓰고 있을 때만 승인하세요."},
	"web_compare":    {"Approve only if your browser shows this same code.", "브라우저에 이 코드와 같은 코드가 보일 때만 승인하세요."},
	"web_replaces":   {"Approving ends the setup already open in the browser you approved before.", "승인하면 앞서 승인한 브라우저에서 진행 중인 설치가 끝납니다."},
	"web_q":          {"Approve this browser?", "이 브라우저를 승인할까요?"},
	"web_rejected":   {"Rejected. That browser cannot continue setup. Waiting for another request.", "거절했습니다. 그 브라우저로는 설치를 이어 갈 수 없습니다. 다른 요청을 기다립니다."},
	"web_gone":       {"That request is no longer waiting. Waiting for another request.", "그 요청은 이제 기다리고 있지 않습니다. 다른 요청을 기다립니다."},
	"web_approved":   {"Approved. Continue in the browser.", "승인했습니다. 브라우저에서 계속하세요."},
	"web_finishing":  {"Waiting for the browser to finish setup.", "브라우저에서 설치를 마치기를 기다리는 중입니다."},
	"web_progress":   {"Answers received from the browser:", "브라우저에서 받은 답:"},
	"done_elsewhere": {"Setup was finished in a browser.", "브라우저에서 설치를 마쳤습니다."},

	"dev_title":    {"Other devices", "다른 기기에서 접속"},
	"dev_found":    {"Tailscale is running on this computer.", "이 컴퓨터에서 Tailscale이 실행 중입니다."},
	"dev_missing":  {"Tailscale was not found on this computer.", "이 컴퓨터에서 Tailscale을 찾지 못했습니다."},
	"dev_stopped":  {"Tailscale is installed but not running.", "Tailscale이 설치되어 있지만 실행 중이 아닙니다."},
	"dev_addr":     {"Tailscale address", "Tailscale 주소"},
	"dev_name":     {"MagicDNS name", "MagicDNS 이름"},
	"dev_how":      {"To use OwnGit from your other Tailscale devices, save these network settings with the command below, then restart OwnGit after setup. You can change them later in Settings, under Network.", "다른 Tailscale 기기에서 OwnGit을 쓰려면 아래 명령으로 네트워크 설정을 저장하고, 설치를 마친 뒤 OwnGit을 다시 시작하세요. 나중에 설정 화면의 \"네트워크\"에서 바꿀 수도 있습니다."},
	"dev_enc":      {"Tailscale encrypts the connection between devices, but OwnGit still reports plain HTTP because it cannot see that protection.", "Tailscale이 기기 사이의 연결을 암호화하지만, OwnGit은 그 보호를 확인할 수 없어 계속 일반 HTTP로 표시합니다."},
	"dev_docs":     {`See "Reaching the server from another device" in the OwnGit docs.`, `OwnGit 문서의 "다른 기기에서 서버에 접속하기"를 보세요.`},
	"dev_cmd":      {"Command that saves the settings (one line, copy all of it):", "설정을 저장하는 명령 (한 줄 전체를 복사하세요):"},
	"dev_service":  {`Saved settings also apply when OwnGit runs as a background service. Leave --listen and --base-url out of the service definition, because an option there replaces the saved value. See "Options for a background service" in the OwnGit docs.`, `OwnGit을 백그라운드 서비스로 실행해도 저장된 설정이 적용됩니다. 서비스 정의에 --listen이나 --base-url 옵션이 있으면 저장된 값 대신 옵션 값을 쓰므로 빼 두세요. OwnGit 문서의 "백그라운드 서비스의 옵션"을 보세요.`},
	"dev_continue": {"Press Enter to continue", "계속하려면 Enter를 누르세요"},

	"stop_title":  {"Setup stopped", "설치를 멈췄습니다"},
	"stop_body":   {"Nothing was saved. OwnGit is still not set up.", "저장한 내용은 없습니다. OwnGit은 아직 설정되지 않은 상태입니다."},
	"stop_resume": {"To continue, run owngit serve again.", "이어서 하려면 owngit serve를 다시 실행하세요."},
}

// The language prompt comes before a language is chosen, so it is bilingual.
const (
	languageTitle  = "Language / 언어"
	languagePrompt = "Choice / 선택"
	languageBad    = "Enter 1 or 2. / 1 또는 2를 입력하세요."
)

var languageHint = [2]string{
	"Press Enter for the default. You can switch later with L.",
	"Enter만 누르면 기본값을 씁니다. 나중에 L 키로 바꿀 수 있습니다.",
}

// say returns the phrase for key in lang, with {name} placeholders replaced
// by the given name and value pairs.
func say(lang webui.Lang, key string, replacements ...string) string {
	entry, ok := phrases[key]
	if !ok {
		panic("firstrun: unknown phrase " + key)
	}
	text := entry.en
	if lang == webui.LangKO {
		text = entry.ko
	}
	for i := 0; i+1 < len(replacements); i += 2 {
		text = strings.ReplaceAll(text, "{"+replacements[i]+"}", replacements[i+1])
	}
	return text
}

// localeLanguage chooses the default language from the locale variables, in
// the order the C library reads them.
func localeLanguage(getenv func(string) string) webui.Lang {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := getenv(name); value != "" {
			if strings.HasPrefix(strings.ToLower(value), "ko") {
				return webui.LangKO
			}
			return webui.LangEN
		}
	}
	return webui.LangEN
}

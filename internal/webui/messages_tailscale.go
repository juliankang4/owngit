package webui

import (
	"fmt"
	"html/template"
)

// Strings for sharing on the tailnet over HTTPS: the problems Tailscale can
// report and what readiness waits for. "owngit tailscale" prints the English
// text of the same codes, so the page and the command say the same thing.

// TailscaleProblemCode is the message for a problem named by
// server.TailscaleError or server.TailscaleReport: a tailscale.Kind or a
// server problem. An unknown problem gets the message for a failed command.
func TailscaleProblemCode(problem string) MessageCode {
	if code := MessageCode("tailscale.problem." + problem); Has(code) {
		return code
	}
	return MsgTSProblemFailed
}

// TailscaleWaitCode is the message for one entry of
// server.TailscaleReport.Waiting.
func TailscaleWaitCode(wait string) MessageCode {
	if code := MessageCode("tailscale.wait." + wait); Has(code) {
		return code
	}
	return MsgTSWaitUnknown
}

// TailscaleRefusalCode is the message for a refusal on the Settings page.
// When the page lists what is on the port below, a refusal about the port
// points there instead of ending in that list.
func TailscaleRefusalCode(problem string, listed bool) MessageCode {
	code := TailscaleProblemCode(problem)
	if listed && Has(code+"_listed") {
		return code + "_listed"
	}
	return code
}

// TailscaleUse is one thing Tailscale has on its HTTPS port, as
// tailscale.Use describes it.
type TailscaleUse struct {
	Kind, Address, Target string
}

// TailscaleUseCode is the message that describes a TailscaleUse of kind.
// Its text takes the address as %[1]s and the target as %[2]s.
func TailscaleUseCode(kind string) MessageCode {
	if code := MessageCode("tailscale.use." + kind); Has(code) {
		return code
	}
	return "tailscale.use.unknown"
}

// TailscaleUseText describes use in lang.
func TailscaleUseText(lang Lang, use TailscaleUse) string {
	return fmt.Sprintf(Text(lang, TailscaleUseCode(use.Kind)), use.Address, use.Target)
}

// TailscaleProblemBrief is the message for problem without what Tailscale
// printed, for a viewer who is not the administrator: a message that would
// end in that detail gets a complete sentence instead.
func TailscaleProblemBrief(problem MessageCode) MessageCode {
	if Has(problem + "_brief") {
		return problem + "_brief"
	}
	return problem
}

// TailscalePortNoteBrief is note without the list of what is on the port,
// for a viewer who is not the administrator.
func TailscalePortNoteBrief(note MessageCode) MessageCode {
	if note == MsgTSChanged {
		return MsgTSChangedBrief
	}
	return MsgTSTakenBrief
}

// tsUse renders the description of use in both languages.
func tsUse(lang Lang, use TailscaleUse) template.HTML {
	return biText(lang, TailscaleUseText(LangEN, use), TailscaleUseText(LangKO, use))
}

const (
	MsgTSProblemFailed = MessageCode("tailscale.problem.failed")
	MsgTSWaitUnknown   = MessageCode("tailscale.wait.server_unknown")
	// MsgTSMacApp is the one line for the Tailscale app for macOS.
	MsgTSMacApp = MessageCode("tailscale.mac_app")
	// MsgTSReadBackMacApp is added to the read-back problem for that app.
	MsgTSReadBackMacApp = MessageCode("tailscale.problem.read_back_mac_app")
	// MsgTSStale introduces what Tailscale keeps under an earlier name of
	// this computer, and how to remove it.
	MsgTSStale = MessageCode("tailscale.stale")
)

// Messages that end with a colon are followed by the detail named in their
// comment, such as what Tailscale printed.
var tailscaleCatalog = map[MessageCode]message{
	"tailscale.problem.not_installed": {
		en: "OwnGit cannot find the tailscale command on this computer. Install Tailscale and sign in, or give the command's path with the --tailscale option of owngit.",
		ko: "이 컴퓨터에서 tailscale 명령을 찾지 못했습니다. Tailscale을 설치하고 로그인하거나, owngit의 --tailscale 옵션으로 명령의 경로를 알려 주세요.",
	},
	"tailscale.problem.not_running": {
		en: "Tailscale is installed but does not answer. Start Tailscale on this computer and try again.",
		ko: "Tailscale이 설치되어 있지만 응답하지 않습니다. 이 컴퓨터에서 Tailscale을 실행한 뒤 다시 시도하세요.",
	},
	"tailscale.problem.logged_out": {
		en: "Tailscale on this computer is signed out. Sign in to Tailscale and try again.",
		ko: "이 컴퓨터의 Tailscale이 로그아웃되어 있습니다. Tailscale에 로그인한 뒤 다시 시도하세요.",
	},
	"tailscale.problem.stopped": {
		en: "Tailscale on this computer is turned off. Turn it on in the Tailscale app or with \"tailscale up\", and try again.",
		ko: "이 컴퓨터의 Tailscale이 꺼져 있습니다. Tailscale 앱이나 \"tailscale up\"으로 켠 뒤 다시 시도하세요.",
	},
	"tailscale.problem.needs_approval": {
		en: "This computer is waiting for approval in the Tailscale admin console. Approve it there and try again.",
		ko: "이 컴퓨터가 Tailscale 관리 콘솔에서 승인을 기다리고 있습니다. 관리 콘솔에서 승인한 뒤 다시 시도하세요.",
	},
	"tailscale.problem.magicdns_off": {
		en: "MagicDNS is off in your tailnet, so this computer has no name for the address. Turn on MagicDNS on the DNS page of the Tailscale admin console and try again.",
		ko: "tailnet에서 MagicDNS가 꺼져 있어 주소에 쓸 이 컴퓨터의 이름이 없습니다. Tailscale 관리 콘솔의 DNS 페이지에서 MagicDNS를 켠 뒤 다시 시도하세요.",
	},
	"tailscale.problem.https_off": {
		en: "HTTPS certificates are not enabled in your tailnet. Turn on HTTPS Certificates on the DNS page of the Tailscale admin console and try again.",
		ko: "tailnet에서 HTTPS 인증서가 켜져 있지 않습니다. Tailscale 관리 콘솔의 DNS 페이지에서 HTTPS Certificates를 켠 뒤 다시 시도하세요.",
	},
	"tailscale.problem.permission": {
		en: "Tailscale did not let OwnGit change its settings. On Linux, allow your user once in a terminal with \"sudo tailscale set --operator=$USER\", then try again. OwnGit never runs sudo itself.",
		ko: "Tailscale이 OwnGit의 설정 변경을 거부했습니다. Linux에서는 터미널에서 \"sudo tailscale set --operator=$USER\"를 한 번 실행해 사용자를 허용한 뒤 다시 시도하세요. OwnGit은 sudo를 직접 실행하지 않습니다.",
	},
	"tailscale.problem.timeout": {
		en: "Tailscale did not answer in time. Check that Tailscale is running and try again.",
		ko: "Tailscale이 제시간에 응답하지 않았습니다. Tailscale이 실행 중인지 확인한 뒤 다시 시도하세요.",
	},
	"tailscale.problem.unreadable": {
		en: "OwnGit could not read Tailscale's answer. Update Tailscale and try again.",
		ko: "OwnGit이 Tailscale의 응답을 읽지 못했습니다. Tailscale을 업데이트한 뒤 다시 시도하세요.",
	},
	// Detail: what Tailscale printed.
	MsgTSProblemFailed: {
		en: "Tailscale reported an error:",
		ko: "Tailscale이 오류를 알렸습니다:",
	},
	// For a viewer who does not see what Tailscale printed.
	"tailscale.problem.failed_brief": {
		en: "Tailscale reported an error. \"owngit tailscale status\" on this computer shows it.",
		ko: "Tailscale이 오류를 알렸습니다. 이 컴퓨터에서 \"owngit tailscale status\"를 실행하면 내용을 볼 수 있습니다.",
	},
	// Detail: what is on the port.
	"tailscale.problem.port_taken": {
		en: "Tailscale already serves something else on HTTPS port 443 of this computer, so OwnGit changed nothing. If you no longer need it, remove it with \"tailscale serve\" and try again. On the port now:",
		ko: "이 컴퓨터의 HTTPS 포트 443에서 Tailscale이 이미 다른 것을 제공하고 있어 OwnGit은 아무것도 바꾸지 않았습니다. 더 이상 필요 없다면 \"tailscale serve\"로 지운 뒤 다시 시도하세요. 지금 이 포트의 설정:",
	},
	"tailscale.problem.port_taken_listed": {
		en: "Tailscale already serves something else on HTTPS port 443 of this computer, so OwnGit changed nothing. If you no longer need it, remove it with \"tailscale serve\" and try again. What is on the port is listed below.",
		ko: "이 컴퓨터의 HTTPS 포트 443에서 Tailscale이 이미 다른 것을 제공하고 있어 OwnGit은 아무것도 바꾸지 않았습니다. 더 이상 필요 없다면 \"tailscale serve\"로 지운 뒤 다시 시도하세요. 이 포트의 설정은 아래에 있습니다.",
	},
	"tailscale.problem.read_back": {
		en: "Tailscale accepted the change but did not keep it, so OwnGit does not use it.",
		ko: "Tailscale이 변경을 받아들였지만 유지하지 않아 OwnGit은 이 주소를 쓰지 않습니다.",
	},
	MsgTSReadBackMacApp: {
		en: "The Tailscale app for macOS does this when it cannot save its settings. Quit and reopen the Tailscale app, then try again.",
		ko: "macOS용 Tailscale 앱은 설정을 저장하지 못할 때 이렇게 됩니다. Tailscale 앱을 종료했다가 다시 연 뒤 다시 시도하세요.",
	},
	// Detail: what is on the port now.
	"tailscale.problem.endpoint_changed": {
		en: "The Tailscale address was changed after OwnGit made it, so OwnGit changed nothing. Change it back or remove it with \"tailscale serve\", then turn sharing off again. On the port now:",
		ko: "OwnGit이 만든 뒤 Tailscale 주소 설정이 바뀌어 OwnGit은 아무것도 바꾸지 않았습니다. 원래대로 돌리거나 \"tailscale serve\"로 지운 뒤 공유를 다시 끄세요. 지금 이 포트의 설정:",
	},
	"tailscale.problem.endpoint_changed_listed": {
		en: "The Tailscale address was changed after OwnGit made it, so OwnGit changed nothing. Change it back or remove it with \"tailscale serve\", then turn sharing off again. What is on the port is listed below.",
		ko: "OwnGit이 만든 뒤 Tailscale 주소 설정이 바뀌어 OwnGit은 아무것도 바꾸지 않았습니다. 원래대로 돌리거나 \"tailscale serve\"로 지운 뒤 공유를 다시 끄세요. 이 포트의 설정은 아래에 있습니다.",
	},

	// Followed by the addresses.
	MsgTSStale: {
		en: "Tailscale still has an address under a name this computer had before. It answers for nothing, because Tailscale answers only for the current name, and it does not keep OwnGit from sharing. Tailscale can remove it only while the computer has that name: rename the computer back in the Tailscale admin console, run \"tailscale serve --https=443 --set-path=/ off\", then rename it again. If Tailscale serves nothing else on this computer, \"tailscale serve reset\" also removes it. The address:",
		ko: "Tailscale에 이 컴퓨터의 예전 이름으로 된 주소가 남아 있습니다. Tailscale은 지금 이름으로만 응답하므로 이 주소는 아무 데도 연결되지 않으며, OwnGit의 공유도 막지 않습니다. Tailscale은 컴퓨터가 그 이름일 때만 이 주소를 지울 수 있습니다. Tailscale 관리 콘솔에서 이름을 예전 이름으로 되돌리고 \"tailscale serve --https=443 --set-path=/ off\"를 실행한 뒤 다시 이름을 바꾸세요. 이 컴퓨터에서 Tailscale이 다른 것을 제공하지 않는다면 \"tailscale serve reset\"으로도 지울 수 있습니다. 남아 있는 주소:",
	},

	// What is on Tailscale's HTTPS port: %[1]s is the address and %[2]s the
	// target (tailscale.Use).
	"tailscale.use.proxy":       {en: "%[1]s to %[2]s", ko: "%[1]s에서 %[2]s(으)로 전달"},
	"tailscale.use.files":       {en: "%[1]s serving the files at %[2]s", ko: "%[1]s에서 %[2]s의 파일 제공"},
	"tailscale.use.redirect":    {en: "%[1]s redirecting to %[2]s", ko: "%[1]s에서 %[2]s(으)로 리디렉션"},
	"tailscale.use.text":        {en: "%[1]s answering with fixed text", ko: "%[1]s에서 고정된 텍스트로 응답"},
	"tailscale.use.empty":       {en: "%[1]s with a handler that serves nothing", ko: "%[1]s에 아무것도 제공하지 않는 핸들러"},
	"tailscale.use.tcp_forward": {en: "TCP forwarding of port %[1]s to %[2]s", ko: "포트 %[1]s의 TCP 전달(%[2]s)"},
	"tailscale.use.plain_http":  {en: "plain HTTP on port %[1]s", ko: "포트 %[1]s의 일반 HTTP"},
	"tailscale.use.funnel":      {en: "Funnel, open to the public Internet, on %[1]s", ko: "%[1]s의 Funnel(공개 인터넷에 열림)"},
	"tailscale.use.foreground":  {en: "a foreground \"tailscale serve\" session on port %[1]s", ko: "포트 %[1]s의 포그라운드 \"tailscale serve\" 세션"},
	"tailscale.use.incomplete":  {en: "an incomplete Serve setting on port %[1]s", ko: "포트 %[1]s의 불완전한 Serve 설정"},
	"tailscale.use.unknown":     {en: "%[1]s", ko: "%[1]s"},

	// Detail: the listen address.
	"tailscale.problem.listen_option": {
		en: "The running OwnGit was started with a --listen option that Tailscale cannot reach: Tailscale connects to OwnGit through this computer's own loopback address. Start OwnGit without that option and try again. The option was:",
		ko: "실행 중인 OwnGit이 Tailscale이 접속할 수 없는 --listen 옵션으로 시작되었습니다. Tailscale은 이 컴퓨터 자신의 루프백 주소로 OwnGit에 접속합니다. 이 옵션 없이 OwnGit을 시작한 뒤 다시 시도하세요. 옵션 값:",
	},
	"tailscale.problem.not_on": {
		en: "Sharing on the tailnet is not on. Nothing changed.",
		ko: "tailnet 공유가 켜져 있지 않습니다. 바뀐 것은 없습니다.",
	},

	"tailscale.wait.tailscale": {
		en: "Tailscale on this computer has a problem; see above.",
		ko: "이 컴퓨터의 Tailscale에 문제가 있습니다. 위의 설명을 보세요.",
	},
	"tailscale.wait.unfinished": {
		en: "Turning sharing on did not finish. Turn it on again.",
		ko: "공유를 켜는 작업이 끝나지 않았습니다. 다시 켜세요.",
	},
	"tailscale.wait.name_changed": {
		en: "This computer's name in the tailnet changed. Turn sharing on again to use the new name.",
		ko: "tailnet에서 이 컴퓨터의 이름이 바뀌었습니다. 새 이름을 쓰려면 공유를 다시 켜세요.",
	},
	"tailscale.wait.endpoint": {
		en: "Tailscale no longer has the address OwnGit made. Turn sharing on again, or off if you changed it on purpose.",
		ko: "OwnGit이 만든 주소가 Tailscale에 더 이상 없습니다. 공유를 다시 켜거나, 일부러 바꿨다면 끄세요.",
	},
	"tailscale.wait.server_not_running": {
		en: "OwnGit is not running. The address works once OwnGit starts.",
		ko: "OwnGit이 실행 중이 아닙니다. OwnGit을 시작하면 주소가 동작합니다.",
	},
	MsgTSWaitUnknown: {
		en: "OwnGit cannot confirm what the running server uses. Restart OwnGit to be sure.",
		ko: "실행 중인 서버가 어떤 값을 쓰는지 확인할 수 없습니다. 확실히 하려면 OwnGit을 다시 시작하세요.",
	},
	"tailscale.wait.restart": {
		en: "Restart OwnGit to finish. The running server does not use the new settings yet.",
		ko: "마무리하려면 OwnGit을 다시 시작하세요. 실행 중인 서버는 아직 새 설정을 쓰지 않습니다.",
	},
	"tailscale.wait.start_option": {
		en: "The running OwnGit was started with --listen or --trusted-proxy, which keeps Tailscale from reaching it. Start it without that option.",
		ko: "실행 중인 OwnGit이 --listen 또는 --trusted-proxy 옵션으로 시작되어 Tailscale이 접속할 수 없습니다. 이 옵션 없이 시작하세요.",
	},
	MsgTSMacApp: {
		en: "With the Tailscale app for macOS, HTTPS does not work after the Mac restarts until someone logs in. Turn on automatic login, or use Homebrew's tailscaled, which runs without a login.",
		ko: "macOS용 Tailscale 앱을 쓰면 Mac을 다시 시작한 뒤 누군가 로그인할 때까지 HTTPS가 동작하지 않습니다. 자동 로그인을 켜거나, 로그인 없이 실행되는 Homebrew의 tailscaled를 쓰세요.",
	},
}

// Strings of the Tailscale block of the Settings page.
const (
	MsgTSTitle         MessageCode = "settings.tailscale.title"
	MsgTSIntro         MessageCode = "settings.tailscale.intro"
	MsgTSOff           MessageCode = "settings.tailscale.off"
	MsgTSReady         MessageCode = "settings.tailscale.ready"
	MsgTSWaiting       MessageCode = "settings.tailscale.waiting"
	MsgTSAddress       MessageCode = "settings.tailscale.address"
	MsgTSCloneHint     MessageCode = "settings.tailscale.clone_hint"
	MsgTSTaken         MessageCode = "settings.tailscale.taken"
	MsgTSChanged       MessageCode = "settings.tailscale.changed"
	MsgTSTakenBrief    MessageCode = "settings.tailscale.taken_brief"
	MsgTSChangedBrief  MessageCode = "settings.tailscale.changed_brief"
	MsgTSTurnOn        MessageCode = "settings.tailscale.turn_on"
	MsgTSTurnOnButton  MessageCode = "settings.tailscale.turn_on_button"
	MsgTSTurnOff       MessageCode = "settings.tailscale.turn_off"
	MsgTSTurnOffBtn    MessageCode = "settings.tailscale.turn_off_button"
	MsgTSCertLog       MessageCode = "settings.tailscale.certificate_log"
	MsgTSHome          MessageCode = "settings.tailscale.home_network"
	MsgTSListenOption  MessageCode = "settings.tailscale.listen_option"
	MsgTSHomeHelp      MessageCode = "settings.tailscale.home_network_help"
	MsgTSOnNote        MessageCode = "settings.tailscale.on_note"
	MsgTSOffNote       MessageCode = "settings.tailscale.off_note"
	MsgTSTurnedOn      MessageCode = "settings.tailscale.turned_on"
	MsgTSTurnedOff     MessageCode = "settings.tailscale.turned_off"
	MsgConnTailscaleOn MessageCode = "connection.encrypted_tailscale"
)

var tailscaleBlockCatalog = map[MessageCode]message{
	MsgTSTitle: {en: "Share on your tailnet over HTTPS", ko: "tailnet에서 HTTPS로 공유"},
	MsgTSIntro: {
		en: "Other devices signed in to your tailnet can open OwnGit and clone over HTTPS at this computer's Tailscale name. Tailscale on this computer holds the certificate and encrypts the connection.",
		ko: "tailnet에 로그인한 다른 기기에서 이 컴퓨터의 Tailscale 이름으로 OwnGit을 열고 HTTPS로 클론할 수 있습니다. 인증서는 이 컴퓨터의 Tailscale이 관리하고, 연결도 Tailscale이 암호화합니다.",
	},
	MsgTSOff:     {en: "Off.", ko: "꺼져 있습니다."},
	MsgTSReady:   {en: "On. Encrypted by Tailscale on this computer.", ko: "켜져 있습니다. 이 컴퓨터의 Tailscale이 암호화합니다."},
	MsgTSWaiting: {en: "On, but the address does not work yet.", ko: "켜져 있지만 주소가 아직 동작하지 않습니다."},
	MsgTSAddress: {en: "HTTPS address", ko: "HTTPS 주소"},
	// Value: the HTTPS address.
	MsgTSCloneHint: {
		en: "Clone addresses start with it, such as %sgit/project.git.",
		ko: "클론 주소는 %sgit/project.git처럼 이 주소로 시작합니다.",
	},
	MsgTSTakenBrief: {
		en: "Tailscale's HTTPS port 443 on this computer already serves something else, so sharing cannot be turned on. \"owngit tailscale status\" on this computer shows what is there.",
		ko: "이 컴퓨터에서 Tailscale의 HTTPS 포트 443이 이미 다른 것을 제공하고 있어 공유를 켤 수 없습니다. 이 컴퓨터에서 \"owngit tailscale status\"를 실행하면 무엇이 있는지 볼 수 있습니다.",
	},
	MsgTSChangedBrief: {
		en: "Tailscale's HTTPS port 443 on this computer now has something other than the address OwnGit made. \"owngit tailscale status\" on this computer shows what is there.",
		ko: "이 컴퓨터에서 Tailscale의 HTTPS 포트 443에 OwnGit이 만든 주소 대신 다른 설정이 있습니다. 이 컴퓨터에서 \"owngit tailscale status\"를 실행하면 무엇이 있는지 볼 수 있습니다.",
	},
	MsgTSTaken: {
		en: "Tailscale's HTTPS port 443 on this computer already serves something else, so sharing cannot be turned on. If you no longer need it, remove it with \"tailscale serve\". On the port now:",
		ko: "이 컴퓨터에서 Tailscale의 HTTPS 포트 443이 이미 다른 것을 제공하고 있어 공유를 켤 수 없습니다. 더 이상 필요 없다면 \"tailscale serve\"로 지우세요. 지금 이 포트의 설정:",
	},
	MsgTSChanged: {
		en: "Tailscale's HTTPS port 443 on this computer now has something other than the address OwnGit made:",
		ko: "이 컴퓨터에서 Tailscale의 HTTPS 포트 443에 OwnGit이 만든 주소 대신 다른 설정이 있습니다:",
	},
	MsgTSTurnOn:       {en: "Turn on", ko: "켜기"},
	MsgTSTurnOnButton: {en: "Turn on sharing", ko: "공유 켜기"},
	MsgTSTurnOff:      {en: "Turn off", ko: "끄기"},
	MsgTSTurnOffBtn:   {en: "Turn off sharing", ko: "공유 끄기"},
	// Value: this computer's MagicDNS name.
	MsgTSCertLog: {
		en: "When Tailscale issues the certificate for this address, the names of this computer and your tailnet, as in %s, are recorded in a public certificate log. Only the fact that the address was opened is recorded, not your code, repositories, passwords or other content. This computer's name can be changed in the Tailscale admin console.",
		ko: "Tailscale이 이 주소의 인증서를 발급하면 %s처럼 이 컴퓨터와 tailnet의 이름이 공개 인증서 로그에 기록됩니다. 주소를 열었다는 기록만 남을 뿐 코드, 저장소, 비밀번호 같은 내용은 기록되지 않습니다. 이 컴퓨터의 이름은 Tailscale 관리 콘솔에서 바꿀 수 있습니다.",
	},
	// Value: the --listen option.
	MsgTSListenOption: {
		en: "OwnGit was started with --listen %s, which decides where it listens, so turning sharing on keeps that address. To choose whether devices on the home network can also connect, start OwnGit without that option.",
		ko: "OwnGit이 --listen %s 옵션으로 시작되어 이 옵션이 연결 주소를 정합니다. 그래서 공유를 켜도 이 주소를 그대로 씁니다. 홈 네트워크의 기기도 접속할 수 있게 할지 고르려면 이 옵션 없이 OwnGit을 시작하세요.",
	},
	MsgTSHome: {en: "Also allow on the home network (not encrypted)", ko: "홈 네트워크에서도 허용 (암호화되지 않음)"},
	// Values: the listen address with the home network, then without it.
	MsgTSHomeHelp: {
		en: "Ticked, OwnGit listens at %s, and devices on your home network can also connect over plain HTTP. Unticked, it listens at %s, on this computer only, and other devices use the HTTPS address. A different listen address applies the next time OwnGit starts.",
		ko: "선택하면 OwnGit이 %s에서 연결을 받아 홈 네트워크의 기기도 일반 HTTP로 접속할 수 있습니다. 선택하지 않으면 %s에서 이 컴퓨터의 연결만 받고, 다른 기기는 HTTPS 주소를 씁니다. 연결을 받는 주소가 바뀌면 OwnGit을 다음에 시작할 때 적용됩니다.",
	},
	MsgTSOnNote: {
		en: "OwnGit asks Tailscale on this computer to answer HTTPS for this computer's name and pass the requests to OwnGit, and saves that address as the address other devices use. If Tailscale already uses port 443 for something else, nothing changes. OwnGit also trusts this computer's loopback address as a proxy, so while sharing is on, programs on this computer can choose the client address that OwnGit uses for sign-in limits.",
		ko: "OwnGit이 이 컴퓨터의 Tailscale에 이 컴퓨터 이름으로 오는 HTTPS 요청을 받아 OwnGit에 넘기도록 요청하고, 그 주소를 다른 기기가 쓰는 주소로 저장합니다. Tailscale이 포트 443을 이미 다른 용도로 쓰고 있으면 아무것도 바꾸지 않습니다. OwnGit은 이 컴퓨터의 루프백 주소도 프록시로 믿으므로, 공유가 켜져 있는 동안 이 컴퓨터의 프로그램은 OwnGit이 로그인 제한에 쓰는 클라이언트 주소를 정할 수 있습니다.",
	},
	MsgTSOffNote: {
		en: "Turning off removes the Tailscale address that OwnGit made, if it is still as OwnGit made it, and takes back the address, name and proxy that sharing added. The listen address stays as it is.",
		ko: "끄면 OwnGit이 만든 Tailscale 주소를 만든 그대로일 때만 지우고, 공유가 추가한 주소, 이름, 프록시를 되돌립니다. 연결을 받는 주소는 그대로 둡니다.",
	},
	MsgTSTurnedOn:  {en: "Sharing on the tailnet is on.", ko: "tailnet 공유를 켰습니다."},
	MsgTSTurnedOff: {en: "Sharing on the tailnet is off.", ko: "tailnet 공유를 껐습니다."},
	// The connection indicator for a request through OwnGit's Tailscale
	// Serve endpoint: Tailscale, not OwnGit, encrypted it.
	MsgConnTailscaleOn: {en: "Encrypted by Tailscale on this computer", ko: "이 컴퓨터의 Tailscale이 암호화함"},
}

func init() {
	for code, entry := range tailscaleBlockCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
	for code, entry := range tailscaleCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

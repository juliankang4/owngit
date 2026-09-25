package webui

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

const (
	MsgTSProblemFailed = MessageCode("tailscale.problem.failed")
	MsgTSWaitUnknown   = MessageCode("tailscale.wait.server_unknown")
	// MsgTSMacApp is the one line for the Tailscale app for macOS.
	MsgTSMacApp = MessageCode("tailscale.mac_app")
	// MsgTSReadBackMacApp is added to the read-back problem for that app.
	MsgTSReadBackMacApp = MessageCode("tailscale.problem.read_back_mac_app")
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
	// Detail: what is on the port.
	"tailscale.problem.port_taken": {
		en: "Tailscale already serves something else on HTTPS port 443 of this computer, so OwnGit changed nothing. If you no longer need it, remove it with \"tailscale serve\" and try again. On the port now:",
		ko: "이 컴퓨터의 HTTPS 포트 443에서 Tailscale이 이미 다른 것을 제공하고 있어 OwnGit은 아무것도 바꾸지 않았습니다. 더 이상 필요 없다면 \"tailscale serve\"로 지운 뒤 다시 시도하세요. 지금 이 포트의 설정:",
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
	// Detail: the name when sharing was turned on.
	"tailscale.problem.name_changed": {
		en: "This computer's name in the tailnet changed after sharing was turned on. Turn sharing off and on again to use the new name. Name when sharing was turned on:",
		ko: "공유를 켠 뒤 tailnet에서 이 컴퓨터의 이름이 바뀌었습니다. 새 이름을 쓰려면 공유를 껐다가 다시 켜세요. 공유를 켤 때의 이름:",
	},
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
		en: "This computer's name in the tailnet changed. Turn sharing off and on again.",
		ko: "tailnet에서 이 컴퓨터의 이름이 바뀌었습니다. 공유를 껐다가 다시 켜세요.",
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

func init() {
	for code, entry := range tailscaleCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

package webui

// The HTTPS port of Tailscale sharing and the replacement of what another
// service has on a port.
const (
	MsgTSPort              MessageCode = "settings.tailscale.port"
	MsgTSPortAuto          MessageCode = "settings.tailscale.port_auto"
	MsgTSPortCustom        MessageCode = "settings.tailscale.port_custom"
	MsgTSPortHelp          MessageCode = "settings.tailscale.port_help"
	MsgTSPortNumber        MessageCode = "settings.tailscale.port_number"
	MsgTSPortNumberHelp    MessageCode = "settings.tailscale.port_number_help"
	MsgTSPortInvalid       MessageCode = "settings.tailscale.port_invalid"
	MsgTSPortMoveWarning   MessageCode = "settings.tailscale.port_move_warning"
	MsgTSMoved             MessageCode = "settings.tailscale.moved"
	MsgTSMoveStopped       MessageCode = "settings.tailscale.move_stopped"
	MsgTSReplaceTitle      MessageCode = "settings.tailscale.replace_title"
	MsgTSReplaceLead       MessageCode = "settings.tailscale.replace_lead"
	MsgTSReplacePort       MessageCode = "settings.tailscale.replace_port"
	MsgTSReplaceWarning    MessageCode = "settings.tailscale.replace_warning"
	MsgTSReplaceButton     MessageCode = "settings.tailscale.replace_button"
	MsgTSReplaceNotAllowed MessageCode = "settings.tailscale.replace_not_allowed"
	// MsgTSTakenBelow introduces the ports turning on would try when what
	// is on each is listed below, port by port.
	MsgTSTakenBelow MessageCode = "settings.tailscale.taken_below"
)

var tailscalePortCatalog = map[MessageCode]message{
	MsgTSPort:       {en: "HTTPS port", ko: "HTTPS 포트"},
	MsgTSPortAuto:   {en: "Automatic", ko: "자동"},
	MsgTSPortCustom: {en: "Custom", ko: "직접 지정"},
	MsgTSPortHelp: {
		en: "Automatic uses 443, or 8443 or 10000 when something else is on 443, and keeps the port sharing uses now. Custom uses the port you enter.",
		ko: "자동은 443을 쓰고, 443에 다른 것이 있으면 8443이나 10000을 씁니다. 공유가 이미 쓰는 포트는 그대로 둡니다. 직접 지정은 입력한 포트를 씁니다.",
	},
	MsgTSPortNumber:     {en: "Port", ko: "포트 번호"},
	MsgTSPortNumberHelp: {en: "From 1 to 65535.", ko: "1부터 65535까지입니다."},
	MsgTSPortInvalid:    {en: "Enter a port from 1 to 65535.", ko: "1부터 65535까지의 포트를 입력하세요."},
	MsgTSPortMoveWarning: {
		en: "Saving another port moves sharing to a new address, and %s stops working. Clones that use it need the new address, for example with git remote set-url.",
		ko: "다른 포트를 저장하면 공유가 새 주소로 옮겨지고 %s 주소는 더는 쓸 수 없습니다. 이 주소를 쓰는 클론은 git remote set-url 등으로 새 주소로 바꿔야 합니다.",
	},
	MsgTSMoved: {
		en: "Sharing moved to its new address, below. The earlier address no longer works, so clones that use it need the new one, for example with git remote set-url.",
		ko: "공유를 아래의 새 주소로 옮겼습니다. 예전 주소는 더는 쓸 수 없으니, 그 주소를 쓰는 클론은 git remote set-url 등으로 새 주소로 바꿔야 합니다.",
	},
	MsgTSMoveStopped: {
		en: "Sharing was turned off at its earlier address to move it, and could not be turned on at the new port, so it is off now. Why:",
		ko: "공유를 옮기려고 예전 주소에서 껐지만 새 포트에서 켜지 못해 지금은 공유가 꺼져 있습니다. 이유:",
	},
	MsgTSTakenBelow: {
		en: "Tailscale on this computer already serves something else on each HTTPS port OwnGit can use, so sharing cannot be turned on. What is on each port is below.",
		ko: "이 컴퓨터에서 OwnGit이 쓸 수 있는 HTTPS 포트마다 Tailscale이 이미 다른 것을 제공하고 있어 공유를 켤 수 없습니다. 포트마다 무엇이 있는지는 아래에 있습니다.",
	},
	MsgTSReplaceTitle: {en: "Replace what is on a port", ko: "포트의 기존 설정 바꾸기"},
	MsgTSReplaceLead: {
		en: "Choosing a free port above keeps everything else working. If you no longer need what Tailscale serves on one of these ports, OwnGit can take its place there. Only that port changes; other ports, names and Funnel stay as they are.",
		ko: "위에서 비어 있는 포트를 고르면 다른 설정은 그대로 둘 수 있습니다. 이 포트들 중 하나에서 Tailscale이 제공하는 것이 더는 필요 없다면, 그 자리에 OwnGit을 둘 수 있습니다. 그 포트만 바뀌고 다른 포트와 이름, Funnel은 그대로입니다.",
	},
	MsgTSReplacePort: {en: "On port %s:", ko: "포트 %s의 설정:"},
	MsgTSReplaceWarning: {
		en: "What is listed here stops answering at %s, and OwnGit answers there instead.",
		ko: "여기 나열된 것은 %s 주소에서 더는 응답하지 않고, 그 자리에서 OwnGit이 응답합니다.",
	},
	MsgTSReplaceButton: {en: "Replace this endpoint", ko: "이 설정 바꾸기"},
	MsgTSReplaceNotAllowed: {
		en: "OwnGit does not replace a port open to Funnel, which would make OwnGit public, or one a \"tailscale serve\" in a terminal holds.",
		ko: "OwnGit은 Funnel로 공개된 포트는 바꾸지 않습니다. 바꾸면 OwnGit이 인터넷에 공개되기 때문입니다. 터미널에서 실행 중인 \"tailscale serve\"가 쓰는 포트도 바꾸지 않습니다.",
	},
	"tailscale.problem.replace_changed": {
		en: "What Tailscale serves on this port changed after you reviewed it, so OwnGit replaced nothing. What is there now is below; review it and choose again.",
		ko: "검토한 뒤에 이 포트에서 Tailscale이 제공하는 설정이 바뀌어 OwnGit은 아무것도 바꾸지 않았습니다. 지금 설정이 아래에 있으니 다시 확인하고 골라 주세요.",
	},
	"tailscale.problem.not_replaceable": {
		en: "This port is open to Funnel or held by a \"tailscale serve\" in a terminal, which OwnGit never replaces, so it changed nothing. Choose another port.",
		ko: "이 포트는 Funnel로 공개되어 있거나 터미널의 \"tailscale serve\"가 쓰고 있어 OwnGit이 바꾸지 않으므로 아무것도 바꾸지 않았습니다. 다른 포트를 고르세요.",
	},
}

func init() {
	for code, entry := range tailscalePortCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

package webui

// Strings for the Network block of the Settings page.

const (
	MsgNetTitle       MessageCode = "settings.network.title"
	MsgNetIntro       MessageCode = "settings.network.intro"
	MsgNetCurrent     MessageCode = "settings.network.current"
	MsgNetCurrentOpt  MessageCode = "settings.network.current_with_option"
	MsgNetRestart     MessageCode = "settings.network.restart"
	MsgNetUnconfirmed MessageCode = "settings.network.unconfirmed"
	MsgNetOptionNote  MessageCode = "settings.network.option_note"
	MsgNetValuesLabel MessageCode = "settings.network.values_label"
	MsgNetSetting     MessageCode = "settings.network.setting"
	MsgNetNext        MessageCode = "settings.network.next_start"
	MsgNetNow         MessageCode = "settings.network.running_now"
	MsgNetNotKnown    MessageCode = "settings.network.not_confirmed"
	MsgNetPending     MessageCode = "settings.network.pending"
	MsgNetNone        MessageCode = "settings.network.none"
	MsgNetNoBaseURL   MessageCode = "settings.network.no_base_url"
	MsgNetFromSaved   MessageCode = "settings.network.from_saved"
	MsgNetFromDefault MessageCode = "settings.network.from_default"
	MsgNetFromOption  MessageCode = "settings.network.from_option"
	MsgNetListen      MessageCode = "settings.network.listen"
	MsgNetBaseURL     MessageCode = "settings.network.base_url"
	MsgNetHosts       MessageCode = "settings.network.allowed_hosts"
	MsgNetProxies     MessageCode = "settings.network.trusted_proxies"
	MsgNetListenHelp  MessageCode = "settings.network.listen_help"
	MsgNetBaseHelp    MessageCode = "settings.network.base_url_help"
	MsgNetHostsHelp   MessageCode = "settings.network.allowed_hosts_help"
	MsgNetProxiesHelp MessageCode = "settings.network.trusted_proxies_help"
	MsgNetPlainHTTP   MessageCode = "settings.network.plain_http"
	MsgNetNeedsName   MessageCode = "settings.network.needs_name"
	MsgNetHTTPSProxy  MessageCode = "settings.network.https_without_proxy"
	MsgNetAckHelp     MessageCode = "settings.network.ack_help"
	MsgNetChange      MessageCode = "settings.network.change"
	MsgNetSave        MessageCode = "settings.network.save"
	MsgNetSaveNote    MessageCode = "settings.network.save_note"
	MsgNetReset       MessageCode = "settings.network.reset"
	MsgNetSaved       MessageCode = "settings.network.saved"
	MsgNetStale       MessageCode = "settings.network.stale"
	MsgNetBadListen   MessageCode = "settings.network.invalid_listen"
	MsgNetBadBaseURL  MessageCode = "settings.network.invalid_base_url"
	MsgNetBadHost     MessageCode = "settings.network.invalid_host"
	MsgNetBadProxy    MessageCode = "settings.network.invalid_proxy"
)

var networkCatalog = map[MessageCode]message{
	MsgNetTitle: {en: "Network", ko: "네트워크"},
	MsgNetIntro: {
		en: "How this computer and other devices reach OwnGit. Saved values apply the next time OwnGit starts. The running server keeps its current values until then.",
		ko: "이 컴퓨터와 다른 기기가 OwnGit에 접속하는 방법입니다. 저장한 값은 OwnGit을 다음에 시작할 때 적용되며, 그때까지 실행 중인 서버는 지금 값을 그대로 씁니다.",
	},
	MsgNetCurrent: {
		en: "The running server uses the saved settings.",
		ko: "실행 중인 서버가 저장된 설정을 쓰고 있습니다.",
	},
	MsgNetCurrentOpt: {
		en: "Apart from the values given as start options, the running server uses the saved settings.",
		ko: "시작 옵션으로 준 값을 빼면 실행 중인 서버가 저장된 설정을 쓰고 있습니다.",
	},
	MsgNetRestart: {
		en: "Saved changes are waiting. Restart OwnGit to apply them.",
		ko: "저장한 변경이 아직 적용되지 않았습니다. OwnGit을 다시 시작하면 적용됩니다.",
	},
	MsgNetUnconfirmed: {
		en: "OwnGit cannot confirm the values this server runs with. The saved settings apply at the next start.",
		ko: "이 서버가 실제로 쓰는 값을 확인할 수 없습니다. 저장된 설정은 다음 시작 때 적용됩니다.",
	},
	MsgNetOptionNote: {
		en: "This server was started with an option such as --listen, --base-url or --trusted-proxy, which replaces the saved value for this run. If a service starts OwnGit with that option, remove it there so the saved value applies.",
		ko: "이 서버는 --listen, --base-url, --trusted-proxy 같은 옵션으로 시작되어 이번 실행에서는 저장된 값 대신 옵션 값을 씁니다. 서비스가 이 옵션으로 OwnGit을 시작한다면 서비스 정의에서 옵션을 지워야 저장된 값이 적용됩니다.",
	},
	MsgNetValuesLabel: {en: "Network settings at the next start and now", ko: "다음 시작 때와 지금의 네트워크 설정"},
	MsgNetSetting:     {en: "Setting", ko: "항목"},
	MsgNetNext:        {en: "Next start", ko: "다음 시작"},
	MsgNetNow:         {en: "Running now", ko: "지금 실행 중"},
	MsgNetNotKnown:    {en: "Not confirmed", ko: "확인할 수 없음"},
	MsgNetPending:     {en: "Applies at next start", ko: "다음 시작 때 적용"},
	MsgNetNone:        {en: "None", ko: "없음"},
	MsgNetNoBaseURL: {
		en: "None. Each device uses the address it opened.",
		ko: "없음. 각 기기가 연 주소를 그대로 씁니다.",
	},
	MsgNetFromSaved:   {en: "saved", ko: "저장됨"},
	MsgNetFromDefault: {en: "default", ko: "기본값"},
	MsgNetFromOption:  {en: "start option", ko: "시작 옵션"},
	MsgNetListen:      {en: "This computer's address", ko: "이 컴퓨터의 주소"},
	MsgNetBaseURL:     {en: "Address other devices use", ko: "다른 기기가 쓰는 주소"},
	MsgNetHosts:       {en: "Allowed names", ko: "허용한 이름"},
	MsgNetProxies:     {en: "Trusted proxies", ko: "신뢰하는 프록시"},
	MsgNetListenHelp: {
		en: "Where OwnGit listens, as address:port. Leave it empty for %s, which only this computer can reach. %s listens on every network of this computer.",
		ko: "OwnGit이 연결을 받는 곳으로, 주소:포트 형식입니다. 비워 두면 이 컴퓨터에서만 접속할 수 있는 %s를 씁니다. %s는 이 컴퓨터의 모든 네트워크에서 연결을 받습니다.",
	},

	MsgNetBaseHelp: {
		en: "The address other devices open, such as http://gitbox.local:7654. Clone addresses use it. Leave it empty to use whatever address each device opened.",
		ko: "다른 기기가 여는 주소입니다. 예: http://gitbox.local:7654. 클론 주소에도 이 주소를 씁니다. 비워 두면 각 기기가 연 주소를 그대로 씁니다.",
	},
	MsgNetHostsHelp: {
		en: "Other names OwnGit accepts, one per line. This computer's own names, such as localhost, are always accepted, as are the names in the two addresses above.",
		ko: "OwnGit이 받아들일 다른 이름을 한 줄에 하나씩 적습니다. localhost처럼 이 컴퓨터 자신을 가리키는 이름과 위 두 주소에 든 이름은 항상 받아들입니다.",
	},

	MsgNetProxiesHelp: {
		en: "Reverse proxies whose forwarded headers OwnGit believes, one IP address or range per line, such as %s. Leave it empty when no proxy sits in front of OwnGit.",
		ko: "OwnGit이 전달 헤더를 믿을 리버스 프록시를 한 줄에 하나씩 적습니다. IP 주소나 범위를 씁니다. 예: %s. OwnGit 앞에 프록시가 없으면 비워 두세요.",
	},

	MsgNetPlainHTTP: {
		en: "At the next start other devices reach OwnGit over plain HTTP, which OwnGit does not encrypt. HTTPS through a reverse proxy or Tailscale protects the connection.",
		ko: "다음 시작부터 다른 기기는 일반 HTTP로 OwnGit에 접속하며, OwnGit은 이 연결을 암호화하지 않습니다. 리버스 프록시나 Tailscale의 HTTPS를 쓰면 연결을 보호할 수 있습니다.",
	},
	MsgNetNeedsName: {
		en: "OwnGit will listen on every network, but no name for other devices is saved. Add the address other devices use or an allowed name, or they will be refused.",
		ko: "OwnGit이 모든 네트워크에서 연결을 받지만 다른 기기가 쓸 이름이 저장되어 있지 않습니다. 다른 기기가 쓰는 주소나 허용할 이름을 추가하지 않으면 다른 기기의 접속은 거부됩니다.",
	},
	MsgNetHTTPSProxy: {
		en: "The address other devices use starts with https, but no reverse proxy is trusted. Add the proxy's address under Trusted proxies so that OwnGit treats requests through it as HTTPS.",
		ko: "다른 기기가 쓰는 주소가 https로 시작하지만 신뢰하는 리버스 프록시가 없습니다. OwnGit이 프록시를 거친 요청을 HTTPS로 다루도록 '신뢰하는 프록시'에 그 프록시의 주소를 추가하세요.",
	},
	MsgNetAckHelp: {
		en: "Needed only when this computer's address reaches other devices, because they would connect over plain HTTP.",
		ko: "이 컴퓨터의 주소가 다른 기기에서 접속할 수 있는 주소일 때만 필요합니다. 그 기기들은 일반 HTTP로 접속하기 때문입니다.",
	},
	MsgNetChange: {en: "Change network settings", ko: "네트워크 설정 바꾸기"},
	MsgNetSave:   {en: "Save network settings", ko: "네트워크 설정 저장"},
	MsgNetSaveNote: {
		en: "Saving does not change the running server. The new values apply the next time OwnGit starts.",
		ko: "저장해도 실행 중인 서버는 바뀌지 않습니다. 새 값은 OwnGit을 다음에 시작할 때 적용됩니다.",
	},
	MsgNetReset: {
		en: "If a saved value keeps you out, run this command on the computer running OwnGit. It removes the saved values for this computer's address and the address other devices use, so OwnGit listens on %s at the next start. Allowed names and trusted proxies stay.",
		ko: "저장한 값 때문에 접속할 수 없게 되면 OwnGit을 실행 중인 컴퓨터에서 이 명령을 실행하세요. 저장된 '이 컴퓨터의 주소'와 '다른 기기가 쓰는 주소'를 지우므로 다음 시작부터 OwnGit이 %s에서 연결을 받습니다. 허용한 이름과 신뢰하는 프록시는 그대로 남습니다.",
	},

	MsgNetSaved: {
		en: "Network settings saved. They apply the next time OwnGit starts; the running server is unchanged.",
		ko: "네트워크 설정을 저장했습니다. OwnGit을 다음에 시작할 때 적용되며, 실행 중인 서버는 바뀌지 않았습니다.",
	},
	MsgNetStale: {
		en: "The network settings changed after this page was opened. Check the current values below and save again.",
		ko: "이 페이지를 연 뒤에 네트워크 설정이 바뀌었습니다. 아래의 현재 값을 확인하고 다시 저장하세요.",
	},
	MsgNetBadListen: {
		en: "Enter an address and a port as address:port, with a port from 1 to 65535.",
		ko: "주소:포트 형식으로 주소와 포트를 입력하세요. 포트는 1부터 65535까지입니다.",
	},

	MsgNetBadBaseURL: {
		en: "Enter an http or https address without a path, such as http://gitbox.local:7654.",
		ko: "http://gitbox.local:7654처럼 경로가 없는 http 또는 https 주소를 입력하세요.",
	},
	MsgNetBadHost: {
		en: "This is not a host name or IP address:",
		ko: "호스트 이름이나 IP 주소가 아닙니다:",
	},
	MsgNetBadProxy: {
		en: "Use an IP address or a range no wider than /8 for IPv4 or /32 for IPv6, without a port or a name:",
		ko: "포트나 이름 없이 IP 주소나 범위를 쓰세요. 범위는 IPv4에서 /8, IPv6에서 /32보다 넓을 수 없습니다:",
	},
}

func init() {
	for code, entry := range networkCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

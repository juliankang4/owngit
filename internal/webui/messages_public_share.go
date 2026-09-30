package webui

// Strings for the public share address: a second address that answers
// share links only, set in the Network settings.

const (
	MsgPublicShareTitle         MessageCode = "settings.public_share.title"
	MsgPublicShareIntro         MessageCode = "settings.public_share.intro"
	MsgPublicShareListen        MessageCode = "settings.public_share.listen"
	MsgPublicShareListenHelp    MessageCode = "settings.public_share.listen_help"
	MsgPublicShareURL           MessageCode = "settings.public_share.url"
	MsgPublicShareURLHelp       MessageCode = "settings.public_share.url_help"
	MsgPublicShareOff           MessageCode = "settings.public_share.off"
	MsgPublicShareNow           MessageCode = "settings.public_share.now"
	MsgPublicShareNowOff        MessageCode = "settings.public_share.now_off"
	MsgPublicShareFailed        MessageCode = "settings.public_share.failed"
	MsgPublicShareInvalid       MessageCode = "settings.public_share.invalid"
	MsgPublicShareWarnOn        MessageCode = "settings.public_share.warn_on"
	MsgPublicShareWarnDirect    MessageCode = "settings.public_share.warn_direct"
	MsgPublicShareWarnPlainHTTP MessageCode = "settings.public_share.warn_plain_http"
	MsgPublicShareWarnProxy     MessageCode = "settings.public_share.warn_proxy"
	MsgShareCreatedPublic       MessageCode = "share.created.public_link"
	MsgShareCreatedPublicClone  MessageCode = "share.created.public_clone"
)

var publicShareCatalog = map[MessageCode]message{
	MsgPublicShareTitle: {en: "Public address for share links", ko: "공유 링크용 공개 주소"},
	MsgPublicShareIntro: {
		en: "A second address that answers share links and nothing else, for a tunnel or reverse proxy on the Internet such as Tailscale Funnel. OwnGit's own address stays as it is. Changes apply at the next start.",
		ko: "공유 링크에만 답하는 두 번째 주소입니다. Tailscale Funnel 같은 터널이나 인터넷에 열린 리버스 프록시에 연결합니다. OwnGit 자체 주소는 그대로입니다. 바꾼 값은 다음 시작부터 적용됩니다.",
	},
	MsgPublicShareListen: {en: "Listen address", ko: "수신 주소"},
	MsgPublicShareListenHelp: {
		en: "The address on this computer that the tunnel or proxy forwards to, such as 127.0.0.1:7655. It needs a port other than OwnGit's own.",
		ko: "터널이나 프록시가 요청을 넘겨 줄 이 컴퓨터의 주소입니다. 예: 127.0.0.1:7655. OwnGit 자체 주소와 다른 포트를 써야 합니다.",
	},
	MsgPublicShareURL: {en: "Public URL", ko: "공개 URL"},
	MsgPublicShareURLHelp: {
		en: "The address visitors use, such as https://box.tail1234.ts.net. New share links show it next to OwnGit's own address.",
		ko: "방문자가 쓰는 주소입니다. 예: https://box.tail1234.ts.net. 새 공유 링크를 만들면 OwnGit 자체 주소와 함께 이 주소도 보여 줍니다.",
	},
	MsgPublicShareOff: {
		en: "Leave both empty to turn the public address off.",
		ko: "공개 주소를 끄려면 두 칸을 모두 비우세요.",
	},
	MsgPublicShareNow:    {en: "The running server answers share links on", ko: "실행 중인 서버가 공유 링크에 답하는 주소:"},
	MsgPublicShareNowOff: {en: "The running server has no public address.", ko: "실행 중인 서버에는 공개 주소가 없습니다."},
	MsgPublicShareFailed: {
		en: "The public address could not listen when OwnGit started, so share links answer only on OwnGit's own address:",
		ko: "OwnGit이 시작할 때 공개 주소에서 수신하지 못해 공유 링크는 OwnGit 자체 주소에서만 열립니다:",
	},
	MsgPublicShareInvalid: {
		en: "Set both the listen address and the public URL, or neither. The listen address is host:port with a port other than OwnGit's own, and the URL an http or https address without a path.",
		ko: "수신 주소와 공개 URL을 모두 적거나 모두 비우세요. 수신 주소는 OwnGit 자체 주소와 다른 포트를 쓰는 host:port이고, URL은 경로가 없는 http 또는 https 주소입니다.",
	},
	MsgPublicShareWarnOn: {
		en: "Anyone on the Internet can reach the public address. Only share links and the files their pages load answer there; every other page, sign-in, the API and Git answer not found. Anyone who has a share link can use it there until it expires or you revoke it.",
		ko: "공개 주소에는 인터넷의 누구나 접속할 수 있습니다. 그곳에서는 공유 링크와 그 페이지가 쓰는 파일만 답하고, 다른 페이지, 로그인, API, Git은 모두 찾을 수 없다고 답합니다. 공유 링크를 가진 사람은 링크가 만료되거나 폐기될 때까지 그 주소에서 쓸 수 있습니다.",
	},
	MsgPublicShareWarnDirect: {
		en: "The listen address reaches beyond this computer, so anyone on that network reaches the public address directly, over plain HTTP and without the tunnel or proxy.",
		ko: "수신 주소가 이 컴퓨터 밖으로 열려 있어, 그 네트워크의 누구나 터널이나 프록시를 거치지 않고 암호화되지 않은 HTTP로 공개 주소에 바로 접속할 수 있습니다.",
	},
	MsgPublicShareWarnPlainHTTP: {
		en: "The public URL uses plain HTTP. Share links, extra passwords and repository content then cross the Internet unencrypted, so anyone on the way can read a link and use it. Use an https address unless you accept that.",
		ko: "공개 URL이 일반 HTTP를 씁니다. 그러면 공유 링크, 추가 비밀번호, 저장소 내용이 암호화되지 않은 채 인터넷을 지나므로 중간에 있는 누구나 링크를 읽고 쓸 수 있습니다. 이를 받아들이는 경우가 아니면 https 주소를 쓰세요.",
	},
	MsgPublicShareWarnProxy: {
		en: "No reverse proxy is trusted. Add the address the tunnel or proxy connects from (127.0.0.1 for Tailscale Funnel) as a trusted proxy. Otherwise every visitor counts as that one address for wrong passwords, and the share cookie is not limited to HTTPS.",
		ko: "신뢰하는 리버스 프록시가 없습니다. 터널이나 프록시가 접속해 오는 주소(Tailscale Funnel이면 127.0.0.1)를 신뢰하는 프록시에 추가하세요. 그러지 않으면 모든 방문자가 틀린 비밀번호 횟수에서 그 주소 하나로 계산되고, 공유 쿠키가 HTTPS 전용으로 제한되지 않습니다.",
	},
	MsgShareCreatedPublic:      {en: "Public share link", ko: "공개 공유 링크"},
	MsgShareCreatedPublicClone: {en: "Public Git address", ko: "공개 Git 주소"},
}

func init() {
	for code, entry := range publicShareCatalog {
		if _, exists := catalog[code]; exists {
			panic("webui: duplicate message code " + string(code))
		}
		catalog[code] = entry
	}
}

import CryptoKit
import Foundation

// What the OwnGit icon shows, decided from the answers of the running
// server and of the bundled owngit command. The icon never decides that
// OwnGit stopped from a failed request alone: only "owngit doctor" saying
// that the server does not run means stopped. Everything else that is not a
// valid status answer is "status unavailable".

/// The access file the server writes at every start: where to ask and the
/// token of the current start.
struct TrayAccess: Decodable, Equatable {
    let url: String
    let token: String
    /// The secret with which the server proves its answers. It never
    /// crosses the connection.
    let proof: String
}

/// statusURL is the status address of the server in the access file.
func statusURL(access: TrayAccess, lang: String) -> URL? {
    dashboardURL(access: access.url).flatMap {
        URL(string: "/tray/status?lang=" + lang, relativeTo: $0)?.absoluteURL
    }
}

/// The headers of the status proof: the icon sends a new nonce with every
/// request, and the server answers with trayProof of that nonce and the
/// exact body.
let trayNonceHeader = "X-OwnGit-Tray-Nonce"
let trayProofHeader = "X-OwnGit-Tray-Proof"

/// newTrayNonce returns 32 new random bytes in unpadded base64url.
func newTrayNonce() -> String {
    base64URL(Data(SymmetricKey(size: .bits256).withUnsafeBytes { Array($0) }))
}

func base64URL(_ data: Data) -> String {
    data.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

private func fromBase64URL(_ text: String) -> Data? {
    var standard = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    standard += String(repeating: "=", count: (4 - standard.count % 4) % 4)
    return Data(base64Encoded: standard)
}

/// trayProofMessage is what the server's HMAC-SHA256 covers.
private func trayProofMessage(nonce: String, body: Data) -> Data {
    Data("owngit tray status\n\(nonce)\n".utf8) + body + Data("\n".utf8)
}

/// proves reports, in constant time, whether proof is the server's proof
/// of body for nonce under secret.
func proves(_ proof: String?, secret: String, nonce: String, body: Data) -> Bool {
    guard let proof, !secret.isEmpty, let code = fromBase64URL(proof), code.count == 32 else {
        return false
    }
    return HMAC<SHA256>.isValidAuthenticationCode(
        code, authenticating: trayProofMessage(nonce: nonce, body: body),
        using: SymmetricKey(data: Data(secret.utf8)))
}

/// dashboardURL is the address of this computer's server from the access
/// file, which only this account can read. The icon never opens a local
/// address taken from a status answer.
func dashboardURL(access: String) -> URL? {
    guard let url = URL(string: access), url.scheme == "http",
          let host = url.host, ["127.0.0.1", "::1", "localhost"].contains(host),
          url.port != nil, url.path.isEmpty || url.path == "/",
          url.query == nil, url.user == nil
    else {
        return nil
    }
    return url
}


/// The answer of "owngit tray status --json".
struct TrayReport: Decodable, Equatable {
    let available: Bool
    let shown: Bool
    let desktop: Bool
    let state_dir: String
    let access_file: String
}

/// The 200 answer of GET /tray/status.
struct TrayStatus: Decodable, Equatable {
    struct Update: Decodable, Equatable {
        let version: String
        let notes_url: String
        let command: String
        let start: String
        let restart: Bool
        let guide_url: String
    }

    struct Finding: Decodable, Equatable {
        let code: String
        let message: String
        let repair: String
        let unchecked: Bool
    }

    struct Push: Decodable, Equatable {
        let repository: String
        let ref: String
        let branch: String
        let pushed_at: String
        let actor_label: String
    }

    let ok: Bool
    let state: String
    let version: String
    let shown: Bool
    let clone_address: String
    let setup_required: Bool
    let update: Update?
    let findings: [Finding]
    let pushes: [Push]

    /// Findings that ask the owner to act; unchecked ones only say that a
    /// check could not run.
    var actionableFindings: [Finding] { findings.filter { !$0.unchecked } }
}

/// The part of "owngit doctor --json" the icon reads when the server does
/// not answer.
struct DoctorReport: Decodable, Equatable {
    struct Finding: Decodable, Equatable {
        let code: String
        let message: String
        let repair: String?
    }

    let running: Bool
    let findings: [Finding]
}

/// What the icon shows.
enum PanelState: Equatable {
    /// The server answered with its status (running or needs attention).
    case status(TrayStatus)
    /// OwnGit does not run. start holds the owngit arguments that start it.
    case stopped(start: [String])
    /// OwnGit's state could not be read: why names the reason.
    case unavailable(why: Unavailable)

    var name: StateName {
        switch self {
        case .status(let status):
            return status.state == "attention" ? .attention : .running
        case .stopped:
            return .stopped
        case .unavailable:
            return .unavailable
        }
    }
}

enum StateName: Equatable {
    case running, attention, stopped, unavailable
}

enum Unavailable: Equatable {
    /// The server did not give a valid status answer.
    case noAnswer
    /// OwnGit runs but does not offer the icon's status: a version older
    /// than the icon.
    case noStatus
    /// OwnGit runs but does not answer; restart holds owngit arguments that
    /// restart it, or is empty.
    case silent(restart: [String])
    /// Another program answers at OwnGit's address.
    case addressTaken
    /// The check itself could not tell; detail is its message.
    case unchecked(detail: String)
}

/// How one status request ended.
enum StatusAnswer: Equatable {
    case status(TrayStatus)
    case unauthorized
    case noConnection
    /// The server answered without the status route.
    case noStatus
    case unavailable
}

/// statusAnswer reads the HTTP answer of GET /tray/status. httpStatus is nil
/// when no connection was made (refused or timed out). A 200 answer counts
/// only when proof is the server's proof of the body for the nonce sent;
/// nothing in the body is read before that.
func statusAnswer(httpStatus: Int?, body: Data?, proof: String?, secret: String, nonce: String) -> StatusAnswer {
    guard let httpStatus else {
        return .noConnection
    }
    if httpStatus == 401 {
        return .unauthorized
    }
    if httpStatus == 404 {
        return .noStatus
    }
    guard httpStatus == 200, let body,
          proves(proof, secret: secret, nonce: nonce, body: body),
          let status = try? JSONDecoder().decode(TrayStatus.self, from: body), status.ok,
          status.state == "running" || status.state == "attention"
    else {
        return .unavailable
    }
    return .status(status)
}

/// StatusClient asks the server for its status: straight at the address of
/// the access file, with no proxy and no redirect, and with a new nonce
/// whose proof the answer must carry.
final class StatusClient: NSObject, URLSessionTaskDelegate {
    private var session: URLSession!

    init(timeout: TimeInterval) {
        super.init()
        let configuration = URLSessionConfiguration.ephemeral
        // The server's address is on this Mac; no proxy may see the token.
        configuration.connectionProxyDictionary = [:]
        configuration.timeoutIntervalForRequest = timeout
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
    }

    /// ask sends one status request to url and calls done, on a background
    /// queue, with how it ended.
    func ask(url: URL, access: TrayAccess, done: @escaping (StatusAnswer) -> Void) {
        let nonce = newTrayNonce()
        var request = URLRequest(url: url)
        request.setValue("Bearer " + access.token, forHTTPHeaderField: "Authorization")
        request.setValue(nonce, forHTTPHeaderField: trayNonceHeader)
        session.dataTask(with: request) { data, response, error in
            let http = response as? HTTPURLResponse
            var httpStatus = http?.statusCode
            if let error = error as? URLError {
                let noConnection: Set<URLError.Code> = [.cannotConnectToHost, .timedOut, .networkConnectionLost]
                httpStatus = noConnection.contains(error.code) ? nil : -1
            } else if error != nil {
                httpStatus = -1
            }
            done(statusAnswer(httpStatus: httpStatus, body: data,
                              proof: http?.value(forHTTPHeaderField: trayProofHeader),
                              secret: access.proof, nonce: nonce))
        }.resume()
    }

    /// A redirect is not followed: its own answer is not the status.
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

/// doctorState reads "owngit doctor --json" after the server did not
/// answer. output is nil when the command failed.
func doctorState(output: Data?) -> PanelState {
    guard let output, let report = try? JSONDecoder().decode(DoctorReport.self, from: output) else {
        return .unavailable(why: .noAnswer)
    }
    let codes = Dictionary(report.findings.map { ($0.code, $0) }, uniquingKeysWith: { first, _ in first })
    if let stopped = codes["doctor.not_running"], let start = helperArguments(stopped.repair) {
        return .stopped(start: start)
    }
    if let silent = codes["doctor.silent"] {
        return .unavailable(why: .silent(restart: helperArguments(silent.repair) ?? []))
    }
    if codes["doctor.address_taken"] != nil {
        return .unavailable(why: .addressTaken)
    }
    if let unchecked = codes["doctor.unchecked_server"] {
        return .unavailable(why: .unchecked(detail: unchecked.message))
    }
    // Running without such a finding, while the status request found no
    // access file or no server: a server that writes no access file is
    // older than the icon.
    return .unavailable(why: report.running ? .noStatus : .noAnswer)
}

/// The service commands the icon may run for the owner, as doctor names
/// them. Anything else is shown, never run.
private let runnableRepairs: Set<String> = [
    "owngit service install", "owngit service start", "owngit service restart",
]

/// helperArguments turns a repair that doctor names into the arguments of
/// the bundled owngit command, or nil when it is not one the icon runs.
func helperArguments(_ repair: String?) -> [String]? {
    guard let repair, runnableRepairs.contains(repair) else {
        return nil
    }
    return Array(repair.split(separator: " ").dropFirst().map(String.init))
}

/// The OwnGit LaunchAgent as the icon reads it: the program it runs and the
/// state directory it passes.
struct InstalledAgent: Equatable {
    let program: String
    let stateDir: String?

    /// isAppHelper reports whether the agent runs a program inside an app
    /// bundle (this layout or an earlier one), which the app that is opened
    /// keeps up to date.
    var isAppHelper: Bool { program.contains(".app/Contents/") }
}

func readInstalledAgent(_ data: Data) -> InstalledAgent? {
    guard let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
          let arguments = plist["ProgramArguments"] as? [String], let program = arguments.first
    else {
        return nil
    }
    var stateDir: String?
    if let index = arguments.firstIndex(of: "--state-dir"), index + 1 < arguments.count {
        stateDir = arguments[index + 1]
    }
    return InstalledAgent(program: program, stateDir: stateDir)
}

/// launchRepair decides what opening the app does about the service: the
/// app makes sure OwnGit runs, and that a service which runs a program of
/// an app runs the helper of this app at this version. A service that runs
/// another program (Homebrew, npm, a program the owner placed) is left as
/// it is. It returns the owngit arguments to run, or nil.
func launchRepair(state: PanelState, agent: InstalledAgent?, helper: String, version: String) -> [String]? {
    let appAgent = agent?.isAppHelper ?? false
    let otherApp = appAgent && agent?.program != helper
    switch state {
    case .stopped(let start):
        return otherApp ? ["service", "install"] : start
    case .status(let status):
        let outdated = agent?.program == helper && status.version != version
        return otherApp || outdated ? ["service", "install"] : nil
    case .unavailable(why: .noStatus):
        // An earlier app's server still runs, possibly from this very path.
        return appAgent ? ["service", "install"] : nil
    case .unavailable:
        return nil
    }
}

/// parsePushTime reads a push time as the server writes it (RFC 3339, with
/// or without fractions of a second).
func parsePushTime(_ text: String) -> Date? {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = formatter.date(from: text) {
        return date
    }
    formatter.formatOptions = [.withInternetDateTime]
    return formatter.date(from: text)
}

/// The icon's words in English and Korean.
struct Words {
    let lang: String
    let running, attention, stopped, unavailable: String
    let version: String
    let cloneAddress, cloneHelp, copy, copied, copyCloneAddress: String
    let recentPushes, noPushes: String
    let openDashboard, finishSetup, setupLine: String
    let updateLine, runInTerminal, copyCommand, updateInDashboard, restartAfterUpdate: String
    let stoppedLine, start, restart, working: String
    let noAnswerLine, noStatusLine, silentLine, addressTakenLine, uncheckedLine: String
    let hide, hideHelp: String
    let settings, settingsTitle, back: String
    let openAtSignIn, openAtSignInHelp, allowSignIn, openLoginItems: String
    let quit, quitHelp: String
    let moveApp: String
    let failed: String
    let readFailed: String
    let noDashboard, openFailed, notConfirmed: String

    static let en = Words(
        lang: "en",
        running: "Running", attention: "Needs attention", stopped: "Not running", unavailable: "Status unavailable",
        version: "Version %@",
        cloneAddress: "Clone address", cloneHelp: "Add the repository name and .git to this address when cloning, as in notes.git.",
        copy: "Copy", copied: "Copied", copyCloneAddress: "Copy clone address",
        recentPushes: "Recent pushes", noPushes: "No pushes yet.",
        openDashboard: "Open dashboard", finishSetup: "Finish setup",
        setupLine: "Setup is not complete. Finish it in your browser.",
        updateLine: "OwnGit %@ is available. You are running %@.", runInTerminal: "Run in a terminal:",
        copyCommand: "Copy command", updateInDashboard: "The dashboard says how to update this install.",
        restartAfterUpdate: "Restart OwnGit after the update.",
        stoppedLine: "OwnGit is not running.", start: "Start OwnGit", restart: "Restart OwnGit",
        working: "Working…",
        noAnswerLine: "OwnGit's status could not be read. The icon asks again in a moment.",
        noStatusLine: "The OwnGit that runs is older than this icon and does not report its status. Update OwnGit, then restart it.",
        silentLine: "OwnGit is running but does not answer.",
        addressTakenLine: "Another program answers at OwnGit's address.",
        uncheckedLine: "OwnGit could not tell whether its server runs: %@",
        hide: "Hide from the menu bar",
        hideHelp: "OwnGit keeps running. To show the icon again, open the OwnGit app, turn on the OwnGit icon switch in the dashboard's Settings, or run owngit tray on.",
        settings: "Settings", settingsTitle: "Icon settings on this Mac", back: "Back",
        openAtSignIn: "Open at sign-in", openAtSignInHelp: "Shows this icon when you sign in. OwnGit itself starts either way.",
        allowSignIn: "Allow OwnGit in System Settings, under Login Items.", openLoginItems: "Open Login Items",
        quit: "Quit the icon", quitHelp: "OwnGit keeps running. Open the OwnGit app to see the icon again.",
        moveApp: "Move OwnGit to your Applications folder, then open it from there.",
        failed: "That did not work: %@",
        readFailed: "OwnGit could not read the icon setting: %@",
        noDashboard: "OwnGit could not read this computer's dashboard address from %@. Start OwnGit and try again.",
        openFailed: "macOS could not open %@.",
        notConfirmed: "OwnGit did not confirm that it answers at this computer's address, so nothing was opened."
    )

    static let ko = Words(
        lang: "ko",
        running: "실행 중", attention: "확인 필요", stopped: "실행되지 않음", unavailable: "상태를 알 수 없음",
        version: "버전 %@",
        cloneAddress: "클론 주소", cloneHelp: "클론할 때 이 주소 뒤에 저장소 이름과 .git을 붙이세요. 예: notes.git",
        copy: "복사", copied: "복사됨", copyCloneAddress: "클론 주소 복사",
        recentPushes: "최근 푸시", noPushes: "아직 푸시가 없습니다.",
        openDashboard: "대시보드 열기", finishSetup: "설정 마치기",
        setupLine: "설정이 끝나지 않았습니다. 브라우저에서 마저 진행하세요.",
        updateLine: "OwnGit %@ 버전이 나왔습니다. 지금 쓰는 버전은 %@입니다.", runInTerminal: "터미널에서 실행:",
        copyCommand: "명령 복사", updateInDashboard: "이 설치를 업데이트하는 방법은 대시보드에 나옵니다.",
        restartAfterUpdate: "업데이트한 뒤 OwnGit을 다시 시작하세요.",
        stoppedLine: "OwnGit이 실행 중이 아닙니다.", start: "OwnGit 시작", restart: "OwnGit 다시 시작",
        working: "처리하는 중…",
        noAnswerLine: "OwnGit 상태를 읽지 못했습니다. 잠시 뒤 다시 확인합니다.",
        noStatusLine: "실행 중인 OwnGit이 이 아이콘보다 오래된 버전이라 상태를 알려 주지 않습니다. OwnGit을 업데이트한 뒤 다시 시작하세요.",
        silentLine: "OwnGit이 실행 중이지만 응답하지 않습니다.",
        addressTakenLine: "다른 프로그램이 OwnGit 주소에서 응답하고 있습니다.",
        uncheckedLine: "OwnGit 서버가 실행 중인지 확인하지 못했습니다: %@",
        hide: "메뉴 막대에서 숨기기",
        hideHelp: "OwnGit은 계속 실행됩니다. 아이콘을 다시 보려면 OwnGit 앱을 다시 열거나, 대시보드 설정에서 OwnGit 아이콘 스위치를 켜거나, owngit tray on을 실행하세요.",
        settings: "설정", settingsTitle: "이 Mac의 아이콘 설정", back: "뒤로",
        openAtSignIn: "로그인할 때 열기", openAtSignInHelp: "로그인하면 이 아이콘을 보여 줍니다. OwnGit 자체는 이 설정과 관계없이 시작됩니다.",
        allowSignIn: "시스템 설정의 로그인 항목에서 OwnGit을 허용하세요.", openLoginItems: "로그인 항목 열기",
        quit: "아이콘 종료", quitHelp: "OwnGit은 계속 실행됩니다. OwnGit 앱을 다시 열면 아이콘이 나타납니다.",
        moveApp: "OwnGit을 응용 프로그램 폴더로 옮긴 뒤 그곳에서 여세요.",
        failed: "완료하지 못했습니다: %@",
        readFailed: "OwnGit 아이콘 설정을 읽지 못했습니다: %@",
        noDashboard: "%@에서 이 컴퓨터의 대시보드 주소를 읽지 못했습니다. OwnGit을 시작한 뒤 다시 해 보세요.",
        openFailed: "macOS가 %@ 주소를 열지 못했습니다.",
        notConfirmed: "이 컴퓨터의 주소에서 OwnGit이 응답하는지 확인하지 못해 아무것도 열지 않았습니다."
    )

    /// forLanguages picks Korean when the first preferred language is
    /// Korean, and English otherwise.
    static func forLanguages(_ preferred: [String]) -> Words {
        preferred.first?.hasPrefix("ko") == true ? .ko : .en
    }

    func stateName(_ name: StateName) -> String {
        switch name {
        case .running: return running
        case .attention: return attention
        case .stopped: return stopped
        case .unavailable: return unavailable
        }
    }
}

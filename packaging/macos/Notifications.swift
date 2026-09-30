import Foundation
import os
import UserNotifications

// Desktop notifications of the OwnGit icon. The server composes, groups and
// words them in its event feed; the icon reads the feed under the rules of
// the status, shows each notification, and keeps the feed's cursor only
// once all of them are shown, so a restart of either side neither repeats
// nor skips one.

/// notificationLog records why a notification was not shown, in the macOS
/// log under the subsystem app.owngit.OwnGit.
let notificationLog = Logger(subsystem: "app.owngit.OwnGit", category: "notifications")

/// notifyKinds are the kinds of notification, in the order the settings
/// list them (state.NotifyKinds).
let notifyKinds = ["push", "pull_request", "check_failed", "import_failed", "backup_failed", "update"]

/// The files of the state directory that notifications use.
let trayHiddenFile = "tray-hidden"
let trayCursorFile = "tray-cursor"

/// The feed cursor is unpadded base64url of at most this many bytes.
let trayCursorLimit = 512

/// One notification of the event feed. Every string is plain text.
struct TrayNotification: Decodable, Equatable {
    let id: String
    let kind: String
    let title: String
    let subtitle: String
    let body: String
    /// The dashboard page that clicking opens.
    let path: String
}

/// The 200 answer of GET /tray/events.
struct TrayEvents: Decodable, Equatable {
    let ok: Bool
    /// What the icon sends next, once it has shown the notifications.
    let cursor: String
    /// The feed started over from now, so there is nothing earlier.
    let started: Bool
    let notifications: [TrayNotification]
}

/// eventsAnswer reads a proven body of the event feed. An answer that is not
/// the feed as the contract describes is nil as a whole, so nothing of it
/// is shown and its cursor is not kept.
func eventsAnswer(_ body: Data) -> TrayEvents? {
    guard let events = try? JSONDecoder().decode(TrayEvents.self, from: body), events.ok,
          validTrayCursor(events.cursor),
          events.notifications.allSatisfy({ !$0.id.isEmpty && notifyKinds.contains($0.kind) && dashboardPath($0.path) })
    else {
        return nil
    }
    return events
}

/// validTrayCursor reports whether cursor has the form of a feed cursor.
func validTrayCursor(_ cursor: String) -> Bool {
    let alphabet = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
    // Unpadded base64 never leaves a single character in its last group.
    return !cursor.isEmpty && cursor.utf8.count <= trayCursorLimit && cursor.utf8.count % 4 != 1
        && cursor.unicodeScalars.allSatisfy(alphabet.contains)
}

/// dashboardPath reports whether path is a page of the dashboard: it begins
/// with one "/", so the address stays on the dashboard's server.
func dashboardPath(_ path: String) -> Bool {
    path.hasPrefix("/") && !path.hasPrefix("//") && path.rangeOfCharacter(from: CharacterSet(charactersIn: "\\\0\r\n")) == nil
}

/// dashboardPage is path on this computer's dashboard, whose address comes
/// from the access file, never from an answer.
func dashboardPage(access: String, path: String) -> URL? {
    guard dashboardPath(path), let base = dashboardURL(access: access) else {
        return nil
    }
    var address = base.absoluteString
    if address.hasSuffix("/") {
        address.removeLast()
    }
    guard let page = URL(string: address + path), page.scheme == base.scheme, page.host == base.host, page.port == base.port else {
        return nil
    }
    return page
}

/// The owner's choice of notifications on this computer, as
/// "owngit tray notifications --json" reports it.
struct NotificationChoice: Decodable, Equatable {
    let all: Bool
    let only_others: Bool
    let kinds: [String: Bool]

    /// requestKinds are the kinds the feed is asked for: every kind that is
    /// on, or none while all notifications are off.
    var requestKinds: [String] {
        all ? notifyKinds.filter { kinds[$0] == true } : []
    }
}

/// eventsURL is the address of the event feed of the server in the access
/// file, asking for choice after cursor, or from now without one.
func eventsURL(access: TrayAccess, lang: String, choice: NotificationChoice, cursor: String?) -> URL? {
    guard let base = dashboardURL(access: access.url),
          var parts = URLComponents(url: URL(string: "/tray/events", relativeTo: base)!.absoluteURL, resolvingAgainstBaseURL: false)
    else {
        return nil
    }
    parts.queryItems = [
        URLQueryItem(name: "lang", value: lang),
        URLQueryItem(name: "kinds", value: choice.requestKinds.joined(separator: ",")),
        URLQueryItem(name: "only_others", value: choice.only_others ? "1" : "0"),
    ] + (cursor.map { [URLQueryItem(name: "cursor", value: $0)] } ?? [])
    return parts.url
}

/// deliver shows notifications in order, except those already in shown,
/// and adds each one shown to shown. It stops at the first one that fails
/// to show, and before any notification once the owner hid the icon. It
/// returns whether the cursor may be kept: everything was shown and the
/// icon still shows.
func deliver(_ notifications: [TrayNotification], shown: inout Set<String>,
             hidden: () -> Bool, show: (TrayNotification) -> Bool) -> Bool {
    for notification in notifications where !shown.contains(notification.id) {
        if hidden() || !show(notification) {
            return false
        }
        shown.insert(notification.id)
    }
    return !hidden()
}

// MARK: - Files of the state directory

/// StateFileError says why a file of the state directory could not be used.
struct StateFileError: Error, CustomStringConvertible {
    let description: String

    init(_ name: String, _ problem: String) {
        description = "\(name): \(problem)"
    }

    static func posix(_ name: String) -> StateFileError {
        StateFileError(name, String(cString: strerror(errno)))
    }
}

/// StateFile reads, replaces and removes a small file of the state
/// directory by the rules of the owngit program (state.OpenOwnFile): the
/// name is never followed as a link, and a file counts only when it is a
/// regular file of this account with no other name. A replacement is
/// written under a temporary name, private from its start, and renamed.
struct StateFile {
    let dir: URL
    let name: String

    /// read returns the content, or nil when there is no such file.
    func read(limit: Int) throws -> Data? {
        let folder = try openFolder()
        defer { close(folder) }
        let file = openat(folder, name, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
        if file < 0 {
            if errno == ENOENT {
                return nil
            }
            throw StateFileError.posix(name)
        }
        defer { close(file) }
        try requireOwn(file)
        var content = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while content.count <= limit {
            let count = Darwin.read(file, &buffer, buffer.count)
            if count < 0 {
                throw StateFileError.posix(name)
            }
            if count == 0 {
                return content
            }
            content.append(buffer, count: count)
        }
        throw StateFileError(name, "larger than \(limit) bytes")
    }

    func replace(with content: Data) throws {
        let folder = try openFolder()
        defer { close(folder) }
        let temporary = "." + name + "-" + base64URL(Data((0..<8).map { _ in UInt8.random(in: 0...255) }))
        let file = openat(folder, temporary, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        if file < 0 {
            throw StateFileError.posix(name)
        }
        var failure: StateFileError?
        // The file is 0600 whatever the umask took away from that mode.
        if fchmod(file, 0o600) != 0 {
            failure = .posix(name)
        }
        if failure == nil {
            let written = content.withUnsafeBytes { write(file, $0.baseAddress, $0.count) }
            if written != content.count || fsync(file) != 0 {
                failure = .posix(name)
            }
        }
        if close(file) != 0 && failure == nil {
            failure = .posix(name)
        }
        if failure == nil && renameat(folder, temporary, folder, name) != 0 {
            failure = .posix(name)
        }
        if let failure {
            unlinkat(folder, temporary, 0)
            throw failure
        }
    }

    /// remove removes the file; a missing file is already removed.
    func remove() throws {
        let folder = try openFolder()
        defer { close(folder) }
        let file = openat(folder, name, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
        if file < 0 {
            if errno == ENOENT {
                return
            }
            throw StateFileError.posix(name)
        }
        defer { close(file) }
        try requireOwn(file)
        if unlinkat(folder, name, 0) != 0 && errno != ENOENT {
            throw StateFileError.posix(name)
        }
    }

    /// exists reports whether something has the name, or whether that
    /// cannot be told.
    func exists() -> Bool {
        var facts = stat()
        return lstat(dir.appendingPathComponent(name).path, &facts) == 0 || errno != ENOENT
    }

    private func openFolder() throws -> Int32 {
        let folder = open(dir.path, O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        if folder < 0 {
            throw StateFileError.posix(dir.path)
        }
        return folder
    }

    private func requireOwn(_ file: Int32) throws {
        var facts = stat()
        if fstat(file, &facts) != 0 {
            throw StateFileError.posix(name)
        }
        if facts.st_mode & S_IFMT != S_IFREG {
            throw StateFileError(name, "not a regular file")
        }
        if facts.st_uid != geteuid() {
            throw StateFileError(name, "belongs to another account")
        }
        if facts.st_nlink != 1 {
            throw StateFileError(name, "has another name as well, so it may be another file")
        }
    }
}

/// readCursor returns the kept feed cursor, or nil when there is none. A
/// file that holds no cursor is an error.
func readCursor(stateDir: URL) throws -> String? {
    guard let content = try StateFile(dir: stateDir, name: trayCursorFile).read(limit: trayCursorLimit + 1) else {
        return nil
    }
    var cursor = String(decoding: content, as: UTF8.self)
    if cursor.hasSuffix("\n") {
        cursor.removeLast()
    }
    guard validTrayCursor(cursor) else {
        throw StateFileError(trayCursorFile, "holds no cursor")
    }
    return cursor
}

func writeCursor(_ cursor: String, stateDir: URL) throws {
    guard validTrayCursor(cursor) else {
        throw StateFileError(trayCursorFile, "not a cursor")
    }
    try StateFile(dir: stateDir, name: trayCursorFile).replace(with: Data((cursor + "\n").utf8))
}

// MARK: - Showing

/// Notifier reads the event feed after a status answer and shows what it
/// reports in macOS Notification Center. It works on its own queue, one
/// read at a time.
final class Notifier {
    private let queue = DispatchQueue(label: "app.owngit.notifications")
    private let client: StatusClient
    /// The notifications shown since the cursor last moved, so that a read
    /// whose cursor could not be kept does not show them again.
    private var shown = Set<String>()
    private var reading = false
    private let lock = NSLock()

    init(client: StatusClient) {
        self.client = client
    }

    /// read reads the feed once, unless a read runs. choice returns the
    /// owner's choice of notifications, or nil when it cannot be read, and
    /// then nothing is read.
    func read(stateDir: URL, lang: String, choice: @escaping () -> NotificationChoice?) {
        lock.lock()
        defer { lock.unlock() }
        if reading {
            return
        }
        reading = true
        queue.async { [self] in
            readOnce(stateDir: stateDir, lang: lang, choice: choice)
            lock.lock()
            reading = false
            lock.unlock()
        }
    }

    private func readOnce(stateDir: URL, lang: String, choice readChoice: () -> NotificationChoice?) {
        let hiddenFile = StateFile(dir: stateDir, name: trayHiddenFile)
        guard let choice = readChoice() else {
            return
        }
        var cursor: String?
        do {
            cursor = try readCursor(stateDir: stateDir)
        } catch {
            log("notifications start again from now: \(error)")
        }
        guard let events = ask(stateDir: stateDir, lang: lang, choice: choice, cursor: cursor) else {
            return
        }
        var allowed = true
        if !events.notifications.isEmpty {
            guard let answer = mayShow() else {
                return
            }
            allowed = answer
        }
        let kept = deliver(events.notifications, shown: &shown, hidden: hiddenFile.exists) { notification in
            // Notifications that macOS settings turned off are dropped as
            // the owner chose there.
            !allowed || show(notification)
        }
        guard kept else {
            return
        }
        do {
            try writeCursor(events.cursor, stateDir: stateDir)
            shown.removeAll()
        } catch {
            log("\(error)")
        }
    }

    /// ask reads the feed, and once more with the access file read again
    /// when the token is refused.
    private func ask(stateDir: URL, lang: String, choice: NotificationChoice, cursor: String?) -> TrayEvents? {
        for _ in 0..<2 {
            let accessFile = stateDir.appendingPathComponent("tray-access.json")
            guard let data = try? Data(contentsOf: accessFile),
                  let access = try? JSONDecoder().decode(TrayAccess.self, from: data),
                  let url = eventsURL(access: access, lang: lang, choice: choice, cursor: cursor)
            else {
                log("the access file cannot be read")
                return nil
            }
            let done = DispatchSemaphore(value: 0)
            var answer = TrayAnswer.unavailable
            client.ask(url: url, access: access) {
                answer = $0
                done.signal()
            }
            done.wait()
            switch answer {
            case .proven(let body):
                guard let events = eventsAnswer(body) else {
                    log("the event feed answered with something that is not OwnGit's feed")
                    return nil
                }
                return events
            case .unauthorized:
                continue
            default:
                log("the event feed did not answer: \(answer)")
                return nil
            }
        }
        log("the event feed refused the token")
        return nil
    }

    /// mayShow reports whether macOS lets the icon show notifications,
    /// asking the owner the first time. It is false when the owner refused,
    /// and nil when macOS could not ask.
    private func mayShow() -> Bool? {
        let center = UNUserNotificationCenter.current()
        let done = DispatchSemaphore(value: 0)
        var status = UNAuthorizationStatus.notDetermined
        center.getNotificationSettings {
            status = $0.authorizationStatus
            done.signal()
        }
        done.wait()
        if status != .notDetermined {
            return status != .denied
        }
        var granted: Bool?
        center.requestAuthorization(options: [.alert, .sound]) { answer, error in
            if let error {
                self.log("macOS did not ask about notifications: \(error.localizedDescription)")
            } else {
                granted = answer
            }
            done.signal()
        }
        done.wait()
        return granted
    }

    /// show hands one notification to macOS and waits for its answer.
    private func show(_ notification: TrayNotification) -> Bool {
        let content = UNMutableNotificationContent()
        content.title = notification.title
        content.subtitle = notification.subtitle
        content.body = notification.body
        content.threadIdentifier = notification.kind
        content.userInfo = ["path": notification.path]
        content.sound = .default
        let done = DispatchSemaphore(value: 0)
        var failure: Error?
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: notification.id, content: content, trigger: nil)) {
            failure = $0
            done.signal()
        }
        done.wait()
        if let failure {
            log("macOS did not show a notification: \(failure.localizedDescription)")
        }
        return failure == nil
    }

    private func log(_ message: String) {
        notificationLog.error("\(message, privacy: .public)")
    }
}

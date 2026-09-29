import AppKit
import Foundation
import ServiceManagement

// OwnGit.app is the OwnGit icon in the menu bar. The server belongs to the
// OwnGit service, a LaunchAgent that runs the owngit binary inside this app
// and starts whenever the owner logs in. Opening the app makes sure that
// service exists and runs; quitting or hiding the icon never stops it.

// The owngit binary inside the app. Apple reserves Contents/Helpers for helper
// tools; tools/release (appHelperPath) places the binary at the same path.
private let helperPath = "Contents/Helpers/owngit"

// How often the icon asks the server while the panel is open, and while it
// is closed.
private let openPanelInterval: TimeInterval = 5
private let closedPanelInterval: TimeInterval = 30

// The server's checkup can take up to a minute; a slower answer counts as no
// answer.
private let statusTimeout: TimeInterval = 70

/// What the bundled owngit command printed and how it ended.
struct HelperResult {
    let status: Int32
    let output: Data
    let errorOutput: String

    /// failureText is the reason to show: the JSON error message, else the
    /// last lines of the error output, else the exit status.
    var failureText: String {
        struct Failure: Decodable {
            struct Detail: Decodable { let message: String }
            let error: Detail
        }
        if let failure = try? JSONDecoder().decode(Failure.self, from: output) {
            return failure.error.message
        }
        let text = errorOutput.isEmpty ? String(decoding: output, as: UTF8.self) : errorOutput
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? "exit status \(status)" : String(trimmed.suffix(600))
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, NSPopoverDelegate {
    private let words = Words.forLanguages(Locale.preferredLanguages)
    private let helper = Bundle.main.bundleURL.appendingPathComponent(helperPath)
    private let appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
    private let agentURL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/LaunchAgents/app.owngit.server.plist")
    private var agent: InstalledAgent?
    private var stateDir = URL(fileURLWithPath: "/")
    private var statusItem: NSStatusItem?
    private let popover = NSPopover()
    private lazy var panel = PanelViewController(words: words, appVersion: appVersion) { [weak self] action in
        self?.perform(action)
    }
    private var model = PanelModel()
    private var timer: Timer?
    /// The callers waiting for the check that runs now, or nil when none
    /// runs.
    private var waiting: [(PanelState) -> Void]?
    private let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        // The server's address is on this Mac; no proxy may see the token.
        configuration.connectionProxyDictionary = [:]
        configuration.timeoutIntervalForRequest = statusTimeout
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: configuration)
    }()

    func applicationDidFinishLaunching(_ notification: Notification) {
        popover.behavior = .transient
        popover.contentViewController = panel
        popover.delegate = self
        if runsFromTemporaryPlace() {
            model.misplaced = true
            showIcon()
            showPanel()
            return
        }
        readAgent()
        setUpSignInOnce()
        guard FileManager.default.fileExists(atPath: stateDir.path) else {
            // Nothing ran yet: the first check finds OwnGit stopped and
            // installs the service.
            startVisible(openPanel: true)
            return
        }
        runHelper(["tray", "status", "--json", "--state-dir", stateDir.path]) { [self] result in
            guard result.status == 0, let report = try? JSONDecoder().decode(TrayReport.self, from: result.output) else {
                fail(String(format: words.readFailed, result.failureText))
                return
            }
            if report.available && report.shown {
                startVisible(openPanel: false)
            } else {
                hideIcon()
            }
        }
    }

    /// Opening the app again while it runs shows the panel, and shows the
    /// icon again when it was hidden.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if statusItem != nil {
            showPanel()
            return false
        }
        runHelper(["tray", "on", "--json", "--state-dir", stateDir.path]) { [self] result in
            guard result.status == 0 else {
                fail(String(format: words.readFailed, result.failureText))
                return
            }
            startVisible(openPanel: true)
        }
        return false
    }

    // MARK: - Where things are

    /// runsFromTemporaryPlace reports whether the app runs from a place the
    /// service could not start it from later: a disk image or another
    /// read-only volume, or the random folder macOS runs a downloaded app
    /// from until it is moved.
    private func runsFromTemporaryPlace() -> Bool {
        let bundle = Bundle.main.bundleURL
        let readOnly = (try? bundle.resourceValues(forKeys: [.volumeIsReadOnlyKey]))?.volumeIsReadOnly ?? false
        return readOnly || bundle.path.contains("/AppTranslocation/")
    }

    /// readAgent reads the OwnGit LaunchAgent, which names the state
    /// directory of the service. Without one the default state directory
    /// applies, the one Homebrew's service and a new service use.
    private func readAgent() {
        agent = (try? Data(contentsOf: agentURL)).flatMap(readInstalledAgent)
        if let dir = agent?.stateDir {
            stateDir = URL(fileURLWithPath: dir)
        } else {
            let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            stateDir = support.appendingPathComponent("owngit")
        }
    }

    // MARK: - Icon and panel

    private func startVisible(openPanel: Bool) {
        showIcon()
        if openPanel {
            showPanel()
        }
        refresh { [self] state in
            if let repair = launchRepair(state: state, agent: agent, helper: helper.path, version: appVersion) {
                run(repair)
            }
        }
    }

    private func showIcon() {
        if statusItem == nil {
            let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
            item.button?.target = self
            item.button?.action = #selector(togglePanel)
            statusItem = item
        }
        drawIcon()
        schedule(interval: closedPanelInterval)
    }

    private func hideIcon() {
        popover.performClose(nil)
        if let statusItem {
            NSStatusBar.system.removeStatusItem(statusItem)
        }
        statusItem = nil
        // While hidden, the icon waits for the owner to show it again from
        // the dashboard or with "owngit tray on", which remove this file.
        schedule(interval: openPanelInterval)
    }

    private func drawIcon() {
        guard let button = statusItem?.button else {
            return
        }
        let name = model.misplaced ? nil : model.state?.name
        button.image = menuBarImage(name)
        let label = name.map { "OwnGit, " + words.stateName($0) } ?? "OwnGit"
        button.setAccessibilityLabel(label)
        button.toolTip = label
    }

    @objc private func togglePanel() {
        if popover.isShown {
            popover.performClose(nil)
        } else {
            showPanel()
        }
    }

    private func showPanel() {
        guard let button = statusItem?.button else {
            return
        }
        model.showingSettings = false
        model.signIn = signInState()
        panel.render(model)
        NSApp.activate(ignoringOtherApps: true)
        popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        popover.contentViewController?.view.window?.makeKey()
        panel.focusFirstControl()
        schedule(interval: openPanelInterval)
        refresh()
    }

    func popoverDidClose(_ notification: Notification) {
        model.failure = nil
        schedule(interval: closedPanelInterval)
    }

    private func update(_ change: (inout PanelModel) -> Void) {
        change(&model)
        drawIcon()
        panel.render(model)
        if popover.isShown {
            popover.contentSize = panel.preferredContentSize
        }
    }

    private func schedule(interval: TimeInterval) {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            self?.tick()
        }
    }

    private func tick() {
        if model.misplaced {
            return
        }
        if statusItem == nil {
            let hidden = stateDir.appendingPathComponent("tray-hidden")
            if !FileManager.default.fileExists(atPath: hidden.path) {
                startVisible(openPanel: false)
            }
            return
        }
        refresh()
    }

    // MARK: - Status

    /// refresh asks the server for its status, and asks owngit doctor when
    /// the server does not answer. done receives the new state; a call
    /// while a check runs waits for that check.
    private func refresh(done: ((PanelState) -> Void)? = nil) {
        if model.misplaced {
            return
        }
        if waiting != nil {
            done.map { waiting?.append($0) }
            return
        }
        waiting = done.map { [$0] } ?? []
        requestStatus(retry: true) { [self] state in
            let callers = waiting ?? []
            waiting = nil
            if case .status(let status) = state, !status.shown {
                hideIcon()
                return
            }
            update { $0.state = state }
            callers.forEach { $0(state) }
        }
    }

    private func requestStatus(retry: Bool, done: @escaping (PanelState) -> Void) {
        let accessFile = stateDir.appendingPathComponent("tray-access.json")
        guard let data = try? Data(contentsOf: accessFile),
              let access = try? JSONDecoder().decode(TrayAccess.self, from: data),
              let url = URL(string: access.url + "/tray/status?lang=" + words.lang)
        else {
            // No access file: this state directory's server never started,
            // or it is unreadable. doctor tells which.
            askDoctor(done: done)
            return
        }
        var request = URLRequest(url: url)
        request.setValue("Bearer " + access.token, forHTTPHeaderField: "Authorization")
        session.dataTask(with: request) { data, response, error in
            var httpStatus = (response as? HTTPURLResponse)?.statusCode
            if let error = error as? URLError {
                let noConnection: Set<URLError.Code> = [.cannotConnectToHost, .timedOut, .networkConnectionLost]
                httpStatus = noConnection.contains(error.code) ? nil : -1
            } else if error != nil {
                httpStatus = -1
            }
            let answer = statusAnswer(httpStatus: httpStatus, body: data)
            DispatchQueue.main.async { [self] in
                switch answer {
                case .status(let status):
                    done(.status(status))
                case .unauthorized where retry:
                    // A new start wrote a new token.
                    requestStatus(retry: false, done: done)
                case .unauthorized, .unavailable:
                    done(.unavailable(why: .noAnswer))
                case .noConnection:
                    askDoctor(done: done)
                }
            }
        }.resume()
    }

    private func askDoctor(done: @escaping (PanelState) -> Void) {
        runHelper(["doctor", "--json", "--state-dir", stateDir.path]) { result in
            done(doctorState(output: result.status == 0 ? result.output : nil))
        }
    }

    // MARK: - Actions

    private func perform(_ action: PanelAction) {
        switch action {
        case .openDashboard(let address):
            popover.performClose(nil)
            if let url = URL(string: address) {
                NSWorkspace.shared.open(url)
            }
        case .finishSetup:
            popover.performClose(nil)
            openSetup()
        case .copy(let text):
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
        case .openLink(let address):
            popover.performClose(nil)
            if let url = URL(string: address) {
                NSWorkspace.shared.open(url)
            }
        case .run(let arguments):
            run(arguments)
        case .hide:
            runHelper(["tray", "off", "--json", "--state-dir", stateDir.path]) { [self] result in
                if result.status == 0 {
                    hideIcon()
                } else {
                    update { $0.failure = result.failureText }
                }
            }
        case .settings(let shown):
            update {
                $0.showingSettings = shown
                $0.signIn = signInState()
            }
            panel.focusFirstControl()
        case .openAtSignIn(let on):
            setOpenAtSignIn(on)
        case .openLoginItems:
            SMAppService.openSystemSettingsLoginItems()
        case .quit:
            NSApp.terminate(nil)
        case .close:
            popover.performClose(nil)
        }
    }

    /// run runs an owngit service command that the panel or opening the app
    /// asked for, then checks again. When OwnGit then runs and setup is not
    /// complete, it opens setup in the browser, as starting OwnGit always did.
    private func run(_ arguments: [String]) {
        update {
            $0.busy = true
            $0.failure = nil
        }
        runHelper(arguments) { [self] result in
            update {
                $0.busy = false
                $0.failure = result.status == 0 ? nil : result.failureText
            }
            readAgent()
            refresh { [self] state in
                if case .status(let status) = state, status.setup_required, result.status == 0 {
                    openSetup()
                }
            }
        }
    }

    /// openSetup opens the one-time setup link in the browser.
    private func openSetup() {
        runHelper(["setup-link", "--state-dir", stateDir.path]) { [self] result in
            if result.status != 0 {
                update { $0.failure = result.failureText }
            }
        }
    }

    // MARK: - Open at sign-in

    /// The icon opens at sign-in by default. The choice is asked of macOS
    /// once; after that it is the owner's, in the panel or in System
    /// Settings.
    private func setUpSignInOnce() {
        let key = "OpenAtSignInConfigured"
        if UserDefaults.standard.bool(forKey: key) {
            return
        }
        try? SMAppService.mainApp.register()
        UserDefaults.standard.set(true, forKey: key)
    }

    private func signInState() -> SignIn {
        switch SMAppService.mainApp.status {
        case .enabled: return .on
        case .requiresApproval: return .needsApproval
        default: return .off
        }
    }

    private func setOpenAtSignIn(_ on: Bool) {
        var failure: String?
        do {
            if on {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
        } catch {
            failure = error.localizedDescription
        }
        update {
            $0.signIn = signInState()
            $0.failure = failure
        }
    }

    // MARK: - The bundled owngit command

    /// runHelper runs the owngit binary inside the app away from the main
    /// thread and calls done on the main thread.
    private func runHelper(_ arguments: [String], done: @escaping (HelperResult) -> Void) {
        let executable = helper
        DispatchQueue.global(qos: .userInitiated).async {
            let result = runCommand(executable, arguments)
            DispatchQueue.main.async {
                done(result)
            }
        }
    }

    /// fail tells the owner why the icon cannot work and quits the icon.
    /// OwnGit itself is not affected.
    private func fail(_ message: String) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "OwnGit"
        alert.informativeText = message
        alert.runModal()
        NSApp.terminate(nil)
    }
}

/// runCommand runs executable with arguments and waits for it. Its input is
/// empty, so it never waits for an answer from the owner.
private func runCommand(_ executable: URL, _ arguments: [String]) -> HelperResult {
    let process = Process()
    process.executableURL = executable
    process.arguments = arguments
    var environment = ProcessInfo.processInfo.environment
    environment["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    process.environment = environment
    let output = Pipe()
    let errors = Pipe()
    process.standardInput = FileHandle.nullDevice
    process.standardOutput = output
    process.standardError = errors
    let lock = NSLock()
    var errorData = Data()
    errors.fileHandleForReading.readabilityHandler = { handle in
        let data = handle.availableData
        lock.lock()
        errorData.append(data)
        lock.unlock()
    }
    do {
        try process.run()
    } catch {
        errors.fileHandleForReading.readabilityHandler = nil
        return HelperResult(status: -1, output: Data(), errorOutput: error.localizedDescription)
    }
    let data = output.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    errors.fileHandleForReading.readabilityHandler = nil
    let rest = errors.fileHandleForReading.readDataToEndOfFile()
    lock.lock()
    errorData.append(rest)
    let errorText = String(decoding: errorData, as: UTF8.self)
    lock.unlock()
    return HelperResult(status: process.terminationStatus, output: data, errorOutput: errorText)
}

@main
struct OwnGitLauncher {
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.setActivationPolicy(.accessory)
        application.run()
    }
}

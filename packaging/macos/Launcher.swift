import AppKit
import Foundation
import ServiceManagement

// OwnGit.app is the OwnGit icon in the menu bar. The server belongs to the
// OwnGit service, a LaunchAgent that runs the owngit binary inside this app
// and starts whenever the owner logs in. Opening the app makes sure that
// service exists and runs; quitting or hiding the icon never stops it.


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
    private let helper = ownGitProgram(app: Bundle.main.bundleURL) {
        FileManager.default.isExecutableFile(atPath: $0.path)
    }
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
    /// macOS opened the app because the owner signed in, not because the
    /// owner opened it.
    private var openedAtSignIn = false
    private var timer: Timer?
    /// The callers waiting for the check that runs now, or nil when none
    /// runs.
    private var waiting: [(PanelState) -> Void]?
    private let client = StatusClient(timeout: statusTimeout)

    func applicationDidFinishLaunching(_ notification: Notification) {
        // The open event that started the app is current only now, not yet
        // in applicationWillFinishLaunching.
        let event = NSAppleEventManager.shared().currentAppleEvent
        // An icon that opens itself again after an update counts like a
        // sign-in launch: it keeps a hidden icon hidden.
        openedAtSignIn = event?.eventID == kAEOpenApplication
            && event?.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue == keyAELaunchedAsLogInItem
            || CommandLine.arguments.contains(atSignInArgument)
        popover.behavior = .transient
        // The panel changes size with its state, often right after it
        // opens; without the animation every new size applies at once.
        popover.animates = false
        popover.contentViewController = panel
        popover.delegate = self
        if runsFromTemporaryPlace() {
            fail(words.moveApp)
            return
        }
        guard FileManager.default.isExecutableFile(atPath: helper.path) else {
            fail(String(format: words.noProgram, helper.path))
            return
        }
        // Before the icon registers for sign-in or runs owngit, no other
        // account may be able to replace either program.
        for program in [Bundle.main.executablePath ?? "", helper.path] where protectedPathProblem(program) != nil {
            fail(String(format: words.unprotected, Bundle.main.bundlePath))
            return
        }
        readAgent()
        setUpSignInOnce()
        guard FileManager.default.fileExists(atPath: stateDir.path) else {
            // Nothing ran yet: the first check finds OwnGit stopped and
            // installs the service.
            startVisible()
            return
        }
        runHelper(["tray", "status", "--json", "--state-dir", stateDir.path]) { [self] result in
            guard result.status == 0, let report = try? JSONDecoder().decode(TrayReport.self, from: result.output) else {
                fail(String(format: words.readFailed, result.failureText))
                return
            }
            if report.available && report.shown {
                startVisible()
            } else if report.available && !openedAtSignIn {
                // The owner opened the app: that asks for the icon.
                showAgain()
            } else {
                hideIcon()
            }
        }
    }

    /// Opening the app again while it runs shows the panel, or shows the
    /// icon again when it was hidden.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if statusItem != nil {
            showPanel()
        } else {
            showAgain()
        }
        return false
    }

    /// showAgain turns the hidden icon back on for this computer and shows
    /// it, which also checks the service as opening the app does.
    private func showAgain() {
        runHelper(["tray", "on", "--json", "--state-dir", stateDir.path]) { [self] result in
            guard result.status == 0 else {
                fail(String(format: words.readFailed, result.failureText))
                return
            }
            startVisible()
        }
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

    private func startVisible() {
        showIcon()
        refresh { [self] state in
            // At sign-in launchd starts the service by itself, and a service
            // the owner removed stays removed.
            if openedAtSignIn {
                return
            }
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
        let name = model.state?.name
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
        popover.contentSize = panel.preferredContentSize
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
        popover.contentSize = panel.preferredContentSize
    }

    private func schedule(interval: TimeInterval) {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            self?.tick()
        }
    }

    private func tick() {
        if reopenAfterUpdate() {
            return
        }
        if statusItem == nil {
            let hidden = stateDir.appendingPathComponent("tray-hidden")
            if !FileManager.default.fileExists(atPath: hidden.path) {
                startVisible()
            }
            return
        }
        refresh()
    }

    // MARK: - Status

    /// refresh asks the server for its status, and asks owngit doctor when
    /// the server does not answer. done receives the new state; a call
    /// while a check runs waits for that check.
    /// startingInARow counts the checks in a row that found OwnGit starting.
    private var startingInARow = 0

    private func refresh(done: ((PanelState) -> Void)? = nil) {
        if waiting != nil {
            done.map { waiting?.append($0) }
            return
        }
        waiting = done.map { [$0] } ?? []
        requestStatus(retry: true) { [self] answer in
            let state = boundedStarting(answer, checks: startingInARow)
            startingInARow = state == .unavailable(why: .starting) ? startingInARow + 1 : 0
            let callers = waiting ?? []
            waiting = nil
            if case .status(let status) = state, !status.shown {
                hideIcon()
                return
            }
            update { $0.state = state }
            callers.forEach { $0(state) }
            if state == .unavailable(why: .starting) {
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in self?.refresh() }
            }
        }
    }

    private func requestStatus(retry: Bool, done: @escaping (PanelState) -> Void) {
        let accessFile = stateDir.appendingPathComponent("tray-access.json")
        guard let data = try? Data(contentsOf: accessFile),
              let access = try? JSONDecoder().decode(TrayAccess.self, from: data),
              let url = statusURL(access: access, lang: words.lang)
        else {
            // No access file: this state directory's server never started,
            // or it is unreadable. doctor tells which.
            askDoctor(.noAccessFile, done: done)
            return
        }
        client.ask(url: url, access: access) { answer in
            DispatchQueue.main.async { [self] in
                switch answer {
                case .status(let status):
                    done(.status(status))
                case .unauthorized where retry:
                    // A new start wrote a new token.
                    requestStatus(retry: false, done: done)
                case .notFound:
                    askDoctor(.notFound, done: done)
                case .unauthorized, .unavailable:
                    done(.unavailable(why: .noAnswer))
                case .noConnection:
                    askDoctor(.refused, done: done)
                }
            }
        }
    }

    private func askDoctor(_ asked: DoctorAsked, done: @escaping (PanelState) -> Void) {
        runHelper(["doctor", "--json", "--state-dir", stateDir.path]) { result in
            done(doctorState(output: result.status == 0 ? result.output : nil, asked: asked))
        }
    }

    // MARK: - Actions

    private func perform(_ action: PanelAction) {
        switch action {
        case .openDashboard:
            confirmServer { [self] in openDashboard() }
        case .finishSetup:
            confirmServer { [self] in
                popover.performClose(nil)
                openSetup()
            }
        case .copy(let text):
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
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

    /// confirmServer asks the server for a new proven status right when the
    /// owner asks to open it, and does then only when the answer is proven.
    /// An earlier answer does not count: OwnGit may have stopped since, and
    /// another program may answer at its address.
    private func confirmServer(then: @escaping () -> Void) {
        update {
            $0.busy = true
            $0.failure = nil
        }
        requestStatus(retry: true) { [self] state in
            let proven: Bool
            if case .status = state { proven = true } else { proven = false }
            update {
                $0.busy = false
                $0.state = state
                $0.failure = proven ? nil : words.notConfirmed
            }
            if proven {
                then()
            }
        }
    }

    private func openDashboard() {
        let accessFile = stateDir.appendingPathComponent("tray-access.json")
        guard let data = try? Data(contentsOf: accessFile),
              let access = try? JSONDecoder().decode(TrayAccess.self, from: data),
              let url = dashboardURL(access: access.url)
        else {
            update { $0.failure = String(format: words.noDashboard, accessFile.path) }
            return
        }
        open(url)
    }

    /// open opens an address in the browser and closes the panel, or keeps
    /// the panel open with the failure.
    private func open(_ url: URL) {
        if NSWorkspace.shared.open(url) {
            popover.performClose(nil)
        } else {
            update { $0.failure = String(format: words.openFailed, url.absoluteString) }
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
    /// Settings. A registration that fails is tried again at the next open;
    /// the settings view shows the switch off meanwhile, and turning it on
    /// there shows the reason.
    /// When the app now runs from another place than the one registered,
    /// as after it was moved, the registration is renewed for this place,
    /// unless the owner turned it off in the panel. One turned off in System
    /// Settings stays off there.
    private func setUpSignInOnce() {
        let defaults = UserDefaults.standard
        let here = SignInItem.place
        let configured = defaults.bool(forKey: signInConfiguredKey)
        if configured && defaults.string(forKey: signInPathKey) == here || defaults.bool(forKey: signInOffKey) {
            return
        }
        if (try? SignInItem.register()) != nil {
            defaults.set(true, forKey: signInConfiguredKey)
            defaults.set(here, forKey: signInPathKey)
        }
    }

    /// reopenAfterUpdate opens the icon again from Homebrew's stable place
    /// when an upgrade removed the versioned folder it runs from, and quits
    /// this one. It returns whether it did.
    private func reopenAfterUpdate() -> Bool {
        let bundle = Bundle.main.bundleURL
        guard !FileManager.default.fileExists(atPath: bundle.path),
              let stable = homebrewStableFolder(app: bundle)?.appendingPathComponent("OwnGit.app"),
              FileManager.default.fileExists(atPath: stable.path)
        else {
            return false
        }
        let open = Process()
        open.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        open.arguments = ["-n", stable.path, "--args", atSignInArgument]
        guard (try? open.run()) != nil else {
            return false
        }
        NSApp.terminate(nil)
        return true
    }

    private func signInState() -> SignIn {
        SignInItem.state()
    }

    private func setOpenAtSignIn(_ on: Bool) {
        var failure: String?
        do {
            if on {
                try SignInItem.register()
                UserDefaults.standard.set(SignInItem.place, forKey: signInPathKey)
            } else {
                try SignInItem.unregister()
            }
            UserDefaults.standard.set(!on, forKey: signInOffKey)
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
    // The icon already runs, so "owngit service install" does not open it.
    environment["OWNGIT_FROM_ICON"] = "1"
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

/// SignInItem opens the icon at sign-in. For most places it is the app's
/// own login item (SMAppService). macOS runs a Homebrew app from its
/// versioned Cellar folder, which an upgrade removes, and such an item
/// names that folder; a Homebrew icon is therefore opened by a LaunchAgent
/// that names Homebrew's stable folder, so it survives an upgrade while the
/// icon is not running.
enum SignInItem {
    static let stableApp = homebrewStableFolder(app: Bundle.main.bundleURL)?.appendingPathComponent("OwnGit.app")
    static let agentURL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/LaunchAgents/\(iconAgentLabel).plist")

    /// place is the app the item opens.
    static var place: String { stableApp?.path ?? Bundle.main.bundlePath }

    static func register() throws {
        guard let stableApp else {
            try SMAppService.mainApp.register()
            return
        }
        let agent = iconAgent(app: stableApp.path, bundleID: Bundle.main.bundleIdentifier ?? "")
        let data = try PropertyListSerialization.data(fromPropertyList: agent, format: .xml, options: 0)
        try FileManager.default.createDirectory(at: agentURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try data.write(to: agentURL, options: .atomic)
        // An item an earlier icon registered names a versioned folder.
        if SMAppService.mainApp.status != .notRegistered {
            try? SMAppService.mainApp.unregister()
        }
    }

    static func unregister() throws {
        if FileManager.default.fileExists(atPath: agentURL.path) {
            try FileManager.default.removeItem(at: agentURL)
        }
        // An item that is not registered cannot be unregistered.
        if [.enabled, .requiresApproval].contains(SMAppService.mainApp.status) {
            try SMAppService.mainApp.unregister()
        }
    }

    static func state() -> SignIn {
        var status = SMAppService.mainApp.status
        if stableApp != nil && status != .enabled {
            status = SMAppService.statusForLegacyPlist(at: agentURL)
        }
        switch status {
        case .enabled: return .on
        case .requiresApproval: return .needsApproval
        default: return .off
        }
    }
}

@main
struct OwnGitLauncher {
    static func main() {
        // "owngit service uninstall" runs the launcher with this argument:
        // the icon stops opening at sign-in, and nothing is shown.
        if CommandLine.arguments.dropFirst().first == signInOffArgument {
            // Opening the app again later registers it again.
            for key in [signInConfiguredKey, signInPathKey, signInOffKey] {
                UserDefaults.standard.removeObject(forKey: key)
            }
            var failure = ""
            do {
                try SignInItem.unregister()
            } catch {
                failure = error.localizedDescription
            }
            // The answer is what macOS reports afterwards.
            if SignInItem.state() != .off {
                FileHandle.standardError.write(Data("macOS still opens the icon at sign-in. \(failure)\n".utf8))
                exit(1)
            }
            exit(0)
        }
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.setActivationPolicy(.accessory)
        application.run()
    }
}

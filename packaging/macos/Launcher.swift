import AppKit
import Foundation

private let capturedOutputLimit = 4 * 1024
private let gracefulShutdownLimit: TimeInterval = 15

final class AppDelegate: NSObject, NSApplicationDelegate {
    private let outputLock = NSLock()
    private var capturedOutput = Data()
    private var server: Process?
    private var outputPipe: Pipe?
    private var shutdownRequested = false
    private var terminationReplyPending = false
    private var shutdownTimer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        installMainMenu()
        startServer()
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let server, server.isRunning else {
            return .terminateNow
        }
        if terminationReplyPending {
            return .terminateLater
        }

        shutdownRequested = true
        terminationReplyPending = true
        server.terminate()
        shutdownTimer = Timer.scheduledTimer(withTimeInterval: gracefulShutdownLimit, repeats: false) { [weak self] _ in
            self?.shutdownTimedOut()
        }
        return .terminateLater
    }

    private func installMainMenu() {
        let mainMenu = NSMenu()
        let applicationItem = NSMenuItem()
        let applicationMenu = NSMenu()
        applicationMenu.addItem(
            withTitle: "Quit OwnGit",
            action: #selector(NSApplication.terminate(_:)),
            keyEquivalent: "q"
        )
        applicationItem.submenu = applicationMenu
        mainMenu.addItem(applicationItem)
        NSApp.mainMenu = mainMenu
    }

    private func startServer() {
        guard let resources = Bundle.main.resourceURL else {
            showFatalError("OwnGit resources are unavailable.")
            return
        }
        let executable = resources.appendingPathComponent("bin/owngit")
        guard FileManager.default.isExecutableFile(atPath: executable.path) else {
            showFatalError("The bundled OwnGit executable is unavailable.")
            return
        }

        let pipe = Pipe()
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if !data.isEmpty {
                self?.appendOutput(data)
            }
        }

        let process = Process()
        process.executableURL = executable
        process.arguments = ["serve", "--open"]
        var environment = ProcessInfo.processInfo.environment
        environment["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        process.environment = environment
        process.standardOutput = pipe
        process.standardError = pipe
        process.terminationHandler = { [weak self] finished in
            let status = finished.terminationStatus
            let reason = finished.terminationReason
            DispatchQueue.main.async {
                self?.serverExited(status: status, reason: reason)
            }
        }

        server = process
        outputPipe = pipe
        do {
            try process.run()
        } catch {
            server = nil
            stopReadingOutput()
            showFatalError("OwnGit could not start: \(error.localizedDescription)")
        }
    }

    private func appendOutput(_ data: Data) {
        outputLock.lock()
        defer { outputLock.unlock() }
        capturedOutput.append(data)
        if capturedOutput.count > capturedOutputLimit {
            capturedOutput.removeFirst(capturedOutput.count - capturedOutputLimit)
        }
    }

    private func serverExited(status: Int32, reason: Process.TerminationReason) {
        shutdownTimer?.invalidate()
        shutdownTimer = nil
        stopReadingOutput()
        server = nil

        if launcherExitDisposition(
            shutdownRequested: shutdownRequested,
            status: status,
            reason: reason
        ) == .finishQuit {
            finishQuitAfterServerExit()
            return
        }

        if terminationReplyPending {
            terminationReplyPending = false
            NSApp.reply(toApplicationShouldTerminate: false)
        }
        shutdownRequested = false
        let message = launcherExitMessage(
            status: status,
            reason: reason,
            diagnostics: capturedText()
        )
        showFatalError(message)
    }

    private func finishQuitAfterServerExit() {
        if terminationReplyPending {
            terminationReplyPending = false
            NSApp.reply(toApplicationShouldTerminate: true)
        } else {
            NSApp.terminate(nil)
        }
    }

    private func shutdownTimedOut() {
        guard let server, server.isRunning, terminationReplyPending else {
            return
        }
        terminationReplyPending = false
        NSApp.reply(toApplicationShouldTerminate: false)
        showError(
            title: "OwnGit is still stopping",
            message: "OwnGit did not stop within 15 seconds. It was not force-quit."
        )
    }

    private func stopReadingOutput() {
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        outputPipe = nil
    }

    private func capturedText() -> String {
        outputLock.lock()
        let data = capturedOutput
        outputLock.unlock()
        return String(decoding: data, as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func showFatalError(_ message: String) {
        showError(title: "OwnGit could not continue", message: message)
        NSApp.terminate(nil)
    }

    private func showError(title: String, message: String) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = title
        alert.informativeText = message
        alert.runModal()
    }
}

@main
struct OwnGitLauncher {
    static func main() {
        let application = NSApplication.shared
        let delegate = AppDelegate()
        application.delegate = delegate
        application.setActivationPolicy(.regular)
        application.run()
    }
}

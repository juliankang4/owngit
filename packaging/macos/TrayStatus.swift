import Foundation

enum LauncherExitDisposition: Equatable {
    case finishQuit
    case reportFailure
}

func launcherExitDisposition(
    shutdownRequested: Bool,
    status: Int32,
    reason: Process.TerminationReason
) -> LauncherExitDisposition {
    if shutdownRequested && reason == .exit && status == 0 {
        return .finishQuit
    }
    return .reportFailure
}

func launcherExitSummary(status: Int32, reason: Process.TerminationReason) -> String {
    switch reason {
    case .exit:
        return "OwnGit exited with status \(status)."
    case .uncaughtSignal:
        return "OwnGit stopped after signal \(status)."
    @unknown default:
        return "OwnGit stopped for an unknown reason with status \(status)."
    }
}

func launcherExitMessage(
    status: Int32,
    reason: Process.TerminationReason,
    diagnostics: String
) -> String {
    let summary = launcherExitSummary(status: status, reason: reason)
    let detail = diagnostics.trimmingCharacters(in: .whitespacesAndNewlines)
    return detail.isEmpty ? summary : summary + "\n\n" + detail
}

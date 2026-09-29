import AppKit

// The panel that opens from the OwnGit icon in the menu bar: the state, the
// clone address, the three latest pushes, the dashboard, and this Mac's
// icon settings. It follows the system's Light or Dark appearance, and every
// control can be reached with Tab and used with Space or Return.

/// Whether OwnGit.app opens when the owner signs in.
enum SignIn: Equatable {
    case on, off, needsApproval
}

/// Everything the panel shows.
struct PanelModel: Equatable {
    /// nil until the first check finished.
    var state: PanelState?
    /// A command the owner started from the panel is running.
    var busy = false
    /// Why the last command the owner started failed.
    var failure: String?
    var showingSettings = false
    var signIn = SignIn.off
}

/// What the owner asked for in the panel.
enum PanelAction {
    case openDashboard
    case finishSetup
    case copy(String)
    case run([String])
    case hide
    case settings(Bool)
    case openAtSignIn(Bool)
    case openLoginItems
    case quit
    case close
}

/// A button that keyboard users reach with Tab whatever the system's
/// keyboard navigation setting is, and that calls a closure.
final class PanelButton: NSButton {
    private var handler: ((PanelButton) -> Void)?

    convenience init(title: String, bezel: NSButton.BezelStyle = .push, handler: @escaping (PanelButton) -> Void) {
        self.init(frame: .zero)
        self.title = title
        bezelStyle = bezel
        self.handler = handler
        target = self
        action = #selector(fire)
    }

    override var canBecomeKeyView: Bool { isEnabled }

    @objc private func fire() {
        handler?(self)
    }
}

final class PanelViewController: NSViewController {
    private let words: Words
    private let appVersion: String
    private let perform: (PanelAction) -> Void
    private var stack = NSStackView()
    private var rendered: PanelModel?
    private weak var firstControl: NSView?
    private static let width: CGFloat = 320
    private static let inner: CGFloat = width - 28

    init(words: Words, appVersion: String, perform: @escaping (PanelAction) -> Void) {
        self.words = words
        self.appVersion = appVersion
        self.perform = perform
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) {
        fatalError("not used")
    }

    override func loadView() {
        view = NSView(frame: NSRect(x: 0, y: 0, width: Self.width, height: 100))
        view.setAccessibilityLabel("OwnGit")
    }

    /// Esc closes the panel.
    override func cancelOperation(_ sender: Any?) {
        perform(.close)
    }

    /// focusFirstControl puts keyboard focus on the button titled
    /// preferring when the panel has one, and on its main button otherwise.
    func focusFirstControl(preferring title: String? = nil) {
        guard let window = view.window else {
            return
        }
        window.autorecalculatesKeyViewLoop = true
        window.makeFirstResponder(title.flatMap { $0.isEmpty ? nil : button(titled: $0, in: view) } ?? firstControl)
    }

    private func button(titled title: String, in parent: NSView) -> NSView? {
        for child in parent.subviews {
            if let button = child as? PanelButton, button.title == title {
                return button
            }
            if let found = button(titled: title, in: child) {
                return found
            }
        }
        return nil
    }

    /// render shows model. An unchanged model is not drawn again, so the
    /// periodic refresh does not move keyboard focus.
    func render(_ model: PanelModel) {
        if model == rendered {
            return
        }
        let focusWasInside = view.window?.firstResponder is NSView
        let focusedTitle = (view.window?.firstResponder as? NSButton)?.title
        rendered = model
        // Each state is built in a new stack, measured before it joins the
        // panel: a view already in the panel measures as the panel's size.
        stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10
        stack.edgeInsets = NSEdgeInsets(top: 14, left: 14, bottom: 14, right: 14)
        stack.translatesAutoresizingMaskIntoConstraints = false
        stack.widthAnchor.constraint(equalToConstant: Self.width).isActive = true
        firstControl = nil
        if model.showingSettings {
            buildSettings(model)
        } else {
            buildPanel(model)
        }
        let size = NSSize(width: Self.width, height: stack.fittingSize.height)
        view.subviews.forEach { $0.removeFromSuperview() }
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            stack.topAnchor.constraint(equalTo: view.topAnchor),
        ])
        preferredContentSize = size
        if focusWasInside {
            focusFirstControl(preferring: focusedTitle)
        }
    }

    // MARK: - Main panel

    private func buildPanel(_ model: PanelModel) {
        let state = model.state
        var status: TrayStatus?
        if case .status(let answer)? = state {
            status = answer
        }
        add(header(state: state, status: status))
        if let failure = model.failure {
            add(notice([label(String(format: words.failed, failure), selectable: true)]))
        }
        var noticeAction: NSView?
        if let lines = noticeLines(state: state, busy: model.busy) {
            add(notice(lines))
            noticeAction = lines.first { $0 is PanelButton }
        }
        if let status {
            add(caption(words.cloneAddress))
            add(cloneField(status.clone_address))
            add(label(words.cloneHelp, secondary: true, size: 11))
            add(caption(words.recentPushes))
            add(pushList(status.pushes))
        }
        var primary: PanelButton?
        if let status {
            if status.setup_required {
                primary = PanelButton(title: words.finishSetup) { [perform] _ in perform(.finishSetup) }
            } else {
                primary = PanelButton(title: words.openDashboard) { [perform] _ in perform(.openDashboard) }
            }
        }
        add(footer(primary: primary))
        if primary == nil, let noticeAction {
            firstControl = noticeAction
        }
    }

    private func header(state: PanelState?, status: TrayStatus?) -> NSView {
        let tile = NSImageView(image: tileImage(size: 32))
        tile.setAccessibilityElement(false)
        let name = label("OwnGit", size: 13, weight: .semibold)
        let sub = String(format: words.version, status?.version ?? appVersion)
        let names = NSStackView(views: [name, label(sub, secondary: true, size: 11)])
        names.orientation = .vertical
        names.alignment = .leading
        names.spacing = 1
        let row = NSStackView(views: [tile, names])
        row.spacing = 10
        if let state {
            row.addView(stateLine(state.name), in: .trailing)
        }
        row.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
        return row
    }

    private func stateLine(_ name: StateName) -> NSView {
        let symbol: String
        let color: NSColor
        switch name {
        case .running: (symbol, color) = ("checkmark.circle", .systemGreen)
        case .attention: (symbol, color) = ("exclamationmark.triangle", .systemOrange)
        case .stopped: (symbol, color) = ("circle.slash", .systemRed)
        case .unavailable: (symbol, color) = ("questionmark.circle", .secondaryLabelColor)
        }
        let image = NSImageView(image: NSImage(systemSymbolName: symbol, accessibilityDescription: nil) ?? NSImage())
        image.contentTintColor = color
        image.setAccessibilityElement(false)
        let text = line(words.stateName(name), size: 12, weight: .medium)
        text.textColor = color
        text.setContentCompressionResistancePriority(.required, for: .horizontal)
        let line = NSStackView(views: [image, text])
        line.spacing = 4
        return line
    }

    /// noticeLines is the box under the header for every state other than
    /// plain running.
    private func noticeLines(state: PanelState?, busy: Bool) -> [NSView]? {
        if busy {
            return [label(words.working)]
        }
        switch state {
        case nil:
            return nil
        case .status(let status):
            var lines: [NSView] = []
            if status.setup_required {
                lines.append(label(words.setupLine))
            }
            if let update = status.update {
                lines.append(label(String(format: words.updateLine, update.version, status.version)))
                if update.command.isEmpty {
                    lines.append(label(words.updateInDashboard, secondary: true, size: 11))
                } else {
                    lines.append(label(words.runInTerminal, secondary: true, size: 11))
                    lines.append(code(update.command))
                    lines.append(PanelButton(title: words.copyCommand) { [perform, words] button in
                        perform(.copy(update.command))
                        button.title = words.copied
                    })
                }
                if update.restart {
                    lines.append(label(words.restartAfterUpdate, secondary: true, size: 11))
                }
            }
            for finding in status.actionableFindings {
                lines.append(label(finding.message, selectable: true))
                if !finding.repair.isEmpty {
                    lines.append(code(finding.repair))
                }
            }
            return lines.isEmpty ? nil : lines
        case .stopped(let start):
            return [label(words.stoppedLine),
                    PanelButton(title: words.start) { [perform] _ in perform(.run(start)) }]
        case .unavailable(let why):
            switch why {
            case .noAnswer:
                return [label(words.noAnswerLine)]
            case .noStatus:
                return [label(words.noStatusLine)]
            case .starting:
                return [label(words.startingLine)]
            case .silent(let restart):
                var lines: [NSView] = [label(words.silentLine)]
                if !restart.isEmpty {
                    lines.append(PanelButton(title: words.restart) { [perform] _ in perform(.run(restart)) })
                }
                return lines
            case .addressTaken:
                return [label(words.addressTakenLine)]
            case .unchecked(let detail):
                return [label(String(format: words.uncheckedLine, detail), selectable: true)]
            }
        }
    }

    private func cloneField(_ address: String) -> NSView {
        let field = code(address)
        let copy = PanelButton(title: words.copy) { [perform, words] button in
            perform(.copy(address))
            button.title = words.copied
        }
        copy.setAccessibilityLabel(words.copyCloneAddress)
        copy.controlSize = .small
        field.setContentHuggingPriority(.defaultLow, for: .horizontal)
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        let row = NSStackView(views: [field, copy])
        row.spacing = 8
        row.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
        return row
    }

    private func pushList(_ pushes: [TrayStatus.Push]) -> NSView {
        if pushes.isEmpty {
            return label(words.noPushes, secondary: true)
        }
        let relative = RelativeDateTimeFormatter()
        relative.locale = Locale(identifier: words.lang)
        relative.unitsStyle = .full
        let rows: [NSView] = pushes.map { push in
            let repository = line(push.repository, size: 12, weight: .medium)
            let ref = line(push.branch.isEmpty ? push.ref : push.branch, secondary: true, size: 11)
            ref.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
            let when = parsePushTime(push.pushed_at).map { relative.localizedString(for: $0, relativeTo: Date()) } ?? push.pushed_at
            let time = line(when, secondary: true, size: 11)
            time.setContentCompressionResistancePriority(.required, for: .horizontal)
            let row = NSStackView(views: [repository, ref])
            row.spacing = 8
            row.addView(time, in: .trailing)
            row.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
            row.setAccessibilityElement(true)
            row.setAccessibilityRole(.staticText)
            row.setAccessibilityLabel("\(push.repository), \(push.branch.isEmpty ? push.ref : push.branch), \(when), \(push.actor_label)")
            return row
        }
        let list = NSStackView(views: rows)
        list.orientation = .vertical
        list.alignment = .leading
        list.spacing = 6
        return list
    }

    private func footer(primary: PanelButton?) -> NSView {
        var views: [NSView] = []
        if let primary {
            primary.keyEquivalent = "\r"
            primary.bezelColor = .controlAccentColor
            views.append(primary)
        }
        let settings = PanelButton(title: "", bezel: .texturedRounded) { [perform] _ in perform(.settings(true)) }
        settings.image = NSImage(systemSymbolName: "gearshape", accessibilityDescription: words.settings)
        settings.imagePosition = .imageOnly
        settings.setAccessibilityLabel(words.settings)
        settings.toolTip = words.settings
        let row = NSStackView(views: views)
        row.spacing = 8
        row.addView(settings, in: .trailing)
        row.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
        firstControl = primary ?? settings
        let hide = PanelButton(title: words.hide, bezel: .inline) { [perform] _ in perform(.hide) }
        hide.isBordered = false
        hide.contentTintColor = .linkColor
        let column = NSStackView(views: [row, hide])
        column.orientation = .vertical
        column.alignment = .leading
        column.spacing = 6
        return column
    }

    // MARK: - Settings

    private func buildSettings(_ model: PanelModel) {
        let back = PanelButton(title: words.back, bezel: .texturedRounded) { [perform] _ in perform(.settings(false)) }
        back.image = NSImage(systemSymbolName: "chevron.left", accessibilityDescription: nil)
        back.imagePosition = .imageLeading
        let title = label(words.settingsTitle, size: 13, weight: .semibold)
        let top = NSStackView(views: [back, title])
        top.spacing = 10
        add(top)

        let signIn = PanelButton(title: words.openAtSignIn) { [perform] button in
            perform(.openAtSignIn(button.state == .on))
        }
        signIn.setButtonType(.switch)
        signIn.state = model.signIn == .off ? .off : .on
        add(signIn)
        add(label(words.openAtSignInHelp, secondary: true, size: 11))
        if model.signIn == .needsApproval {
            add(notice([label(words.allowSignIn),
                        PanelButton(title: words.openLoginItems) { [perform] _ in perform(.openLoginItems) }]))
        }
        if let failure = model.failure {
            add(notice([label(String(format: words.failed, failure), selectable: true)]))
        }
        add(separator())
        add(PanelButton(title: words.hide) { [perform] _ in perform(.hide) })
        add(label(words.hideHelp, secondary: true, size: 11))
        add(separator())
        add(PanelButton(title: words.quit) { [perform] _ in perform(.quit) })
        add(label(words.quitHelp, secondary: true, size: 11))
        firstControl = back
    }

    // MARK: - Pieces

    private func add(_ view: NSView) {
        stack.addArrangedSubview(view)
    }

    private func label(_ text: String, secondary: Bool = false, size: CGFloat = 12, weight: NSFont.Weight = .regular, selectable: Bool = false) -> NSTextField {
        let field = NSTextField(wrappingLabelWithString: text)
        field.font = .systemFont(ofSize: size, weight: weight)
        field.textColor = secondary ? .secondaryLabelColor : .labelColor
        field.isSelectable = selectable
        field.preferredMaxLayoutWidth = Self.inner - 20
        return field
    }

    /// line is a single-line label that shortens a long text in the middle.
    private func line(_ text: String, secondary: Bool = false, size: CGFloat = 12, weight: NSFont.Weight = .regular) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .systemFont(ofSize: size, weight: weight)
        field.textColor = secondary ? .secondaryLabelColor : .labelColor
        field.lineBreakMode = .byTruncatingMiddle
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        return field
    }

    private func caption(_ text: String) -> NSTextField {
        let field = label(text, secondary: true, size: 11, weight: .semibold)
        field.setAccessibilityRole(.staticText)
        return field
    }

    /// code shows a command or an address in a monospaced, selectable field.
    private func code(_ text: String) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .monospacedSystemFont(ofSize: 11.5, weight: .regular)
        field.isSelectable = true
        field.lineBreakMode = .byTruncatingMiddle
        field.drawsBackground = true
        field.backgroundColor = .quaternaryLabelColor.withAlphaComponent(0.12)
        field.toolTip = text
        return field
    }

    private func notice(_ lines: [NSView]) -> NSView {
        let column = NSStackView(views: lines)
        column.orientation = .vertical
        column.alignment = .leading
        column.spacing = 6
        column.edgeInsets = NSEdgeInsets(top: 8, left: 10, bottom: 8, right: 10)
        column.wantsLayer = true
        column.layer?.cornerRadius = 6
        column.layer?.backgroundColor = NSColor.quaternaryLabelColor.withAlphaComponent(0.1).cgColor
        column.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
        return column
    }

    private func separator() -> NSView {
        let line = NSBox()
        line.boxType = .separator
        line.widthAnchor.constraint(equalToConstant: Self.inner).isActive = true
        return line
    }
}

// MARK: - OwnGit mark

/// The OwnGit mark in a 40 by 40 grid: a branch with three commits.
private func strokeMark(scale: CGFloat, lineWidth: CGFloat) {
    let path = NSBezierPath()
    path.lineWidth = lineWidth * scale
    path.lineCapStyle = .round
    path.lineJoinStyle = .round
    func point(_ x: CGFloat, _ y: CGFloat) -> NSPoint { NSPoint(x: x * scale, y: y * scale) }
    path.move(to: point(13, 13))
    path.line(to: point(13, 27))
    path.move(to: point(27, 13))
    path.line(to: point(27, 16))
    path.curve(to: point(13, 27), controlPoint1: point(27, 23), controlPoint2: point(13, 19))
    for (x, y) in [(13.0, 11.0), (27.0, 11.0), (13.0, 29.0)] {
        path.appendOval(in: NSRect(x: (x - 3) * scale, y: (y - 3) * scale, width: 6 * scale, height: 6 * scale))
    }
    path.stroke()
}

/// menuBarImage is the monochrome menu bar icon without a background. A
/// stopped server dims the mark and adds a slashed circle; attention adds a
/// dot; an unknown status dims the mark and adds a question mark. The image
/// is a template, so macOS draws it in the menu bar's own color.
func menuBarImage(_ name: StateName?) -> NSImage {
    let size: CGFloat = 18
    let image = NSImage(size: NSSize(width: size, height: size), flipped: true) { _ in
        let scale = size / 40
        let dimmed = name == .stopped || name == .unavailable
        NSColor.black.withAlphaComponent(dimmed ? 0.55 : 1).setStroke()
        strokeMark(scale: scale, lineWidth: 3.6)
        guard name == .stopped || name == .attention || name == .unavailable,
              let context = NSGraphicsContext.current
        else {
            return true
        }
        let badge = NSRect(x: 23.5 * scale, y: 23.5 * scale, width: 17 * scale, height: 17 * scale)
        context.compositingOperation = .clear
        NSBezierPath(ovalIn: badge.insetBy(dx: -1 * scale, dy: -1 * scale)).fill()
        context.compositingOperation = .sourceOver
        NSColor.black.set()
        if name == .attention {
            NSBezierPath(ovalIn: badge.insetBy(dx: 1.5 * scale, dy: 1.5 * scale)).fill()
        } else if name == .unavailable {
            let mark = NSAttributedString(string: "?", attributes: [
                .font: NSFont.systemFont(ofSize: 21 * scale, weight: .heavy),
                .foregroundColor: NSColor.black,
            ])
            let fits = mark.size()
            mark.draw(at: NSPoint(x: badge.midX - fits.width / 2, y: badge.midY - fits.height / 2))
        } else {
            let ring = NSBezierPath(ovalIn: badge.insetBy(dx: 1 * scale, dy: 1 * scale))
            ring.lineWidth = 2 * scale
            ring.stroke()
            let slash = NSBezierPath()
            slash.lineWidth = 2.4 * scale
            slash.lineCapStyle = .round
            slash.move(to: NSPoint(x: 28.5 * scale, y: 35.5 * scale))
            slash.line(to: NSPoint(x: 35.5 * scale, y: 28.5 * scale))
            slash.stroke()
        }
        return true
    }
    image.isTemplate = true
    return image
}

/// tileImage is the OwnGit mark on its blue tile, used in the panel header.
func tileImage(size: CGFloat) -> NSImage {
    NSImage(size: NSSize(width: size, height: size), flipped: true) { _ in
        let scale = size / 40
        NSColor(srgbRed: 0x0A / 255, green: 0x62 / 255, blue: 0xC9 / 255, alpha: 1).setFill()
        NSBezierPath(roundedRect: NSRect(x: 0, y: 0, width: size, height: size), xRadius: 10 * scale, yRadius: 10 * scale).fill()
        NSColor.white.setStroke()
        strokeMark(scale: scale, lineWidth: 2.6)
        return true
    }
}

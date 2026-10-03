import AppKit

// The panel that opens from the OwnGit icon in the menu bar: the state, the
// clone address, the three latest pushes, the dashboard, and this Mac's
// icon settings. It follows the system's Light or Dark appearance, and every
// control can be reached with Tab and used with Space or Return. It draws at
// macOS's standard sizes, or larger as the owner chooses, and scrolls when
// it is taller than the screen.

/// Whether OwnGit.app opens when the owner signs in.
enum SignIn: Equatable {
    case on, off, needsApproval
}

/// How large the panel draws. Default uses macOS's standard sizes, as menus
/// and Control Center do; Large and Larger scale the panel's text, symbols,
/// spacing and width for a large display or one seen from further away.
enum PanelSize: Int, CaseIterable {
    case standard, large, larger

    var scale: CGFloat {
        switch self {
        case .standard: return 1
        case .large: return 1.25
        case .larger: return 1.5
        }
    }

    /// init(stored:) reads the size kept in the app's defaults: one of the
    /// integer choices, and Default for anything else, also for a fraction,
    /// a Boolean or text that would convert to a choice.
    init(stored value: Any?) {
        guard let number = value as? NSNumber,
              CFGetTypeID(number) == CFNumberGetTypeID(),
              !CFNumberIsFloatType(number),
              let size = PanelSize(rawValue: number.intValue)
        else {
            self = .standard
            return
        }
        self = size
    }

    /// The control size of check boxes and the size choice, which grows
    /// with the scale.
    var controlSize: NSControl.ControlSize {
        switch self {
        case .standard: return .regular
        case .large: return .large
        case .larger: return .extraLarge
        }
    }
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
    /// The owner's choice of notifications, nil until it was read.
    var notifications: NotificationChoice?
    /// macOS settings turned OwnGit's notifications off.
    var notificationsDenied = false
    var size = PanelSize.standard
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
    /// Turn a notification setting on or off: "all", "only_others" or a
    /// kind.
    case notify(String, Bool)
    case openNotificationSettings
    /// Draw the panel at this size from now on.
    case size(PanelSize)
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

    override func becomeFirstResponder() -> Bool {
        guard super.becomeFirstResponder() else {
            return false
        }
        revealFocus(self)
        return true
    }

    @objc private func fire() {
        handler?(self)
    }
}

/// A segmented control that keyboard users reach with Tab, like
/// PanelButton, and that calls a closure.
final class PanelSegments: NSSegmentedControl {
    private var handler: ((PanelSegments) -> Void)?

    convenience init(labels: [String], handler: @escaping (PanelSegments) -> Void) {
        self.init(frame: .zero)
        segmentCount = labels.count
        for (index, label) in labels.enumerated() {
            setLabel(label, forSegment: index)
        }
        trackingMode = .selectOne
        self.handler = handler
        target = self
        action = #selector(fire)
    }

    override var canBecomeKeyView: Bool { isEnabled }

    override func becomeFirstResponder() -> Bool {
        guard super.becomeFirstResponder() else {
            return false
        }
        revealFocus(self)
        return true
    }

    @objc private func fire() {
        handler?(self)
    }
}

/// revealFocus scrolls a panel taller than the screen to the control that
/// takes keyboard focus, with room for its focus ring.
private func revealFocus(_ control: NSView) {
    control.scrollToVisible(control.bounds.insetBy(dx: 0, dy: -8))
}

/// The panel's own view. It takes the size keys, which reach a window's
/// views before its controls.
private final class PanelView: NSView {
    var keyEquivalent: ((NSEvent) -> Bool)?

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        keyEquivalent?(event) == true || super.performKeyEquivalent(with: event)
    }
}

/// The scrolled content, laid out from the top.
private final class FlippedView: NSView {
    override var isFlipped: Bool { true }
}

final class PanelViewController: NSViewController {
    private let words: Words
    private let appVersion: String
    private let perform: (PanelAction) -> Void
    private var stack = NSStackView()
    private var rendered: PanelModel?
    private weak var firstControl: NSView?
    private let scroll = NSScrollView()
    private let content = FlippedView()
    private var contentHeight: CGFloat = 100
    /// The size of the panel that render builds now.
    private var size = PanelSize.standard
    /// The tallest the panel may be; taller content scrolls. The app sets it
    /// from the screen of the menu bar icon.
    var maxHeight = CGFloat.greatestFiniteMagnitude {
        didSet {
            if maxHeight != oldValue {
                fit()
            }
        }
    }

    // The panel's measures at Default, which the chosen size scales: 13 pt
    // body text and 11 pt secondary text, macOS's standard and small system
    // font sizes, in a panel wide enough for about 50 characters a line.
    private var scale: CGFloat { size.scale }
    private var body: CGFloat { NSFont.systemFontSize * scale }
    private var small: CGFloat { NSFont.smallSystemFontSize * scale }
    private var width: CGFloat { 344 * scale }
    private var inset: CGFloat { 16 * scale }
    private var inner: CGFloat { width - 2 * inset }

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
        let root = PanelView(frame: NSRect(x: 0, y: 0, width: width, height: contentHeight))
        root.setAccessibilityLabel("OwnGit")
        root.keyEquivalent = { [weak self] event in self?.sizeKey(event) ?? false }
        scroll.frame = root.bounds
        scroll.autoresizingMask = [.width, .height]
        scroll.drawsBackground = false
        scroll.borderType = .noBorder
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.horizontalScrollElasticity = .none
        scroll.documentView = content
        view = root
    }

    /// Esc closes the panel.
    override func cancelOperation(_ sender: Any?) {
        perform(.close)
    }

    /// focusFirstControl puts keyboard focus on the control named preferring
    /// when the panel has one, and on its main control otherwise.
    func focusFirstControl(preferring name: String? = nil) {
        guard let window = view.window else {
            return
        }
        window.autorecalculatesKeyViewLoop = true
        let control = name.flatMap { self.control(named: $0, in: view) } ?? firstControl
        window.makeFirstResponder(control)
        // The popover takes its new size after render; reveal the control
        // again once it has.
        if let control {
            DispatchQueue.main.async { revealFocus(control) }
        }
    }

    /// control finds the control named name: its identifier, which stays the
    /// same across redraws whatever its title shows.
    private func control(named name: String, in parent: NSView) -> NSView? {
        for child in parent.subviews {
            if child.identifier?.rawValue == name {
                return child
            }
            if let found = control(named: name, in: child) {
                return found
            }
        }
        return nil
    }

    /// sizeKey takes Command with + (or =), - and 0, which make the panel
    /// larger, smaller or Default while it is open.
    private func sizeKey(_ event: NSEvent) -> Bool {
        guard event.modifierFlags.intersection([.command, .option, .control]) == .command,
              let current = rendered?.size
        else {
            return false
        }
        let next: PanelSize?
        switch event.charactersIgnoringModifiers {
        case "+", "=": next = PanelSize(rawValue: current.rawValue + 1)
        case "-": next = PanelSize(rawValue: current.rawValue - 1)
        case "0": next = .standard
        default: return false
        }
        guard let next else {
            // Already the largest or the smallest size.
            NSSound.beep()
            return true
        }
        if next != current {
            perform(.size(next))
            NSAccessibility.post(element: view, notification: .announcementRequested, userInfo: [
                .announcement: "\(words.panelSize): \(sizeName(next))",
                .priority: NSAccessibilityPriorityLevel.high.rawValue,
            ])
        }
        return true
    }

    private func sizeName(_ size: PanelSize) -> String {
        switch size {
        case .standard: return words.sizeDefault
        case .large: return words.sizeLarge
        case .larger: return words.sizeLarger
        }
    }

    /// render shows model. An unchanged model is not drawn again, so the
    /// periodic refresh does not move keyboard focus.
    func render(_ model: PanelModel) {
        if model == rendered {
            return
        }
        let focused = view.window?.firstResponder
        let focusWasInside = focused is NSView
        let focusedName = (focused as? NSView)?.identifier?.rawValue
        rendered = model
        size = model.size
        // Each state is built in a new stack, measured before it joins the
        // panel: a view already in the panel measures as the panel's size.
        stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10 * scale
        stack.edgeInsets = NSEdgeInsets(top: inset, left: inset, bottom: inset, right: inset)
        stack.translatesAutoresizingMaskIntoConstraints = false
        stack.widthAnchor.constraint(equalToConstant: width).isActive = true
        firstControl = nil
        if model.showingSettings {
            buildSettings(model)
        } else {
            buildPanel(model)
        }
        contentHeight = stack.fittingSize.height
        view.subviews.forEach { $0.removeFromSuperview() }
        content.subviews.forEach { $0.removeFromSuperview() }
        fit()
        if focusWasInside {
            focusFirstControl(preferring: focusedName)
        }
    }

    /// fit sizes the panel to its content, at most maxHeight tall. Content
    /// that fits sits in the panel itself, so the popover's vibrancy reaches
    /// it; taller content scrolls, with room for a scroll bar that takes
    /// space.
    private func fit() {
        guard isViewLoaded else {
            return
        }
        let scrolls = contentHeight > maxHeight
        let holder: NSView = scrolls ? content : view
        if stack.superview !== holder {
            stack.removeFromSuperview()
            holder.addSubview(stack)
            NSLayoutConstraint.activate([
                stack.leadingAnchor.constraint(equalTo: holder.leadingAnchor),
                stack.topAnchor.constraint(equalTo: holder.topAnchor),
            ])
        }
        if scrolls {
            content.frame = NSRect(x: 0, y: 0, width: width, height: contentHeight)
            if scroll.superview == nil {
                scroll.frame = view.bounds
                view.addSubview(scroll)
            }
        } else {
            scroll.removeFromSuperview()
        }
        var panelWidth = width
        if scrolls && NSScroller.preferredScrollerStyle == .legacy {
            panelWidth += NSScroller.scrollerWidth(for: .regular, scrollerStyle: .legacy)
        }
        preferredContentSize = NSSize(width: panelWidth, height: scrolls ? maxHeight : contentHeight)
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
            add(help(words.cloneHelp))
            add(caption(words.recentPushes))
            add(pushList(status.pushes))
        }
        var primary: PanelButton?
        if let status {
            if status.setup_required {
                primary = button(words.finishSetup, id: "finish-setup") { [perform] _ in perform(.finishSetup) }
            } else {
                primary = button(words.openDashboard, id: "open-dashboard") { [perform] _ in perform(.openDashboard) }
            }
        }
        add(footer(primary: primary))
        if primary == nil, let noticeAction {
            firstControl = noticeAction
        }
    }

    private func header(state: PanelState?, status: TrayStatus?) -> NSView {
        let tile = NSImageView(image: tileImage(size: 32 * scale))
        tile.setAccessibilityElement(false)
        let name = label("OwnGit", weight: .semibold)
        let sub = String(format: words.version, status?.version ?? appVersion)
        let names = NSStackView(views: [name, help(sub)])
        names.orientation = .vertical
        names.alignment = .leading
        names.spacing = 1 * scale
        let row = NSStackView(views: [tile, names])
        row.spacing = 10 * scale
        if let state {
            row.addView(stateLine(state.name), in: .trailing)
        }
        row.widthAnchor.constraint(equalToConstant: inner).isActive = true
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
        image.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: body, weight: .medium)
        image.contentTintColor = color
        image.setAccessibilityElement(false)
        let text = line(words.stateName(name), weight: .medium)
        text.textColor = color
        text.setContentCompressionResistancePriority(.required, for: .horizontal)
        let line = NSStackView(views: [image, text])
        line.spacing = 4 * scale
        // The header's free space stays between the name and the state, which
        // keeps the state at the trailing edge.
        line.setHuggingPriority(.defaultHigh, for: .horizontal)
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
                    lines.append(help(words.updateInDashboard))
                } else {
                    lines.append(help(words.runInTerminal))
                    lines.append(code(update.command))
                    lines.append(button(words.copyCommand, id: "copy-command") { [perform, words] button in
                        perform(.copy(update.command))
                        button.title = words.copied
                    })
                }
                if update.restart {
                    lines.append(help(words.restartAfterUpdate))
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
                    button(words.start, id: "start") { [perform] _ in perform(.run(start)) }]
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
                    lines.append(button(words.restart, id: "restart") { [perform] _ in perform(.run(restart)) })
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
        let copy = button(words.copy, id: "copy-clone-address") { [perform, words] button in
            perform(.copy(address))
            button.title = words.copied
        }
        copy.setAccessibilityLabel(words.copyCloneAddress)
        field.setContentHuggingPriority(.defaultLow, for: .horizontal)
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        let row = NSStackView(views: [field, copy])
        row.spacing = 8 * scale
        row.widthAnchor.constraint(equalToConstant: inner).isActive = true
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
            let repository = line(push.repository, weight: .medium)
            let ref = line(push.branch.isEmpty ? push.ref : push.branch, secondary: true)
            ref.font = .monospacedSystemFont(ofSize: small, weight: .regular)
            let when = parsePushTime(push.pushed_at).map { relative.localizedString(for: $0, relativeTo: Date()) } ?? push.pushed_at
            let time = line(when, secondary: true, size: small)
            time.setContentCompressionResistancePriority(.required, for: .horizontal)
            let row = NSStackView(views: [repository, ref])
            row.spacing = 8 * scale
            row.addView(time, in: .trailing)
            row.widthAnchor.constraint(equalToConstant: inner).isActive = true
            row.setAccessibilityElement(true)
            row.setAccessibilityRole(.staticText)
            row.setAccessibilityLabel("\(push.repository), \(push.branch.isEmpty ? push.ref : push.branch), \(when), \(push.actor_label)")
            return row
        }
        let list = NSStackView(views: rows)
        list.orientation = .vertical
        list.alignment = .leading
        list.spacing = 6 * scale
        return list
    }

    private func footer(primary: PanelButton?) -> NSView {
        var views: [NSView] = []
        if let primary {
            primary.keyEquivalent = "\r"
            primary.bezelColor = .controlAccentColor
            views.append(primary)
        }
        let settings = button("", id: "settings") { [perform] _ in perform(.settings(true)) }
        settings.image = NSImage(systemSymbolName: "gearshape", accessibilityDescription: words.settings)
        settings.imagePosition = .imageOnly
        settings.setAccessibilityLabel(words.settings)
        settings.toolTip = words.settings
        let row = NSStackView(views: views)
        row.spacing = 8 * scale
        row.addView(settings, in: .trailing)
        row.widthAnchor.constraint(equalToConstant: inner).isActive = true
        firstControl = primary ?? settings
        let hide = button(words.hide, id: "hide", inline: true) { [perform] _ in perform(.hide) }
        hide.isBordered = false
        hide.contentTintColor = .linkColor
        let column = NSStackView(views: [row, hide])
        column.orientation = .vertical
        column.alignment = .leading
        column.spacing = 6 * scale
        return column
    }

    // MARK: - Settings

    private func buildSettings(_ model: PanelModel) {
        let back = button(words.back, id: "back") { [perform] _ in perform(.settings(false)) }
        back.image = NSImage(systemSymbolName: "chevron.left", accessibilityDescription: nil)
        back.imagePosition = .imageLeading
        let title = label(words.settingsTitle, weight: .semibold)
        let top = NSStackView(views: [back, title])
        top.spacing = 10 * scale
        add(top)

        add(caption(words.panelSize))
        add(sizeChoice(model.size))
        add(help(words.panelSizeHelp))
        add(separator())

        let signIn = toggle(words.openAtSignIn, id: "open-at-sign-in", on: model.signIn != .off) { [perform] button in
            perform(.openAtSignIn(button.state == .on))
        }
        add(signIn)
        add(help(words.openAtSignInHelp))
        if model.signIn == .needsApproval {
            add(notice([label(words.allowSignIn),
                        button(words.openLoginItems, id: "open-login-items") { [perform] _ in perform(.openLoginItems) }]))
        }
        if let failure = model.failure {
            add(notice([label(String(format: words.failed, failure), selectable: true)]))
        }
        if let choice = model.notifications {
            add(separator())
            buildNotifications(choice, denied: model.notificationsDenied)
        }
        add(separator())
        add(button(words.hide, id: "hide") { [perform] _ in perform(.hide) })
        add(help(words.hideHelp))
        add(separator())
        add(button(words.quit, id: "quit") { [perform] _ in perform(.quit) })
        add(help(words.quitHelp))
        firstControl = back
    }

    /// sizeChoice is the choice of Default, Large and Larger. Tab reaches
    /// it, the arrow keys move between the sizes, and Space chooses one.
    private func sizeChoice(_ current: PanelSize) -> NSView {
        let choice = PanelSegments(labels: PanelSize.allCases.map(sizeName)) { [perform] control in
            if let size = PanelSize(rawValue: control.selectedSegment) {
                perform(.size(size))
            }
        }
        choice.controlSize = size.controlSize
        choice.font = .systemFont(ofSize: body)
        choice.segmentDistribution = .fillEqually
        choice.selectedSegment = current.rawValue
        choice.identifier = NSUserInterfaceItemIdentifier("panel-size")
        choice.setAccessibilityLabel(words.panelSize)
        choice.widthAnchor.constraint(equalToConstant: inner).isActive = true
        return choice
    }

    /// buildNotifications shows the notification switches: one for all,
    /// "only what I did not do", and one per kind. The others keep their
    /// own choice while all are off.
    private func buildNotifications(_ choice: NotificationChoice, denied: Bool) {
        add(caption(words.notifications))
        if denied {
            add(notice([label(words.notificationsOff),
                        button(words.openNotificationSettings, id: "open-notification-settings") { [perform] _ in perform(.openNotificationSettings) }]))
        }
        add(notificationSwitch("all", words.notifyAll, on: choice.all, enabled: true))
        add(notificationSwitch("only_others", words.notifyOthers, on: choice.only_others, enabled: choice.all))
        add(help(words.notifyOthersHelp))
        for kind in notifyKinds {
            add(notificationSwitch(kind, words.kindNames[kind] ?? kind, on: choice.kinds[kind] == true, enabled: choice.all))
        }
        add(help(words.notificationsHint))
    }

    private func notificationSwitch(_ setting: String, _ title: String, on: Bool, enabled: Bool) -> PanelButton {
        let button = toggle(title, id: "notify-" + setting, on: on) { [perform] button in
            perform(.notify(setting, button.state == .on))
        }
        button.isEnabled = enabled
        return button
    }

    // MARK: - Pieces

    private func add(_ view: NSView) {
        stack.addArrangedSubview(view)
    }

    /// The height of macOS's standard push button.
    private static let pushHeight: CGFloat = {
        let button = NSButton(title: "Open", target: nil, action: nil)
        button.bezelStyle = .push
        return button.intrinsicContentSize.height
    }()

    /// button is a push button at the panel's size: the standard push
    /// button scaled. A push button of a larger control size sets a larger
    /// title above its middle, so the button has a flexible height instead,
    /// which looks the same at Default and takes the capsule shape of
    /// macOS's larger buttons at Large and Larger. inline gives a link-like
    /// button instead. id names the control across redraws, so focus stays
    /// on it when the panel is drawn again, also when its title is empty or
    /// has changed, as Copy's does.
    private func button(_ title: String, id: String, inline: Bool = false, handler: @escaping (PanelButton) -> Void) -> PanelButton {
        let button = PanelButton(title: title, bezel: inline ? .inline : .flexiblePush, handler: handler)
        button.identifier = NSUserInterfaceItemIdentifier(id)
        button.font = .systemFont(ofSize: body)
        button.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: body, weight: .regular)
        if !inline {
            button.borderShape = size == .standard ? .automatic : .capsule
            button.heightAnchor.constraint(equalToConstant: Self.pushHeight * scale).isActive = true
        }
        return button
    }

    /// toggle is a check box at the panel's size, named id like a button.
    private func toggle(_ title: String, id: String, on: Bool, handler: @escaping (PanelButton) -> Void) -> PanelButton {
        let toggle = PanelButton(title: title, handler: handler)
        toggle.identifier = NSUserInterfaceItemIdentifier(id)
        toggle.setButtonType(.switch)
        toggle.controlSize = size.controlSize
        toggle.font = .systemFont(ofSize: body)
        toggle.state = on ? .on : .off
        return toggle
    }

    /// label is wrapping text, at the body size unless size is given.
    private func label(_ text: String, secondary: Bool = false, size: CGFloat? = nil, weight: NSFont.Weight = .regular, selectable: Bool = false) -> NSTextField {
        let field = NSTextField(wrappingLabelWithString: text)
        field.font = .systemFont(ofSize: size ?? body, weight: weight)
        field.textColor = secondary ? .secondaryLabelColor : .labelColor
        field.isSelectable = selectable
        field.preferredMaxLayoutWidth = inner - 20 * scale
        return field
    }

    /// help is secondary text at the small size.
    private func help(_ text: String) -> NSTextField {
        label(text, secondary: true, size: small)
    }

    /// line is a single-line label that shortens a long text in the middle.
    private func line(_ text: String, secondary: Bool = false, size: CGFloat? = nil, weight: NSFont.Weight = .regular) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .systemFont(ofSize: size ?? body, weight: weight)
        field.textColor = secondary ? .secondaryLabelColor : .labelColor
        field.lineBreakMode = .byTruncatingMiddle
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        return field
    }

    private func caption(_ text: String) -> NSTextField {
        let field = label(text, secondary: true, size: small, weight: .semibold)
        field.setAccessibilityRole(.staticText)
        return field
    }

    /// code shows a command or an address in a monospaced, selectable field,
    /// a point smaller than the body text, which its wider letters match.
    private func code(_ text: String) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .monospacedSystemFont(ofSize: body - scale, weight: .regular)
        field.isSelectable = true
        field.lineBreakMode = .byTruncatingMiddle
        // A long command shortens within the notice's padding.
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        field.drawsBackground = true
        field.backgroundColor = .quaternaryLabelColor.withAlphaComponent(0.12)
        field.toolTip = text
        return field
    }

    private func notice(_ lines: [NSView]) -> NSView {
        let column = NSStackView(views: lines)
        column.orientation = .vertical
        column.alignment = .leading
        column.spacing = 6 * scale
        column.edgeInsets = NSEdgeInsets(top: 8 * scale, left: 10 * scale, bottom: 8 * scale, right: 10 * scale)
        column.wantsLayer = true
        column.layer?.cornerRadius = 6 * scale
        column.layer?.backgroundColor = NSColor.quaternaryLabelColor.withAlphaComponent(0.1).cgColor
        column.widthAnchor.constraint(equalToConstant: inner).isActive = true
        return column
    }

    private func separator() -> NSView {
        let line = NSBox()
        line.boxType = .separator
        line.widthAnchor.constraint(equalToConstant: inner).isActive = true
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

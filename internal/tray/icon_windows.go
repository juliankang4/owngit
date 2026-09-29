//go:build windows

package tray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The icon is one hidden popup window that owns the notification area icon
// and is also the panel. A left click (or Enter on the icon) opens the
// dashboard; a right click (or the context menu key) opens the panel, which
// closes when it loses focus or on Esc. A poller reads the status and the
// hidden choice every few seconds and hands each reading to the window.

// Messages of the icon's window.
const (
	wmTray    = wmApp + 1 // the notification area icon's events
	wmReading = wmApp + 2 // the poller has a new reading
)

// Control IDs. Open is IDOK, so Enter outside a button opens the
// dashboard; Esc sends IDCANCEL, which closes the panel.
const (
	idOpen        = idOK
	idCopyCommand = 10
	idCopyClone   = 12
	idHide        = 13
	idQuit        = 14
	idEdit        = 100
)

// How often the poller reads: often while the panel is open or the icon is
// hidden (a file check), less often while the server runs, and least often
// while it does not answer, since the checkup then asks Task Scheduler.
const (
	pollOpen      = 5 * time.Second
	pollHidden    = 5 * time.Second
	pollAnswering = 10 * time.Second
	pollSilent    = 20 * time.Second
)

// reading is one result of the poller.
type reading struct {
	// show is false while the icon is hidden, or while its choice cannot
	// be read (as before the server's first start).
	show   bool
	report Report
}

// palette holds the panel's colors (COLORREF).
type palette struct {
	bg, fg, fg2, line, field, button, buttonLine, buttonPressed uint32
	accent, onAccent, ok, warn, bad, warnBg, badBg, neutralBg   uint32
	icon                                                        uint32
}

type app struct {
	stateDir string
	client   *Client
	lang     webui.Lang
	instance uintptr
	hwnd     uintptr
	// taskbarCreated is the message Explorer sends when the taskbar
	// starts again; the icon is then added again.
	taskbarCreated uint32

	mu        sync.Mutex
	latest    reading
	refresh   chan struct{}
	panelOpen atomic.Bool

	// The rest belongs to the window's thread.
	current   reading
	view      View
	iconAdded bool
	icon      uintptr
	iconKey   string
	dpi       uint32
	colors    palette
	brushes   map[uint32]uintptr
	fonts     struct{ normal, semibold, title, small, mono uintptr }
	texts     map[uintptr]string
	controls  panelControls
	// Rectangles the window paints itself, from the last layout.
	tile, symbol, notice rect
	fields               []rect
	separator            int32
	closedAt, openedAt   time.Time
}

type panelControls struct {
	title, subtitle, state                  uintptr
	notice, commandIntro, command, copyCmd  uintptr
	cloneLabel, clone, copyClone            uintptr
	recent, noPushes                        uintptr
	pushes                                  [3][3]uintptr
	open, section, hide, quit, keepsRunning uintptr
	buttons                                 []uintptr
}

// current is the icon of this process; the window procedure reaches it.
var current *app

var windowProc = windows.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
	if current == nil {
		return call(procDefWindowProc, hwnd, msg, wParam, lParam)
	}
	return current.handle(hwnd, uint32(msg), wParam, lParam)
})

// Run shows the icon of the server of options.StateDir in the notification
// area until the icon is quit or options.Stop closes. It returns
// ErrAlreadyRunning when the icon of that state directory already runs in
// this sign-in session.
func Run(options Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(options.StateDir))))
	mutex, err := windows.CreateMutex(nil, false, utf16(`Local\OwnGitIcon-`+hex.EncodeToString(sum[:16])))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return ErrAlreadyRunning
	}
	if err != nil {
		return fmt.Errorf("start the OwnGit icon: %w", err)
	}
	defer windows.CloseHandle(mutex)
	call(procSetProcessDpiAwarenessContext, perMonitorAwareV2)

	a := &app{
		stateDir: options.StateDir, client: NewClient(options.StateDir, options.Diagnose),
		lang: userLanguage(), refresh: make(chan struct{}, 1),
		brushes: map[uint32]uintptr{}, texts: map[uintptr]string{},
	}
	current = a
	if err := a.createWindow(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.poll(ctx)
	go func() {
		select {
		case <-options.Stop:
			call(procPostMessage, a.hwnd, wmClose, 0, 0)
		case <-ctx.Done():
		}
	}()
	var msg message
	for int32(call(procGetMessage, ptr(&msg), 0, 0, 0)) > 0 {
		// Enter on a focused button presses it; the dialog manager would
		// press Open instead, since the buttons are drawn by the icon.
		if msg.message == wmKeyDown && msg.wParam == vkReturn && a.isButton(call(procGetFocus)) {
			call(procSendMessage, call(procGetFocus), bmClick, 0, 0)
			continue
		}
		if call(procIsDialogMessage, a.hwnd, ptr(&msg)) != 0 {
			continue
		}
		call(procTranslateMessage, ptr(&msg))
		call(procDispatchMessage, ptr(&msg))
	}
	return nil
}

// userLanguage is Korean when Windows shows its own text in Korean, and
// English otherwise.
func userLanguage() webui.Lang {
	if call(procGetUserDefaultUILanguage)&0x3FF == langKorean {
		return webui.LangKO
	}
	return webui.LangEN
}

func (a *app) createWindow() error {
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		return err
	}
	a.instance = uintptr(instance)
	class := wndClassEx{
		style: csDropShadow, windowProc: windowProc, instance: a.instance,
		cursor: call(procLoadCursor, 0, idcArrow), className: utf16("OwnGitIcon"),
	}
	class.size = uint32(unsafe.Sizeof(class))
	if call(procRegisterClassEx, ptr(&class)) == 0 {
		return errors.New("register the window of the OwnGit icon")
	}
	a.taskbarCreated = uint32(call(procRegisterWindowMessage, ptr(utf16("TaskbarCreated"))))
	a.hwnd = call(procCreateWindowEx, wsExToolWindow|wsExTopmost|wsExControlParent, ptr(class.className), ptr(utf16("OwnGit")),
		wsPopup|wsClipChildren, 0, 0, 0, 0, 0, 0, a.instance, 0)
	if a.hwnd == 0 {
		return errors.New("create the window of the OwnGit icon")
	}
	corner := int32(dwmwcpRound)
	call(procDwmSetWindowAttribute, a.hwnd, dwmwaCornerPref, ptr(&corner), unsafe.Sizeof(corner))
	a.readTheme()
	a.setDPI(uint32(call(procGetDpiForSystem)))
	a.createControls()
	return nil
}

func (a *app) createControls() {
	c := &a.controls
	static := func(style uintptr) uintptr { return a.child("STATIC", ssNoPrefix|style, 0) }
	button := func(id int) uintptr {
		b := a.child("BUTTON", bsOwnerDraw|wsTabStop, id)
		c.buttons = append(c.buttons, b)
		return b
	}
	edit := func() uintptr { return a.child("EDIT", esReadOnly|esAutoHScroll|wsTabStop, idEdit) }
	// Creation order is the order of Tab.
	c.title = static(ssLeft | ssEndEllipsis)
	c.subtitle = static(ssLeft | ssEndEllipsis)
	c.state = static(ssRight)
	c.notice = static(ssLeft)
	c.commandIntro = static(ssLeft)
	c.command = edit()
	c.copyCmd = button(idCopyCommand)
	c.cloneLabel = static(ssLeft)
	c.clone = edit()
	c.copyClone = button(idCopyClone)
	c.recent = static(ssLeft)
	c.noPushes = static(ssLeft)
	for row := range c.pushes {
		c.pushes[row] = [3]uintptr{static(ssLeft | ssEndEllipsis), static(ssLeft | ssEndEllipsis), static(ssRight)}
	}
	c.open = button(idOpen)
	c.section = static(ssLeft)
	c.hide = button(idHide)
	c.quit = button(idQuit)
	c.keepsRunning = static(ssLeft)

	text := func(code webui.MessageCode) string { return webui.Text(a.lang, code) }
	a.setText(c.title, "OwnGit")
	a.setText(c.cloneLabel, text(webui.MsgTrayCloneAddress))
	a.setText(c.copyCmd, text(webui.MsgTrayCopy))
	a.setText(c.copyClone, text(webui.MsgTrayCopy))
	a.setText(c.recent, text(webui.MsgTrayRecent))
	a.setText(c.open, text(webui.MsgTrayOpen))
	a.setText(c.section, text(webui.MsgTrayThisComputer))
	a.setText(c.hide, text(webui.MsgTrayHide))
	a.setText(c.quit, text(webui.MsgTrayQuit))
	a.setText(c.keepsRunning, text(webui.MsgTrayKeepsRunning))
	a.applyFonts()
}

func (a *app) child(class string, style uintptr, id int) uintptr {
	return call(procCreateWindowEx, 0, ptr(utf16(class)), ptr(utf16("")), wsChild|style, 0, 0, 0, 0, a.hwnd, uintptr(id), a.instance, 0)
}

func (a *app) setText(control uintptr, text string) {
	if a.texts[control] == text {
		return
	}
	a.texts[control] = text
	call(procSetWindowText, control, ptr(utf16(text)))
}

func (a *app) isButton(control uintptr) bool {
	for _, button := range a.controls.buttons {
		if control != 0 && control == button {
			return true
		}
	}
	return false
}

// poll reads the hidden choice and, while the icon shows, the status, and
// hands each reading to the window.
func (a *app) poll(ctx context.Context) {
	for {
		next := reading{show: a.mayShow()}
		wait := pollHidden
		if next.show {
			next.report = a.client.Read(ctx, string(a.lang))
			// The server says so too when the choice changed meanwhile.
			if next.report.Status != nil && !next.report.Status.Shown {
				next.show = false
			}
			switch next.report.Condition {
			case Running, Attention:
				wait = pollAnswering
			default:
				wait = pollSilent
			}
		}
		if a.panelOpen.Load() {
			wait = pollOpen
		}
		// The owner may have hidden the icon while the status was read.
		if next.show && !a.mayShow() {
			next.show = false
		}
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.latest = next
		a.mu.Unlock()
		call(procPostMessage, a.hwnd, wmReading, 0, 0)
		select {
		case <-ctx.Done():
			return
		case <-a.refresh:
		case <-time.After(wait):
		}
	}
}

// mayShow reports whether the owner lets the icon show: the hidden choice
// can be read and is not set.
func (a *app) mayShow() bool {
	held, err := state.OpenStateDirectory(a.stateDir)
	if err != nil {
		return false
	}
	defer held.Close()
	hidden, err := state.TrayHidden(held)
	return err == nil && !hidden
}

func (a *app) askAgain() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

func (a *app) handle(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmReading:
		a.mu.Lock()
		a.current = a.latest
		a.mu.Unlock()
		a.apply()
		return 0
	case wmTray:
		switch lParam & 0xFFFF {
		case ninSelect, ninKeySelect:
			a.primary()
		case wmContextMenu:
			a.togglePanel()
		}
		return 0
	case wmCommand:
		a.command(int(wParam & 0xFFFF))
		return 0
	case wmActivate:
		if wParam&0xFFFF == waInactive {
			a.closePanel()
			return 0
		}
	case wmTimer:
		call(procKillTimer, hwnd, wParam)
		switch wParam {
		case idCopyCommand:
			a.setText(a.controls.copyCmd, webui.Text(a.lang, webui.MsgTrayCopy))
		case idCopyClone:
			a.setText(a.controls.copyClone, webui.Text(a.lang, webui.MsgTrayCopy))
		}
		return 0
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		a.paint()
		return 0
	case wmDrawItem:
		a.drawButton(fromAddress[drawItemStruct](lParam))
		return 1
	case wmCtlColorStatic, wmCtlColorEdit, wmCtlColorBtn:
		return a.controlColors(wParam, lParam)
	case wmSettingChange, wmSysColorChange, wmThemeChanged:
		a.readTheme()
		a.applyIcon()
		a.redraw()
		return call(procDefWindowProc, hwnd, uintptr(msg), wParam, lParam)
	case wmDpiChanged:
		a.setDPI(uint32(wParam & 0xFFFF))
		suggested := fromAddress[rect](lParam)
		width, height := a.layout()
		call(procSetWindowPos, hwnd, 0, uintptr(suggested.left), uintptr(suggested.top), uintptr(width), uintptr(height), swpNoActivate)
		a.redraw()
		return 0
	case wmClose:
		call(procDestroyWindow, hwnd)
		return 0
	case wmDestroy:
		a.removeIcon()
		call(procPostQuitMessage, 0)
		return 0
	}
	if a.taskbarCreated != 0 && msg == a.taskbarCreated {
		a.iconAdded = false
		a.apply()
		return 0
	}
	return call(procDefWindowProc, hwnd, uintptr(msg), wParam, lParam)
}

// apply shows the current reading: the icon, and the panel when it is open.
func (a *app) apply() {
	if !a.current.show {
		a.closePanel()
		a.removeIcon()
		return
	}
	view := NewView(a.current.report, a.lang, time.Now())
	changed := !reflect.DeepEqual(view, a.view)
	a.view = view
	a.applyIcon()
	if changed && a.panelOpen.Load() {
		a.placePanel()
	}
}

// primary is a left click on the icon, or Enter on it: it opens the
// dashboard, or the panel when there is no dashboard to open.
func (a *app) primary() {
	if time.Since(a.openedAt) < 500*time.Millisecond {
		return
	}
	a.openedAt = time.Now()
	if a.view.DashboardURL != "" && openURL(a.view.DashboardURL) {
		a.closePanel()
		return
	}
	a.openPanel()
}

func (a *app) togglePanel() {
	switch {
	case a.panelOpen.Load():
		a.closePanel()
	// The click on the icon that took the focus from the panel closed it
	// already; it does not open it again.
	case time.Since(a.closedAt) > 300*time.Millisecond:
		a.openPanel()
	}
}

func (a *app) openPanel() {
	if !a.iconAdded {
		return
	}
	a.panelOpen.Store(true)
	a.placePanel()
	call(procSetForegroundWindow, a.hwnd)
	first := a.controls.open
	if a.view.DashboardURL == "" {
		first = a.controls.hide
	}
	call(procSetFocus, first)
	a.askAgain()
}

// placePanel lays the panel out for the current view and puts it beside
// the icon, inside the work area of the icon's display.
func (a *app) placePanel() {
	anchor, ok := a.iconRect()
	if !ok {
		var cursor pointXY
		call(procGetCursorPos, ptr(&cursor))
		anchor = rect{cursor.x, cursor.y, cursor.x, cursor.y}
	}
	center := pointXY{anchor.left + anchor.width()/2, anchor.top + anchor.height()/2}
	monitor := call(procMonitorFromPoint, uintptr(*(*uint64)(unsafe.Pointer(&center))), monitorNearest)
	info := monitorInfo{}
	info.size = uint32(unsafe.Sizeof(info))
	call(procGetMonitorInfo, monitor, ptr(&info))
	var dpiX, dpiY uint32
	if call(procGetDpiForMonitor, monitor, 0, ptr(&dpiX), ptr(&dpiY)) == 0 && dpiX != 0 {
		a.setDPI(dpiX)
	}
	width, height := a.layout()
	margin := a.scale(8)
	x := clamp(center.x-width/2, info.work.left+margin, info.work.right-width-margin)
	y := clamp(anchor.top-height-margin, info.work.top+margin, info.work.bottom-height-margin)
	call(procSetWindowText, a.hwnd, ptr(utf16("OwnGit, "+a.view.State)))
	call(procSetWindowPos, a.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(width), uintptr(height), swpShowWindow)
	a.redraw()
}

func clamp(value, low, high int32) int32 {
	return max(low, min(value, high))
}

func (a *app) closePanel() {
	if !a.panelOpen.Load() {
		return
	}
	a.panelOpen.Store(false)
	a.closedAt = time.Now()
	call(procShowWindow, a.hwnd, swHide)
}

func (a *app) command(id int) {
	switch id {
	case idOpen:
		if openURL(a.view.DashboardURL) {
			a.closePanel()
		}
	case idCancel:
		a.closePanel()
		data := a.iconData()
		call(procShellNotifyIcon, nimSetFocus, ptr(&data))
	case idCopyCommand:
		a.copy(a.controls.copyCmd, idCopyCommand, a.view.Command)
	case idCopyClone:
		a.copy(a.controls.copyClone, idCopyClone, a.view.CloneAddress)
	case idHide:
		a.hideIcon()
	case idQuit:
		call(procDestroyWindow, a.hwnd)
	}
}

// hideIcon is Hide from the notification area: the same choice as "owngit
// tray off" and the dashboard switch.
func (a *app) hideIcon() {
	held, err := state.OpenStateDirectory(a.stateDir)
	if err == nil {
		err = state.SetTrayHidden(held, true)
		held.Close()
	}
	if err != nil {
		text := fmt.Sprintf(webui.Text(a.lang, webui.MsgTrayHideFailed), err)
		windows.MessageBox(windows.HWND(a.hwnd), utf16(text), utf16("OwnGit"), windows.MB_OK|windows.MB_ICONWARNING)
		return
	}
	a.current.show = false
	a.apply()
	a.askAgain()
}

// copy puts text on the clipboard and says on the button whether it did.
func (a *app) copy(button uintptr, id int, text string) {
	result := webui.MsgTrayCopied
	if !a.copyText(text) {
		result = webui.MsgTrayCopyFailed
	}
	a.setText(button, webui.Text(a.lang, result))
	call(procSetTimer, a.hwnd, uintptr(id), 2000, 0)
}

func (a *app) copyText(text string) bool {
	units, err := windows.UTF16FromString(text)
	if err != nil || text == "" || call(procOpenClipboard, a.hwnd) == 0 {
		return false
	}
	defer call(procCloseClipboard)
	call(procEmptyClipboard)
	memory := call(procGlobalAlloc, gmemMoveable, uintptr(2*len(units)))
	if memory == 0 {
		return false
	}
	target := call(procGlobalLock, memory)
	if target == 0 {
		call(procGlobalFree, memory)
		return false
	}
	copy(unsafe.Slice(fromAddress[uint16](target), len(units)), units)
	call(procGlobalUnlock, memory)
	if call(procSetClipboardData, cfUnicodeText, memory) == 0 {
		call(procGlobalFree, memory)
		return false
	}
	return true
}

// openURL opens an http or https address in the browser.
func openURL(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	return windows.ShellExecute(0, utf16("open"), utf16(address), nil, nil, swShowNormal) == nil
}

// The notification area icon.

func (a *app) iconData() notifyIconData {
	data := notifyIconData{hwnd: a.hwnd, id: 1}
	data.size = uint32(unsafe.Sizeof(data))
	return data
}

func (a *app) applyIcon() {
	if !a.current.show {
		return
	}
	dpi := uint32(call(procGetDpiForSystem))
	size := int(call(procGetSystemMetricsForDpi, smCxSmIcon, uintptr(dpi)))
	if size <= 0 {
		size = 16
	}
	key := fmt.Sprint(a.view.Condition, size, a.colors.icon)
	if key != a.iconKey || a.icon == 0 {
		icon := newIcon(Glyph(a.view.Condition, size), size, a.colors.icon)
		if icon == 0 {
			return
		}
		if a.icon != 0 {
			defer call(procDestroyIcon, a.icon)
		}
		a.icon, a.iconKey = icon, key
	}
	data := a.iconData()
	data.flags = nifMessage | nifIcon | nifTip | nifShowTip
	data.callbackMessage = wmTray
	data.icon = a.icon
	tip, _ := windows.UTF16FromString(a.view.Tooltip)
	copy(data.tip[:len(data.tip)-1], tip)
	if a.iconAdded {
		if call(procShellNotifyIcon, nimModify, ptr(&data)) != 0 {
			return
		}
		// Explorer lost the icon; add it again.
		a.iconAdded = false
	}
	if call(procShellNotifyIcon, nimAdd, ptr(&data)) == 0 {
		// Explorer may not be ready yet at sign-in; the next reading tries
		// again.
		return
	}
	data.version = notifyIconVersion4
	call(procShellNotifyIcon, nimSetVersion, ptr(&data))
	a.iconAdded = true
}

func (a *app) removeIcon() {
	if !a.iconAdded {
		return
	}
	data := a.iconData()
	call(procShellNotifyIcon, nimDelete, ptr(&data))
	a.iconAdded = false
}

func (a *app) iconRect() (rect, bool) {
	identifier := notifyIconIdentifier{hwnd: a.hwnd, id: 1}
	identifier.size = uint32(unsafe.Sizeof(identifier))
	var r rect
	return r, a.iconAdded && call(procShellNotifyIconGetRect, ptr(&identifier), ptr(&r)) == 0
}

// Colors and fonts.

// readTheme follows the Windows color mode of apps (the panel) and of the
// taskbar (the icon), and high contrast, whose colors replace both.
func (a *app) readTheme() {
	appsLight, taskbarLight := true, false
	if key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE); err == nil {
		if value, _, err := key.GetIntegerValue("AppsUseLightTheme"); err == nil {
			appsLight = value != 0
		}
		if value, _, err := key.GetIntegerValue("SystemUsesLightTheme"); err == nil {
			taskbarLight = value != 0
		}
		key.Close()
	}
	contrast := highContrast{}
	contrast.size = uint32(unsafe.Sizeof(contrast))
	call(procSystemParametersInfo, spiGetHighContrast, uintptr(contrast.size), ptr(&contrast), 0)
	switch {
	case contrast.flags&hcfHighContrastOn != 0:
		system := func(index int) uint32 { return uint32(call(procGetSysColor, uintptr(index))) }
		fg, bg := system(colorWindowText), system(colorWindow)
		a.colors = palette{
			bg: bg, fg: fg, fg2: fg, line: fg, field: bg, button: system(colorBtnFace), buttonLine: system(colorBtnText), buttonPressed: system(colorHighlight),
			accent: system(colorHighlight), onAccent: system(colorHighlightText), ok: fg, warn: fg, bad: fg,
			warnBg: bg, badBg: bg, neutralBg: bg, icon: fg,
		}
		a.colors.buttonPressed = a.colors.button
	case appsLight:
		a.colors = palette{
			bg: rgb(0xF3, 0xF3, 0xF3), fg: rgb(0x1A, 0x1A, 0x1A), fg2: rgb(0x5F, 0x5F, 0x5F), line: rgb(0xDD, 0xDD, 0xDD),
			field: rgb(0xFF, 0xFF, 0xFF), button: rgb(0xFB, 0xFB, 0xFB), buttonLine: rgb(0xD0, 0xD0, 0xD0), buttonPressed: rgb(0xE8, 0xE8, 0xE8),
			accent: rgb(0x00, 0x67, 0xC0), onAccent: rgb(0xFF, 0xFF, 0xFF), ok: rgb(0x0F, 0x7B, 0x3B), warn: rgb(0x8A, 0x5A, 0x00), bad: rgb(0xC4, 0x2B, 0x1C),
			warnBg: rgb(0xFF, 0xF4, 0xCE), badBg: rgb(0xFD, 0xE7, 0xE9), neutralBg: rgb(0xE9, 0xE9, 0xE9),
		}
	default:
		a.colors = palette{
			bg: rgb(0x2B, 0x2B, 0x2B), fg: rgb(0xFF, 0xFF, 0xFF), fg2: rgb(0xB0, 0xB0, 0xB0), line: rgb(0x40, 0x40, 0x40),
			field: rgb(0x1F, 0x1F, 0x1F), button: rgb(0x37, 0x37, 0x37), buttonLine: rgb(0x4A, 0x4A, 0x4A), buttonPressed: rgb(0x42, 0x42, 0x42),
			accent: rgb(0x4C, 0xC2, 0xFF), onAccent: rgb(0x00, 0x00, 0x00), ok: rgb(0x6C, 0xCB, 0x5F), warn: rgb(0xFC, 0xE1, 0x00), bad: rgb(0xFF, 0x99, 0xA4),
			warnBg: rgb(0x43, 0x35, 0x19), badBg: rgb(0x44, 0x27, 0x26), neutralBg: rgb(0x35, 0x35, 0x35),
		}
	}
	if contrast.flags&hcfHighContrastOn == 0 {
		a.colors.icon = rgb(0xFF, 0xFF, 0xFF)
		if taskbarLight {
			a.colors.icon = rgb(0x00, 0x00, 0x00)
		}
	}
	for color, brush := range a.brushes {
		call(procDeleteObject, brush)
		delete(a.brushes, color)
	}
}

func (a *app) brush(color uint32) uintptr {
	if brush, ok := a.brushes[color]; ok {
		return brush
	}
	brush := call(procCreateSolidBrush, uintptr(color))
	a.brushes[color] = brush
	return brush
}

func (a *app) scale(value int32) int32 { return value * int32(a.dpi) / 96 }

func (a *app) setDPI(dpi uint32) {
	if dpi == 0 || dpi == a.dpi {
		return
	}
	a.dpi = dpi
	for _, font := range []uintptr{a.fonts.normal, a.fonts.semibold, a.fonts.title, a.fonts.small, a.fonts.mono} {
		if font != 0 {
			call(procDeleteObject, font)
		}
	}
	font := func(pixels int32, weight int, face string) uintptr {
		return call(procCreateFont, uintptr(-a.scale(pixels)), 0, 0, 0, uintptr(weight), 0, 0, 0, defaultCharset, 0, 0, cleartypeQuality, 0, ptr(utf16(face)))
	}
	a.fonts.normal = font(14, fwNormal, "Segoe UI")
	a.fonts.semibold = font(14, fwSemiBold, "Segoe UI")
	a.fonts.title = font(15, fwSemiBold, "Segoe UI")
	a.fonts.small = font(12, fwNormal, "Segoe UI")
	a.fonts.mono = font(13, fwNormal, "Consolas")
	if a.controls.title != 0 {
		a.applyFonts()
	}
}

func (a *app) applyFonts() {
	c := &a.controls
	set := func(font uintptr, controls ...uintptr) {
		for _, control := range controls {
			call(procSendMessage, control, wmSetFont, font, 0)
		}
	}
	set(a.fonts.normal, c.notice, c.commandIntro, c.noPushes)
	set(a.fonts.semibold, c.state, c.cloneLabel, c.recent, c.section)
	set(a.fonts.title, c.title)
	set(a.fonts.small, c.subtitle, c.keepsRunning)
	set(a.fonts.mono, c.command, c.clone)
	for _, row := range c.pushes {
		set(a.fonts.semibold, row[0])
		set(a.fonts.small, row[1], row[2])
	}
}

// measure returns the size of text in font: on one line, or wrapped at
// width when width is not 0.
func (a *app) measure(font uintptr, text string, width int32) (int32, int32) {
	hdc := call(procGetDC, a.hwnd)
	defer call(procReleaseDC, a.hwnd, hdc)
	old := call(procSelectObject, hdc, font)
	defer call(procSelectObject, hdc, old)
	area := rect{0, 0, width, 0}
	format := uintptr(dtCalcRect | dtNoPrefix)
	if width == 0 {
		format |= dtSingleLine
		text = strings.ReplaceAll(text, "\n", " ")
	} else {
		format |= dtWordBreak
	}
	if text == "" {
		text = " "
	}
	call(procDrawText, hdc, ptr(utf16(text)), ^uintptr(0), ptr(&area), format)
	return area.width(), area.height()
}

// The panel's layout.

// layout places the controls for the current view and returns the panel's
// size. Sizes are in pixels at 96 DPI and scaled.
func (a *app) layout() (int32, int32) {
	c, view, s := &a.controls, a.view, a.scale
	width, pad := s(360), s(16)
	inner := width - 2*pad
	a.fields = a.fields[:0]
	place := func(control uintptr, x, y, w, h int32) {
		call(procMoveWindow, control, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
		call(procShowWindow, control, 5) // SW_SHOW
	}
	hide := func(controls ...uintptr) {
		for _, control := range controls {
			call(procShowWindow, control, swHide)
		}
	}
	_, lineSemibold := a.measure(a.fonts.semibold, "Ag가", 0)
	_, lineTitle := a.measure(a.fonts.title, "Ag가", 0)
	_, lineSmall := a.measure(a.fonts.small, "Ag가", 0)
	_, lineMono := a.measure(a.fonts.mono, "Ag", 0)
	buttonHeight := s(32)
	buttonWidth := func(label string) int32 {
		w, _ := a.measure(a.fonts.normal, label, 0)
		return w + s(24)
	}
	// A field: a read-only edit on a rounded background, and its Copy
	// button.
	field := func(edit, button uintptr, x, y, w int32) {
		copyWidth := int32(0)
		for _, code := range []webui.MessageCode{webui.MsgTrayCopy, webui.MsgTrayCopied, webui.MsgTrayCopyFailed} {
			copyWidth = max(copyWidth, buttonWidth(webui.Text(a.lang, code)))
		}
		box := rect{x, y, x + w - copyWidth - s(8), y + buttonHeight}
		a.fields = append(a.fields, box)
		place(edit, box.left+s(8), y+(buttonHeight-lineMono)/2, box.width()-s(16), lineMono)
		place(button, box.right+s(8), y, copyWidth, buttonHeight)
	}

	// Heading: tile, name, version and address, and the condition.
	y := pad
	a.tile = rect{pad, y, pad + s(32), y + s(32)}
	a.setText(c.state, view.State)
	stateWidth, _ := a.measure(a.fonts.semibold, view.State, 0)
	stateX := width - pad - stateWidth
	place(c.state, stateX, y+s(2), stateWidth, lineSemibold)
	symbolSize := s(16)
	a.symbol = rect{stateX - symbolSize - s(6), y + s(2) + (lineSemibold-symbolSize)/2, stateX - s(6), y + s(2) + (lineSemibold+symbolSize)/2}
	textX := pad + s(44)
	place(c.title, textX, y, a.symbol.left-s(8)-textX, lineTitle)
	if view.Subtitle != "" {
		a.setText(c.subtitle, view.Subtitle)
		place(c.subtitle, textX, y+lineTitle+s(2), width-pad-textX, lineSmall)
	} else {
		hide(c.subtitle)
	}
	y += max(s(32), lineTitle+s(2)+lineSmall) + s(14)

	// Notice: what needs doing, and its one command.
	a.notice = rect{}
	hide(c.notice, c.commandIntro, c.command, c.copyCmd)
	if len(view.Notice) > 0 || view.Command != "" {
		top := y
		x, w := pad+s(12), inner-s(24)
		y += s(10)
		if len(view.Notice) > 0 {
			text := strings.Join(view.Notice, "\n")
			a.setText(c.notice, text)
			_, h := a.measure(a.fonts.normal, text, w)
			place(c.notice, x, y, w, h)
			y += h + s(8)
		}
		if view.Command != "" {
			a.setText(c.commandIntro, view.CommandIntro)
			_, h := a.measure(a.fonts.normal, view.CommandIntro, w)
			place(c.commandIntro, x, y, w, h)
			y += h + s(6)
			a.setText(c.command, view.Command)
			field(c.command, c.copyCmd, x, y, w)
			y += buttonHeight + s(8)
		}
		y += s(2)
		a.notice = rect{pad, top, width - pad, y}
		y += s(14)
	}

	// Clone address.
	hide(c.cloneLabel, c.clone, c.copyClone)
	if view.CloneAddress != "" {
		place(c.cloneLabel, pad, y, inner, lineSemibold)
		y += lineSemibold + s(6)
		a.setText(c.clone, view.CloneAddress)
		field(c.clone, c.copyClone, pad, y, inner)
		y += buttonHeight + s(14)
	}

	// Recent pushes.
	place(c.recent, pad, y, inner, lineSemibold)
	y += lineSemibold + s(6)
	for row, controls := range c.pushes {
		if row >= len(view.Pushes) {
			hide(controls[:]...)
			continue
		}
		push := view.Pushes[row]
		a.setText(controls[0], push.Repository)
		a.setText(controls[1], push.Branch)
		a.setText(controls[2], push.When)
		whenWidth, _ := a.measure(a.fonts.small, push.When, 0)
		repoWidth, _ := a.measure(a.fonts.semibold, push.Repository, 0)
		repoWidth = min(repoWidth, inner*45/100)
		offset := (lineSemibold - lineSmall) / 2
		place(controls[0], pad, y, repoWidth, lineSemibold)
		place(controls[1], pad+repoWidth+s(8), y+offset, max(0, inner-repoWidth-whenWidth-s(16)), lineSmall)
		place(controls[2], width-pad-whenWidth, y+offset, whenWidth, lineSmall)
		y += lineSemibold + s(6)
	}
	if len(view.Pushes) == 0 {
		a.setText(c.noPushes, view.NoPushes)
		_, h := a.measure(a.fonts.normal, view.NoPushes, inner)
		place(c.noPushes, pad, y, inner, h)
		y += h + s(6)
	} else {
		hide(c.noPushes)
	}
	y += s(8)

	// Open dashboard, then the settings of this computer.
	if view.DashboardURL != "" {
		place(c.open, pad, y, buttonWidth(a.texts[c.open])+s(8), buttonHeight)
		y += buttonHeight + s(14)
	} else {
		hide(c.open)
	}
	a.separator = y
	y += s(14)
	place(c.section, pad, y, inner, lineSemibold)
	y += lineSemibold + s(8)
	hideWidth, quitWidth := buttonWidth(a.texts[c.hide]), buttonWidth(a.texts[c.quit])
	place(c.hide, pad, y, hideWidth, buttonHeight)
	if hideWidth+s(8)+quitWidth <= inner {
		place(c.quit, pad+hideWidth+s(8), y, quitWidth, buttonHeight)
	} else {
		y += buttonHeight + s(8)
		place(c.quit, pad, y, quitWidth, buttonHeight)
	}
	y += buttonHeight + s(8)
	_, h := a.measure(a.fonts.small, a.texts[c.keepsRunning], inner)
	place(c.keepsRunning, pad, y, inner, h)
	y += h + pad
	return width, y
}

// redraw paints the panel and its controls again.
func (a *app) redraw() {
	const rdwInvalidate, rdwErase, rdwAllChildren = 0x1, 0x4, 0x80
	call(procRedrawWindow, a.hwnd, 0, 0, rdwInvalidate|rdwErase|rdwAllChildren)
}

func (a *app) paint() {
	var paint paintStruct
	hdc := call(procBeginPaint, a.hwnd, ptr(&paint))
	defer call(procEndPaint, a.hwnd, ptr(&paint))
	var client rect
	call(procGetClientRect, a.hwnd, ptr(&client))
	call(procFillRect, hdc, ptr(&client), a.brush(a.colors.bg))
	call(procFrameRect, hdc, ptr(&client), a.brush(a.colors.line))
	size := int(a.tile.width())
	drawPixels(hdc, Tile(size), size, a.tile.left, a.tile.top)
	symbolSize := int(a.symbol.width())
	drawPixels(hdc, tinted(Symbol(a.view.Condition, symbolSize), a.stateColor()), symbolSize, a.symbol.left, a.symbol.top)
	if a.notice.height() > 0 {
		a.roundRect(hdc, a.notice, a.noticeColor(), a.noticeColor(), a.scale(8))
	}
	for _, field := range a.fields {
		a.roundRect(hdc, field, a.colors.field, a.colors.line, a.scale(4))
	}
	line := rect{a.scale(16), a.separator, client.right - a.scale(16), a.separator + max(1, a.scale(1))}
	call(procFillRect, hdc, ptr(&line), a.brush(a.colors.line))
}

func (a *app) roundRect(hdc uintptr, r rect, fill, outline uint32, radius int32) {
	pen := call(procCreatePen, 0, uintptr(max(1, a.scale(1))), uintptr(outline))
	defer call(procDeleteObject, pen)
	oldPen := call(procSelectObject, hdc, pen)
	oldBrush := call(procSelectObject, hdc, a.brush(fill))
	call(procRoundRect, hdc, uintptr(r.left), uintptr(r.top), uintptr(r.right), uintptr(r.bottom), uintptr(2*radius), uintptr(2*radius))
	call(procSelectObject, hdc, oldPen)
	call(procSelectObject, hdc, oldBrush)
}

func (a *app) stateColor() uint32 {
	switch a.view.Condition {
	case Running:
		return a.colors.ok
	case Attention:
		return a.colors.warn
	case Stopped:
		return a.colors.bad
	}
	return a.colors.fg2
}

func (a *app) noticeColor() uint32 {
	switch a.view.Condition {
	case Attention:
		return a.colors.warnBg
	case Stopped:
		return a.colors.badBg
	}
	return a.colors.neutralBg
}

// controlColors answers WM_CTLCOLOR* for a control: its text color and the
// background it stands on.
func (a *app) controlColors(hdc, control uintptr) uintptr {
	c := &a.controls
	background, text := a.colors.bg, a.colors.fg
	switch control {
	case c.notice, c.commandIntro, c.copyCmd:
		background = a.noticeColor()
	case c.command, c.clone:
		background = a.colors.field
	case c.subtitle, c.noPushes, c.keepsRunning:
		text = a.colors.fg2
	case c.state:
		text = a.stateColor()
	}
	for _, row := range c.pushes {
		if control == row[1] || control == row[2] {
			text = a.colors.fg2
		}
	}
	call(procSetTextColor, hdc, uintptr(text))
	call(procSetBkColor, hdc, uintptr(background))
	return a.brush(background)
}

// drawButton draws a button: Open filled with the accent color, the
// others outlined, and a focus ring when the keyboard moved the focus
// there.
func (a *app) drawButton(item *drawItemStruct) {
	c := &a.controls
	background := a.colors.bg
	if item.item == c.copyCmd {
		background = a.noticeColor()
	}
	r := item.rect
	call(procFillRect, item.hdc, ptr(&r), a.brush(background))
	fill, outline, text := a.colors.button, a.colors.buttonLine, a.colors.fg
	switch {
	case item.item == c.open:
		fill, outline, text = a.colors.accent, a.colors.accent, a.colors.onAccent
	case item.itemState&odsSelected != 0:
		fill = a.colors.buttonPressed
	}
	if item.itemState&odsDisabled != 0 {
		text = a.colors.fg2
	}
	inset := rect{r.left + 1, r.top + 1, r.right - 1, r.bottom - 1}
	a.roundRect(item.hdc, inset, fill, outline, a.scale(4))
	old := call(procSelectObject, item.hdc, a.fonts.normal)
	call(procSetBkMode, item.hdc, transparent)
	call(procSetTextColor, item.hdc, uintptr(text))
	label := a.texts[item.item]
	call(procDrawText, item.hdc, ptr(utf16(label)), ^uintptr(0), ptr(&r), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	call(procSelectObject, item.hdc, old)
	if item.itemState&odsFocus != 0 && item.itemState&odsNoFocusRect == 0 {
		pen := call(procCreatePen, 0, uintptr(max(2, a.scale(2))), uintptr(a.colors.fg))
		defer call(procDeleteObject, pen)
		oldPen := call(procSelectObject, item.hdc, pen)
		oldBrush := call(procSelectObject, item.hdc, call(procGetStockObject, nullBrush))
		radius := uintptr(2 * a.scale(4))
		call(procRoundRect, item.hdc, uintptr(r.left+1), uintptr(r.top+1), uintptr(r.right), uintptr(r.bottom), radius, radius)
		call(procSelectObject, item.hdc, oldPen)
		call(procSelectObject, item.hdc, oldBrush)
	}
}

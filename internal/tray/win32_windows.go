//go:build windows

package tray

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows functions and structures the icon uses. OwnGit builds without
// cgo, so they are called through the system DLLs.

var (
	user32                            = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassEx               = user32.NewProc("RegisterClassExW")
	procCreateWindowEx                = user32.NewProc("CreateWindowExW")
	procDefWindowProc                 = user32.NewProc("DefWindowProcW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procRedrawWindow                  = user32.NewProc("RedrawWindow")
	procGetClientRect                 = user32.NewProc("GetClientRect")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetMessage                    = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessage               = user32.NewProc("DispatchMessageW")
	procIsDialogMessage               = user32.NewProc("IsDialogMessageW")
	procPostMessage                   = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procSendMessage                   = user32.NewProc("SendMessageW")
	procSetWindowText                 = user32.NewProc("SetWindowTextW")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procSetFocus                      = user32.NewProc("SetFocus")
	procGetFocus                      = user32.NewProc("GetFocus")
	procInvalidateRect                = user32.NewProc("InvalidateRect")
	procBeginPaint                    = user32.NewProc("BeginPaint")
	procEndPaint                      = user32.NewProc("EndPaint")
	procFillRect                      = user32.NewProc("FillRect")
	procDrawText                      = user32.NewProc("DrawTextW")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procCreateIconIndirect            = user32.NewProc("CreateIconIndirect")
	procDestroyIcon                   = user32.NewProc("DestroyIcon")
	procGetSystemMetricsForDpi        = user32.NewProc("GetSystemMetricsForDpi")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo                = user32.NewProc("GetMonitorInfoW")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procSetTimer                      = user32.NewProc("SetTimer")
	procKillTimer                     = user32.NewProc("KillTimer")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procLoadCursor                    = user32.NewProc("LoadCursorW")
	procRegisterWindowMessage         = user32.NewProc("RegisterWindowMessageW")
	procSystemParametersInfo          = user32.NewProc("SystemParametersInfoW")
	procGetSysColor                   = user32.NewProc("GetSysColor")
	procMoveWindow                    = user32.NewProc("MoveWindow")
	procFrameRect                     = user32.NewProc("FrameRect")

	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIcon          = shell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect   = shell32.NewProc("Shell_NotifyIconGetRect")
	shcore                       = windows.NewLazySystemDLL("shcore.dll")
	procGetDpiForMonitor         = shcore.NewProc("GetDpiForMonitor")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalAlloc              = kernel32.NewProc("GlobalAlloc")
	procGlobalLock               = kernel32.NewProc("GlobalLock")
	procGlobalUnlock             = kernel32.NewProc("GlobalUnlock")
	procGlobalFree               = kernel32.NewProc("GlobalFree")
	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")

	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procCreateFont         = gdi32.NewProc("CreateFontW")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procSetBkColor         = gdi32.NewProc("SetBkColor")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procCreatePen          = gdi32.NewProc("CreatePen")
	procRoundRect          = gdi32.NewProc("RoundRect")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procGetStockObject     = gdi32.NewProc("GetStockObject")
	procGdiAlphaBlend      = gdi32.NewProc("GdiAlphaBlend")
)

// Messages, styles and other constants from the Windows headers.
const (
	wmDestroy          = 0x0002
	wmActivate         = 0x0006
	wmPaint            = 0x000F
	wmClose            = 0x0010
	wmEraseBkgnd       = 0x0014
	wmSettingChange    = 0x001A
	wmDrawItem         = 0x002B
	wmSetFont          = 0x0030
	wmContextMenu      = 0x007B
	wmKeyDown          = 0x0100
	wmCommand          = 0x0111
	wmTimer            = 0x0113
	wmCtlColorEdit     = 0x0133
	wmCtlColorBtn      = 0x0135
	wmCtlColorStatic   = 0x0138
	wmSysColorChange   = 0x0015
	wmThemeChanged     = 0x031A
	wmDpiChanged       = 0x02E0
	wmApp              = 0x8000
	bmClick            = 0x00F5
	ninSelect          = 0x0400
	ninKeySelect       = 0x0401
	waInactive         = 0
	vkReturn           = 0x0D
	idOK               = 1
	idCancel           = 2
	swHide             = 0
	swShowNormal       = 1
	wsPopup            = 0x80000000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsClipChildren     = 0x02000000
	wsTabStop          = 0x00010000
	wsExToolWindow     = 0x00000080
	wsExTopmost        = 0x00000008
	wsExControlParent  = 0x00010000
	csDropShadow       = 0x00020000
	ssLeft             = 0x0000
	ssRight            = 0x0002
	ssNoPrefix         = 0x0080
	ssEndEllipsis      = 0x4000
	esAutoHScroll      = 0x0080
	esReadOnly         = 0x0800
	bsOwnerDraw        = 0x000B
	odsSelected        = 0x0001
	odsDisabled        = 0x0004
	odsFocus           = 0x0010
	odsNoFocusRect     = 0x0200
	dtLeft             = 0x0000
	dtCenter           = 0x0001
	dtVCenter          = 0x0004
	dtWordBreak        = 0x0010
	dtSingleLine       = 0x0020
	dtCalcRect         = 0x0400
	dtNoPrefix         = 0x0800
	transparent        = 1
	nullBrush          = 5
	nimAdd             = 0
	nimModify          = 1
	nimDelete          = 2
	nimSetFocus        = 3
	nimSetVersion      = 4
	nifMessage         = 0x01
	nifIcon            = 0x02
	nifTip             = 0x04
	nifShowTip         = 0x80
	notifyIconVersion4 = 4
	hwndTopmost        = ^uintptr(0)
	swpNoActivate      = 0x0010
	swpShowWindow      = 0x0040
	monitorNearest     = 2
	smCxSmIcon         = 49
	cfUnicodeText      = 13
	gmemMoveable       = 0x0002
	spiGetHighContrast = 0x0042
	hcfHighContrastOn  = 0x0001
	colorWindow        = 5
	colorWindowText    = 8
	colorHighlight     = 13
	colorHighlightText = 14
	colorGrayText      = 17
	colorBtnFace       = 15
	colorBtnText       = 18
	fwNormal           = 400
	fwSemiBold         = 600
	defaultCharset     = 1
	cleartypeQuality   = 5
	dwmwaCornerPref    = 33
	dwmwcpRound        = 2
	idcArrow           = 32512
	perMonitorAwareV2  = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, -4
	langKorean         = 0x12
)

type rect struct{ left, top, right, bottom int32 }

func (r rect) width() int32  { return r.right - r.left }
func (r rect) height() int32 { return r.bottom - r.top }

type pointXY struct{ x, y int32 }

type wndClassEx struct {
	size, style                   uint32
	windowProc                    uintptr
	classExtra, windowExtra       int32
	instance, icon, cursor, brush uintptr
	menuName, className           *uint16
	smallIcon                     uintptr
}

type message struct {
	hwnd           uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	point          pointXY
	private        uint32
}

type paintStruct struct {
	hdc       uintptr
	erase     int32
	paint     rect
	restore   int32
	incUpdate int32
	reserved  [32]byte
}

type drawItemStruct struct {
	controlType, controlID, itemID, itemAction, itemState uint32
	item, hdc                                             uintptr
	rect                                                  rect
	itemData                                              uintptr
}

type notifyIconData struct {
	size             uint32
	hwnd             uintptr
	id, flags        uint32
	callbackMessage  uint32
	icon             uintptr
	tip              [128]uint16
	state, stateMask uint32
	info             [256]uint16
	version          uint32
	infoTitle        [64]uint16
	infoFlags        uint32
	guid             windows.GUID
	balloonIcon      uintptr
}

type notifyIconIdentifier struct {
	size uint32
	hwnd uintptr
	id   uint32
	guid windows.GUID
}

type monitorInfo struct {
	size          uint32
	monitor, work rect
	flags         uint32
}

type iconInfo struct {
	isIcon      int32
	hotspotX    uint32
	hotspotY    uint32
	mask, color uintptr
}

type bitmapInfoHeader struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, imageSize uint32
	xPerMeter, yPerMeter   int32
	colorsUsed, important  uint32
}

type highContrast struct {
	size, flags   uint32
	defaultScheme *uint16
}

// call calls a Win32 function. Pass a Go pointer as
// uintptr(unsafe.Pointer(&x)) written in the call itself: with
// go:uintptrescapes the compiler then keeps x on the heap and alive during
// the call, which matters because many calls (GetMessage, DispatchMessage,
// BeginPaint, Shell_NotifyIcon and others) run the window procedure in Go
// meanwhile, and a Go stack can move then. A helper that converts the
// pointer loses that guarantee.
//
//go:uintptrescapes
func call(proc *windows.LazyProc, args ...uintptr) uintptr {
	result, _, _ := proc.Call(args...)
	return result
}

func utf16(text string) *uint16 {
	pointer, err := windows.UTF16PtrFromString(text)
	if err != nil {
		// Text with a NUL character cannot be shown; show it up to there.
		pointer, _ = windows.UTF16PtrFromString(text[:indexNUL(text)])
	}
	return pointer
}

func indexNUL(text string) int {
	for index := range len(text) {
		if text[index] == 0 {
			return index
		}
	}
	return len(text)
}

// fromAddress is the structure at an address that Windows passed, as in the
// lParam of WM_DRAWITEM, or that it returned.
func fromAddress[T any](address uintptr) *T { return *(**T)(unsafe.Pointer(&address)) }

// rgb is a COLORREF.
func rgb(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

// dibSection makes a 32-bit top-down bitmap of size by size pixels from
// pixels (blue, green, red, alpha).
func dibSection(pixels []uint8, size int) uintptr {
	header := bitmapInfoHeader{width: int32(size), height: -int32(size), planes: 1, bitCount: 32}
	header.size = uint32(unsafe.Sizeof(header))
	var bits unsafe.Pointer
	bitmap := call(procCreateDIBSection, 0, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return 0
	}
	copy(unsafe.Slice((*uint8)(bits), len(pixels)), pixels)
	return bitmap
}

// tinted turns coverage into pixels of one color, premultiplied.
func tinted(coverage []uint8, color uint32) []uint8 {
	pixels := make([]uint8, 4*len(coverage))
	for index, alpha := range coverage {
		scale := func(channel uint32) uint8 { return uint8((channel & 0xFF) * uint32(alpha) / 255) }
		pixels[4*index] = scale(color >> 16)
		pixels[4*index+1] = scale(color >> 8)
		pixels[4*index+2] = scale(color)
		pixels[4*index+3] = alpha
	}
	return pixels
}

// drawPixels draws premultiplied pixels of size by size at x, y on hdc.
func drawPixels(hdc uintptr, pixels []uint8, size int, x, y int32) {
	bitmap := dibSection(pixels, size)
	if bitmap == 0 {
		return
	}
	defer call(procDeleteObject, bitmap)
	memory := call(procCreateCompatibleDC, hdc)
	defer call(procDeleteDC, memory)
	old := call(procSelectObject, memory, bitmap)
	defer call(procSelectObject, memory, old)
	// BLENDFUNCTION: AC_SRC_OVER, constant alpha 255, AC_SRC_ALPHA.
	const blend = 255<<16 | 1<<24
	call(procGdiAlphaBlend, hdc, uintptr(x), uintptr(y), uintptr(size), uintptr(size), memory, 0, 0, uintptr(size), uintptr(size), blend)
}

// newIcon makes an icon of size by size pixels from coverage in color.
func newIcon(coverage []uint8, size int, color uint32) uintptr {
	pixels := make([]uint8, 4*len(coverage))
	for index, alpha := range coverage {
		pixels[4*index], pixels[4*index+1], pixels[4*index+2] = uint8(color>>16), uint8(color>>8), uint8(color)
		pixels[4*index+3] = alpha
	}
	colorBitmap := dibSection(pixels, size)
	if colorBitmap == 0 {
		return 0
	}
	defer call(procDeleteObject, colorBitmap)
	// An all-zero mask: the alpha of the color bitmap decides.
	maskBits := make([]uint8, (size+15)/16*2*size)
	mask := call(procCreateBitmap, uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	defer call(procDeleteObject, mask)
	info := iconInfo{isIcon: 1, mask: mask, color: colorBitmap}
	return call(procCreateIconIndirect, uintptr(unsafe.Pointer(&info)))
}

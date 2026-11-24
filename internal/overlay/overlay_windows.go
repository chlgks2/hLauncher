// Package overlay draws the room roster on top of StarCraft, so you can see who
// you are playing with — and who is blacklisted — without leaving the game.
//
// It is a plain layered Win32 window owned by this process: always on top,
// click-through, and painted with GDI. Nothing is injected into the game and
// nothing about the game is altered; the overlay only *covers* pixels, the same
// way any other desktop window would. That keeps it inside the read-only line
// the rest of this build holds to.
//
// One limitation worth knowing: a window cannot be drawn over a game running in
// exclusive fullscreen. StarCraft's windowed and borderless modes are fine.
package overlay

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Row is one player line in the overlay.
type Row struct {
	Name  string
	Race  string
	Ping  int    // -1 when unknown (the in-game player array carries no latency)
	Black string // blacklist reason; empty when not blacklisted
	White bool
	Me    bool
}

// ---------------------------------------------------------------------------
// Win32
// ---------------------------------------------------------------------------

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetLayeredWAttrs = user32.NewProc("SetLayeredWindowAttributes")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procDrawTextW        = user32.NewProc("DrawTextW")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procSetTimer         = user32.NewProc("SetTimer")
	procEnumWindows      = user32.NewProc("EnumWindows")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procGetWindowTextLen = user32.NewProc("GetWindowTextLengthW")
	procGetWndThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procGetForegroundWnd = user32.NewProc("GetForegroundWindow")
	procFrameRect        = user32.NewProc("FrameRect")

	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procCreateCompatDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	procBitBlt           = gdi32.NewProc("BitBlt")
	procDeleteDC         = gdi32.NewProc("DeleteDC")
)

const (
	wsPopup = 0x80000000

	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020 // click-through: input falls through to the game
	wsExTopMost     = 0x00000008
	wsExToolWindow  = 0x00000080 // keeps it out of the taskbar and alt-tab
	wsExNoActivate  = 0x08000000

	lwaColorKey = 0x00000001
	lwaAlpha    = 0x00000002

	swShowNoActivate = 4
	swHide           = 0

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoActivate = 0x0010
	swpShowWindow = 0x0040

	wmDestroy    = 0x0002
	wmPaint      = 0x000F
	wmTimer      = 0x0113
	wmEraseBkgnd = 0x0014
	wmClose      = 0x0010
	wmUser       = 0x0400
	wmAppRepaint = wmUser + 1
	wmAppQuit    = wmUser + 2

	transparentBk = 1

	dtLeft        = 0x0000
	dtRight       = 0x0002
	dtSingleLine  = 0x0020
	dtVCenter     = 0x0004
	dtNoPrefix    = 0x0800
	dtEndEllipsis = 0x8000

	srcCopy = 0x00CC0020

	defaultCharset = 1
	fwBold         = 700
	fwNormal       = 400
	clearTypeQual  = 5
)

// hwndTopmost is (HWND)-1.
var hwndTopmost = ^uintptr(0)

type rect struct{ Left, Top, Right, Bottom int32 }

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	RcPaint     rect
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

// rgb builds a COLORREF (0x00BBGGRR).
func rgb(r, g, b uint32) uintptr { return uintptr(b<<16 | g<<8 | r) }

// ---------------------------------------------------------------------------
// Layout and palette
// ---------------------------------------------------------------------------

const (
	panelWidth  = 300
	rowHeight   = 21
	headerH     = 24
	padX        = 10
	padY        = 6
	marginLeft  = 12 // offset from the game window's top-left
	marginTop   = 12
	maxRows     = 8
	panelAlpha  = 242 // 0-255; high enough to stay readable over bright scenes
	repaintTick = 1   // timer id
)

var (
	colKey      = rgb(255, 0, 255) // chroma key: this exact colour becomes invisible
	colPanel    = rgb(16, 20, 29)
	colBorder   = rgb(70, 110, 180)
	colHeader   = rgb(120, 170, 255)
	colText     = rgb(230, 238, 248)
	colMuted    = rgb(130, 145, 165)
	colBlack    = rgb(255, 92, 92)
	colWhite    = rgb(255, 205, 80)
	colMe       = rgb(120, 220, 160)
	colPingGood = rgb(110, 210, 130)
	colPingMid  = rgb(240, 190, 90)
	colPingBad  = rgb(255, 110, 110)
)

// ---------------------------------------------------------------------------
// Overlay
// ---------------------------------------------------------------------------

// Overlay owns the on-top window. All exported methods are safe to call from
// any goroutine; the window itself lives on one locked OS thread.
type Overlay struct {
	mu      sync.Mutex
	rows    []Row
	enabled bool
	pid     uint32
	hwnd    uintptr

	started bool
	fontRow uintptr
	fontHdr uintptr
}

// New creates an overlay. Call Start to open the window.
func New() *Overlay { return &Overlay{} }

// Start opens the overlay window and runs its message loop on a dedicated OS
// thread. It returns once the window exists.
func (o *Overlay) Start() error {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return nil
	}
	o.started = true
	o.mu.Unlock()

	ready := make(chan error, 1)
	go o.run(ready)
	return <-ready
}

// SetEnabled shows or hides the overlay.
func (o *Overlay) SetEnabled(on bool) {
	o.mu.Lock()
	o.enabled = on
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmAppRepaint, 0, 0)
	}
}

// Enabled reports whether the overlay is currently shown.
func (o *Overlay) Enabled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enabled
}

// SetTarget names the process whose window the overlay should follow.
func (o *Overlay) SetTarget(pid uint32) {
	o.mu.Lock()
	o.pid = pid
	o.mu.Unlock()
}

// Update replaces the displayed rows.
func (o *Overlay) Update(rows []Row) {
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	o.mu.Lock()
	o.rows = append(o.rows[:0:0], rows...)
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmAppRepaint, 0, 0)
	}
}

// Close destroys the window.
func (o *Overlay) Close() {
	o.mu.Lock()
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmAppQuit, 0, 0)
	}
}

func (o *Overlay) run(ready chan<- error) {
	// The window and its message loop must stay on one thread for their whole
	// lifetime, so this goroutine never leaves it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := windows.UTF16PtrFromString("HLauncherOverlay")
	inst := moduleHandle()

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   windows.NewCallback(o.wndProc),
		Instance:  inst,
		ClassName: className,
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- fmt.Errorf("overlay: RegisterClassEx: %w", err)
		return
	}

	title, _ := windows.UTF16PtrFromString("H런처 오버레이")
	hwnd, _, err := procCreateWindowExW.Call(
		wsExLayered|wsExTransparent|wsExTopMost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, panelWidth, headerH+rowHeight*maxRows+padY*2,
		0, 0, uintptr(inst), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("overlay: CreateWindowEx: %w", err)
		return
	}

	// The key colour disappears entirely; everything else is drawn at panelAlpha.
	procSetLayeredWAttrs.Call(hwnd, colKey, panelAlpha, lwaColorKey|lwaAlpha)

	o.mu.Lock()
	o.hwnd = hwnd
	o.fontRow = newFont(14, fwNormal)
	o.fontHdr = newFont(14, fwBold)
	o.mu.Unlock()

	// Re-assert position and z-order periodically: games take the foreground and
	// can push other windows out of the topmost band.
	procSetTimer.Call(hwnd, repaintTick, 500, 0)

	ready <- nil

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	o.mu.Lock()
	if o.fontRow != 0 {
		procDeleteObject.Call(o.fontRow)
	}
	if o.fontHdr != 0 {
		procDeleteObject.Call(o.fontHdr)
	}
	o.hwnd, o.started = 0, false
	o.mu.Unlock()
}

// moduleHandle returns this executable's base handle, used as the window class
// instance.
func moduleHandle() windows.Handle {
	h, _, _ := procGetModuleHandleW.Call(0)
	return windows.Handle(h)
}

func newFont(height int32, weight uintptr) uintptr {
	face, _ := windows.UTF16PtrFromString("Malgun Gothic")
	h, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0, weight,
		0, 0, 0, defaultCharset,
		0, 0, clearTypeQual, 0,
		uintptr(unsafe.Pointer(face)))
	return h
}

func (o *Overlay) wndProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	switch message {
	case wmEraseBkgnd:
		return 1 // painted wholly in WM_PAINT; skipping this avoids flicker
	case wmPaint:
		o.paint(hwnd)
		return 0
	case wmTimer, wmAppRepaint:
		o.reposition(hwnd)
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmAppQuit, wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return r
}

// reposition parks the overlay at the top-left of the target window and keeps
// it in the topmost band, hiding it when there is nothing to show.
func (o *Overlay) reposition(hwnd uintptr) {
	o.mu.Lock()
	on, pid, n := o.enabled, o.pid, len(o.rows)
	o.mu.Unlock()

	// Hide unless there is something to show AND the game is the window being
	// looked at. Without the foreground check the panel sits on top of the
	// desktop while you are not even playing, which is just in the way.
	if !on || n == 0 || (pid != 0 && !isForeground(pid)) {
		procShowWindow.Call(hwnd, swHide)
		return
	}
	x, y := int32(40), int32(40)
	if pid != 0 {
		if r, ok := mainWindowRect(pid); ok {
			x, y = r.Left+marginLeft, r.Top+marginTop
		}
	}
	h := int32(headerH + rowHeight*int32(n) + padY*2)
	procSetWindowPos.Call(hwnd, hwndTopmost,
		uintptr(x), uintptr(y), uintptr(int32(panelWidth)), uintptr(h),
		swpNoActivate|swpShowWindow)
}

func (o *Overlay) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	o.mu.Lock()
	rows := append([]Row(nil), o.rows...)
	fontRow, fontHdr := o.fontRow, o.fontHdr
	o.mu.Unlock()

	w := int32(panelWidth)
	h := int32(headerH + rowHeight*int32(len(rows)) + padY*2)

	// Draw into a memory bitmap first, then blit: painting directly over a game
	// flickers badly.
	memDC, _, _ := procCreateCompatDC.Call(hdc)
	bmp, _, _ := procCreateCompatBmp.Call(hdc, uintptr(w), uintptr(h))
	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer func() {
		procSelectObject.Call(memDC, old)
		procDeleteObject.Call(bmp)
		procDeleteDC.Call(memDC)
	}()

	// Everything starts as the key colour (invisible), then the panel is drawn
	// on top of it — that is what gives rounded-looking margins for free.
	fill(memDC, rect{0, 0, w, h}, colKey)
	fill(memDC, rect{0, 0, w, h}, colPanel)
	frame(memDC, rect{0, 0, w, h}, colBorder)

	procSetBkMode.Call(memDC, transparentBk)

	// Header
	procSelectObject.Call(memDC, fontHdr)
	drawText(memDC, rect{padX, padY, w - padX, padY + headerH}, colHeader,
		fmt.Sprintf("H런처  ·  %d명", len(rows)), dtLeft)

	// Rows
	procSelectObject.Call(memDC, fontRow)
	y := int32(padY + headerH)
	for i, r := range rows {
		line := rect{padX, y, w - padX, y + rowHeight}

		name := fmt.Sprintf("%d. %s", i+1, r.Name)
		col := colText
		switch {
		case r.Black != "":
			col = colBlack
		case r.Me:
			col = colMe
		case r.White:
			col = colWhite
		}
		drawText(memDC, rect{line.Left, line.Top, line.Right - 86, line.Bottom}, col, name, dtLeft|dtEndEllipsis)

		// Race, dimmed, right before the ping column.
		drawText(memDC, rect{line.Right - 86, line.Top, line.Right - 46, line.Bottom},
			colMuted, raceShort(r.Race), dtLeft)

		// Ping, colour-coded; "-" when the engine does not expose it.
		drawText(memDC, rect{line.Right - 46, line.Top, line.Right, line.Bottom},
			pingColor(r.Ping), pingText(r.Ping), dtRight)

		y += rowHeight
	}

	procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), memDC, 0, 0, srcCopy)
}

func fill(hdc uintptr, rc rect, color uintptr) {
	br, _, _ := procCreateSolidBrush.Call(color)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
	procDeleteObject.Call(br)
}

// frame outlines a rectangle, so the panel reads as a panel rather than a
// smudge over whatever is behind it.
func frame(hdc uintptr, rc rect, color uintptr) {
	br, _, _ := procCreateSolidBrush.Call(color)
	procFrameRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
	procDeleteObject.Call(br)
}

// isForeground reports whether the focused window belongs to pid.
func isForeground(pid uint32) bool {
	fg, _, _ := procGetForegroundWnd.Call()
	if fg == 0 {
		return false
	}
	var owner uint32
	procGetWndThreadPID.Call(fg, uintptr(unsafe.Pointer(&owner)))
	return owner == pid
}

func drawText(hdc uintptr, rc rect, color uintptr, s string, flags uintptr) {
	if s == "" {
		return
	}
	procSetTextColor.Call(hdc, color)
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return
	}
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(p)), ^uintptr(0),
		uintptr(unsafe.Pointer(&rc)), flags|dtSingleLine|dtVCenter|dtNoPrefix)
}

func raceShort(r string) string {
	switch r {
	case "Terran":
		return "테란"
	case "Zerg":
		return "저그"
	case "Protoss":
		return "토스"
	case "Random":
		return "랜덤"
	}
	return ""
}

func pingText(p int) string {
	if p < 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", p)
}

func pingColor(p int) uintptr {
	switch {
	case p < 0:
		return colMuted
	case p <= 60:
		return colPingGood
	case p <= 150:
		return colPingMid
	}
	return colPingBad
}

// ---------------------------------------------------------------------------
// Finding the game window
// ---------------------------------------------------------------------------

// mainWindowRect returns the bounds of pid's largest visible titled window.
func mainWindowRect(pid uint32) (rect, bool) {
	var best rect
	var bestArea int32
	found := false

	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var owner uint32
		procGetWndThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		if n, _, _ := procGetWindowTextLen.Call(hwnd); n == 0 {
			return 1
		}
		var rc rect
		if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
			return 1
		}
		area := (rc.Right - rc.Left) * (rc.Bottom - rc.Top)
		if area > bestArea {
			best, bestArea, found = rc, area, true
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return best, found
}

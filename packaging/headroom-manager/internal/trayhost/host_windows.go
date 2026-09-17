//go:build windows

package trayhost

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	gdi32              = windows.NewLazySystemDLL("gdi32.dll")
	registerClass      = user32.NewProc("RegisterClassExW")
	createWindow       = user32.NewProc("CreateWindowExW")
	destroyWindow      = user32.NewProc("DestroyWindow")
	defWindowProc      = user32.NewProc("DefWindowProcW")
	postMessage        = user32.NewProc("PostMessageW")
	getMessage         = user32.NewProc("GetMessageW")
	dispatchMessage    = user32.NewProc("DispatchMessageW")
	postQuit           = user32.NewProc("PostQuitMessage")
	registerMessage    = user32.NewProc("RegisterWindowMessageW")
	setTimer           = user32.NewProc("SetTimer")
	getCursorPos       = user32.NewProc("GetCursorPos")
	setForeground      = user32.NewProc("SetForegroundWindow")
	allowForeground    = user32.NewProc("AllowSetForegroundWindow")
	notifyIcon         = shell32.NewProc("Shell_NotifyIconW")
	notifyRect         = shell32.NewProc("Shell_NotifyIconGetRect")
	createDIB          = gdi32.NewProc("CreateDIBSection")
	createBitmap       = gdi32.NewProc("CreateBitmap")
	deleteObject       = gdi32.NewProc("DeleteObject")
	createIconIndirect = user32.NewProc("CreateIconIndirect")
	destroyIcon        = user32.NewProc("DestroyIcon")
)

const (
	wmClose    = 0x10
	wmDestroy  = 2
	wmTimer    = 0x113
	wmCommand  = 0x8001
	wmTray     = 0x8002
	nifGuid    = 0x20
	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nimFocus   = 3
	nimVersion = 4
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type message struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type notifyData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	BalloonIcon         uintptr
}
type iconIdentifier struct {
	Size   uint32
	Window uintptr
	ID     uint32
	GUID   windows.GUID
}
type bitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, Bits           uint16
	Compression, ImageSize uint32
	XPels, YPels           int32
	Used, Important        uint32
}
type iconInfo struct {
	IsIcon             int32
	XHotspot, YHotspot uint32
	Mask, Color        uintptr
}

type host struct {
	window                      uintptr
	parent                      windows.Handle
	parentPID                   uint32
	guid                        windows.GUID
	icon                        uintptr
	tooltip                     string
	added, ready, ignoreRelease bool
	taskbarCreated              uint32
	encoder                     *json.Encoder
	commands                    chan Command
	done                        chan struct{}
	lastGeometry                Event
	sendShell                   func(uint32, *notifyData) bool
}

func guidForPath(path string) windows.GUID {
	id := identity(path)
	guid := windows.GUID{Data1: binary.BigEndian.Uint32(id[0:4]), Data2: binary.BigEndian.Uint16(id[4:6]), Data3: binary.BigEndian.Uint16(id[6:8])}
	copy(guid.Data4[:], id[8:])
	return guid
}

func copyText(target []uint16, text string) {
	units := utf16.Encode([]rune(text))
	n := len(units)
	if n >= len(target) {
		n = len(target) - 1
	}
	// Never split a surrogate pair at the Shell's UTF-16 boundary.
	if n > 0 && units[n-1] >= 0xd800 && units[n-1] <= 0xdbff {
		n--
	}
	copy(target, units[:n])
	target[n] = 0
}

func (h *host) data() notifyData {
	return notifyData{Size: uint32(unsafe.Sizeof(notifyData{})), Window: h.window, Flags: nifGuid, GUID: h.guid}
}

func (h *host) updateIcon(add bool) bool {
	if h.icon == 0 {
		return false
	}
	data := h.data()
	data.Flags |= 1 | 2 | 4 | 0x80 // callback, icon, tooltip, standard tooltip with v4
	data.Callback = wmTray
	data.Icon = h.icon
	copyText(data.Tip[:], h.tooltip)
	operation := uint32(nimModify)
	if add {
		operation = nimAdd
	}
	if !h.sendShell(operation, &data) {
		return false
	}
	if add {
		h.added = true
		data.Version = 4
		if !h.sendShell(nimVersion, &data) {
			h.remove()
			return false
		}
	}
	return true
}

func (h *host) remove() {
	if h.added {
		data := h.data()
		h.sendShell(nimDelete, &data)
		h.added = false
	}
}

func (h *host) emit(event Event) {
	event.Version = ProtocolVersion
	if h.encoder.Encode(event) != nil {
		postMessage.Call(h.window, wmClose, 0, 0)
	}
}

func traySize() int {
	size, _, _ := user32.NewProc("GetSystemMetrics").Call(49) // SM_CXSMICON
	taskbar, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Shell_TrayWnd"))), 0)
	if taskbar != 0 {
		dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(taskbar)
		if dpi > 0 {
			size, _, _ = user32.NewProc("GetSystemMetricsForDpi").Call(49, dpi)
		}
	}
	if size < 16 {
		return 16
	}
	if size > 64 {
		return 64
	}
	return int(size)
}

func (h *host) geometry() {
	id := iconIdentifier{Size: uint32(unsafe.Sizeof(iconIdentifier{})), GUID: h.guid}
	var bounds rect
	result, _, _ := notifyRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&bounds)))
	event := Event{Event: "geometry"}
	if result == 0 && bounds.Right > bounds.Left && bounds.Bottom > bounds.Top {
		event.X, event.Y = bounds.Left, bounds.Top
		event.Width, event.Height = bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
		var cursor point
		getCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
		event.Hover = cursor.X >= bounds.Left && cursor.X < bounds.Right && cursor.Y >= bounds.Top && cursor.Y < bounds.Bottom
	}
	if event != h.lastGeometry {
		h.lastGeometry = event
		h.emit(event)
	}
}

func bitmapIcon(size int, pixels []byte) (uintptr, error) {
	info := bitmapInfo{Size: 40, Width: int32(size), Height: -int32(size), Planes: 1, Bits: 32}
	var bits unsafe.Pointer
	color, _, _ := createDIB.Call(0, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0, errors.New("cannot allocate tray bitmap")
	}
	defer deleteObject.Call(color)
	copy(unsafe.Slice((*byte)(bits), len(pixels)), pixels)
	maskBits := make([]byte, ((size+15)/16)*2*size)
	mask, _, _ := createBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	if mask == 0 {
		return 0, errors.New("cannot allocate tray mask")
	}
	defer deleteObject.Call(mask)
	icon := iconInfo{IsIcon: 1, Color: color, Mask: mask}
	handle, _, _ := createIconIndirect.Call(uintptr(unsafe.Pointer(&icon)))
	if handle == 0 {
		return 0, errors.New("cannot create tray icon")
	}
	return handle, nil
}

func (h *host) apply(command Command) bool {
	switch command.Op {
	case "icon":
		icon, err := bitmapIcon(command.Size, command.Pixels)
		if err != nil {
			return false
		}
		prior := h.icon
		h.icon = icon
		ok := h.updateIcon(!h.added)
		if prior != 0 {
			destroyIcon.Call(prior)
		}
		if !h.ready {
			if !ok {
				return false
			}
			h.ready = true
			h.emit(Event{Event: "ready", Size: traySize()})
		}
	case "tooltip":
		h.tooltip = command.Tooltip
		if h.added {
			h.updateIcon(false)
		}
	case "notify":
		if !h.added {
			return true
		}
		data := h.data()
		data.Flags |= 0x10 // NIF_INFO
		data.InfoFlags = 1
		if command.Severity == 2 {
			data.InfoFlags = 2
		}
		if command.Severity == 3 {
			data.InfoFlags = 3
		}
		copyText(data.Info[:], command.Message)
		copyText(data.InfoTitle[:], command.Title)
		h.sendShell(nimModify, &data)
	case "focus":
		data := h.data()
		h.sendShell(nimFocus, &data)
	case "quit":
		return false
	}
	return true
}

func (h *host) callback(window uintptr, event uint32, wParam, lParam uintptr) uintptr {
	switch event {
	case wmCommand:
		select {
		case command := <-h.commands:
			if !h.apply(command) {
				destroyWindow.Call(window)
			}
		default:
		}
		return 0
	case wmTimer:
		h.geometry()
		return 0
	case wmClose:
		destroyWindow.Call(window)
		return 0
	case wmDestroy:
		h.remove()
		postQuit.Call(0)
		return 0
	case wmTray:
		reason := int(uint16(lParam))
		activation := 0
		switch reason {
		case 0x400, 0x401: // NIN_SELECT, NIN_KEYSELECT
			if h.ignoreRelease {
				h.ignoreRelease = false
				return 0
			}
			activation = 3 // QSystemTrayIcon::Trigger
		case 0x203: // WM_LBUTTONDBLCLK
			h.ignoreRelease = true
			activation = 2
		case 0x7b: // WM_CONTEXTMENU
			activation = 1
		case 0x208: // WM_MBUTTONUP
			activation = 4
		case 0x405: // NIN_BALLOONUSERCLICK
			h.emit(Event{Event: "message"})
			return 0
		}
		if activation != 0 {
			setForeground.Call(window)
			allowForeground.Call(uintptr(h.parentPID))
			h.geometry()
			h.emit(Event{Event: "activate", Reason: activation})
		}
		return 0
	}
	if event == h.taskbarCreated {
		h.added = false
		if h.icon != 0 {
			h.updateIcon(true)
		}
		h.emit(Event{Event: "size", Size: traySize()})
		return 0
	}
	result, _, _ := defWindowProc.Call(window, uintptr(event), wParam, lParam)
	return result
}

// Run must be launched by the installed desktop, through inherited private
// pipes. Pin its actual parent process, not a caller-supplied PID or hostname.
func Run(input io.Reader, output io.Writer, launcher, parentExecutable string) error {
	parentPID := uint32(os.Getppid())
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, parentPID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	path := make([]uint16, 32768)
	length := uint32(len(path))
	if err = windows.QueryFullProcessImageName(parent, 0, &path[0], &length); err != nil {
		return err
	}
	actual, actualErr := os.Stat(windows.UTF16ToString(path[:length]))
	expected, expectedErr := os.Stat(parentExecutable)
	if actualErr != nil || expectedErr != nil || !os.SameFile(actual, expected) {
		return errors.New("tray host parent is not the installed desktop")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Keep Shell geometry in physical pixels; Qt converts it per monitor.
	user32.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	h := &host{parent: parent, parentPID: parentPID, guid: guidForPath(launcher), encoder: json.NewEncoder(output), commands: make(chan Command, 1), done: make(chan struct{})}
	h.sendShell = func(op uint32, data *notifyData) bool {
		result, _, _ := notifyIcon.Call(uintptr(op), uintptr(unsafe.Pointer(data)))
		return result != 0
	}
	var module windows.Handle
	err = windows.GetModuleHandleEx(0, nil, &module)
	if err != nil {
		return err
	}
	name := windows.StringToUTF16Ptr("HeadroomStableTrayV1")
	callback := syscall.NewCallback(h.callback)
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Proc: callback, Instance: uintptr(module), ClassName: name}
	atom, _, _ := registerClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return errors.New("cannot register tray window")
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), uintptr(module))
	window, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, uintptr(module), 0)
	if window == 0 {
		return errors.New("cannot create tray window")
	}
	h.window = window
	created, _, _ := registerMessage.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	h.taskbarCreated = uint32(created)
	setTimer.Call(window, 1, 200, 0)
	defer func() {
		h.remove()
		destroyWindow.Call(window)
		if h.icon != 0 {
			destroyIcon.Call(h.icon)
		}
	}()
	// EOF handles ordinary exit and crashes; the pinned process handle also
	// covers accidentally inherited pipe handles without ever following a reused PID.
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			status, _ := windows.WaitForSingleObject(parent, 200)
			if status == windows.WAIT_OBJECT_0 {
				postMessage.Call(window, wmClose, 0, 0)
				return
			}
			select {
			case <-h.done:
				return
			default:
			}
		}
	}()
	defer workers.Wait()
	// Unblock the parent watcher before waiting for it during return.
	defer func() {
		select {
		case <-h.done:
		default:
			close(h.done)
		}
	}()
	go func() {
		_ = readCommands(input, func(command Command) bool {
			select {
			case h.commands <- command:
			case <-h.done:
				return false
			}
			postMessage.Call(window, wmCommand, 0, 0)
			return command.Op != "quit"
		})
		postMessage.Call(window, wmClose, 0, 0)
	}()
	var msg message
	for {
		result, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return errors.New("tray message loop failed")
		}
		if result == 0 {
			break
		}
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	if !h.ready {
		return errors.New("tray host stopped before registration")
	}
	return nil
}

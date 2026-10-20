package main

// Small Windows helpers without extra dependencies: file version resources (to recognise DLSS DLLs whatever their
// name), PE machine type, the screen's work area, the GPU name, DPAPI for the saved login and a global hotkey.

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modVersion                  = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoSizeW = modVersion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = modVersion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = modVersion.NewProc("VerQueryValueW")
	modUser32                   = windows.NewLazySystemDLL("user32.dll")
	procSystemParametersInfoW   = modUser32.NewProc("SystemParametersInfoW")
	procRegisterHotKey          = modUser32.NewProc("RegisterHotKey")
	procUnregisterHotKey        = modUser32.NewProc("UnregisterHotKey")
	procGetMessageW             = modUser32.NewProc("GetMessageW")
	procPostThreadMessageW      = modUser32.NewProc("PostThreadMessageW")
	procFindWindowExW           = modUser32.NewProc("FindWindowExW")
	procGetWindowThreadProcId   = modUser32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow     = modUser32.NewProc("SetForegroundWindow")
	procGetForegroundWindow     = modUser32.NewProc("GetForegroundWindow")
	procShowWindow              = modUser32.NewProc("ShowWindow")
	procIsIconic                = modUser32.NewProc("IsIconic")
	procIsWindowVisible         = modUser32.NewProc("IsWindowVisible")
	procAttachThreadInput       = modUser32.NewProc("AttachThreadInput")
)

// ownWindow: the launcher's own top-level window (Wails' class, this process).
func ownWindow() uintptr {
	cls, _ := syscall.UTF16PtrFromString("wailsWindow")
	pid := windows.GetCurrentProcessId()
	var h uintptr
	for {
		h, _, _ = procFindWindowExW.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var p uint32
		procGetWindowThreadProcId.Call(h, uintptr(unsafe.Pointer(&p)))
		if p == pid {
			return h
		}
	}
}

// inFront: the window is shown, not minimised, and has the focus.
func inFront(h uintptr) bool {
	if h == 0 {
		return false
	}
	vis, _, _ := procIsWindowVisible.Call(h)
	ico, _, _ := procIsIconic.Call(h)
	fg, _, _ := procGetForegroundWindow.Call()
	return vis != 0 && ico == 0 && fg == h
}

// toFront restores the window and gives it the keyboard and the controller. After our hotkey Windows allows it
// directly; after the controller combo (pad input does not count as input for that rule) it only works with our
// thread attached to the input of the window in front (the game), as Windows itself suggests.
func toFront(h uintptr) {
	if h == 0 {
		return
	}
	const swRestore, swShow = 9, 5
	if ico, _, _ := procIsIconic.Call(h); ico != 0 {
		procShowWindow.Call(h, swRestore)
	} else {
		procShowWindow.Call(h, swShow)
	}
	if ok, _, _ := procSetForegroundWindow.Call(h); ok != 0 {
		return
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fgThread, _, _ := procGetWindowThreadProcId.Call(fg, 0)
	me := uintptr(windows.GetCurrentThreadId())
	if fgThread != 0 && fgThread != me {
		procAttachThreadInput.Call(me, fgThread, 1)
		procSetForegroundWindow.Call(h)
		procAttachThreadInput.Call(me, fgThread, 0)
	}
}

type fileVersion struct {
	Major, Minor, Build, Private uint16
	Product, Description         string
}

func (v fileVersion) String() string {
	if v.Private > 0 {
		return fmt.Sprintf("%d.%d.%d.%d", v.Major, v.Minor, v.Build, v.Private)
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Build)
}

func (v fileVersion) less(o fileVersion) bool {
	a := [4]uint16{v.Major, v.Minor, v.Build, v.Private}
	b := [4]uint16{o.Major, o.Minor, o.Build, o.Private}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func readFileVersion(path string) (fileVersion, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileVersion{}, false
	}
	n, _, _ := procGetFileVersionInfoSizeW.Call(uintptr(unsafe.Pointer(p)), 0)
	if n == 0 {
		return fileVersion{}, false
	}
	buf := make([]byte, n)
	if r, _, _ := procGetFileVersionInfoW.Call(uintptr(unsafe.Pointer(p)), 0, n, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return fileVersion{}, false
	}
	query := func(sub string) (unsafe.Pointer, uint32) {
		s, _ := syscall.UTF16PtrFromString(sub)
		var ptr unsafe.Pointer
		var l uint32
		if r, _, _ := procVerQueryValueW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&l))); r == 0 {
			return nil, 0
		}
		return ptr, l
	}
	var v fileVersion
	if ptr, l := query(`\`); ptr != nil && l >= 52 {
		fi := unsafe.Slice((*byte)(ptr), l)
		ms, ls := binary.LittleEndian.Uint32(fi[8:]), binary.LittleEndian.Uint32(fi[12:])
		v.Major, v.Minor, v.Build, v.Private = uint16(ms>>16), uint16(ms), uint16(ls>>16), uint16(ls)
	}
	str := func(name string) string {
		if ptr, l := query(`\VarFileInfo\Translation`); ptr != nil && l >= 4 {
			t := unsafe.Slice((*uint16)(ptr), 2)
			if sp, sl := query(fmt.Sprintf(`\StringFileInfo\%04x%04x\%s`, t[0], t[1], name)); sp != nil && sl > 0 {
				return strings.TrimRight(windows.UTF16ToString(unsafe.Slice((*uint16)(sp), sl)), "\x00")
			}
		}
		return ""
	}
	v.Product, v.Description = str("ProductName"), str("FileDescription")
	return v, true
}

// isX64 reads the PE header: the DLSS SDK also ships ARM64 builds of the same DLLs, which would not load.
func isX64(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := f.ReadAt(b, 0x3C); err != nil {
		return false
	}
	pe := int64(binary.LittleEndian.Uint32(b))
	m := make([]byte, 2)
	if _, err := f.ReadAt(m, pe+4); err != nil {
		return false
	}
	return binary.LittleEndian.Uint16(m) == 0x8664
}

func workArea() (int, int) {
	var r struct{ Left, Top, Right, Bottom int32 }
	const spiGetWorkArea = 0x0030
	if ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok == 0 {
		return 1920, 1080
	}
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// gpuNames lists the display adapters' driver descriptions (no WMI needed).
func gpuNames() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return ""
	}
	defer k.Close()
	subs, _ := k.ReadSubKeyNames(-1)
	var out []string
	for _, s := range subs {
		sk, err := registry.OpenKey(k, s, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if d, _, err := sk.GetStringValue("DriverDesc"); err == nil {
			out = append(out, d)
		}
		sk.Close()
	}
	return strings.Join(out, " | ")
}

// protect / unprotect: Windows DPAPI, bound to this Windows user. The saved login token is useless on another
// account or another PC.
func protect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// hotkey: a global keyboard shortcut (registered only while the game runs, so it does not steal it elsewhere).
type hotkey struct {
	mu       sync.Mutex
	threadID uint32
}

const (
	modShift   = 0x0004
	modNoRep   = 0x4000
	vkTab      = 0x09
	wmHotkey   = 0x0312
	wmQuit     = 0x0012
	hotkeyID   = 0xB71
)

// start registers Shift+Tab and calls fn on every press, from its own OS thread (RegisterHotKey's messages go to
// the registering thread's queue).
func (h *hotkey) start(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.threadID != 0 {
		return
	}
	ready := make(chan uint32)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		tid := windows.GetCurrentThreadId()
		ok, _, _ := procRegisterHotKey.Call(0, hotkeyID, modShift|modNoRep, vkTab)
		ready <- tid
		if ok == 0 {
			return
		}
		defer procUnregisterHotKey.Call(0, hotkeyID)
		var msg struct {
			Hwnd    uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			Pt      struct{ X, Y int32 }
		}
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			if msg.Message == wmHotkey && msg.WParam == hotkeyID {
				fn()
			}
		}
	}()
	h.threadID = <-ready
}

func (h *hotkey) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.threadID != 0 {
		procPostThreadMessageW.Call(uintptr(h.threadID), wmQuit, 0, 0)
		h.threadID = 0
	}
}

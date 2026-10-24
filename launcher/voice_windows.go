package main

// [voice] The voice engine (voice/engine.c) on Windows: a DLL embedded in Recomp.exe (the launcher stays one file
// and updates itself as one), written once to %LOCALAPPDATA%\RecompLauncher under a name of its own content and
// loaded from there. Only ints cross (voice/engine.h), so no cgo. voice/build.sh cross-compiles it.

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed recompvoice_windows_amd64.dll
var voiceDLL []byte

var (
	rvOnce                                                 sync.Once
	rvErr                                                  error
	rvpStart, rvpStop, rvpSet, rvpPeer, rvpDevice, rvpDevs *windows.LazyProc
	rvpStats                                               *windows.LazyProc
)

func rvLoad() error {
	rvOnce.Do(func() {
		if runtime.GOARCH != "amd64" {
			rvErr = errors.New("el chat de voz solo está para Windows de 64 bits (x64)")
			return
		}
		sum := sha256.Sum256(voiceDLL)
		dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "RecompLauncher")
		p := filepath.Join(dir, fmt.Sprintf("recompvoice-%x.dll", sum[:6]))
		if b, err := os.ReadFile(p); err != nil || sha256.Sum256(b) != sum {
			_ = os.MkdirAll(dir, 0o755)
			tmp := p + ".tmp"
			if err := os.WriteFile(tmp, voiceDLL, 0o644); err != nil {
				rvErr = err
				return
			}
			if err := os.Rename(tmp, p); err != nil {
				rvErr = err
				return
			}
		}
		d := windows.NewLazyDLL(p)
		if rvErr = d.Load(); rvErr != nil {
			return
		}
		rvpStart, rvpStop, rvpSet = d.NewProc("rv_start"), d.NewProc("rv_stop"), d.NewProc("rv_set")
		rvpPeer, rvpDevice, rvpDevs, rvpStats = d.NewProc("rv_peer"), d.NewProc("rv_device"), d.NewProc("rv_devices"), d.NewProc("rv_stats")
	})
	return rvErr
}

func cstr(s string) *byte {
	p, err := windows.BytePtrFromString(s)
	if err != nil {
		p, _ = windows.BytePtrFromString("")
	}
	return p
}

func rvStart(host string, port int, token string, slot int) int {
	h, t := cstr(host), cstr(token)
	r, _, _ := rvpStart.Call(uintptr(unsafe.Pointer(h)), uintptr(port), uintptr(unsafe.Pointer(t)), uintptr(slot))
	runtime.KeepAlive(h)
	runtime.KeepAlive(t)
	return int(int32(r))
}

func rvStop()                 { rvpStop.Call() }
func rvSet(key, v int)        { rvpSet.Call(uintptr(key), uintptr(v)) }
func rvPeer(slot, vol, m int) { rvpPeer.Call(uintptr(slot), uintptr(vol), uintptr(m)) }

func rvDevice(capture int, name string) int {
	n := cstr(name)
	r, _, _ := rvpDevice.Call(uintptr(capture), uintptr(unsafe.Pointer(n)))
	runtime.KeepAlive(n)
	return int(int32(r))
}

func rvDevices(capture int) []string {
	buf := make([]byte, 8192)
	r, _, _ := rvpDevs.Call(uintptr(capture), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	n := int(int32(r))
	if n <= 0 {
		return nil
	}
	s := string(buf[:min(n, len(buf))])
	return strings.Split(strings.TrimRight(s, "\x00"), "\n")
}

func rvStats(out []int32) {
	rvpStats.Call(uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)))
}

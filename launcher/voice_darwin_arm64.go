package main

// [voice] The voice engine (voice/engine.c) compiled into the macOS launcher through cgo (voice_darwin_arm64.c); the
// Windows launcher loads the same engine as a DLL (voice_windows.go).

/*
#cgo CFLAGS: -I${SRCDIR}/voice/include -I${SRCDIR}/voice
#cgo LDFLAGS: ${SRCDIR}/voice/lib/darwin-arm64/libopus.a -framework CoreAudio -framework AudioToolbox -framework CoreFoundation
#include <stdlib.h>
#include "voice/engine.h"
*/
import "C"

import (
	"strings"
	"unsafe"
)

func rvLoad() error { return nil }

func rvStart(host string, port int, token string, slot int) int {
	h, t := C.CString(host), C.CString(token)
	defer C.free(unsafe.Pointer(h))
	defer C.free(unsafe.Pointer(t))
	return int(C.rv_start(h, C.int(port), t, C.int(slot)))
}

func rvStop()                 { C.rv_stop() }
func rvSet(key, v int)        { C.rv_set(C.int(key), C.int(v)) }
func rvPeer(slot, vol, m int) { C.rv_peer(C.int(slot), C.int(vol), C.int(m)) }

func rvDevice(capture int, name string) int {
	n := C.CString(name)
	defer C.free(unsafe.Pointer(n))
	return int(C.rv_device(C.int(capture), n))
}

func rvDevices(capture int) []string {
	buf := make([]byte, 8192)
	n := int(C.rv_devices(C.int(capture), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
	if n <= 0 {
		return nil
	}
	s := string(buf[:min(n, len(buf))])
	return strings.Split(strings.TrimRight(s, "\x00"), "\n")
}

func rvStats(out []int32) { C.rv_stats((*C.int)(unsafe.Pointer(&out[0])), C.int(len(out))) }

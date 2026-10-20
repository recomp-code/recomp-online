package main

// XInput (Windows): the buttons held on any connected controller, for the Back + Start / Guide shortcut.

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modXInput = windows.NewLazySystemDLL("xinput1_4.dll")
	// ordinal 100 = XInputGetStateEx: same as XInputGetState plus the Guide button
	xinputGetStateEx, _ = func() (uintptr, error) {
		if err := modXInput.Load(); err != nil {
			return 0, err
		}
		return windows.GetProcAddressByOrdinal(windows.Handle(modXInput.Handle()), 100)
	}()
	procXInputGetState = modXInput.NewProc("XInputGetState")
)

type xinputState struct {
	Packet  uint32
	Buttons uint16
	_       [10]byte // triggers and sticks
}

// padButtons: the buttons held on any connected controller.
func padButtons() uint16 {
	var all uint16
	for i := uintptr(0); i < 4; i++ {
		var s xinputState
		var r uintptr
		if xinputGetStateEx != 0 {
			r, _, _ = syscall.SyscallN(xinputGetStateEx, i, uintptr(unsafe.Pointer(&s)))
		} else if procXInputGetState.Find() == nil {
			r, _, _ = procXInputGetState.Call(i, uintptr(unsafe.Pointer(&s)))
		} else {
			return 0
		}
		if r == 0 {
			all |= s.Buttons
		}
	}
	return all
}

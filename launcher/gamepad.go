package main

// The controller shortcut for the overlay: most people play with a pad, so Back + Start (pressed together) does what
// Shift+Tab does. The Guide button works too where XInput exposes it (Windows' Game Bar may also take it). The game
// itself ignores Start while Back is held, so the combo does not pause the fight.

import (
	"sync"
	"syscall"
	"time"
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

const (
	padStart = 0x0010
	padBack  = 0x0020
	padGuide = 0x0400
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

// padWatch polls the controllers while a game runs and calls fn on each press of the combo.
type padWatch struct {
	mu   sync.Mutex
	stop chan struct{}
}

func (p *padWatch) start(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	go func() {
		t := time.NewTicker(25 * time.Millisecond)
		defer t.Stop()
		was := true // a combo already held when the game starts does not count
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			b := padButtons()
			now := b&(padBack|padStart) == padBack|padStart || b&padGuide != 0
			if now && !was {
				fn()
			}
			was = now
		}
	}()
}

func (p *padWatch) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
}

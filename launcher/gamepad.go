package main

// The controller shortcut for the overlay: most people play with a pad, so Back + Start (pressed together) does what
// Shift+Tab does. The Guide button works too where the system lets the launcher see it (Windows' Game Bar or macOS
// may take it). The game itself ignores Start while Back is held, so the combo does not pause the fight.
// padButtons (gamepad_windows.go: XInput, gamepad_darwin.go: GameController) reports the buttons in XInput's bits.

import (
	"sync"
	"time"
)

// XInput's button bits
const (
	padStart = 0x0010
	padBack  = 0x0020
	padGuide = 0x0400
)

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

package main

// macOS side of the window handling (mac_darwin.m). As on Windows the launcher never just vanishes while the game
// runs: it stays on screen while the game boots, then goes to the Dock (minimised) with the game in front; Shift+Tab,
// Back + Start or its Dock icon bring it back over the game, with the focus.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework Carbon -framework GameController -framework AVFoundation -framework CoreGraphics
int rlInFront(void);
void rlToFront(void);
int rlFocusChildApp(int parent);
void rlWorkArea(int *w, int *h);
int rlHotkeyStart(void);
void rlHotkeyStop(void);
unsigned rlPadButtons(void);
int rlRightOptionDown(void);
int rlMicAccess(void);
void rlMicRequest(void);
*/
import "C"

import (
	"context"
	"os/exec"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/menu"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// toBackground: the game in front first (only an active app can hand the focus over), then the launcher to the Dock.
func (a *App) toBackground() {
	if pid := a.game.pid(); pid > 0 {
		C.rlFocusChildApp(C.int(pid))
	}
	wruntime.WindowMinimise(a.ctx)
}

func (a *App) toForeground() {
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
	C.rlToFront()
}

func appMenu() *menu.Menu { return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu()) }

func launcherInFront() bool { return C.rlInFront() != 0 }
func launcherToFront()      { C.rlToFront() }

func openFolder(ctx context.Context, dir string) { _ = exec.Command("/usr/bin/open", dir).Start() }

func workArea() (int, int) {
	var w, h C.int
	C.rlWorkArea(&w, &h)
	return int(w), int(h)
}

func padButtons() uint16 { return uint16(C.rlPadButtons()) }

// hotkey: Shift+Tab (hkOverlay) and Shift+M (hkMute) while the game runs (Carbon's RegisterEventHotKey: it reaches
// the launcher behind the game).
type hotkey struct {
	mu sync.Mutex
	on bool
}

var (
	hotkeyMu sync.Mutex
	hotkeyFn func(id int)
)

//export goHotkey
func goHotkey(id C.int) {
	hotkeyMu.Lock()
	fn := hotkeyFn
	hotkeyMu.Unlock()
	if fn != nil {
		go fn(int(id)) // called on the main thread: the overlay itself needs it
	}
}

func (h *hotkey) start(fn func(id int)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.on {
		return
	}
	hotkeyMu.Lock()
	hotkeyFn = fn
	hotkeyMu.Unlock()
	h.on = C.rlHotkeyStart() != 0
}

func (h *hotkey) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.on {
		C.rlHotkeyStop()
		h.on = false
	}
	hotkeyMu.Lock()
	hotkeyFn = nil
	hotkeyMu.Unlock()
}

// [voice] push to talk: the right Option key, held
const pttKeyName = "Opción derecha"

func pttDown() bool { return C.rlRightOptionDown() != 0 }

// [voice] the microphone permission: 0 not asked, 1 restricted, 2 denied, 3 allowed
func micAccess() int { return int(C.rlMicAccess()) }

var (
	micMu   sync.Mutex
	micDone func(bool)
)

// micRequest asks macOS (once: it remembers the answer) and calls done with it, on another goroutine.
func micRequest(done func(bool)) {
	micMu.Lock()
	micDone = done
	micMu.Unlock()
	C.rlMicRequest()
}

//export goMicAnswer
func goMicAnswer(granted C.int) {
	micMu.Lock()
	fn := micDone
	micDone = nil
	micMu.Unlock()
	if fn != nil {
		go fn(granted != 0)
	}
}

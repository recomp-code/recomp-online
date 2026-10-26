//go:build android

package main

// Android: the launcher is the app's main activity and the game an activity of its own (another process, see
// game_android.go). There is no window to minimise or keep on top: "background" is the game in front, "foreground" the
// launcher brought back over it (an invitation that needs an answer, the end of a match).

import (
	"context"
	"encoding/json"
	"strconv"
)

func (a *App) toBackground() {}

func (a *App) toForeground() { launcherToFront() }

func launcherInFront() bool { s, _ := platform("inFront", nil); return s == "1" }
func launcherToFront()      { _, _ = platform("toFront", nil) }

// openFolder: Android has no file manager to open an app's private folder in.
func openFolder(ctx context.Context, dir string) {}

// workArea: the screen in pixels (the runtime takes the whole screen anyway: game_android.go).
func workArea() (int, int) {
	s, err := platform("screen", nil)
	if err == nil {
		var wh [2]int
		if json.Unmarshal([]byte(s), &wh) == nil && wh[0] > 0 && wh[1] > 0 {
			return wh[0], wh[1]
		}
	}
	return 1920, 1080
}

// padButtons: the overlay combo is the game's own business here (its touch menu button), the launcher reads no pad.
func padButtons() uint16 { return 0 }

// hotkey: no global keyboard shortcuts on Android.
type hotkey struct{}

func (h *hotkey) start(fn func(id int)) {}
func (h *hotkey) stop()                 {}

// [voice] voice chat is not built for Android yet (voice_stub.go): the push-to-talk and microphone hooks are inert.
const pttKeyName = ""

func pttDown() bool { return false }
func micAccess() int {
	s, _ := platform("micAccess", nil)
	n, _ := strconv.Atoi(s)
	return n
}
func micRequest(done func(bool)) { go done(false) }

// The session key: the app's private storage is already readable by this app only (the system's sandbox), which is
// what the Keychain / DPAPI give the desktop launchers.
func protect(b []byte) ([]byte, error)   { return b, nil }
func unprotect(b []byte) ([]byte, error) { return b, nil }

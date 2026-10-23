package main

import (
	"context"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/menu"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// While the game runs the launcher waits in the taskbar; Windows gives the focus back to the game.
func (a *App) toBackground() { wruntime.WindowMinimise(a.ctx) }

// toForeground: restored first, then shown. Shown while still minimized, the window got the focus before WebView2 was
// visible again; its Focus failed and go-webview2's error handler ends the process (a match arriving while the
// launcher waited minimized closed it).
func (a *App) toForeground() {
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

func launcherInFront() bool { return inFront(ownWindow()) }
func launcherToFront()      { toFront(ownWindow()) }

func openFolder(ctx context.Context, dir string) {
	wruntime.BrowserOpenURL(ctx, "file:///"+filepath.ToSlash(dir))
}

func appMenu() *menu.Menu { return nil }

package main

import (
	"context"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/menu"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// While the game runs the launcher waits in the taskbar; Windows gives the focus back to the game.
func (a *App) toBackground() { wruntime.WindowMinimise(a.ctx) }

func (a *App) toForeground() {
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
}

func launcherInFront() bool { return inFront(ownWindow()) }
func launcherToFront()      { toFront(ownWindow()) }

func openFolder(ctx context.Context, dir string) {
	wruntime.BrowserOpenURL(ctx, "file:///"+filepath.ToSlash(dir))
}

func appMenu() *menu.Menu { return nil }

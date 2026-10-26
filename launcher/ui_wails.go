//go:build !android

package main

// The window and system services the launcher uses, through Wails (Windows, macOS). Android has its own (ui_android.go).

import (
	"context"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func uiEmit(ctx context.Context, name string, data any) { wruntime.EventsEmit(ctx, name, data) }

func uiOnFileDrop(ctx context.Context, fn func(paths []string)) {
	wruntime.OnFileDrop(ctx, func(x, y int, paths []string) { fn(paths) })
}

func uiShow(ctx context.Context)              { wruntime.WindowShow(ctx) }
func uiLog(ctx context.Context, msg string)   { wruntime.LogInfo(ctx, msg) }
func uiOpenURL(ctx context.Context, u string) { wruntime.BrowserOpenURL(ctx, u) }
func uiQuit(ctx context.Context)              { wruntime.Quit(ctx) }
func uiAlwaysOnTop(ctx context.Context, on bool) {
	wruntime.WindowSetAlwaysOnTop(ctx, on)
}
func uiMinimised(ctx context.Context) bool { return wruntime.WindowIsMinimised(ctx) }

func uiOpenISO(ctx context.Context, title string) (string, error) {
	return wruntime.OpenFileDialog(ctx, wruntime.OpenDialogOptions{Title: title,
		Filters: []wruntime.FileFilter{{DisplayName: "Imagen de disco PS2 (*.iso)", Pattern: "*.iso"}}})
}

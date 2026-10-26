//go:build android

package main

// The window and system services on Android: the app's activity does them (RecompBridge.platform in Java).

import "context"

func uiEmit(ctx context.Context, name string, data any) { emitAndroid(name, data) }

// No drag and drop on a phone.
func uiOnFileDrop(ctx context.Context, fn func(paths []string)) {}

func uiShow(ctx context.Context)              { _, _ = platform("show", nil) }
func uiLog(ctx context.Context, msg string)   { _, _ = platform("log", msg) }
func uiOpenURL(ctx context.Context, u string) { _, _ = platform("openUrl", u) }
func uiQuit(ctx context.Context)              { _, _ = platform("quit", nil) }

// The game is its own activity: "on top" is the launcher brought to the front (app_android.go).
func uiAlwaysOnTop(ctx context.Context, on bool) {}
func uiMinimised(ctx context.Context) bool       { return !launcherInFront() }

// uiOpenISO: the system's document picker; the answer is the document's content:// URI, kept readable across restarts
// (a persisted grant), which isoOpen turns into a file descriptor.
func uiOpenISO(ctx context.Context, title string) (string, error) { return platform("pickIso", title) }

package main

// [update] The launcher updates itself. recomp-online's releases carry recomp-launcher.json:
//
//	{"version": "3.1", "files": {"Windows": {"file": "Recomp.exe", "sha256": "...", "size": 123},
//	                             "macOS": {"file": "DBZ_BT4_Mac.zip", "sha256": "...", "size": 456, "version": "3.0"}}}
//
// A file's own "version" (when the release was built on another system and keeps this system's previous file) wins
// over the manifest's: without it a Mac would take the 3.0 app for 3.1 and download it again at every check.
// At start (and every 6 hours while it runs) the launcher looks at the latest release; a newer version is
// downloaded beside the running one, checked (SHA-256), and put in its place (updater_windows.go: the running exe is
// renamed, which Windows allows, and the new one takes its name; updater_darwin.go: the whole .app is swapped). The
// next start is the new version; the interface offers to restart now when no game is open.

import (
	"errors"
	"os"
	"strings"
	"time"
)

const launcherRepo = "recomp-code/recomp-online"
const launcherManifest = "recomp-launcher.json"

type launcherManifestT struct {
	Version string              `json:"version"`
	Files   map[string]packFile `json:"files"`
}

// executable: the running launcher (tests point it at a fake app).
var executable = os.Executable

// pendingUpdate: a newer launcher already in place, waiting for a restart ("" = none).
var pendingUpdate string

func (a *App) updateLoop() {
	if os.Getenv("RECOMP_NO_UPDATE") == "1" {
		return
	}
	cleanupOldExe()
	for {
		if err := a.checkLauncherUpdate(); err != nil && a.ctx != nil {
			a.emit("launcher_update", map[string]any{"error": err.Error()})
		}
		a.checkGameUpdates()
		time.Sleep(6 * time.Hour)
	}
}

func (a *App) checkLauncherUpdate() error {
	if pendingUpdate != "" {
		return nil
	}
	rel, err := latestRelease(launcherRepo)
	if err != nil {
		return nil // offline or no release yet: nothing to say
	}
	var murl string
	for _, as := range rel.Assets {
		if as.Name == launcherManifest {
			murl = as.URL
		}
	}
	if murl == "" {
		return nil
	}
	var m launcherManifestT
	if err := getJSON(murl, &m); err != nil {
		return nil
	}
	pf, ok := m.Files[platformName]
	if !ok {
		return nil
	}
	if pf.Version != "" { // this system's file is from another version than the release's (see packFile)
		m.Version = pf.Version
	}
	if !newerVersion(m.Version, launcherVersion) {
		return nil
	}
	var furl string
	for _, as := range rel.Assets {
		if as.Name == pf.File {
			furl = as.URL
		}
	}
	if furl == "" {
		return errors.New("la actualización de Recomp no está bien publicada")
	}
	if !canSelfUpdate { // the interface links to the release
		a.emit("launcher_update", map[string]any{"version": m.Version, "phase": "manual"})
		return nil
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	a.emit("launcher_update", map[string]any{"version": m.Version, "phase": "download"})
	next := updateDownloadPath(exe, pf.File)
	if err := download(furl, next, pf, nil); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := replaceExe(exe, next); err != nil {
		_ = os.Remove(next)
		return err
	}
	pendingUpdate = m.Version
	a.emit("launcher_update", map[string]any{"version": m.Version, "phase": "ready"})
	return nil
}

// RestartForUpdate: the interface's "Reiniciar" once an update is in place (not while a game runs).
func (a *App) RestartForUpdate() string {
	if pendingUpdate == "" {
		return ""
	}
	if a.game.running() {
		return "cierra el juego antes de reiniciar Recomp"
	}
	if err := relaunch(); err != nil {
		return err.Error()
	}
	a.Quit()
	return ""
}

// LauncherUpdate: the version waiting for a restart ("" = none), for the interface when it loads.
func (a *App) LauncherUpdate() string { return pendingUpdate }

func trimVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

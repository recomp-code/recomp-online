//go:build android

package main

// Android: a newer launcher is a new APK (recomp-launcher.json's "Android" file). It is downloaded and checked like the
// desktop ones; "Reiniciar" hands it to the system's package installer, which asks the player to confirm (an app can
// not replace itself silently).

import (
	"os"
	"path/filepath"
)

const canSelfUpdate = true

var downloadedAPK string

func updateDownloadPath(exe, file string) string {
	d := filepath.Join(launcherHome(""), "update")
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, file)
}

func replaceExe(exe, next string) error {
	downloadedAPK = next
	return nil
}

// cleanupOldExe: an APK already installed is not needed any more.
func cleanupOldExe() {
	d := filepath.Join(launcherHome(""), "update")
	if ents, err := os.ReadDir(d); err == nil {
		for _, e := range ents {
			_ = os.Remove(filepath.Join(d, e.Name()))
		}
	}
}

func relaunch() error {
	_, err := platform("installApk", downloadedAPK)
	return err
}

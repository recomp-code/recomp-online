package main

// [update] macOS: the launcher is part of an app bundle (DBZ_BT4.app: the launcher, the GS host and the game), so an
// update replaces the whole bundle. recomp-launcher.json's "macOS" entry is the app zipped (DBZ_BT4_Mac.zip, made by
// dbz-bt4-recomp's macos/make_dist.sh). It is downloaded into Application Support (the app itself may be read-only),
// checked (SHA-256), unpacked beside the installed app, its signature verified, and swapped in: the running app is
// renamed aside (the running process keeps its files) and the new one takes its name. The player's files live in
// Application Support and stay as they are.
//
// An app that macOS runs from a translocated copy (opened from Downloads without being moved: the path has
// /AppTranslocation/) cannot be replaced; the player is asked to move it to Applications.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const canSelfUpdate = true

// appBundle: the .app the running launcher belongs to (Contents/MacOS/<exe> -> the .app).
func appBundle(exe string) (string, error) {
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(app, ".app") || filepath.Base(filepath.Dir(exe)) != "MacOS" {
		return "", errors.New("Recomp no está dentro de una app")
	}
	return app, nil
}

// updateDownloadPath: the zip goes to the launcher's own folder, never into the app.
func updateDownloadPath(exe, file string) string {
	dir := filepath.Join(launcherHome(""), "update")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, filepath.Base(file))
}

// replaceExe: unpacks the downloaded app next to the installed one and swaps them.
func replaceExe(exe, zipPath string) error {
	defer os.Remove(zipPath)
	app, err := appBundle(exe)
	if err != nil {
		return err
	}
	if strings.Contains(app, "/AppTranslocation/") {
		return errors.New("hay una versión nueva de Recomp: mueve " + filepath.Base(app) +
			" a Aplicaciones (o a cualquier carpeta) y ábrela desde ahí para que se actualice sola")
	}
	parent := filepath.Dir(app)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(app)+".update-")
	if err != nil {
		return fmt.Errorf("no puedo escribir junto a %s para actualizarla: %v", filepath.Base(app), err)
	}
	defer os.RemoveAll(stage)
	// ditto keeps the bundle as it was zipped (symlinks, permissions, signature)
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", zipPath, stage).CombinedOutput(); err != nil {
		return fmt.Errorf("no pude abrir la actualización: %v %s", err, strings.TrimSpace(string(out)))
	}
	next := filepath.Join(stage, filepath.Base(app))
	if !fileExists(next) {
		apps, _ := filepath.Glob(filepath.Join(stage, "*.app"))
		if len(apps) != 1 {
			return errors.New("la actualización no trae la app")
		}
		next = apps[0]
	}
	_ = exec.Command("/usr/bin/xattr", "-dr", "com.apple.quarantine", next).Run()
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", next).CombinedOutput(); err != nil {
		return fmt.Errorf("la actualización no pasa la comprobación de firma: %s", strings.TrimSpace(string(out)))
	}
	old := filepath.Join(parent, fmt.Sprintf(".%s.old%d", filepath.Base(app), time.Now().Unix()))
	if err := os.Rename(app, old); err != nil {
		return fmt.Errorf("no pude preparar la actualización: %v", err)
	}
	if err := os.Rename(next, app); err != nil {
		_ = os.Rename(old, app)
		return fmt.Errorf("no pude instalar la actualización: %v", err)
	}
	return nil
}

// cleanupOldExe: the previous app, moved aside by the last update (at the next start, when it no longer runs).
func cleanupOldExe() {
	exe, err := executable()
	if err != nil {
		return
	}
	app, err := appBundle(exe)
	if err != nil {
		return
	}
	// and what an interrupted update left behind
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(app), "."+filepath.Base(app)+".old*"))
	stages, _ := filepath.Glob(filepath.Join(filepath.Dir(app), "."+filepath.Base(app)+".update-*"))
	for _, m := range append(old, stages...) {
		_ = os.RemoveAll(m)
	}
}

// relaunch opens the (new) app as a new instance, with the same arguments.
func relaunch() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	app, err := appBundle(exe)
	if err != nil {
		return err
	}
	args := []string{"-n", app}
	if len(os.Args) > 1 {
		args = append(append(args, "--args"), os.Args[1:]...)
	}
	return exec.Command("/usr/bin/open", args...).Start()
}

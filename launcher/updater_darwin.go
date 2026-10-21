package main

import "errors"

// TODO(mac): a macOS update replaces the whole .app bundle (signed, possibly translocated): until that is done the
// launcher only says a newer version exists (checkLauncherUpdate stops at replaceExe's error).
func replaceExe(exe, next string) error {
	return errors.New("hay una versión nueva de Recomp: descárgala desde GitHub")
}

func relaunch() error { return errors.New("reinicia Recomp a mano") }

const canSelfUpdate = false

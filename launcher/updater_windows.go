package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// updateDownloadPath: the new exe is downloaded beside the running one.
func updateDownloadPath(exe, file string) string { return exe + ".new" }

// cleanupOldExe: the previous version, renamed aside by the last update.
func cleanupOldExe() {
	exe, err := executable()
	if err != nil {
		return
	}
	matches, _ := filepath.Glob(exe + ".old*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// replaceExe: Windows lets a running exe be renamed, not overwritten: the running one moves aside (.old, removed at
// the next start) and the new one takes its name.
func replaceExe(exe, next string) error {
	old := fmt.Sprintf("%s.old%d", exe, time.Now().Unix())
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("no pude preparar la actualización: %v", err)
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("no pude instalar la actualización: %v", err)
	}
	return nil
}

// relaunch starts the (new) launcher with the same arguments.
func relaunch() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = os.Environ()
	return cmd.Start()
}

const canSelfUpdate = true

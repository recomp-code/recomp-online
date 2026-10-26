//go:build !android

package main

import (
	"os"
	"os/exec"
)

// startRunner starts the recompiled game as a child process; wait blocks until it exits and gives its exit code.
// (Android starts it as an Activity of its own: game_android.go.)
func (g *game) startRunner(elf, dir string, env []string) (*os.Process, func() int, error) {
	cmd := exec.Command(g.runner, elf)
	cmd.Dir, cmd.Env = dir, env
	cleanup := g.prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return cmd.Process, func() int {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		cleanup()
		return code
	}, nil
}

// The ISO is a plain file here (on Android it can be a document the system picker granted: game_android.go).
func isoOpen(path string) (*os.File, error)    { return os.Open(path) }
func isoStat(path string) (os.FileInfo, error) { return os.Stat(path) }

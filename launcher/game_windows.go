package main

// Windows: a game lives in its folder (<launcher>\games\<id>, or the launcher's own folder for an old DBZ BT4 zip);
// the GS host draws with Vulkan (DX12 for DLSS 5) and can use the player's DLSS DLLs (dlss_windows.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const platformName = "Windows"

const fullscreenKey = "Alt+Enter"

// cfgName: the settings file in the game's folder (DBZ keeps the v1.x name, so an update keeps its settings).
func (g *game) cfgName() string {
	if g.p.ID == dbzProfile.ID {
		return "DBZ_BT4.cfg"
	}
	return "settings.cfg"
}

func (g *game) setPaths() {
	g.home, g.res = g.dir, g.dir
	g.runtimeDir, g.gshostDir = filepath.Join(g.dir, "runtime"), filepath.Join(g.dir, "gshost")
	g.cfgPath, g.dlssDir, g.logs = filepath.Join(g.dir, g.cfgName()), filepath.Join(g.dir, "DLSS"), filepath.Join(g.dir, "logs")
	g.runner, g.sites = filepath.Join(g.runtimeDir, "ps2EntryRunner.exe"), filepath.Join(g.runtimeDir, "fps60_sites.txt")
}

// gameDir: where a game lives: games\<id>. An old DBZ BT4 zip has runtime\ next to the launcher: it is played from
// there until the first update installs games\dbzbt4 (which keeps its settings, saves and ISO files).
func gameDir(launcherRoot string, p *profile) string {
	dir := filepath.Join(gamesRoot(launcherRoot), p.ID)
	if p.ID == dbzProfile.ID && !fileExists(filepath.Join(dir, "runtime", "ps2EntryRunner.exe")) &&
		fileExists(filepath.Join(launcherRoot, "runtime", "ps2EntryRunner.exe")) {
		return launcherRoot
	}
	return dir
}

// launcherHome: where the launcher keeps its own files (the account, online.json).
func launcherHome(launcherRoot string) string { return launcherRoot }

func (g *game) defaultQuality() int {
	qs := g.p.qualities()
	rtx := strings.Contains(strings.ToUpper(gpuNames()), "RTX")
	for i, q := range qs {
		if rtx && q.DLSS5 && g.dlss5Parts() != nil {
			return i
		}
	}
	for i, q := range qs {
		if rtx && strings.Contains(q.Ini, "dlss") && !q.DLSS5 {
			return i
		}
	}
	if len(qs) > 1 && !rtx {
		return 1 // the second preset is the lighter one
	}
	return 0
}

// hostSetup: the GS host's ini for the chosen quality (its path, read back when the game closes), the host's arguments
// and the variables only Windows uses.
func (g *game) hostSetup(s Settings) (string, string, []string, error) {
	// the DLSS the player chose (DLSS\ or the bundled one) goes next to the host
	if g.p.DLSSFolder {
		if sr := g.superResDLL(); sr != "" && !strings.EqualFold(filepath.Clean(sr), filepath.Clean(filepath.Join(g.gshostDir, "nvngx_dlss.dll"))) {
			_ = copyIfChanged(sr, filepath.Join(g.gshostDir, "nvngx_dlss.dll"))
		}
	}
	qs := g.p.qualities()
	q, hostDir := s.Quality, g.gshostDir
	if q < 0 || q >= len(qs) {
		q = 0
	}
	if qs[q].DLSS5 {
		if d, err := g.prepareDLSS5Host(); err == nil {
			hostDir = d
		} else {
			for i, p := range qs { // fall back to the plain DLSS preset
				if strings.Contains(p.Ini, "dlss") && !p.DLSS5 {
					q = i
				}
			}
		}
	}
	ini := g.hostIni(s, q)
	if !fileExists(filepath.Join(hostDir, ini)) {
		_ = copyIfChanged(filepath.Join(g.gshostDir, "presets", ini), filepath.Join(hostDir, ini))
	}
	renderer := "-renderer " + qs[q].Renderer
	env := []string{"PS2X_LOGFILE=" + filepath.Join(g.logs, "juego.log")}
	if hostDir != g.gshostDir || g.p.ID != dbzProfile.ID {
		env = append(env, "PS2X_GSHOST="+filepath.Join(hostDir, "pcsx2-gsrunner.exe"))
	}
	return filepath.Join(hostDir, ini), renderer + " -ini " + ini, env, nil
}

// prepareCmd: DBZ's runtime writes its own log (PS2X_LOGFILE); PS2Recomp's (BR4) writes to its standard output and
// error, which go to logs\juego.log (the previous one stays as juego-anterior.log).
func (g *game) prepareCmd(cmd *exec.Cmd) func() {
	if g.p.ID == dbzProfile.ID {
		return func() {}
	}
	log := filepath.Join(g.logs, "juego.log")
	_ = os.Rename(log, filepath.Join(g.logs, "juego-anterior.log"))
	f, err := os.Create(log)
	if err != nil {
		return func() {}
	}
	cmd.Stdout, cmd.Stderr = f, f
	return func() { f.Close() }
}

// dataFolder: what "Abrir carpeta" opens (the DLSS folder on Windows).
func (g *game) dataFolder() string { return g.dlssDir }

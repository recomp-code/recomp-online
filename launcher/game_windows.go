package main

// Windows: a game lives in its folder (<launcher>\games\<id>, or the launcher's own folder for an old DBZ BT4 zip);
// the GS host (BR4, NBA) draws with Vulkan and DBZ's native renderer runs itself; both can use the player's DLSS DLL
// (dlss_windows.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const platformName = "Windows"

// runnerInPack: the runner ships in a separately downloaded game pack here, so online gates on the pack version.
const runnerInPack = true

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

// gamesRoot: the games go next to the launcher (the zip's folder is the player's).
func gamesRoot(launcherRoot string) string { return filepath.Join(launcherRoot, "games") }

// gameDir: where a game lives: games\<id>. An old DBZ BT4 zip has runtime\ next to the launcher: it is played from
// there until the first update installs games\dbzbt4 (which keeps its settings, saves and ISO files).
// inApp: no game comes inside the launcher on Windows (an old DBZ zip next to it is updated into games\\<id>).
func (g *game) inApp() bool { return false }

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
	for i, q := range qs { // the runtime's own renderer wherever the game has it (DBZ BT4: its only quality)
		if q.Renderer == "native" {
			return i
		}
	}
	rtx := strings.Contains(strings.ToUpper(gpuNames()), "RTX")
	for i, q := range qs {
		if rtx && strings.Contains(q.Ini, "dlss") {
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
	q, hostDir := g.hostQuality(s), g.gshostDir
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

// nativeSetup: the runtime's own renderer runs DLSS itself, so the DLSS the player chose goes next to the runtime too.
func (g *game) nativeSetup() []string {
	if g.p.DLSSFolder {
		if sr := g.superResDLL(); sr != "" {
			_ = copyIfChanged(sr, filepath.Join(g.runtimeDir, "nvngx_dlss.dll"))
		}
	}
	env := []string{"PS2X_LOGFILE=" + filepath.Join(g.logs, "juego.log")}
	// the window is Vulkan's alone and presented through the runtime's swapchain (GL draws off screen on a hidden window):
	// from pack 2.4.1, whose runtime does that. Older runtimes presented through Vulkan on a window GL also drew into, and
	// a fullscreen GL window that the driver scans out directly hid the swapchain: a black screen.
	if v := g.packVersion(); v != "" && !newerVersion(vulkanWindowPack, v) {
		env = append(env, "PS2X_SEAMVK_VKPRESENT=1")
	}
	return env
}

// nativeScale: the native renderer's resolution. From pack 2.5.2 the runtime picks it from the size the game is shown at
// ("auto": ~75% of the output's height under DLSS / FSR 2, 3x on a 1440p screen); older runtimes read "auto" as 1x, so
// they keep the fixed 2x.
func (g *game) nativeScale() string {
	if v := g.packVersion(); v != "" && !newerVersion(autoScalePack, v) {
		return "auto"
	}
	return "2"
}

const autoScalePack = "2.5.2"

// vulkanWindowPack: the first DBZ BT4 pack whose window is Vulkan's
const vulkanWindowPack = "2.4.1"

// nativeFrameGen: the "more fps on screen" option on the native renderer: a frame generated from the game's real motion
// between every two
var nativeFrameGen = []string{"PS2X_SEAMVK_FRAMEGEN=1"}

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

// nativeUpscale: the native renderer's upscaler: DLSS on NVIDIA RTX, FSR 2 wherever DLSS cannot run (the runtime decides)
const nativeUpscale = "dlss"

//go:build android

package main

// Android: the app carries the game (libmain.so, the recompiled runner, plus the runtime's assets in the APK); what the
// launcher and the game write goes to the app's private storage (androidRoot, Context.getFilesDir()):
//
//	files/res/runtime/   assets, fps60_sites.txt, settings.toml (extracted from the APK by the app for each version)
//	files/runtime/       data/ (from the ISO), savedata/ (memory card + settings), mods/, assets -> res's
//	files/DBZ_BT4.cfg, online.json, logs/
//
// The ISO stays where the player keeps it: the system's document picker grants this app access to it (a content://
// URI, persisted), and both the launcher (isoOpen) and the game read it through a file descriptor -- 5 GB are never
// copied. The game runs as the app's GameActivity in a process of its own (":game"): the runtime exits its process when
// the game ends, as on the desktop, and the launcher's process keeps the online session.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const platformName = "Android"

const fullscreenKey = ""

// androidRoot: the app's files folder, from goInit.
var androidRoot string

func launcherHome(launcherRoot string) string {
	if androidRoot == "" {
		androidRoot = launcherRoot
	}
	return androidRoot
}

// No downloadable games on Android yet: only DBZ BT4 (in the app). Other profiles have no Android quality, so the
// launcher already says they are not available here.
func gamesRoot(launcherRoot string) string { return filepath.Join(launcherHome(launcherRoot), "games") }

func gameDir(launcherRoot string, p *profile) string {
	if p.ID == dbzProfile.ID {
		return launcherHome(launcherRoot)
	}
	return filepath.Join(gamesRoot(launcherRoot), p.ID)
}

func (g *game) inApp() bool { return g.p.ID == dbzProfile.ID }

func (g *game) setPaths() {
	g.home = launcherHome(g.root)
	g.res = filepath.Join(g.home, "res")
	if g.p.ID != dbzProfile.ID {
		g.home, g.res = g.dir, g.dir
	}
	g.runtimeDir, g.gshostDir = filepath.Join(g.home, "runtime"), filepath.Join(g.home, "gshost")
	g.cfgPath, g.logs = filepath.Join(g.home, "DBZ_BT4.cfg"), filepath.Join(g.home, "logs")
	// the app's own library (GameActivity loads it): installed with the app, so the game always is
	g.runner = "libmain.so"
	if d, err := platform("nativeLibDir", nil); err == nil && d != "" {
		g.runner = filepath.Join(d, "libmain.so")
	}
	g.sites = filepath.Join(g.res, "runtime", "fps60_sites.txt")
}

func (g *game) importOldSettings() {}

// defaultQuality: "Alta" (twice the PS2's resolution + FSR 2) on a phone with 8 GB or more -- the flagships, where a
// 60 fps fight holds with room to spare (a POCO F8 Ultra: 7 ms of guest CPU per frame) -- and "Equilibrada" (the PS2's
// own resolution + FSR 2) below that.
func (g *game) defaultQuality() int {
	if memTotalGB() >= 7.5 {
		return 1
	}
	return 0
}

func memTotalGB() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseFloat(f[1], 64)
			return kb / (1024 * 1024)
		}
	}
	return 0
}

// setupRuntime: the writable runtime folder, as on macOS: data/, savedata/, mods/, and assets linked to res's copy.
func (g *game) setupRuntime() error {
	for _, d := range []string{g.runtimeDir, g.dataDir, filepath.Join(g.runtimeDir, "savedata"), filepath.Join(g.runtimeDir, "mods"), g.logs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	assets := filepath.Join(g.runtimeDir, "assets")
	_ = os.RemoveAll(assets)
	if err := os.Symlink(filepath.Join(g.res, "runtime", "assets"), assets); err != nil {
		return err
	}
	settings := filepath.Join(g.runtimeDir, "savedata", "settings.toml")
	if !fileExists(settings) {
		if err := copyWritable(filepath.Join(g.res, "runtime", "settings.toml"), settings); err != nil {
			return err
		}
	}
	return nil
}

func copyWritable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func (g *game) hostSetup(s Settings) (string, string, []string, error) {
	return "", "", nil, errors.New("en Android solo está el renderizador nativo")
}

// androidQuality: the runtime's render scale and upscaler for each Android quality (profiles.go, Qualities["Android"]).
var androidQuality = []struct{ scale, upscale string }{
	{"1", "fsr"}, // Equilibrada: the PS2's resolution, FSR 2 to the screen
	{"2", "fsr"}, // Alta: twice the PS2's resolution, FSR 2 to the screen
	{"1", "off"}, // Ahorro: the PS2's resolution, plain filtering (the least GPU and battery)
}

// nativeSetup: the runtime's folder, and this platform's choices. They come after the generic variables, and the last
// value of a variable wins (GameActivity applies them in order): the quality's scale and upscaler, and the whole screen
// (the runtime takes the display's own size when the window size is empty).
func (g *game) nativeSetup() []string {
	_ = g.setupRuntime()
	q := androidQuality[0]
	if i := g.settings().Quality; i >= 0 && i < len(androidQuality) {
		q = androidQuality[i]
	}
	return []string{"PS2X_EXEDIR=" + g.runtimeDir, "PS2X_SEAMVK_SCALE=" + q.scale, "PS2X_SEAMVK_UPSCALE=" + q.upscale,
		"PS2X_WINDOW_MODE=2", "PS2X_WINDOW_W=", "PS2X_WINDOW_H="}
}

const nativeUpscale = "fsr"

func (g *game) nativeScale() string { return "1" }

var nativeFrameGen []string

func (g *game) dataFolder() string { return g.home }

func gpuNames() string { s, _ := platform("device", nil); return s }

// startRunner: GameActivity in its own process, with the environment the desktop gives the runner (the ISO's
// content:// URI is opened there, as a file descriptor). The game reports its process id (game.pid) and its exit
// code (game.exit) beside its log; wait() follows the process until it is gone.
func (g *game) startRunner(elf, dir string, env []string) (*os.Process, func() int, error) {
	_ = os.MkdirAll(g.logs, 0o755)
	log := filepath.Join(g.logs, "juego.log")
	_ = os.Rename(log, filepath.Join(g.logs, "juego-anterior.log"))
	pidFile, exitFile := filepath.Join(g.logs, "game.pid"), filepath.Join(g.logs, "game.exit")
	_ = os.Remove(pidFile)
	_ = os.Remove(exitFile)
	env = append(env, "PS2X_ANDROID_ELF="+elf, "PS2X_ANDROID_CWD="+dir, "PS2X_ANDROID_LOG="+log,
		"PS2X_ANDROID_PIDFILE="+pidFile, "PS2X_ANDROID_EXITFILE="+exitFile)
	if _, err := platform("startGame", map[string]any{"env": env}); err != nil {
		return nil, nil, err
	}
	pid := 0
	for i := 0; i < 300 && pid == 0; i++ { // up to 30 s: the system may have to stop a previous game's process first
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if pid == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if pid == 0 {
		return nil, nil, errors.New("el juego no arrancó")
	}
	proc, _ := os.FindProcess(pid)
	return proc, func() int {
		for proc.Signal(syscall.Signal(0)) == nil {
			time.Sleep(300 * time.Millisecond)
		}
		if b, err := os.ReadFile(exitFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
		return 1 // gone without saying how: it crashed or the system closed it
	}, nil
}

// isoOpen: a content:// ISO is opened by the app (ContentResolver) and handed over as a file descriptor.
func isoOpen(path string) (*os.File, error) {
	if !strings.HasPrefix(path, "content://") {
		return os.Open(path)
	}
	s, err := platform("openFd", path)
	if err != nil {
		return nil, err
	}
	fd, err := strconv.Atoi(s)
	if err != nil || fd < 0 {
		return nil, errors.New("no pude abrir la ISO")
	}
	return os.NewFile(uintptr(fd), path), nil
}

func isoStat(path string) (os.FileInfo, error) {
	if !strings.HasPrefix(path, "content://") {
		fi, err := os.Stat(path)
		if err != nil {
			_, _ = platform("log", "ISO: "+err.Error())
		}
		return fi, err
	}
	f, err := isoOpen(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

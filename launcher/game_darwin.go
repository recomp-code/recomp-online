package main

// macOS: DBZ_BT4.app holds the game read-only (a downloaded app runs from a read-only, translocated copy), so what the
// launcher and the game write goes to ~/Library/Application Support/DBZ_BT4:
//
//	DBZ_BT4.app/Contents/MacOS/      DBZ_BT4 (this launcher), pcsx2-gsrunner (PCSX2's GS on Metal)
//	DBZ_BT4.app/Contents/Helpers/    Game.app: ps2EntryRunner, a UI element (the GS host's window is the Dock icon)
//	DBZ_BT4.app/Contents/Resources/  presets/ (one GS ini per quality), runtime/ (assets, fps60_sites.txt, settings.toml)
//	Application Support/DBZ_BT4/     DBZ_BT4.cfg, online.json, logs/, gshost/ (the presets in use), runtime/ (data/ from
//	                                 the ISO, savedata/, mods/, assets -> the app's)
//
// RECOMP_LAUNCHER_DATA replaces that folder (tests: two launchers on one Mac).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const platformName = "macOS"

// The quality presets are per game (profiles.go, Qualities["macOS"]).

const fullscreenKey = "Option+Return (o Ctrl+Cmd+F)"

// launcherHome: where the launcher keeps its own files (online.json): DBZ BT4's folder, as before.
func launcherHome(launcherRoot string) string {
	if d := os.Getenv("RECOMP_LAUNCHER_DATA"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "Application Support", "DBZ_BT4")
}

// gamesRoot: where downloaded games go. Not inside the app: a downloaded .app runs from a read-only, translocated
// copy, and an app update replaces the whole bundle. RECOMP_LAUNCHER_DATA (tests) moves it with the launcher's files.
func gamesRoot(launcherRoot string) string {
	if d := os.Getenv("RECOMP_LAUNCHER_DATA"); d != "" {
		return filepath.Join(d, "games")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "Application Support", "Recomp", "games")
}

// gameDir: DBZ BT4 is inside the app (Resources, Helpers); another game is a downloaded pack in gamesRoot/<id>, laid
// out like the Windows pack (runtime/ with ps2EntryRunner, gshost/ with the presets; see setPaths).
func gameDir(launcherRoot string, p *profile) string {
	if p.ID == dbzProfile.ID {
		return launcherRoot
	}
	return filepath.Join(gamesRoot(launcherRoot), p.ID)
}

// inApp: DBZ BT4 comes inside the app and is updated with it (updater.go), never from its repo.
func (g *game) inApp() bool { return g.p.ID == dbzProfile.ID }

func (g *game) setPaths() {
	if g.p.ID != dbzProfile.ID {
		// a downloaded game: its folder exists once it is installed (installer.go), as on Windows
		g.home, g.res = g.dir, g.dir
		g.runtimeDir, g.gshostDir = filepath.Join(g.dir, "runtime"), filepath.Join(g.dir, "gshost")
		g.cfgPath, g.logs = filepath.Join(g.dir, "settings.cfg"), filepath.Join(g.dir, "logs")
		g.runner, g.sites = filepath.Join(g.runtimeDir, "ps2EntryRunner"), filepath.Join(g.runtimeDir, "fps60_sites.txt")
		return
	}
	contents := filepath.Dir(g.root) // Contents/MacOS -> Contents
	g.res = filepath.Join(contents, "Resources")
	g.home = launcherHome(g.root)
	_ = os.MkdirAll(g.home, 0o755)
	g.runtimeDir, g.gshostDir = filepath.Join(g.home, "runtime"), filepath.Join(g.home, "gshost")
	g.cfgPath, g.logs = filepath.Join(g.home, "DBZ_BT4.cfg"), filepath.Join(g.home, "logs")
	g.runner = filepath.Join(contents, "Helpers", "Game.app", "Contents", "MacOS", "ps2EntryRunner")
	g.sites = filepath.Join(g.res, "runtime", "fps60_sites.txt")
	g.importOldSettings()
}

// importOldSettings: the first Mac launcher (v1.x, Swift) kept its settings in the user defaults; an update keeps them.
func (g *game) importOldSettings() {
	if fileExists(g.cfgPath) {
		return
	}
	read := func(key string) (string, bool) {
		out, err := exec.Command("/usr/bin/defaults", "read", "io.github.recomp-code.dbz-bt4", key).Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	iso, ok := read("iso")
	if !ok {
		return
	}
	s := "iso=" + iso + "\n"
	if q, ok := read("quality"); ok {
		if n, err := strconv.Atoi(q); err == nil && n >= 0 && n < len(g.p.qualities()) {
			s += "quality=" + q + "\n"
		}
	}
	for _, k := range []string{"fps60", "fullscreen"} {
		if v, ok := read(k); ok {
			s += k + "=" + b01(v == "1") + "\n"
		}
	}
	_ = os.WriteFile(g.cfgPath, []byte(s), 0o644)
}

// defaultQuality by chip: 6x on Max/Ultra, 4x on Pro, plain 3x on the rest (their GPU is the smallest; MetalFX costs
// nearly twice the GPU of 3x).
func (g *game) defaultQuality() int {
	chip := gpuNames()
	switch {
	case strings.Contains(chip, "Max") || strings.Contains(chip, "Ultra"):
		return 0
	case strings.Contains(chip, "Pro"):
		return 1
	}
	return 2
}

// setupRuntime: the writable runtime folder: data/ (from the ISO), savedata/ (memory card + settings), mods/, and
// assets/ linked to the app's copy (linked again on every start: the app may have moved).
func (g *game) setupRuntime() error {
	for _, d := range []string{g.runtimeDir, g.dataDir, filepath.Join(g.runtimeDir, "savedata"), filepath.Join(g.runtimeDir, "mods"), g.gshostDir, g.logs} {
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

// touchISO reads the start of the ISO from the launcher. macOS asks each app once for its Downloads, Documents or
// Desktop folder (and again after an update of an ad-hoc signed app); the game runs as part of the launcher, so the
// question would otherwise come at the game's first disc read, with the game stuck at frame 0 -- online, long enough
// for the other player to give up. Asked here, it shows while the launcher is in front, before the match connects.
func touchISO(path string) {
	if f, err := os.Open(path); err == nil {
		b := make([]byte, 2048)
		_, _ = f.ReadAt(b, 16*2048) // the ISO 9660 volume descriptor
		f.Close()
	}
}

// copyWritable copies the bytes, not the permissions: the app's own files may be read-only.
func copyWritable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func (g *game) hostSetup(s Settings) (string, string, []string, error) {
	if err := g.setupRuntime(); err != nil {
		return "", "", nil, err
	}
	touchISO(s.ISO)
	name := g.hostIni(s, g.hostQuality(s))
	ini := filepath.Join(g.gshostDir, name)
	if !fileExists(ini) {
		if err := copyWritable(filepath.Join(g.res, "presets", name), ini); err != nil {
			return "", "", nil, err
		}
	}
	env := []string{"PS2X_EXEDIR=" + g.runtimeDir, "PS2X_GSHOST=" + filepath.Join(g.root, "pcsx2-gsrunner")}
	// the ini's full path, quoted: Application Support has a space
	return ini, `-ini "` + ini + `"`, env, nil
}

// nativeSetup: the runtime's own renderer (no GS host); the Mac runtime still needs its folders and data in place, and
// its Vulkan is the MoltenVK the app carries (Contents/Frameworks), opened by path.
func (g *game) nativeSetup() []string {
	_ = g.setupRuntime()
	env := []string{"PS2X_EXEDIR=" + g.runtimeDir}
	if mvk := filepath.Join(filepath.Dir(g.res), "Frameworks", "libMoltenVK.dylib"); fileExists(mvk) {
		env = append(env, "GRANITE_VULKAN_LIBRARY="+mvk)
	}
	return env
}

// nativeUpscale: the native renderer's upscaler on a Mac is Apple's MetalFX (the runtime builds no FSR2 or DLSS there)
const nativeUpscale = "metalfx"

// nativeFrameGen: frame generation is not on macOS yet (the window takes the frame through an IOSurface there)
var nativeFrameGen []string

// prepareCmd: the runtime and the GS host write to logs/juego.log (their standard output and error). The previous
// game's log stays as juego-anterior.log: after an online match that went wrong the player often starts another game
// before anyone looks at it.
func (g *game) prepareCmd(cmd *exec.Cmd) func() {
	log := filepath.Join(g.logs, "juego.log")
	_ = os.Rename(log, filepath.Join(g.logs, "juego-anterior.log"))
	f, err := os.Create(log)
	if err != nil {
		return func() {}
	}
	cmd.Stdout, cmd.Stderr = f, f
	return func() { f.Close() }
}

// dataFolder: what "Abrir carpeta" opens: saves, settings and logs.
func (g *game) dataFolder() string { return g.home }

func gpuNames() string {
	s, _ := unix.Sysctl("machdep.cpu.brand_string")
	return s
}

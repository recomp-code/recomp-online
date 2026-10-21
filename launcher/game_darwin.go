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

var qualityIni = []string{"dbz.ini", "dbz_4x.ini", "dbz_3x.ini", "dbz_metalfx.ini"}

var qualityOptions = []QualityOption{
	{"Máxima (6x)", "Para M Pro, Max o Ultra."},
	{"Alta (4x)", "Un buen punto medio en un M Pro."},
	{"Equilibrada (3x)", "Para cualquier Mac con chip M."},
	{"Apple MetalFX (3x reescalado)", "Reescala a tu pantalla con MetalFX. Ligero en cualquier Mac."},
}

const fullscreenKey = "Option+Return (o Ctrl+Cmd+F)"

func (g *game) setPaths() {
	contents := filepath.Dir(g.root) // Contents/MacOS -> Contents
	g.res = filepath.Join(contents, "Resources")
	g.home = os.Getenv("RECOMP_LAUNCHER_DATA")
	if g.home == "" {
		h, _ := os.UserHomeDir()
		g.home = filepath.Join(h, "Library", "Application Support", "DBZ_BT4")
	}
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
		if n, err := strconv.Atoi(q); err == nil && n >= 0 && n < len(qualityIni) {
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

// defaultQuality by chip: 6x on Max/Ultra, 4x on Pro, MetalFX on the rest.
func (g *game) defaultQuality() int {
	chip := gpuNames()
	switch {
	case strings.Contains(chip, "Max") || strings.Contains(chip, "Ultra"):
		return 0
	case strings.Contains(chip, "Pro"):
		return 1
	}
	return 3
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
	name := qualityIni[s.Quality]
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

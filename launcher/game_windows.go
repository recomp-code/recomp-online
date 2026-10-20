package main

// Windows: everything lives in the zip's folder; the GS host draws with Vulkan (DX12 for DLSS 5) and can use the
// player's DLSS DLLs (dlss_windows.go).

import (
	"os/exec"
	"path/filepath"
	"strings"
)

const platformName = "Windows"

var qualityIni = []string{"dbz.ini", "dbz_3x.ini", "dbz_dlss.ini", "dbz_dlss_dx12.ini"}

const (
	qualityMax, quality3x, qualityDLSS, qualityDLSS5 = 0, 1, 2, 3
)

// qualityOptions: the Calidad menu, in qualityIni's order.
var qualityOptions = []QualityOption{
	{"Máxima (6x)", "Para gráficas potentes (RTX 3070, RX 6800 o mejor)."},
	{"Equilibrada (3x)", "Un buen punto medio."},
	{"DLSS / FSR (2x reescalado)", "Reescala a tu pantalla: DLSS en NVIDIA RTX, FSR en las demás."},
	{"DLSS 5 (experimental)", "DLSS con el pase neural de NVIDIA. Necesita una RTX y DLSS 5 Swapper."},
}

const fullscreenKey = "Alt+Enter"

func (g *game) setPaths() {
	g.home, g.res = g.root, g.root
	g.runtimeDir, g.gshostDir = filepath.Join(g.root, "runtime"), filepath.Join(g.root, "gshost")
	g.cfgPath, g.dlssDir, g.logs = filepath.Join(g.root, "DBZ_BT4.cfg"), filepath.Join(g.root, "DLSS"), filepath.Join(g.root, "logs")
	g.runner, g.sites = filepath.Join(g.runtimeDir, "ps2EntryRunner.exe"), filepath.Join(g.runtimeDir, "fps60_sites.txt")
}

func (g *game) defaultQuality() int {
	if strings.Contains(strings.ToUpper(gpuNames()), "RTX") {
		if g.dlss5Parts() != nil {
			return qualityDLSS5
		}
		return qualityDLSS
	}
	return quality3x
}

// hostSetup: the GS host's ini for the chosen quality (its path, read back when the game closes), the host's arguments
// and the variables only Windows uses.
func (g *game) hostSetup(s Settings) (string, string, []string, error) {
	// the DLSS the player chose (DLSS\ or the bundled one) goes next to the host
	if sr := g.superResDLL(); !strings.EqualFold(filepath.Clean(sr), filepath.Clean(filepath.Join(g.gshostDir, "nvngx_dlss.dll"))) {
		_ = copyIfChanged(sr, filepath.Join(g.gshostDir, "nvngx_dlss.dll"))
	}
	q, hostDir := s.Quality, g.gshostDir
	if q == qualityDLSS5 {
		if d, err := g.prepareDLSS5Host(); err == nil {
			hostDir = d
		} else {
			q = qualityDLSS
		}
	}
	ini := qualityIni[q]
	if !fileExists(filepath.Join(hostDir, ini)) {
		_ = copyIfChanged(filepath.Join(g.gshostDir, "presets", ini), filepath.Join(hostDir, ini))
	}
	renderer := "-renderer vulkan"
	if q == qualityDLSS5 {
		renderer = "-renderer dx12"
	}
	env := []string{"PS2X_LOGFILE=" + filepath.Join(g.logs, "juego.log")}
	if hostDir != g.gshostDir {
		env = append(env, "PS2X_GSHOST="+filepath.Join(hostDir, "pcsx2-gsrunner.exe"))
	}
	return filepath.Join(hostDir, ini), renderer + " -ini " + ini, env, nil
}

// prepareCmd: the runtime writes its own log (PS2X_LOGFILE) on Windows.
func (g *game) prepareCmd(cmd *exec.Cmd) func() { return func() {} }

// dataFolder: what "Abrir carpeta" opens (the DLSS folder on Windows).
func (g *game) dataFolder() string { return g.dlssDir }

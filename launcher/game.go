package main

// The game side of the launcher for DBZ Budokai Tenkaichi 4 (Team BT4's mod of BT3, Beta 14 Rev2): the player's ISO
// (checked and the boot files copied out of it), the quality presets, the player's own DLSS DLLs, DLSS 5 and
// starting ps2EntryRunner.exe -- offline, or online with the match the server handed out.

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gameID        = "dbzbt4"
	gameTitle     = "Dragon Ball Z Budokai Tenkaichi 4"
	gameElf       = "SLUS_219.78"
	gameElfSha256 = "224525C5639C00E01C2604C4685D307BC739C041000DCF8C69A4F884F465E8A4"
	gameSerial    = "SLUS-21978"
)

var qualityIni = []string{"dbz.ini", "dbz_3x.ini", "dbz_dlss.ini", "dbz_dlss_dx12.ini"}

const (
	qualityMax, quality3x, qualityDLSS, qualityDLSS5 = 0, 1, 2, 3
)

type Settings struct {
	ISO        string `json:"iso"`
	Quality    int    `json:"quality"`
	Fps60      bool   `json:"fps60"`
	Fullscreen bool   `json:"fullscreen"`
	NetDelay   int    `json:"netDelay"` // online input delay in frames, 0 = automatic
}

type game struct {
	root, runtimeDir, dataDir, gshostDir, cfgPath, dlssDir, logs string
	mu                                                           sync.Mutex
	cfg                                                          Settings
	proc                                                         *os.Process
}

func newGame(root string) *game {
	g := &game{root: root, runtimeDir: filepath.Join(root, "runtime"), gshostDir: filepath.Join(root, "gshost"),
		cfgPath: filepath.Join(root, "DBZ_BT4.cfg"), dlssDir: filepath.Join(root, "DLSS"), logs: filepath.Join(root, "logs")}
	g.dataDir = filepath.Join(g.runtimeDir, "data")
	g.load()
	return g
}

// ---- settings (same key=value file as the v1.x launcher, so an update keeps them) ----

func (g *game) load() {
	g.cfg = Settings{Quality: g.defaultQuality(), Fps60: true, Fullscreen: true}
	f, err := os.Open(g.cfgPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimPrefix(sc.Text(), string(rune(0xFEFF))), "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "iso":
			g.cfg.ISO = v
		case "quality":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(qualityIni) {
				g.cfg.Quality = n
			}
		case "fps60":
			g.cfg.Fps60 = v == "1"
		case "fullscreen":
			g.cfg.Fullscreen = v == "1"
		case "netdelay":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 10 {
				g.cfg.NetDelay = n
			}
		}
	}
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (g *game) save() error {
	s := fmt.Sprintf("iso=%s\nquality=%d\nfps60=%s\nfullscreen=%s\nnetdelay=%d\n", g.cfg.ISO, g.cfg.Quality, b01(g.cfg.Fps60), b01(g.cfg.Fullscreen), g.cfg.NetDelay)
	return os.WriteFile(g.cfgPath, []byte(s), 0o644)
}

func (g *game) settings() Settings {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg
}

func (g *game) setSettings(s Settings) error {
	if s.Quality < 0 || s.Quality >= len(qualityIni) || s.NetDelay < 0 || s.NetDelay > 10 {
		return errors.New("ajuste no válido")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s.ISO = g.cfg.ISO // the ISO only changes through chooseISO (it is checked there)
	g.cfg = s
	return g.save()
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

// ---- the ISO ----

func (g *game) ready() bool {
	s := g.settings()
	_, e1 := os.Stat(s.ISO)
	_, e2 := os.Stat(filepath.Join(g.dataDir, "SLUS_216.78"))
	return s.ISO != "" && e1 == nil && e2 == nil
}

// prepare checks the disc is BT4 Beta 14 Rev2 (the game code was recompiled from exactly that ELF) and copies the
// ELF and the small boot files out of it. The big AFS archives are streamed from the ISO by the game.
func (g *game) prepare(isoPath string) error {
	im, err := openISO(isoPath)
	if err != nil {
		return fmt.Errorf("no pude leer la ISO: %v", err)
	}
	defer im.Close()
	top, err := im.root()
	if err != nil {
		return err
	}
	elf := findEntry(top, gameElf)
	if elf == nil {
		return fmt.Errorf("esta ISO no es DBZ BT4 (no tiene %s): hace falta la Beta 14 Rev2", gameElf)
	}
	data, err := im.read(elf)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), gameElfSha256) {
		return errors.New("esta ISO es otra versión del mod: el juego está recompilado para la Beta 14 Rev2 (SLUS-219.78)")
	}
	if err := os.MkdirAll(g.dataDir, 0o755); err != nil {
		return err
	}
	for _, n := range []string{"SLUS_216.78", gameElf} {
		if err := os.WriteFile(filepath.Join(g.dataDir, n), data, 0o644); err != nil {
			return err
		}
	}
	if cnf := findEntry(top, "SYSTEM.CNF"); cnf != nil {
		if b, err := im.read(cnf); err == nil {
			_ = os.WriteFile(filepath.Join(g.dataDir, "SYSTEM.CNF"), b, 0o644)
		}
	}
	for _, sub := range []string{"IRX", "BIN"} {
		d := findEntry(top, sub)
		if d == nil || !d.Dir {
			continue
		}
		files, err := im.list(d.LBA, d.Size)
		if err != nil {
			return err
		}
		out := filepath.Join(g.dataDir, sub)
		_ = os.MkdirAll(out, 0o755)
		for i := range files {
			if files[i].Dir || files[i].Size > 64<<20 || strings.ContainsAny(files[i].Name, `\/:`) {
				continue
			}
			b, err := im.read(&files[i])
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, files[i].Name), b, 0o644); err != nil {
				return err
			}
		}
	}
	g.mu.Lock()
	g.cfg.ISO = isoPath
	err = g.save()
	g.mu.Unlock()
	return err
}

// ---- DLSS ----

type dlssKind int

const (
	dlssNone dlssKind = iota
	dlssSuperRes
	dlssNeural
	dlssReShade
	dlssAddon
)

func kindOf(path string) dlssKind {
	name := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(name, ".addon64") {
		if strings.Contains(name, "dlss5") {
			return dlssAddon
		}
		return dlssNone
	}
	if !strings.HasSuffix(name, ".dll") || !isX64(path) {
		return dlssNone
	}
	v, ok := readFileVersion(path)
	if !ok {
		return dlssNone
	}
	switch {
	case strings.Contains(strings.ToLower(v.Product), "deep learning supersampling"):
		return dlssSuperRes
	case strings.EqualFold(v.Product, "NVIDIA DLSSNR"):
		return dlssNeural
	case strings.EqualFold(v.Product, "ReShade") && strings.Contains(v.Description, "64-bit"):
		return dlssReShade
	}
	return dlssNone
}

func (g *game) userDLSS(kind dlssKind) string {
	best, bestV := "", fileVersion{}
	_ = filepath.WalkDir(g.dlssDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || kindOf(p) != kind {
			return nil
		}
		v, _ := readFileVersion(p)
		if best == "" || bestV.less(v) {
			best, bestV = p, v
		}
		return nil
	})
	return best
}

func swapperFile(rel string) string {
	p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Programs\DLSS 5 Swapper\resources\payload`, rel)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// dlss5Parts: ReShade, the RenoDX DLSS 5 add-on and nvngx_dlssnr.dll (DLSS\ first, then DLSS 5 Swapper), or nil.
func (g *game) dlss5Parts() []string {
	r := firstNonEmpty(g.userDLSS(dlssReShade), swapperFile(`reshade-vulkan\ReShade64.dll`))
	a := firstNonEmpty(g.userDLSS(dlssAddon), swapperFile("renodx-dlss5.addon64"))
	n := firstNonEmpty(g.userDLSS(dlssNeural), swapperFile(`streamline\nvngx_dlssnr.dll`))
	if r == "" || a == "" || n == "" {
		return nil
	}
	return []string{r, a, n}
}

func (g *game) superResDLL() string {
	if u := g.userDLSS(dlssSuperRes); u != "" {
		return u
	}
	if p := filepath.Join(g.gshostDir, "presets", "nvngx_dlss.dll"); fileExists(p) {
		return p
	}
	return filepath.Join(g.gshostDir, "nvngx_dlss.dll")
}

type DLSSInfo struct {
	Version     string `json:"version"`
	Own         bool   `json:"own"`
	DLSS5       bool   `json:"dlss5"`
	DLSS5Neural string `json:"dlss5Neural"`
	Folder      string `json:"folder"`
}

func (g *game) dlssInfo() DLSSInfo {
	sr := g.superResDLL()
	v, _ := readFileVersion(sr)
	info := DLSSInfo{Version: v.String(), Own: g.userDLSS(dlssSuperRes) != "", Folder: g.dlssDir}
	if p := g.dlss5Parts(); p != nil {
		nv, _ := readFileVersion(p[2])
		info.DLSS5, info.DLSS5Neural = true, nv.String()
	}
	return info
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func copyIfChanged(src, dst string) error {
	a, err := os.Stat(src)
	if err != nil {
		return err
	}
	if b, err := os.Stat(dst); err == nil && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	_ = os.Chtimes(tmp, a.ModTime(), a.ModTime())
	return os.Rename(tmp, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		return copyIfChanged(p, t)
	})
}

// addDLSS copies files the player dropped (or picked) into DLSS\ when they are DLSS files.
func (g *game) addDLSS(paths []string) (int, []string) {
	n, skipped := 0, []string(nil)
	_ = os.MkdirAll(g.dlssDir, 0o755)
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".zip") { // DLSS usually comes zipped (TechPowerUp)
			k, s := g.addDLSSZip(p)
			n, skipped = n+k, append(skipped, s...)
			continue
		}
		if kindOf(p) == dlssNone {
			skipped = append(skipped, filepath.Base(p))
			continue
		}
		if err := copyIfChanged(p, filepath.Join(g.dlssDir, filepath.Base(p))); err != nil {
			skipped = append(skipped, filepath.Base(p)+" ("+err.Error()+")")
			continue
		}
		n++
	}
	return n, skipped
}

// addDLSSZip takes the DLLs out of a zip: each one is unpacked to a temporary file, checked like a dropped DLL and
// kept only if it is one of ours.
func (g *game) addDLSSZip(p string) (int, []string) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return 0, []string{filepath.Base(p) + " (no se pudo abrir el zip)"}
	}
	defer zr.Close()
	n := 0
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		ext := strings.ToLower(filepath.Ext(name))
		if f.FileInfo().IsDir() || (ext != ".dll" && ext != ".addon64") || f.UncompressedSize64 > 256<<20 {
			continue
		}
		tmp, err := os.CreateTemp(g.dlssDir, "zip-*-"+name)
		if err != nil {
			continue
		}
		rc, err := f.Open()
		if err == nil {
			_, err = io.Copy(tmp, io.LimitReader(rc, 256<<20))
			rc.Close()
		}
		tmp.Close()
		if err == nil && kindOf(tmp.Name()) != dlssNone && copyIfChanged(tmp.Name(), filepath.Join(g.dlssDir, name)) == nil {
			n++
		}
		os.Remove(tmp.Name())
	}
	if n == 0 {
		return 0, []string{filepath.Base(p) + " (no trae ninguna DLL de DLSS)"}
	}
	return n, nil
}

func (g *game) prepareDLSS5Host() (string, error) {
	parts := g.dlss5Parts()
	if parts == nil {
		return "", errors.New("DLSS 5 necesita una NVIDIA RTX y DLSS 5 Swapper (o sus archivos en la carpeta DLSS)")
	}
	dir := filepath.Join(g.root, "gshost_dlss5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ents, _ := os.ReadDir(g.gshostDir)
	for _, e := range ents {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".exe" || ext == ".dll") {
			if err := copyIfChanged(filepath.Join(g.gshostDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	if err := copyTree(filepath.Join(g.gshostDir, "resources"), filepath.Join(dir, "resources")); err != nil {
		return "", err
	}
	if !fileExists(filepath.Join(dir, "portable.ini")) {
		_ = os.WriteFile(filepath.Join(dir, "portable.ini"), nil, 0o644)
	}
	for i, n := range []string{"dxgi.dll", "renodx-dlss5.addon64", "nvngx_dlssnr.dll"} {
		if err := copyIfChanged(parts[i], filepath.Join(dir, n)); err != nil {
			return "", err
		}
	}
	for _, n := range []string{"dbz_dlss_dx12.ini", "ReShade.ini"} {
		if !fileExists(filepath.Join(dir, n)) {
			_ = copyIfChanged(filepath.Join(g.gshostDir, "presets", n), filepath.Join(dir, n))
		}
	}
	return dir, nil
}

// ---- playing ----

// netMatch is a match from the online server, already validated.
type netMatch struct {
	ID, Relay, Token string
	Player           int
	Fps60            bool
	Delay            int
}

var (
	hostPortRe = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,253}:[0-9]{1,5}$`)
	tokenRe    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func (m *netMatch) valid() bool {
	return hostPortRe.MatchString(m.Relay) && tokenRe.MatchString(m.Token) && (m.Player == 1 || m.Player == 2) && m.Delay >= 0 && m.Delay <= 10
}

// build identifies the exact game build: lockstep online play needs the same runner and 60 fps rules on both PCs.
// netProtocol: bump it whenever the netplay code changes what crosses the wire or how a frame is simulated.
const netProtocol = "net1"

// build: who can play with whom online. Not a hash of the executable (a Mac and a Windows copy of the same game never
// match byte for byte, and crossplay must work) but what decides the simulation besides the disc, whose ELF is
// already checked: the netplay protocol and the 60 fps table. Both ports build the runtime with exact floating point
// (-ffp-contract=off), and the games compare a hash of their state as they play, so a difference shows at once.
// The Mac launcher computes the same: "net1-" + the first 8 hex of sha256(fps60_sites.txt with CRLF -> LF, trailing
// blanks trimmed).
func (g *game) build() string {
	b, _ := os.ReadFile(filepath.Join(g.runtimeDir, "fps60_sites.txt"))
	return netProtocol + "-" + fps60Key(b)
}

func fps60Key(table []byte) string {
	t := strings.TrimRight(strings.ReplaceAll(string(table), "\r\n", "\n"), " \t\n")
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])[:8]
}

func (g *game) running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.proc != nil
}

// play starts the game and calls done(exitCode) when it closes. m == nil plays offline.
func (g *game) play(m *netMatch, done func(code int, logs string)) error {
	if !g.ready() {
		return errors.New("primero elige tu ISO de DBZ BT4 Beta 14 Rev2 (SLUS-219.78)")
	}
	if g.running() {
		return errors.New("el juego ya está abierto")
	}
	s := g.settings()
	if m != nil && !m.valid() {
		return errors.New("la partida que mandó el servidor no es válida")
	}
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
	_ = os.MkdirAll(g.logs, 0o755)
	ww, wh := workArea()
	w := min(1920, ww*9/10)
	h := w * 9 / 16
	if h > wh*9/10 {
		h = wh * 9 / 10
		w = h * 16 / 9
	}
	renderer := "-renderer vulkan"
	if q == qualityDLSS5 {
		renderer = "-renderer dx12"
	}
	args := renderer + " -ini " + ini
	if s.Fullscreen {
		args += " -fullscreen"
	}
	fps60 := s.Fps60
	env := append(os.Environ(),
		"PS2X_CD_IMAGE="+s.ISO, "PS2X_GS_LIVE=1", "PS2X_GSHOST_ARGS="+args,
		"PS2X_LIVE_SERIAL="+gameSerial, "PS2X_LIVE_TITLE="+gameTitle,
		fmt.Sprintf("PS2X_HOST_WINDOW=%dx%d", w, h), "PS2X_LOGFILE="+filepath.Join(g.logs, "juego.log"),
		"PS2X_HALFSTEP="+filepath.Join(g.runtimeDir, "fps60_sites.txt"))
	if hostDir != g.gshostDir {
		env = append(env, "PS2X_GSHOST="+filepath.Join(hostDir, "pcsx2-gsrunner.exe"))
	}
	if m != nil {
		fps60 = m.Fps60 // both players must run the same mode: the match decides
		delay := m.Delay
		if delay == 0 {
			delay = s.NetDelay
		}
		if delay == 0 {
			delay = 3 // a few frames cover an internet round trip through the relay
		}
		env = append(env, "PS2X_NET_RELAY="+m.Relay, "PS2X_NET_TOKEN="+m.Token, fmt.Sprintf("PS2X_NET_PLAYER=%d", m.Player),
			fmt.Sprintf("PS2X_NET_DELAY=%d", delay), "NET_OVERLAY=1", "PS2X_NET_JUMP=1")
	}
	env = append(env, "PS2X_FPS60="+b01(fps60))
	cmd := exec.Command(filepath.Join(g.runtimeDir, "ps2EntryRunner.exe"), filepath.Join(g.dataDir, "SLUS_216.78"))
	cmd.Dir, cmd.Env = g.runtimeDir, env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("no pude arrancar el juego: %v", err)
	}
	g.mu.Lock()
	g.proc = cmd.Process
	g.mu.Unlock()
	started := time.Now()
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		g.mu.Lock()
		g.proc = nil
		g.mu.Unlock()
		g.readBackFps60(filepath.Join(hostDir, ini))
		_ = started
		done(code, filepath.Join(g.logs, "juego.log"))
	}()
	return nil
}

// readBackFps60: the in-game menu saves "DbzFps60 = true/false" in its ini; it becomes the next start's default.
func (g *game) readBackFps60(iniPath string) {
	b, err := os.ReadFile(iniPath)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.ReplaceAll(strings.TrimSpace(l), " ", "")
		if strings.HasPrefix(strings.ToLower(l), "dbzfps60=") {
			g.mu.Lock()
			g.cfg.Fps60 = strings.HasSuffix(strings.ToLower(l), "true")
			_ = g.save()
			g.mu.Unlock()
		}
	}
}

func (g *game) stop() {
	g.mu.Lock()
	p := g.proc
	g.mu.Unlock()
	if p != nil {
		_ = p.Kill()
	}
}

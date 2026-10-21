package main

// The game side of the launcher for DBZ Budokai Tenkaichi 4 (Team BT4's mod of BT3, Beta 14 Rev2): the player's ISO
// (checked and the boot files copied out of it), the settings and starting ps2EntryRunner -- offline, or online with
// the match the server handed out. What differs per system (paths, quality presets, the GS host's renderer, DLSS on
// Windows) is in game_windows.go / game_darwin.go.

import (
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
	"unicode"
)

const (
	gameID        = "dbzbt4"
	gameTitle     = "Dragon Ball Z Budokai Tenkaichi 4"
	gameElf       = "SLUS_219.78"
	gameElfSha256 = "224525C5639C00E01C2604C4685D307BC739C041000DCF8C69A4F884F465E8A4"
	gameSerial    = "SLUS-21978"
)

type Settings struct {
	ISO        string `json:"iso"`
	Quality    int    `json:"quality"`
	Fps60      bool   `json:"fps60"`
	Fullscreen bool   `json:"fullscreen"`
	NetDelay   int    `json:"netDelay"` // online input delay in frames, 0 = automatic
}

type game struct {
	// root: the launcher's folder. home: where it writes (settings, logs, the runtime's data); res: its read-only
	// files. On Windows the three are the zip's folder; on macOS home is ~/Library/Application Support/DBZ_BT4 and
	// res the app's Resources (a downloaded app runs from a read-only copy). paths_*.go sets them.
	root, home, res, runtimeDir, dataDir, gshostDir, cfgPath, dlssDir, logs, runner, sites string
	mu                                                                                     sync.Mutex
	cfg                                                                                    Settings
	proc                                                                                   *os.Process
}

func newGame(root string) *game {
	g := &game{root: root}
	g.setPaths()
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

// QualityOption: one entry of the Calidad menu (the presets are per system).
type QualityOption struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
}

type DLSSInfo struct {
	Version     string `json:"version"`
	Own         bool   `json:"own"`
	DLSS5       bool   `json:"dlss5"`
	DLSS5Neural string `json:"dlss5Neural"`
	Folder      string `json:"folder"`
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

// ---- playing ----

// safeName: a player name as it may go into the game's environment and on screen -- printable runes only, 24 at most.
func safeName(s string) string {
	out := make([]rune, 0, 24)
	for _, r := range s {
		if len(out) == 24 {
			break
		}
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
	}
	return string(out)
}

// netMatch is a match from the online server, already validated.
type netMatch struct {
	ID, Relay, Token string
	Player           int
	Fps60            bool
	Delay            int
	CardDir          string // [netcard] the memory card both games boot from (matchCard)
	Opponent         string // the other player's name, for the game's "waiting for ..." banner
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
	b, _ := os.ReadFile(g.sites)
	return netProtocol + "-" + fps60Key(b)
}

func fps60Key(table []byte) string {
	t := strings.TrimRight(strings.ReplaceAll(string(table), "\r\n", "\n"), " \t\n")
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])[:8]
}

// pid: the running game's process (0 when none).
func (g *game) pid() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.proc == nil {
		return 0
	}
	return g.proc.Pid
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
	// the GS host, its ini and the platform's own variables (game_windows.go / game_darwin.go)
	hostIni, args, penv, err := g.hostSetup(s)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(g.logs, 0o755)
	ww, wh := workArea()
	w := min(1920, ww*9/10)
	h := w * 9 / 16
	if h > wh*9/10 {
		h = wh * 9 / 10
		w = h * 16 / 9
	}
	if s.Fullscreen {
		args += " -fullscreen"
	}
	fps60 := s.Fps60
	env := append(os.Environ(),
		"PS2X_CD_IMAGE="+s.ISO, "PS2X_GS_LIVE=1", "PS2X_GSHOST_ARGS="+args,
		"PS2X_LIVE_SERIAL="+gameSerial, "PS2X_LIVE_TITLE="+gameTitle,
		fmt.Sprintf("PS2X_HOST_WINDOW=%dx%d", w, h), "PS2X_HALFSTEP="+g.sites)
	env = append(env, penv...)
	if m != nil {
		fps60 = m.Fps60 // both players must run the same mode: the match decides
		// the input delay: the match's (the inviter's setting) or, at 0 = "Automático", measured by the two games
		// on the real path when they connect (player 1 pings player 2 through the relay and both use its choice)
		delay := "auto"
		if m.Delay > 0 {
			delay = strconv.Itoa(m.Delay)
		}
		env = append(env, "PS2X_NET_RELAY="+m.Relay, "PS2X_NET_TOKEN="+m.Token, fmt.Sprintf("PS2X_NET_PLAYER=%d", m.Player),
			"PS2X_NET_DELAY="+delay, "NET_OVERLAY=1", "PS2X_NET_JUMP=1",
			// the jump stops at the game's multiplayer menu: player 1 picks Simple / Equipos / Equipos PD there, as on
			// the console, and both go on to character select together
			"PS2X_NET_JUMP_STOP=menu",
			// and each game boots on its own: the two meet at that menu ("waiting for <opponent>" until the other one
			// is there), instead of one waiting before the boot for the other to start
			"PS2X_NET_MEET=menu", "PS2X_NET_PEER_NAME="+safeName(m.Opponent))
		// Lockstep needs two identical simulations, whatever each machine's speed or system:
		//  - the virtual clock (frame-stepped, fibers): the game's threads, disc and sound advance by frames, not by the
		//    host's clock (without it the RAMs differed by frame 60 as soon as one side was slower);
		//  - the same random seed and clock (both read the host's time otherwise), from the match token;
		//  - the same memory card (matchCard);
		//  - and the two compare a hash of the game state every second, so any difference shows at once.
		seed, rtc := matchSeeds(m.Token)
		env = append(env, "PS2X_FRAMESTEP=1", "PS2X_FIBERS=1", "PS2X_DETHASH=60",
			fmt.Sprintf("PS2X_RANDSEED=%d", seed), fmt.Sprintf("PS2X_RTC=%d", rtc))
		if m.CardDir != "" {
			env = append(env, "PS2X_MC_ROOT="+m.CardDir)
		}
		_ = os.Remove(g.netStatusPath()) // a previous match's "offline" must not end this one
		env = append(env, "PS2X_NET_STATUS_FILE="+g.netStatusPath())
	}
	env = append(env, "PS2X_FPS60="+b01(fps60))
	cmd := exec.Command(g.runner, filepath.Join(g.dataDir, "SLUS_216.78"))
	cmd.Dir, cmd.Env = g.runtimeDir, env
	cleanup := g.prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		cleanup()
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
		cleanup()
		g.mu.Lock()
		g.proc = nil
		g.mu.Unlock()
		g.readBackFps60(hostIni)
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

// netStatusPath: where an online game says whether it is still online (PS2X_NET_STATUS_FILE, see watchOnline).
func (g *game) netStatusPath() string { return filepath.Join(g.logs, "online.status") }

func (g *game) stop() {
	g.mu.Lock()
	p := g.proc
	g.mu.Unlock()
	if p != nil {
		_ = p.Kill()
	}
}

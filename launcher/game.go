package main

// The game side of the launcher, for one installed game (its profile, profiles.go): the player's ISO (checked and
// the boot files copied out of it), the settings and starting ps2EntryRunner -- offline, or online with the match
// the server handed out. What differs per system (paths, the GS host's renderer, DLSS on Windows) is in
// game_windows.go / game_darwin.go; what differs per game is in the profile.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Settings struct {
	ISO        string          `json:"iso"`
	Quality    int             `json:"quality"`
	Fullscreen bool            `json:"fullscreen"`
	NetDelay   int             `json:"netDelay"` // online input delay in frames, 0 = automatic
	Opts       map[string]bool `json:"opts"`     // the game's own switches (profile.Options)
}

type game struct {
	p *profile
	// root: the launcher's folder. dir: this game's folder (root itself for an old DBZ BT4 zip). home: where it writes
	// (settings, logs, the runtime's data); res: its read-only files. On Windows the three are the game's folder; on
	// macOS home is in Application Support and res the app's Resources. game_*.go sets them.
	root, dir, home, res, runtimeDir, dataDir, gshostDir, cfgPath, dlssDir, logs, runner, sites string
	mu                                                                                          sync.Mutex
	attach                                                                                      string // [handover] the ring the next game takes over
	handingOver                                                                                 bool   // [handover] the running game is leaving its window
	cfg                                                                                         Settings
	proc                                                                                        *os.Process
}

func newGame(root, dir string, p *profile) *game {
	g := &game{p: p, root: root, dir: dir}
	g.setPaths()
	g.dataDir = filepath.Join(g.runtimeDir, "data")
	g.load()
	return g
}

// installed: the game's runtime is there (the ISO may still be missing).
func (g *game) installed() bool { return fileExists(g.runner) }

// ---- settings (key=value, the same file as the v1.x launcher, so an update keeps them) ----

func (g *game) defaults() Settings {
	s := Settings{Quality: g.defaultQuality(), Fullscreen: true, Opts: map[string]bool{}}
	for _, o := range g.p.Options {
		s.Opts[o.Key] = o.Default
	}
	return s
}

func (g *game) load() {
	g.cfg = g.defaults()
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
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(g.p.qualities()) {
				g.cfg.Quality = n
			}
		case "fullscreen":
			g.cfg.Fullscreen = v == "1"
		case "netdelay":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 10 {
				g.cfg.NetDelay = n
			}
		default:
			for _, o := range g.p.Options {
				if o.Key == k {
					g.cfg.Opts[k] = v == "1"
				}
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
	var sb strings.Builder
	fmt.Fprintf(&sb, "iso=%s\nquality=%d\nfullscreen=%s\nnetdelay=%d\n", g.cfg.ISO, g.cfg.Quality, b01(g.cfg.Fullscreen), g.cfg.NetDelay)
	for _, o := range g.p.Options {
		fmt.Fprintf(&sb, "%s=%s\n", o.Key, b01(g.p.opt(g.cfg, o.Key)))
	}
	_ = os.MkdirAll(filepath.Dir(g.cfgPath), 0o755)
	return os.WriteFile(g.cfgPath, []byte(sb.String()), 0o644)
}

func (g *game) settings() Settings {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.cfg
	s.Opts = map[string]bool{}
	for k, v := range g.cfg.Opts {
		s.Opts[k] = v
	}
	return s
}

func (g *game) setSettings(s Settings) error {
	if s.Quality < 0 || s.Quality >= len(g.p.qualities()) || s.NetDelay < 0 || s.NetDelay > 10 {
		return errors.New("ajuste no válido")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	opts := map[string]bool{}
	for _, o := range g.p.Options { // only the game's own keys
		opts[o.Key] = g.p.opt(s, o.Key)
	}
	s.Opts = opts
	s.ISO = g.cfg.ISO // the ISO only changes through chooseISO (it is checked there)
	g.cfg = s
	return g.save()
}

// ---- the ISO ----

func (g *game) ready() bool {
	s := g.settings()
	_, e1 := isoStat(s.ISO)
	_, e2 := os.Stat(filepath.Join(g.dataDir, g.p.BootName))
	return g.installed() && s.ISO != "" && e1 == nil && e2 == nil
}

// prepare checks the disc is the one the game code was recompiled from (the ELF's hash) and copies the ELF and the
// small boot files out of it (the IOP modules). The big archives are streamed from the ISO by the game.
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
	elf := findEntry(top, g.p.Elf)
	if elf == nil {
		return fmt.Errorf("esta ISO no es %s (no tiene %s)", g.p.Title, g.p.Elf)
	}
	data, err := im.read(elf)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), g.p.ElfSHA256) {
		return fmt.Errorf("esta ISO es otra versión de %s: el juego está recompilado para %s", g.p.Title, g.p.Serial)
	}
	if err := os.MkdirAll(g.dataDir, 0o755); err != nil {
		return err
	}
	names := []string{g.p.Elf}
	if g.p.BootName != g.p.Elf {
		names = append(names, g.p.BootName)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(g.dataDir, n), data, 0o644); err != nil {
			return err
		}
	}
	if cnf := findEntry(top, "SYSTEM.CNF"); cnf != nil {
		if b, err := im.read(cnf); err == nil {
			_ = os.WriteFile(filepath.Join(g.dataDir, "SYSTEM.CNF"), b, 0o644)
		}
	}
	for _, sub := range g.p.CopyDirs {
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

// QualityOption: one entry of the Calidad menu (the presets are per game and system).
type QualityOption struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
}

func (g *game) qualityOptions() []QualityOption {
	var out []QualityOption
	for _, q := range g.p.qualities() {
		out = append(out, q.QualityOption)
	}
	return out
}

// nativeQuality: the chosen quality is the runtime's own renderer (no GS host).
func (g *game) nativeQuality(s Settings) bool {
	qs := g.p.qualities()
	return s.Quality >= 0 && s.Quality < len(qs) && qs[s.Quality].Renderer == "native"
}

// hostQuality: the quality the GS host runs: the chosen one, or for the native renderer (online) the plain DLSS / FSR
// preset, else the first.
func (g *game) hostQuality(s Settings) int {
	qs := g.p.qualities()
	q := s.Quality
	if q < 0 || q >= len(qs) {
		return 0
	}
	if qs[q].Renderer == "native" {
		for i, p := range qs {
			if strings.Contains(p.Ini, "dlss") {
				return i
			}
		}
		return 0
	}
	return q
}

// hostIni: the GS host ini for the chosen quality and the game's options.
func (g *game) hostIni(s Settings, q int) string {
	p := g.p.qualities()[q]
	if g.p.iniFor != nil {
		return g.p.iniFor(g.p, p, s)
	}
	return p.Ini
}

type DLSSInfo struct {
	Version string `json:"version"`
	Own     bool   `json:"own"`
	Folder  string `json:"folder"`
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

// build: who can play with whom online. Not a hash of the executable (a Mac and a Windows copy of the same game never
// match byte for byte, and crossplay must work) but what decides the simulation besides the disc, whose ELF is
// already checked: the game's netplay protocol, and for DBZ BT4 its 60 fps table. Both ports build the runtime with
// exact floating point (-ffp-contract=off), and the games compare a hash of their state as they play, so a
// difference shows at once. The Mac launcher computes the same: netProtocol + "-" + the first 8 hex of
// sha256(fps60_sites.txt with CRLF -> LF, trailing blanks trimmed).
func (g *game) build() string {
	b := g.p.netProtocol
	if g.p.Fps60Sites {
		t, _ := os.ReadFile(g.sites)
		b += "-" + fps60Key(t)
	}
	if v := g.packVersion(); v != "" && !g.p.Fps60Sites {
		b += "-" + v
	}
	// Gate: where the runner ships in a downloadable pack (Windows), the launcher advertises netProtocol from its own
	// version, but an installed pack older than the profile's minimum lacks what that protocol needs (net3 -> the
	// detmath runtime, first in pack 2.5.7). Such a pack would otherwise pair as a current client and desync. Diverge
	// the key so it cannot match up-to-date clients; the lobby then shows it as a different version.
	if runnerInPack && g.p.minPack != "" {
		if v := g.packVersion(); v == "" || newerVersion(g.p.minPack, v) {
			b += "-needpack" + g.p.minPack
		}
	}
	return b
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
		return fmt.Errorf("primero elige tu ISO de %s (%s)", g.p.Title, g.p.Serial)
	}
	if g.running() {
		return errors.New("el juego ya está abierto")
	}
	s := g.settings()
	if m != nil && !m.valid() {
		return errors.New("la partida que mandó el servidor no es válida")
	}
	// the GS host, its ini and the platform's own variables (game_windows.go / game_darwin.go); the native renderer has no
	// host: the runtime draws and presents itself. Online too: a native player and a PCSX2 one stay in sync (0 desyncs in
	// 8000 frames; the fight-start fade's pixel download is out of the state hash in every mode)
	native := g.nativeQuality(s)
	var hostIni, args string
	var penv []string
	var err error
	if native {
		penv = g.nativeSetup()
	} else if hostIni, args, penv, err = g.hostSetup(s); err != nil {
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
	env := append(os.Environ(), "PS2X_CD_IMAGE="+s.ISO, "PS2X_LIVE_SERIAL="+g.p.Serial, "PS2X_LIVE_TITLE="+g.p.Title)
	if native {
		mode := "0"
		if s.Fullscreen {
			mode = "2"
		}
		env = append(env, "PS2X_GS_LIVE=0", "PS2X_SEAMVK=1", "PS2X_SEAMVK_SCALE="+g.nativeScale(), "PS2X_SEAMVK_UPSCALE="+nativeUpscale, "PS2X_WINDOW_MODE="+mode,
			fmt.Sprintf("PS2X_WINDOW_W=%d", w), fmt.Sprintf("PS2X_WINDOW_H=%d", h))
		if g.p.opt(s, "framegen") {
			env = append(env, nativeFrameGen...)
		}
	} else {
		env = append(env, "PS2X_GS_LIVE=1", "PS2X_GSHOST_ARGS="+args, fmt.Sprintf("PS2X_HOST_WINDOW=%dx%d", w, h))
	}
	env = append(env, penv...)
	if g.p.ID != dbzProfile.ID { // the game's own memory card folder (DBZ's runtime keeps it in savedata/ by itself)
		_ = os.MkdirAll(g.cardRoot(), 0o755)
		if m == nil {
			env = append(env, "PS2X_MC_ROOT="+g.cardRoot())
		}
	}
	if m != nil {
		// the input delay: the match's (the inviter's setting) or, at 0 = "Automático", measured by the two games
		// on the real path when they connect (player 1 pings player 2 through the relay and both use its choice)
		delay := "auto"
		if m.Delay > 0 {
			delay = strconv.Itoa(m.Delay)
		}
		env = append(env, "PS2X_NET_RELAY="+m.Relay, "PS2X_NET_TOKEN="+m.Token, fmt.Sprintf("PS2X_NET_PLAYER=%d", m.Player),
			"PS2X_NET_DELAY="+delay, "PS2X_NET_PEER_NAME="+safeName(m.Opponent))
		// the same clock on both sides (both games read the host's time otherwise), from the match token; the game's
		// state hash every second (both players must use the same interval: a diagnostic one given to the launcher
		// itself, e.g. 1 = every frame, wins over the default 60)
		_, rtc := matchSeeds(m.Token)
		dethash := "60"
		if v := os.Getenv("PS2X_DETHASH"); v != "" {
			dethash = v
		}
		env = append(env, fmt.Sprintf("PS2X_RTC=%d", rtc), "PS2X_DETHASH="+dethash)
		if m.CardDir != "" {
			env = append(env, "PS2X_MC_ROOT="+m.CardDir)
		}
		_ = os.Remove(g.netStatusPath()) // a previous match's "offline" must not end this one
		env = append(env, "PS2X_NET_STATUS_FILE="+g.netStatusPath())
		// [stats] the game writes each fight's result here (watchOnline sends them to the server)
		_ = os.Remove(g.netFightsPath())
		env = append(env, "PS2X_NET_STATS_FILE="+g.netFightsPath())
	}
	if g.p.env != nil {
		env = append(env, g.p.env(g, s, m)...)
	}
	// [invite] an invitation that arrives while playing is shown by the GS host over the game and answered with the pad
	_ = os.Remove(g.invitePath())
	_ = os.Remove(g.inviteAnswerPath())
	env = append(env, "PS2X_INVITE_FILE="+g.invitePath(), "PS2X_INVITE_ANSWER="+g.inviteAnswerPath())
	// [handover] every game can leave its window to the next one (handover), and the next one takes it over
	_ = os.Remove(g.handoverPath())
	env = append(env, "PS2X_LIVE_RING_FILE="+g.ringPath(), "PS2X_HANDOVER_FILE="+g.handoverPath())
	g.mu.Lock()
	if g.attach != "" {
		env = append(env, "PS2X_LIVE_ATTACH="+g.attach)
		g.attach = ""
	}
	g.mu.Unlock()
	proc, wait, err := g.startRunner(filepath.Join(g.dataDir, g.p.BootName), g.bootDir(), env)
	if err != nil {
		return fmt.Errorf("no pude arrancar el juego: %v", err)
	}
	g.mu.Lock()
	g.proc = proc
	g.mu.Unlock()
	go func() {
		code := wait()
		g.mu.Lock()
		g.proc = nil
		handedOver := g.handingOver
		g.handingOver = false
		g.mu.Unlock()
		if handedOver { // [handover] it left its window to the next game: not a game that ended
			return
		}
		if g.p.afterGame != nil {
			g.p.afterGame(g, hostIni)
		}
		done(code, filepath.Join(g.logs, "juego.log"))
	}()
	return nil
}

// bootDir: the runner's working folder. The BT3 runtime (DBZ) finds its assets next to it; PS2Recomp's (BR4) finds
// the disc's IOP modules (MODULES/) next to the ELF.
func (g *game) bootDir() string {
	if g.p.ID == dbzProfile.ID {
		return g.runtimeDir
	}
	return g.dataDir
}

// netStatusPath: where an online game says whether it is still online (PS2X_NET_STATUS_FILE, see watchOnline).
func (g *game) netStatusPath() string { return filepath.Join(g.logs, "online.status") }

// netFightsPath: where an online game writes the result of each fight (PS2X_NET_STATS_FILE), one line each:
// "fight <n> winner=<1|2|0> c1=<character> c2=<character> ...".
func (g *game) netFightsPath() string { return filepath.Join(g.logs, "online.fights") }

// [handover] The game window outlives the game: the running game writes its window's ring name into ringPath
// (PS2X_LIVE_RING_FILE) and leaves the window when handoverPath appears (PS2X_HANDOVER_FILE); the next game is
// started with PS2X_LIVE_ATTACH=<ring> and draws into the same window. For an online match accepted during an
// offline game: the new game must boot from zero (both players' games have to be identical), but the player never
// sees the window close.
// [invite] the invitation for the GS host ("<id>\n<from>\n<line>") and its answer ("<id> accept|decline").
func (g *game) invitePath() string       { return filepath.Join(g.logs, "invite.req") }
func (g *game) inviteAnswerPath() string { return filepath.Join(g.logs, "invite.ans") }

func (g *game) ringPath() string     { return filepath.Join(g.logs, "gshost.ring") }
func (g *game) handoverPath() string { return filepath.Join(g.logs, "handover.req") }

// handover asks the running game to leave its window and waits for it to exit; true: the next play() attaches to
// that window. False (no window to hand over, an older runtime, or it did not leave in time): stop it the old way.
func (g *game) handover() bool {
	b, err := os.ReadFile(g.ringPath())
	if err != nil || !g.running() {
		return false
	}
	ring := strings.Fields(string(b))
	if len(ring) == 0 {
		return false
	}
	g.mu.Lock()
	g.handingOver = true
	g.mu.Unlock()
	if os.WriteFile(g.handoverPath(), []byte("1"), 0o644) != nil {
		g.mu.Lock()
		g.handingOver = false
		g.mu.Unlock()
		return false
	}
	for i := 0; i < 60 && g.running(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.proc != nil { // still running: it did not hand over
		g.handingOver = false
		_ = os.Remove(g.handoverPath())
		return false
	}
	g.attach = ring[0]
	return true
}

func (g *game) stop() {
	g.mu.Lock()
	p := g.proc
	g.mu.Unlock()
	if p != nil {
		_ = p.Kill()
	}
}

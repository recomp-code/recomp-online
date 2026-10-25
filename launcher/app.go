package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const launcherVersion = "3.4.1"

// defaultServer is set at build time: -ldflags "-X main.defaultServer=https://online.tudominio.com"
var defaultServer = "https://online.example.com"

type App struct {
	ctx      context.Context
	root     string
	game     *game            // the game selected in the launcher (the one Jugar starts and the lobby is for)
	games    map[string]*game // every game the launcher knows, installed or not
	net      *online
	voice    *voiceChat // [voice]
	hk       hotkey
	pad      padWatch
	mu       sync.Mutex
	overlay  bool // the launcher is shown over the running game (Shift+Tab or a notice)
	direct   bool // --jugar: quit when the game closes
	match    *netMatch
	quitting bool
}

func NewApp(root string, direct bool) *App {
	a := &App{root: root, direct: direct, games: map[string]*game{}}
	for _, p := range profiles {
		a.games[p.ID] = newGame(root, gameDir(root, p), p)
	}
	a.game = a.games[a.lastGame()]
	a.net = newOnline(launcherHome(root), a.game.p.ID, a.game.build(), a.emit)
	a.net.onEvent = a.onNetEvent
	a.voice = newVoice(launcherHome(root), a.emit)
	return a
}

func (a *App) emit(name string, data any) {
	if ev, ok := data.(map[string]any); ok && name == "online" && (ev["type"] == "logged_out" || ev["type"] == "replaced") {
		a.voice.leave() // [voice] signed out, or another launcher took this account: out of the channel
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, name, data)
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	wruntime.OnFileDrop(ctx, func(x, y int, paths []string) {
		n, skipped := a.game.addDLSS(paths)
		a.emit("dlss", map[string]any{"added": n, "skipped": skipped, "info": a.game.dlssInfo()})
	})
	// [test] RECOMP_TEST_LOGIN=<user>:<password>: sign in at start when not signed in (unattended online tests,
	// with RECOMP_TEST_INVITE / RECOMP_TEST_ACCEPT, against a test server)
	if v := os.Getenv("RECOMP_TEST_LOGIN"); v != "" {
		if _, ok := a.net.loggedIn(); !ok {
			if u, pw, ok := strings.Cut(v, ":"); ok {
				_ = a.net.login(u, pw, false)
			}
		}
	}
	if _, ok := a.net.loggedIn(); ok {
		a.net.connect()
	}
	go a.updateLoop() // [update] the launcher and the installed games (updater.go, installer.go)
	if a.direct && a.game.ready() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := a.Play(); err != "" {
				wruntime.WindowShow(ctx)
			}
		}()
	}
	if u := strings.TrimSpace(os.Getenv("RECOMP_TEST_INVITE")); u != "" {
		go a.testInvite(u)
	}
}

// [test] RECOMP_TEST_INVITE=<user>: invite that player every 10 s until a match starts, once per launch;
// RECOMP_TEST_ACCEPT=1: accept every invitation (onNetEvent). For unattended online tests between two machines
// with test accounts -- together with the game's PS2X_AUTOSTART, nobody has to touch either one.
func (a *App) testInvite(user string) {
	for {
		time.Sleep(10 * time.Second)
		a.mu.Lock()
		inMatch := a.match != nil
		a.mu.Unlock()
		if inMatch || a.game.running() {
			return
		}
		if msg := a.Invite(user); msg != "" {
			wruntime.LogInfo(a.ctx, "test invite: "+msg)
		}
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.hk.stop()
	a.voice.close()
}

// ---- the games ----

// prefsPath: the launcher's own choices (the selected game).
func (a *App) prefsPath() string { return filepath.Join(launcherHome(a.root), "recomp.json") }

// lastGame: the game selected last time; else the first one installed; else the first one.
func (a *App) lastGame() string {
	var prefs struct {
		Game string `json:"game"`
	}
	if readJSONFile(a.prefsPath(), &prefs) == nil {
		if g := a.games[prefs.Game]; g != nil && g.p.supported() {
			return prefs.Game
		}
	}
	for _, p := range profiles {
		if a.games[p.ID].installed() && p.supported() {
			return p.ID
		}
	}
	return profiles[0].ID
}

// GameEntry: one game of the sidebar.
type GameEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Short     string `json:"short"`
	Banner    string `json:"banner"`
	Installed bool   `json:"installed"`
	Supported bool   `json:"supported"` // the game exists for this system
	Selected  bool   `json:"selected"`
}

func (a *App) gameEntries() []GameEntry {
	var out []GameEntry
	for _, p := range profiles {
		g := a.games[p.ID]
		out = append(out, GameEntry{ID: p.ID, Title: p.Title, Short: p.Short, Banner: p.Banner, Installed: g.installed(),
			Supported: p.supported(), Selected: g == a.game})
	}
	return out
}

// SelectGame: the player picks a game in the sidebar (not while one is open or a match is on).
func (a *App) SelectGame(id string) string {
	g := a.games[id]
	if g == nil || !g.p.supported() {
		return "ese juego no está disponible para " + platformName
	}
	a.mu.Lock()
	busy := a.match != nil
	a.mu.Unlock()
	if busy || a.game.running() {
		return "cierra el juego abierto antes de cambiar"
	}
	a.game = g
	b, _ := json.Marshal(map[string]string{"game": id})
	_ = os.WriteFile(a.prefsPath(), b, 0o644)
	a.net.setGame(g.p.ID, g.build())
	return ""
}

// ---- state and settings ----

type State struct {
	Games      []GameEntry  `json:"games"`
	Game       string       `json:"game"`
	Short      string       `json:"short"`
	Banner     string       `json:"banner"`
	Sub        string       `json:"sub"`
	Serial     string       `json:"serial"`
	Installed  bool         `json:"installed"`
	ISOMissing string       `json:"isoMissing"`
	Controls   string       `json:"controls"`
	Options    []GameOption `json:"options"`
	HasDLSS    bool         `json:"hasDLSS"`
	Title      string       `json:"title"`
	Version    string       `json:"version"`
	Build      string       `json:"build"`
	Ready      bool         `json:"ready"`
	ISO        string       `json:"iso"`
	Settings   Settings     `json:"settings"`
	DLSS       DLSSInfo     `json:"dlss"`
	GPU        string       `json:"gpu"`
	Server     string       `json:"server"`
	User       string       `json:"user"`
	LoggedIn   bool         `json:"loggedIn"`
	Playing    bool         `json:"playing"`
	// what differs per system, for the interface
	Platform      string          `json:"platform"`
	Qualities     []QualityOption `json:"qualities"`
	FullscreenKey string          `json:"fullscreenKey"`
	DataFolder    string          `json:"dataFolder"`
}

func (a *App) State() State {
	s := a.game.settings()
	u, ok := a.net.loggedIn()
	a.net.mu.Lock()
	server := a.net.cfg.Server
	a.net.mu.Unlock()
	p := a.game.p
	return State{Games: a.gameEntries(), Game: p.ID, Short: p.Short, Banner: p.Banner, Sub: p.Sub, Serial: p.Serial,
		Installed: a.game.installed(), ISOMissing: p.ISOMissing, Controls: p.Controls, Options: p.Options, HasDLSS: p.DLSSFolder,
		Title: p.Title, Version: launcherVersion, Build: a.game.build(), Ready: a.game.ready(), ISO: s.ISO, Settings: s,
		DLSS: a.game.dlssInfo(), GPU: gpuNames(), Server: server, User: u, LoggedIn: ok, Playing: a.game.running(),
		Platform: platformName, Qualities: a.game.qualityOptions(), FullscreenKey: fullscreenKey, DataFolder: a.game.dataFolder()}
}

func (a *App) SaveSettings(s Settings) string { return errText(a.game.setSettings(s)) }

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *App) ChooseISO() string {
	p, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: a.game.p.ISOTitle,
		Filters: []wruntime.FileFilter{{DisplayName: "Imagen de disco PS2 (*.iso)", Pattern: "*.iso"}}})
	if err != nil || p == "" {
		return ""
	}
	return errText(a.game.prepare(p))
}

// OpenDLSSFolder: the DLSS folder on Windows, the saves and logs folder on macOS (game_*.go, dataFolder).
func (a *App) OpenDLSSFolder() {
	d := a.game.dataFolder()
	_ = os.MkdirAll(d, 0o755)
	openFolder(a.ctx, d)
}

// OpenLink: only the pages the launcher links to.
func (a *App) OpenLink(which string) {
	links := map[string]string{"github": "https://github.com/" + a.game.p.Repo}
	if u, ok := links[which]; ok {
		wruntime.BrowserOpenURL(a.ctx, u)
	}
}

// ---- playing ----

func (a *App) Play() string { return a.start(nil) }

func (a *App) start(m *netMatch) string {
	err := a.game.play(m, func(code int, logs string) {
		a.hk.stop()
		a.pad.end()
		a.mu.Lock()
		cur := a.match
		a.match = nil
		quit := a.direct && m == nil && code == 0
		a.mu.Unlock()
		if cur != nil {
			_ = a.net.call("/api/match/end", map[string]string{"id": cur.ID}, nil)
		}
		if quit {
			wruntime.Quit(a.ctx)
			return
		}
		a.setOverlay(false)
		a.toForeground()
		a.emit("game", map[string]any{"running": false, "code": code, "log": logs})
	})
	if err != nil {
		return err.Error()
	}
	a.mu.Lock()
	a.match = m
	a.mu.Unlock()
	a.emit("game", map[string]any{"running": true, "online": m != nil})
	if m != nil {
		go a.watchOnline(m)
	}
	a.hk.start(func(id int) {
		if id == hkMute {
			a.voice.toggleMute(true) // [voice] Shift+M: the microphone, with a beep (the launcher is not on screen)
			return
		}
		a.toggleOverlay()
	})
	a.pad.start(func() { a.toggleOverlay() })
	// the launcher stays on screen while the game boots (it says Shift+Tab brings it back), then goes to the
	// taskbar: it never just vanishes
	go func() {
		time.Sleep(2500 * time.Millisecond)
		a.mu.Lock()
		on := a.overlay
		a.mu.Unlock()
		if !on && a.game.running() {
			a.toBackground()
		}
	}()
	return ""
}

// overlay: the launcher shown on top of the game (Shift+Tab, or when an invitation arrives).
func (a *App) setOverlay(on bool) {
	a.mu.Lock()
	a.overlay = on
	a.mu.Unlock()
	wruntime.WindowSetAlwaysOnTop(a.ctx, on)
	a.emit("overlay", on)
}

func (a *App) showOverlay() {
	a.setOverlay(true)
	a.toForeground() // restored before shown (app_windows.go)
	launcherToFront()
}

// toggleOverlay (Shift+Tab during a game): in front -> back to the game; anywhere else (taskbar, behind the
// game) -> on top with the focus.
func (a *App) toggleOverlay() {
	if !a.game.running() {
		return
	}
	if launcherInFront() {
		a.HideOverlay()
		return
	}
	a.showOverlay()
}

// watchOnline: while an online game runs, follow what it reports (PS2X_NET_STATUS_FILE). When it leaves the online
// session -- nobody at the meeting in the multiplayer menu, the other player gone -- the match ends on the server at
// once and the launcher stops showing it as being played, though the game stays open offline.
func (a *App) watchOnline(m *netMatch) {
	path := a.game.netStatusPath()
	sent := 0
	defer func() { a.sendFights(m, &sent) }() // the last fight, written as the game closed or went offline
	for a.game.running() {
		time.Sleep(time.Second)
		a.sendFights(m, &sent)
		b, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(b), "offline") {
			continue
		}
		a.mu.Lock()
		cur := a.match
		if cur == m {
			a.match = nil
		}
		a.mu.Unlock()
		if cur != m {
			return
		}
		_ = a.net.call("/api/match/end", map[string]string{"id": m.ID}, nil)
		a.emit("game_offline", map[string]any{"why": strings.TrimSpace(strings.TrimPrefix(string(b), "offline"))})
		return
	}
}

// sendFights: the fights the game wrote since the last call go to the server, which counts each one once both
// players' games reported the same result (the online statistics).
func (a *App) sendFights(m *netMatch, sent *int) {
	b, err := os.ReadFile(a.game.netFightsPath())
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for *sent < len(lines) {
		var n, winner, c1, c2 int
		if _, err := fmt.Sscanf(lines[*sent], "fight %d winner=%d c1=%d c2=%d", &n, &winner, &c1, &c2); err != nil {
			*sent++
			continue
		}
		if err := a.net.call("/api/match/fight", map[string]any{"id": m.ID, "n": n, "winner": winner, "c1": c1, "c2": c2}, nil); err != nil {
			return // the server is out of reach for a moment: again on the next call
		}
		*sent++
	}
}

// Stats: the online table of this game (the best 50) and one player's own line (user "" = the player).
func (a *App) Stats(user string) map[string]any {
	var out map[string]any
	if err := a.net.call("/api/stats", map[string]string{"game": a.game.p.ID, "user": user}, &out); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return out
}

// LeaveMatch (the overlay's "Salir de la partida online"): closes the game; the match ends on the server when it
// exits (start's done), and the other game sees the session drop and plays on offline.
func (a *App) LeaveMatch() {
	a.mu.Lock()
	online := a.match != nil
	a.mu.Unlock()
	if online {
		a.game.stop()
	}
}

func (a *App) HideOverlay() {
	if a.game.running() {
		a.setOverlay(false)
		a.toBackground() // the focus goes back to the game
	}
}

func (a *App) onNetEvent(ev map[string]any) {
	a.voice.onEvent(ev) // [voice]
	switch ev["type"] {
	case "invite":
		if os.Getenv("RECOMP_TEST_ACCEPT") == "1" { // [test] see testInvite
			if inv, ok := ev["invite"].(map[string]any); ok {
				if id, _ := inv["id"].(string); id != "" {
					go a.Respond(id, true)
				}
			}
			break
		}
		if a.game.running() { // the player is in a game: the GS host shows it over the game (or, failing that, the launcher)
			if inv, ok := ev["invite"].(map[string]any); ok {
				go a.inGameInvite(inv)
			}
		}
	case "match":
		mm, _ := ev["match"].(map[string]any)
		m := &netMatch{}
		m.ID, _ = mm["id"].(string)
		m.Relay, _ = mm["relay"].(string)
		m.Token, _ = mm["token"].(string)
		if p, ok := mm["player"].(float64); ok {
			m.Player = int(p)
		}
		m.Opponent, _ = mm["opponent"].(string)
		if st, ok := mm["settings"].(map[string]any); ok {
			m.Fps60, _ = st["fps60"].(bool)
			if d, ok := st["delay"].(float64); ok {
				m.Delay = int(d)
			}
		}
		if !m.valid() {
			a.emit("toast", map[string]any{"kind": "bad", "text": "La partida que mandó el servidor no es válida."})
			return
		}
		go func() {
			// an offline game is open: the match replaces it IN ITS WINDOW (handover) -- the new game boots from zero,
			// as lockstep needs, but the window stays and says "Preparando la partida"; the launcher stays out of the
			// way. Only if that is not possible, the old way: close it and open the match's game.
			inGame := false
			if a.game.running() {
				inGame = a.game.handover()
				if !inGame {
					a.game.stop()
					for i := 0; i < 50 && a.game.running(); i++ {
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			a.setOverlay(false)
			if inGame {
				a.toBackground()
			} else {
				// minimized, it stays so: the game's window opens on its own, and a WebView2 that was never shown (a
				// launcher started minimized) fails its focus and go-webview2 ends the process
				if !wruntime.WindowIsMinimised(a.ctx) {
					a.toForeground()
				}
				a.emit("match_countdown", map[string]any{"opponent": mm["opponent"], "player": m.Player})
			}
			t0 := time.Now()
			// [netcard] during the countdown: player 1 shares its memory card, player 2 fetches it
			dir, err := a.matchCard(m)
			if err != nil {
				a.emit("toast", map[string]any{"kind": "bad", "text": err.Error()})
				_ = a.net.call("/api/match/end", map[string]string{"id": m.ID}, nil)
				return
			}
			m.CardDir = dir
			if rest := 3*time.Second - time.Since(t0); rest > 0 {
				time.Sleep(rest)
			}
			if msg := a.start(m); msg != "" {
				a.emit("toast", map[string]any{"kind": "bad", "text": msg})
				_ = a.net.call("/api/match/end", map[string]string{"id": m.ID}, nil)
			}
		}()
	}
}

// ---- online (called from the interface; "" = ok, otherwise the message to show) ----

func (a *App) Login(user, pass string) string {
	return errText(a.net.login(strings.TrimSpace(user), pass, false))
}
func (a *App) Register(user, pass string) string {
	return errText(a.net.login(strings.TrimSpace(user), pass, true))
}
func (a *App) Logout() { a.net.logout() }

// OnlineSnapshot: the interface calls it when it loads, to catch up with the live lobby.
func (a *App) OnlineSnapshot() []map[string]any { return a.net.snapshot() }
func (a *App) SetServer(s string) string        { return errText(a.net.setServer(s)) }
func (a *App) SetWaiting(on bool) string {
	return errText(a.net.call("/api/status", map[string]bool{"waiting": on}, nil))
}
func (a *App) Invite(user string) string {
	s := a.game.settings()
	return errText(a.net.call("/api/invite", map[string]any{"to": user, "settings": map[string]any{"fps60": a.game.p.opt(s, "fps60"), "delay": s.NetDelay}}, nil))
}
func (a *App) Respond(id string, accept bool) string {
	return errText(a.net.call("/api/invite/respond", map[string]any{"id": id, "accept": accept}, nil))
}
func (a *App) CreateRoom(name, pass string, max int) string {
	s := a.game.settings()
	return errText(a.net.call("/api/rooms", map[string]any{"name": name, "password": pass, "max": max,
		"settings": map[string]any{"fps60": a.game.p.opt(s, "fps60"), "delay": s.NetDelay}}, nil))
}
func (a *App) JoinRoom(id, pass string) string {
	return errText(a.net.call("/api/rooms/join", map[string]string{"id": id, "password": pass}, nil))
}
func (a *App) LeaveRoom() string { return errText(a.net.call("/api/rooms/leave", nil, nil)) }
func (a *App) StartRoom() string { return errText(a.net.call("/api/rooms/start", nil, nil)) }
func (a *App) Kick(user string) string {
	return errText(a.net.call("/api/rooms/kick", map[string]string{"user": user}, nil))
}
func (a *App) Chat(text string) string {
	return errText(a.net.call("/api/chat", map[string]string{"text": text}, nil))
}

// Search: accounts whose name contains q, online or not ({users, error}).
func (a *App) Search(q string) map[string]any {
	var out struct {
		Users []map[string]any `json:"users"`
	}
	if err := a.net.call("/api/users/search", map[string]string{"q": q}, &out); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"users": out.Users}
}

// Block: [block] block or unblock a player (server: no invitations either way, no shared room, no chat from them).
func (a *App) Block(user string, block bool) string {
	return errText(a.net.call("/api/block", map[string]any{"user": user, "block": block}, nil))
}
func (a *App) Friend(user string, add bool) string {
	return errText(a.net.call("/api/friends", map[string]any{"user": user, "add": add}, nil))
}

// ---- [voice] the voice chat (voice.go) ----

func (a *App) VoiceState() VoiceLive             { return a.voice.live() }
func (a *App) VoiceSave(s VoiceSettings)         { a.voice.set(s) }
func (a *App) VoiceToggleMute() bool             { return a.voice.toggleMute(false) }
func (a *App) VoiceDevices() map[string][]string { return a.voice.devices() }

// VoicePeer: my volume (percent) and mute for another player; remembered for the next time.
func (a *App) VoicePeer(user string, volume int, muted bool) { a.voice.setPeer(user, volume, muted) }

// RoomMute: the room's owner silences a player for everybody, or gives the voice back.
func (a *App) RoomMute(user string, mute bool) string {
	return errText(a.net.call("/api/rooms/mute", map[string]any{"user": user, "mute": mute}, nil))
}
func (a *App) Quit() {
	if a.game.running() {
		a.game.stop()
	}
	wruntime.Quit(a.ctx)
}

// inGameInvite: [invite] hands an invitation to the running game's GS host, which draws it over the game and takes the
// pad's answer (pcsx2-gsrunner). If the host does not take it within 2 s (an older host, or the window is busy), the
// launcher comes over the game as before. The answer goes to the server like the interface's buttons.
func (a *App) inGameInvite(inv map[string]any) {
	id, _ := inv["id"].(string)
	from, _ := inv["from"].(string)
	if id == "" || from == "" || strings.ContainsAny(id, "\r\n") {
		return
	}
	g := a.game
	line := "Si aceptas, empieza la partida online en esta ventana."
	_ = os.Remove(g.inviteAnswerPath())
	if os.WriteFile(g.invitePath(), []byte(id+"\n"+safeName(from)+"\n"+line+"\n"), 0o644) != nil {
		a.showOverlay()
		return
	}
	for i := 0; i < 20 && fileExists(g.invitePath()); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if fileExists(g.invitePath()) {
		_ = os.Remove(g.invitePath())
		a.showOverlay()
		return
	}
	deadline := time.Now().Add(65 * time.Second)
	for time.Now().Before(deadline) && g.running() {
		time.Sleep(200 * time.Millisecond)
		b, err := os.ReadFile(g.inviteAnswerPath())
		if err != nil {
			continue
		}
		_ = os.Remove(g.inviteAnswerPath())
		f := strings.Fields(string(b))
		if len(f) != 2 || f[0] != id {
			continue
		}
		accept := f[1] == "accept"
		a.emit("invite_answered", map[string]any{"id": id, "accept": accept})
		if msg := a.Respond(id, accept); msg != "" {
			a.emit("toast", map[string]any{"kind": "bad", "text": msg})
		}
		return
	}
}

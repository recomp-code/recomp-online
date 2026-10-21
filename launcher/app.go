package main

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const launcherVersion = "2.0"

// defaultServer is set at build time: -ldflags "-X main.defaultServer=https://online.tudominio.com"
var defaultServer = "https://online.example.com"

type App struct {
	ctx      context.Context
	root     string
	game     *game
	net      *online
	hk       hotkey
	pad      padWatch
	mu       sync.Mutex
	overlay  bool // the launcher is shown over the running game (Shift+Tab or a notice)
	direct   bool // --jugar: quit when the game closes
	match    *netMatch
	quitting bool
}

func NewApp(root string, direct bool) *App {
	a := &App{root: root, direct: direct}
	a.game = newGame(root)
	a.net = newOnline(a.game.home, a.game.build(), a.emit) // home: where the launcher writes (game_*.go)
	a.net.onEvent = a.onNetEvent
	return a
}

func (a *App) emit(name string, data any) {
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
	if _, ok := a.net.loggedIn(); ok {
		a.net.connect()
	}
	if a.direct && a.game.ready() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := a.Play(); err != "" {
				wruntime.WindowShow(ctx)
			}
		}()
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.hk.stop()
}

// ---- state and settings ----

type State struct {
	Title    string   `json:"title"`
	Version  string   `json:"version"`
	Build    string   `json:"build"`
	Ready    bool     `json:"ready"`
	ISO      string   `json:"iso"`
	Settings Settings `json:"settings"`
	DLSS     DLSSInfo `json:"dlss"`
	GPU      string   `json:"gpu"`
	Server   string   `json:"server"`
	User     string   `json:"user"`
	LoggedIn bool     `json:"loggedIn"`
	Playing  bool     `json:"playing"`
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
	return State{Title: gameTitle, Version: launcherVersion, Build: a.net.build, Ready: a.game.ready(), ISO: s.ISO, Settings: s,
		DLSS: a.game.dlssInfo(), GPU: gpuNames(), Server: server, User: u, LoggedIn: ok, Playing: a.game.running(),
		Platform: platformName, Qualities: qualityOptions, FullscreenKey: fullscreenKey, DataFolder: a.game.dataFolder()}
}

func (a *App) SaveSettings(s Settings) string { return errText(a.game.setSettings(s)) }

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *App) ChooseISO() string {
	p, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: "Elige la ISO de DBZ Budokai Tenkaichi 4 (Beta 14 Rev2)",
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
	links := map[string]string{"github": "https://github.com/recomp-code/dbz-bt4-recomp"}
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
	a.hk.start(func() { a.toggleOverlay() })
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
	wruntime.WindowShow(a.ctx)
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

func (a *App) HideOverlay() {
	if a.game.running() {
		a.setOverlay(false)
		a.toBackground() // the focus goes back to the game
	}
}

func (a *App) onNetEvent(ev map[string]any) {
	switch ev["type"] {
	case "invite":
		if a.game.running() { // the player is in a game: show the notice over it
			a.showOverlay()
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
			if a.game.running() { // an offline game is open: the match replaces it
				a.game.stop()
				for i := 0; i < 50 && a.game.running(); i++ {
					time.Sleep(100 * time.Millisecond)
				}
			}
			a.setOverlay(false)
			wruntime.WindowShow(a.ctx)
			a.emit("match_countdown", map[string]any{"opponent": mm["opponent"], "player": m.Player})
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

func (a *App) Login(user, pass string) string    { return errText(a.net.login(strings.TrimSpace(user), pass, false)) }
func (a *App) Register(user, pass string) string { return errText(a.net.login(strings.TrimSpace(user), pass, true)) }
func (a *App) Logout()                           { a.net.logout() }

// OnlineSnapshot: the interface calls it when it loads, to catch up with the live lobby.
func (a *App) OnlineSnapshot() []map[string]any { return a.net.snapshot() }
func (a *App) SetServer(s string) string         { return errText(a.net.setServer(s)) }
func (a *App) SetWaiting(on bool) string {
	return errText(a.net.call("/api/status", map[string]bool{"waiting": on}, nil))
}
func (a *App) Invite(user string) string {
	s := a.game.settings()
	return errText(a.net.call("/api/invite", map[string]any{"to": user, "settings": map[string]any{"fps60": s.Fps60, "delay": s.NetDelay}}, nil))
}
func (a *App) Respond(id string, accept bool) string {
	return errText(a.net.call("/api/invite/respond", map[string]any{"id": id, "accept": accept}, nil))
}
func (a *App) CreateRoom(name, pass string, max int) string {
	s := a.game.settings()
	return errText(a.net.call("/api/rooms", map[string]any{"name": name, "password": pass, "max": max,
		"settings": map[string]any{"fps60": s.Fps60, "delay": s.NetDelay}}, nil))
}
func (a *App) JoinRoom(id, pass string) string {
	return errText(a.net.call("/api/rooms/join", map[string]string{"id": id, "password": pass}, nil))
}
func (a *App) LeaveRoom() string            { return errText(a.net.call("/api/rooms/leave", nil, nil)) }
func (a *App) StartRoom() string            { return errText(a.net.call("/api/rooms/start", nil, nil)) }
func (a *App) Kick(user string) string      { return errText(a.net.call("/api/rooms/kick", map[string]string{"user": user}, nil)) }
func (a *App) Chat(text string) string      { return errText(a.net.call("/api/chat", map[string]string{"text": text}, nil)) }
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
func (a *App) Friend(user string, add bool) string {
	return errText(a.net.call("/api/friends", map[string]any{"user": user, "add": add}, nil))
}
func (a *App) Quit() {
	if a.game.running() {
		a.game.stop()
	}
	wruntime.Quit(a.ctx)
}

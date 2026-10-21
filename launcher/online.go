package main

// The online client built into the launcher: REST calls over HTTPS for the actions, one WebSocket for the live
// lobby (who is online and waiting, invitations, rooms, matches, chat). The login token is kept encrypted with
// Windows DPAPI (only this Windows user on this PC can read it); the password itself is never stored.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type onlineCfg struct {
	Server string `json:"server"`
	User   string `json:"user"`
	Token  string `json:"token,omitempty"` // DPAPI-encrypted, base64
}

type online struct {
	mu      sync.Mutex
	path    string
	cfg     onlineCfg
	token   string // decrypted, in memory only
	game    string // the game the player is in (the lobby, rooms and stats are per game)
	build   string
	http    *http.Client
	cancel  context.CancelFunc
	emit    func(name string, data any)
	onEvent func(ev map[string]any) // the app reacts to matches
	// the latest live state, so the interface can catch up whenever it (re)loads: events emitted before the page
	// listened would otherwise be lost
	connected bool
	lastLobby map[string]any
	lastRoom  map[string]any
	lastMe    string
}

// snapshot: the events that rebuild the interface's view of the lobby.
func (o *online) snapshot() []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []map[string]any
	if o.connected {
		out = append(out, map[string]any{"type": "hello", "user": o.lastMe})
	} else if o.token != "" {
		out = append(out, map[string]any{"type": "disconnected"})
	}
	if o.lastLobby != nil {
		out = append(out, o.lastLobby)
	}
	if o.lastRoom != nil {
		out = append(out, o.lastRoom)
	}
	return out
}

func (o *online) remember(ev map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch ev["type"] {
	case "connected":
		o.connected = true
	case "disconnected", "replaced":
		o.connected = false
	case "logged_out":
		o.connected, o.lastLobby, o.lastRoom = false, nil, nil
	case "hello":
		o.connected = true
		o.lastMe, _ = ev["user"].(string)
		o.lastRoom = nil // the server resends the room right after hello if the player is still in one
	case "lobby":
		o.lastLobby = ev
	case "room":
		o.lastRoom = ev
	case "room_left":
		o.lastRoom = nil
	}
}

func newOnline(root, game, build string, emit func(string, any)) *online {
	o := &online{path: filepath.Join(root, "online.json"), game: game, build: build,
		http: &http.Client{Timeout: 15 * time.Second}}
	o.emit = func(name string, data any) {
		if ev, ok := data.(map[string]any); ok && name == "online" {
			o.remember(ev)
		}
		emit(name, data)
	}
	_ = readJSONFile(o.path, &o.cfg)
	if o.cfg.Server == "" {
		o.cfg.Server = defaultServer
	}
	if o.cfg.Token != "" {
		if enc, err := base64.StdEncoding.DecodeString(o.cfg.Token); err == nil {
			if b, err := unprotect(enc); err == nil {
				o.token = string(b)
			}
		}
	}
	return o
}

func readJSONFile(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}), v) // a file saved by Notepad starts with a BOM
}

func (o *online) saveLocked() {
	o.cfg.Token = ""
	if o.token != "" {
		if enc, err := protect([]byte(o.token)); err == nil {
			o.cfg.Token = base64.StdEncoding.EncodeToString(enc)
		}
	}
	b, _ := json.MarshalIndent(o.cfg, "", "  ")
	_ = os.WriteFile(o.path, b, 0o600)
}

// server URL: only https (a plain http address is accepted for localhost, for tests)
func validServer(s string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(s), "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("dirección del servidor no válida")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (strings.HasPrefix(u.Host, "127.0.0.1") || strings.HasPrefix(u.Host, "localhost"))) {
		return "", errors.New("el servidor tiene que ser https://")
	}
	return u.Scheme + "://" + u.Host, nil
}

type apiError struct{ msg string }

func (e apiError) Error() string { return e.msg }

func (o *online) call(path string, body, out any) error {
	o.mu.Lock()
	server, tok := o.cfg.Server, o.token
	o.mu.Unlock()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", server+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return errors.New("no hay conexión con el servidor")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("error del servidor (%d)", resp.StatusCode)
		}
		if resp.StatusCode == 401 && path != "/api/login" {
			o.forget()
		}
		return apiError{e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// raw: a request with a binary body (the match's memory card). got(status, body) sees any 2xx answer; other
// statuses come back as the server's error.
func (o *online) raw(method, path string, body []byte, got func(int, []byte)) error {
	o.mu.Lock()
	server, tok := o.cfg.Server, o.token
	o.mu.Unlock()
	req, err := http.NewRequest(method, server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return errors.New("no hay conexión con el servidor")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("error del servidor (%d)", resp.StatusCode)
		}
		return apiError{e.Error}
	}
	if got != nil {
		got(resp.StatusCode, data)
	}
	return nil
}

func (o *online) loggedIn() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cfg.User, o.token != ""
}

func (o *online) login(user, pass string, register bool) error {
	path := "/api/login"
	if register {
		path = "/api/register"
	}
	var out struct{ Token, User string }
	if err := o.call(path, map[string]string{"user": user, "password": pass}, &out); err != nil {
		return err
	}
	o.mu.Lock()
	o.token, o.cfg.User = out.Token, out.User
	o.saveLocked()
	o.mu.Unlock()
	o.connect()
	return nil
}

func (o *online) forget() {
	o.mu.Lock()
	o.token = ""
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	o.saveLocked()
	o.mu.Unlock()
	o.emit("online", map[string]any{"type": "logged_out"})
}

func (o *online) logout() {
	_ = o.call("/api/logout", nil, nil)
	o.forget()
}

func (o *online) setServer(s string) error {
	v, err := validServer(s)
	if err != nil {
		return err
	}
	o.mu.Lock()
	changed := v != o.cfg.Server
	o.cfg.Server = v
	if changed {
		o.token = "" // a session belongs to its server
	}
	o.saveLocked()
	o.mu.Unlock()
	return nil
}

// connect keeps the live channel open (reconnecting with a growing pause) until logout.
func (o *online) connect() {
	o.mu.Lock()
	if o.cancel != nil {
		o.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	server, tok, game, build := o.cfg.Server, o.token, o.game, o.build
	o.mu.Unlock()
	if tok == "" {
		return
	}
	go func() {
		// [reconnect] the first retry is immediate, then 0.5 s, 1 s, 2 s, 4 s and every 5 s; a connection that lasted
		// a while resets the sequence. The server keeps the player's place (presence, room, invitations) for 15 s.
		pause := time.Duration(0)
		for ctx.Err() == nil {
			t0 := time.Now()
			err := o.stream(ctx, server, tok, game, build)
			if ctx.Err() != nil {
				return
			}
			var ae apiError
			if errors.As(err, &ae) {
				o.forget()
				return
			}
			if time.Since(t0) > 30*time.Second {
				pause = 0
			}
			o.emit("online", map[string]any{"type": "disconnected", "retry": pause.Seconds()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
			switch {
			case pause == 0:
				pause = 500 * time.Millisecond
			case pause < 5*time.Second:
				pause = min(pause*2, 5*time.Second)
			}
		}
	}()
}

func (o *online) stream(ctx context.Context, server, tok, game, build string) error {
	u := "ws" + strings.TrimPrefix(server, "http") + "/api/ws?game=" + url.QueryEscape(game) + "&build=" + url.QueryEscape(build)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			return apiError{"sesión caducada"}
		}
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	o.emit("online", map[string]any{"type": "connected"})
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
				o.emit("online", map[string]any{"type": "replaced"})
				<-ctx.Done() // another launcher took over this account: stop reconnecting
			}
			return err
		}
		var ev map[string]any
		if json.Unmarshal(b, &ev) != nil {
			continue
		}
		if o.onEvent != nil {
			o.onEvent(ev)
		}
		o.emit("online", ev)
	}
}

// setGame: the player switched games in the launcher: the live channel reconnects as that game (its lobby, rooms and
// build). Nothing to do when it is the same.
func (o *online) setGame(game, build string) {
	o.mu.Lock()
	same := o.game == game && o.build == build
	o.game, o.build = game, build
	if !same {
		o.lastLobby, o.lastRoom = nil, nil
	}
	tok := o.token
	o.mu.Unlock()
	if !same && tok != "" {
		o.connect()
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type tclient struct {
	t      *testing.T
	base   string
	token  string
	user   string
	events chan map[string]any
}

func post(t *testing.T, base, path, token string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func newTClient(t *testing.T, base, user, pass, game, build string) *tclient {
	code, out := post(t, base, "/api/register", "", map[string]string{"user": user, "password": pass})
	if code != 200 {
		t.Fatalf("register %s: %d %v", user, code, out)
	}
	c := &tclient{t: t, base: base, token: out["token"].(string), user: user, events: make(chan map[string]any, 100)}
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/api/ws?game=" + game + "&build=" + build
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}}})
	if err != nil {
		t.Fatalf("ws %s: %v", user, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var ev map[string]any
			if json.Unmarshal(b, &ev) == nil {
				c.events <- ev
			}
		}
	}()
	return c
}

// wait for an event of that type (skipping others)
func (c *tclient) wait(typ string) map[string]any {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev["type"] == typ {
				return ev
			}
		case <-deadline:
			c.t.Fatalf("%s: no %q event", c.user, typ)
		}
	}
}

func udpRegister(t *testing.T, relay, token string, player int) *net.UDPConn {
	ra, _ := net.ResolveUDPAddr("udp", relay)
	c, err := net.DialUDP("udp", nil, ra)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("RLY1" + token + string(rune('0'+player))))
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil || n != 6 || string(buf[:4]) != "RLYA" {
		t.Fatalf("relay ack for player %d: %v %q", player, err, buf[:n])
	}
	return c
}

func expectUDP(t *testing.T, c *net.UDPConn, want string) {
	t.Helper()
	buf := make([]byte, 1600)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != want {
		t.Fatalf("relay forward: got %q err %v, want %q", buf[:n], err, want)
	}
}

func testServer(t *testing.T) (string, string) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rl, err := newRelay("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relayAddr := rl.conn.LocalAddr().String()
	h := newHub(st, rl, relayAddr)
	srv := httptest.NewServer((&api{st: st, hub: h, limiter: newLimiter(), conns: newConnLimit()}).routes())
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() }) // the event streams never end on their own
	return srv.URL, relayAddr
}

func TestInviteMatchRelay(t *testing.T) {
	base, relay := testServer(t)
	a := newTClient(t, base, "Goku", "kamehameha", "dbzbt4", "1.4")
	b := newTClient(t, base, "Vegeta", "finalflash", "dbzbt4", "1.4")
	a.wait("hello")
	b.wait("hello")

	// wrong password is refused, the right one works
	if code, _ := post(t, base, "/api/login", "", map[string]string{"user": "goku", "password": "nope"}); code != 401 {
		t.Fatalf("bad password accepted: %d", code)
	}
	if code, _ := post(t, base, "/api/login", "", map[string]string{"user": "goku", "password": "kamehameha"}); code != 200 {
		t.Fatalf("login failed: %d", code)
	}

	// Vegeta waits; Goku sees him waiting in the lobby
	post(t, base, "/api/status", b.token, map[string]bool{"waiting": true})
	for {
		ev := a.wait("lobby")
		found := false
		for _, p := range ev["players"].([]any) {
			pm := p.(map[string]any)
			if pm["user"] == "Vegeta" && pm["status"] == "waiting" {
				found = true
			}
		}
		if found {
			break
		}
	}

	code, inv := post(t, base, "/api/invite", a.token, map[string]any{"to": "vegeta", "settings": map[string]any{"fps60": true}})
	if code != 200 {
		t.Fatalf("invite: %d %v", code, inv)
	}
	got := b.wait("invite")["invite"].(map[string]any)
	if got["from"] != "Goku" {
		t.Fatalf("invite from %v", got["from"])
	}
	if code, out := post(t, base, "/api/invite/respond", b.token, map[string]any{"id": got["id"], "accept": true}); code != 200 {
		t.Fatalf("accept: %d %v", code, out)
	}
	ma := a.wait("match")["match"].(map[string]any)
	mb := b.wait("match")["match"].(map[string]any)
	if ma["token"] != mb["token"] || ma["player"].(float64) != 1 || mb["player"].(float64) != 2 || ma["relay"] != relay {
		t.Fatalf("match mismatch: %v / %v", ma, mb)
	}
	if !mb["settings"].(map[string]any)["fps60"].(bool) {
		t.Fatalf("settings not passed to the invitee")
	}

	// both games register; packets cross both ways untouched
	tok := ma["token"].(string)
	u1 := udpRegister(t, relay, tok, 1)
	u2 := udpRegister(t, relay, tok, 2)
	_, _ = u1.Write([]byte("hello from p1"))
	expectUDP(t, u2, "hello from p1")
	_, _ = u2.Write([]byte("hello from p2"))
	expectUDP(t, u1, "hello from p2")

	// an unknown token gets no ack and is not forwarded
	ra, _ := net.ResolveUDPAddr("udp", relay)
	x, _ := net.DialUDP("udp", nil, ra)
	_, _ = x.Write([]byte("RLY1" + strings.Repeat("0", 32) + "1"))
	_ = x.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := x.Read(make([]byte, 16)); err == nil {
		t.Fatalf("unknown token acked (%d bytes)", n)
	}

	// both launchers report the end: the match closes, the relay forgets the token
	post(t, base, "/api/match/end", a.token, map[string]string{"id": ma["id"].(string)})
	post(t, base, "/api/match/end", b.token, map[string]string{"id": ma["id"].(string)})
	a.wait("match_end")
	b.wait("match_end")
	_, _ = u1.Write([]byte("after the end"))
	_ = u2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := u2.Read(make([]byte, 64)); err == nil {
		t.Fatalf("relay still forwarding after the end (%d bytes)", n)
	}
}

func TestRoomPasswordRotationAndBuild(t *testing.T) {
	base, _ := testServer(t)
	p := []*tclient{
		newTClient(t, base, "Owner", "secret1", "dbzbt4", "1.4"),
		newTClient(t, base, "Second", "secret2", "dbzbt4", "1.4"),
		newTClient(t, base, "Third", "secret3", "dbzbt4", "1.4"),
		newTClient(t, base, "OldBuild", "secret4", "dbzbt4", "1.3"),
	}
	code, rm := post(t, base, "/api/rooms", p[0].token, map[string]any{"name": "Torneo", "password": "zeni", "max": 4})
	if code != 200 {
		t.Fatalf("create room: %d %v", code, rm)
	}
	id := rm["id"].(string)
	if code, _ := post(t, base, "/api/rooms/join", p[1].token, map[string]any{"id": id, "password": "wrong"}); code != 409 {
		t.Fatalf("wrong room password accepted: %d", code)
	}
	for _, c := range p[1:3] {
		if code, out := post(t, base, "/api/rooms/join", c.token, map[string]any{"id": id, "password": "zeni"}); code != 200 {
			t.Fatalf("join %s: %d %v", c.user, code, out)
		}
	}
	if code, _ := post(t, base, "/api/rooms/join", p[3].token, map[string]any{"id": id, "password": "zeni"}); code != 409 {
		t.Fatalf("other build joined: %d", code)
	}
	if code, _ := post(t, base, "/api/rooms/start", p[1].token, nil); code != 409 {
		t.Fatalf("a non-owner started the room")
	}
	if code, out := post(t, base, "/api/rooms/start", p[0].token, nil); code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	m1 := p[0].wait("match")["match"].(map[string]any)
	if m1["opponent"] != "Second" {
		t.Fatalf("first match against %v", m1["opponent"])
	}
	post(t, base, "/api/match/end", p[0].token, map[string]string{"id": m1["id"].(string)})
	post(t, base, "/api/match/end", p[1].token, map[string]string{"id": m1["id"].(string)})
	p[0].wait("match_end")
	// [rotation] the two who played go to the back: Third, Owner, Second
	for {
		r := p[2].wait("room")["room"].(map[string]any)
		ms := r["members"].([]any)
		if r["match"] == nil && len(ms) == 3 && ms[0] == "Third" && ms[1] == "Owner" && ms[2] == "Second" {
			break
		}
	}
	if code, out := post(t, base, "/api/rooms/start", p[0].token, nil); code != 200 {
		t.Fatalf("second start: %d %v", code, out)
	}
	m2 := p[2].wait("match")["match"].(map[string]any)
	if m2["opponent"] != "Owner" || m2["player"].(float64) != 1 {
		t.Fatalf("second match: %v", m2)
	}
}

func TestSecurity(t *testing.T) {
	base, relay := testServer(t)
	a := newTClient(t, base, "Krilin", "destructo", "dbzbt4", "1.4")
	b := newTClient(t, base, "Yamcha", "lobofang", "dbzbt4", "1.4")
	a.wait("hello")
	b.wait("hello")

	// a token in the URL is not accepted (it would leak into logs)
	resp, err := http.Get(base + "/api/rooms/leave?token=" + a.token)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 405 && resp.StatusCode != 401 {
			t.Fatalf("token in URL: %d", resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", base+"/api/rooms/leave?token="+a.token, nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("token in URL accepted: %v %v", err, resp.StatusCode)
	}
	// security headers on every answer
	if resp, err := http.Get(base + "/api/health"); err != nil || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers missing")
	}

	// login brute force: the 11th try in a minute is refused even with the right password
	for i := 0; i < 10; i++ {
		post(t, base, "/api/login", "", map[string]string{"user": "krilin", "password": "wrong"})
	}
	if code, _ := post(t, base, "/api/login", "", map[string]string{"user": "krilin", "password": "destructo"}); code != 429 {
		t.Fatalf("login limiter: %d", code)
	}

	// relay: once player 1 is live, another address with the same token cannot take the slot
	post(t, base, "/api/invite", a.token, map[string]any{"to": "Yamcha"})
	inv := b.wait("invite")["invite"].(map[string]any)
	post(t, base, "/api/invite/respond", b.token, map[string]any{"id": inv["id"], "accept": true})
	tok := a.wait("match")["match"].(map[string]any)["token"].(string)
	u1 := udpRegister(t, relay, tok, 1)
	u2 := udpRegister(t, relay, tok, 2)
	_, _ = u1.Write([]byte("live"))
	expectUDP(t, u2, "live")
	ra, _ := net.ResolveUDPAddr("udp", relay)
	thief, _ := net.DialUDP("udp", nil, ra)
	_, _ = thief.Write([]byte("RLY1" + tok + "1"))
	_ = thief.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := thief.Read(make([]byte, 16)); err == nil {
		t.Fatalf("hijack registration acked (%d bytes)", n)
	}
	_, _ = u2.Write([]byte("to the real p1"))
	expectUDP(t, u1, "to the real p1")
	_ = thief.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := thief.Read(make([]byte, 64)); err == nil {
		t.Fatalf("the hijacker received p2's packets")
	}
}

func TestSessionsAtRestAreHashed(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	if err := st.register("Piccolo", "makankosappo"); err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.login("Piccolo", "makankosappo")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "sessions.json"))
	if strings.Contains(string(raw), tok) {
		t.Fatalf("the session token is stored in clear")
	}
	users, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if strings.Contains(string(users), "makankosappo") {
		t.Fatalf("the password is stored in clear")
	}
	st2, _ := openStore(dir) // a restart keeps the session valid
	if u, ok := st2.userForToken(tok); !ok || u != "Piccolo" {
		t.Fatalf("session lost across restart")
	}
}
// dialWS opens a live channel with an existing token (a reconnect).
func dialWS(t *testing.T, base, token, game, build string) (*websocket.Conn, chan map[string]any) {
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/api/ws?game=" + game + "&build=" + build
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	ch := make(chan map[string]any, 100)
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var ev map[string]any
			if json.Unmarshal(b, &ev) == nil {
				ch <- ev
			}
		}
	}()
	return conn, ch
}

func TestReconnectGrace(t *testing.T) {
	base, _ := testServer(t)
	a := newTClient(t, base, "Gohan", "masenko1", "dbzbt4", "1.4")
	a.wait("hello")
	code, out := post(t, base, "/api/register", "", map[string]string{"user": "Trunks", "password": "burning1"})
	if code != 200 {
		t.Fatal(out)
	}
	tok := out["token"].(string)
	c1, e1 := dialWS(t, base, tok, "dbzbt4", "1.4")
	(&tclient{t: t, user: "Trunks", events: e1}).wait("hello")
	// Trunks' connection drops; Gohan invites him during the blip; nobody sees him go offline
	c1.CloseNow()
	time.Sleep(200 * time.Millisecond)
	if code, out := post(t, base, "/api/invite", a.token, map[string]any{"to": "Trunks"}); code != 200 {
		t.Fatalf("invite during the blip: %d %v", code, out)
	}
	_, e2 := dialWS(t, base, tok, "dbzbt4", "1.4")
	tc := &tclient{t: t, user: "Trunks", events: e2}
	tc.wait("hello")
	inv := tc.wait("invite")["invite"].(map[string]any)
	if inv["from"] != "Gohan" {
		t.Fatalf("invite not resent after reconnect: %v", inv)
	}
	if code, out := post(t, base, "/api/invite/respond", tok, map[string]any{"id": inv["id"], "accept": true}); code != 200 {
		t.Fatalf("accept after reconnect: %d %v", code, out)
	}
	a.wait("match")
}
// search by nickname: every account, online or not, those starting with the text first; the lobby message
// carries the player's own friends list.
func TestPlayerSearch(t *testing.T) {
	base, _ := testServer(t)
	a := newTClient(t, base, "Goku", "kamehameha", "dbzbt4", "b1")
	newTClient(t, base, "Gohan", "masenko123", "dbzbt4", "b1")
	if code, _ := post(t, base, "/api/register", "", map[string]string{"user": "Goten", "password": "trunks123"}); code != 200 {
		t.Fatal("register Goten")
	}
	post(t, base, "/api/register", "", map[string]string{"user": "SonGoku", "password": "kakarot12"})

	names := func(q string) ([]string, []map[string]any) {
		code, out := post(t, base, "/api/users/search", a.token, map[string]string{"q": q})
		if code != 200 {
			t.Fatalf("search %q: %d %v", q, code, out)
		}
		var n []string
		var rows []map[string]any
		for _, u := range out["users"].([]any) {
			m := u.(map[string]any)
			n = append(n, m["user"].(string))
			rows = append(rows, m)
		}
		return n, rows
	}
	got, rows := names("go")
	if strings.Join(got, ",") != "Gohan,Goku,Goten,SonGoku" {
		t.Fatalf("search go: %v", got)
	}
	if rows[0]["status"] != "lobby" || rows[0]["game"] != "dbzbt4" || rows[2]["status"] != "" {
		t.Fatalf("search status: %v", rows)
	}
	if got, _ := names("HAN"); strings.Join(got, ",") != "Gohan" {
		t.Fatalf("search HAN: %v", got)
	}
	if code, _ := post(t, base, "/api/users/search", a.token, map[string]string{"q": "g"}); code != 400 {
		t.Fatalf("one letter accepted: %d", code)
	}
	if code, _ := post(t, base, "/api/users/search", "", map[string]string{"q": "go"}); code != 401 {
		t.Fatalf("search without login: %d", code)
	}

	post(t, base, "/api/friends", a.token, map[string]any{"user": "goten", "add": true})
	for {
		ev := a.wait("lobby")
		if f, _ := ev["friends"].([]any); len(f) == 1 && f[0] == "Goten" {
			break
		}
	}
	if _, rows := names("goten"); rows[0]["friend"] != true {
		t.Fatalf("friend flag: %v", rows)
	}
}

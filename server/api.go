package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var gameRe = regexp.MustCompile(`^[a-z0-9_\-]{2,24}$`)

type api struct {
	st      *store
	hub     *hub
	limiter *limiter
	conns   *connLimit
	lookups *limiter // player search: generous (the launcher searches as you type) but bounded
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSONResp(w, code, map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.New("petición mal formada")
	}
	return nil
}

func clientIP(r *http.Request) string {
	// Behind Caddy on the same machine: trust X-Forwarded-For only from loopback, and only its LAST entry (the one
	// Caddy added). The first entries come from the client and can be anything, which would let anyone dodge the
	// login limiter by sending a new fake address each time.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			parts := strings.Split(f, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

// auth returns the user of the request's bearer token.
func (a *api) auth(w http.ResponseWriter, r *http.Request) (string, bool) {
	// only in the header: a token in the URL would end up in proxy and server logs
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u, ok := a.st.userForToken(tok)
	if !ok {
		fail(w, http.StatusUnauthorized, errors.New("sesión caducada: entra otra vez"))
		return "", false
	}
	return u, true
}

func (a *api) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		on, rooms, matches := a.hub.counts()
		relays, packets := a.hub.rl.stats()
		writeJSONResp(w, 200, map[string]any{"ok": true, "online": on, "rooms": rooms, "matches": matches, "relays": relays, "relayed": packets})
	})
	mux.HandleFunc("POST /api/register", a.register)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		a.st.logout(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/password", a.password)
	mux.HandleFunc("GET /api/ws", a.ws)
	mux.HandleFunc("POST /api/status", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ Waiting bool }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		a.hub.setWaiting(u, b.Waiting)
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/invite", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct {
			To       string
			Settings settings
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		inv, err := a.hub.invite(u, b.To, b.Settings)
		if err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, inv)
	}))
	mux.HandleFunc("POST /api/invite/respond", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct {
			ID     string
			Accept bool
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.hub.respond(u, b.ID, b.Accept); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name, Password string
			Max            int
			Settings       settings
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		rm, err := a.hub.createRoom(u, b.Name, b.Password, b.Max, b.Settings)
		if err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, rm)
	}))
	mux.HandleFunc("POST /api/rooms/join", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ ID, Password string }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if !a.limiter.allow("room:" + clientIP(r)) {
			fail(w, 429, errors.New("demasiados intentos: espera un minuto"))
			return
		}
		if err := a.hub.joinRoom(u, b.ID, b.Password); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms/leave", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		a.hub.leaveRoom(u)
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms/start", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		if err := a.hub.startRoom(u); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms/kick", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ User string }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.hub.kick(u, b.User); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/chat", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ Text string }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.hub.chat(u, b.Text); err != nil {
			fail(w, 409, err)
			return
		}
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/match/end", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ ID string }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		a.hub.reportEnd(u, b.ID)
		writeJSONResp(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/friends", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct {
			User string
			Add  bool
		}
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.st.setFriend(u, b.User, b.Add); err != nil {
			fail(w, 409, err)
			return
		}
		a.hub.friendsChanged(u)
		writeJSONResp(w, 200, map[string]any{"friends": a.st.friends(u)})
	}))
	if a.lookups == nil {
		a.lookups = newLimiterN(60)
	}
	// player search by nickname: every account (online or not, in any game), to invite or add as a friend
	mux.HandleFunc("POST /api/users/search", a.withUser(func(u string, w http.ResponseWriter, r *http.Request) {
		var b struct{ Q string }
		if err := readBody(r, &b); err != nil {
			fail(w, 400, err)
			return
		}
		q := strings.TrimSpace(b.Q)
		if n := len([]rune(q)); n < 2 || n > 16 {
			fail(w, 400, errors.New("escribe de 2 a 16 letras del nombre"))
			return
		}
		if !a.lookups.allow("search:" + strings.ToLower(u)) {
			fail(w, 429, errors.New("demasiadas búsquedas: espera un momento"))
			return
		}
		friends := map[string]bool{}
		for _, f := range a.st.friends(u) {
			friends[strings.ToLower(f)] = true
		}
		out := []map[string]any{}
		for _, name := range a.st.search(q, 20) {
			game, status := a.hub.whereIs(name)
			out = append(out, map[string]any{"user": name, "friend": friends[strings.ToLower(name)], "game": game, "status": status})
		}
		writeJSONResp(w, 200, map[string]any{"users": out})
	}))
	return securityHeaders(mux)
}

// securityHeaders: an API, never a page to render or cache.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *api) withUser(f func(string, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := a.auth(w, r); ok {
			f(u, w, r)
		}
	}
}

func (a *api) register(w http.ResponseWriter, r *http.Request) {
	var b struct{ User, Password string }
	if err := readBody(r, &b); err != nil {
		fail(w, 400, err)
		return
	}
	if !a.limiter.allow("reg:" + clientIP(r)) {
		fail(w, 429, errors.New("demasiados intentos: espera un minuto"))
		return
	}
	if err := a.st.register(strings.TrimSpace(b.User), b.Password); err != nil {
		fail(w, 409, err)
		return
	}
	tok, name, err := a.st.login(strings.TrimSpace(b.User), b.Password)
	if err != nil {
		fail(w, 500, err)
		return
	}
	log.Printf("new account: %s", name)
	writeJSONResp(w, 200, map[string]string{"token": tok, "user": name})
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	var b struct{ User, Password string }
	if err := readBody(r, &b); err != nil {
		fail(w, 400, err)
		return
	}
	if !a.limiter.allow("login:" + clientIP(r)) {
		fail(w, 429, errors.New("demasiados intentos: espera un minuto"))
		return
	}
	tok, name, err := a.st.login(strings.TrimSpace(b.User), b.Password)
	if err != nil {
		fail(w, 401, err)
		return
	}
	writeJSONResp(w, 200, map[string]string{"token": tok, "user": name})
}

func (a *api) password(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	var b struct{ Old, New string }
	if err := readBody(r, &b); err != nil {
		fail(w, 400, err)
		return
	}
	if err := a.st.changePassword(u, b.Old, b.New); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

// ws: the launcher's live channel (WebSocket). The server pushes one JSON object per message (lobby, invitations,
// rooms, matches, chat); the launcher's actions go through the REST calls above. The client answers pings, and a
// dead connection is noticed within a minute.
func (a *api) ws(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	game, build := r.URL.Query().Get("game"), r.URL.Query().Get("build")
	if !gameRe.MatchString(game) || len(build) > 32 {
		fail(w, 400, errors.New("juego o versión no válidos"))
		return
	}
	ip := clientIP(r)
	if !a.conns.acquire(ip) {
		fail(w, 429, errors.New("demasiadas conexiones desde tu red"))
		return
	}
	defer a.conns.release(ip)
	// The launcher is a native client (no Origin header). A browser page from another site could still open a socket
	// with a stolen token only if it had the token, and Origin is checked against our own host when present.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(4 << 10)
	ctx := c.CloseRead(r.Context()) // nothing is read: the context ends when the client goes away
	cl := a.hub.attach(u, game, build)
	defer a.hub.detach(cl)
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-cl.ch:
			if !ok {
				_ = c.Close(websocket.StatusPolicyViolation, "sesión abierta en otro launcher")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// connLimit: live channels per IP (a household behind one NAT has a few) and in total.
type connLimit struct {
	mu    sync.Mutex
	perIP map[string]int
	total int
}

const (
	maxConnsPerIP = 16
	maxConnsTotal = 5000
)

func newConnLimit() *connLimit { return &connLimit{perIP: map[string]int{}} }

func (l *connLimit) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP[ip] >= maxConnsPerIP || l.total >= maxConnsTotal {
		return false
	}
	l.perIP[ip]++
	l.total++
	return true
}

func (l *connLimit) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP[ip]--; l.perIP[ip] <= 0 {
		delete(l.perIP, ip)
	}
	l.total--
}
// limiter: at most max attempts per key per minute (10 for logins, registrations, room passwords).
type limiter struct {
	mu   sync.Mutex
	max  int
	hits map[string][]time.Time
}

func newLimiter() *limiter        { return newLimiterN(10) }
func newLimiterN(n int) *limiter { return &limiter{max: n, hits: map[string][]time.Time{}} }

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < time.Minute {
			h = append(h, t)
		}
	}
	if len(h) >= l.max {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 { // forget idle keys
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Minute {
				delete(l.hits, k)
			}
		}
	}
	return true
}

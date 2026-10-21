package main

// The lobby: who is online in each game (and whether they are waiting for a match), invitations,
// rooms and matches. Launchers keep one WebSocket open; everything they need to draw arrives on it
// as JSON messages, so the API calls only change state.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type settings struct {
	Fps60 bool `json:"fps60"`
	Delay int  `json:"delay"` // input delay in frames; 0 = the game's default
}

type client struct {
	user  string
	game  string
	build string
	ch    chan []byte
	since time.Time
	gone  bool // the stream ended; the place is kept for reconnectGrace
}

// reconnectGrace: a launcher whose connection drops keeps its place (presence, room, invitations) this long, so a
// network blip or a server restart is invisible to the others; the launcher retries at once.
const reconnectGrace = 15 * time.Second

type presence struct {
	User   string `json:"user"`
	Build  string `json:"build"`
	Status string `json:"status"` // lobby | waiting | room | playing
	Room   string `json:"room,omitempty"`
}

// lobbyBatch: lobby changes that land this close together go out as one update (a room starting a match changes
// several players at once), and the list is built and encoded once per game, not once per player.
const lobbyBatch = 40 * time.Millisecond

type invite struct {
	ID       string    `json:"id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Game     string    `json:"game"`
	Settings settings  `json:"settings"`
	Expires  time.Time `json:"expires"`
}

type room struct {
	ID       string   `json:"id"`
	Game     string   `json:"game"`
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Locked   bool     `json:"locked"`
	Max      int      `json:"max"`
	Members  []string `json:"members"` // queue order: the first two play the next match
	Settings settings `json:"settings"`
	Build    string   `json:"build"`
	Match    string   `json:"match,omitempty"`
	passSalt string
	passHash string
}

type match struct {
	ID       string    `json:"id"`
	Game     string    `json:"game"`
	P1       string    `json:"p1"`
	P2       string    `json:"p2"`
	Room     string    `json:"room,omitempty"`
	Settings settings  `json:"settings"`
	Started  time.Time `json:"started"`
	token    string
	card     []byte // [netcard] player 1's memory card for this match (both games boot from it); in memory only
}

// maxCard: a BT4 save is 16 KB plus its icon; anything near this is not a memory card.
const maxCard = 512 << 10

var errNoMatch = errors.New("esa partida ya no existe")

// setCard: player 1 hands in the memory card both games of the match boot from. Once per match.
func (h *hub) setCard(user, id string, card []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.matches[id]
	if m == nil {
		return errNoMatch
	}
	if !strings.EqualFold(user, m.P1) {
		return errors.New("la memory card la pone el jugador 1")
	}
	if m.card != nil {
		return errors.New("esta partida ya tiene memory card")
	}
	m.card = append([]byte(nil), card...)
	return nil
}

// card: the match's memory card, for its two players; nil (and no error) while player 1 has not sent it yet.
func (h *hub) card(user, id string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.matches[id]
	if m == nil {
		return nil, errNoMatch
	}
	if !strings.EqualFold(user, m.P1) && !strings.EqualFold(user, m.P2) {
		return nil, errNoMatch // not a player of this match: it does not exist for them
	}
	return m.card, nil
}

type hub struct {
	mu        sync.Mutex
	st        *store
	rl        *relay
	relayAddr string
	clients   map[string]*client // by lower-case user (one stream per account)
	invites   map[string]*invite
	rooms     map[string]*room
	matches   map[string]*match
	playing   map[string]string // user -> match id
	inRoom    map[string]string // user -> room id
	waiting   map[string]bool
	dirty     map[string]bool // games whose lobby changed since the last flush
	flushing  bool
	stats     *statsStore // [stats] fights and time of the online matches (nil: not kept)
}

func newHub(st *store, rl *relay, relayAddr string) *hub {
	h := &hub{st: st, rl: rl, relayAddr: relayAddr, clients: map[string]*client{}, invites: map[string]*invite{},
		rooms: map[string]*room{}, matches: map[string]*match{}, playing: map[string]string{}, inRoom: map[string]string{}, waiting: map[string]bool{}, dirty: map[string]bool{}}
	go func() {
		for range time.Tick(10 * time.Second) {
			h.expireInvites()
		}
	}()
	return h
}

func lc(s string) string { return strings.ToLower(s) }

func (h *hub) sendLocked(user string, v any) {
	c := h.clients[lc(user)]
	if c == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	sendTo(c, b)
}

func sendTo(c *client, b []byte) {
	select {
	case c.ch <- b:
	default: // a stuck client loses events rather than blocking the lobby
	}
}

func (h *hub) statusLocked(user string) (string, string) {
	k := lc(user)
	if _, ok := h.playing[k]; ok {
		return "playing", h.inRoom[k]
	}
	if r, ok := h.inRoom[k]; ok {
		return "room", r
	}
	if h.waiting[k] {
		return "waiting", ""
	}
	return "lobby", ""
}

// lobbyOf: everybody online in a game, by name. It is the same for every player; each one also gets their own
// friends list and the launcher puts those first.
func (h *hub) lobbyOf(game string) []presence {
	out := []presence{}
	for _, c := range h.clients {
		if c.game != game {
			continue
		}
		st, r := h.statusLocked(c.user)
		out = append(out, presence{User: c.user, Build: c.build, Status: st, Room: r})
	}
	sort.Slice(out, func(i, j int) bool { return lc(out[i].User) < lc(out[j].User) })
	return out
}

func (h *hub) roomsFor(game string) []*room {
	out := []*room{}
	for _, r := range h.rooms {
		if r.Game == game {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return lc(out[i].Name) < lc(out[j].Name) })
	return out
}

// broadcastLocked schedules the lobby and room list of a game for every one of its clients (see lobbyBatch).
func (h *hub) broadcastLocked(game string) {
	h.dirty[game] = true
	if !h.flushing {
		h.flushing = true
		time.AfterFunc(lobbyBatch, h.flush)
	}
}

func (h *hub) flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushing = false
	for game := range h.dirty {
		delete(h.dirty, game)
		shared := h.lobbyJSON(game)
		for _, c := range h.clients {
			if c.game == game && !c.gone {
				sendTo(c, h.lobbyMsg(shared, c.user))
			}
		}
	}
}

// lobbyJSON encodes the part of the lobby message every player of a game shares.
func (h *hub) lobbyJSON(game string) []byte {
	b, _ := json.Marshal(struct {
		Type    string     `json:"type"`
		Players []presence `json:"players"`
		Rooms   []*room    `json:"rooms"`
	}{"lobby", h.lobbyOf(game), h.roomsFor(game)})
	return b
}

// lobbyMsg completes the shared part with the player's own friends and blocked players:
// {...,"friends":[...],"blocked":[...]} (the launcher hides the blocked ones).
func (h *hub) lobbyMsg(shared []byte, user string) []byte {
	f, bl := h.st.friends(user), h.st.blocked(user)
	if f == nil {
		f = []string{}
	}
	if bl == nil {
		bl = []string{}
	}
	fb, _ := json.Marshal(f)
	bb, _ := json.Marshal(bl)
	out := make([]byte, 0, len(shared)+len(fb)+len(bb)+24)
	out = append(out, shared[:len(shared)-1]...)
	out = append(out, `,"friends":`...)
	out = append(out, fb...)
	out = append(out, `,"blocked":`...)
	out = append(out, bb...)
	return append(out, '}')
}

func (h *hub) sendRoomLocked(r *room) {
	for _, m := range r.Members {
		h.sendLocked(m, map[string]any{"type": "room", "room": r})
	}
}

// ---- connections -------------------------------------------------------------------------

func (h *hub) attach(user, game, build string) *client {
	c := &client{user: user, game: game, build: build, ch: make(chan []byte, 64), since: time.Now()}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.clients[lc(user)]; old != nil && !old.gone {
		close(old.ch) // the same account opened a new launcher: the old stream ends
		if old.game != game {
			h.leaveRoomLocked(user)
			h.broadcastLocked(old.game)
		}
	}
	h.clients[lc(user)] = c
	h.sendLocked(user, map[string]any{"type": "hello", "user": user, "game": game})
	if rid, ok := h.inRoom[lc(user)]; ok {
		if r := h.rooms[rid]; r != nil {
			h.sendLocked(user, map[string]any{"type": "room", "room": r})
		}
	}
	if mid, ok := h.playing[lc(user)]; ok { // back during a match: it is still on (the relay never depended on this channel)
		h.sendLocked(user, map[string]any{"type": "playing", "id": mid})
	}
	for _, inv := range h.invites { // invitations that arrived while the connection was down
		if strings.EqualFold(inv.To, user) {
			h.sendLocked(user, map[string]any{"type": "invite", "invite": inv})
		}
	}
	h.broadcastLocked(game)
	log.Printf("online: %s (%s %s)", user, game, build)
	return c
}

func (h *hub) detach(c *client) {
	h.mu.Lock()
	if h.clients[lc(c.user)] != c {
		h.mu.Unlock()
		return // replaced by a newer stream
	}
	c.gone = true
	h.mu.Unlock()
	time.AfterFunc(reconnectGrace, func() { h.expire(c) })
}

// expire: the grace period ran out and the player did not come back.
func (h *hub) expire(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[lc(c.user)] != c {
		return // reconnected in time (a newer stream took the place)
	}
	delete(h.clients, lc(c.user))
	delete(h.waiting, lc(c.user))
	if _, playing := h.playing[lc(c.user)]; !playing {
		h.leaveRoomLocked(c.user)
	}
	for id, inv := range h.invites {
		if strings.EqualFold(inv.From, c.user) || strings.EqualFold(inv.To, c.user) {
			delete(h.invites, id)
		}
	}
	h.broadcastLocked(c.game)
	log.Printf("offline: %s", c.user)
}

func (h *hub) clientOf(user string) *client { return h.clients[lc(user)] }

// whereIs: the game a player is connected to and their status there ("", "" when offline).
func (h *hub) whereIs(user string) (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clientOf(user)
	if c == nil || c.gone {
		return "", ""
	}
	st, _ := h.statusLocked(c.user)
	return c.game, st
}

var (
	errNotOnline  = errors.New("ese jugador no está conectado")
	errBusy       = errors.New("ese jugador está en una partida")
	errYouBusy    = errors.New("ya estás en una partida")
	errOtherBuild = errors.New("ese jugador tiene otra versión del juego: tienen que usar la misma")
	// [block] the same answer whoever blocked whom: the blocked player is not told
	errBlocked = errors.New("no puedes jugar con ese jugador")
)

func (h *hub) setWaiting(user string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clientOf(user)
	if c == nil {
		return
	}
	if on {
		h.waiting[lc(user)] = true
	} else {
		delete(h.waiting, lc(user))
	}
	h.broadcastLocked(c.game)
}

// ---- invitations ------------------------------------------------------------------------

func (h *hub) invite(from, to string, s settings) (*invite, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, b := h.clientOf(from), h.clientOf(to)
	if a == nil {
		return nil, errNotOnline
	}
	if b == nil || b.game != a.game {
		return nil, errNotOnline
	}
	if strings.EqualFold(from, to) {
		return nil, errors.New("no puedes invitarte a ti mismo")
	}
	if _, ok := h.playing[lc(from)]; ok {
		return nil, errYouBusy
	}
	if _, ok := h.playing[lc(to)]; ok {
		return nil, errBusy
	}
	if a.build != b.build {
		return nil, errOtherBuild
	}
	if h.st.blockedEither(a.user, b.user) {
		return nil, errBlocked
	}
	inv := &invite{ID: randomHex(8), From: a.user, To: b.user, Game: a.game, Settings: s, Expires: time.Now().Add(60 * time.Second)}
	h.invites[inv.ID] = inv
	h.sendLocked(b.user, map[string]any{"type": "invite", "invite": inv})
	return inv, nil
}

func (h *hub) respond(user, id string, accept bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	inv := h.invites[id]
	if inv == nil || !strings.EqualFold(inv.To, user) {
		return errors.New("esa invitación ya no existe")
	}
	delete(h.invites, id)
	if !accept {
		h.sendLocked(inv.From, map[string]any{"type": "invite_declined", "id": id, "user": inv.To})
		return nil
	}
	if h.clientOf(inv.From) == nil {
		return errNotOnline
	}
	if _, ok := h.playing[lc(inv.From)]; ok {
		return errBusy
	}
	if _, ok := h.playing[lc(inv.To)]; ok {
		return errYouBusy
	}
	h.startMatchLocked(inv.Game, inv.From, inv.To, "", inv.Settings)
	return nil
}

func (h *hub) expireInvites() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for id, inv := range h.invites {
		if now.After(inv.Expires) {
			delete(h.invites, id)
			h.sendLocked(inv.From, map[string]any{"type": "invite_expired", "id": id, "user": inv.To})
			h.sendLocked(inv.To, map[string]any{"type": "invite_expired", "id": id, "user": inv.From})
		}
	}
}

// ---- matches ----------------------------------------------------------------------------

func (h *hub) startMatchLocked(game, p1, p2, roomID string, s settings) *match {
	m := &match{ID: randomHex(8), Game: game, P1: p1, P2: p2, Room: roomID, Settings: s, Started: time.Now().UTC(),
		token: randomHex(16)}
	h.matches[m.ID] = m
	h.playing[lc(p1)] = m.ID
	h.playing[lc(p2)] = m.ID
	delete(h.waiting, lc(p1))
	delete(h.waiting, lc(p2))
	h.rl.open(m.token, func(token, why string) { h.endMatch(m.ID, why) })
	if h.stats != nil {
		h.stats.matchStarted(m.ID, game, p1, p2)
	}
	for i, u := range []string{p1, p2} {
		other := p2
		if i == 1 {
			other = p1
		}
		h.sendLocked(u, map[string]any{"type": "match", "match": map[string]any{
			"id": m.ID, "token": m.token, "relay": h.relayAddr, "player": i + 1, "opponent": other,
			"room": roomID, "settings": s}})
	}
	if r := h.rooms[roomID]; r != nil {
		r.Match = m.ID
		h.sendRoomLocked(r)
	}
	h.broadcastLocked(game)
	log.Printf("match %s: %s vs %s (%s)", m.ID, p1, p2, game)
	return m
}

// endMatch is called by the relay (idle) or once both players reported the end.
func (h *hub) endMatch(id, why string) {
	h.mu.Lock()
	m := h.matches[id]
	if m == nil {
		h.mu.Unlock()
		return
	}
	delete(h.matches, id)
	for _, u := range []string{m.P1, m.P2} {
		if h.playing[lc(u)] == id {
			delete(h.playing, lc(u))
		}
		h.sendLocked(u, map[string]any{"type": "match_end", "id": id, "why": why})
	}
	if r := h.rooms[m.Room]; r != nil && r.Match == id {
		r.Match = ""
		// [rotation] the two who just played go to the back of the queue; the next two play next
		var rest, played []string
		for _, u := range r.Members {
			if strings.EqualFold(u, m.P1) || strings.EqualFold(u, m.P2) {
				played = append(played, u)
			} else {
				rest = append(rest, u)
			}
		}
		r.Members = append(rest, played...)
		h.sendRoomLocked(r)
	}
	h.broadcastLocked(m.Game)
	h.mu.Unlock()
	h.rl.close(m.token, why)
	if h.stats != nil {
		h.stats.matchEnded(id, m.Game, m.P1, m.P2, m.Started)
	}
	log.Printf("match %s ended (%s)", id, why)
}

// reportEnd: a launcher saw its game close, or its game left the online session. That ends the match for both: it
// cannot go on with one player, and waiting for the other's report kept both shown as playing until the relay's
// idle timeout whenever one launcher never reported (closed with the game, or the game ended offline).
func (h *hub) reportEnd(user, id string) {
	h.mu.Lock()
	m := h.matches[id]
	if m == nil || (!strings.EqualFold(user, m.P1) && !strings.EqualFold(user, m.P2)) {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.endMatch(id, user+" salió de la partida")
}

// ---- rooms ------------------------------------------------------------------------------

func roomPassHash(salt, pass string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + pass))
	return hex.EncodeToString(sum[:])
}

func (h *hub) createRoom(user, name, pass string, max int, s settings) (*room, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 32 {
		return nil, errors.New("el nombre de la sala debe tener de 1 a 32 caracteres")
	}
	if max < 2 || max > 4 {
		max = 2
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clientOf(user)
	if c == nil {
		return nil, errNotOnline
	}
	if _, ok := h.playing[lc(user)]; ok {
		return nil, errYouBusy
	}
	h.leaveRoomLocked(user)
	r := &room{ID: randomHex(6), Game: c.game, Name: name, Owner: c.user, Max: max, Members: []string{c.user}, Settings: s, Build: c.build}
	if pass != "" {
		r.Locked, r.passSalt = true, randomHex(8)
		r.passHash = roomPassHash(r.passSalt, pass)
	}
	h.rooms[r.ID] = r
	h.inRoom[lc(user)] = r.ID
	delete(h.waiting, lc(user))
	h.sendRoomLocked(r)
	h.broadcastLocked(c.game)
	return r, nil
}

func (h *hub) joinRoom(user, id, pass string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clientOf(user)
	r := h.rooms[id]
	if c == nil {
		return errNotOnline
	}
	if r == nil || r.Game != c.game {
		return errors.New("esa sala ya no existe")
	}
	if h.inRoom[lc(user)] == id {
		return nil
	}
	if _, ok := h.playing[lc(user)]; ok {
		return errYouBusy
	}
	if r.Locked && subtle.ConstantTimeCompare([]byte(roomPassHash(r.passSalt, pass)), []byte(r.passHash)) != 1 {
		return errors.New("contraseña de la sala incorrecta")
	}
	if len(r.Members) >= r.Max {
		return errors.New("la sala está llena")
	}
	if r.Build != c.build {
		return errOtherBuild
	}
	for _, m := range r.Members {
		if h.st.blockedEither(c.user, m) {
			return errors.New("no puedes entrar en esa sala")
		}
	}
	h.leaveRoomLocked(user)
	r.Members = append(r.Members, c.user)
	h.inRoom[lc(user)] = id
	delete(h.waiting, lc(user))
	h.sendRoomLocked(r)
	h.broadcastLocked(c.game)
	return nil
}

func (h *hub) leaveRoomLocked(user string) {
	id, ok := h.inRoom[lc(user)]
	if !ok {
		return
	}
	delete(h.inRoom, lc(user))
	r := h.rooms[id]
	if r == nil {
		return
	}
	out := r.Members[:0]
	for _, m := range r.Members {
		if !strings.EqualFold(m, user) {
			out = append(out, m)
		}
	}
	r.Members = out
	h.sendLocked(user, map[string]any{"type": "room_left", "id": id})
	if len(r.Members) == 0 {
		delete(h.rooms, id)
		return
	}
	if strings.EqualFold(r.Owner, user) {
		r.Owner = r.Members[0]
	}
	h.sendRoomLocked(r)
}

func (h *hub) leaveRoom(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clientOf(user)
	h.leaveRoomLocked(user)
	if c != nil {
		h.broadcastLocked(c.game)
	}
}

func (h *hub) kick(owner, user string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[h.inRoom[lc(owner)]]
	if r == nil || !strings.EqualFold(r.Owner, owner) {
		return errors.New("solo el creador de la sala puede sacar a alguien")
	}
	if h.inRoom[lc(user)] != r.ID || strings.EqualFold(user, owner) {
		return errors.New("ese jugador no está en tu sala")
	}
	h.leaveRoomLocked(user)
	h.sendLocked(user, map[string]any{"type": "error", "message": "el creador de la sala te sacó"})
	h.broadcastLocked(r.Game)
	return nil
}

// startRoom: the owner starts the next match, the first two of the queue.
func (h *hub) startRoom(user string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[h.inRoom[lc(user)]]
	if r == nil {
		return errors.New("no estás en una sala")
	}
	if !strings.EqualFold(r.Owner, user) {
		return errors.New("solo el creador de la sala puede empezar")
	}
	if r.Match != "" {
		return errors.New("ya hay una pelea en curso en la sala")
	}
	if len(r.Members) < 2 {
		return errors.New("hacen falta al menos 2 jugadores")
	}
	h.startMatchLocked(r.Game, r.Members[0], r.Members[1], r.ID, r.Settings)
	return nil
}

func (h *hub) chat(user, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len([]rune(text)) > 300 {
		text = string([]rune(text)[:300])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[h.inRoom[lc(user)]]
	if r == nil {
		return errors.New("el chat es dentro de una sala")
	}
	for _, m := range r.Members {
		if h.st.isBlocked(m, user) { // [block] it does not reach who blocked the sender
			continue
		}
		h.sendLocked(m, map[string]any{"type": "chat", "from": user, "text": text})
	}
	return nil
}

func (h *hub) friendsChanged(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.clientOf(user); c != nil {
		sendTo(c, h.lobbyMsg(h.lobbyJSON(c.game), c.user))
	}
}

func (h *hub) counts() (int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients), len(h.rooms), len(h.matches)
}

package main

// UDP relay for the matches. The games never talk to each other directly (no open ports at
// home): each one sends a registration datagram
//
//	"RLY1" + token (32 hex chars) + player ('1' or '2')
//
// and the relay answers "RLYA" + player + ('1' when the other player is registered too, else '0').
// From then on every datagram from a registered address is forwarded, untouched, to the other
// player of the same match. The game's own packets (inputs, a few hundred bytes per second) are
// opaque to the relay. Unknown senders are dropped. The voice chat goes through the same port (voice.go).

import (
	"log"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	relayMagic     = "RLY1"
	relayAck       = "RLYA"
	relayIdle      = 90 * time.Second // a match with no traffic for this long is over
	relayMaxPacket = 1500
	relayRebind    = 1500 * time.Millisecond // a registered slot only moves to a new address after this much silence
	relayMaxRate   = 400                     // packets per second per player (the game sends 30-60)
)

type relayMatch struct {
	token    string
	addr     [3]*net.UDPAddr // [1], [2]
	lastFrom [3]time.Time    // last packet from each player
	rateSec  [3]int64        // [rate] the second being counted, per player
	rateN    [3]int
	lastSeen time.Time
	created  time.Time
	onEnd    func(token, why string)
}

type relay struct {
	mu      sync.Mutex
	conn    *net.UDPConn
	matches map[string]*relayMatch // by token
	byAddr  map[string]*relayMatch // by "ip:port" of a registered player
	slot    map[string]int         // "ip:port" -> player
	packets uint64
	voice   *voiceRelay // [voice]
}

func newRelay(listen string) (*relay, error) {
	ua, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	r := &relay{conn: c, matches: map[string]*relayMatch{}, byAddr: map[string]*relayMatch{}, slot: map[string]int{},
		voice: newVoiceRelay()}
	go r.loop()
	go r.reaper()
	return r, nil
}

// open makes a token usable; onEnd is called once when the match goes idle or is closed.
func (r *relay) open(token string, onEnd func(token, why string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.matches[token] = &relayMatch{token: token, lastSeen: now, created: now, onEnd: onEnd}
}

func (r *relay) close(token, why string) {
	r.mu.Lock()
	m := r.matches[token]
	if m == nil {
		r.mu.Unlock()
		return
	}
	r.removeLocked(m)
	r.mu.Unlock()
	if m.onEnd != nil {
		m.onEnd(token, why)
	}
}

func (r *relay) removeLocked(m *relayMatch) {
	delete(r.matches, m.token)
	for p := 1; p <= 2; p++ {
		if a := m.addr[p]; a != nil {
			delete(r.byAddr, a.String())
			delete(r.slot, a.String())
		}
	}
}

func (r *relay) loop() {
	buf := make([]byte, relayMaxPacket+1)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("relay: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if n > relayMaxPacket {
			continue
		}
		pkt := buf[:n]
		if n == 37 && string(pkt[:4]) == relayMagic {
			r.register(string(pkt[4:36]), pkt[36], from)
			continue
		}
		if n == 36 && string(pkt[:4]) == voiceMagic {
			r.voice.register(r.conn, string(pkt[4:36]), voiceAddr(from))
			continue
		}
		key := from.String()
		r.mu.Lock()
		m := r.byAddr[key]
		var to *net.UDPAddr
		if m != nil {
			now := time.Now()
			p := r.slot[key]
			if sec := now.Unix(); m.rateSec[p] != sec {
				m.rateSec[p], m.rateN[p] = sec, 0
			}
			m.rateN[p]++
			m.lastSeen, m.lastFrom[p] = now, now
			if m.rateN[p] <= relayMaxRate { // [rate] beyond that the packets are dropped: no flooding through the relay
				to = m.addr[3-p]
				r.packets++
			}
		}
		r.mu.Unlock()
		if to != nil {
			_, _ = r.conn.WriteToUDP(pkt, to)
		} else if m == nil {
			r.voice.handle(r.conn, pkt, voiceAddr(from)) // the match packets go first; then the voice
		}
	}
}

// voiceAddr: the sender as a map key, IPv4 unmapped (the port listens on both IPv4 and IPv6).
func voiceAddr(a *net.UDPAddr) netip.AddrPort {
	ap := a.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func (r *relay) register(token string, playerByte byte, from *net.UDPAddr) {
	p := int(playerByte - '0')
	if p != 1 && p != 2 {
		return
	}
	r.mu.Lock()
	m := r.matches[token]
	if m == nil {
		r.mu.Unlock()
		return
	}
	if old := m.addr[p]; old != nil && old.String() != from.String() {
		// [rebind] The slot moves to the new address only once the old one has gone quiet (the player's router
		// changed the port, or the game restarted). While the old one is live, a second registration with the same
		// token is somebody else: refused, so a match cannot be taken over mid-fight.
		if time.Since(m.lastFrom[p]) < relayRebind {
			r.mu.Unlock()
			return
		}
		delete(r.byAddr, old.String())
		delete(r.slot, old.String())
	}
	if other := r.byAddr[from.String()]; other != nil && other != m {
		r.mu.Unlock() // this address already plays another match
		return
	}
	m.addr[p] = from
	r.byAddr[from.String()] = m
	r.slot[from.String()] = p
	m.lastSeen, m.lastFrom[p] = time.Now(), time.Now()
	both := m.addr[1] != nil && m.addr[2] != nil
	r.mu.Unlock()
	ack := []byte(relayAck + string(rune('0'+p)) + "0")
	if both {
		ack[5] = '1'
	}
	_, _ = r.conn.WriteToUDP(ack, from)
}

func (r *relay) reaper() {
	for range time.Tick(5 * time.Second) {
		now := time.Now()
		var ended []*relayMatch
		r.mu.Lock()
		for _, m := range r.matches {
			if now.Sub(m.lastSeen) > relayIdle {
				r.removeLocked(m)
				ended = append(ended, m)
			}
		}
		r.mu.Unlock()
		for _, m := range ended {
			if m.onEnd != nil {
				m.onEnd(m.token, "sin tráfico")
			}
		}
	}
}

func (r *relay) stats() (int, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.matches), r.packets
}

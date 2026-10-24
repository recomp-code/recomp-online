package main

// [voice] Voice chat through the relay. A voice channel is a room (up to 4: the two fighting and the ones waiting
// their turn keep talking across the matches and the rotation) or the 1-vs-1 match of an invitation. The launcher
// talks, not the game: it stays alive during the match and plays the voices.
//
// On the relay's UDP port, from a socket of its own (never the game's):
//
//	"RLV1" + token (32 hex chars)  register; answered "RLVA" + slot ('1'..'4'). The launcher sends it again every few
//	                               seconds: it keeps the NAT mapping open while nobody talks (silence sends nothing).
//	'v' + slot + payload           voice: the relay writes the sender's slot over the second byte (whatever the
//	                               launcher put there) and copies the datagram to the others of the channel.
//	'p' + payload                  ping: echoed unchanged to the sender (the launcher's latency meter).
//
// The payload (sequence, timestamp, Opus frame) is the launchers' business. Nothing is decoded or mixed here: one
// datagram in, up to three out, so the relay adds no delay. Who hears whom (blocks, the room owner's mute) is
// decided by the hub when it changes and kept as one bit mask per sender, so a datagram costs a map lookup and a
// mask test.

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	voiceMagic   = "RLV1"
	voiceAck     = "RLVA"
	voiceSlots   = 4
	voiceMaxRate = 250 // datagrams per second per slot (10 ms frames are 100, plus the pings)
)

type voiceSlot struct {
	user    string
	token   string
	addr    netip.AddrPort // zero until the launcher registers
	to      uint8          // [route] bit r-1: this slot's voice goes to slot r
	rateSec int64
	rateN   int
}

type voiceChan struct {
	id    string
	slots [voiceSlots + 1]*voiceSlot // [1..4]
}

type voiceRef struct {
	ch   *voiceChan
	slot int
}

type voiceRelay struct {
	mu      sync.Mutex
	chans   map[string]*voiceChan
	byToken map[string]voiceRef
	byAddr  map[netip.AddrPort]voiceRef
	packets uint64
}

func newVoiceRelay() *voiceRelay {
	return &voiceRelay{chans: map[string]*voiceChan{}, byToken: map[string]voiceRef{}, byAddr: map[netip.AddrPort]voiceRef{}}
}

// join gives the user a place in the channel (created on the first join) and their own token. Joining again returns
// the same place and token (the launcher reconnected). 0 when the channel is full.
func (v *voiceRelay) join(id, user string) (int, string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := v.chans[id]
	if ch == nil {
		ch = &voiceChan{id: id}
		v.chans[id] = ch
	}
	free := 0
	for i := voiceSlots; i >= 1; i-- {
		if s := ch.slots[i]; s == nil {
			free = i
		} else if lc(s.user) == lc(user) {
			return i, s.token
		}
	}
	if free == 0 {
		return 0, ""
	}
	s := &voiceSlot{user: user, token: randomHex(16)}
	ch.slots[free] = s
	v.byToken[s.token] = voiceRef{ch, free}
	return free, s.token
}

// leave frees the user's place: from now on their datagrams are dropped and nothing is sent to them.
func (v *voiceRelay) leave(id, user string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := v.chans[id]
	if ch == nil {
		return
	}
	for i := 1; i <= voiceSlots; i++ {
		if s := ch.slots[i]; s != nil && lc(s.user) == lc(user) {
			v.dropLocked(ch, i)
		}
	}
}

func (v *voiceRelay) close(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := v.chans[id]
	if ch == nil {
		return
	}
	for i := 1; i <= voiceSlots; i++ {
		if ch.slots[i] != nil {
			v.dropLocked(ch, i)
		}
	}
	delete(v.chans, id)
}

func (v *voiceRelay) dropLocked(ch *voiceChan, i int) {
	s := ch.slots[i]
	delete(v.byToken, s.token)
	if s.addr.IsValid() {
		delete(v.byAddr, s.addr)
	}
	ch.slots[i] = nil
}

// setRoutes: to[s] is the mask of the slots that hear slot s (see hub.voiceRoutesLocked).
func (v *voiceRelay) setRoutes(id string, to [voiceSlots + 1]uint8) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := v.chans[id]
	if ch == nil {
		return
	}
	for i := 1; i <= voiceSlots; i++ {
		if s := ch.slots[i]; s != nil {
			s.to = to[i]
		}
	}
}

// register binds a token to the address it comes from. Unlike a match token (shared by both players, so a second
// address is somebody else), a voice token is the player's own: a new address is their launcher after the router
// changed the port or a restart, and the place moves at once.
func (v *voiceRelay) register(conn *net.UDPConn, token string, from netip.AddrPort) {
	v.mu.Lock()
	ref, ok := v.byToken[token]
	if !ok {
		v.mu.Unlock()
		return
	}
	s := ref.ch.slots[ref.slot]
	if s.addr != from {
		if other, taken := v.byAddr[from]; taken && other != ref {
			v.mu.Unlock() // this address already talks in another place
			return
		}
		if s.addr.IsValid() {
			delete(v.byAddr, s.addr)
		}
		s.addr = from
		v.byAddr[from] = ref
	}
	v.mu.Unlock()
	_, _ = conn.WriteToUDPAddrPort([]byte{voiceAck[0], voiceAck[1], voiceAck[2], voiceAck[3], byte('0' + ref.slot)}, from)
}

// handle forwards a datagram from a registered voice address; false when the sender is not one. pkt is the read
// buffer: the slot is stamped in place before it is sent on.
func (v *voiceRelay) handle(conn *net.UDPConn, pkt []byte, from netip.AddrPort) bool {
	v.mu.Lock()
	ref, ok := v.byAddr[from]
	if !ok {
		v.mu.Unlock()
		return false
	}
	s := ref.ch.slots[ref.slot]
	if sec := time.Now().Unix(); s.rateSec != sec {
		s.rateSec, s.rateN = sec, 0
	}
	s.rateN++
	if s.rateN > voiceMaxRate || len(pkt) < 2 { // [rate] no flooding through the relay
		v.mu.Unlock()
		return true
	}
	var to [voiceSlots]netip.AddrPort
	n := 0
	if pkt[0] == 'v' {
		for r := 1; r <= voiceSlots; r++ {
			if d := ref.ch.slots[r]; r != ref.slot && d != nil && d.addr.IsValid() && s.to&(1<<(r-1)) != 0 {
				to[n] = d.addr
				n++
			}
		}
		v.packets += uint64(n)
	}
	v.mu.Unlock()
	switch pkt[0] {
	case 'p':
		_, _ = conn.WriteToUDPAddrPort(pkt, from)
	case 'v':
		pkt[1] = byte('0' + ref.slot)
		for i := 0; i < n; i++ {
			_, _ = conn.WriteToUDPAddrPort(pkt, to[i])
		}
	}
	return true
}

func (v *voiceRelay) stats() (int, uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.chans), v.packets
}

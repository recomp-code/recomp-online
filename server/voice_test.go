package main

import (
	"encoding/binary"
	"net"
	"sort"
	"sync"
	"testing"
	"time"
)

type vconn struct {
	t    *testing.T
	user string
	c    *net.UDPConn
	slot byte
}

// voiceJoin: the player's "voice" event, and their launcher registering on the relay with its token.
func voiceJoin(t *testing.T, c *tclient, relay string) *vconn {
	t.Helper()
	ev := c.wait("voice")
	tok, slot := ev["token"].(string), byte('0'+int(ev["slot"].(float64)))
	if ev["relay"] != relay || len(tok) != 32 {
		t.Fatalf("%s: voice event %v", c.user, ev)
	}
	v := &vconn{t: t, user: c.user, slot: slot}
	v.c = voiceRegister(t, relay, tok, slot)
	return v
}

func voiceRegister(t *testing.T, relay, tok string, slot byte) *net.UDPConn {
	t.Helper()
	ra, _ := net.ResolveUDPAddr("udp", relay)
	c, err := net.DialUDP("udp", nil, ra)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_, _ = c.Write([]byte(voiceMagic + tok))
	buf := make([]byte, 16)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != voiceAck+string(slot) {
		t.Fatalf("voice ack: %v %q, want slot %c", err, buf[:n], slot)
	}
	return c
}

func (v *vconn) say(text string) { _, _ = v.c.Write([]byte("v9" + text)) } // '9': the relay must overwrite it

func (v *vconn) hears(from *vconn, text string) {
	v.t.Helper()
	buf := make([]byte, 1600)
	_ = v.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := v.c.Read(buf)
	if want := "v" + string(from.slot) + text; err != nil || string(buf[:n]) != want {
		v.t.Fatalf("%s: got %q (%v), want %q from %s", v.user, buf[:n], err, want, from.user)
	}
}

func (v *vconn) hearsNothing() {
	v.t.Helper()
	buf := make([]byte, 1600)
	_ = v.c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if n, err := v.c.Read(buf); err == nil {
		v.t.Fatalf("%s heard %q", v.user, buf[:n])
	}
}

// room4: a room of 4 with everybody's launcher registered for voice.
func room4(t *testing.T) (string, []*tclient, []*vconn) {
	base, relay := testServer(t)
	names := [][2]string{{"Goku", "kamehameha"}, {"Vegeta", "finalflash"}, {"Piccolo", "makanko12"}, {"Gohan", "masenko12"}}
	var cs []*tclient
	for _, n := range names {
		c := newTClient(t, base, n[0], n[1], "dbzbt4", "v1")
		c.wait("hello")
		cs = append(cs, c)
	}
	_, rm := post(t, base, "/api/rooms", cs[0].token, map[string]any{"name": "Torneo", "max": 4})
	for _, c := range cs[1:] {
		if code, out := post(t, base, "/api/rooms/join", c.token, map[string]any{"id": rm["id"]}); code != 200 {
			t.Fatalf("join %s: %d %v", c.user, code, out)
		}
	}
	var vs []*vconn
	seen := map[byte]bool{}
	for _, c := range cs {
		v := voiceJoin(t, c, relay)
		if seen[v.slot] {
			t.Fatalf("slot %c given twice", v.slot)
		}
		seen[v.slot] = true
		vs = append(vs, v)
	}
	return base, cs, vs
}

// [voice] a room of 4: everybody hears everybody else, the two fighting and the two waiting, across the matches.
func TestVoiceRoom(t *testing.T) {
	base, cs, vs := room4(t)
	for {
		r := cs[0].wait("room")["room"].(map[string]any)
		if vm, _ := r["voice"].(map[string]any); len(vm) == 4 {
			break
		}
	}
	all := func(from int, text string) {
		vs[from].say(text)
		for i, v := range vs {
			if i != from {
				v.hears(vs[from], text)
			}
		}
		vs[from].hearsNothing() // nobody gets their own voice back
	}
	all(0, "hola")
	all(3, "que tal")

	// ping: back to the sender only
	_, _ = vs[1].c.Write([]byte("p12345678"))
	buf := make([]byte, 64)
	_ = vs[1].c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := vs[1].c.Read(buf); err != nil || string(buf[:n]) != "p12345678" {
		t.Fatalf("ping: %q %v", buf[:n], err)
	}
	vs[0].hearsNothing()

	// a match starts in the room (Goku vs Vegeta): the voice does not change
	if code, out := post(t, base, "/api/rooms/start", cs[0].token, nil); code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	m := cs[0].wait("match")["match"].(map[string]any)
	if m["voice"] != nil {
		t.Fatalf("a room's match got a voice channel of its own: %v", m["voice"])
	}
	all(2, "dale Goku")
	all(1, "final flash")
	post(t, base, "/api/match/end", cs[0].token, map[string]string{"id": m["id"].(string)})
	cs[0].wait("match_end")
	all(0, "otra")

	// the owner mutes Piccolo for everybody; a non-owner cannot
	if code, _ := post(t, base, "/api/rooms/mute", cs[1].token, map[string]any{"user": "piccolo", "mute": true}); code != 409 {
		t.Fatal("a non-owner muted somebody")
	}
	if code, out := post(t, base, "/api/rooms/mute", cs[0].token, map[string]any{"user": "piccolo", "mute": true}); code != 200 {
		t.Fatalf("mute: %d %v", code, out)
	}
	vs[2].say("me oyen?")
	for _, v := range vs {
		v.hearsNothing()
	}
	all(1, "Piccolo sigue oyendo") // muted is not deaf
	post(t, base, "/api/rooms/mute", cs[0].token, map[string]any{"user": "piccolo", "mute": false})
	all(2, "ya")

	// Gohan leaves: out of the channel at once, both ways
	post(t, base, "/api/rooms/leave", cs[3].token, nil)
	cs[3].wait("voice_end")
	vs[3].say("sigo aqui")
	for _, v := range vs[:3] {
		v.hearsNothing()
	}
	vs[0].say("adios Gohan")
	vs[1].hears(vs[0], "adios Gohan")
	vs[2].hears(vs[0], "adios Gohan")
	vs[3].hearsNothing()
}

// [voice] [block] a block in the middle of a room cuts the voice between those two, both ways, and only them.
func TestVoiceBlock(t *testing.T) {
	base, cs, vs := room4(t)
	post(t, base, "/api/block", cs[0].token, map[string]any{"user": "vegeta", "block": true})
	vs[1].say("kakarotto")
	vs[2].hears(vs[1], "kakarotto")
	vs[3].hears(vs[1], "kakarotto")
	vs[0].hearsNothing()
	vs[0].say("no te oigo")
	vs[2].hears(vs[0], "no te oigo")
	vs[3].hears(vs[0], "no te oigo")
	vs[1].hearsNothing()
	post(t, base, "/api/block", cs[0].token, map[string]any{"user": "vegeta", "block": false})
	vs[1].say("otra vez")
	vs[0].hears(vs[1], "otra vez")
}

// [voice] an invitation's match has a channel of its own that closes with the match; a launcher that comes back
// (new port, restart) gets the same place and its address moves.
func TestVoiceInviteMatch(t *testing.T) {
	base, relay := testServer(t)
	a := newTClient(t, base, "Goku", "kamehameha", "dbzbt4", "v1")
	b := newTClient(t, base, "Vegeta", "finalflash", "dbzbt4", "v1")
	a.wait("hello")
	b.wait("hello")
	_, inv := post(t, base, "/api/invite", a.token, map[string]any{"to": "vegeta"})
	b.wait("invite")
	post(t, base, "/api/invite/respond", b.token, map[string]any{"id": inv["id"], "accept": true})
	va, vb := voiceJoin(t, a, relay), voiceJoin(t, b, relay)
	m := a.wait("match")["match"].(map[string]any)
	if vm := m["voice"].(map[string]any); vm["Goku"] != float64(va.slot-'0') || vm["Vegeta"] != float64(vb.slot-'0') {
		t.Fatalf("match voice map %v", vm)
	}
	va.say("listo?")
	vb.hears(va, "listo?")

	// Vegeta's launcher on a new port: same token, the place moves; the old socket hears nothing more
	old := vb.c
	vb.c = reconnectVoice(t, base, b, relay)
	va.say("sigues?")
	vb.hears(va, "sigues?")
	_ = old.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if n, err := old.Read(make([]byte, 64)); err == nil {
		t.Fatalf("the old address still hears (%d bytes)", n)
	}

	post(t, base, "/api/match/end", a.token, map[string]string{"id": m["id"].(string)})
	a.wait("voice_end")
	va.say("despues")
	vb.hearsNothing()
}

// reconnectVoice: the player's launcher opens a new event stream (it gets the same voice token again) and registers
// from a new UDP socket.
func reconnectVoice(t *testing.T, base string, c *tclient, relay string) *net.UDPConn {
	t.Helper()
	_, events := dialWS(t, base, c.token, "dbzbt4", "v1")
	ev := (&tclient{t: t, user: c.user, events: events}).wait("voice")
	return voiceRegister(t, relay, ev["token"].(string), byte('0'+int(ev["slot"].(float64))))
}

// [voice] what the relay adds: 4 launchers in a room, all talking at once at 100 datagrams per second (10 ms Opus
// frames), each datagram stamped with its send time and timed at the other three. Over loopback this is the relay's
// own cost; on the internet add the network.
func TestVoiceRelayLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	_, _, vs := room4(t)
	const frames = 300 // 3 s at 10 ms
	start := time.Now()
	var mu sync.Mutex
	var lat []time.Duration
	var got int
	var wg sync.WaitGroup
	for _, v := range vs {
		wg.Add(1)
		go func(v *vconn) { // receiver
			defer wg.Done()
			buf := make([]byte, 1600)
			for {
				_ = v.c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, err := v.c.Read(buf)
				if err != nil {
					return
				}
				if n >= 10 && buf[0] == 'v' {
					d := time.Since(start) - time.Duration(binary.LittleEndian.Uint64(buf[2:10]))
					mu.Lock()
					lat, got = append(lat, d), got+1
					mu.Unlock()
				}
			}
		}(v)
	}
	pkt := make([]byte, 2+8+60) // 60 bytes: a 10 ms Opus frame at ~48 kbit/s
	for i := 0; i < frames; i++ {
		for _, v := range vs {
			p := append([]byte(nil), pkt...)
			p[0], p[1] = 'v', '9'
			binary.LittleEndian.PutUint64(p[2:10], uint64(time.Since(start)))
			_, _ = v.c.Write(p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	want := frames * 4 * 3
	if got < want*99/100 {
		t.Fatalf("delivered %d of %d datagrams", got, want)
	}
	pct := func(p int) time.Duration { return lat[len(lat)*p/100] }
	t.Logf("relay, 4 talking at once: %d/%d datagrams, p50 %v, p99 %v, max %v", got, want, pct(50), pct(99), lat[len(lat)-1])
	if pct(99) > 5*time.Millisecond {
		t.Errorf("the relay adds %v at p99", pct(99))
	}
}

// BenchmarkVoiceForward: the relay's cost per voice datagram, from the read to the three writes.
func BenchmarkVoiceForward(b *testing.B) {
	rl, err := newRelay("127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var addrs []*net.UDPAddr
	for i := 0; i < 4; i++ {
		c, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		defer c.Close()
		_, tok := rl.voice.join("r:bench", string(rune('A'+i)))
		a := c.LocalAddr().(*net.UDPAddr)
		rl.voice.register(rl.conn, tok, voiceAddr(a))
		addrs = append(addrs, a)
	}
	rl.voice.setRoutes("r:bench", [voiceSlots + 1]uint8{0, 0b1110, 0b1101, 0b1011, 0b0111})
	pkt := make([]byte, 70)
	pkt[0] = 'v'
	from := voiceAddr(addrs[0])
	sender := rl.voice.byAddr[from]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sender.ch.slots[sender.slot].rateN = 0 // the rate limit would drop all but 250 a second
		rl.voice.handle(rl.conn, pkt, from)
	}
}

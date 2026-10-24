//go:build darwin && arm64

package main

// [voice] The launcher's voice (voice.go and the engine) against the real server (../server, built and started
// here): a room of two, the launcher's engine registers with the relay and measures its ping, the other player
// (a bare UDP client) talks, and leaving the room ends the voice. It opens this machine's speakers (the microphone
// stays muted, so it is never opened): run it on purpose, RECOMP_VOICE_E2E=1 go test -run VoiceServer .

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func freePort(t *testing.T, network string) int {
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func e2ePost(t *testing.T, base, path, tok string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d %v", path, resp.StatusCode, out)
	}
	return out
}

// e2eStream: the lobby's WebSocket; every event goes to fn (on its own goroutine).
func e2eStream(t *testing.T, base, tok string, fn func(map[string]any)) {
	ws := "ws" + base[len("http"):] + "/api/ws?game=dbzbt4&build=v1"
	c, _, err := websocket.Dial(context.Background(), ws, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var ev map[string]any
			if json.Unmarshal(b, &ev) == nil {
				fn(ev)
			}
		}
	}()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestVoiceServerE2E(t *testing.T) {
	if os.Getenv("RECOMP_VOICE_E2E") != "1" {
		t.Skip("set RECOMP_VOICE_E2E=1 (builds the server, opens the speakers)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "recomp-online")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "../server"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("server build: %v\n%s", err, out)
	}
	hp, up := freePort(t, "tcp"), freePort(t, "udp")
	relay := fmt.Sprintf("127.0.0.1:%d", up)
	srv := exec.Command(bin, "--http", fmt.Sprintf("127.0.0.1:%d", hp), "--relay", relay, "--relay-public", relay, "--data", filepath.Join(dir, "data"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", hp)
	waitFor(t, "the server", func() bool { r, err := http.Get(base + "/api/health"); return err == nil && r.StatusCode == 200 })

	// Goku: this launcher's voice. Its microphone is muted (never opened); the speakers play at 1%.
	home := t.TempDir()
	emitted := make(chan VoiceLive, 256)
	v := newVoice(home, func(name string, data any) {
		if l, ok := data.(VoiceLive); ok && name == "voice" {
			select {
			case emitted <- l:
			default:
			}
		}
	})
	v.cfg.Muted, v.cfg.Volume = true, 1
	t.Cleanup(v.close)
	goku := e2ePost(t, base, "/api/register", "", map[string]string{"user": "Goku", "password": "kamehameha"})["token"].(string)
	vegeta := e2ePost(t, base, "/api/register", "", map[string]string{"user": "Vegeta", "password": "finalflash"})["token"].(string)
	e2eStream(t, base, goku, v.onEvent)
	vegVoice := make(chan map[string]any, 4)
	e2eStream(t, base, vegeta, func(ev map[string]any) {
		if ev["type"] == "voice" {
			vegVoice <- ev
		}
	})
	time.Sleep(200 * time.Millisecond)
	room := e2ePost(t, base, "/api/rooms", goku, map[string]any{"name": "Torneo", "max": 4})
	e2ePost(t, base, "/api/rooms/join", vegeta, map[string]any{"id": room["id"]})

	// the engine registers with the real relay and measures its ping
	waitFor(t, "the engine live with a ping", func() bool { l := v.live(); return l.On && l.State == 2 && l.Ping >= 0 })
	waitFor(t, "Vegeta in the voice", func() bool { l := v.live(); return len(l.Players) == 1 && l.Players[0].User == "Vegeta" })
	l := v.live()
	t.Logf("live: slot %d, ping %d ms, players %+v", l.Me, l.Ping, l.Players)

	// Vegeta talks from a bare UDP socket: registered with its own token, its frames (empty Opus frames: the decoder
	// conceals them) reach the engine through the relay with Vegeta's slot stamped on them
	ev := <-vegVoice
	tok, slot := ev["token"].(string), int(ev["slot"].(float64))
	ra, _ := net.ResolveUDPAddr("udp", relay)
	u, err := net.DialUDP("udp", nil, ra)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	_, _ = u.Write([]byte("RLV1" + tok))
	ack := make([]byte, 16)
	_ = u.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := u.Read(ack); err != nil || string(ack[:n]) != fmt.Sprintf("RLVA%d", slot) {
		t.Fatalf("Vegeta's ack: %q %v", ack[:n], err)
	}
	for i := 0; i < 100; i++ {
		flags := byte(0)
		if i == 0 {
			flags = 1 // the start of a talk spurt
		}
		_, _ = u.Write([]byte{'v', '9', byte(i), 0, flags, 20, 0xF0}) // TOC only: CELT, 10 ms, no data
		time.Sleep(10 * time.Millisecond)
		if i == 60 {
			l = v.live()
		}
	}
	if len(l.Players) != 1 || l.Players[0].Ms < 0 {
		t.Fatalf("Vegeta's stream did not reach the engine: %+v", l.Players)
	}
	t.Logf("Vegeta heard: estimate %d ms mouth to ear (his relay leg 20 ms), loss %d‰", l.Players[0].Ms, l.Players[0].Loss)

	// the room's owner mutes Vegeta for everybody: the launcher shows it
	e2ePost(t, base, "/api/rooms/mute", goku, map[string]any{"user": "vegeta", "mute": true})
	waitFor(t, "Vegeta shown silenced", func() bool { l := v.live(); return len(l.Players) == 1 && l.Players[0].Silent })

	// leaving the room ends the voice
	e2ePost(t, base, "/api/rooms/leave", goku, nil)
	waitFor(t, "the voice to stop", func() bool { l := v.live(); return !l.On && l.Channel == "" })
}

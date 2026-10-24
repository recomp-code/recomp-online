package main

// [voice] The voice chat. The server gives the launcher a place in the voice channel of its room (or of an
// invitation's match): a "voice" event with the relay, the slot and a token of its own. The engine (voice/engine.c)
// talks to the relay from the launcher, which stays alive during the match, so the game is not involved. The room
// says who is in which slot ("room".voice); the player's choices, and the volume and mute of each other player, are
// kept in voice.json.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// the engine's keys and stats (voice/engine.h)
const (
	rvMuted   = 1
	rvPTT     = 2
	rvPTTDown = 3
	rvSense   = 4
	rvMicGain = 5
	rvVolume  = 6
	rvEcho    = 7
	rvBeep    = 9

	rvStState   = 0
	rvStPing    = 1
	rvStLevel   = 2
	rvStSending = 3
	rvStPeers   = 5
	rvPeerN     = 5
	rvSlots     = 4
	rvStN       = rvStPeers + rvSlots*rvPeerN
)

// hotkey ids (hotkey.start)
const (
	hkOverlay = 1
	hkMute    = 2
)

type VoicePeer struct {
	Volume int  `json:"volume"` // percent, 0-200
	Muted  bool `json:"muted"`
}

type VoiceSettings struct {
	Enabled bool                 `json:"enabled"` // talk in rooms and matches
	Muted   bool                 `json:"muted"`   // the microphone closed
	PTT     bool                 `json:"ptt"`     // push to talk instead of voice activity
	Sense   int                  `json:"sense"`   // 0-100, how easily the voice opens the microphone
	MicGain int                  `json:"micGain"` // percent
	Volume  int                  `json:"volume"`  // percent, everybody
	Echo    bool                 `json:"echo"`    // echo cancellation (speakers instead of headphones); macOS
	Mic     string               `json:"mic"`     // device names, "" = the system's
	Speaker string               `json:"speaker"` //
	Peers   map[string]VoicePeer `json:"peers"`   // by lower-case player
}

type voiceChat struct {
	mu      sync.Mutex
	path    string
	cfg     VoiceSettings
	emit    func(string, any)
	channel string // the channel the server gave us ("" none)
	relay   string
	token   string
	slot    int
	members map[string]int // player -> slot (ours included)
	muted   []string       // muted by the room's owner, for everybody
	on      bool           // the engine is running
	err     string
	stop    chan struct{}
	stats   [rvStN]int32
}

func newVoice(home string, emit func(string, any)) *voiceChat {
	v := &voiceChat{path: filepath.Join(home, "voice.json"), emit: emit, members: map[string]int{},
		cfg: VoiceSettings{Enabled: true, Sense: 50, MicGain: 100, Volume: 100}}
	_ = readJSONFile(v.path, &v.cfg)
	if v.cfg.Peers == nil {
		v.cfg.Peers = map[string]VoicePeer{}
	}
	return v
}

// onEvent: the server's events that concern the voice (App.onNetEvent).
func (v *voiceChat) onEvent(ev map[string]any) {
	switch ev["type"] {
	case "voice":
		ch, _ := ev["channel"].(string)
		relay, _ := ev["relay"].(string)
		tok, _ := ev["token"].(string)
		slot, _ := ev["slot"].(float64)
		v.join(ch, relay, tok, int(slot))
	case "voice_end":
		ch, _ := ev["channel"].(string)
		v.end(ch)
	case "room":
		if r, ok := ev["room"].(map[string]any); ok {
			v.setMembers(r["voice"], r["muted"])
		}
	case "match":
		if m, ok := ev["match"].(map[string]any); ok && m["voice"] != nil {
			v.setMembers(m["voice"], nil)
		}
	}
}

func (v *voiceChat) join(ch, relay, tok string, slot int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if ch == v.channel && tok == v.token && slot == v.slot && v.on {
		return // the same place after a reconnection: the engine never stopped
	}
	v.channel, v.relay, v.token, v.slot = ch, relay, tok, slot
	if v.cfg.Enabled {
		v.startLocked()
	}
}

func (v *voiceChat) end(ch string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if ch != "" && ch != v.channel {
		return
	}
	v.stopLocked()
	v.channel, v.token, v.slot = "", "", 0
	v.members, v.muted = map[string]int{}, nil
	v.publishLocked()
}

// leave: logged out, or this launcher was replaced by another.
func (v *voiceChat) leave() { v.end("") }

func (v *voiceChat) setMembers(voice, muted any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	m := map[string]int{}
	if vm, ok := voice.(map[string]any); ok {
		for u, s := range vm {
			if f, ok := s.(float64); ok {
				m[u] = int(f)
			}
		}
	}
	v.members = m
	v.muted = nil
	if ml, ok := muted.([]any); ok {
		for _, x := range ml {
			if s, ok := x.(string); ok {
				v.muted = append(v.muted, s)
			}
		}
	}
	v.applyPeersLocked()
	v.publishLocked()
}

func (v *voiceChat) startLocked() {
	v.stopLocked()
	v.err = ""
	if err := rvLoad(); err != nil {
		v.err = err.Error()
		v.publishLocked()
		return
	}
	host, ps, err := net.SplitHostPort(v.relay)
	port, _ := strconv.Atoi(ps)
	if err != nil || port == 0 || v.token == "" {
		v.err = "el servidor no mandó un relay de voz válido"
		v.publishLocked()
		return
	}
	// macOS asks for the microphone once; the engine starts anyway (the others are heard) and again once allowed
	switch micAccess() {
	case 0:
		micRequest(func(ok bool) {
			v.mu.Lock()
			defer v.mu.Unlock()
			if ok && v.on {
				v.startLocked()
			} else if !ok {
				v.err = micDeniedText
				v.publishLocked()
			}
		})
	case 1, 2:
		v.err = micDeniedText
	}
	rvDevice(1, v.cfg.Mic)
	rvDevice(0, v.cfg.Speaker)
	v.applyLocked()
	if r := rvStart(host, port, v.token, v.slot); r != 0 {
		switch r {
		case -1:
			v.err = "no pude abrir el audio: revisa que haya altavoces o auriculares"
		case -2:
			v.err = "no llego al relay de voz (" + v.relay + ")"
		default:
			v.err = fmt.Sprintf("el chat de voz no arrancó (%d)", r)
		}
		v.publishLocked()
		return
	}
	v.on = true
	v.stop = make(chan struct{})
	go v.loop(v.stop, v.cfg.PTT)
	v.publishLocked()
}

func (v *voiceChat) stopLocked() {
	if !v.on {
		return
	}
	close(v.stop)
	v.on = false
	rvStop()
}

func (v *voiceChat) applyLocked() {
	rvSet(rvMuted, boolInt(v.cfg.Muted))
	rvSet(rvPTT, boolInt(v.cfg.PTT))
	rvSet(rvSense, v.cfg.Sense)
	rvSet(rvMicGain, v.cfg.MicGain)
	rvSet(rvVolume, v.cfg.Volume)
	rvSet(rvEcho, boolInt(v.cfg.Echo))
	v.applyPeersLocked()
}

// applyPeersLocked: each slot gets the volume and mute the player chose for whoever is in it now.
func (v *voiceChat) applyPeersLocked() {
	if rvLoad() != nil {
		return
	}
	for s := 1; s <= rvSlots; s++ {
		vol, muted := 100, 0
		for u, us := range v.members {
			if us == s {
				if p, ok := v.cfg.Peers[strings.ToLower(u)]; ok {
					vol = p.Volume
					if p.Muted {
						muted = 1
					}
				}
			}
		}
		rvPeer(s, vol, muted)
	}
}

// loop: push to talk (the key's state every 10 ms) and the live state for the interface (every 100 ms).
func (v *voiceChat) loop(stop chan struct{}, ptt bool) {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	was := false
	for n := 0; ; n++ {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if ptt {
			if d := pttDown(); d != was {
				rvSet(rvPTTDown, boolInt(d))
				was = d
			}
		}
		if n%10 == 0 {
			v.mu.Lock()
			if v.on {
				rvStats(v.stats[:])
				v.publishLocked()
			}
			v.mu.Unlock()
		}
	}
}

// VoicePlayer: one other player of the channel, as the interface shows them.
type VoicePlayer struct {
	User    string `json:"user"`
	Slot    int    `json:"slot"`
	Level   int    `json:"level"`
	Talking bool   `json:"talking"`
	Ms      int    `json:"ms"`   // mouth to ear, estimated (-1 unknown)
	Loss    int    `json:"loss"` // per mille
	Volume  int    `json:"volume"`
	Muted   bool   `json:"muted"`  // by me
	Silent  bool   `json:"silent"` // by the room's owner
}

type VoiceLive struct {
	VoiceSettings
	Channel string        `json:"channel"`
	On      bool          `json:"on"`
	State   int           `json:"state"` // the engine's: 1 connecting, 2 live, <0 failed
	Ping    int           `json:"ping"`
	Level   int           `json:"level"`
	Sending bool          `json:"sending"`
	Me      int           `json:"me"`
	Silent  bool          `json:"silent"` // I was muted by the room's owner
	Players []VoicePlayer `json:"players"`
	Error   string        `json:"error"`
	PTTKey  string        `json:"pttKey"`
}

func (v *voiceChat) liveLocked() VoiceLive {
	l := VoiceLive{VoiceSettings: v.cfg, Channel: v.channel, On: v.on, Me: v.slot, Error: v.err, PTTKey: pttKeyName,
		Players: []VoicePlayer{}}
	l.Peers = nil
	if v.on {
		l.State, l.Ping, l.Level, l.Sending = int(v.stats[rvStState]), int(v.stats[rvStPing]), int(v.stats[rvStLevel]), v.stats[rvStSending] != 0
	}
	for u, s := range v.members {
		silent := false
		for _, m := range v.muted {
			silent = silent || strings.EqualFold(m, u)
		}
		if s == v.slot {
			l.Silent = silent
			continue
		}
		p := VoicePlayer{User: u, Slot: s, Ms: -1, Volume: 100, Silent: silent}
		if c, ok := v.cfg.Peers[strings.ToLower(u)]; ok {
			p.Volume, p.Muted = c.Volume, c.Muted
		}
		if v.on && s >= 1 && s <= rvSlots {
			q := v.stats[rvStPeers+(s-1)*rvPeerN:]
			p.Level, p.Talking, p.Ms, p.Loss = int(q[0]), q[1] != 0, int(q[2]), int(q[3])
		}
		l.Players = append(l.Players, p)
	}
	return l
}

func (v *voiceChat) publishLocked() { v.emit("voice", v.liveLocked()) }

func (v *voiceChat) live() VoiceLive {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.liveLocked()
}

func (v *voiceChat) saveLocked() {
	b, _ := json.MarshalIndent(v.cfg, "", "  ")
	_ = os.WriteFile(v.path, b, 0o600)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// set: the player changed the voice settings in the interface.
func (v *voiceChat) set(s VoiceSettings) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s.Peers = v.cfg.Peers
	s.Sense = min(max(s.Sense, 0), 100)
	s.MicGain = min(max(s.MicGain, 0), 400)
	s.Volume = min(max(s.Volume, 0), 200)
	old := v.cfg
	v.cfg = s
	v.saveLocked()
	switch {
	case !s.Enabled:
		v.stopLocked()
	case v.channel != "" && (!v.on || s.Echo != old.Echo || s.PTT != old.PTT):
		v.startLocked() // echo changes the devices; push to talk changes the loop
	case v.on:
		if s.Mic != old.Mic {
			rvDevice(1, s.Mic)
		}
		if s.Speaker != old.Speaker {
			rvDevice(0, s.Speaker)
		}
		v.applyLocked()
	}
	v.publishLocked()
}

// toggleMute: the microphone button, or Shift+M during the game (with a beep: the launcher is not on screen).
func (v *voiceChat) toggleMute(beep bool) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cfg.Muted = !v.cfg.Muted
	v.saveLocked()
	if v.on {
		rvSet(rvMuted, boolInt(v.cfg.Muted))
		if beep {
			rvSet(rvBeep, 1+boolInt(v.cfg.Muted)) // low: closed, high: open
		}
	}
	v.publishLocked()
	return v.cfg.Muted
}

// setPeer: my volume and mute for another player (kept for the next time we meet).
func (v *voiceChat) setPeer(user string, volume int, muted bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cfg.Peers[strings.ToLower(user)] = VoicePeer{Volume: min(max(volume, 0), 200), Muted: muted}
	v.saveLocked()
	if v.on {
		v.applyPeersLocked()
	}
	v.publishLocked()
}

func (v *voiceChat) devices() map[string][]string {
	if rvLoad() != nil {
		return map[string][]string{"mics": {}, "speakers": {}}
	}
	return map[string][]string{"mics": rvDevices(1), "speakers": rvDevices(0)}
}

func (v *voiceChat) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stopLocked()
}

const micDeniedText = "Recomp no tiene permiso para el micrófono: actívalo en Ajustes del Sistema > Privacidad y seguridad > Micrófono"

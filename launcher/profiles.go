package main

// The games the launcher knows. Everything that differs from one game to another lives in its profile: the disc it
// needs (and what to copy out of it), the game's own options, its GS host presets per system, and the variables its
// runtime gets offline and online. The rest of the launcher (account, friends, rooms, overlay, handover, stats) is
// shared. A game is installed under <launcher>/games/<id> (gamesRoot); an old DBZ BT4 zip, with runtime/ next to the
// launcher, still works as is (legacy layout, see game_windows.go).

import (
	"fmt"
	"os"
	"strings"
)

// GameOption: one of a game's own switches in the interface (Settings.Opts[Key]).
type GameOption struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Default bool   `json:"default"`
}

// qualityPreset: one entry of the Calidad menu, the GS host ini it uses and how the host draws it.
type qualityPreset struct {
	QualityOption
	Ini      string // file in gshost/ (copied from gshost/presets/ the first time)
	Renderer string // GS host renderer: "vulkan", "dx12", "metal"
}

type profile struct {
	ID, Title, Short string // Short: the sidebar's and the friends list's name
	Banner           string // css class of the banner art
	Sub              string // the banner's subtitle
	Serial           string // PS2X_LIVE_SERIAL (the GS host's per-game settings and patches)
	Elf, ElfSHA256   string // the disc's executable, checked: the game code was recompiled from exactly that ELF
	BootName         string // the name the runner starts the ELF as (DBZ BT4 boots as BT3's SLUS_216.78)
	CopyDirs         []string
	CardPrefix       string // the game's memory card folders (BASLUS-..., BESLES-...)
	Repo             string // GitHub owner/name: source link, releases with the game's pack
	ISOTitle         string // the file dialog's title
	ISOMissing       string // what the play line says before the disc is chosen
	Controls         string // the Controles paragraph (HTML)
	ControlsAndroid  string // the same on Android (touch controls, Bluetooth pads); empty = Controls
	Options          []GameOption
	Qualities        map[string][]qualityPreset // by platformName
	DLSSFolder       bool                       // Windows: the DLSS DLL folder and drop zone
	Fps60Sites       bool                       // runtime/fps60_sites.txt is part of the online build key
	// iniFor: the GS host ini for a quality, given the game's options (nil: the preset's Ini)
	iniFor func(pr *profile, p qualityPreset, s Settings) string
	// env: the runtime's variables for this game, offline (m == nil) and online
	env func(g *game, s Settings, m *netMatch) []string
	// afterGame: called when the game closes (e.g. read back a setting the game's own menu changed)
	afterGame func(g *game, hostIni string)
	// netProtocol: who can play with whom online (bump it when the netplay changes what crosses the wire)
	netProtocol string
	// minPack: where the runner ships in a downloadable pack (Windows), online needs at least this pack version.
	// Below it the build key diverges so a stale pack cannot pair with up-to-date clients and desync. Empty: no gate.
	minPack string
}

func (p *profile) qualities() []qualityPreset { return p.Qualities[platformName] }

// controls: the Controles paragraph for this system.
func (p *profile) controls() string {
	if platformName == "Android" && p.ControlsAndroid != "" {
		return p.ControlsAndroid
	}
	return p.Controls
}

func (p *profile) opt(s Settings, key string) bool {
	if v, ok := s.Opts[key]; ok {
		return v
	}
	for _, o := range p.Options {
		if o.Key == key {
			return o.Default
		}
	}
	return false
}

func (p *profile) supported() bool { return len(p.qualities()) > 0 }

// windowsOnlyOptions: switches that only do something on Windows (frame generation: the Mac window takes its frames
// through an IOSurface, and nativeFrameGen is empty there), kept out of the interface elsewhere.
var windowsOnlyOptions = map[string]bool{"framegen": true}

// shownOptions: the game's switches the interface lists on this system.
func (p *profile) shownOptions() []GameOption {
	var out []GameOption
	for _, o := range p.Options {
		if windowsOnlyOptions[o.Key] && platformName != "Windows" {
			continue
		}
		out = append(out, o)
	}
	return out
}

var profiles = []*profile{dbzProfile, br4Profile, nbaProfile}

func profileByID(id string) *profile {
	for _, p := range profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ---- DBZ Budokai Tenkaichi 4 (Team BT4's mod of BT3, Beta 14 Rev2) ----

var dbzProfile = &profile{
	ID: "dbzbt4", Title: "Dragon Ball Z Budokai Tenkaichi 4", Short: "DBZ Budokai Tenkaichi 4", Banner: "dbz",
	Sub: "Dragon Ball Z (Beta 14 Rev2)", Serial: "SLUS-21978",
	Elf: "SLUS_219.78", ElfSHA256: "224525C5639C00E01C2604C4685D307BC739C041000DCF8C69A4F884F465E8A4",
	BootName: "SLUS_216.78", CopyDirs: []string{"IRX", "BIN"}, CardPrefix: "BASLUS-",
	Repo:       "recomp-code/dbz-bt4-recomp",
	ISOTitle:   "Elige la ISO de DBZ Budokai Tenkaichi 4 (Beta 14 Rev2)",
	ISOMissing: "Necesitas tu ISO del mod (SLUS-219.78, Beta 14 Rev2).",
	Controls: "Mando de Xbox: A cruz, B círculo, X cuadrado, Y triángulo. Mantén START (o pulsa F1) para el menú de " +
		"opciones del juego. Durante la partida, <b>Back + Start</b> (o Shift+Tab) abre esta ventana encima, para ver quién " +
		"está en línea o invitar; aquí se maneja con la cruceta, A para aceptar y B para volver.",
	ControlsAndroid: "Controles táctiles en pantalla: el stick aparece donde pongas el pulgar izquierdo. Un mando Bluetooth " +
		"(Xbox, PlayStation u otro) funciona solo y esconde los controles táctiles. El botón <b>≡</b> (abajo, en el centro) abre el menú de " +
		"opciones del juego.",
	Options: []GameOption{
		{"fps60", "Peleas a 60 fps", "En PS2 iban a 30. También se cambia dentro del juego.", true},
		{"unlock", "Contenido desbloqueado", "Todos los personajes, escenarios y modos desde el principio, como el truco del menú. " +
			"Cuando el juego guarda, se queda en tu memoria. En línea siempre está activado.", true},
		{"framegen", "Más fps en pantalla", "Solo con la calidad Nativo en Windows: crea un cuadro intermedio entre cada dos con el movimiento real del juego, " +
			"hasta 120 en pantalla sin acelerar el juego. En monitores de menos de 90 Hz se apaga sola.", true},
	},
	Qualities: map[string][]qualityPreset{
		// Windows: only the runtime's own Vulkan renderer, as on macOS (no PCSX2 host any more): render 2x, upscaled to the
		// window with DLSS (NVIDIA RTX) or FSR 2 (any GPU) fed the game's real depth and motion. Online with everyone. A
		// quality saved from the old list (6x, 3x, DLSS / FSR, DLSS 5) is out of range and falls back to this one.
		"Windows": {
			{QualityOption{"Nativo + DLSS / FSR 2", "El renderizador propio en Vulkan: DLSS en NVIDIA RTX, FSR 2 en las demás. También en línea."}, "", "native"},
		},
		// macOS: only the runtime's own renderer on Vulkan (MoltenVK), no PCSX2 host: render 2x, upscaled to the screen's
		// real pixels (Retina) by MetalFX's temporal scaler with the game's real depth and motion. A 60 fps fight in a
		// Retina fullscreen holds 60 on an M2 Pro (the PCSX2 presets were 6x 13.5 ms of GPU per frame, MetalFX 10.5 ms).
		// Online too (plays with PCSX2 players). A saved quality from the old list is out of range and falls back to it.
		// Android: the runtime's own renderer on the phone's Vulkan; the quality picks the render scale and the upscaler
		// (game_android.go, androidQuality). Online with everyone.
		"Android": {
			{QualityOption{"Equilibrada", "La resolución de la PS2 reescalada a tu pantalla (SGSR 2 en Snapdragon, FSR 2 en los demás). La recomendada para casi todos los teléfonos. También en línea."}, "", "native"},
			{QualityOption{"Alta", "El doble de resolución, reescalada con SGSR 2 en Snapdragon o FSR 2 en los demás. Para teléfonos de gama alta. También en línea."}, "", "native"},
			{QualityOption{"Ahorro de batería", "La resolución de la PS2 sin reescalador: lo que menos gasta. También en línea."}, "", "native"},
		},
		"macOS": {
			{QualityOption{"Nativo + MetalFX", "El renderizador propio, sin emulador de gráficos: más fps y menos CPU; lo reescala MetalFX de Apple a la resolución de tu pantalla. También en línea."}, "", "native"},
		},
	},
	DLSSFolder: true, Fps60Sites: true,
	netProtocol: "net3", // net3: the detmath layer (portable sin/cos kernels and acosf); net2 builds drift apart from it
	minPack:     "2.5.7", // detmath first ships in the Windows pack here; older packs run net3 but desync
	env:         dbzEnv,
	afterGame:   dbzReadBackFps60,
}

func dbzEnv(g *game, s Settings, m *netMatch) []string {
	fps60, unlock := g.p.opt(s, "fps60"), g.p.opt(s, "unlock")
	env := []string{"PS2X_HALFSTEP=" + g.sites}
	if m != nil {
		fps60 = m.Fps60 // both players must run the same mode: the match decides
		// both games must also start from the same unlocks: online, everything is unlocked (the whole roster for both)
		unlock = true
		// the jump stops at the game's multiplayer menu: player 1 picks Simple / Equipos / Equipos PD there, as on
		// the console, and both go on to character select together; each game boots on its own and the two meet at
		// that menu ("waiting for <opponent>" until the other one is there)
		env = append(env, "NET_OVERLAY=1", "PS2X_NET_JUMP=1", "PS2X_NET_JUMP_STOP=menu", "PS2X_NET_MEET=menu")
		// Lockstep needs two identical simulations, whatever each machine's speed or system: the virtual clock
		// (frame-stepped, fibers), the same random seed and clock (from the match token), the same memory card
		// (matchCard); the two compare a hash of the game state every second, so any difference shows at once.
		seed, _ := matchSeeds(m.Token)
		env = append(env, "PS2X_FRAMESTEP=1", "PS2X_FIBERS=1", fmt.Sprintf("PS2X_RANDSEED=%d", seed))
	} else {
		// Offline the GS host skips its synchronous GPU readbacks (HWDownloadMode 2): the glow builds its palette from
		// a render target every frame and the host waited on the GPU for it -- +12% frames in an uncapped fight, the
		// picture unchanged. Online keeps the preset's accurate mode: a readback can reach the game, and both machines
		// must see the same data.
		env = append(env, "PS2X_HWDOWNLOAD_MODE=2")
	}
	return append(env, "PS2X_FPS60="+b01(fps60), "PS2X_UNLOCK_ALL="+b01(unlock))
}

// dbzReadBackFps60: the in-game menu saves "DbzFps60 = true/false" in its ini; it becomes the next start's default.
func dbzReadBackFps60(g *game, iniPath string) {
	b, err := os.ReadFile(iniPath)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.ReplaceAll(strings.TrimSpace(l), " ", "")
		if strings.HasPrefix(strings.ToLower(l), "dbzfps60=") {
			g.mu.Lock()
			if g.cfg.Opts == nil {
				g.cfg.Opts = map[string]bool{}
			}
			g.cfg.Opts["fps60"] = strings.HasSuffix(strings.ToLower(l), "true")
			_ = g.save()
			g.mu.Unlock()
		}
	}
}

// ---- Bloody Roar 4 (PAL, SLES-51877, with the Analog Stick Mod) ----

var br4Profile = &profile{
	ID: "br4", Title: "Bloody Roar 4", Short: "Bloody Roar 4", Banner: "br", Sub: "Hudson Soft / Eighting (PAL)",
	Serial: "SLES-51877", Elf: "SLES_518.77", ElfSHA256: "228548DAB8E4EFD03316C643F39210498691C24C9C39D65DF7715A74BF83DCDB",
	BootName: "SLES_518.77", CopyDirs: []string{"MODULES"}, CardPrefix: "BESLES-",
	Repo:       "recomp-code/bloody-roar4-recomp",
	ISOTitle:   "Elige la ISO de Bloody Roar 4 (PAL, SLES-51877)",
	ISOMissing: "Necesitas tu ISO de Bloody Roar 4 (PAL, SLES-51877, con el Analog Stick Mod).",
	Controls: "Mando de Xbox: A cruz, B círculo, X cuadrado, Y triángulo; el stick izquierdo mueve como la cruceta. Teclado: " +
		"flechas, Z/X/C/V, Q/E = L1/R1, Enter = START. F1 abre las opciones gráficas. Durante la partida, <b>Back + Start</b> " +
		"(o Shift+Tab) abre esta ventana encima.",
	Options: []GameOption{
		{"widescreen", "Pantalla panorámica (16:9)", "Apagado: la imagen original 4:3.", true},
	},
	Qualities: map[string][]qualityPreset{
		"Windows": {
			{QualityOption{"Máxima (6x)", "6x interno, para gráficas potentes."}, "br4.ini", "vulkan"},
			{QualityOption{"DLSS / FSR (2x reescalado)", "Reescala a tu pantalla: DLSS en NVIDIA RTX, FSR en las demás."}, "br4_dlss.ini", "vulkan"},
		},
	},
	DLSSFolder:  true,
	netProtocol: "br4net1",
	iniFor: func(pr *profile, p qualityPreset, s Settings) string {
		if !pr.opt(s, "widescreen") {
			return strings.TrimSuffix(p.Ini, ".ini") + "_4x3.ini"
		}
		return p.Ini
	},
	env: func(g *game, s Settings, m *netMatch) []string {
		env := []string{"PS2X_PAD_ANALOG=1", "PS2X_BR4_WIDESCREEN=" + b01(g.p.opt(s, "widescreen"))}
		if m != nil {
			// [netplay] the game is a function of its inputs: emulated time from the game's own work
			// (PS2X_DETERMINISTIC), the same clock (PS2X_RTC, matchSeeds) and the same memory card on both sides
			env = append(env, "PS2X_DETERMINISTIC=1")
		}
		return env
	},
}

// ---- NBA Street V3 (NTSC-U, SLUS-21126) ----

// Online here is the game's own 2-player local match played by two machines in lockstep (the BR4 runtime's
// deterministic mode, see street-recomp-v3's docs/ONLINE_LOCKSTEP.md): any court, the ballers on the match's memory
// card. The EA-lobby online of the standalone NBA Street V3 PC release is a different mode and is not offered here
// (its network adapter needs PS2X_NET, a name this mode must never set).
var nbaProfile = &profile{
	ID: "nbastreetv3", Title: "NBA Street V3", Short: "NBA Street V3", Banner: "nba", Sub: "EA Sports BIG (NTSC-U)",
	Serial: "SLUS-21126", Elf: "SLUS_211.26", ElfSHA256: "D8ECAE84A16F8AFE930FB29AFF5CB4CA88C35D8C92A3F6A4FBB9219B692AA937",
	BootName: "SLUS_211.26", CardPrefix: "BASLUS-", // the IOP modules and the game's archives are read from the ISO
	Repo:       "recomp-code/street-recomp-v3",
	ISOTitle:   "Elige la ISO de NBA Street V3 (NTSC-U, SLUS-21126)",
	ISOMissing: "Necesitas tu ISO de NBA Street V3 (NTSC-U, SLUS-21126).",
	Controls: "Mando de Xbox: A cruz, B círculo, X cuadrado, Y triángulo. Teclado: flechas, Z/X/C/V, Q/E = L1/R1, " +
		"Enter = START. F1 abre las opciones gráficas. En línea, el rival es el jugador 2: en GAME ON elegid lado en " +
		"USER SETUP y cada uno su equipo. Durante la partida, <b>Back + Start</b> (o Shift+Tab) abre esta ventana encima.",
	Options: []GameOption{
		{"widescreen", "Pantalla panorámica (16:9)", "Apagado: la imagen original 4:3.", true},
	},
	Qualities: map[string][]qualityPreset{
		"Windows": {
			{QualityOption{"Máxima (4x)", "4x interno, para gráficas potentes."}, "nba.ini", "vulkan"},
			{QualityOption{"DLSS / FSR (2x reescalado)", "Reescala a tu pantalla: DLSS en NVIDIA RTX, FSR en las demás."}, "nba_dlss.ini", "vulkan"},
		},
	},
	DLSSFolder:  true,
	netProtocol: "nbanet1",
	iniFor: func(pr *profile, p qualityPreset, s Settings) string {
		if !pr.opt(s, "widescreen") {
			return strings.TrimSuffix(p.Ini, ".ini") + "_4x3.ini"
		}
		return p.Ini
	},
	env: func(g *game, s Settings, m *netMatch) []string {
		if m != nil {
			// [netplay] as Bloody Roar 4: emulated time from the game's own work, the match's clock and memory card
			return []string{"PS2X_DETERMINISTIC=1"}
		}
		return nil
	},
}

// gamesRoot: where the launcher installs games (game_windows.go: next to Recomp.exe; game_darwin.go: Application
// Support, since the app itself is read-only).

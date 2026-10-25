package main

// [library] Adding and updating games, ready to play, nothing to compile. Each game's GitHub repo publishes in its
// releases a manifest, recomp-game.json:
//
//	{"id": "br4", "version": "1.0", "packs": {"Windows": {"file": "BR4_Windows.zip", "sha256": "...", "size": 123}}}
//
// and the pack it names: the game's runtime and GS host for that system -- never the game itself; the player's ISO
// stays the player's, and the launcher copies the boot files out of it (game.prepare). The launcher downloads the
// pack from the release, checks its SHA-256, unpacks it beside the installed copy and swaps the two, keeping the
// player's files (settings, logs, saves, the files taken from the ISO, DLSS DLLs). A game is added from the list of
// games this launcher knows (profiles.go) or by pasting its repo's address; a game this version does not know needs
// a newer launcher (updater.go updates it by itself).

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const manifestName = "recomp-game.json"

type packFile struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Version: recomp-launcher.json only, the version of this system's file when it is not the manifest's (a release
	// built on one system keeps another system's previous file: that system must not take it for the new version)
	Version string `json:"version,omitempty"`
}

type gameManifest struct {
	ID          string              `json:"id"`
	Version     string              `json:"version"`
	LauncherMin string              `json:"launcherMin,omitempty"`
	Packs       map[string]packFile `json:"packs"`
	// filled in by the launcher
	Repo string `json:"repo,omitempty"`
	Tag  string `json:"tag,omitempty"`
	URL  string `json:"url,omitempty"` // the pack's download address
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

var (
	repoRe   = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([A-Za-z0-9_.-]{1,100})/([A-Za-z0-9_.-]{1,100})(?:\.git)?/?(?:[#?].*)?$`)
	ownerRe  = regexp.MustCompile(`^([A-Za-z0-9_.-]{1,100})/([A-Za-z0-9_.-]{1,100})$`)
	httpLong = &http.Client{Timeout: 30 * time.Minute} // downloads
	httpAPI  = &http.Client{Timeout: 20 * time.Second} // GitHub's API and the manifests
)

// parseRepo: "https://github.com/owner/name", "github.com/owner/name" or "owner/name" -> "owner/name".
func parseRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := repoRe.FindStringSubmatch(s); m != nil {
		return m[1] + "/" + strings.TrimSuffix(m[2], ".git"), nil
	}
	if m := ownerRe.FindStringSubmatch(s); m != nil {
		return m[1] + "/" + m[2], nil
	}
	return "", errors.New("pega la dirección del repositorio en GitHub (https://github.com/dueño/juego)")
}

// githubAPI: the GitHub API base (RECOMP_GITHUB_API for tests).
func githubAPI() string {
	if v := os.Getenv("RECOMP_GITHUB_API"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

func getJSON(u string, v any) error {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Recomp/"+launcherVersion)
	resp, err := httpAPI.Do(req)
	if err != nil {
		return fmt.Errorf("sin conexión con GitHub: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("no encuentro ese repositorio o no tiene versiones publicadas")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub respondió %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// latestRelease: the repo's latest release (GitHub's "latest": published, not a pre-release).
func latestRelease(repo string) (*ghRelease, error) {
	var rel ghRelease
	if err := getJSON(githubAPI()+"/repos/"+repo+"/releases/latest", &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// gameRelease: the newest published release that carries a Recomp pack. A game repo may also publish releases of
// its own (a standalone build) that have no manifest; "latest" alone would then hide the pack.
func gameRelease(repo string) (*ghRelease, error) {
	var rels []ghRelease
	if err := getJSON(githubAPI()+"/repos/"+repo+"/releases?per_page=30", &rels); err != nil {
		return nil, err
	}
	for i := range rels {
		if rels[i].Draft || rels[i].Prerelease {
			continue
		}
		for _, a := range rels[i].Assets {
			if a.Name == manifestName {
				return &rels[i], nil
			}
		}
	}
	return latestRelease(repo) // none carries one: fetchManifest reports it on the latest
}

// fetchManifest: the game manifest of the repo's newest Recomp release, with this system's pack resolved to a URL.
func fetchManifest(repo string) (*gameManifest, error) {
	rel, err := gameRelease(repo)
	if err != nil {
		return nil, err
	}
	var murl string
	for _, a := range rel.Assets {
		if a.Name == manifestName {
			murl = a.URL
		}
	}
	if murl == "" {
		return nil, errors.New("esa versión no trae un paquete para Recomp (" + manifestName + ")")
	}
	var m gameManifest
	if err := getJSON(murl, &m); err != nil {
		return nil, err
	}
	m.Repo, m.Tag = repo, rel.Tag
	pf, ok := m.Packs[platformName]
	if !ok {
		return nil, fmt.Errorf("este juego todavía no tiene versión para %s", platformName)
	}
	for _, a := range rel.Assets {
		if a.Name == pf.File {
			m.URL = a.URL
		}
	}
	if m.URL == "" || !regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(pf.SHA256) {
		return nil, errors.New("el paquete del juego no está bien publicado")
	}
	return &m, nil
}

// newerVersion: a > b, comparing dot-separated numbers ("3.10" > "3.9"); anything not a number compares as 0.
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// installedManifest: the manifest of the installed pack (nil for an old zip or a game not installed).
func (g *game) installedManifest() *gameManifest {
	var m gameManifest
	if readJSONFile(filepath.Join(g.dir, manifestName), &m) != nil {
		return nil
	}
	return &m
}

// packVersion: the installed pack's version ("" when unknown).
func (g *game) packVersion() string {
	if m := g.installedManifest(); m != nil {
		return m.Version
	}
	return ""
}

// keepOnUpdate: what is the player's, carried from the old copy into the new one (relative to the game's folder).
var keepOnUpdate = []string{"DBZ_BT4.cfg", "settings.cfg", "logs", "DLSS", "netcard",
	filepath.Join("runtime", "data"), filepath.Join("runtime", "savedata")}

// download fetches u into path, checking size and SHA-256 as it goes; progress(done, total) is called every ~250 ms.
func download(u, path string, want packFile, progress func(done, total int64)) error {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" {
		return errors.New("dirección de descarga no válida")
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "Recomp/"+launcherVersion)
	resp, err := httpLong.Do(req)
	if err != nil {
		return fmt.Errorf("la descarga falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("la descarga falló: %s", resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	total := want.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	var done int64
	last := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if want.Size > 0 && done > want.Size {
				f.Close()
				return errors.New("el paquete es más grande de lo anunciado")
			}
			if progress != nil && time.Since(last) > 250*time.Millisecond {
				progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("la descarga se cortó: %v", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if progress != nil {
		progress(done, total)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want.SHA256) {
		return errors.New("el paquete descargado no coincide con el publicado (SHA-256): no lo instalo")
	}
	return nil
}

// unzipTo writes the zip's files under dir. Paths that would climb out of dir are refused.
func unzipTo(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("el paquete no es un zip válido")
	}
	defer zr.Close()
	base, _ := filepath.Abs(dir)
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		target := filepath.Join(base, name)
		if rel, err := filepath.Rel(base, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			return errors.New("el paquete tiene rutas no válidas")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// install downloads and installs (or updates) a game from its manifest. The game must be closed.
func (a *App) install(g *game, m *gameManifest) error {
	if g.running() {
		return errors.New("cierra el juego antes de actualizarlo")
	}
	if m.LauncherMin != "" && newerVersion(m.LauncherMin, launcherVersion) {
		return fmt.Errorf("este juego necesita Recomp %s o más nuevo: actualiza el launcher", m.LauncherMin)
	}
	pf := m.Packs[platformName]
	root := gamesRoot(a.root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(root, g.p.ID+".download")
	defer os.Remove(zipPath)
	err := download(m.URL, zipPath, pf, func(done, total int64) {
		a.emit("install", map[string]any{"game": g.p.ID, "phase": "download", "done": done, "total": total})
	})
	if err != nil {
		return err
	}
	a.emit("install", map[string]any{"game": g.p.ID, "phase": "unpack"})
	dest := filepath.Join(root, g.p.ID)
	fresh := dest + ".new"
	_ = os.RemoveAll(fresh)
	if err := unzipTo(zipPath, fresh); err != nil {
		_ = os.RemoveAll(fresh)
		return err
	}
	// the player's files, from the installed copy (games/<id>, or the launcher's folder for an old DBZ zip)
	for _, rel := range keepOnUpdate {
		src := filepath.Join(g.dir, rel)
		if !fileExists(src) {
			continue
		}
		dst := filepath.Join(fresh, rel)
		_ = os.RemoveAll(dst)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if g.dir == dest {
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("no pude conservar %s: %v", rel, err)
			}
		} else if err := copyAny(src, dst); err != nil { // an old layout: copied, the old copy is left as it was
			return fmt.Errorf("no pude conservar %s: %v", rel, err)
		}
	}
	// the player's GS host settings (the presets they changed with F1), when the new pack still has a GS host
	if ents, err := os.ReadDir(filepath.Join(g.dir, "gshost")); err == nil && fileExists(filepath.Join(fresh, "gshost")) {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".ini") {
				_ = copyIfChanged(filepath.Join(g.dir, "gshost", e.Name()), filepath.Join(fresh, "gshost", e.Name()))
			}
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(fresh, manifestName), b, 0o644); err != nil {
		return err
	}
	old := dest + ".old"
	_ = os.RemoveAll(old)
	if fileExists(dest) {
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("no pude reemplazar la versión instalada (¿un archivo abierto?): %v", err)
		}
	}
	if err := os.Rename(fresh, dest); err != nil {
		_ = os.Rename(old, dest)
		return err
	}
	_ = os.RemoveAll(old)
	// the game object now points at games/<id>
	ng := newGame(a.root, dest, g.p)
	a.mu.Lock()
	a.games[g.p.ID] = ng
	if a.game == g {
		a.game = ng
	}
	a.mu.Unlock()
	if a.game == ng {
		a.net.setGame(ng.p.ID, ng.build())
	}
	a.emit("install", map[string]any{"game": g.p.ID, "phase": "done", "version": m.Version})
	return nil
}

func copyAny(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return copyTree(src, dst)
	}
	return copyIfChanged(src, dst)
}

// ---- called from the interface ----

// CatalogEntry: a game the player can add (or update).
type CatalogEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Repo      string `json:"repo"`
	Installed bool   `json:"installed"`
	Supported bool   `json:"supported"`
	Version   string `json:"version"` // installed
	InApp     bool   `json:"inApp"`   // comes inside the launcher and is updated with it (DBZ BT4 on macOS)
}

func (a *App) Catalog() []CatalogEntry {
	var out []CatalogEntry
	for _, p := range profiles {
		g := a.games[p.ID]
		e := CatalogEntry{ID: p.ID, Title: p.Title, Repo: p.Repo, Installed: g.installed(), Supported: p.supported(), Version: g.packVersion()}
		if g.inApp() {
			e.InApp, e.Version = true, launcherVersion
		}
		out = append(out, e)
	}
	return out
}

// AddGame: from the catalog (a game id) or a pasted repo address. Runs in the background; the interface follows
// the "install" events. "" = started.
func (a *App) AddGame(idOrRepo string) string {
	var p *profile
	if p = profileByID(strings.TrimSpace(idOrRepo)); p == nil {
		repo, err := parseRepo(idOrRepo)
		if err != nil {
			return err.Error()
		}
		m, err := fetchManifest(repo)
		if err != nil {
			return err.Error()
		}
		if p = profileByID(m.ID); p == nil {
			return "esta versión de Recomp no conoce ese juego: se actualiza sola; prueba de nuevo cuando se haya actualizado"
		}
		go a.installFrom(a.games[p.ID], m)
		return ""
	}
	if !p.supported() {
		return "este juego todavía no tiene versión para " + platformName
	}
	g := a.games[p.ID]
	if g.inApp() { // its repo has no pack for this system: looking for an update means looking for a new launcher
		go a.checkInAppUpdate(g)
		return ""
	}
	go func() {
		m, err := fetchManifest(p.Repo)
		if err != nil {
			a.emit("install", map[string]any{"game": p.ID, "phase": "error", "error": err.Error()})
			return
		}
		if m.ID != p.ID {
			a.emit("install", map[string]any{"game": p.ID, "phase": "error", "error": "el repositorio publica otro juego"})
			return
		}
		a.installFrom(g, m)
	}()
	return ""
}

// checkInAppUpdate: "Buscar actualización" on a game inside the app. A new launcher shows its own notes
// ("launcher_update"); otherwise the game is up to date ("current").
func (a *App) checkInAppUpdate(g *game) {
	if pendingUpdate != "" {
		a.emit("launcher_update", map[string]any{"version": pendingUpdate, "phase": "ready"})
		return
	}
	if err := a.checkLauncherUpdate(); err != nil {
		a.emit("launcher_update", map[string]any{"error": err.Error()})
		return
	}
	if pendingUpdate == "" {
		a.emit("install", map[string]any{"game": g.p.ID, "phase": "current", "version": launcherVersion})
	}
}

func (a *App) installFrom(g *game, m *gameManifest) {
	if err := a.install(g, m); err != nil {
		a.emit("install", map[string]any{"game": g.p.ID, "phase": "error", "error": err.Error()})
	}
}

// checkGameUpdates: for each installed game, whether its repo published a newer pack ("update" events).
func (a *App) checkGameUpdates() {
	for _, p := range profiles {
		g := a.games[p.ID]
		if !g.installed() || !p.supported() {
			continue
		}
		m, err := fetchManifest(p.Repo)
		if err != nil || m.ID != p.ID {
			continue
		}
		if _, ok := m.Packs[platformName]; !ok {
			continue // no pack for this system (macOS: DBZ BT4 comes inside the app and is updated with it)
		}
		if cur := g.packVersion(); cur == "" || newerVersion(m.Version, cur) {
			a.emit("update", map[string]any{"game": p.ID, "version": m.Version, "current": cur})
		}
	}
}

package main

// [netcard] Online both games boot from the same memory card: player 1's. Each player has their own saves (options,
// unlocks), and the game loads its card at boot, so two different cards are two different games from the first
// frames. Player 1 zips its card and hands it to the server for the match; player 2 fetches it; both run the match
// on a temporary copy (PS2X_MC_ROOT), so nobody's own card is written by an online match. Plain data, the same on
// Windows and macOS.

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cardRoot: where the game keeps its memory cards (savedata/ beside data/, on both systems; for PS2Recomp games the
// launcher points PS2X_MC_ROOT there).
func (g *game) cardRoot() string { return filepath.Join(g.runtimeDir, "savedata") }

// packCard zips the game's save folders (not the runtime's settings or logs). An empty card is an empty zip: still
// the same on both sides.
// hostJunk: files an operating system leaves in folders on its own, never part of a PS2 memory card.
func hostJunk(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(name, ".") || l == "thumbs.db" || l == "desktop.ini"
}

func packCard(root, prefix string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			// the host's own files (.DS_Store, ._* AppleDouble, Thumbs.db, desktop.ini) are not part of a PS2 save:
			// shared, they make the two games see different cards
			if f.IsDir() || hostJunk(f.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				return nil, err
			}
			w, err := zw.Create(e.Name() + "/" + f.Name())
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(b); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unpackCard writes the zip into dir (made empty first). Only plain "folder/file" entries: nothing can climb out.
func unpackCard(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return errors.New("la memory card de la partida no es válida")
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(f.Name, `\:`) ||
			parts[0] == ".." || parts[1] == ".." || f.UncompressedSize64 > maxCardBytes {
			return errors.New("la memory card de la partida no es válida")
		}
		if hostJunk(parts[1]) { // a card packed by an older launcher may carry them
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxCardBytes))
		rc.Close()
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Join(dir, parts[0]), 0o755)
		if err := os.WriteFile(filepath.Join(dir, parts[0], parts[1]), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

const maxCardBytes = 512 << 10

// matchCard prepares the match's card and returns the folder to boot from: player 1 uploads its own, player 2
// waits for it (up to 30 s). The copy lives in <home>/netcard and is replaced by the next match.
func (a *App) matchCard(m *netMatch) (string, error) {
	dir := filepath.Join(a.game.home, "netcard")
	if m.Player == 1 {
		data, err := packCard(a.game.cardRoot(), a.game.p.CardPrefix)
		if err != nil {
			return "", fmt.Errorf("no pude leer tu memory card: %v", err)
		}
		if err := a.net.raw("POST", "/api/match/card?id="+m.ID, data, nil); err != nil {
			return "", fmt.Errorf("no pude compartir tu memory card: %v", err)
		}
		return dir, unpackCard(data, dir)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var data []byte
		var status int
		if err := a.net.raw("GET", "/api/match/card?id="+m.ID, nil, func(code int, b []byte) { status, data = code, b }); err != nil {
			return "", err
		}
		if status == http.StatusOK {
			return dir, unpackCard(data, dir)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", errors.New("el otro jugador no compartió su memory card")
}

// matchSeeds: the random seed and the clock both games start from, the same on both sides (from the match token).
func matchSeeds(token string) (seed uint64, rtc int64) {
	seed, _ = strconv.ParseUint(token[:16], 16, 64)
	d, _ := strconv.ParseUint(token[16:24], 16, 32)
	const jan2026 = 1767225600
	return seed, jan2026 + int64(d%(365*86400))
}

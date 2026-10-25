package main

// The player's own DLSS DLL (Windows): recognised by its version resource whatever its name, dropped on the window or
// picked from a zip. (The DLSS 5 host -- ReShade + RenoDX + NVIDIA's neural pass on a PCSX2 host -- went with the hosts.)

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type dlssKind int

const (
	dlssNone dlssKind = iota
	dlssSuperRes
)

func kindOf(path string) dlssKind {
	if !strings.EqualFold(filepath.Ext(path), ".dll") || !isX64(path) {
		return dlssNone
	}
	if v, ok := readFileVersion(path); ok && strings.Contains(strings.ToLower(v.Product), "deep learning supersampling") {
		return dlssSuperRes
	}
	return dlssNone
}

func (g *game) userDLSS(kind dlssKind) string {
	best, bestV := "", fileVersion{}
	_ = filepath.WalkDir(g.dlssDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || kindOf(p) != kind {
			return nil
		}
		v, _ := readFileVersion(p)
		if best == "" || bestV.less(v) {
			best, bestV = p, v
		}
		return nil
	})
	return best
}

func (g *game) superResDLL() string {
	if u := g.userDLSS(dlssSuperRes); u != "" {
		return u
	}
	// the DLSS the pack ships: runtime\dlss\ from DBZ pack 2.5 (no GS host), the host's presets\ before
	if p := filepath.Join(g.runtimeDir, "dlss", "nvngx_dlss.dll"); fileExists(p) {
		return p
	}
	if p := filepath.Join(g.gshostDir, "presets", "nvngx_dlss.dll"); fileExists(p) {
		return p
	}
	return filepath.Join(g.gshostDir, "nvngx_dlss.dll")
}

func (g *game) dlssInfo() DLSSInfo {
	sr := g.superResDLL()
	v, _ := readFileVersion(sr)
	return DLSSInfo{Version: v.String(), Own: g.userDLSS(dlssSuperRes) != "", Folder: g.dlssDir}
}

// addDLSS copies files the player dropped (or picked) into DLSS\ when they are DLSS files.
func (g *game) addDLSS(paths []string) (int, []string) {
	n, skipped := 0, []string(nil)
	_ = os.MkdirAll(g.dlssDir, 0o755)
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".zip") { // DLSS usually comes zipped (TechPowerUp)
			k, s := g.addDLSSZip(p)
			n, skipped = n+k, append(skipped, s...)
			continue
		}
		if kindOf(p) == dlssNone {
			skipped = append(skipped, filepath.Base(p))
			continue
		}
		if err := copyIfChanged(p, filepath.Join(g.dlssDir, filepath.Base(p))); err != nil {
			skipped = append(skipped, filepath.Base(p)+" ("+err.Error()+")")
			continue
		}
		n++
	}
	return n, skipped
}

// addDLSSZip takes the DLLs out of a zip: each one is unpacked to a temporary file, checked like a dropped DLL and
// kept only if it is one of ours.
func (g *game) addDLSSZip(p string) (int, []string) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return 0, []string{filepath.Base(p) + " (no se pudo abrir el zip)"}
	}
	defer zr.Close()
	n := 0
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		ext := strings.ToLower(filepath.Ext(name))
		if f.FileInfo().IsDir() || ext != ".dll" || f.UncompressedSize64 > 256<<20 {
			continue
		}
		tmp, err := os.CreateTemp(g.dlssDir, "zip-*-"+name)
		if err != nil {
			continue
		}
		rc, err := f.Open()
		if err == nil {
			_, err = io.Copy(tmp, io.LimitReader(rc, 256<<20))
			rc.Close()
		}
		tmp.Close()
		if err == nil && kindOf(tmp.Name()) != dlssNone && copyIfChanged(tmp.Name(), filepath.Join(g.dlssDir, name)) == nil {
			n++
		}
		os.Remove(tmp.Name())
	}
	if n == 0 {
		return 0, []string{filepath.Base(p) + " (no trae ninguna DLL de DLSS)"}
	}
	return n, nil
}

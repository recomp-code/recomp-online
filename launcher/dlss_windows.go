package main

// The player's own DLSS DLLs (Windows): recognised by their version resource whatever their name, dropped on the window
// or picked from a zip, and DLSS 5 (ReShade + the RenoDX add-on + NVIDIA's neural pass) from DLSS 5 Swapper.

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type dlssKind int

const (
	dlssNone dlssKind = iota
	dlssSuperRes
	dlssNeural
	dlssReShade
	dlssAddon
)

func kindOf(path string) dlssKind {
	name := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(name, ".addon64") {
		if strings.Contains(name, "dlss5") {
			return dlssAddon
		}
		return dlssNone
	}
	if !strings.HasSuffix(name, ".dll") || !isX64(path) {
		return dlssNone
	}
	v, ok := readFileVersion(path)
	if !ok {
		return dlssNone
	}
	switch {
	case strings.Contains(strings.ToLower(v.Product), "deep learning supersampling"):
		return dlssSuperRes
	case strings.EqualFold(v.Product, "NVIDIA DLSSNR"):
		return dlssNeural
	case strings.EqualFold(v.Product, "ReShade") && strings.Contains(v.Description, "64-bit"):
		return dlssReShade
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

func swapperFile(rel string) string {
	p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Programs\DLSS 5 Swapper\resources\payload`, rel)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// dlss5Parts: ReShade, the RenoDX DLSS 5 add-on and nvngx_dlssnr.dll (DLSS\ first, then DLSS 5 Swapper), or nil.
func (g *game) dlss5Parts() []string {
	r := firstNonEmpty(g.userDLSS(dlssReShade), swapperFile(`reshade-vulkan\ReShade64.dll`))
	a := firstNonEmpty(g.userDLSS(dlssAddon), swapperFile("renodx-dlss5.addon64"))
	n := firstNonEmpty(g.userDLSS(dlssNeural), swapperFile(`streamline\nvngx_dlssnr.dll`))
	if r == "" || a == "" || n == "" {
		return nil
	}
	return []string{r, a, n}
}

func (g *game) superResDLL() string {
	if u := g.userDLSS(dlssSuperRes); u != "" {
		return u
	}
	if p := filepath.Join(g.gshostDir, "presets", "nvngx_dlss.dll"); fileExists(p) {
		return p
	}
	return filepath.Join(g.gshostDir, "nvngx_dlss.dll")
}

func (g *game) dlssInfo() DLSSInfo {
	sr := g.superResDLL()
	v, _ := readFileVersion(sr)
	info := DLSSInfo{Version: v.String(), Own: g.userDLSS(dlssSuperRes) != "", Folder: g.dlssDir}
	if p := g.dlss5Parts(); p != nil {
		nv, _ := readFileVersion(p[2])
		info.DLSS5, info.DLSS5Neural = true, nv.String()
	}
	return info
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
		if f.FileInfo().IsDir() || (ext != ".dll" && ext != ".addon64") || f.UncompressedSize64 > 256<<20 {
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

func (g *game) prepareDLSS5Host() (string, error) {
	parts := g.dlss5Parts()
	if parts == nil {
		return "", errors.New("DLSS 5 necesita una NVIDIA RTX y DLSS 5 Swapper (o sus archivos en la carpeta DLSS)")
	}
	dir := filepath.Join(g.root, "gshost_dlss5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ents, _ := os.ReadDir(g.gshostDir)
	for _, e := range ents {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".exe" || ext == ".dll") {
			if err := copyIfChanged(filepath.Join(g.gshostDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	if err := copyTree(filepath.Join(g.gshostDir, "resources"), filepath.Join(dir, "resources")); err != nil {
		return "", err
	}
	if !fileExists(filepath.Join(dir, "portable.ini")) {
		_ = os.WriteFile(filepath.Join(dir, "portable.ini"), nil, 0o644)
	}
	for i, n := range []string{"dxgi.dll", "renodx-dlss5.addon64", "nvngx_dlssnr.dll"} {
		if err := copyIfChanged(parts[i], filepath.Join(dir, n)); err != nil {
			return "", err
		}
	}
	for _, n := range []string{"dbz_dlss_dx12.ini", "ReShade.ini"} {
		if !fileExists(filepath.Join(dir, n)) {
			_ = copyIfChanged(filepath.Join(g.gshostDir, "presets", n), filepath.Join(dir, n))
		}
	}
	return dir, nil
}

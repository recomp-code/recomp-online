package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// A DLSS DLL dropped as is or inside a zip ends up in the DLSS folder; anything else is refused.
func TestAddDLSS(t *testing.T) {
	src := os.Getenv("RECOMP_TEST_DLSS") // a real nvngx_dlss.dll
	if src == "" {
		t.Skip("RECOMP_TEST_DLSS not set")
	}
	dll, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	g := &game{root: root, dlssDir: filepath.Join(root, "DLSS")}

	zp := filepath.Join(root, "nvngx_dlss_310.zip")
	zf, _ := os.Create(zp)
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("nvngx_dlss_310/nvngx_dlss.dll")
	w.Write(dll)
	w, _ = zw.Create("readme.txt")
	w.Write([]byte("hola"))
	zw.Close()
	zf.Close()
	junk := filepath.Join(root, "otra.dll")
	os.WriteFile(junk, []byte("MZ no es una dll"), 0o644)

	n, skipped := g.addDLSS([]string{zp, junk})
	if n != 1 || len(skipped) != 1 {
		t.Fatalf("added %d, skipped %v", n, skipped)
	}
	if b, err := os.ReadFile(filepath.Join(g.dlssDir, "nvngx_dlss.dll")); err != nil || len(b) != len(dll) {
		t.Fatalf("dll not copied: %v", err)
	}
	ents, _ := os.ReadDir(g.dlssDir)
	if len(ents) != 1 {
		t.Fatalf("leftovers in the DLSS folder: %d files", len(ents))
	}
}

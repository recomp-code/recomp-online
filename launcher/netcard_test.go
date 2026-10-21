package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The card round-trips (only the save folders, not settings or logs) and a hostile zip cannot write outside.
func TestCardPackUnpack(t *testing.T) {
	root := t.TempDir()
	save := filepath.Join(root, "BASLUS-21978DBZT4")
	os.MkdirAll(save, 0o755)
	os.WriteFile(filepath.Join(save, "BASLUS-21978DBZT4"), bytes.Repeat([]byte{7}, 16384), 0o644)
	os.WriteFile(filepath.Join(save, "icon.sys"), []byte("icon"), 0o644)
	os.WriteFile(filepath.Join(root, "settings.toml"), []byte("[video]"), 0o644)

	data, err := packCard(root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "netcard")
	if err := unpackCard(data, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "BASLUS-21978DBZT4", "BASLUS-21978DBZT4")); len(b) != 16384 {
		t.Fatalf("save not copied: %d bytes", len(b))
	}
	if _, err := os.Stat(filepath.Join(out, "settings.toml")); err == nil {
		t.Fatal("settings.toml travelled with the card")
	}

	var evil bytes.Buffer
	zw := zip.NewWriter(&evil)
	w, _ := zw.Create("../../escape.txt")
	w.Write([]byte("x"))
	zw.Close()
	if err := unpackCard(evil.Bytes(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("a path outside the card folder was accepted")
	}
}

func TestMatchSeeds(t *testing.T) {
	s1, r1 := matchSeeds("0123456789abcdef0123456789abcdef")
	s2, r2 := matchSeeds("0123456789abcdef0123456789abcdef")
	if s1 != s2 || r1 != r2 || s1 != 0x0123456789abcdef || r1 < 1767225600 || r1 >= 1767225600+365*86400 {
		t.Fatalf("seeds: %x %d", s1, r1)
	}
}

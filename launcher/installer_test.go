package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/recomp-code/bloody-roar4-recomp":    "recomp-code/bloody-roar4-recomp",
		"github.com/recomp-code/bloody-roar4-recomp/":           "recomp-code/bloody-roar4-recomp",
		"https://github.com/recomp-code/dbz-bt4-recomp.git":     "recomp-code/dbz-bt4-recomp",
		"https://github.com/recomp-code/dbz-bt4-recomp#readme":  "recomp-code/dbz-bt4-recomp",
		"recomp-code/dbz-bt4-recomp":                            "recomp-code/dbz-bt4-recomp",
		"  https://www.github.com/recomp-code/dbz-bt4-recomp  ": "recomp-code/dbz-bt4-recomp",
	} {
		if got, err := parseRepo(in); err != nil || got != want {
			t.Errorf("parseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"https://evil.example/recomp-code/x", "https://github.com/onlyowner", "../../etc", ""} {
		if _, err := parseRepo(bad); err == nil {
			t.Errorf("parseRepo(%q) accepted", bad)
		}
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"3.1", "3.0", true}, {"3.10", "3.9", true}, {"3.0", "3.0", false}, {"2.9", "3.0", false}, {"v1.2", "1.1", true}, {"1.0.1", "1.0", true}}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestUnzipRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, evil := range []string{"../escape.txt", "runtime/../../escape.txt"} {
		zp := filepath.Join(dir, "evil.zip")
		os.WriteFile(zp, zipOf(t, map[string]string{evil: "x"}), 0o644)
		if err := unzipTo(zp, filepath.Join(dir, "out")); err == nil {
			t.Errorf("unzipTo accepted %q", evil)
		}
	}
	if fileExists(filepath.Join(dir, "escape.txt")) {
		t.Fatal("a file escaped")
	}
}

// fakeGitHub serves a release of owner/br4 with its manifest and pack.
func fakeGitHub(t *testing.T, version string, pack []byte) *httptest.Server {
	sum := sha256.Sum256(pack)
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/recomp-code/bloody-roar4-recomp/releases/latest":
			json.NewEncoder(w).Encode(ghRelease{Tag: "v" + version, Assets: []ghAsset{
				{Name: manifestName, URL: ts.URL + "/m"}, {Name: "BR4_Windows.zip", URL: ts.URL + "/pack"}}})
		case "/m":
			json.NewEncoder(w).Encode(gameManifest{ID: "br4", Version: version, Packs: map[string]packFile{
				platformName: {File: "BR4_Windows.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(pack))}}})
		case "/pack":
			w.Write(pack)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Setenv("RECOMP_GITHUB_API", ts.URL)
	httpAPI, httpLong = ts.Client(), ts.Client()
	return ts
}

func TestInstallAndUpdateKeepsPlayerFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RECOMP_NO_UPDATE", "1")
	runner := filepath.Base(newGame(root, root, br4Profile).runner)
	pack1 := zipOf(t, map[string]string{"runtime/" + runner: "v1", "gshost/presets/br4.ini": "preset"})
	ts := fakeGitHub(t, "1.0", pack1)
	defer ts.Close()

	a := NewApp(root, false)
	m, err := fetchManifest(br4Profile.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.install(a.games["br4"], m); err != nil {
		t.Fatal(err)
	}
	g := a.games["br4"]
	if !g.installed() || g.packVersion() != "1.0" || g.dir != filepath.Join(root, "games", "br4") {
		t.Fatalf("not installed: %v %q %s", g.installed(), g.packVersion(), g.dir)
	}
	// the player's files: settings, a save, the ISO's files, a changed GS ini
	os.WriteFile(filepath.Join(g.dir, "settings.cfg"), []byte("iso=x\n"), 0o644)
	os.MkdirAll(filepath.Join(g.dir, "runtime", "savedata", "BESLES-51877"), 0o755)
	os.WriteFile(filepath.Join(g.dir, "runtime", "savedata", "BESLES-51877", "save"), []byte("mine"), 0o644)
	os.MkdirAll(filepath.Join(g.dir, "runtime", "data"), 0o755)
	os.WriteFile(filepath.Join(g.dir, "runtime", "data", "SLES_518.77"), []byte("elf"), 0o644)
	os.WriteFile(filepath.Join(g.dir, "gshost", "br4.ini"), []byte("player's"), 0o644)

	pack2 := zipOf(t, map[string]string{"runtime/" + runner: "v2", "gshost/presets/br4.ini": "preset2"})
	ts.Close()
	ts2 := fakeGitHub(t, "1.1", pack2)
	defer ts2.Close()
	m2, err := fetchManifest(br4Profile.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.install(a.games["br4"], m2); err != nil {
		t.Fatal(err)
	}
	g = a.games["br4"]
	read := func(rel string) string { b, _ := os.ReadFile(filepath.Join(g.dir, rel)); return string(b) }
	if g.packVersion() != "1.1" || read(filepath.Join("runtime", runner)) != "v2" {
		t.Fatalf("not updated: %q %q", g.packVersion(), read(filepath.Join("runtime", runner)))
	}
	for rel, want := range map[string]string{"settings.cfg": "iso=x\n", filepath.Join("runtime", "savedata", "BESLES-51877", "save"): "mine",
		filepath.Join("runtime", "data", "SLES_518.77"): "elf", filepath.Join("gshost", "br4.ini"): "player's"} {
		if got := read(rel); got != want {
			t.Errorf("%s = %q after the update, want %q", rel, got, want)
		}
	}
	if fileExists(g.dir+".old") || fileExists(g.dir+".new") {
		t.Error("leftovers of the update")
	}
}

func TestInstallRefusesABadPack(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RECOMP_NO_UPDATE", "1")
	ts := fakeGitHub(t, "1.0", zipOf(t, map[string]string{"runtime/x": "v1"}))
	defer ts.Close()
	a := NewApp(root, false)
	m, err := fetchManifest(br4Profile.Repo)
	if err != nil {
		t.Fatal(err)
	}
	m.Packs[platformName] = packFile{File: m.Packs[platformName].File, SHA256: "00" + m.Packs[platformName].SHA256[2:], Size: m.Packs[platformName].Size}
	if err := a.install(a.games["br4"], m); err == nil {
		t.Fatal("installed a pack whose SHA-256 does not match")
	}
	if fileExists(filepath.Join(root, "games", "br4")) {
		t.Fatal("a bad pack left a game folder")
	}
}

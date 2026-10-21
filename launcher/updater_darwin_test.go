package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeApp: a minimal ad-hoc signed bundle whose executable says what it is.
func fakeApp(t *testing.T, dir, what string, sign bool) string {
	t.Helper()
	app := filepath.Join(dir, "DBZ_BT4.app")
	exe := filepath.Join(app, "Contents", "MacOS", "DBZ_BT4")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleExecutable</key><string>DBZ_BT4</string>
<key>CFBundleIdentifier</key><string>test.recomp.update</string></dict></plist>`), 0o644)
	os.WriteFile(exe, []byte("#!/bin/sh\necho "+what+"\n"), 0o755)
	if sign {
		if out, err := exec.Command("/usr/bin/codesign", "--force", "-s", "-", app).CombinedOutput(); err != nil {
			t.Fatalf("codesign: %v %s", err, out)
		}
	}
	return app
}

func zipApp(t *testing.T, app, zipPath string) {
	t.Helper()
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", app, zipPath).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v %s", err, out)
	}
}

func readExe(t *testing.T, app string) string {
	b, _ := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "DBZ_BT4"))
	return string(b)
}

// [update] the new app takes the installed one's place, the old one waits aside until the next start, a bad
// signature or a translocated app changes nothing
func TestReplaceAppBundle(t *testing.T) {
	tmp := t.TempDir()
	installed := fakeApp(t, filepath.Join(tmp, "Applications"), "old", true)
	exe := filepath.Join(installed, "Contents", "MacOS", "DBZ_BT4")
	zip := filepath.Join(tmp, "DBZ_BT4_Mac.zip")
	zipApp(t, fakeApp(t, filepath.Join(tmp, "build"), "new", true), zip)

	if err := replaceExe(exe, zip); err != nil {
		t.Fatal(err)
	}
	if got := readExe(t, installed); !strings.Contains(got, "new") {
		t.Fatalf("installed app not replaced: %q", got)
	}
	old, _ := filepath.Glob(filepath.Join(tmp, "Applications", ".DBZ_BT4.app.old*"))
	if len(old) != 1 || !strings.Contains(readExe(t, old[0]), "old") {
		t.Fatalf("old app not kept aside: %v", old)
	}
	if fileExists(zip) {
		t.Fatal("the downloaded zip was left behind")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "Applications", ".DBZ_BT4.app.update-*")); len(left) != 0 {
		t.Fatalf("staging left: %v", left)
	}

	// a modified app (signature broken) is refused and the installed one stays
	bad := fakeApp(t, filepath.Join(tmp, "bad"), "bad", true)
	os.WriteFile(filepath.Join(bad, "Contents", "MacOS", "DBZ_BT4"), []byte("#!/bin/sh\necho tampered\n"), 0o755)
	zipApp(t, bad, zip)
	if err := replaceExe(exe, zip); err == nil {
		t.Fatal("an app whose signature does not verify was installed")
	}
	if got := readExe(t, installed); !strings.Contains(got, "new") {
		t.Fatalf("a refused update touched the installed app: %q", got)
	}

	// a translocated app cannot be replaced: the player is asked to move it
	tr := fakeApp(t, filepath.Join(tmp, "AppTranslocation", "X", "d"), "tr", true)
	zipApp(t, fakeApp(t, filepath.Join(tmp, "build2"), "new2", true), zip)
	if err := replaceExe(filepath.Join(tr, "Contents", "MacOS", "DBZ_BT4"), zip); err == nil || !strings.Contains(err.Error(), "Aplicaciones") {
		t.Fatalf("translocated: %v", err)
	}
	if got := readExe(t, tr); !strings.Contains(got, "tr") {
		t.Fatalf("translocated app touched: %q", got)
	}
}

func TestAppBundle(t *testing.T) {
	if app, err := appBundle("/Applications/DBZ_BT4.app/Contents/MacOS/DBZ_BT4"); err != nil || app != "/Applications/DBZ_BT4.app" {
		t.Fatalf("%q %v", app, err)
	}
	if _, err := appBundle("/usr/local/bin/recomp"); err == nil {
		t.Fatal("a launcher outside an app was taken for one")
	}
}

// [update] the whole path: recomp-online's latest release has a newer recomp-launcher.json with a macOS entry; the
// launcher downloads the app zip, checks it and swaps the installed app
func TestLauncherUpdateMac(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("RECOMP_LAUNCHER_DATA", filepath.Join(tmp, "data"))
	installed := fakeApp(t, filepath.Join(tmp, "Applications"), "old", true)
	exe := filepath.Join(installed, "Contents", "MacOS", "DBZ_BT4")
	executable = func() (string, error) { return exe, nil }
	defer func() { executable = os.Executable }()
	zip := filepath.Join(tmp, "DBZ_BT4_Mac.zip")
	zipApp(t, fakeApp(t, filepath.Join(tmp, "build"), "new", true), zip)
	pack, _ := os.ReadFile(zip)
	sum := sha256.Sum256(pack)
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + launcherRepo + "/releases/latest":
			json.NewEncoder(w).Encode(ghRelease{Tag: "v99.0", Assets: []ghAsset{
				{Name: launcherManifest, URL: ts.URL + "/m"}, {Name: "DBZ_BT4_Mac.zip", URL: ts.URL + "/app"}}})
		case "/m":
			json.NewEncoder(w).Encode(launcherManifestT{Version: "99.0", Files: map[string]packFile{
				"Windows": {File: "Recomp.exe", SHA256: strings.Repeat("0", 64), Size: 1},
				platformName: {File: "DBZ_BT4_Mac.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(pack))}}})
		case "/app":
			w.Write(pack)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("RECOMP_GITHUB_API", ts.URL)
	httpAPI, httpLong = ts.Client(), ts.Client()
	pendingUpdate = ""
	defer func() { pendingUpdate = "" }()

	a := &App{root: filepath.Dir(exe), games: map[string]*game{}}
	if err := a.checkLauncherUpdate(); err != nil {
		t.Fatal(err)
	}
	if pendingUpdate != "99.0" || !strings.Contains(readExe(t, installed), "new") {
		t.Fatalf("not updated: pending %q, app %q", pendingUpdate, readExe(t, installed))
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "data", "update", "*")); len(left) != 0 {
		t.Fatalf("download left: %v", left)
	}
	cleanupOldExe()
	if old, _ := filepath.Glob(filepath.Join(tmp, "Applications", ".DBZ_BT4.app.old*")); len(old) != 0 {
		t.Fatalf("old app not cleaned at the next start: %v", old)
	}
}

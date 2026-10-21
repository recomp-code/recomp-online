package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// [update] a release built on another system keeps this system's previous file with its own "version": the launcher
// must not take it for the release's version (it would download the same build again at every check)
func TestLauncherUpdateFileVersion(t *testing.T) {
	var downloads atomic.Int32
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + launcherRepo + "/releases/latest":
			json.NewEncoder(w).Encode(ghRelease{Tag: "v99.0", Assets: []ghAsset{
				{Name: launcherManifest, URL: ts.URL + "/m"}, {Name: "file", URL: ts.URL + "/file"}}})
		case "/m":
			json.NewEncoder(w).Encode(launcherManifestT{Version: "99.0", Files: map[string]packFile{
				platformName: {File: "file", SHA256: "00", Size: 1, Version: launcherVersion}}})
		case "/file":
			downloads.Add(1)
			w.Write([]byte("x"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("RECOMP_GITHUB_API", ts.URL)
	httpAPI, httpLong = ts.Client(), ts.Client()
	pendingUpdate = ""
	a := &App{root: t.TempDir(), games: map[string]*game{}}
	if err := a.checkLauncherUpdate(); err != nil {
		t.Fatal(err)
	}
	if pendingUpdate != "" || downloads.Load() != 0 {
		t.Fatalf("took this system's %s file for 99.0: pending %q, %d downloads", launcherVersion, pendingUpdate, downloads.Load())
	}
}

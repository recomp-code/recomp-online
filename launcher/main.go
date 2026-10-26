//go:build !android

// Recomp launcher (Go + Wails / WebView2): the game, its settings and the online lobby in one window.
//
// Build (no Wails CLI needed):
//
//	go build -tags desktop,production -trimpath -ldflags "-H windowsgui -s -w -X main.defaultServer=https://online.tudominio.com" -o Recomp.exe
//
// macOS (cgo; the app bundle around it is made by dbz-bt4-recomp's macos/make_dist.sh):
//
//	CGO_LDFLAGS="-framework UniformTypeIdentifiers" MACOSX_DEPLOYMENT_TARGET=14.0 go build -tags desktop,production -trimpath -ldflags "-s -w -X main.defaultServer=https://online.tudominio.com" -o DBZ_BT4
package main

import (
	"embed"
	"flag"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	direct := flag.Bool("jugar", false, "empezar el juego directamente con los ajustes guardados")
	iso := flag.String("iso", "", "preparar esta ISO (como Seleccionar ISO)")
	flag.Parse()
	exe, _ := os.Executable()
	root := filepath.Dir(exe)
	if r := os.Getenv("RECOMP_LAUNCHER_ROOT"); r != "" { // tests: run against another install
		root = r
	}
	app := NewApp(root, *direct)
	if *iso != "" {
		_ = app.game.prepare(*iso)
	}
	err := wails.Run(&options.App{
		Title:                    "Recomp",
		Width:                    1180,
		Height:                   720,
		MinWidth:                 980,
		MinHeight:                620,
		Frameless:                true,
		StartHidden:              *direct && app.game.ready(),
		BackgroundColour:         &options.RGBA{R: 14, G: 18, B: 26, A: 255},
		AssetServer:              &assetserver.Options{Assets: assets},
		OnStartup:                app.startup,
		OnShutdown:               app.shutdown,
		Bind:                     []interface{}{app},
		DragAndDrop:              &options.DragAndDrop{EnableFileDrop: true}, // app.js stops the browser from opening dropped files
		EnableDefaultContextMenu: false,
		Menu:                     appMenu(), // macOS: the app and Edit menus (Cmd+Q, Cmd+C / Cmd+V in the fields)
		Windows: &windows.Options{
			Theme:               windows.Dark,
			DisableWindowIcon:   false,
			WebviewUserDataPath: filepath.Join(os.Getenv("LOCALAPPDATA"), "RecompLauncher", "webview"),
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
